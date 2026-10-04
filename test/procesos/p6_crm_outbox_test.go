//go:build integracion

package procesos

import (
	"maps"
	"net/http"
	"reflect"
	"strconv"
	"testing"
)

// El final de una entrega que el CRM rechaza siempre, y el resumen de la cola. Re-expresa
// internal/integrations/outbox_stats_integration_test.go (contadores por estado, aislamiento por
// empresa, ceros sin filas) y la mitad «dead conserva el payload» de payload_purge_integration_test.go.

// pushExhausted: el reintento llega cuando toca (el backoff del primer fallo), el CRM vuelve a
// contestar 500 y, con el tope en 2 intentos, la entrega queda `dead`: terminal, con sus dos intentos
// contados, el motivo y —al revés que una entregada— su payload, que es lo único que dice qué no llegó.
func (w *p6World) pushExhausted(t *testing.T) {
	got := w.crm.Wait(t, p6Timeout, "el reintento de la entrega que el CRM rechaza", 2, p6ForIntake(w.dead))
	first, second := got[0], got[1]
	want := "dead|2|" + p6BridgeFailure + "|sin claim|con payload|intake.push|" + w.sc.Tenant
	p6WaitScalar(t, w.sc, want, "la fila de la entrega agotada", p6OutboxRow, first.ID)
	if n := len(w.crm.Select(p6ForIntake(w.dead))); n != 2 {
		t.Errorf("el CRM falso recibió %d intentos de la entrega agotada, quería 2 (WAPP_WEBHOOK_MAX_ATTEMPTS)", n)
	}

	p6CheckRetry(t, first, second)
	if got := p9Scalar(t, w.sc.DB, `SELECT payload->>'intake_id' FROM public.webhook_outbox WHERE id = $1::bigint`, first.ID); got != w.dead {
		t.Errorf("la entrega agotada guarda en su payload el intake_id %q, quería %s", got, w.dead)
	}

	// El segundo fallo cuenta como `dead`, no como otro `failed`; y es el único ERROR del worker.
	w.metrics["dead"] = 1
	p3WaitCounter(t, w.sc.S, p6MetricDeliveries, p6MetricLabel, "dead", 1)
	p3WaitCounter(t, w.sc.S, p6MetricDeliveries, p6MetricLabel, "failed", 1)
	w.errors[p6MsgDead] = 1
	line := edgeEsperarLinea(t, w.sc.S, p6MsgDead, "reason", p6BridgeFailure)
	if line["level"] != "ERROR" || line["tenant"] != w.sc.Tenant || line["attempts"] != 2.0 || strconv.FormatFloat(p6Float(line["outbox_id"]), 'f', -1, 64) != first.ID {
		t.Errorf("la línea de la entrega agotada: %v; quería ERROR con la empresa, attempts=2 y outbox_id=%s", line, first.ID)
	}

	// Que la entrega muera no toca la solicitud: sigue por aprobar, con su revisión.
	if got := p9Scalar(t, w.sc.DB, `SELECT status || '|' || (SELECT count(*) FROM public.intake_revisions r WHERE r.intake_id = i.id)::text
		FROM public.intakes i WHERE id = $1::uuid`, w.dead); got != "pending_approval|2" {
		t.Errorf("la solicitud de la entrega agotada quedó en %q, quería pending_approval con dos revisiones", got)
	}
}

// p6CheckRetry compara los dos intentos de una entrega que el CRM rechazó.
func p6CheckRetry(t *testing.T, first, second crmFakeDelivery) {
	t.Helper()
	// Es la MISMA entrega las dos veces: mismo X-Wapp-Delivery y mismo documento (el `timestamp` del
	// cuerpo es el del push, no el del intento), firmada cada vez con el instante de su intento. Lo
	// único que cambia es lo que el worker lee AL ENTREGAR: entre los dos intentos la empresa puso sus
	// variables, y el reintento las lleva (contrato, intake.push.md: «puede diferir entre el primer
	// intento y un reintento»).
	stable := func(d crmFakeDelivery) map[string]any {
		doc := maps.Clone(d.Doc)
		delete(doc, "variables")
		return doc
	}
	if first.ID != second.ID || !reflect.DeepEqual(stable(first), stable(second)) || first.Text("timestamp") == "" {
		t.Errorf("el reintento no es la misma entrega: ids %s y %s\nprimero: %s\nsegundo: %s", first.ID, second.ID, first.Body, second.Body)
	}
	want1, want2 := map[string]any{}, map[string]any{}
	for k, v := range p6Variables() {
		want2[k] = v
	}
	if !reflect.DeepEqual(first.Doc["variables"], want1) || !reflect.DeepEqual(second.Doc["variables"], want2) {
		t.Errorf("variables del primer intento %v y del reintento %v; quería {} y las que la empresa puso entre los dos",
			first.Doc["variables"], second.Doc["variables"])
	}
	for i, d := range []crmFakeDelivery{first, second} {
		if !d.SignatureOK || !d.InWindow || d.SchemaErr != nil || d.Status != http.StatusInternalServerError {
			t.Errorf("intento %d: firma=%v ventana=%v schema=%v respuesta=%d; quería un push válido contestado con 500",
				i+1, d.SignatureOK, d.InWindow, d.SchemaErr, d.Status)
		}
	}
	p6CheckBackoff(t, first, second)
}

// p6CheckBackoff exige que el segundo intento saliera 30 s ± 20 % después del primero (con holgura
// para la máquina cargada) y con otra firma: cada intento se firma con su propio instante.
func p6CheckBackoff(t *testing.T, first, second crmFakeDelivery) {
	t.Helper()
	t1, err1 := strconv.ParseInt(first.Timestamp, 10, 64)
	t2, err2 := strconv.ParseInt(second.Timestamp, 10, 64)
	if err1 != nil || err2 != nil || t2-t1 < 22 || t2-t1 > 40 || first.Header.Get(crmFakeHeaderSignature) == second.Header.Get(crmFakeHeaderSignature) {
		t.Errorf("X-Wapp-Timestamp de los dos intentos: %q y %q; quería el segundo 30 s ± 20 %% después, con otra firma",
			first.Timestamp, second.Timestamp)
	}
}

// p6Float devuelve v como float64, o -1 si no es un número (los números de una línea de log JSON).
func p6Float(v any) float64 {
	f, ok := v.(float64)
	if !ok {
		return -1
	}
	return f
}

// outboxView: GET /api/v1/integrations/outbox son CONTADORES de la empresa del token y nada más —ni
// ids, ni payloads, ni motivos—; sin nada en cola no hay `oldest_pending_at`; el viewer lo lee igual, y
// otra empresa ve la suya (a cero), no esta.
func (w *p6World) outboxView(t *testing.T) {
	mark := p9Scalar(t, w.sc.DB, p6OutboxMark)
	want := map[string]any{"pending": 0.0, "delivering": 0.0, "delivered": 4.0, "dead": 1.0}
	for name, caller := range map[string]p9Caller{"la administradora": w.admin, "el viewer": w.viewer} {
		r := w.call(t, caller, "", http.MethodGet, p6PathOutbox, nil)
		if got := p6Fields(t, r); r.Codigo != http.StatusOK || !reflect.DeepEqual(got, want) {
			t.Errorf("GET integrations/outbox como %s: HTTP %d %v, quería 200 %v", name, r.Codigo, got, want)
		}
	}
	zeros := map[string]any{"pending": 0.0, "delivering": 0.0, "delivered": 0.0, "dead": 0.0}
	r := w.call(t, w.other, "", http.MethodGet, p6PathOutbox, nil)
	if got := p6Fields(t, r); r.Codigo != http.StatusOK || !reflect.DeepEqual(got, zeros) {
		t.Errorf("GET integrations/outbox de la otra empresa: HTTP %d %v, quería 200 con todo a cero", r.Codigo, got)
	}
	if got := p9Scalar(t, w.sc.DB, `SELECT string_agg(status || '=' || n::text, ',' ORDER BY status) FROM
		(SELECT status, count(*) AS n FROM public.webhook_outbox WHERE tenant_id = $1 GROUP BY status) s`, w.sc.Tenant); got != "dead=1,delivered=4" {
		t.Errorf("webhook_outbox de la empresa por estado: %s, quería dead=1,delivered=4", got)
	}
	p6WantMark(t, w.sc, "tras leer el resumen de la cola", mark, p6OutboxMark)
}
