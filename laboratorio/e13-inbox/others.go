//go:build laboratorio

package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/google/uuid"

	sharedjwt "github.com/EduGoGroup/wapp-shared/auth/jwt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"

	"github.com/EduGoGroup/wapp-cloud-platform/laboratorio/common"
)

// others.go — las otras dos solicitudes (una que se rechaza y un carrito que se descarta), el
// descarte por lotes, el export y el rastro final.

// otherIntakes fabrica a Bea (otro borrador del pipeline, que se rechazará) y siembra a Carla (un
// carrito `open`, como lo deja el proyector del carrito de E08).
func (l *lab) otherIntakes() (bea, car string, err error) {
	l.n.Step("Dos solicitudes más: un borrador que se rechaza y un carrito abandonado")
	beaContact, err := l.box.NewCustomer(l.ctx, phoneBea, "Bea")
	if err != nil {
		l.n.Fail(err, "contacto")
		return "", "", err
	}
	d, err := l.box.DraftFromMessages(l.ctx, "lab-session-e13-bea", beaContact, common.AmberBurst(), nil)
	if err != nil || d.IntakeID == "" {
		l.n.Fail(err, "el borrador de Bea")
		return "", "", errUnexpected
	}
	l.n.OK("Bea pide lo mismo que Ana: borrador %s en pending_approval (pipeline real).", short(d.IntakeID))

	carContact, err := l.box.NewCustomer(l.ctx, phoneCar, "Carla")
	if err != nil {
		l.n.Fail(err, "contacto")
		return "", "", err
	}
	car, event := uuid.NewString(), uuid.NewString()
	now := l.box.Clock.Now()
	l.box.Intakes.Add(common.LabTenant,
		intakes.Intake{ID: car, ContactID: carContact, SessionID: "lab-session-e13-carla", Status: intakes.StatusOpen, Total: 980, CreatedAt: now, UpdatedAt: now},
		intakes.Item{SKU: "TEQ-30", Label: "Tequeños congelados", Qty: 2, UnitPrice: 490, AddedAt: now})
	l.box.Intakes.BindEvent(car, event)
	l.box.Intakes.SetEvent(event, "open")
	l.n.Note("Carla tiene un carrito %s en open (2 paquetes de tequeños) con su evento %s VIVO.", short(car), short(event))
	l.n.Note("  Se SIEMBRA directo (Add + BindEvent + SetEvent): el carrito es la pieza de E08, no la de aquí.")
	l.carEvent = event
	return d.IntakeID, car, nil
}

// discard enseña el descarte por lotes: quién se descarta, quién no y por qué.
func (l *lab) discard(ana, bea, car string) {
	l.n.Step("Descartar: Discard por lotes (abandoned, sin aviso)")
	l.n.Note("El descarte NO usa la máquina de estados (CanTransition): tiene su conjunto propio, CanDiscard = {open, expired}.")
	ghost := "00000000-0000-4000-8000-000000000000"
	res, err := l.box.Service.Discard(l.ctx, common.LabTenant, []string{ana, bea, car, ghost, ana})
	if err != nil {
		l.explain(err, "Discard")
		return
	}
	l.showDiscard(res)
	l.n.Note("Ana (confirmed) y Bea (pending_approval) son not_open: un presupuesto no se «descarta», se rechaza o se cancela.")
	l.n.Note("Carla es live_event: su conversación sigue abierta. El id repetido (Ana) se contesta UNA vez.")

	l.n.Note("Bea se RECHAZA por la máquina de estados (SetStatus rejected, con aviso al cliente):")
	if _, err := l.box.Service.SetStatus(l.ctx, common.LabTenant, bea, intakes.StatusRejected, intakes.NoticeToClient); err != nil {
		l.explain(err, "SetStatus(rejected)")
	}
	l.sends = l.showSends(l.sends, "")

	l.n.Note("El evento de Carla caduca (conversation_events → cancelled) y la dueña repite el lote:")
	l.box.Intakes.SetEvent(l.carEvent, "cancelled")
	res, _ = l.box.Service.Discard(l.ctx, common.LabTenant, []string{car})
	l.showDiscard(res)
	res, _ = l.box.Service.Discard(l.ctx, common.LabTenant, []string{car})
	l.n.Note("Otra vez el mismo lote (idempotente):")
	l.showDiscard(res)
	l.showIntake(l.mustGet(car))
	l.sends = l.showSends(l.sends, "el descarte es higiene interna: D-041.18, «no borra y no notifica»")
}

func (l *lab) showDiscard(res intakes.DiscardResult) {
	for _, id := range res.Discarded {
		l.n.OK("descartada %s", short(id))
	}
	for _, s := range res.Skipped {
		l.n.Note("  saltada %s · %s", short(s.IntakeID), s.Reason)
	}
}

// export pide el CSV por la cara HTTP REAL: MountIntakeReports, con su JWT, su permiso y su gate.
func (l *lab) export() error {
	l.n.Step("Exportar: GET /api/v1/intakes/export por la cara HTTP real")
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return err
	}
	jwt := sharedjwt.NewJWTManager(hex.EncodeToString(secret), "wapp-lab-e13")
	cara := apipublica.Nueva()
	apipublica.MountIntakeReports(cara, apipublica.Common{MW: httpapi.NewMiddleware(jwt, nil)},
		apipublica.IntakeReportsDeps{Intakes: l.box.Service, Entitlements: l.box.Rights, Now: l.box.Clock.Now})
	token, _, err := jwt.GenerateToken("lab-owner", common.LabTenant, []string{"operator"},
		sharedjwt.Grants{Allow: []string{"intakes.read"}}, time.Hour)
	if err != nil {
		l.n.Fail(err, "firmando el token")
		return err
	}
	l.n.Note("Un Context Token HS256 con secreto ALEATORIO del proceso (no es una credencial) y el permiso intakes.read.")
	get := func(target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		cara.ServeHTTP(rec, req)
		return rec
	}

	rec := get("/api/v1/intakes/export?format=csv")
	l.n.Fail(nil, "sin la feature intakes_export → HTTP %d %s", rec.Code, strings.TrimSpace(rec.Body.String()))
	l.box.Rights.Enable(common.LabTenant, entitlements.FeatureIntakesExport)
	l.n.Note("Se enciende intakes_export (la venta aparte de sacar la bandeja a un fichero) y se repite:")
	rec = get("/api/v1/intakes/export?format=csv")
	l.n.OK("HTTP %d · %s · %s", rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Content-Disposition"))
	l.n.Show("el CSV (BOM UTF-8 delante; una fila por LÍNEA, la cabecera repetida)", strings.TrimPrefix(rec.Body.String(), "\xef\xbb\xbf"))
	rec = get("/api/v1/intakes/export?format=csv&status=confirmed")
	l.n.Show(fmt.Sprintf("con ?status=confirmed (HTTP %d)", rec.Code), strings.TrimPrefix(rec.Body.String(), "\xef\xbb\xbf"))
	rec = get("/api/v1/intakes/summary.json")
	l.n.Show(fmt.Sprintf("GET /api/v1/intakes/summary.json (HTTP %d)", rec.Code), rec.Body.String())
	l.n.Note("⚠️ totals.revenue SUMA TODOS los estados: el carrito abandonado de Carla cuenta igual que el pedido confirmado")
	l.n.Note("  de Ana. Es una decisión escrita (intakes/summary.go: «quien lee decide… acotando con ?status=»), no un descuido.")
	l.n.Note("contact_ref sale OPACO (ADR-0010): ni teléfono ni JID. Y exportar NO toca los recordatorios perezosos.")
	return nil
}

// trail resume el rastro de Ana y lo que el sistema dejó escrito.
func (l *lab) trail(ana string) {
	l.n.Step("El rastro: revisiones, telemetría, mensajes y auditoría")
	d := l.mustGet(ana)
	kinds := make([]string, 0, len(d.Revisions))
	for _, r := range d.Revisions {
		kinds = append(kinds, fmt.Sprintf("%d %s (%s)", r.RevisionNo, r.Kind, r.CreatedBy))
	}
	l.n.Note("intake_revisions de Ana: %s.", strings.Join(kinds, " → "))
	l.n.Note("  needs_info y la vuelta a pending_approval NO dejaron revisión: una revisión retrata líneas y total, no estados.")
	for _, ev := range l.box.Stages.Flow.FlowEvents() {
		l.n.Note("flow_events · %q · %v", ev.Name, ev.Payload)
	}
	l.n.Note("El cliente recibió %d mensaje(s) en total (runtimehelpertest.Sender).", len(l.box.Sender.Sends()))
	l.n.Note("🔍 Auditoría: NO hay. La escribe httpapi.AuditMiddleware en las rutas de ESCRITURA de la cara HTTP (protect),")
	l.n.Note("  y este escalón llama al servicio directamente. Solo el export pasó por HTTP, y es una lectura (protectRead: sin auditoría).")
}
