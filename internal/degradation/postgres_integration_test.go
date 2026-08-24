// postgres_integration_test.go verifica contra Postgres real lo que NINGÚN doble
// en memoria puede demostrar: que el DEDUPE LO GARANTIZA LA BASE.
//
// El test de unidad demuestra que el escritor le da la misma clave a los N
// fallos; eso deja abierta la pregunta que de verdad importa —«¿y si dos réplicas
// escriben a la vez?»— y esa pregunta solo la responde el índice único
// ux_owner_degradation_notices_ventana. Un fake que colapsa por un mapa demuestra
// que el mapa colapsa.
//
// Mismo contrato que el resto de los *_integration_test.go del repo (sin
// WAPP_TEST_DB_DSN se salta) y mismo criterio que
// tenantllm/postgres_integration_test.go:1-8.
package degradation_test

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/degradation"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
)

const dsnEnv = "WAPP_TEST_DB_DSN"

const tenantIntegracion = "t-degradacion-integracion"

// openTestDB abre la BD de integración y aplica el esquema — mismo contrato que
// el resto de los *_integration_test.go del repo: sin DSN se salta.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("%s no definido pero WAPP_TEST_REQUIRE_DB exige BD", dsnEnv)
		}
		t.Skipf("%s no definido: se omiten los tests de integración con BD", dsnEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, postgres.Config{DSN: dsn})
	if err != nil {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("BD no disponible en %s (%v) pero WAPP_TEST_REQUIRE_DB exige BD", dsnEnv, err)
		}
		t.Skipf("BD no disponible en %s (%v): se omiten los tests de integración", dsnEnv, err)
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

// nuevoStorePG arma el store y deja la tabla limpia PARA ESTOS TENANTS. No hace
// un DELETE global —al revés que wipeTenantLLM— porque esta tabla no es de
// configuración: si algún día la puebla un productor y alguien corre los tests
// contra UAT, un DELETE sin WHERE borraría avisos de verdad.
func nuevoStorePG(t *testing.T) (*degradation.Postgres, *sql.DB) {
	t.Helper()
	db := openTestDB(t)
	limpiar(t, db)
	t.Cleanup(func() { limpiar(t, db) })
	return degradation.NewPostgres(db), db
}

func limpiar(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.ExecContext(context.Background(),
		`DELETE FROM public.owner_degradation_notices WHERE tenant_id LIKE 't-degradacion%'`)
	if err != nil {
		t.Fatalf("limpiando public.owner_degradation_notices: %v", err)
	}
}

func contarFilas(ctx context.Context, t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM public.owner_degradation_notices WHERE tenant_id = $1`,
		tenantIntegracion).Scan(&n)
	if err != nil {
		t.Fatalf("contando avisos: %v", err)
	}
	return n
}

// TestDedupeNFallosUnaFila es EL criterio de T1.5-4 contra la base: N fallos en
// la ventana ⇒ 1 fila, con el contador en N.
//
// Mutación: en la migración 0075, cambiar
//
//	CREATE UNIQUE INDEX IF NOT EXISTS ux_owner_degradation_notices_ventana
//
// por
//
//	CREATE INDEX IF NOT EXISTS ux_owner_degradation_notices_ventana
//
// ⇒ el ON CONFLICT deja de tener arbitrio que inferir y el INSERT falla en
// ejecución: este test se pone ROJO en el primer Record. (Es la mutación que
// importa: sin el índice ÚNICO no hay dedupe, aunque el Go siga igual de bonito.)
func TestDedupeNFallosUnaFila(t *testing.T) {
	store, db := nuevoStorePG(t)
	n := degradation.NewNotifier(store, 15*time.Minute)
	ctx := context.Background()
	base := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)

	const fallos = 25
	nacidos := 0
	for i := range fallos {
		at := base.Add(time.Duration(i) * 30 * time.Second) // 25 × 30 s = 12,5 min: caben en la ventana
		creado, err := n.Record(ctx, tenantIntegracion, degradation.ReasonOllamaDown, degradation.ViaLocal, at)
		if err != nil {
			t.Fatalf("Record #%d: %v", i, err)
		}
		if creado {
			nacidos++
		}
	}
	if nacidos != 1 {
		t.Errorf("nacieron %d avisos con %d fallos sostenidos, se esperaba 1", nacidos, fallos)
	}
	if got := contarFilas(ctx, t, db); got != 1 {
		t.Errorf("quedaron %d filas, se esperaba 1 (REQ-38)", got)
	}

	var occurrences int
	var createdAt, lastSeenAt time.Time
	err := db.QueryRowContext(ctx,
		`SELECT occurrences, created_at, last_seen_at FROM public.owner_degradation_notices
		  WHERE tenant_id = $1`, tenantIntegracion).Scan(&occurrences, &createdAt, &lastSeenAt)
	if err != nil {
		t.Fatalf("leyendo el aviso colapsado: %v", err)
	}
	if occurrences != fallos {
		t.Errorf("occurrences = %d, se esperaban %d: el aviso no dice cuánto duró la degradación",
			occurrences, fallos)
	}
	// El aviso nació con el primer fallo y siguió viendo fallos después: eso es lo
	// que le dice al dueño «sigue rota» en vez de «se cayó una vez».
	if !lastSeenAt.After(createdAt) && !lastSeenAt.Equal(createdAt) {
		t.Errorf("last_seen_at (%s) es anterior a created_at (%s)", lastSeenAt, createdAt)
	}
}

// TestDedupeAguantaEscriturasConcurrentes es la pregunta que el fake no puede
// responder: DOS caminos escribiendo a la vez (el equivalente en un proceso de
// dos réplicas del servidor) siguen produciendo UNA fila, porque quien arbitra es
// el índice único y no un `if` de Go.
//
// Mutación: en saveSQL (postgres.go), cambiar
//
//	ON CONFLICT (tenant_id, reason, via, window_start) DO UPDATE
//
// por
//
//	ON CONFLICT DO NOTHING
//
// ⇒ este test se pone ROJO por otro lado: sigue habiendo 1 fila, pero
// `occurrences` se queda en 1 y el Scan del RETURNING falla con sql.ErrNoRows en
// las 19 escrituras colapsadas — que es justo el fallo que enseña por qué DO
// NOTHING no sirve aquí.
func TestDedupeAguantaEscriturasConcurrentes(t *testing.T) {
	store, db := nuevoStorePG(t)
	n := degradation.NewNotifier(store, 15*time.Minute)
	ctx := context.Background()
	at := time.Date(2026, 8, 23, 11, 7, 0, 0, time.UTC)

	const escritores = 20
	var wg sync.WaitGroup
	errs := make([]error, escritores)
	creados := make([]bool, escritores)
	wg.Add(escritores)
	for i := range escritores {
		go func(idx int) {
			defer wg.Done()
			creados[idx], errs[idx] = n.Record(ctx, tenantIntegracion,
				degradation.ReasonBreakerOpen, degradation.ViaLocal, at)
		}(i)
	}
	wg.Wait()

	nacidos := 0
	for i, err := range errs {
		if err != nil {
			t.Errorf("escritor #%d: %v", i, err)
		}
		if creados[i] {
			nacidos++
		}
	}
	if nacidos != 1 {
		t.Errorf("%d escritores concurrentes dijeron haber creado el aviso, se esperaba exactamente 1", nacidos)
	}
	if got := contarFilas(ctx, t, db); got != 1 {
		t.Errorf("quedaron %d filas con %d escritores concurrentes, se esperaba 1", got, escritores)
	}
}

// TestVentanaSiguienteAbreAvisoNuevo comprueba lo contrario del dedupe, que es
// igual de importante: el aviso es POR VENTANA, no eterno. Sin esto, un tenant
// con el Ollama caído toda la semana recibiría un solo aviso el lunes y nada más.
//
// Mutación: en VentanaDe (degradation.go), cambiar
//
//	inicio = at.UTC().Truncate(v)
//
// por
//
//	inicio = at.UTC().Truncate(24 * time.Hour)
//
// ⇒ este test se pone ROJO: los dos fallos caerían en el mismo bucket diario.
func TestVentanaSiguienteAbreAvisoNuevo(t *testing.T) {
	store, db := nuevoStorePG(t)
	n := degradation.NewNotifier(store, 15*time.Minute)
	ctx := context.Background()
	base := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

	for _, at := range []time.Time{base, base.Add(16 * time.Minute)} {
		creado, err := n.Record(ctx, tenantIntegracion, degradation.ReasonEdgeOffline, degradation.ViaLocal, at)
		if err != nil {
			t.Fatalf("Record(%s): %v", at, err)
		}
		if !creado {
			t.Errorf("Record(%s) no abrió aviso nuevo: las dos ventanas se fundieron", at)
		}
	}
	if got := contarFilas(ctx, t, db); got != 2 {
		t.Errorf("quedaron %d filas, se esperaban 2 (dos ventanas distintas)", got)
	}
}

// TestElCheckDeMotivosEsLaRedDeAbajo comprueba que el vocabulario cerrado está
// TAMBIÉN en la base, no solo en Go. El escritor rechaza el motivo sano antes de
// llegar aquí (test de unidad); esto verifica que si alguien se salta el escritor
// —un INSERT a mano, un futuro store distinto— la base sigue diciendo que no.
//
// Mutación: en la 0075, mover el CHECK de reason DENTRO del CREATE TABLE ⇒ sobre
// una base ya migrada el CHECK desaparece (el CREATE no vuelve a correr) y este
// test se pone ROJO. Es exactamente el NO-OP del segundo arranque que la 0071
// pagó con tiempo real.
func TestElCheckDeMotivosEsLaRedDeAbajo(t *testing.T) {
	_, db := nuevoStorePG(t)
	ctx := context.Background()

	_, err := db.ExecContext(ctx, `
		INSERT INTO public.owner_degradation_notices
		       (tenant_id, reason, via, window_start, window_end)
		VALUES ($1, 'fastlane', 'local', now(), now() + interval '15 minutes')`,
		tenantIntegracion)
	if err == nil {
		t.Fatal("la base aceptó un motivo SANO: el CHECK del vocabulario no está vigilando")
	}
	if !strings.Contains(err.Error(), "owner_degradation_notices_reason_check") {
		t.Errorf("la base rechazó por %v, se esperaba owner_degradation_notices_reason_check", err)
	}
}

// TestLosMotivosDeT166EntranEnLaBase es el criterio de la ampliación contra
// Postgres real: los DOS motivos que añade T1.6-6 son ACEPTADOS, y el CHECK
// sigue siendo un vocabulario CERRADO y no uno decorativo.
//
// El test de unidad compara las dos listas —el enum y el `.sql`— y eso deja una
// pregunta abierta que solo la base responde: ¿el `ADD CONSTRAINT` de la 0075
// llegó de verdad a esta base con los ocho valores? Que el fichero los liste es
// una afirmación sobre el fichero, no sobre la base que el runner dejó.
//
// Mutaciones, una por mitad:
//
//   - quitar `'lease_invalid','edge_sin_capacidad'` de la lista del `IN` de la
//     0075 ⇒ la base vuelve al vocabulario de SEIS y los DOS primeros INSERT
//     fallan: rojo en la primera mitad.
//   - borrar el `ADD CONSTRAINT` de la 0075 dejando solo su `DROP` ⇒ la tabla se
//     queda SIN CHECK de motivo y acepta cualquier cadena: rojo en la segunda
//     mitad, que es la que distingue «ampliar» de «abrir».
func TestLosMotivosDeT166EntranEnLaBase(t *testing.T) {
	_, db := nuevoStorePG(t)
	ctx := context.Background()

	for _, motivo := range []degradation.Reason{
		degradation.ReasonLeaseInvalid,
		degradation.ReasonEdgeSinCapacidad,
	} {
		_, err := db.ExecContext(ctx, `
			INSERT INTO public.owner_degradation_notices
			       (tenant_id, reason, via, window_start, window_end)
			VALUES ($1, $2, 'local', now(), now() + interval '15 minutes')`,
			tenantIntegracion, string(motivo))
		if err != nil {
			t.Errorf("la base RECHAZÓ %q: el CHECK de la 0075 en esta base sigue siendo el de SEIS (%v)",
				motivo, err)
		}
	}

	// La otra mitad, y la que de verdad prueba que ampliar no es abrir: un motivo
	// inventado sigue sin entrar. Sin este caso, un CHECK borrado por accidente
	// —el `DROP` sin su `ADD`— dejaría el test de arriba en verde.
	_, err := db.ExecContext(ctx, `
		INSERT INTO public.owner_degradation_notices
		       (tenant_id, reason, via, window_start, window_end)
		VALUES ($1, 'lease_expired', 'local', now(), now() + interval '15 minutes')`,
		tenantIntegracion)
	if err == nil {
		t.Fatal("la base aceptó 'lease_expired': el vocabulario dejó de estar cerrado")
	}
	if !strings.Contains(err.Error(), "owner_degradation_notices_reason_check") {
		t.Errorf("la base rechazó por %v, se esperaba owner_degradation_notices_reason_check", err)
	}
}

// TestListDevuelveLoDelTenantYNadaMas custodia INV-7 en el store: el tenant es un
// ARGUMENTO, y no hay forma de que la lista traiga filas de otro.
//
// Mutación: en listSQL (postgres.go), quitar
//
//	WHERE tenant_id = $1
//
// (dejando `WHERE $1 = $1`) ⇒ este test se pone ROJO.
func TestListDevuelveLoDelTenantYNadaMas(t *testing.T) {
	store, _ := nuevoStorePG(t)
	n := degradation.NewNotifier(store, 15*time.Minute)
	ctx := context.Background()
	base := time.Date(2026, 8, 23, 13, 0, 0, 0, time.UTC)

	if _, err := n.Record(ctx, tenantIntegracion, degradation.ReasonAPIError, degradation.ViaAPI, base); err != nil {
		t.Fatalf("Record del tenant propio: %v", err)
	}
	if _, err := n.Record(ctx, "t-degradacion-ajeno", degradation.ReasonAPIError, degradation.ViaAPI, base); err != nil {
		t.Fatalf("Record del tenant ajeno: %v", err)
	}

	avisos, err := store.List(ctx, tenantIntegracion, degradation.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(avisos) != 1 {
		t.Fatalf("List devolvió %d avisos, se esperaba 1", len(avisos))
	}
	a := avisos[0]
	if a.TenantID != tenantIntegracion {
		t.Errorf("List devolvió un aviso de %q", a.TenantID)
	}
	if a.Reason != degradation.ReasonAPIError || a.Via != degradation.ViaAPI {
		t.Errorf("aviso = (%s, %s), se esperaba (api_error, api)", a.Reason, a.Via)
	}
	if a.ID == "" {
		t.Error("el aviso llegó sin id: el RETURNING o el Scan no lo trajeron")
	}
	if a.Leida() {
		t.Error("un aviso recién escrito llegó marcado como leído: nada debe escribir read_at hoy")
	}
	if a.Occurrences != 1 {
		t.Errorf("occurrences = %d en un aviso recién nacido, se esperaba 1", a.Occurrences)
	}

	// El filtro «sin leer» tiene que devolver lo mismo hoy: NADA escribe read_at.
	sinLeer, err := store.List(ctx, tenantIntegracion, degradation.ListFilter{SoloSinLeer: true})
	if err != nil {
		t.Fatalf("List(SoloSinLeer): %v", err)
	}
	if len(sinLeer) != 1 {
		t.Errorf("List(SoloSinLeer) devolvió %d avisos, se esperaba 1", len(sinLeer))
	}
}
