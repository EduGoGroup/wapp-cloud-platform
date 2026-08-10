// projection_integration_test.go — T4.5.3 contra Postgres REAL (Plan 043 · Ola
// 4.5, D-043.21): la solicitud DECLARA A SU PADRE al nacer, sin sembrar ligaduras
// a mano. Lo que un doble no puede afirmar y aquí se afirma:
//
//   - la fila de `intakes` nace con el `event_id` de la EffectMeta y la acepta la
//     cadena real FK + CHECK + índice único parcial de la 0054;
//   - el REUSO de una "open" LEGADA (event_id NULL, pre-0054) la estampa;
//   - la MUTACIÓN del criterio: sin la escritura del proyector (meta sin
//     EventID), el INSERT revienta contra el CHECK — el huérfano es imposible,
//     no improbable.
package cart_test

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
)

// Los tres puertos que projectOpenLines NO toca (revisiones, envío, datos del
// comprador) se satisfacen con dobles inertes: si el camino del item_added los
// llamara, el test tiene que enterarse por el Fatal, no por un efecto silencioso.
type revisionesInertes struct{ t *testing.T }

func (r revisionesInertes) InsertRevision(context.Context, intakes.Revision) (intakes.Revision, error) {
	r.t.Fatal("projectOpenLines no escribe revisiones")
	return intakes.Revision{}, nil
}

type envioInerte struct{ t *testing.T }

func (e envioInerte) EnsureShippingLine(context.Context, string, string, intakes.ShippingPolicy) error {
	e.t.Fatal("projectOpenLines no asegura la línea de envío")
	return nil
}

type buyerInerte struct{ t *testing.T }

func (b buyerInerte) PutBuyerField(context.Context, string, string, string) error {
	b.t.Fatal("projectOpenLines no escribe datos del comprador")
	return nil
}

// abrirBD abre wapp_test con el mismo contrato de salto del resto del repo.
func abrirBD(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("WAPP_TEST_DB_DSN")
	if dsn == "" {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatal("WAPP_TEST_DB_DSN no definido pero WAPP_TEST_REQUIRE_DB exige BD")
		}
		t.Skip("WAPP_TEST_DB_DSN no definido: se omiten los tests de integración con BD")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, postgres.Config{DSN: dsn})
	if err != nil {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("BD no disponible (%v) pero WAPP_TEST_REQUIRE_DB exige BD", err)
		}
		t.Skipf("BD no disponible (%v): se omiten", err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Logf("cerrando BD: %v", cerr)
		}
	})
	if _, err := migrations.Migrate(ctx, db); err != nil {
		t.Fatalf("migrando: %v", err)
	}
	return db
}

// sembrarEvento crea tenant real + evento `open` de tipo cart y devuelve sus ids
// (prefijo t45i-; limpieza por t.Cleanup en orden LIFO: las solicitudes que nazcan
// después se limpian antes, y el CASCADE del tenant se lleva el evento).
func sembrarEvento(t *testing.T, db *sql.DB) (tenantID, eventID string) {
	t.Helper()
	tenantID = uuid.NewString()
	if _, err := db.Exec(`
		INSERT INTO public.tenants (id, slug, display_name)
		VALUES ($1, $2, 'Ola 4.5 cart')
	`, tenantID, "t45i-"+tenantID); err != nil {
		t.Fatalf("sembrando tenant: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec(`DELETE FROM public.intakes WHERE tenant_id = $1`, tenantID); err != nil {
			t.Logf("limpiando solicitudes: %v", err)
		}
		if _, err := db.Exec(`DELETE FROM public.tenants WHERE id = $1`, tenantID); err != nil {
			t.Logf("limpiando tenant: %v", err)
		}
	})
	if err := db.QueryRow(`
		INSERT INTO public.conversation_events
			(tenant_id, session_id, contact_id, kind, history_id, status, flow_id, flow_version)
		VALUES ($1, 't45i-sess-' || gen_random_uuid(), gen_random_uuid(), 'cart',
		        't45i-' || gen_random_uuid(), 'open', 'flujo-w45', 1)
		RETURNING id::text
	`, tenantID).Scan(&eventID); err != nil {
		t.Fatalf("sembrando evento: %v", err)
	}
	return tenantID, eventID
}

func proyector(t *testing.T, db *sql.DB) *cart.Projector {
	t.Helper()
	return cart.NewProjector(store.NewPostgresRepository(db),
		revisionesInertes{t}, envioInerte{t}, buyerInerte{t})
}

// itemAdded arma el efecto con la foto mínima de una línea, como lo publica el
// módulo (snapshot bajo la clave `items`).
func itemAdded() modules.Effect {
	return modules.Effect{
		Name: cart.EffectItemAdded,
		Payload: map[string]any{
			"items": []any{map[string]any{"sku": "torta-v1", "label": "Torta", "qty": 1, "unit_price": 18000.0}},
		},
	}
}

// TestPG_Projection_LaSolicitudNaceDeclarandoASuPadre: al agregar la primera
// línea, la fila de intakes nace con el event_id de la meta — sin ningún UPDATE
// posterior ni siembra manual (T4.5.3).
func TestPG_Projection_LaSolicitudNaceDeclarandoASuPadre(t *testing.T) {
	db := abrirBD(t)
	tenant, evento := sembrarEvento(t, db)
	contacto := uuid.NewString()
	meta := modules.EffectMeta{
		TenantID: tenant, ContactID: contacto, SessionID: "t45i-sess",
		FlowID: "flujo-w45", FlowVersion: 1, EventID: evento,
	}

	if err := proyector(t, db).Project(context.Background(), meta, itemAdded()); err != nil {
		t.Fatalf("Project(item_added): %v", err)
	}

	var eventID sql.NullString
	if err := db.QueryRow(`
		SELECT event_id::text FROM public.intakes
		WHERE tenant_id = $1 AND contact_id = $2 AND status = 'open'
	`, tenant, contacto).Scan(&eventID); err != nil {
		t.Fatalf("leyendo la solicitud nacida: %v", err)
	}
	if !eventID.Valid || eventID.String != evento {
		t.Fatalf("event_id=%v, quiero %s: el hijo declara a su padre AL NACER", eventID, evento)
	}
}

// TestPG_Projection_ElReusoEstampaElLegado: una "open" pre-0054 (event_id NULL,
// sembrada saltándose el CHECK NOT VALID como una fila vieja real) queda ESTAMPADA
// al primer item_added que la reusa — la única reparación legítima del huérfano.
func TestPG_Projection_ElReusoEstampaElLegado(t *testing.T) {
	db := abrirBD(t)
	tenant, evento := sembrarEvento(t, db)
	contacto := uuid.NewString()

	// La fila LEGADA: anterior al CHECK, así que para sembrarla como existía hay
	// que retirar el CHECK un instante (es exactamente lo que "NOT VALID" tolera
	// en reposo: filas viejas sin ligadura). Se restaura de inmediato.
	legadoID := uuid.NewString()
	if _, err := db.Exec(`ALTER TABLE public.intakes DROP CONSTRAINT intakes_event_id_required_chk`); err != nil {
		t.Fatalf("retirando el CHECK: %v", err)
	}
	_, insErr := db.Exec(`
		INSERT INTO public.intakes (id, tenant_id, contact_id, session_id, status, total, created_at, updated_at)
		VALUES ($1, $2, $3, 't45i-sess', 'open', 0, now(), now())
	`, legadoID, tenant, contacto)
	if _, err := db.Exec(`
		ALTER TABLE public.intakes ADD CONSTRAINT intakes_event_id_required_chk
		CHECK (event_id IS NOT NULL) NOT VALID`); err != nil {
		t.Fatalf("restaurando el CHECK: %v", err)
	}
	if insErr != nil {
		t.Fatalf("sembrando la fila legada: %v", insErr)
	}

	meta := modules.EffectMeta{
		TenantID: tenant, ContactID: contacto, SessionID: "t45i-sess",
		FlowID: "flujo-w45", FlowVersion: 1, EventID: evento,
	}
	if err := proyector(t, db).Project(context.Background(), meta, itemAdded()); err != nil {
		t.Fatalf("Project(item_added) sobre el legado: %v", err)
	}

	var eventID sql.NullString
	if err := db.QueryRow(`SELECT event_id::text FROM public.intakes WHERE id = $1`, legadoID).Scan(&eventID); err != nil {
		t.Fatalf("leyendo el legado: %v", err)
	}
	if !eventID.Valid || eventID.String != evento {
		t.Fatalf("event_id=%v, quiero %s: el reuso estampa el NULL legado", eventID, evento)
	}
}

// TestPG_Projection_SinEventoElHuerfanoEsImposible es la MUTACIÓN del criterio de
// T4.5.3 hecha test: si el proyector no escribiera la ligadura (meta sin
// EventID), el INSERT no degrada a una fila huérfana — REVIENTA contra el CHECK
// de la 0054. No se inventa ligadura (NULL + log) y la base hace el resto.
func TestPG_Projection_SinEventoElHuerfanoEsImposible(t *testing.T) {
	db := abrirBD(t)
	tenant, _ := sembrarEvento(t, db)
	meta := modules.EffectMeta{
		TenantID: tenant, ContactID: uuid.NewString(), SessionID: "t45i-sess",
		FlowID: "flujo-w45", FlowVersion: 1, // EventID vacío A PROPÓSITO
	}

	if err := proyector(t, db).Project(context.Background(), meta, itemAdded()); err == nil {
		t.Fatal("un item_added sin evento creó una solicitud huérfana: el CHECK de la 0054 no está haciendo su trabajo")
	}
}
