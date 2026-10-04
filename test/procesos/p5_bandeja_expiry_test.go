//go:build integracion

package procesos

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// El plazo del presupuesto (24 h, constante de plataforma) y la poda del literal del cliente. Los dos
// son PEREZOSOS: no hay reloj; se evalúan cuando la dueña lee su bandeja. Los candados que pasan a
// aserción son internal/intakes/inv_vencimiento_ast_test.go (vencer AVISA y no mata: ni transición,
// ni evento de telemetría, ni mensaje al cliente) y sello_poda_ast_test.go (la lectura que poda
// publica el instante de la poda).

const (
	// p5DefaultLiteralTTL es el plazo de retención del literal por defecto (12 meses, migración 0079).
	p5DefaultLiteralTTL = 31536000
)

// p5ListEntry es una fila de GET /api/v1/intakes.
type p5ListEntry struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Overdue bool   `json:"overdue"`
}

// overdueList lee la bandeja (GET /api/v1/intakes) y devuelve «id → vencida». Falla (t.Fatalf) si no
// es un 200 con las tres solicitudes del proceso.
func (w *p5World) overdueList(t *testing.T) map[string]bool {
	t.Helper()
	r := w.read(t, http.MethodGet, "/api/v1/intakes", nil)
	var list struct {
		Intakes []p5ListEntry `json:"intakes"`
		Total   int           `json:"total"`
	}
	r.JSON(t, &list)
	if r.Codigo != http.StatusOK || list.Total != 3 || len(list.Intakes) != 3 {
		t.Fatalf("GET /api/v1/intakes: HTTP %d con %d solicitudes (total %d), quería las 3 del proceso\ncuerpo: %s",
			r.Codigo, len(list.Intakes), list.Total, recortar(r.Cuerpo))
	}
	out := map[string]bool{}
	for _, in := range list.Intakes {
		out[in.ID] = in.Overdue
	}
	return out
}

// reminders cuenta las líneas del recordatorio del plazo que el log tiene de una solicitud.
func (w *p5World) reminders(id string) int {
	return len(p9LogLines(w.sc.S, p5MsgReminder, map[string]string{"intake_id": id, "emisor": "traza"}))
}

// p5Expiry es el vencimiento sobre el borrador que la dueña dejó esperando (w.open, en
// `pending_approval` con sus líneas): en plazo no se marca; el export no hace de reloj; pasado el
// plazo, la PRIMERA lectura lo marca y deja UN recordatorio sin tocar nada más; las siguientes no
// repiten; corregir reinicia el plazo pero no devuelve el recordatorio. Las solicitudes que ya no
// esperan a la dueña (aprobada, en needs_info) no vencen por viejas que sean.
func p5Expiry(t *testing.T, w *p5World) {
	sc := w.sc
	envejecer(t, sc.DB, "intakes", w.open, 23*time.Hour)
	if got := w.overdueList(t); got[w.open] || got[w.first] || got[w.asked] {
		t.Errorf("con 23 h de espera la bandeja marca vencidas %v; el plazo son 24 h", got)
	}
	envejecer(t, sc.DB, "intakes", w.open, 2*time.Hour)
	envejecer(t, sc.DB, "intakes", w.first, 25*time.Hour)
	envejecer(t, sc.DB, "intakes", w.asked, 25*time.Hour)
	before := p5Snapshot(t, sc.DB, sc.Tenant, w.open)
	if before.Intake["expiry_reminded_at"] != nil || w.reminders(w.open) != 0 {
		t.Fatalf("antes de leer la bandeja el presupuesto ya tiene marca (%v) o recordatorio", before.Intake["expiry_reminded_at"])
	}

	// El export y el resumen leen las mismas filas y NO hacen de reloj: descargar un CSV no avisa.
	for _, path := range []string{"/api/v1/intakes/export", "/api/v1/intakes/summary.json"} {
		if r := w.read(t, http.MethodGet, path, nil); r.Codigo != http.StatusOK {
			t.Errorf("GET %s: HTTP %d, quería 200", path, r.Codigo)
		}
	}
	before = w.expectUntouched(t, before, w.open, "tras el export y el resumen de un presupuesto vencido")

	marked := p5FirstOverdueRead(t, w, before)
	p5LaterOverdueReads(t, w, marked)
}

// p5FirstOverdueRead es la lectura que hace de reloj: la bandeja marca el presupuesto como vencido,
// escribe expiry_reminded_at —y NINGUNA otra columna: ni status ni updated_at, que es la base del
// plazo— y deja una traza del recordatorio. No publica telemetría ni le escribe al cliente.
func p5FirstOverdueRead(t *testing.T, w *p5World, before p5Snap) p5Snap {
	t.Helper()
	sc := w.sc
	if got := w.overdueList(t); !got[w.open] || got[w.first] || got[w.asked] {
		t.Errorf("la bandeja marca vencidas %v; quería solo el presupuesto que espera a la dueña (%s)", got, w.open)
	}
	marked := p5Snapshot(t, sc.DB, sc.Tenant, w.open)
	p5ExpectIntakeChange(t, before, marked, "la lectura de un presupuesto vencido", "expiry_reminded_at")
	p5ExpectSame(t, before, marked, "la lectura de un presupuesto vencido",
		"items", "revisions", "event", "jobs", "flow_events", "flow_state", "thread", "buyer", "outbox")
	if marked.Intake["status"] != "pending_approval" || marked.Intake["expiry_reminded_at"] == nil {
		t.Errorf("tras leerlo vencido, el presupuesto está %v con marca %v; quería pending_approval y la marca puesta",
			marked.Intake["status"], marked.Intake["expiry_reminded_at"])
	}
	lines := p9LogLines(sc.S, p5MsgReminder, map[string]string{"intake_id": w.open, "tenant_id": sc.Tenant, "emisor": "traza"})
	if len(lines) != 1 || fmt.Sprint(lines[0]["plazo_horas"]) != "24" {
		t.Errorf("el log tiene %d recordatorios del presupuesto vencido (%v), quería 1 con plazo_horas 24", len(lines), lines)
	}
	for _, id := range []string{w.first, w.asked} {
		if got := p9Scalar(t, sc.DB, `SELECT (expiry_reminded_at IS NULL)::text FROM public.intakes WHERE id = $1::uuid`, id); got != "true" || w.reminders(id) != 0 {
			t.Errorf("la solicitud %s, que ya no espera a la dueña, recibió marca o recordatorio de plazo", id)
		}
	}
	sc.expectNoPendingText(t, "tras marcar un presupuesto vencido: al cliente no se le dice nada")
	return marked
}

// p5LaterOverdueReads afirma el «una sola vez»: la bandeja y el detalle se vuelven a leer y nada
// cambia; la dueña corrige las líneas (updated_at se mueve: el presupuesto deja de estar vencido) y,
// vuelto a envejecer, vuelve a salir vencido pero el recordatorio NO se repite.
func p5LaterOverdueReads(t *testing.T, w *p5World, marked p5Snap) {
	t.Helper()
	sc := w.sc
	if got := w.overdueList(t); !got[w.open] {
		t.Errorf("en la segunda lectura la bandeja ya no marca el presupuesto como vencido")
	}
	d := p5DecodeDetail(t, w.read(t, http.MethodGet, p5Path(w.open, ""), nil), "GET del presupuesto vencido")
	if !d.Overdue || d.Status != "pending_approval" || fmt.Sprint(d.AllowedTransitions) != "[cancelled confirmed needs_info rejected]" {
		t.Errorf("el detalle del presupuesto vencido = %s vencida=%v con destinos %v; vencer no cambia el estado ni sus salidas",
			d.Status, d.Overdue, d.AllowedTransitions)
	}
	w.expectUntouched(t, marked, w.open, "tras releer un presupuesto vencido y ya avisado")
	if n := w.reminders(w.open); n != 1 {
		t.Errorf("tras releer, el log tiene %d recordatorios del presupuesto, quería 1", n)
	}

	if d = w.putItems(t, w.open, false); d.Overdue {
		t.Errorf("recién corregido, el presupuesto sigue saliendo vencido: corregir reinicia el plazo")
	}
	envejecer(t, sc.DB, "intakes", w.open, 25*time.Hour)
	if got := w.overdueList(t); !got[w.open] {
		t.Errorf("vuelto a envejecer tras la corrección, la bandeja no marca el presupuesto como vencido")
	}
	again := p5Snapshot(t, sc.DB, sc.Tenant, w.open)
	if again.Intake["expiry_reminded_at"] != marked.Intake["expiry_reminded_at"] || w.reminders(w.open) != 1 {
		t.Errorf("el recordatorio se repitió: marca %v → %v, %d trazas; el presupuesto se avisa UNA vez",
			marked.Intake["expiry_reminded_at"], again.Intake["expiry_reminded_at"], w.reminders(w.open))
	}
	sc.expectNoPendingText(t, "tras el vencimiento")
}

// p5AgeRevision retrasa en `age` el created_at de UNA revisión, como si se hubiera escrito hace ese
// tiempo: la retención del literal se mide contra esa fecha con el reloj de Postgres. Falla
// (t.Fatalf) si el SQL falla o no existe exactamente esa revisión.
//
// 🔧 POR QUÉ NO HAY PUERTA HTTP: el plazo de retención son meses y ninguna ruta mueve el reloj ni
// reescribe la fecha de una revisión (la misma razón que envejecer, que no admite esta tabla).
func p5AgeRevision(t *testing.T, db *sql.DB, intakeID string, revisionNo int, age time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), topeFixture)
	defer cancel()
	res, err := db.ExecContext(ctx, `UPDATE public.intake_revisions
		SET created_at = created_at - $3::double precision * interval '1 second'
		WHERE intake_id = $1::uuid AND revision_no = $2`, intakeID, revisionNo, age.Seconds())
	if err != nil {
		t.Fatalf("p5AgeRevision(%s, %d): %v", intakeID, revisionNo, err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		t.Fatalf("p5AgeRevision(%s, %d): tocó %d revisiones (error %v), quería 1", intakeID, revisionNo, n, err)
	}
}

// p5SetLiteralTTL deja el plazo de retención del literal de la empresa
// (tenant_settings.intake_literal_ttl_seconds; 0 = sin poda). Es un upsert que no toca el resto.
//
// 🔧 POR QUÉ NO HAY PUERTA HTTP: ninguna ruta escribe ese ajuste; hoy lo cambia el operador por SQL.
func p5SetLiteralTTL(t *testing.T, db *sql.DB, tenant string, seconds int) {
	t.Helper()
	if err := exigirUUID("la empresa", tenant); err != nil {
		t.Fatalf("p5SetLiteralTTL: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), topeFixture)
	defer cancel()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.tenant_settings (tenant_id, intake_literal_ttl_seconds) VALUES ($1, $2)
		ON CONFLICT (tenant_id) DO UPDATE SET intake_literal_ttl_seconds = $2`, tenant, seconds); err != nil {
		t.Fatalf("p5SetLiteralTTL: escribir tenant_settings de %s: %v", tenant, err)
	}
}

// p5LiteralView es lo que el detalle publica del literal de la revisión 1 (la del pipeline).
type p5LiteralView struct {
	HasText     bool   // el payload trae `source_text`
	HasEvidence bool   // alguna línea trae `evidence`
	Lines       int    // cuántas líneas trae el payload
	PrunedAt    string // `literal_pruned_at`; vacío si la clave no viene
}

// String resume la vista para compararla de un vistazo: «literal|evidencias|líneas|podada».
func (v p5LiteralView) String() string {
	return fmt.Sprintf("%v|%v|%d|%v", v.HasText, v.HasEvidence, v.Lines, v.PrunedAt != "")
}

// literal lee el detalle de la solicitud y devuelve lo que publica del literal de su revisión 1.
// Afirma de paso que las demás revisiones —que nunca tuvieron literal— salen sin `literal_pruned_at`.
func (w *p5World) literal(t *testing.T, id string) p5LiteralView {
	t.Helper()
	d := p5DecodeDetail(t, w.read(t, http.MethodGet, p5Path(id, ""), nil), "GET del detalle")
	if !strings.HasPrefix(p5Kinds(d.Revisions), "1:interpreted:system") {
		t.Fatalf("el detalle de %s no trae la revisión 1 interpreted la primera: %q", id, p5Kinds(d.Revisions))
	}
	var payload struct {
		SourceText string            `json:"source_text"`
		Lines      []json.RawMessage `json:"lines"`
	}
	if err := json.Unmarshal(d.Revisions[0].Payload, &payload); err != nil {
		t.Fatalf("el payload de la revisión 1 de %s no decodifica: %v", id, err)
	}
	for i, rev := range d.Revisions[1:] {
		if rev.LiteralPrunedAt != "" {
			t.Errorf("la revisión %d de %s, que nunca tuvo literal, sale con literal_pruned_at %q", i+2, id, rev.LiteralPrunedAt)
		}
	}
	return p5LiteralView{
		HasText:     payload.SourceText != "",
		HasEvidence: strings.Contains(string(d.Revisions[0].Payload), `"evidence"`),
		Lines:       len(payload.Lines),
		PrunedAt:    d.Revisions[0].LiteralPrunedAt,
	}
}

// p5Pruning es la poda de revisiones como conducta. Con el plazo de la empresa en 0 (sin poda), una
// revisión de diez años conserva su literal. Con el plazo por defecto (12 meses), la lectura de una
// revisión de trece meses DESTRUYE el literal del cliente —las tres piezas del sobre— y esa misma
// respuesta ya trae `literal_pruned_at`, el instante que quedó en la fila; la interpretación (las
// líneas) queda intacta; las lecturas siguientes dicen lo mismo y no vuelven a podar.
func p5Pruning(t *testing.T, w *p5World) {
	sc := w.sc
	if got := w.literal(t, w.open).String(); got != "true|true|4|false" {
		t.Fatalf("antes de vencer, el detalle publica literal|evidencias|líneas|podada = %s; quería el literal entero y sin marca", got)
	}

	p5SetLiteralTTL(t, sc.DB, sc.Tenant, 0)
	p5AgeRevision(t, sc.DB, w.asked, 1, 10*365*24*time.Hour)
	if got := w.literal(t, w.asked).String(); got != "true|true|4|false" {
		t.Errorf("con el plazo de retención en 0 (sin poda), una revisión de diez años publica %s; quería su literal entero", got)
	}
	p5SetLiteralTTL(t, sc.DB, sc.Tenant, p5DefaultLiteralTTL)

	p5AgeRevision(t, sc.DB, w.open, 1, 13*30*24*time.Hour)
	before := p5Snapshot(t, sc.DB, sc.Tenant, w.open)
	view := w.literal(t, w.open)
	if got := view.String(); got != "false|false|4|true" {
		t.Errorf("la lectura de una revisión vencida publica literal|evidencias|líneas|podada = %s; quería sin literal, con sus 4 líneas y la marca YA", got)
	}
	after := p5ExpectPrunedRow(t, w, before, view.PrunedAt)
	p5ExpectPrunedOnce(t, w, after, view.PrunedAt)
}

// p5ExpectPrunedRow afirma lo que la lectura que poda deja en Postgres: en la revisión 1 cambian las
// tres piezas del sobre (a NULL) y la marca, y NADA más —el payload, intacto—; las otras revisiones y
// la solicitud entera, iguales; y el instante que la respuesta publicó es el que quedó en la fila.
func p5ExpectPrunedRow(t *testing.T, w *p5World, before p5Snap, published string) p5Snap {
	t.Helper()
	sc := w.sc
	after := p5Snapshot(t, sc.DB, sc.Tenant, w.open)
	p5ExpectIntakeChange(t, before, after, "la lectura que poda")
	p5ExpectSame(t, before, after, "la lectura que poda", "items", "event", "jobs", "flow_events", "flow_state", "thread", "buyer", "outbox")
	if got := p5Changed(before.Revisions[0], after.Revisions[0]); strings.Join(got, ",") != "literal_dek,literal_enc,literal_kek_id,literal_pruned_at" {
		t.Errorf("la poda cambió en la revisión 1 las columnas %v; quería las tres piezas del sobre y la marca, y el payload intacto", got)
	}
	for i := 1; i < len(before.Revisions); i++ {
		if got := p5Changed(before.Revisions[i], after.Revisions[i]); len(got) != 0 {
			t.Errorf("la poda tocó la revisión %d: %v", i+1, got)
		}
	}
	rev := after.Revisions[0]
	if got := fmt.Sprintf("%v|%v|%v|%v", rev["literal_enc"], rev["literal_dek"], rev["literal_kek_id"], rev["literal_pruned_at"] != nil); got != "<nil>|<nil>|<nil>|true" {
		t.Errorf("la revisión podada quedó con sobre|dek|kek|marca = %s; quería el sobre vacío y la marca puesta", got)
	}
	const column = `SELECT to_char(literal_pruned_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
		FROM public.intake_revisions WHERE intake_id = $1::uuid AND revision_no = 1`
	if stored := p9Scalar(t, sc.DB, column, w.open); published != stored {
		t.Errorf("la lectura que podó publicó literal_pruned_at %q y en la fila quedó %q: tiene que decir lo mismo", published, stored)
	}
	return after
}

// p5ExpectPrunedOnce afirma que la poda ocurre UNA vez: la segunda lectura publica el mismo sello y
// no escribe; el log trae un solo evento de poda de la solicitud, de la revisión 1 y con el plazo por
// defecto, sin una palabra del texto destruido; y la revisión leída con el plazo en 0 no dejó ninguno.
func p5ExpectPrunedOnce(t *testing.T, w *p5World, after p5Snap, published string) {
	t.Helper()
	sc := w.sc
	if again := w.literal(t, w.open).PrunedAt; again != published {
		t.Errorf("la segunda lectura mueve el sello de la poda: %q → %q", published, again)
	}
	w.expectUntouched(t, after, w.open, "tras releer una revisión ya podada")
	pruned := p9LogLines(sc.S, p5MsgPruned, map[string]string{"intake_id": w.open})
	if len(pruned) != 1 {
		t.Fatalf("el log tiene %d eventos de poda de la solicitud, quería 1", len(pruned))
	}
	if got := fmt.Sprintf("%v|%v", pruned[0]["revision_no"], pruned[0]["ttl_segundos"] == float64(p5DefaultLiteralTTL)); got != "1|true" {
		t.Errorf("el evento de poda es de la revisión|con el plazo por defecto = %s, quería 1|true: %v", got, pruned[0])
	}
	line := fmt.Sprint(pruned[0])
	for _, needle := range []string{scriptEvidenceTequenos, scriptEvidenceDelivery, "Hola, buenas"} {
		if strings.Contains(line, needle) {
			t.Errorf("el evento de poda arrastró literal del cliente (%q): %s", needle, line)
		}
	}
	if n := len(p9LogLines(sc.S, p5MsgPruned, map[string]string{"intake_id": w.asked})); n != 0 {
		t.Errorf("la revisión leída con el plazo en 0 dejó %d eventos de poda", n)
	}
}
