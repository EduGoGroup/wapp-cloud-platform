//go:build integracion

package procesos

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// POST /api/v1/intakes/discard: el descarte manual, por lotes, de pedidos huérfanos. Contesta por
// ítem (no es todo-o-nada) y solo descarta desde `open` y desde el `expired` legado.

// p5DiscardResult es el cuerpo del 200 del descarte.
type p5DiscardResult struct {
	Discarded []string `json:"discarded"`
	Skipped   []struct {
		IntakeID string `json:"intake_id"`
		Reason   string `json:"reason"`
	} `json:"skipped"`
}

// discard manda un lote y devuelve lo descartado y lo saltado («id=razón»), en el orden de la
// respuesta. Falla (t.Fatalf) si no es un 200.
func (w *p5World) discard(t *testing.T, ids ...string) (discarded, skipped string) {
	t.Helper()
	r := w.write(t, http.MethodPost, "/api/v1/intakes/discard", map[string]any{"intake_ids": ids})
	if r.Codigo != http.StatusOK {
		t.Fatalf("POST /api/v1/intakes/discard: HTTP %d, quería 200\ncuerpo: %s", r.Codigo, recortar(r.Cuerpo))
	}
	var res p5DiscardResult
	r.JSON(t, &res)
	if res.Discarded == nil || res.Skipped == nil {
		t.Errorf("el descarte contestó una lista nula: las dos salen siempre, aunque vacías\ncuerpo: %s", recortar(r.Cuerpo))
	}
	out := make([]string, 0, len(res.Skipped))
	for _, s := range res.Skipped {
		out = append(out, s.IntakeID+"="+s.Reason)
	}
	return strings.Join(res.Discarded, ","), strings.Join(out, ",")
}

// p5ForceLegacyExpired deja la solicitud en el estado LEGADO `expired`, sin tocar ninguna otra
// columna. Falla (t.Fatalf) si el SQL falla o no existe exactamente esa solicitud.
//
// 🔧 POR QUÉ NO HAY PUERTA HTTP: el descarte solo procede desde `open` y desde `expired`. A `expired`
// no lleva NINGUNA transición (es el estado del reloj que se derogó: nada muere por tiempo), y `open`
// es el carrito numérico en curso, que nace de un flujo de carrito y no de un borrador del pipeline:
// desde `pending_approval` no hay camino a ninguno de los dos. Sin este fixture, lo único que un
// proceso montado sobre createDraft puede ver del descarte son sus rechazos.
func p5ForceLegacyExpired(t *testing.T, db *sql.DB, tenant, id string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), topeFixture)
	defer cancel()
	res, err := db.ExecContext(ctx, `UPDATE public.intakes SET status = 'expired' WHERE id = $1::uuid AND tenant_id = $2`, id, tenant)
	if err != nil {
		t.Fatalf("p5ForceLegacyExpired(%s): %v", id, err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		t.Fatalf("p5ForceLegacyExpired(%s): tocó %d solicitudes (error %v), quería 1", id, n, err)
	}
}

// p5Discard es el descarte: los cuerpos que la puerta rechaza enteros; el lote mixto, en el que
// ninguna solicitud nacida del pipeline es descartable (ni por aprobar, ni aprobada, ni en
// needs_info) y los ids adversarios son `not_found`; y, sobre el estado legado, la guarda del evento
// vivo, el descarte de verdad y su idempotencia.
func p5Discard(t *testing.T, w *p5World) {
	sc := w.sc
	for name, c := range map[string]struct {
		body any
		want string
	}{
		"sin cuerpo":                      {nil, p5BadJSON},
		"un cuerpo que no es un objeto":   {"no soy un objeto", p5BadJSON},
		"ids que no son una lista":        {map[string]any{"intake_ids": w.open}, p5BadJSON},
		"sin la clave":                    {map[string]any{}, "intake_ids es obligatorio: manda entre 1 y 200 ids"},
		"la lista vacía":                  {map[string]any{"intake_ids": []string{}}, "intake_ids es obligatorio: manda entre 1 y 200 ids"},
		"un lote de más de 200 ids":       {map[string]any{"intake_ids": make([]string, 201)}, "el lote trae 201 solicitudes y el máximo es 200"},
		"un lote enorme del mismo id":     {map[string]any{"intake_ids": p5Repeat(w.open, 500)}, "el lote trae 500 solicitudes y el máximo es 200"},
		"ids en dígitos no ASCII, de más": {map[string]any{"intake_ids": p5Repeat("١٢٣", 201)}, "el lote trae 201 solicitudes y el máximo es 200"},
	} {
		r := w.write(t, http.MethodPost, "/api/v1/intakes/discard", c.body)
		p5ExpectError(t, r, http.StatusBadRequest, c.want, "descartar con "+name)
	}

	before := map[string]p5Snap{}
	for _, id := range []string{w.first, w.asked, w.open} {
		before[id] = p5Snapshot(t, sc.DB, sc.Tenant, id)
	}
	unknown := uuidAleatorio(t)
	discarded, skipped := w.discard(t, w.open, w.first, w.open, w.asked, unknown, "a@@b", "١"+w.open[1:], " "+w.open, "")
	want := strings.Join([]string{
		w.open + "=not_open", w.first + "=not_open", w.asked + "=not_open", unknown + "=not_found",
		"a@@b=not_found", "١" + w.open[1:] + "=not_found", " " + w.open + "=not_found", "=not_found",
	}, ",")
	if discarded != "" || skipped != want {
		t.Errorf("el lote mixto descartó %q y saltó\n%s\nquería no descartar nada y saltar (sin repetir el id duplicado)\n%s", discarded, skipped, want)
	}
	for id, snap := range before {
		w.expectUntouched(t, snap, id, "tras un descarte que la salta")
	}

	p5DiscardLegacy(t, w, before[w.open])
	sc.expectNoPendingText(t, "tras los descartes: descartar no le habla al cliente")
}

// p5Repeat devuelve n copias de s.
func p5Repeat(s string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}

// p5DiscardLegacy lleva w.open al estado legado `expired` (fixture) y afirma el descarte de verdad:
// con su evento conversacional todavía `open`, la guarda `live_event` lo salta sin escribir; con el
// evento cancelado por su puerta (POST /api/v1/conversation-events/{id}/cancel), se descarta — en
// `intakes` cambian status y updated_at y nada más, nace UNA revisión `discarded` de `owner` con el
// estado de partida y el total, y las líneas se conservan —; y repetirlo es `already_discarded`, sin
// una segunda revisión.
func p5DiscardLegacy(t *testing.T, w *p5World, pending p5Snap) {
	t.Helper()
	sc := w.sc
	p5ForceLegacyExpired(t, sc.DB, sc.Tenant, w.open)
	expired := p5Snapshot(t, sc.DB, sc.Tenant, w.open)
	p5ExpectIntakeChange(t, pending, expired, "el fixture del estado legado", "status")

	if discarded, skipped := w.discard(t, w.open); discarded != "" || skipped != w.open+"=live_event" {
		t.Errorf("con el evento vivo, el descarte contestó %q / %q; quería saltarla por live_event", discarded, skipped)
	}
	w.expectUntouched(t, expired, w.open, "tras el descarte frenado por el evento vivo")

	eventID := fmt.Sprint(expired.Intake["event_id"])
	r := w.write(t, http.MethodPost, "/api/v1/conversation-events/"+eventID+"/cancel", nil)
	if !p9ErrorIs(r, http.StatusOK, `"status":"cancelled"`) {
		t.Fatalf("cancelar el evento %s: HTTP %d %s; quería 200 con el evento cancelled", eventID, r.Codigo, recortar(r.Cuerpo))
	}
	cancelled := p5Snapshot(t, sc.DB, sc.Tenant, w.open)
	p5ExpectIntakeChange(t, expired, cancelled, "cancelar el evento de la solicitud")
	p5ExpectSame(t, expired, cancelled, "cancelar el evento de la solicitud", "items", "revisions", "jobs", "buyer", "outbox")

	if discarded, skipped := w.discard(t, w.open); discarded != w.open || skipped != "" {
		t.Errorf("con el evento cancelado, el descarte contestó %q / %q; quería descartarla", discarded, skipped)
	}
	gone := p5Snapshot(t, sc.DB, sc.Tenant, w.open)
	p5ExpectIntakeChange(t, cancelled, gone, "descartar", "status", "updated_at")
	p5ExpectSame(t, cancelled, gone, "descartar", "items", "event", "jobs", "flow_events", "flow_state", "thread", "buyer", "outbox")
	const revision = `SELECT revision_no::text || '|' || kind || '|' || created_by || '|' || (rendered_text IS NULL)::text
			|| '|' || (payload->>'version') || '|' || (payload->>'from_status') || '|' || (payload->>'total')
		FROM public.intake_revisions WHERE intake_id = $1::uuid ORDER BY revision_no DESC LIMIT 1`
	wantRev := fmt.Sprintf("%d|discarded|owner|true|1|expired|%d", len(cancelled.Revisions)+1, p5ItemsTotal)
	if got := p9Scalar(t, sc.DB, revision, w.open); got != wantRev || len(gone.Revisions) != len(cancelled.Revisions)+1 {
		t.Errorf("la revisión del descarte = %q (%d revisiones), quería %q", got, len(gone.Revisions), wantRev)
	}
	if gone.Intake["status"] != "abandoned" || gone.Event["status"] != "cancelled" || len(gone.Items) != 7 {
		t.Errorf("tras descartar: solicitud %v, evento %v, %d líneas; quería abandoned, cancelled y las 7 líneas",
			gone.Intake["status"], gone.Event["status"], len(gone.Items))
	}

	if discarded, skipped := w.discard(t, w.open, w.open); discarded != "" || skipped != w.open+"=already_discarded" {
		t.Errorf("descartar otra vez contestó %q / %q; quería already_discarded", discarded, skipped)
	}
	w.expectUntouched(t, gone, w.open, "tras repetir el descarte")
}
