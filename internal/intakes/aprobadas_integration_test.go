// aprobadas_integration_test.go — la consulta del HISTORIAL APROBADO por tenant
// (Plan 044 · Ola 5 · T5.1, D-044.11), contra Postgres de verdad.
//
// Corre contra la base porque lo que hay que comprobar solo existe ahí: el JOIN a
// `intakes` —que es lo ÚNICO que acota la consulta al tenant, porque
// `intake_revisions` no tiene `tenant_id`— y el ORDEN, que lo decide el planificador
// si el ORDER BY no es suficiente. Un doble en memoria no puede demostrar ninguna de
// las dos.
package intakes_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// revisiónSembrada es una fila de public.intake_revisions escrita por SQL DIRECTO.
//
// Se siembra a mano y no por InsertRevision a propósito: `created_at` lo pone el
// DEFAULT de la BD, y sin poder elegirlo no se puede probar el orden, que es la mitad
// del contrato de esta consulta.
type revisiónSembrada struct {
	intakeID string
	no       int
	kind     string
	texto    string
	cuando   time.Time
}

// TestApprovedRenderedTextsPG es el contrato entero de la consulta, en un solo
// escenario: dos tenants, cuatro clases de revisión y cinco textos.
func TestApprovedRenderedTextsPG(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	tenant := uuid.NewString()
	otro := uuid.NewString()
	mío1, mío2 := uuid.NewString(), uuid.NewString()
	ajeno := uuid.NewString()

	seedPG(t, db, tenant, []fixture{{mío1, intakes.StatusConfirmed, "sess-a", 1}, {mío2, intakes.StatusConfirmed, "sess-a", 2}})
	seedPG(t, db, otro, []fixture{{ajeno, intakes.StatusConfirmed, "sess-z", 3}})

	base := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	filas := []revisiónSembrada{
		// Las tres del tenant, de la más antigua a la más nueva.
		{mío1, 1, intakes.RevisionKindApproved, "la más antigua", base.Add(-3 * time.Hour)},
		{mío2, 1, intakes.RevisionKindApproved, "la del medio", base.Add(-2 * time.Hour)},
		{mío2, 2, intakes.RevisionKindApproved, "la más reciente", base.Add(-1 * time.Hour)},
		// Ruido que NO debe salir: otra clase, texto vacío, texto nulo…
		{mío1, 2, intakes.RevisionKindCorrected, "una corrección con texto", base},
		{mío1, 3, intakes.RevisionKindApproved, "", base},
		{mío1, 4, intakes.RevisionKindApproved, "   \n  ", base},
		// …y la de OTRO tenant, que es lo que el JOIN tiene que cortar (INV-8).
		{ajeno, 1, intakes.RevisionKindApproved, "la voz de otro negocio", base},
	}
	for _, f := range filas {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO public.intake_revisions (intake_id, revision_no, kind, payload, rendered_text, created_by, created_at)
			VALUES ($1, $2, $3, '{"version":1}'::jsonb, $4, 'owner', $5)
		`, f.intakeID, f.no, f.kind, f.texto, f.cuando); err != nil {
			t.Fatalf("sembrando la revisión %s/%d: %v", f.intakeID, f.no, err)
		}
	}

	store := intakes.NewPostgres(db)

	t.Run("orden, filtro por clase y textos en blanco fuera", func(t *testing.T) {
		got, err := store.ApprovedRenderedTexts(ctx, tenant, 10)
		exigirTextos(t, got, err, "la más reciente", "la del medio", "la más antigua")
	})

	t.Run("el LIMIT recorta por la cola", func(t *testing.T) {
		got, err := store.ApprovedRenderedTexts(ctx, tenant, 2)
		exigirTextos(t, got, err, "la más reciente", "la del medio")
	})

	t.Run("limit <= 0 no toca la BD", func(t *testing.T) {
		got, err := store.ApprovedRenderedTexts(ctx, tenant, 0)
		exigirTextos(t, got, err)
	})

	// 🔴 INV-8, y es lo que SOLO se puede comprobar contra Postgres: lo único que ata
	// una revisión a su tenant es el JOIN, porque `intake_revisions` no tiene
	// `tenant_id`. El control —que el otro tenant SÍ ve lo suyo— es lo que impide que
	// este assert pase con una consulta que no devolviera nada.
	t.Run("el otro tenant ve LO SUYO y solo lo suyo", func(t *testing.T) {
		got, err := store.ApprovedRenderedTexts(ctx, otro, 10)
		exigirTextos(t, got, err, "la voz de otro negocio")
	})

	t.Run("un tenant sin historial devuelve vacío, no error", func(t *testing.T) {
		got, err := store.ApprovedRenderedTexts(ctx, uuid.NewString(), 5)
		exigirTextos(t, got, err)
	})
}

// TestApprovedRenderedTextsPG_ParidadConElDobleEnMemoria: el MemoryStore tiene que
// ordenar y filtrar igual que la consulta real, porque los tests de `quotetext` corren
// contra él y solo dicen algo verdadero sobre producción si las dos coinciden.
func TestApprovedRenderedTextsPG_ParidadConElDobleEnMemoria(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	tenant := uuid.NewString()
	id1, id2 := uuid.NewString(), uuid.NewString()
	seedPG(t, db, tenant, []fixture{{id1, intakes.StatusConfirmed, "sess-a", 1}, {id2, intakes.StatusConfirmed, "sess-a", 2}})

	base := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	mem := intakes.NewMemoryStore()
	mem.Add(tenant, intakes.Intake{ID: id1, Status: intakes.StatusConfirmed})
	mem.Add(tenant, intakes.Intake{ID: id2, Status: intakes.StatusConfirmed})

	filas := []revisiónSembrada{
		{id1, 1, intakes.RevisionKindApproved, "uno", base.Add(-2 * time.Hour)},
		{id2, 1, intakes.RevisionKindCorrected, "no cuenta", base.Add(-90 * time.Minute)},
		{id2, 2, intakes.RevisionKindApproved, "dos", base.Add(-1 * time.Hour)},
	}
	for _, f := range filas {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO public.intake_revisions (intake_id, revision_no, kind, payload, rendered_text, created_by, created_at)
			VALUES ($1, $2, $3, '{"version":1}'::jsonb, $4, 'owner', $5)
		`, f.intakeID, f.no, f.kind, f.texto, f.cuando); err != nil {
			t.Fatalf("sembrando en PG: %v", err)
		}
		if _, err := mem.InsertRevision(ctx, intakes.Revision{
			IntakeID: f.intakeID, Kind: f.kind,
			Payload:      []byte(`{"version":1}`),
			RenderedText: f.texto, CreatedBy: intakes.RevisionByOwner, CreatedAt: f.cuando,
		}); err != nil {
			t.Fatalf("sembrando en memoria: %v", err)
		}
	}

	dePG, err := intakes.NewPostgres(db).ApprovedRenderedTexts(ctx, tenant, 5)
	exigirTextos(t, dePG, err, "dos", "uno")

	deMem, err := mem.ApprovedRenderedTexts(ctx, tenant, 5)
	exigirTextos(t, deMem, err, dePG...)
}
