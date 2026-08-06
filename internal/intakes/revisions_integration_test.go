package intakes_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// solicitudConRevisiones siembra UNA solicitud del tenant y devuelve el store, el
// tenant y su id, listos para colgarle revisiones.
func solicitudConRevisiones(t *testing.T) (*intakes.Postgres, string, string) {
	t.Helper()
	db := openTestDB(t)
	tenant := uuid.NewString()
	id := uuid.NewString()
	seedPG(t, db, tenant, []fixture{{id, intakes.StatusClosedLegacy, "sess-a", 1}})
	t.Cleanup(func() {
		// Las revisiones caen por el ON DELETE CASCADE de la solicitud, que limpia
		// seedPG; este borrado explícito solo cubre el caso de que ese contrato se
		// rompa, y si se rompe queremos enterarnos por otra vía, no por basura.
		if _, err := db.ExecContext(context.Background(),
			`DELETE FROM public.intake_revisions WHERE intake_id = $1`, id); err != nil {
			t.Logf("limpiando revisiones: %v", err)
		}
	})
	return intakes.NewPostgres(db), tenant, id
}

// dosRevisiones cuelga de la solicitud la revisión del cierre de carrito y una
// corrección posterior, y devuelve las dos ya escritas.
func dosRevisiones(t *testing.T, store *intakes.Postgres, id string) (primera, segunda intakes.Revision) {
	t.Helper()
	ctx := context.Background()

	payload, err := intakes.CartRevisionPayload(5000, []intakes.RevisionLine{
		{SKU: "emp-pino", Label: "Empanada de pino", Qty: 2, UnitPrice: 2500},
	})
	if err != nil {
		t.Fatalf("CartRevisionPayload: %v", err)
	}
	primera, err = store.InsertRevision(ctx, intakes.Revision{
		IntakeID: id, Kind: intakes.RevisionKindCart,
		Payload: payload, CreatedBy: intakes.RevisionBySystem,
	})
	if err != nil {
		t.Fatalf("InsertRevision (cart): %v", err)
	}
	segunda, err = store.InsertRevision(ctx, intakes.Revision{
		IntakeID: id, Kind: intakes.RevisionKindCorrected,
		Payload:      json.RawMessage(`{"version":1,"total":6000}`),
		RenderedText: "Te dejo el presupuesto corregido",
		CreatedBy:    intakes.RevisionByOwner,
	})
	if err != nil {
		t.Fatalf("InsertRevision (corrected): %v", err)
	}
	return primera, segunda
}

// TestPostgres_InsertRevision_NumeraSecuencial: la numeración es POR SOLICITUD y
// arranca en 1; la fecha la pone la BD y el texto ausente se guarda NULL.
func TestPostgres_InsertRevision_NumeraSecuencial(t *testing.T) {
	store, _, id := solicitudConRevisiones(t)

	primera, segunda := dosRevisiones(t, store, id)

	if primera.RevisionNo != 1 {
		t.Fatalf("revision_no de la primera=%d, quiero 1", primera.RevisionNo)
	}
	if segunda.RevisionNo != 2 {
		t.Fatalf("revision_no de la segunda=%d, quiero 2", segunda.RevisionNo)
	}
	if primera.CreatedAt.IsZero() {
		t.Fatal("la revisión debería venir fechada por la BD")
	}
	if primera.RenderedText != "" {
		t.Fatalf("rendered_text=%q; sin texto se guarda NULL y se lee vacío", primera.RenderedText)
	}
}

// TestPostgres_Get_DevuelveLasRevisiones: lo escrito sale por Get, que es lo que
// hace la revisión CONSULTABLE por GET /api/v1/intakes/{id}.
func TestPostgres_Get_DevuelveLasRevisiones(t *testing.T) {
	store, tenant, id := solicitudConRevisiones(t)
	dosRevisiones(t, store, id)

	detail, err := store.Get(context.Background(), tenant, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(detail.Revisions) != 2 {
		t.Fatalf("revisiones en el detalle=%d, quiero 2", len(detail.Revisions))
	}
	if detail.Revisions[0].RevisionNo != 1 || detail.Revisions[1].RevisionNo != 2 {
		t.Fatalf("las revisiones no salen en orden: %+v", detail.Revisions)
	}
	if detail.Revisions[0].Kind != intakes.RevisionKindCart ||
		detail.Revisions[0].CreatedBy != intakes.RevisionBySystem {
		t.Fatalf("revisión 1 mal leída: %+v", detail.Revisions[0])
	}
	if detail.Revisions[1].RenderedText != "Te dejo el presupuesto corregido" {
		t.Fatalf("rendered_text de la revisión 2: %q", detail.Revisions[1].RenderedText)
	}
	var payload struct {
		Version int     `json:"version"`
		Total   float64 `json:"total"`
	}
	if err := json.Unmarshal(detail.Revisions[0].Payload, &payload); err != nil {
		t.Fatalf("el payload debería viajar crudo y ser JSON (%s): %v", detail.Revisions[0].Payload, err)
	}
	if payload.Version != intakes.RevisionPayloadVersion || payload.Total != 5000 {
		t.Fatalf("payload leído=%s", detail.Revisions[0].Payload)
	}
}

// TestPostgres_InsertRevision_KindFueraDelCatalogo: el CHECK de la tabla es el que
// mantiene cerrado el conjunto de clases. Un `kind` inventado NO entra —y que
// falle aquí es la garantía de que añadir una clase exige migración.
func TestPostgres_InsertRevision_KindFueraDelCatalogo(t *testing.T) {
	store, _, id := solicitudConRevisiones(t)

	_, err := store.InsertRevision(context.Background(), intakes.Revision{
		IntakeID: id, Kind: "inventado",
		Payload: json.RawMessage(`{"version":1}`), CreatedBy: intakes.RevisionBySystem,
	})
	if err == nil {
		t.Fatal("un kind fuera del CHECK debería fallar, no colarse en la tabla")
	}
}

// TestPostgres_InsertRevision_SolicitudInexistente: sin solicitud no hay revisión
// (la FK). El id que ni siquiera es UUID se corta antes, como en Get.
func TestPostgres_InsertRevision_SolicitudInexistente(t *testing.T) {
	store, _, _ := solicitudConRevisiones(t)
	ctx := context.Background()

	if _, err := store.InsertRevision(ctx, intakes.Revision{
		IntakeID: uuid.NewString(), Kind: intakes.RevisionKindCart,
		Payload: json.RawMessage(`{"version":1}`),
	}); err == nil {
		t.Fatal("una revisión de una solicitud inexistente debería fallar por la FK")
	}

	if _, err := store.InsertRevision(ctx, intakes.Revision{
		IntakeID: "no-soy-un-uuid", Kind: intakes.RevisionKindCart,
		Payload: json.RawMessage(`{"version":1}`),
	}); err == nil {
		t.Fatal("un intake_id que no es UUID debería cortarse sin ir a la BD")
	}
}
