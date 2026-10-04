//go:build integracion

package procesos

import (
	"database/sql"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

// La VUELTA del puente: POST /api/v1/integrations/callback, sin JWT, autenticado por la firma HMAC del
// cuerpo crudo. Re-expresa internal/publicapi/crmcallback_e2e_integration_test.go (la cadena entera:
// firma → gate → schema → UPDATE → aviso al cliente por el Edge) e internal/intakes/crm_integration_test.go
// (los dos instantes que se mueven por separado, el no-op, el aislamiento por empresa).

// Los avisos que el cliente recibe por cada estado del CRM (internal/intakes/notifier.go).
const (
	p6TextPaid      = "Recibimos tu pago. ¡Gracias! Ya estamos con tu pedido."
	p6TextPreparing = "Tu pedido ya se está preparando. Te avisamos apenas salga."
	p6TextDelivered = "Tu pedido fue entregado. ¡Que lo disfrutes! Cualquier cosa, respóndenos por aquí."
	p6TextRejected  = "No podemos tomar tu pedido en este momento. Si quieres, respóndenos y lo vemos."

	// p6Unauthenticated es el cuerpo ÚNICO de todo 401 del callback: no dice qué falló.
	p6Unauthenticated = `{"error":"no autenticado"}`
	// p6GateClosed es el motivo del 403 con el puente apagado.
	p6GateClosed = "el puente CRM no está activo para este tenant"
	// p6IntakeNotFound es el motivo del 404, igual para la solicitud ajena y la inexistente.
	p6IntakeNotFound = "solicitud no encontrada"
)

// p6Reflection son las columnas de una solicitud que el callback puede tocar —el reflejo del CRM
// (migración 0048) y el instante de negocio— más las dos que NO puede tocar: el estado del dueño y el
// total.
type p6Reflection struct {
	Status    string
	Total     float64
	CRMStatus sql.NullString
	Ref       sql.NullString
	Synced    sql.NullTime
	Updated   time.Time
}

// reflection lee de Postgres el reflejo de la solicitud aprobada. Falla (t.Fatalf) si no se puede.
func (w *p6World) reflection(t *testing.T) p6Reflection {
	t.Helper()
	var r p6Reflection
	if err := w.sc.DB.QueryRowContext(t.Context(), `SELECT status, total::float8, crm_status, crm_external_ref, crm_synced_at, updated_at
		FROM public.intakes WHERE id = $1::uuid`, w.approved).Scan(&r.Status, &r.Total, &r.CRMStatus, &r.Ref, &r.Synced, &r.Updated); err != nil {
		t.Fatalf("leer el reflejo de la solicitud: %v", err)
	}
	return r
}

// statusBody es un `intake.status` de la solicitud aprobada, ocurrido ahora.
func (w *p6World) statusBody(t *testing.T, status, ref string) []byte {
	t.Helper()
	return crmFakeStatusBody(t, w.approved, status, ref, time.Now())
}

// signed firma body como el puente de la empresa del proceso, ahora.
func (w *p6World) signed(body []byte) crmFakeCallback {
	return crmFakeSignedCallback(w.secret, w.sc.Tenant, time.Now(), body)
}

// noop es un callback bien formado que NO cambia nada: repite el estado ya reflejado, sin referencia.
// Es el cuerpo de las tablas adversarias: si la puerta lo acepta no avisa a nadie, y si lo rechaza la
// marca de la solicitud no puede moverse.
func (w *p6World) noop(t *testing.T) []byte {
	t.Helper()
	return w.statusBody(t, w.reflection(t).CRMStatus.String, "")
}

// p6WantApplied exige el 200 del callback con su cuerpo: la solicitud, el estado y si cambió algo.
func p6WantApplied(t *testing.T, what string, r respuesta, intakeID, status string, changed bool) {
	t.Helper()
	want := map[string]any{"intake_id": intakeID, "crm_status": status, "changed": changed}
	if got := p6Fields(t, r); r.Codigo != http.StatusOK || !reflect.DeepEqual(got, want) {
		t.Errorf("%s: HTTP %d %v, quería 200 %v", what, r.Codigo, got, want)
	}
}

// p6WantUnauthenticated exige el 401 del callback con su cuerpo ÚNICO: toda forma de fallar la
// autenticación contesta lo mismo, sin detalle.
func p6WantUnauthenticated(t *testing.T, what string, r respuesta) {
	t.Helper()
	if r.Codigo != http.StatusUnauthorized || strings.TrimSpace(string(r.Cuerpo)) != p6Unauthenticated {
		t.Errorf("%s: HTTP %d %s, quería 401 %s", what, r.Codigo, recortar(r.Cuerpo), p6Unauthenticated)
	}
}

// callbackReflects: el primer callback firmado refleja el estado del CRM en las columnas propias de la
// solicitud, NO pisa el estado del dueño, mueve los dos instantes y avisa al cliente por su sesión. Ni
// encola nada ni deja el teléfono en claro.
func (w *p6World) callbackReflects(t *testing.T) {
	before := w.reflection(t)
	if before.Status != "confirmed" || before.CRMStatus.Valid || before.Ref.Valid || before.Synced.Valid {
		t.Fatalf("la solicitud aprobada llega al callback con %+v; quería confirmed y sin reflejo", before)
	}
	outbox := p9Scalar(t, w.sc.DB, p6OutboxMark+` WHERE status = 'delivered'`)
	start := time.Now()

	const ref = "OC-2026-004512"
	r := w.post(t, w.signed(w.statusBody(t, "paid", ref)))
	p6WantApplied(t, "callback paid", r, w.approved, "paid", true)
	w.sc.expectText(t, p6PnApproved, p6TextPaid)

	after := w.reflection(t)
	if after.CRMStatus.String != "paid" || after.Ref.String != ref || !after.Synced.Valid || after.Synced.Time.Before(start.Add(-time.Second)) {
		t.Errorf("el reflejo quedó en %+v; quería crm_status paid, la referencia %q y crm_synced_at de ahora", after, ref)
	}
	if after.Status != "confirmed" || after.Total != before.Total {
		t.Errorf("el callback tocó lo del dueño: estado %q y total %v (antes confirmed y %v)", after.Status, after.Total, before.Total)
	}
	if !after.Updated.After(before.Updated) {
		t.Errorf("un cambio real debe mover updated_at: %s → %s", before.Updated, after.Updated)
	}

	// La bandeja de la dueña sigue diciendo `confirmed`: el reflejo no es una transición del ciclo de vida.
	detail := p6Fields(t, w.call(t, w.admin, "", http.MethodGet, "/api/v1/intakes/"+w.approved, nil))
	if detail["status"] != "confirmed" {
		t.Errorf("GET intakes/{id} tras el callback: estado %v, quería confirmed", detail["status"])
	}
	// La vuelta no produce ida, y el número del cliente no queda escrito en lo que tocó.
	p6WantMark(t, w.sc, "las entregas hechas tras el callback", outbox, p6OutboxMark+` WHERE status = 'delivered'`)
	if got := p9Scalar(t, w.sc.DB, `SELECT (SELECT count(*) FROM public.intakes i WHERE position($1 in to_jsonb(i)::text) > 0)::text || '|' ||
		(SELECT count(*) FROM public.flow_events e WHERE position($1 in e.payload::text) > 0)::text`, p6PnApproved); got != "0|0" {
		t.Errorf("FUGA: el teléfono del cliente aparece en (intakes | flow_events) = %s filas", got)
	}
}

// callbackIdempotent: el mismo estado otra vez es un no-op de negocio —`changed:false`, updated_at
// quieto, ningún aviso— pero crm_synced_at SÍ avanza (un puente que repite está vivo). Un estado
// distinto sí cambia, y una referencia vacía conserva la que había.
func (w *p6World) callbackIdempotent(t *testing.T) {
	prev := w.reflection(t)
	for i := range 3 {
		r := w.post(t, w.signed(w.statusBody(t, "paid", prev.Ref.String)))
		p6WantApplied(t, "callback paid repetido", r, w.approved, "paid", false)
		w.sc.expectNoPendingText(t, "tras un callback repetido")
		now := w.reflection(t)
		if !now.Updated.Equal(prev.Updated) || !now.Synced.Time.After(prev.Synced.Time) || now.CRMStatus != prev.CRMStatus || now.Ref != prev.Ref {
			t.Errorf("repetición %d: %+v → %+v; quería updated_at quieto y crm_synced_at avanzando", i+1, prev, now)
		}
		prev = now
	}

	steps := []struct {
		status, ref, wantRef, text string
	}{
		// Sin referencia: «no me pronuncio», no «bórrala».
		{"preparing", "", "OC-2026-004512", p6TextPreparing},
		// La referencia es opaca: se guarda recortada y tal cual, adversarios incluidos.
		{"delivered", "\u00a0OC@@١٢٣\u00a0", "OC@@١٢٣", p6TextDelivered},
		// `rejected` del CRM no es el `rejected` del dueño: la solicitud sigue `confirmed`.
		{"rejected", "", "OC@@١٢٣", p6TextRejected},
		// El mismo estado con OTRA referencia también es un cambio, y el cliente recibe el aviso otra vez.
		{"rejected", "OC-3", "OC-3", p6TextRejected},
	}
	for _, step := range steps {
		r := w.post(t, w.signed(w.statusBody(t, step.status, step.ref)))
		p6WantApplied(t, "callback "+step.status, r, w.approved, step.status, true)
		w.sc.expectText(t, p6PnApproved, step.text)
		now := w.reflection(t)
		if now.CRMStatus.String != step.status || now.Ref.String != step.wantRef || !now.Updated.After(prev.Updated) || now.Status != "confirmed" {
			t.Errorf("tras %s: %+v; quería ese estado, la referencia %q, updated_at movido y la solicitud en confirmed", step.status, now, step.wantRef)
		}
		prev = now
	}
	w.sc.expectNoPendingText(t, "tras la secuencia de estados")
}

// callbackWindow: la ventana anti-replay es de ±300 s sobre X-Wapp-Timestamp. Los dos bordes exactos
// que no dependen de cuánto tarde la petición van al segundo —301 s atrás se rechaza siempre y 299 s
// adelante se acepta siempre—; los otros dos llevan margen, porque el servidor mide contra SU reloj al
// recibir: un timestamp 301 s adelante entra en ventana si la petición tarda en llegar, y uno 299 s
// atrás sale de ella por lo mismo.
func (w *p6World) callbackWindow(t *testing.T) {
	body := w.noop(t)
	status := w.reflection(t).CRMStatus.String
	for _, c := range []struct {
		name   string
		offset time.Duration
		ok     bool
	}{
		{"301 s atrás (fuera, exacto)", -301 * time.Second, false},
		{"310 s adelante (fuera)", 310 * time.Second, false},
		{"una hora atrás", -time.Hour, false},
		{"299 s adelante (dentro, exacto)", 299 * time.Second, true},
		{"290 s atrás (dentro)", -290 * time.Second, true},
	} {
		mark := p9Scalar(t, w.sc.DB, p6IntakeMark, w.approved)
		r := w.post(t, crmFakeSignedCallback(w.secret, w.sc.Tenant, time.Now().Add(c.offset), body))
		if c.ok {
			p6WantApplied(t, "callback con el timestamp "+c.name, r, w.approved, status, false)
			continue
		}
		p6WantUnauthenticated(t, "callback con el timestamp "+c.name, r)
		p6WantMark(t, w.sc, "tras el callback con el timestamp "+c.name, mark, p6IntakeMark, w.approved)
	}
	w.sc.expectNoPendingText(t, "tras la tabla de la ventana")
}

// callbackIsolation: el tenant del callback es el de la cabecera AUTENTICADA, y acota el reflejo. El
// puente de otra empresa, con su secreto bueno, no encuentra la solicitud de esta (404, el mismo que
// una inexistente: no hay oráculo de ids) ni la toca; y el secreto de una empresa no vale con la
// cabecera de otra, tenga o no tenga puente.
func (w *p6World) callbackIsolation(t *testing.T) {
	mark := p9Scalar(t, w.sc.DB, p6IntakeMark, w.approved)
	body := w.statusBody(t, "paid", "HACK")

	foreign := w.post(t, crmFakeSignedCallback(w.otherSecret, w.other.tenant, time.Now(), body))
	p6WantError(t, "callback del puente de otra empresa sobre esta solicitud", foreign, http.StatusNotFound, p6IntakeNotFound)
	unknown := w.post(t, w.signed(crmFakeStatusBody(t, uuidAleatorio(t), "paid", "HACK", time.Now())))
	if unknown.Codigo != foreign.Codigo || string(unknown.Cuerpo) != string(foreign.Cuerpo) {
		t.Errorf("la solicitud ajena contesta %d %s y la inexistente %d %s: tienen que ser indistinguibles",
			foreign.Codigo, recortar(foreign.Cuerpo), unknown.Codigo, recortar(unknown.Cuerpo))
	}
	for name, tenant := range map[string]string{"otra empresa con puente": w.other.tenant, "una empresa sin puente": w.noBridge.tenant} {
		r := w.post(t, crmFakeSignedCallback(w.secret, tenant, time.Now(), body))
		p6WantUnauthenticated(t, "callback con el secreto de esta empresa y la cabecera de "+name, r)
	}
	p6WantMark(t, w.sc, "tras los callbacks de otras empresas", mark, p6IntakeMark, w.approved)
	w.sc.expectNoPendingText(t, "tras los callbacks de otras empresas")
}

// callbackGate: con el puente APAGADO (la fila y el secreto siguen), el callback bien firmado contesta
// 403 y no refleja; uno mal firmado sigue siendo 401 (la firma va antes que el gate: un desconocido no
// averigua quién tiene puente). Al encenderlo, vuelve a entrar. Apagar y encender no toca el secreto.
func (w *p6World) callbackGate(t *testing.T) {
	envelope := p9Scalar(t, w.sc.DB, p6EnvelopeMark, w.sc.Tenant)
	r := w.call(t, w.admin, p6ActWrite, http.MethodPut, p6PathIntegration, p6Bridge(w.endpoint(), "", false))
	p6WantBridge(t, "PUT apagando el puente", r, w.endpoint(), w.secret, false)

	mark := p9Scalar(t, w.sc.DB, p6IntakeMark, w.approved)
	p6WantError(t, "callback con el puente apagado", w.post(t, w.signed(w.statusBody(t, "paid", "OFF"))), http.StatusForbidden, p6GateClosed)
	bad := w.signed(w.statusBody(t, "paid", "OFF"))
	bad.Signature = crmFakeSignaturePrefix + crmFakeSign(w.otherSecret, time.Now().Unix(), bad.Body)
	p6WantUnauthenticated(t, "callback mal firmado con el puente apagado", w.post(t, bad))
	p6WantMark(t, w.sc, "tras los callbacks con el puente apagado", mark, p6IntakeMark, w.approved)

	r = w.call(t, w.admin, p6ActWrite, http.MethodPut, p6PathIntegration, p6Bridge(w.endpoint(), "", true))
	p6WantBridge(t, "PUT encendiendo el puente", r, w.endpoint(), w.secret, true)
	p6WantMark(t, w.sc, "el sobre del secreto tras apagar y encender", envelope, p6EnvelopeMark, w.sc.Tenant)
	status := w.reflection(t).CRMStatus.String
	p6WantApplied(t, "callback con el puente encendido otra vez", w.post(t, w.signed(w.noop(t))), w.approved, status, false)
	w.sc.expectNoPendingText(t, "tras el gate del callback")
}
