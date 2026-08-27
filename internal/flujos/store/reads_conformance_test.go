// reads_conformance_test.go corre las MISMAS aserciones sobre los DOS adaptadores
// (memoria y Postgres) para las lecturas que alimentan el resumen del rescate
// (Plan 043 · Ola 3): ListIntakeItems y ListResults.
//
// Está escrito así por una razón concreta y no por gusto: los tests unitarios del
// resto del repo usan el repositorio en memoria, de modo que cualquier diferencia de
// comportamiento entre los dos adaptadores se convierte en un test verde que no está
// mirando lo que corre en producción. Una tabla de casos y dos implementaciones es la
// única forma de que "se comportan igual" sea una afirmación comprobada.
//
// El subtest de Postgres se SALTA solo (openTestDB) cuando no hay BD; el de memoria
// corre siempre.
package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// lecturaRepo es lo que necesita esta batería: sembrar y leer. Lo satisfacen
// *store.MemoryRepository y *store.PostgresRepository.
type lecturaRepo interface {
	UpsertIntake(ctx context.Context, o store.Intake) error
	GetOpenIntake(ctx context.Context, tenantID, contactID string) (store.Intake, bool, error)
	GetIntakeByEvent(ctx context.Context, tenantID, eventID string) (store.Intake, bool, error)
	ReplaceIntakeItems(ctx context.Context, intakeID string, items []store.IntakeItem) error
	ListIntakeItems(ctx context.Context, intakeID string) ([]store.IntakeItem, error)
	InsertResults(ctx context.Context, rows []store.SurveyResult) error
	ListResults(ctx context.Context, tenantID, contactID, flowID string) ([]store.SurveyResult, error)
}

// conAmbosAdaptadores ejecuta el caso contra memoria y contra Postgres.
//
// `nuevoEvento` es el padre que la 0054 exige declarar a toda fila nueva
// (D-043.21): contra Postgres crea un evento DE VERDAD (FK + CHECK reales); en
// memoria basta un id — el doble no impone integridad referencial, y esa asimetría
// es exactamente la razón de que esta batería exista.
func conAmbosAdaptadores(t *testing.T, caso func(t *testing.T, repo lecturaRepo, nuevoEvento func() string)) {
	t.Helper()
	t.Run("memoria", func(t *testing.T) {
		caso(t, store.NewMemoryRepository(), uuid.NewString)
	})
	t.Run("postgres", func(t *testing.T) {
		db := openTestDB(t)
		caso(t, store.NewPostgresRepository(db), func() string {
			_, eventID := seedTenantEventoPG(t, db, "cancelled")
			return eventID
		})
	})
}

// sembrarAbierta crea una solicitud "open" con identidades únicas y devuelve su id.
func sembrarAbierta(t *testing.T, repo lecturaRepo, nuevoEvento func() string) (intakeID, tenantID, contactID string) {
	t.Helper()
	sufijo := fmt.Sprintf("%d", time.Now().UnixNano())
	intakeID, tenantID, contactID = uuid.NewString(), "t-lect-"+sufijo, "c-lect-"+sufijo
	if err := repo.UpsertIntake(context.Background(), store.Intake{
		ID: intakeID, TenantID: tenantID, ContactID: contactID,
		SessionID: "s-lect", Status: "open", EventID: nuevoEvento(),
	}); err != nil {
		t.Fatalf("UpsertIntake: %v", err)
	}
	return intakeID, tenantID, contactID
}

// TestConformidad_ListIntakeItems_DevuelveElPedidoEnOrden: las líneas salen con todo
// lo copiado (label, precio, personalización) y EN EL ORDEN DEL CARRITO, que es lo
// que el resumen del rescate le va a leer al cliente.
func TestConformidad_ListIntakeItems_DevuelveElPedidoEnOrden(t *testing.T) {
	conAmbosAdaptadores(t, func(t *testing.T, repo lecturaRepo, nuevoEvento func() string) {
		ctx := context.Background()
		intakeID, _, _ := sembrarAbierta(t, repo, nuevoEvento)

		quiero := []store.IntakeItem{
			{SKU: "CAFE", Label: "Café", Customization: "sin azúcar", Qty: 2, UnitPrice: 2.5},
			{SKU: "TE", Label: "Té", Qty: 3, UnitPrice: 2.0},
			{SKU: "FLAN", Label: "Flan", Qty: 1, UnitPrice: 3.0},
		}
		if err := repo.ReplaceIntakeItems(ctx, intakeID, quiero); err != nil {
			t.Fatalf("ReplaceIntakeItems: %v", err)
		}

		got, err := repo.ListIntakeItems(ctx, intakeID)
		if err != nil {
			t.Fatalf("ListIntakeItems: %v", err)
		}
		if len(got) != len(quiero) {
			t.Fatalf("líneas: got %d, want %d\n  %+v", len(got), len(quiero), got)
		}
		for i, q := range quiero {
			if got[i].SKU != q.SKU || got[i].Label != q.Label || got[i].Qty != q.Qty ||
				got[i].UnitPrice != q.UnitPrice || got[i].Customization != q.Customization {
				t.Errorf("línea %d: got %+v, want %+v", i, got[i], q)
			}
			if got[i].IntakeID != intakeID {
				t.Errorf("línea %d: IntakeID=%q, want %q", i, got[i].IntakeID, intakeID)
			}
		}
	})
}

// TestConformidad_ListIntakeItems_VeElReemplazoYNoElHistorial: la lectura enseña el
// carrito de AHORA. Si acumulara, el resumen del rescate leería líneas que el cliente
// ya cambió.
func TestConformidad_ListIntakeItems_VeElReemplazoYNoElHistorial(t *testing.T) {
	conAmbosAdaptadores(t, func(t *testing.T, repo lecturaRepo, nuevoEvento func() string) {
		ctx := context.Background()
		intakeID, _, _ := sembrarAbierta(t, repo, nuevoEvento)

		if err := repo.ReplaceIntakeItems(ctx, intakeID, []store.IntakeItem{
			{SKU: "CAFE", Label: "Café", Qty: 2, UnitPrice: 2.5},
		}); err != nil {
			t.Fatalf("ReplaceIntakeItems (1.ª): %v", err)
		}
		if err := repo.ReplaceIntakeItems(ctx, intakeID, []store.IntakeItem{
			{SKU: "TE", Label: "Té", Qty: 1, UnitPrice: 2.0},
		}); err != nil {
			t.Fatalf("ReplaceIntakeItems (2.ª): %v", err)
		}

		got, err := repo.ListIntakeItems(ctx, intakeID)
		if err != nil {
			t.Fatalf("ListIntakeItems: %v", err)
		}
		if len(got) != 1 || got[0].SKU != "TE" {
			t.Fatalf("la lectura debe ver SOLO la última foto: %+v", got)
		}
	})
}

// TestConformidad_ListIntakeItems_SinLíneasEsVacíoSinError: una solicitud recién
// abierta (o una cuyo carrito se vació) NO es un error.
func TestConformidad_ListIntakeItems_SinLíneasEsVacíoSinError(t *testing.T) {
	conAmbosAdaptadores(t, func(t *testing.T, repo lecturaRepo, nuevoEvento func() string) {
		intakeID, _, _ := sembrarAbierta(t, repo, nuevoEvento)
		got, err := repo.ListIntakeItems(context.Background(), intakeID)
		if err != nil {
			t.Fatalf("ListIntakeItems: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("líneas: got %d, want 0 (%+v)", len(got), got)
		}
	})
}

// TestConformidad_ListIntakeItems_IdMalformadoEsError: el hueco típico —no comprobar
// el `found` de GetOpenIntake y pasar la cadena vacía— tiene que verse. Si esto
// devolviera «sin líneas», el resumen saldría vacío y nadie sabría por qué.
//
// Es además el caso donde los dos adaptadores se separarían solos si no se cuidara:
// Postgres rechaza el UUID inválido (22P02) y un mapa de cadenas no.
func TestConformidad_ListIntakeItems_IdMalformadoEsError(t *testing.T) {
	conAmbosAdaptadores(t, func(t *testing.T, repo lecturaRepo, nuevoEvento func() string) {
		for _, id := range []string{"", "no-soy-un-uuid"} {
			got, err := repo.ListIntakeItems(context.Background(), id)
			if err == nil {
				t.Fatalf("ListIntakeItems(%q) debe fallar, devolvió %+v", id, got)
			}
		}
	})
}

// TestConformidad_ListResults_DevuelveLoRespondidoEnOrdenYSoloLoSuyo: la lectura que
// hace posible el resumen de una encuesta rescatada, y el acotado que impide que
// enseñe lo de otro tenant, otro contacto u otro flujo (INV-8).
func TestConformidad_ListResults_DevuelveLoRespondidoEnOrdenYSoloLoSuyo(t *testing.T) {
	conAmbosAdaptadores(t, func(t *testing.T, repo lecturaRepo, nuevoEvento func() string) {
		ctx := context.Background()
		sufijo := fmt.Sprintf("%d", time.Now().UnixNano())
		tenant, contacto, flujo := "t-enc-"+sufijo, "c-enc-"+sufijo, "encuesta-"+sufijo

		// UN evento para toda la tanda: survey_results no tiene índice único por
		// event_id (una encuesta son N respuestas del mismo evento, D-043.21).
		evento := nuevoEvento()
		fila := func(tn, ct, fl, q, a string) store.SurveyResult {
			return store.SurveyResult{
				TenantID: tn, ContactID: ct, FlowID: fl, FlowVersion: 1,
				QuestionID: q, AnswerCode: a, EventID: evento,
			}
		}
		// Las tres primeras son suyas y van en este orden; las tres últimas son de
		// otro tenant, otro contacto y otro flujo, y no pueden aparecer.
		if err := repo.InsertResults(ctx, []store.SurveyResult{
			fila(tenant, contacto, flujo, "q1", "a"),
			fila(tenant, contacto, flujo, "q2", "b"),
			fila(tenant, contacto, flujo, "q1", "c"), // recorregida: la ÚLTIMA de q1
			fila("otro-"+tenant, contacto, flujo, "q1", "x"),
			fila(tenant, "otro-"+contacto, flujo, "q1", "y"),
			fila(tenant, contacto, "otro-"+flujo, "q1", "z"),
		}); err != nil {
			t.Fatalf("InsertResults: %v", err)
		}

		got, err := repo.ListResults(ctx, tenant, contacto, flujo)
		if err != nil {
			t.Fatalf("ListResults: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("respuestas: got %d, want 3\n  %+v", len(got), got)
		}
		quiero := []struct{ q, a string }{{"q1", "a"}, {"q2", "b"}, {"q1", "c"}}
		for i, w := range quiero {
			if got[i].QuestionID != w.q || got[i].AnswerCode != w.a {
				t.Errorf("respuesta %d: got (%s,%s), want (%s,%s)",
					i, got[i].QuestionID, got[i].AnswerCode, w.q, w.a)
			}
			if got[i].TenantID != tenant || got[i].ContactID != contacto || got[i].FlowID != flujo {
				t.Errorf("respuesta %d cruzada de otra identidad: %+v", i, got[i])
			}
			if got[i].FlowVersion != 1 {
				t.Errorf("respuesta %d sin la versión del flujo: %+v", i, got[i])
			}
			// created_at es lo ÚNICO con lo que se puede acotar «esta pasada» (la
			// tabla no tiene ni sesión ni evento). Un cero aquí volvería inútil ese
			// filtro sin que ningún test lo notara.
			if got[i].CreatedAt.IsZero() {
				t.Errorf("respuesta %d sin created_at: %+v", i, got[i])
			}
			if i > 0 && got[i].CreatedAt.Before(got[i-1].CreatedAt) {
				t.Errorf("respuesta %d fechada antes que la anterior: %v < %v",
					i, got[i].CreatedAt, got[i-1].CreatedAt)
			}
		}
	})
}

// TestConformidad_ListResults_SinRespuestasEsVacíoSinError: quien nunca respondió no
// es un error — es un resumen sin respuestas, que es justo lo que hay que poder
// distinguir de un fallo de lectura.
func TestConformidad_ListResults_SinRespuestasEsVacíoSinError(t *testing.T) {
	conAmbosAdaptadores(t, func(t *testing.T, repo lecturaRepo, nuevoEvento func() string) {
		got, err := repo.ListResults(context.Background(), "t-vacio", "c-vacio", "f-vacio")
		if err != nil {
			t.Fatalf("ListResults: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("respuestas: got %d, want 0 (%+v)", len(got), got)
		}
	})
}

// TestConformidad_GetIntakeByEvent_NoFiltraPorEstado es EL contrato del lector que
// añadió T4.0 (D-044.46), y el punto entero del hallazgo #24: la solicitud que cuelga
// de un evento se encuentra ESTÉ EN EL ESTADO QUE ESTÉ.
//
// El caso se monta con una fila en `pending_approval` —el estado en el que la etapa
// `draft` del pipeline deja su borrador— porque es exactamente la que GetOpenIntake NO
// VE. Las dos aserciones van juntas a propósito: sin la primera, "lo encuentra" no
// diría nada nuevo; con ella queda escrito que este lector ve lo que el otro no puede.
func TestConformidad_GetIntakeByEvent_NoFiltraPorEstado(t *testing.T) {
	conAmbosAdaptadores(t, func(t *testing.T, repo lecturaRepo, nuevoEvento func() string) {
		ctx := context.Background()
		sufijo := fmt.Sprintf("%d", time.Now().UnixNano())
		intakeID, tenantID, contactID := uuid.NewString(), "t-ev-"+sufijo, "c-ev-"+sufijo
		eventID := nuevoEvento()

		if err := repo.UpsertIntake(ctx, store.Intake{
			ID: intakeID, TenantID: tenantID, ContactID: contactID,
			SessionID: "s-ev", Status: "pending_approval", EventID: eventID,
		}); err != nil {
			t.Fatalf("UpsertIntake: %v", err)
		}

		// La ceguera que motivó la tarea, escrita como aserción.
		if _, found, err := repo.GetOpenIntake(ctx, tenantID, contactID); err != nil || found {
			t.Fatalf("GetOpenIntake NO puede ver una pending_approval: found=%v err=%v", found, err)
		}

		got, found, err := repo.GetIntakeByEvent(ctx, tenantID, eventID)
		if err != nil {
			t.Fatalf("GetIntakeByEvent: %v", err)
		}
		if !found {
			t.Fatalf("el evento %s tiene contenido durable y el lector no lo encontró", eventID)
		}
		if got.ID != intakeID || got.Status != "pending_approval" || got.EventID != eventID {
			t.Errorf("fila leída: got %+v, want id=%s status=pending_approval event=%s",
				got, intakeID, eventID)
		}
	})
}

// TestConformidad_GetIntakeByEvent_AcotaPorTenant: INV-8 también aquí. La solicitud de
// otro tenant es indistinguible de una inexistente, y sin esta aserción el `WHERE
// tenant_id` del SQL se podría borrar sin que nada se pusiera rojo.
func TestConformidad_GetIntakeByEvent_AcotaPorTenant(t *testing.T) {
	conAmbosAdaptadores(t, func(t *testing.T, repo lecturaRepo, nuevoEvento func() string) {
		ctx := context.Background()
		sufijo := fmt.Sprintf("%d", time.Now().UnixNano())
		tenantID := "t-ev-" + sufijo
		eventID := nuevoEvento()

		if err := repo.UpsertIntake(ctx, store.Intake{
			ID: uuid.NewString(), TenantID: tenantID, ContactID: "c-ev-" + sufijo,
			SessionID: "s-ev", Status: "open", EventID: eventID,
		}); err != nil {
			t.Fatalf("UpsertIntake: %v", err)
		}

		if _, found, err := repo.GetIntakeByEvent(ctx, tenantID+"-ajeno", eventID); err != nil || found {
			t.Fatalf("el evento de otro tenant no existe para éste: found=%v err=%v", found, err)
		}
	})
}

// TestConformidad_GetIntakeByEvent_SinContenido_NoEsUnError: un evento de `menu` o de
// `survey` no pare contenido durable, y preguntar por él es lo normal, no un fallo.
func TestConformidad_GetIntakeByEvent_SinContenido_NoEsUnError(t *testing.T) {
	conAmbosAdaptadores(t, func(t *testing.T, repo lecturaRepo, nuevoEvento func() string) {
		got, found, err := repo.GetIntakeByEvent(context.Background(),
			"t-ev-"+fmt.Sprintf("%d", time.Now().UnixNano()), nuevoEvento())
		if err != nil {
			t.Fatalf("un evento sin contenido no es un error: %v", err)
		}
		if found || got.ID != "" {
			t.Fatalf("found=%v got=%+v, want el cero-valor", found, got)
		}
	})
}
