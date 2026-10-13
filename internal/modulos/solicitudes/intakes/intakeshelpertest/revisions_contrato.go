package intakeshelpertest

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// caseInsertRevisionNumbers: la numeración es POR SOLICITUD y arranca en 1; el número que traiga
// la entrada se ignora; dos escrituras iguales son dos revisiones; y escribir una revisión no toca
// la cabecera ni las líneas.
func caseInsertRevisionNumbers(t *testing.T, m Montaje) {
	w := seedWitnesses(t, m)
	first := seed(t, m, m.TenantA, intakes.StatusOpen, 1, eventCancelled, customerLines()...)
	second := seed(t, m, m.TenantA, intakes.StatusOpen, 2, eventCancelled)
	want := take(t, m, first).clone()
	m.Advance(t)

	for i, c := range []struct {
		target   ref
		kind, by string
		text     string
		wantNo   int
	}{
		{first, intakes.RevisionKindCart, intakes.RevisionBySystem, "", 1},
		{first, intakes.RevisionKindCart, intakes.RevisionBySystem, "", 2}, // idéntica: no es idempotente
		{second, intakes.RevisionKindCart, intakes.RevisionBySystem, "", 1},
		{first, intakes.RevisionKindApproved, intakes.RevisionByOwner, "Tu pedido: 2 panes", 3},
	} {
		rev, err := m.Store.InsertRevision(bg(), intakes.Revision{
			IntakeID: c.target.id, RevisionNo: 99, Kind: c.kind, Payload: cartPayload(t),
			RenderedText: c.text, CreatedBy: c.by,
		})
		if err != nil {
			t.Fatalf("InsertRevision %d: error inesperado %v", i, err)
		}
		if rev.RevisionNo != c.wantNo {
			t.Errorf("InsertRevision %d: RevisionNo = %d, quería %d", i, rev.RevisionNo, c.wantNo)
		}
		if rev.IntakeID != c.target.id || rev.Kind != c.kind || rev.CreatedBy != c.by || rev.RenderedText != c.text {
			t.Errorf("InsertRevision %d devolvió %+v; no es lo que se escribió", i, rev)
		}
		if rev.CreatedAt.IsZero() || !rev.LiteralPrunedAt.IsZero() {
			t.Errorf("InsertRevision %d: CreatedAt = %v y LiteralPrunedAt = %v; quería fechada y sin sello",
				i, rev.CreatedAt, rev.LiteralPrunedAt)
		}
		if !sameJSON(rev.Payload, cartPayload(t)) {
			t.Errorf("InsertRevision %d: payload devuelto = %s; uno sin literal vuelve igual", i, rev.Payload)
		}
		if c.target == first {
			want.detail.Revisions = append(want.detail.Revisions, rev)
		}
	}
	requireSame(t, "la solicitud con tres revisiones", take(t, m, first), want)
	if got := get(t, m, second).Revisions; len(got) != 1 || got[0].RevisionNo != 1 {
		t.Errorf("la otra solicitud tiene %+v; quería su única revisión, la nº 1", got)
	}
	w.requireUntouched(t, m)
}

func caseInsertRevisionEmptyPayload(t *testing.T, m Montaje) {
	w := seedWitnesses(t, m)
	r := seed(t, m, m.TenantA, intakes.StatusOpen, 1, eventCancelled, customerLines()...)
	before := take(t, m, r)
	for name, payload := range map[string]json.RawMessage{"nil": nil, "vacío": {}} {
		got, err := m.Store.InsertRevision(bg(), intakes.Revision{
			IntakeID: r.id, Kind: intakes.RevisionKindCart, Payload: payload, CreatedBy: intakes.RevisionBySystem,
		})
		if !errors.Is(err, intakes.ErrEmptyRevisionPayload) {
			t.Errorf("InsertRevision con payload %s: err = %v, quería ErrEmptyRevisionPayload", name, err)
		}
		if got.RevisionNo != 0 {
			t.Errorf("InsertRevision con payload %s devolvió una revisión numerada: %+v", name, got)
		}
	}
	requireSame(t, "tras los rechazos", take(t, m, r), before)
	w.requireUntouched(t, m)
}

// caseInsertRevisionLiteral: el literal del cliente (`source_text` y la `evidence` de cada línea)
// SALE del payload al escribir —la revisión que se devuelve ya no lo lleva— y VUELVE a su sitio al
// leer el detalle. La interpretación estructurada no se toca en ninguno de los dos sentidos.
func caseInsertRevisionLiteral(t *testing.T, m Montaje) {
	w := seedWitnesses(t, m)
	r := seed(t, m, m.TenantA, intakes.StatusNeedsInfo, 1, eventCancelled)
	const (
		full  = `{"version":1,"source_text":"quiero dos panes sin sal","lines":[{"sku":"pan","qty":2,"evidence":"dos panes"},{"sku":"leche","qty":1}],"total":5}`
		clean = `{"version":1,"lines":[{"sku":"pan","qty":2},{"sku":"leche","qty":1}],"total":5}`
	)
	written, err := m.Store.InsertRevision(bg(), intakes.Revision{
		IntakeID: r.id, Kind: intakes.RevisionKindInterpreted, Payload: json.RawMessage(full),
		CreatedBy: intakes.RevisionBySystem,
	})
	if err != nil {
		t.Fatalf("InsertRevision con literal: error inesperado %v", err)
	}
	if !sameJSON(written.Payload, json.RawMessage(clean)) {
		t.Errorf("payload devuelto = %s, quería el payload SIN literal: %s", written.Payload, clean)
	}

	read := get(t, m, r).Revisions
	if len(read) != 1 {
		t.Fatalf("el detalle trae %d revisiones, quería 1", len(read))
	}
	if !sameJSON(read[0].Payload, json.RawMessage(full)) {
		t.Errorf("payload leído = %s, quería el literal devuelto a su sitio: %s", read[0].Payload, full)
	}
	if !read[0].LiteralPrunedAt.IsZero() {
		t.Errorf("una revisión recién escrita sale con sello de poda: %v", read[0].LiteralPrunedAt)
	}
	// Dos lecturas seguidas dan lo mismo: el literal no se acumula ni se gasta al leer.
	if again := get(t, m, r).Revisions; len(again) != 1 || !sameJSON(again[0].Payload, read[0].Payload) {
		t.Errorf("la segunda lectura cambió el payload: %+v", again)
	}
	w.requireUntouched(t, m)
}

// approved escribe una revisión `approved` con ese texto y deja pasar el reloj: la siguiente es
// estrictamente más reciente.
func approved(t *testing.T, m Montaje, r ref, kind, text string) {
	t.Helper()
	insertRevision(t, m, r.id, kind, intakes.RevisionByOwner, text)
	m.Advance(t)
}

// seedApprovedHistory deja en TenantA tres cotizaciones aprobadas con texto —"uno", "dos" y
// " tres ", en ese orden de antigüedad, repartidas en dos solicitudes— rodeadas de todo lo que NO
// cuenta, y una aprobada en TenantB.
func seedApprovedHistory(t *testing.T, m Montaje) (first, second ref) {
	t.Helper()
	first = seed(t, m, m.TenantA, intakes.StatusConfirmed, 1, eventCancelled)
	second = seed(t, m, m.TenantA, intakes.StatusConfirmed, 2, eventCancelled)
	foreign := seed(t, m, m.TenantB, intakes.StatusConfirmed, 1, eventCancelled)

	approved(t, m, first, intakes.RevisionKindApproved, "uno")
	approved(t, m, first, intakes.RevisionKindCart, "del carrito: no cuenta")
	approved(t, m, foreign, intakes.RevisionKindApproved, "ajeno")
	approved(t, m, second, intakes.RevisionKindApproved, "dos")
	approved(t, m, first, intakes.RevisionKindApproved, "")              // sin texto
	approved(t, m, second, intakes.RevisionKindApproved, " \t\n\r\f\v ") // solo blancos
	approved(t, m, second, intakes.RevisionKindCorrected, "corregida: tampoco")
	approved(t, m, first, intakes.RevisionKindApproved, " tres ")
	return first, second
}

// approvedTexts lee el historial o falla el test.
func approvedTexts(t *testing.T, m Montaje, tenant string, limit int) []string {
	t.Helper()
	got, err := m.Store.ApprovedRenderedTexts(bg(), tenant, limit)
	if err != nil {
		t.Fatalf("ApprovedRenderedTexts(%d): error inesperado %v", limit, err)
	}
	return got
}

// caseApprovedTexts: solo las `approved` con texto, de la más reciente a la más antigua, sin
// recortar el texto que cuenta. Las de otra clase, las vacías y las de solo blancos no salen ni
// gastan cupo.
func caseApprovedTexts(t *testing.T, m Montaje) {
	seedApprovedHistory(t, m)
	if got, want := approvedTexts(t, m, m.TenantA, 10), []string{" tres ", "dos", "uno"}; !slices.Equal(got, want) {
		t.Errorf("historial aprobado = %q, quería %q", got, want)
	}
	// El cupo se cuenta sobre las que valen: entre "dos" y " tres " hay tres que no cuentan.
	if got, want := approvedTexts(t, m, m.TenantA, 2), []string{" tres ", "dos"}; !slices.Equal(got, want) {
		t.Errorf("historial aprobado con límite 2 = %q, quería %q", got, want)
	}
}

// caseApprovedTextsLimitAndTenant: pedir cero (o menos) no es un error y no trae nada; el tenant
// acota; y por encima de MaxApprovedTexts se recorta en silencio. A igualdad de fecha desempata el
// número de revisión, descendente.
func caseApprovedTextsLimitAndTenant(t *testing.T, m Montaje) {
	first, _ := seedApprovedHistory(t, m)
	for _, limit := range []int{0, -1} {
		if got := approvedTexts(t, m, m.TenantA, limit); len(got) != 0 {
			t.Errorf("ApprovedRenderedTexts(%d) = %q, quería nada", limit, got)
		}
	}
	if got, want := approvedTexts(t, m, m.TenantB, 10), []string{"ajeno"}; !slices.Equal(got, want) {
		t.Errorf("historial del otro tenant = %q, quería %q", got, want)
	}
	if got := approvedTexts(t, m, uuid.NewString(), 10); len(got) != 0 {
		t.Errorf("historial de un tenant sin nada = %q, quería nada", got)
	}

	// Una tanda sin dejar pasar el reloj, hasta superar la cota: sale la cota, lo último primero.
	const last = "la última de la tanda"
	for range intakes.MaxApprovedTexts {
		insertRevision(t, m, first.id, intakes.RevisionKindApproved, intakes.RevisionByOwner, "de la tanda")
	}
	insertRevision(t, m, first.id, intakes.RevisionKindApproved, intakes.RevisionByOwner, last)
	got := approvedTexts(t, m, m.TenantA, intakes.MaxApprovedTexts+500)
	if len(got) != intakes.MaxApprovedTexts {
		t.Fatalf("pidiendo de más salen %d textos, quería la cota, %d", len(got), intakes.MaxApprovedTexts)
	}
	if got[0] != last {
		t.Errorf("el primero es %q, quería %q (a igualdad de fecha, el número de revisión más alto)", got[0], last)
	}
}
