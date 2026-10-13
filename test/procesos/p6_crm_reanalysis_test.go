//go:build integracion

package procesos

import (
	"encoding/json"
	"net/http"
	"testing"
)

// La tercera puerta del `intake.push`: el re-análisis que pide la dueña (POST …/reanalyze). El pipeline
// normal NO empuja su borrador; el del re-análisis SÍ empuja la revisión que escribe (D-044.19: toda
// revisión posterior al cierre vuelve a salir al CRM). P8 recorre el re-análisis en una empresa sin
// puente; aquí se afirma solo lo que llega al CRM.

// p6ReanalysisRevision es el número de la revisión que escribe el re-análisis de la solicitud que se
// aprueba: la 1 es el borrador y las 2, 3 y 4 las correcciones de pushGateClosed.
const p6ReanalysisRevision = 5

// pushReanalysis: con el puente activo, el re-análisis de una solicitud por aprobar escribe una
// revisión `interpreted` nueva y ESA revisión llega al CRM, firmada y válida, con su número real y el
// estado real. El cliente no recibe nada (INV-1).
func (w *p6World) pushReanalysis(t *testing.T) {
	path := "/api/v1/intakes/" + w.approved + "/reanalyze"
	r := w.call(t, w.admin, "", http.MethodPost, path, map[string]any{})
	ack := p6Fields(t, r)
	if r.Codigo != http.StatusOK || ack["intake_id"] != w.approved || ack["job_id"] == "" {
		t.Fatalf("POST reanalyze: HTTP %d %s; quería 200 con el acuse del job", r.Codigo, recortar(r.Cuerpo))
	}
	// Con el primero pendiente, un segundo no entra: no habrá dos empujes.
	p6WantError(t, "segundo reanalyze con el primero en curso", w.call(t, w.admin, "", http.MethodPost, path, map[string]any{}),
		http.StatusUnprocessableEntity, "reanalysis_in_progress")

	got := w.crm.Wait(t, p6Timeout, "la revisión del re-análisis", 1, p6ForIntake(w.approved))
	d := got[0]
	if !d.SignatureOK || !d.InWindow || d.SchemaErr != nil || d.Status != http.StatusOK {
		t.Errorf("la entrega del re-análisis: firma=%v ventana=%v schema=%v respuesta=%d", d.SignatureOK, d.InWindow, d.SchemaErr, d.Status)
	}
	var doc p6PushDoc
	if err := json.Unmarshal(d.Body, &doc); err != nil {
		t.Fatalf("el cuerpo recibido no se puede leer: %v", err)
	}
	if doc.RevisionNo != p6ReanalysisRevision || doc.LifecycleStatus != "pending_approval" {
		t.Errorf("el re-análisis llegó con revision_no %d y lifecycle_status %q; quería %d y pending_approval",
			doc.RevisionNo, doc.LifecycleStatus, p6ReanalysisRevision)
	}
	// Las líneas del documento son las GUARDADAS de la solicitud (las de la última corrección): el
	// re-análisis escribe las suyas en la revisión, no en intake_items.
	w.checkPushDoc(t, "el re-análisis", d, doc, w.approved, p6DeadLines())
	if kind := p9Scalar(t, w.sc.DB, `SELECT kind || '|' || created_by FROM public.intake_revisions
		WHERE intake_id = $1::uuid AND revision_no = $2`, w.approved, p6ReanalysisRevision); kind != "interpreted|owner" {
		t.Errorf("la revisión %d del re-análisis es %q, quería interpreted|owner", p6ReanalysisRevision, kind)
	}
	want := "delivered|0|sin error|sin claim|payload vacío|intake.push|" + w.sc.Tenant
	p6WaitScalar(t, w.sc, want, "la fila de la entrega del re-análisis", p6OutboxRow, d.ID)
	w.sc.expectNoPendingText(t, "tras el re-análisis")
}

// p6ApprovedReanalysisRevision es el número de la revisión que escribe el re-análisis de la solicitud
// YA aprobada: la 6 es la corrección y la 7 la aprobación.
const p6ApprovedReanalysisRevision = 8

// reanalyzeApproved: 🔴 una solicitud ya APROBADA (`confirmed`) también se re-analiza —el viejo contesta
// 200, no un rechazo— y esa revisión sale al CRM como las demás. Se espera a que llegue para que el
// resto del proceso parta de un estado quieto.
func (w *p6World) reanalyzeApproved(t *testing.T) {
	t.Helper()
	r := w.call(t, w.admin, "", http.MethodPost, "/api/v1/intakes/"+w.approved+"/reanalyze", map[string]any{})
	if ack := p6Fields(t, r); r.Codigo != http.StatusOK || ack["revision_no"] != float64(p6ApprovedReanalysisRevision) {
		t.Fatalf("POST reanalyze de la solicitud aprobada: HTTP %d %s; quería 200 con revision_no %d",
			r.Codigo, recortar(r.Cuerpo), p6ApprovedReanalysisRevision)
	}
	d := w.crm.Wait(t, p6Timeout, "la revisión del re-análisis de la aprobada", 4, p6ForIntake(w.approved))[3]
	status := p9Scalar(t, w.sc.DB, `SELECT status FROM public.intakes WHERE id = $1::uuid`, w.approved)
	if !d.SignatureOK || d.SchemaErr != nil || d.Number("revision_no") != p6ApprovedReanalysisRevision ||
		d.Text("lifecycle_status") != "confirmed" || status != "confirmed" {
		t.Errorf("el re-análisis de la aprobada llegó con firma=%v schema=%v revision_no=%v lifecycle_status=%q (solicitud en %q); quería la revisión %d en confirmed",
			d.SignatureOK, d.SchemaErr, d.Number("revision_no"), d.Text("lifecycle_status"), status, p6ApprovedReanalysisRevision)
	}
	want := "delivered|0|sin error|sin claim|payload vacío|intake.push|" + w.sc.Tenant
	p6WaitScalar(t, w.sc, want, "la fila de la entrega del re-análisis de la aprobada", p6OutboxRow, d.ID)
	w.sc.expectNoPendingText(t, "tras el re-análisis de la aprobada")
}
