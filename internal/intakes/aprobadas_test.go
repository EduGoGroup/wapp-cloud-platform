package intakes_test

// aprobadas_test.go — el HISTORIAL APROBADO sobre el doble en memoria.
//
// El aislamiento por tenant (INV-8) se prueba TAMBIÉN aquí y no solo contra Postgres,
// aunque allí es donde vive el JOIN que de verdad lo garantiza: los tests de
// `quotetext` corren contra este doble, y una fuga aquí les enseñaría al modelo la voz
// de otro negocio sin que nada se pusiera rojo. Dos redes, dos síntomas distintos.

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// baseAprobadas es el instante desde el que se fechan las revisiones de estos tests.
var baseAprobadas = time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

// sembrarAprobada mete una solicitud con su revisión en el doble. El ctx va PRIMERO,
// antes del *testing.T, que es lo que exigen revive/context-as-argument y contextcheck.
func sembrarAprobada(ctx context.Context, t *testing.T, m *intakes.MemoryStore,
	tenantID, intakeID, texto, kind string, cuando time.Time) {
	t.Helper()
	m.Add(tenantID, intakes.Intake{ID: intakeID, Status: intakes.StatusConfirmed})
	if _, err := m.InsertRevision(ctx, intakes.Revision{
		IntakeID: intakeID, Kind: kind, Payload: []byte(`{"version":1}`),
		RenderedText: texto, CreatedBy: intakes.RevisionByOwner, CreatedAt: cuando,
	}); err != nil {
		t.Fatalf("sembrando la revisión: %v", err)
	}
}

// exigirTextos compara la lista devuelta con la esperada, en orden.
func exigirTextos(t *testing.T, got []string, err error, quiero ...string) {
	t.Helper()
	if err != nil {
		t.Fatalf("ApprovedRenderedTexts: %v", err)
	}
	if len(got) != len(quiero) {
		t.Fatalf("salieron %d textos (%v); se esperaban %d (%v)", len(got), got, len(quiero), quiero)
	}
	for i := range quiero {
		if got[i] != quiero[i] {
			t.Errorf("[%d] = %q; se esperaba %q", i, got[i], quiero[i])
		}
	}
}

// bandejaConHistorial siembra dos tenants: A con dos aprobadas, una corregida y una de
// texto en blanco; B con la suya.
func bandejaConHistorial(ctx context.Context, t *testing.T) *intakes.MemoryStore {
	t.Helper()
	m := intakes.NewMemoryStore()
	sembrarAprobada(ctx, t, m, "tenant-a", "a1", "la antigua de A",
		intakes.RevisionKindApproved, baseAprobadas.Add(-2*time.Hour))
	sembrarAprobada(ctx, t, m, "tenant-a", "a2", "la nueva de A",
		intakes.RevisionKindApproved, baseAprobadas.Add(-1*time.Hour))
	sembrarAprobada(ctx, t, m, "tenant-a", "a3", "una corrección",
		intakes.RevisionKindCorrected, baseAprobadas)
	sembrarAprobada(ctx, t, m, "tenant-a", "a4", "   ",
		intakes.RevisionKindApproved, baseAprobadas)
	sembrarAprobada(ctx, t, m, "tenant-b", "b1", "la voz de OTRO negocio",
		intakes.RevisionKindApproved, baseAprobadas)
	return m
}

func TestMemoryApprovedRenderedTexts_OrdenYFiltros(t *testing.T) {
	ctx := context.Background()
	m := bandejaConHistorial(ctx, t)

	got, err := m.ApprovedRenderedTexts(ctx, "tenant-a", 10)
	exigirTextos(t, got, err, "la nueva de A", "la antigua de A")
}

// 🔴 INV-8: la voz de un negocio no se le enseña al modelo de otro.
//
// El control de B es lo que hace que el assert no sea vacuo: si el filtro devolviera
// vacío por accidente, la primera mitad pasaría igual.
func TestMemoryApprovedRenderedTexts_AislamientoPorTenant(t *testing.T) {
	ctx := context.Background()
	m := bandejaConHistorial(ctx, t)

	deA, err := m.ApprovedRenderedTexts(ctx, "tenant-a", 10)
	if err != nil {
		t.Fatalf("ApprovedRenderedTexts(A): %v", err)
	}
	for _, texto := range deA {
		if texto == "la voz de OTRO negocio" {
			t.Fatalf("se filtró el historial del tenant B al tenant A: %v", deA)
		}
	}

	deB, err := m.ApprovedRenderedTexts(ctx, "tenant-b", 10)
	exigirTextos(t, deB, err, "la voz de OTRO negocio")
}

func TestMemoryApprovedRenderedTexts_Limite(t *testing.T) {
	ctx := context.Background()
	m := bandejaConHistorial(ctx, t)

	uno, err := m.ApprovedRenderedTexts(ctx, "tenant-a", 1)
	exigirTextos(t, uno, err, "la nueva de A")

	cero, err := m.ApprovedRenderedTexts(ctx, "tenant-a", 0)
	exigirTextos(t, cero, err)
}

// TestMemoryApprovedRenderedTexts_Cota: pedir más de la cuenta se recorta a
// MaxApprovedTexts en vez de traerse el historial entero del tenant.
func TestMemoryApprovedRenderedTexts_Cota(t *testing.T) {
	ctx := context.Background()
	m := intakes.NewMemoryStore()
	for i := 0; i < intakes.MaxApprovedTexts+10; i++ {
		sembrarAprobada(ctx, t, m, "t", idDePrueba(i), "texto",
			intakes.RevisionKindApproved, baseAprobadas.Add(time.Duration(i)*time.Second))
	}

	got, err := m.ApprovedRenderedTexts(ctx, "t", 10_000)
	if err != nil {
		t.Fatalf("ApprovedRenderedTexts: %v", err)
	}
	if len(got) != intakes.MaxApprovedTexts {
		t.Fatalf("salieron %d textos; la cota es %d", len(got), intakes.MaxApprovedTexts)
	}
}

// idDePrueba fabrica ids distintos y estables. El doble en memoria no valida el
// formato, así que basta con que no se repitan.
func idDePrueba(i int) string { return "id-" + strconv.Itoa(i) }
