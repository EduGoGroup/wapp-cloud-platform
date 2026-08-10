package intakes_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
)

const dsnEnv = "WAPP_TEST_DB_DSN"

// openTestDB abre la BD de integración y aplica el esquema. Sin DSN se salta (el
// mismo contrato que el resto de los *_integration_test.go del repo).
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("%s no definido pero WAPP_TEST_REQUIRE_DB exige BD", dsnEnv)
		}
		t.Skipf("%s no definido: se omiten los tests de integración con BD", dsnEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, postgres.Config{DSN: dsn})
	if err != nil {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("BD no disponible en %s (%v) pero WAPP_TEST_REQUIRE_DB exige BD", dsnEnv, err)
		}
		t.Skipf("BD no disponible en %s (%v): se omiten", dsnEnv, err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Logf("cerrando BD de test: %v", cerr)
		}
	})
	if _, err := migrations.Migrate(ctx, db); err != nil {
		t.Fatalf("migrando BD de test: %v", err)
	}
	return db
}

// fixture es una solicitud sembrada directamente en SQL: el test escribe como
// escribe el módulo cart (incluido el `closed` legado) y lee por el store nuevo.
type fixture struct {
	id      string
	status  string
	session string
	day     int
}

// ensureTenantPG garantiza la fila de public.tenants para el UUID dado (Ola 4.5):
// desde la 0054 toda solicitud nueva declara a su padre (intakes.event_id, CHECK
// NOT VALID) y el padre —conversation_events— tiene FK a tenants, así que sembrar
// una solicitud exige la cadena entera tenant→evento→solicitud. Idempotente
// (ON CONFLICT DO NOTHING); limpia el tenant al terminar (el CASCADE se lleva sus
// eventos, DESPUÉS de que los cleanups posteriores —LIFO— borraran las solicitudes
// que los referencian).
func ensureTenantPG(t *testing.T, db *sql.DB, tenantID string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO public.tenants (id, slug, display_name)
		VALUES ($1, $2, 'Ola 4.5')
		ON CONFLICT (id) DO NOTHING
	`, tenantID, "t45i-"+tenantID); err != nil {
		t.Fatalf("asegurando tenant %s: %v", tenantID, err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(),
			`DELETE FROM public.tenants WHERE id = $1`, tenantID); err != nil {
			t.Logf("limpiando tenant %s: %v", tenantID, err)
		}
	})
}

// seedEventoPG crea un evento conversacional del tenant en el estado pedido y
// devuelve su id: el PADRE que toda solicitud nueva tiene que declarar (D-043.21).
// Terminal ⇒ closed_at sellado, como escribe transitionSQL. La sesión/el contacto
// son únicos por evento para no chocar con el índice «uno vivo por tipo» (E-2).
// Lo limpia el CASCADE del tenant (ensureTenantPG).
func seedEventoPG(t *testing.T, db *sql.DB, tenantID, status string) string {
	t.Helper()
	var id string
	if err := db.QueryRowContext(context.Background(), `
		INSERT INTO public.conversation_events
			(tenant_id, session_id, contact_id, kind, history_id, status, flow_id, flow_version, closed_at)
		VALUES ($1, 't45i-sess-' || gen_random_uuid(), gen_random_uuid(), 'cart',
		        't45i-' || gen_random_uuid(), $2, 'flujo-w45', 1,
		        CASE WHEN $2 = 'open' THEN NULL ELSE now() END)
		RETURNING id::text
	`, tenantID, status).Scan(&id); err != nil {
		t.Fatalf("sembrando evento (%s): %v", status, err)
	}
	return id
}

// seedPG inserta las solicitudes del tenant y devuelve sus ids. Limpia al terminar.
//
// Desde la 0054 cada fila nace declarando a su padre: un evento propio (uno por
// solicitud — índice único parcial intakes_event_id_uidx), TERMINAL (`cancelled`)
// para que la guarda `live_event` del descarte no proteja lo que estos fixtures no
// quieren proteger. El tenant tiene que ser un UUID: la cadena de FKs
// tenant→evento lo exige (ensureTenantPG).
func seedPG(t *testing.T, db *sql.DB, tenantID string, rows []fixture) {
	t.Helper()
	ctx := context.Background()
	ensureTenantPG(t, db, tenantID)
	for _, r := range rows {
		eventID := seedEventoPG(t, db, tenantID, "cancelled")
		if _, err := db.ExecContext(ctx, `
			INSERT INTO public.intakes (id, tenant_id, contact_id, session_id, status, total, event_id, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)
		`, r.id, tenantID, "contacto-opaco-"+r.id[:8], r.session, r.status, 18000, eventID,
			time.Date(2026, 8, r.day, 12, 0, 0, 0, time.UTC)); err != nil {
			t.Fatalf("sembrando solicitud %s: %v", r.id, err)
		}
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(), `
			DELETE FROM public.intake_items WHERE intake_id IN (
				SELECT id FROM public.intakes WHERE tenant_id = $1)
		`, tenantID); err != nil {
			t.Logf("limpiando líneas: %v", err)
		}
		if _, err := db.ExecContext(context.Background(),
			`DELETE FROM public.intakes WHERE tenant_id = $1`, tenantID); err != nil {
			t.Logf("limpiando solicitudes: %v", err)
		}
	})
}

// bandeja siembra cuatro solicitudes del mismo tenant repartidas en fechas,
// estados y sesiones, y devuelve el store, el tenant y sus ids (del más viejo al
// más nuevo). Es el fixture compartido de los tests de listado.
func bandeja(t *testing.T) (*intakes.Postgres, string, [4]string) {
	t.Helper()
	db := openTestDB(t)
	tenant := uuid.NewString()
	ids := [4]string{uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()}
	seedPG(t, db, tenant, []fixture{
		{ids[0], intakes.StatusClosedLegacy, "sess-a", 1},
		{ids[1], intakes.StatusOpen, "sess-a", 2},
		{ids[2], intakes.StatusCancelled, "sess-b", 3},
		{ids[3], intakes.StatusConfirmed, "sess-a", 4},
	})
	return intakes.NewPostgres(db), tenant, ids
}

// TestPostgres_List_SinFiltros: todo el tenant, más recientes primero.
func TestPostgres_List_SinFiltros(t *testing.T) {
	store, tenant, id := bandeja(t)

	got, total, err := store.List(context.Background(), tenant, intakes.Filter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 4 || len(got) != 4 {
		t.Fatalf("total=%d filas=%d, quiero 4 y 4", total, len(got))
	}
	if !slices.Equal(ids(got), []string{id[3], id[2], id[1], id[0]}) {
		t.Fatalf("orden=%v; quiero descendente por created_at", ids(got))
	}
}

// TestPostgres_List_StatusAlcanzaElLegado: filtrar por `confirmed` devuelve
// también las filas que el módulo cart escribió como `closed`. Es la prueba de
// que no hace falta migrar el dato histórico.
func TestPostgres_List_StatusAlcanzaElLegado(t *testing.T) {
	store, tenant, id := bandeja(t)

	got, total, err := store.List(context.Background(), tenant,
		intakes.Filter{Status: intakes.StatusConfirmed})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 2 || !slices.Equal(ids(got), []string{id[3], id[0]}) {
		t.Fatalf("total=%d ids=%v; quiero la confirmed y la closed legada", total, ids(got))
	}
	for _, in := range got {
		if in.Status != intakes.StatusConfirmed {
			t.Fatalf("status=%q; el store normaliza al leer", in.Status)
		}
	}
}

// TestPostgres_List_FiltrosCombinados: rango de fechas [From, To) + sesión.
func TestPostgres_List_FiltrosCombinados(t *testing.T) {
	store, tenant, id := bandeja(t)

	got, total, err := store.List(context.Background(), tenant, intakes.Filter{
		From:      time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		To:        time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC), // exclusivo
		SessionID: "sess-a",
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 2 || !slices.Equal(ids(got), []string{id[1], id[0]}) {
		t.Fatalf("total=%d ids=%v; quiero las dos de sess-a del 1 y el 2", total, ids(got))
	}
}

// TestPostgres_List_TotalCuentaElFiltro: `total` es el de las coincidencias, no
// el de la página — si divergieran, el paginador mentiría.
func TestPostgres_List_TotalCuentaElFiltro(t *testing.T) {
	store, tenant, id := bandeja(t)

	got, total, err := store.List(context.Background(), tenant, intakes.Filter{Page: 2, PageSize: 3})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 4 || !slices.Equal(ids(got), []string{id[0]}) {
		t.Fatalf("total=%d ids=%v; quiero total=4 con la fila suelta de la página 2", total, ids(got))
	}
}

// TestPostgres_List_AisladoPorTenant: el WHERE por tenant no es opcional (INV-8).
func TestPostgres_List_AisladoPorTenant(t *testing.T) {
	store, _, _ := bandeja(t)

	got, total, err := store.List(context.Background(), uuid.NewString(), intakes.Filter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 0 || len(got) != 0 {
		t.Fatalf("total=%d filas=%d; un tenant ajeno no ve nada", total, len(got))
	}
}

// TestPostgres_Get_LíneasYAislamiento valida el detalle contra la tabla real.
func TestPostgres_Get_LíneasYAislamiento(t *testing.T) {
	db := openTestDB(t)
	store := intakes.NewPostgres(db)
	tenant := uuid.NewString()
	ctx := context.Background()

	id := uuid.NewString()
	seedPG(t, db, tenant, []fixture{{id, intakes.StatusClosedLegacy, "sess-a", 1}})
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.intake_items (intake_id, sku, label, qty, unit_price)
		VALUES ($1, 'torta-v1', 'Torta 10-12 porciones', 1, 18000),
		       ($1, '_shipping', 'Envío — Providencia', 1, 3000)
	`, id); err != nil {
		t.Fatalf("sembrando líneas: %v", err)
	}

	detail, err := store.Get(ctx, tenant, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if detail.Status != intakes.StatusConfirmed {
		t.Fatalf("status=%q; el `closed` de BD se lee normalizado", detail.Status)
	}
	if len(detail.Items) != 2 || detail.Items[0].SKU != "torta-v1" {
		t.Fatalf("items=%+v; quiero las dos líneas en orden de alta", detail.Items)
	}
	if detail.ContactID == "" {
		t.Fatal("contact_id vacío: el opaco viaja tal cual desde BD")
	}

	if _, err := store.Get(ctx, uuid.NewString(), id); !errors.Is(err, intakes.ErrNotFound) {
		t.Fatalf("cross-tenant err=%v, quiero ErrNotFound", err)
	}
	if _, err := store.Get(ctx, tenant, uuid.NewString()); !errors.Is(err, intakes.ErrNotFound) {
		t.Fatalf("id inexistente err=%v, quiero ErrNotFound", err)
	}
	// Un id que ni siquiera es UUID no puede reventar la consulta (la columna es
	// UUID): es un 404 como cualquier otro.
	if _, err := store.Get(ctx, tenant, "no-soy-un-uuid"); !errors.Is(err, intakes.ErrNotFound) {
		t.Fatalf("id malformado err=%v, quiero ErrNotFound", err)
	}
}

// TestPostgres_Customization_RoundTrip valida contra la COLUMNA REAL (migración
// 0045, T4.1b) los dos caminos de lectura de una línea: el detalle (Get) y el que
// alimenta export y summary (ListDetails). Ninguno de los dos comparte SQL con el
// otro, así que llevarla en uno no dice nada del otro.
//
// Y prueba la no-regresión que el plan exige: la línea sembrada SIN la columna
// —como todas las que ya están en la base— se lee con la cadena vacía del DEFAULT,
// no con NULL ni con la personalización de la línea vecina.
func TestPostgres_Customization_RoundTrip(t *testing.T) {
	db := openTestDB(t)
	store := intakes.NewPostgres(db)
	tenant := uuid.NewString()
	ctx := context.Background()

	id := uuid.NewString()
	seedPG(t, db, tenant, []fixture{{id, intakes.StatusClosedLegacy, "sess-a", 1}})
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.intake_items (intake_id, sku, label, customization, qty, unit_price, added_at)
		VALUES ($1, 'hotdog', 'Hot dog', 'sin cebolla', 2, 2500, now())
	`, id); err != nil {
		t.Fatalf("sembrando la línea personalizada: %v", err)
	}
	// La segunda se inserta SIN nombrar la columna: es literalmente la sentencia
	// que escribía el repo antes de esta tarea.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.intake_items (intake_id, sku, label, qty, unit_price, added_at)
		VALUES ($1, 'bebida', 'Bebida', 1, 1000, now() + interval '1 second')
	`, id); err != nil {
		t.Fatalf("sembrando la línea heredada: %v", err)
	}

	detail, err := store.Get(ctx, tenant, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(detail.Items) != 2 {
		t.Fatalf("items=%d, quiero 2", len(detail.Items))
	}
	if detail.Items[0].Customization != "sin cebolla" || detail.Items[1].Customization != "" {
		t.Fatalf("Get: customization=%q/%q; quiero 'sin cebolla' y vacío",
			detail.Items[0].Customization, detail.Items[1].Customization)
	}

	got, err := store.ListDetails(ctx, tenant, intakes.Filter{}, intakes.MaxExportIntakes)
	if err != nil {
		t.Fatalf("ListDetails: %v", err)
	}
	if len(got) != 1 || len(got[0].Items) != 2 {
		t.Fatalf("ListDetails devolvió %d solicitudes; quiero 1 con 2 líneas", len(got))
	}
	if got[0].Items[0].Customization != "sin cebolla" || got[0].Items[1].Customization != "" {
		t.Fatalf("ListDetails: customization=%q/%q; quiero 'sin cebolla' y vacío",
			got[0].Items[0].Customization, got[0].Items[1].Customization)
	}
	// INV-13: la personalización no cobra. Los precios que leen el export y el
	// summary son los que se sembraron.
	if got[0].Items[0].UnitPrice != 2500 || got[0].Items[0].Qty != 2 {
		t.Fatalf("la línea personalizada cambió de precio o cantidad: %+v", got[0].Items[0])
	}
}

// notaDePedido es la indicación que siembran y esperan los tres caminos de
// lectura de TestPostgres_CustomerNote_RoundTrip.
const notaDePedido = "dejarlo en portería"

// assertNotaEnLista comprueba el camino de la BANDEJA (List): la nota viaja en la
// cabecera, así que la lista la trae sin abrir cada solicitud.
func assertNotaEnLista(t *testing.T, store *intakes.Postgres, tenant string) {
	t.Helper()
	page, total, err := store.List(context.Background(), tenant, intakes.Filter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 2 || len(page) != 2 {
		t.Fatalf("List devolvió %d de %d; quiero 2 de 2", len(page), total)
	}
	// Orden: más recientes primero ⇒ la del día 2 (con nota) va delante.
	if page[0].CustomerNote != notaDePedido || page[1].CustomerNote != "" {
		t.Fatalf("customer_note=%q/%q; quiero la nota y luego vacío",
			page[0].CustomerNote, page[1].CustomerNote)
	}
	// Un Scan corrido se delata aquí: el estado o la sesión traerían el texto de
	// otra columna, y el total (INV-13) dejaría de ser el que se sembró.
	if page[0].Status != intakes.StatusConfirmed || page[0].SessionID != "sess-a" ||
		page[0].Total != 18000 {
		t.Fatalf("la proyección de la cabecera se descuadró: %+v", page[0])
	}
}

// assertNotaEnDetalle comprueba el camino del DETALLE (Get), en las dos
// solicitudes: la que indicó algo y la sembrada antes de que la columna existiera.
func assertNotaEnDetalle(t *testing.T, store *intakes.Postgres, tenant, conNota, sinNota string) {
	t.Helper()
	ctx := context.Background()
	detail, err := store.Get(ctx, tenant, conNota)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if detail.CustomerNote != notaDePedido || detail.Total != 18000 {
		t.Fatalf("Get: customer_note=%q total=%v", detail.CustomerNote, detail.Total)
	}
	heredada, err := store.Get(ctx, tenant, sinNota)
	if err != nil {
		t.Fatalf("Get (heredada): %v", err)
	}
	if heredada.CustomerNote != "" {
		t.Fatalf("una solicitud anterior a la columna se lee vacía, no %q", heredada.CustomerNote)
	}
}

// assertNotaEnListDetails comprueba el camino del EXPORT y el SUMMARY
// (ListDetails), que tiene su propio SQL y su propio Scan.
func assertNotaEnListDetails(t *testing.T, store *intakes.Postgres, tenant string) {
	t.Helper()
	got, err := store.ListDetails(context.Background(), tenant, intakes.Filter{}, intakes.MaxExportIntakes)
	if err != nil {
		t.Fatalf("ListDetails: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListDetails devolvió %d solicitudes; quiero 2", len(got))
	}
	if got[0].CustomerNote != notaDePedido || got[1].CustomerNote != "" {
		t.Fatalf("customer_note=%q/%q", got[0].CustomerNote, got[1].CustomerNote)
	}
	if got[0].Total != 18000 {
		t.Fatalf("la indicación del pedido movió el dinero: %v", got[0].Total)
	}
}

// TestPostgres_CustomerNote_RoundTrip valida contra la COLUMNA REAL (migración
// 0045, T4.1c) los TRES caminos de lectura de la cabecera: la lista (List), el
// detalle (Get) y el que alimenta export y summary (ListDetails). Los tres tienen
// su propio Scan y ninguno comparte destinos con los otros, así que un desajuste
// de columnas —el error clásico al añadir una a la proyección— solo se ve
// probándolos por separado y contra Postgres, no contra un store en memoria.
//
// Cada camino es un subtest para que un fallo diga CUÁL de los tres se descuadró.
// Y los tres prueban a la vez la no-regresión: la solicitud sembrada SIN nombrar
// la columna —como todas las que ya están en la base— se lee con la cadena vacía
// del DEFAULT, no con NULL ni con la nota de la vecina.
func TestPostgres_CustomerNote_RoundTrip(t *testing.T) {
	db := openTestDB(t)
	store := intakes.NewPostgres(db)
	tenant := uuid.NewString()

	conNota, sinNota := uuid.NewString(), uuid.NewString()
	seedPG(t, db, tenant, []fixture{
		{conNota, intakes.StatusConfirmed, "sess-a", 2},
		{sinNota, intakes.StatusConfirmed, "sess-a", 1},
	})
	if _, err := db.ExecContext(context.Background(),
		`UPDATE public.intakes SET customer_note = $2 WHERE id = $1`, conNota, notaDePedido); err != nil {
		t.Fatalf("sembrando la indicación del pedido: %v", err)
	}

	t.Run("lista", func(t *testing.T) { assertNotaEnLista(t, store, tenant) })
	t.Run("detalle", func(t *testing.T) { assertNotaEnDetalle(t, store, tenant, conNota, sinNota) })
	t.Run("export y summary", func(t *testing.T) { assertNotaEnListDetails(t, store, tenant) })
}

// TestPostgres_UpdateStatus_CAS valida el compare-and-swap contra la BD real.
func TestPostgres_UpdateStatus_CAS(t *testing.T) {
	db := openTestDB(t)
	store := intakes.NewPostgres(db)
	tenant := uuid.NewString()
	ctx := context.Background()

	id := uuid.NewString()
	seedPG(t, db, tenant, []fixture{{id, intakes.StatusClosedLegacy, "sess-a", 1}})

	// Escribe sobre la fila legada usando las variantes del estado canónico.
	updated, err := store.UpdateStatus(ctx, tenant, id, intakes.StatusDepositRequested,
		intakes.StoredVariants(intakes.StatusConfirmed))
	if err != nil {
		t.Fatalf("UpdateStatus sobre la fila legada: %v", err)
	}
	if updated.Status != intakes.StatusDepositRequested {
		t.Fatalf("status=%q, quiero deposit_requested", updated.Status)
	}

	// El mismo CAS otra vez ya no casa: la fila cambió de estado.
	if _, err := store.UpdateStatus(ctx, tenant, id, intakes.StatusDepositPaid,
		intakes.StoredVariants(intakes.StatusConfirmed)); !errors.Is(err, intakes.ErrConflict) {
		t.Fatalf("err=%v, quiero ErrConflict (el estado ya no es el esperado)", err)
	}

	// Cross-tenant: ni escribe ni revela que existe.
	if _, err := store.UpdateStatus(ctx, uuid.NewString(), id, intakes.StatusCancelled,
		intakes.StoredVariants(intakes.StatusDepositRequested)); !errors.Is(err, intakes.ErrNotFound) {
		t.Fatalf("err=%v, quiero ErrNotFound", err)
	}
	after, err := store.Get(ctx, tenant, id)
	if err != nil {
		t.Fatalf("Get tras el intento cross-tenant: %v", err)
	}
	if after.Status != intakes.StatusDepositRequested {
		t.Fatalf("status=%q; el intento ajeno no puede haber escrito", after.Status)
	}
}

// bandejaConLíneas siembra tres solicitudes —una CON dos líneas, dos SIN ninguna—
// y devuelve el store, el tenant y los ids de la más nueva a la más vieja. Es el
// fixture de los tests de ListDetails (la consulta que alimenta export y summary).
func bandejaConLíneas(t *testing.T) (*intakes.Postgres, string, [3]string) {
	t.Helper()
	db := openTestDB(t)
	tenant := uuid.NewString()
	conLíneas, sinLíneas, vieja := uuid.NewString(), uuid.NewString(), uuid.NewString()
	seedPG(t, db, tenant, []fixture{
		{vieja, intakes.StatusCancelled, "sess-b", 1},
		{sinLíneas, intakes.StatusOpen, "sess-a", 2},
		{conLíneas, intakes.StatusClosedLegacy, "sess-a", 3},
	})
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO public.intake_items (intake_id, sku, label, qty, unit_price, added_at)
		VALUES ($1, 'torta-v1',  'Torta 10-12 porciones', 1, 18000, now()),
		       ($1, '_shipping', 'Envío — Providencia',   2,  3000, now() + interval '1 second')
	`, conLíneas); err != nil {
		t.Fatalf("sembrando líneas: %v", err)
	}
	return intakes.NewPostgres(db), tenant, [3]string{conLíneas, sinLíneas, vieja}
}

// TestPostgres_ListDetails_CabecerasYLíneas valida contra la BD real la consulta
// que alimenta el export y el summary. Dos cosas que el store en memoria no puede
// demostrar: que el LEFT JOIN deja pasar las solicitudes SIN líneas (con un INNER
// JOIN desaparecerían del export sin ruido) y que cada línea queda colgada de SU
// cabecera, en orden de alta.
func TestPostgres_ListDetails_CabecerasYLíneas(t *testing.T) {
	store, tenant, id := bandejaConLíneas(t)

	got, err := store.ListDetails(context.Background(), tenant, intakes.Filter{}, intakes.MaxExportIntakes)
	if err != nil {
		t.Fatalf("ListDetails: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("solicitudes=%d, quiero 3 (las que no tienen líneas también salen)", len(got))
	}
	if got[0].ID != id[0] || got[1].ID != id[1] || got[2].ID != id[2] {
		t.Fatalf("orden=%v; quiero descendente por created_at",
			[]string{got[0].ID, got[1].ID, got[2].ID})
	}
	if got[0].Status != intakes.StatusConfirmed {
		t.Fatalf("status=%q; el `closed` de BD se lee normalizado", got[0].Status)
	}
	if len(got[0].Items) != 2 || got[0].Items[0].SKU != "torta-v1" || got[0].Items[1].Qty != 2 {
		t.Fatalf("líneas de la primera=%+v; quiero las dos en orden de alta", got[0].Items)
	}
	if len(got[1].Items) != 0 || len(got[2].Items) != 0 {
		t.Fatalf("solicitudes sin líneas con líneas colgadas: %+v / %+v", got[1].Items, got[2].Items)
	}
}

// TestPostgres_ListDetails_CorteFiltroYTenant: el LIMIT corta CABECERAS, no filas
// del join —si estuviera fuera del CTE, pedir una devolvería una solicitud con las
// líneas a medias—, el predicado es el mismo de List y el tenant acota (INV-8).
func TestPostgres_ListDetails_CorteFiltroYTenant(t *testing.T) {
	store, tenant, id := bandejaConLíneas(t)
	ctx := context.Background()

	got, err := store.ListDetails(ctx, tenant, intakes.Filter{}, 1)
	if err != nil {
		t.Fatalf("ListDetails con limit: %v", err)
	}
	if len(got) != 1 || got[0].ID != id[0] || len(got[0].Items) != 2 {
		t.Fatalf("limit=1 devolvió %+v; quiero UNA solicitud con sus DOS líneas", got)
	}

	got, err = store.ListDetails(ctx, tenant, intakes.Filter{SessionID: "sess-a"}, intakes.MaxExportIntakes)
	if err != nil {
		t.Fatalf("ListDetails con filtro: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("solicitudes de sess-a=%d, quiero 2", len(got))
	}

	got, err = store.ListDetails(ctx, uuid.NewString(), intakes.Filter{}, intakes.MaxExportIntakes)
	if err != nil {
		t.Fatalf("ListDetails de un tenant ajeno: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("un tenant ajeno ve %d solicitudes", len(got))
	}
}

// bandejaAbandonada siembra dos solicitudes ABIERTAS del mismo tenant —la primera
// con dos líneas y una revisión— y abandona esa primera con el MISMO
// compare-and-swap que ejecuta POST /api/v1/intakes/{id}/status. Devuelve el store,
// el tenant y los dos ids (la abandonada y la que sigue viva).
//
// La transición se aplica por el CAS y no con un UPDATE a mano porque lo que se
// valida es el camino por el que el Plan 043 va a abandonar de verdad. Y se hace
// contra la BD REAL porque el store en memoria no puede demostrar lo que aquí
// importa: que la columna `status` ACEPTA la clave nueva —la 0041 dejó fuera el
// CHECK a propósito y la 0045 no lo añadió—. Si alguien añadiera algún día un CHECK
// sin `abandoned`, esto es lo que lo delata; hoy lo descubriría el Plan 043 en
// producción.
func bandejaAbandonada(t *testing.T) (*intakes.Postgres, string, string, string) {
	t.Helper()
	db := openTestDB(t)
	store := intakes.NewPostgres(db)
	tenant := uuid.NewString()
	ctx := context.Background()

	abandonada, viva := uuid.NewString(), uuid.NewString()
	seedPG(t, db, tenant, []fixture{
		{abandonada, intakes.StatusOpen, "sess-a", 1},
		{viva, intakes.StatusOpen, "sess-a", 2},
	})
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.intake_items (intake_id, sku, label, customization, qty, unit_price, added_at)
		VALUES ($1, 'torta-v1', 'Torta 10-12 porciones', 'sin sal', 1, 18000, now()),
		       ($1, 'vela-num', 'Vela número 3',         '',        1,  3000, now() + interval '1 second')
	`, abandonada); err != nil {
		t.Fatalf("sembrando las líneas: %v", err)
	}
	// Una revisión: el rastro de lo negociado tiene que sobrevivir al abandono.
	if _, err := store.InsertRevision(ctx, intakes.Revision{
		IntakeID: abandonada, Kind: intakes.RevisionKindCart,
		Payload:   []byte(`{"version":1,"items":[{"sku":"torta-v1","qty":1}]}`),
		CreatedBy: intakes.RevisionBySystem,
	}); err != nil {
		t.Fatalf("sembrando la revisión: %v", err)
	}

	updated, err := store.UpdateStatus(ctx, tenant, abandonada, intakes.StatusAbandoned,
		intakes.StoredVariants(intakes.StatusOpen))
	if err != nil {
		t.Fatalf("UpdateStatus a abandoned contra la tabla real: %v", err)
	}
	if updated.Status != intakes.StatusAbandoned {
		t.Fatalf("status=%q, quiero abandoned", updated.Status)
	}
	return store, tenant, abandonada, viva
}

// TestPostgres_Abandoned_SeFiltraEnElListado: el WHERE del listado alcanza la clave
// nueva, y la solicitud que sigue abierta no se contamina. Las dos mitades juntas:
// un filtro que devolviera la fila en los dos cubos pasaría la primera y mentiría.
func TestPostgres_Abandoned_SeFiltraEnElListado(t *testing.T) {
	store, tenant, abandonada, viva := bandejaAbandonada(t)
	ctx := context.Background()

	got, total, err := store.List(ctx, tenant, intakes.Filter{Status: intakes.StatusAbandoned})
	if err != nil {
		t.Fatalf("List(abandoned): %v", err)
	}
	if total != 1 || !slices.Equal(ids(got), []string{abandonada}) {
		t.Fatalf("List(abandoned): total=%d ids=%v; quiero solo la abandonada", total, ids(got))
	}

	got, total, err = store.List(ctx, tenant, intakes.Filter{Status: intakes.StatusOpen})
	if err != nil {
		t.Fatalf("List(open): %v", err)
	}
	if total != 1 || !slices.Equal(ids(got), []string{viva}) {
		t.Fatalf("List(open): total=%d ids=%v; quiero solo la que sigue abierta", total, ids(got))
	}
}

// TestPostgres_Abandoned_ConservaLíneasYRevisiones: abandonar cambia el ESTADO y
// nada más. Se comprueban las DOS consultas de lectura, que no comparten SQL:
// ListDetails —la que alimenta el export y el summary— y Get, el detalle.
func TestPostgres_Abandoned_ConservaLíneasYRevisiones(t *testing.T) {
	store, tenant, abandonada, _ := bandejaAbandonada(t)
	ctx := context.Background()

	details, err := store.ListDetails(ctx, tenant,
		intakes.Filter{Status: intakes.StatusAbandoned}, intakes.MaxExportIntakes)
	if err != nil {
		t.Fatalf("ListDetails(abandoned): %v", err)
	}
	if len(details) != 1 || len(details[0].Items) != 2 {
		t.Fatalf("ListDetails(abandoned)=%+v; quiero UNA solicitud con sus DOS líneas", details)
	}
	if details[0].Items[0].Customization != "sin sal" {
		t.Fatalf("la personalización se perdió al abandonar: %+v", details[0].Items[0])
	}

	detail, err := store.Get(ctx, tenant, abandonada)
	if err != nil {
		t.Fatalf("Get de la abandonada: %v", err)
	}
	if len(detail.Revisions) != 1 || detail.Revisions[0].Kind != intakes.RevisionKindCart {
		t.Fatalf("revisions=%+v; la negociación auditada sobrevive al abandono", detail.Revisions)
	}
}

func ids(in []intakes.Intake) []string {
	out := make([]string, 0, len(in))
	for _, i := range in {
		out = append(out, i.ID)
	}
	return out
}
