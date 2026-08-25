// aggregation_window_integration_test.go — Plan 044 · Ola 1 · T1.2, criterio «test de
// INTEGRACIÓN» contra Postgres REAL: la ventana de agregación por tenant
// (`tenant_settings.aggregation_window_seconds`, migración 0072 sección E) y el SELECT
// nuevo de GetTenantSettings que la lee.
//
// ⏳ NINGUNO DE ESTOS TESTS SE HA EJECUTADO. Se escribieron sin Go, sin Docker y sin
// Postgres delante; ninguno está declarado como pasado. Cada aserción lleva escrita la
// SALIDA ESPERADA.
//
// # POR QUÉ ESTO NO SE PUEDE PROBAR CONTRA EL GEMELO EN MEMORIA
//
// Las tres cosas que mide viven SOLO en el esquema:
//
//  1. el DEFAULT 45 de una columna —un mapa Go no tiene defaults—;
//  2. la diferencia entre «NO HAY FILA» (manda DefaultTenantSettings, el espejo de Go)
//     y «HAY FILA CON UN 0 ESCRITO» (manda el 0, que es el override explícito de FLUSH
//     INMEDIATO): son dos CAMINOS distintos del mismo método, y uno de ellos es
//     `sql.ErrNoRows`, que en memoria no existe;
//  3. que el SELECT —con SIETE columnas desde T1.2— case con la tabla que la 0072 deja.
//
// 🔴 EL DEFECTO QUE (2) VIGILA YA OCURRIÓ EN ESTA MISMA CASA, con otra columna:
// repository_postgres.go documenta, sobre este mismo método, que un
// `if x == 0 { x = Default }` convertiría el override «sin vencimiento» de
// `event_inactivity_ttl_seconds` en 2 h sin que nadie se entere. Con
// `aggregation_window_seconds` el daño es simétrico y peor de ver: un tenant que pidió
// FLUSH INMEDIATO empezaría a esperar 45 s por cada presupuesto, y al revés —un cero
// que nadie puso— apagaría la agregación del parque entero, un pipeline por mensaje.
//
// Se corre igual que el resto: sin build tag, por el nombre del fichero, con
// `WAPP_TEST_DB_DSN`. Sin ella se salta solo. Reusa `openTestDB` de integration_test.go
// (mismo paquete `store_test`): no se define un mecanismo nuevo.
package store_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
)

// techoCrudo lee `aggregation_max_seconds` con SQL DIRECTO. La distinción con
// GetTenantSettings es el fichero entero: solo el SELECT crudo distingue «la BD puso el
// valor» de «Go rellenó el hueco» (mismo argumento que ttlConversacionalDeLaBD).
func techoCrudo(t *testing.T, db *sql.DB, tenantID string) int {
	t.Helper()
	var v int
	if err := db.QueryRowContext(context.Background(),
		`SELECT aggregation_max_seconds FROM public.tenant_settings WHERE tenant_id = $1`,
		tenantID).Scan(&v); err != nil {
		t.Fatalf("leer aggregation_max_seconds cruda de %s: %v", tenantID, err)
	}
	return v
}

// conVentanaDeAgregacion siembra la fila de tenant_settings NOMBRANDO
// EXPLÍCITAMENTE la columna de la ventana. Nombrarla es el punto: una fila que la
// omitiera recibiría el DEFAULT 45 del esquema y no probaría nada sobre el override.
// Es el mismo helper —y el mismo cleanup— que conTTLsDeEvento en
// event_seams_integration_test.go.
func conVentanaDeAgregacion(t *testing.T, db *sql.DB, segundos int) (*store.PostgresRepository, string) {
	t.Helper()
	tenant := uuid.NewString()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.tenant_settings (tenant_id, page_size, aggregation_window_seconds)
		VALUES ($1, 5, $2)
	`, tenant, segundos); err != nil {
		t.Fatalf("sembrando tenant_settings con aggregation_window_seconds=%d: %v", segundos, err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(),
			`DELETE FROM public.tenant_settings WHERE tenant_id = $1`, tenant); err != nil {
			t.Logf("limpiando tenant_settings de %s: %v", tenant, err)
		}
	})
	return store.NewPostgresRepository(db), tenant
}

// ---------------------------------------------------------------------------
// (6) SIN FILA MANDA EL DEFAULT DE GO; CON FILA MANDA LA FILA, EL 0 INCLUIDO
// ---------------------------------------------------------------------------

// TestIntegration_AggregationWindow_SinFilaHereda45 es la mitad que el DEFAULT del
// esquema NO cubre: un tenant sin fila no lee ninguna columna, así que el 45 tiene que
// salir de `DefaultTenantSettings` (el espejo de Go). Y no es un caso raro: el propio
// comentario de store.go dice que en UAT los tenants sin fila son 2 de 3.
//
// SALIDA ESPERADA:
//   - GetTenantSettings(<tenant que no existe>).AggregationWindow == 45s
//   - err == nil   (la ausencia de fila NO es un error: es el camino normal)
func TestIntegration_AggregationWindow_SinFilaHereda45(t *testing.T) {
	db := openTestDB(t)
	repo := store.NewPostgresRepository(db)

	got, err := repo.GetTenantSettings(context.Background(), uuid.NewString())
	if err != nil {
		t.Fatalf("GetTenantSettings (sin fila) = error %v; ESPERADO nil — sin fila se devuelven los "+
			"defaults SIN error (sql.ErrNoRows se traduce, no se propaga)", err)
	}
	if got.AggregationWindow != 45*time.Second {
		t.Fatalf("AggregationWindow sin fila = %v; ESPERADO 45s (store.DefaultAggregationWindow). "+
			"Un 0 aquí NO es «sin configurar»: significa FLUSH INMEDIATO, o sea la agregación APAGADA "+
			"en silencio para todos los tenants que no tienen fila", got.AggregationWindow)
	}
	if got.AggregationWindow != store.DefaultAggregationWindow {
		t.Fatalf("AggregationWindow sin fila = %v; ESPERADO que fuera exactamente "+
			"store.DefaultAggregationWindow (%v) — si un día ese default cambia, este test tiene que "+
			"seguir hablando del MISMO número que el código",
			got.AggregationWindow, store.DefaultAggregationWindow)
	}
}

// TestIntegration_AggregationWindow_ConFilaEnCeroSeRespeta es LA aserción de T1.2 y la
// que un `if x == 0 { x = Default }` rompería.
//
// 0 es una configuración LEGÍTIMA —el CHECK de la 0072 es `>= 0` justamente por esto—:
// significa FLUSH INMEDIATO, sin agregación, un pipeline por mensaje. Es lo que el
// sistema hacía antes de esta ola y es una elección razonable para un tenant cuyos
// clientes escriben mensajes largos y sueltos.
//
// SALIDAS ESPERADAS:
//   - fila con aggregation_window_seconds = 0  ⇒  AggregationWindow == 0s   (NO 45s)
//   - fila con aggregation_window_seconds = 30 ⇒  AggregationWindow == 30s
//
// El caso 30 va en el mismo test a propósito: sin él, un método que devolviera SIEMPRE
// 0 pasaría la primera mitad en verde.
func TestIntegration_AggregationWindow_ConFilaEnCeroSeRespeta(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	repo, enCero := conVentanaDeAgregacion(t, db, 0)
	got, err := repo.GetTenantSettings(ctx, enCero)
	if err != nil {
		t.Fatalf("GetTenantSettings (fila con 0): %v", err)
	}
	if got.AggregationWindow != 0 {
		t.Fatalf("AggregationWindow con fila en 0 = %v; ESPERADO 0s. El 0 es el override EXPLÍCITO "+
			"«flush inmediato» (CHECK >= 0 de la 0072), no un hueco que rellenar: sustituirlo por el "+
			"default apaga la elección del tenant sin que nadie se entere", got.AggregationWindow)
	}

	repo2, en30 := conVentanaDeAgregacion(t, db, 30)
	got2, err := repo2.GetTenantSettings(ctx, en30)
	if err != nil {
		t.Fatalf("GetTenantSettings (fila con 30): %v", err)
	}
	if got2.AggregationWindow != 30*time.Second {
		t.Fatalf("AggregationWindow con fila en 30 = %v; ESPERADO 30s — si esto sale 0, el método no está "+
			"leyendo la columna sino devolviendo un cero fijo, y la mitad de arriba estaría en verde por "+
			"la razón equivocada", got2.AggregationWindow)
	}
}

// TestIntegration_AggregationWindow_FilaQueNoLaNombraHereda45DelEsquema cierra el
// tercer camino, que es el del BACKFILL: una fila que NO menciona la columna la recibe
// del `ADD COLUMN … NOT NULL DEFAULT 45` de la 0072. Es lo que le pasa a todo tenant
// que ya tenía config antes de esta ola.
//
// 🔴 Es la (V4) de la migración escrita como test: el `count(*) WHERE
// aggregation_window_seconds = 0` de allí existe porque 0 satisface el CHECK, así que
// un `ADD COLUMN` aplicado SIN default rellenaría con el cero del tipo y apagaría la
// agregación del parque entero SIN dar un solo error.
//
// SALIDAS ESPERADAS:
//   - la columna cruda de esa fila ......... 45
//   - GetTenantSettings(...).AggregationWindow == 45s
func TestIntegration_AggregationWindow_FilaQueNoLaNombraHereda45DelEsquema(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenant := uuid.NewString()

	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.tenant_settings (tenant_id, page_size) VALUES ($1, 5)
	`, tenant); err != nil {
		t.Fatalf("sembrando tenant_settings sin nombrar la ventana: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(),
			`DELETE FROM public.tenant_settings WHERE tenant_id = $1`, tenant); err != nil {
			t.Logf("limpiando tenant_settings de %s: %v", tenant, err)
		}
	})

	// La columna CRUDA, no el struct: solo el SELECT directo responde por el DEFAULT
	// del esquema. Leerla por GetTenantSettings no distinguiría «la BD puso 45» de «Go
	// rellenó el hueco» (mismo argumento que ttlConversacionalDeLaBD en
	// conversation_ttl_integration_test.go).
	var crudo int
	if err := db.QueryRowContext(ctx,
		`SELECT aggregation_window_seconds FROM public.tenant_settings WHERE tenant_id = $1`,
		tenant).Scan(&crudo); err != nil {
		t.Fatalf("leer aggregation_window_seconds cruda: %v", err)
	}
	if crudo != 45 {
		t.Fatalf("aggregation_window_seconds (columna cruda) = %d; ESPERADO 45. Un 0 aquí significa que el "+
			"ADD COLUMN se aplicó SIN default y Postgres rellenó con el cero del tipo: la agregación "+
			"quedaría apagada para todo tenant preexistente, cada mensaje disparando su propio pipeline "+
			"(es la (V4) de la 0072)", crudo)
	}

	repo := store.NewPostgresRepository(db)
	got, err := repo.GetTenantSettings(ctx, tenant)
	if err != nil {
		t.Fatalf("GetTenantSettings: %v", err)
	}
	if got.AggregationWindow != 45*time.Second {
		t.Fatalf("AggregationWindow = %v; ESPERADO 45s (el default del ESQUEMA, leído de la fila)",
			got.AggregationWindow)
	}
}

// ---------------------------------------------------------------------------
// (7) EL SELECT NUEVO CASA CON EL ESQUEMA QUE DEJA LA 0072
// ---------------------------------------------------------------------------

// TestIntegration_GetTenantSettings_FuncionaConLa0072Aplicada es el test más aburrido y
// el que primero se pone rojo si alguien toca la migración: comprueba que la COLUMNA
// existe con la forma que el SELECT de siete columnas da por hecha, y que el método
// corre de punta a punta sobre ella.
//
// No sobra teniendo los de arriba: si la columna no existiera, todos los demás
// fallarían con un error de SQL crudo («column … does not exist») y nadie sabría si el
// problema es la migración, el SELECT o la lógica del 0. Este lo dice.
//
// SALIDAS ESPERADAS (information_schema.columns, tabla tenant_settings):
//
//	column_name                | data_type | is_nullable | column_default
//	aggregation_window_seconds | integer   | NO          | 45
//
// Y además: el CHECK con nombre propio existe y MUERDE.
//
//	INSERT … (aggregation_window_seconds) VALUES (-1)
//	  → ERROR: viola tenant_settings_aggregation_window_check
func TestIntegration_GetTenantSettings_FuncionaConLa0072Aplicada(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	var tipo, nullable string
	var porDefecto sql.NullString
	err := db.QueryRowContext(ctx, `
		SELECT data_type, is_nullable, column_default
		  FROM information_schema.columns
		 WHERE table_schema = 'public'
		   AND table_name   = 'tenant_settings'
		   AND column_name  = 'aggregation_window_seconds'
	`).Scan(&tipo, &nullable, &porDefecto)
	if err != nil {
		t.Fatalf("la columna aggregation_window_seconds no aparece en information_schema: %v. "+
			"ESPERADO una fila (integer | NO | 45): sin ella, la 0072 sección E no se aplicó y el SELECT "+
			"de GetTenantSettings no puede funcionar", err)
	}
	if tipo != "integer" {
		t.Fatalf("data_type = %q; ESPERADO \"integer\"", tipo)
	}
	if nullable != "NO" {
		t.Fatalf("is_nullable = %q; ESPERADO \"NO\" — la columna nace NOT NULL porque lleva default", nullable)
	}
	if !porDefecto.Valid || porDefecto.String != "45" {
		t.Fatalf("column_default = %v; ESPERADO \"45\". Sin default, el ADD COLUMN habría rellenado las "+
			"filas preexistentes con 0 y apagado la agregación del parque", porDefecto)
	}

	// El CHECK con nombre, y que MUERDE. Lo único que descarta es lo que no significa
	// nada: un negativo no es «esperar poco», es un valor sin lectura posible.
	tenant := uuid.NewString()
	t.Cleanup(func() {
		if _, derr := db.ExecContext(context.Background(),
			`DELETE FROM public.tenant_settings WHERE tenant_id = $1`, tenant); derr != nil {
			t.Logf("limpiando tenant_settings de %s: %v", tenant, derr)
		}
	})
	_, err = db.ExecContext(ctx, `
		INSERT INTO public.tenant_settings (tenant_id, page_size, aggregation_window_seconds)
		VALUES ($1, 5, -1)
	`, tenant)
	if err == nil {
		t.Fatal("el INSERT con aggregation_window_seconds = -1 PASÓ; ESPERADO un error de CHECK " +
			"(tenant_settings_aggregation_window_check). Una ventana negativa no tiene lectura posible")
	}

	// Y el método completo corre sobre el esquema real sin reventar.
	repo := store.NewPostgresRepository(db)
	if _, gerr := repo.GetTenantSettings(ctx, uuid.NewString()); gerr != nil {
		t.Fatalf("GetTenantSettings sobre el esquema con la 0072 = error %v; ESPERADO nil", gerr)
	}
}

// ---------------------------------------------------------------------------
// (8) EL TECHO DE LA VENTANA HÍBRIDA — `aggregation_max_seconds` (0076, T1.8-1)
// ---------------------------------------------------------------------------
//
// Los cuatro tests de abajo son los HERMANOS EXACTOS de los de arriba, y existen por
// el mismo motivo: el default vive en el esquema, el override vive en la fila, y la
// diferencia entre «sin fila» y «fila con 0» son dos caminos distintos del mismo
// método. Lo que NO es hermano —y por eso lleva su propio bloque— es el BACKFILL: la
// 0072 pudo dejar que el `DEFAULT 45` hiciera de backfill porque no había ninguna
// columna anterior de la que derivar un valor mejor; la 0076 SÍ la tiene
// (`aggregation_window_seconds`) y un default a secas habría neutralizado en silencio
// la configuración de cualquier tenant con la ventana por encima de 120 s.

// invalidaElHashDelEsquema fuerza que el SIGUIENTE Migrate reejecute TODOS los
// `structure/*.sql` en vez de saltárselos.
//
// 🔴 SIN ESTO, UN TEST QUE «SIMULA EL SEGUNDO ARRANQUE» SALE HUECO. `Migrate` compara
// versión Y hash de contenido (`isUpToDate`, schema.go): con los dos iguales devuelve
// `Skipped=true` y NO TOCA LA BASE, así que un test que llamara a Migrate otra vez
// estaría afirmando que no pasa nada... porque no se ejecutó nada. Se escribe una fila
// NUEVA en `public.schema_version` con un hash imposible —`readSchemaVersion` lee la
// última por `id DESC`— y el replay entra por el camino de verdad. Lo prueba el propio
// llamante asertando `Skipped == false`.
func invalidaElHashDelEsquema(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO public.schema_version (version, content_hash, description)
		VALUES ('0.0.0-test', 'hash-invalidado-por-el-test', 'forzar full-replay en test de backfill')
	`); err != nil {
		t.Fatalf("invalidando el hash de schema_version: %v", err)
	}
}

// TestIntegration_AggregationMax_SinFilaHereda120 es la mitad que el DEFAULT del
// esquema NO cubre: un tenant SIN fila en `tenant_settings` no lee ninguna columna, así
// que su techo sale del espejo de Go (`DefaultTenantSettings`). Si alguien añade el
// campo al struct y olvida esta línea, el techo de esos tenants queda en el cero de Go
// — que aquí significa VENCIDO SIEMPRE, o sea la agregación apagada en silencio para
// todo tenant sin fila (hoy 2 de 3 en UAT).
//
// SALIDA ESPERADA: GetTenantSettings(<tenant que no existe>).AggregationMax == 120s
func TestIntegration_AggregationMax_SinFilaHereda120(t *testing.T) {
	db := openTestDB(t)
	repo := store.NewPostgresRepository(db)

	got, err := repo.GetTenantSettings(context.Background(), uuid.NewString())
	if err != nil {
		t.Fatalf("GetTenantSettings de un tenant sin fila: %v", err)
	}
	if got.AggregationMax != 120*time.Second {
		t.Fatalf("AggregationMax sin fila = %v; ESPERADO 120s. Un 0 aquí NO es «sin configurar»: "+
			"es VENCIDO SIEMPRE, y significa que la ventana cerraría en el primer barrido para todo "+
			"tenant sin fila — un job por mensaje y la agregación apagada", got.AggregationMax)
	}
	if got.AggregationMax != store.DefaultAggregationMax {
		t.Fatalf("AggregationMax sin fila = %v; ESPERADO exactamente store.DefaultAggregationMax (%v)",
			got.AggregationMax, store.DefaultAggregationMax)
	}
}

// TestIntegration_AggregationMax_ConFilaSeRespetaIncluidoElCero es la aserción gemela
// de la de la ventana: con fila manda la fila, y el 0 se respeta.
//
// 🔴 EL 0 DEL TECHO NO SIGNIFICA LO MISMO QUE PODRÍA PARECER, y por eso este test lleva
// el número escrito: significa VENCIDO SIEMPRE (cierre en el primer barrido), NO «sin
// techo». No existe «sin techo» y es deliberado — la ausencia de techo es el defecto
// que T1.8-1 cerró. Ver el COMMENT de la columna en la 0076.
//
// SALIDAS ESPERADAS:
//   - fila con aggregation_max_seconds = 0  ⇒ AggregationMax == 0s   (NO 120s)
//   - fila con aggregation_max_seconds = 90 ⇒ AggregationMax == 90s
func TestIntegration_AggregationMax_ConFilaSeRespetaIncluidoElCero(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	repo, tenantCero := conTechoDeAgregacion(t, db, 0)
	got, err := repo.GetTenantSettings(ctx, tenantCero)
	if err != nil {
		t.Fatalf("GetTenantSettings (techo 0): %v", err)
	}
	if got.AggregationMax != 0 {
		t.Fatalf("AggregationMax con fila en 0 = %v; ESPERADO 0s. Sustituirlo por el default apaga "+
			"el override del tenant sin que nadie se entere — el defecto que repository_postgres.go "+
			"prohíbe por escrito para la columna de al lado", got.AggregationMax)
	}

	repo2, tenant90 := conTechoDeAgregacion(t, db, 90)
	got2, err := repo2.GetTenantSettings(ctx, tenant90)
	if err != nil {
		t.Fatalf("GetTenantSettings (techo 90): %v", err)
	}
	if got2.AggregationMax != 90*time.Second {
		t.Fatalf("AggregationMax con fila en 90 = %v; ESPERADO 90s — si sale 120, el SELECT no está "+
			"leyendo la columna sino que alguien la rellena con el default", got2.AggregationMax)
	}
}

// conTechoDeAgregacion siembra la fila NOMBRANDO la columna del techo. Gemelo exacto
// de conVentanaDeAgregacion, y por el mismo motivo: una fila que la omitiera recibiría
// el DEFAULT del esquema y no probaría nada sobre el override.
func conTechoDeAgregacion(t *testing.T, db *sql.DB, segundos int) (*store.PostgresRepository, string) {
	t.Helper()
	tenant := uuid.NewString()
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO public.tenant_settings (tenant_id, page_size, aggregation_max_seconds)
		VALUES ($1, 5, $2)
	`, tenant, segundos); err != nil {
		t.Fatalf("sembrando tenant_settings con aggregation_max_seconds=%d: %v", segundos, err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(),
			`DELETE FROM public.tenant_settings WHERE tenant_id = $1`, tenant); err != nil {
			t.Logf("limpiando tenant_settings de %s: %v", tenant, err)
		}
	})
	return store.NewPostgresRepository(db), tenant
}

// TestIntegration_AggregationMax_BackfillFabricandoElEstadoAnterior es el criterio (g)
// de T1.8-1, y su montaje es la mitad del test.
//
// 🔴 EN UNA BASE RECIÉN MIGRADA ESTE CRITERIO SALE VERDE POR CERO FILAS. `tenant_settings`
// está vacía, el backfill no toca nada y cualquier barrido «¿queda algún NULL?» pasa sin
// haber probado nada — el modo favorito de este repo de mentir en verde. Por eso aquí se
// FABRICA el estado anterior: se TIRA la columna, se siembran filas como las que existían
// antes de la 0076, y solo entonces se reejecuta la migración encima.
//
// # QUÉ AFIRMA, Y POR QUÉ SON DOS FILAS Y NO UNA
//
//   - el tenant con la ventana en 300 s conserva un techo de 300, no 120. Si el backfill
//     fuera un `DEFAULT 120` a secas —que es lo que la 0072 pudo permitirse—, ese tenant
//     pasaría de cerrar a los 300 s a cerrar a los 120 SIN ERROR Y SIN AVISO: su
//     configuración explícita quedaría neutralizada por un default de plataforma. Es la
//     mitad que solo se ve con una fila así, y en una base de UAT recién migrada no hay
//     ninguna.
//   - el tenant con la ventana en su default (45) recibe los 120 de plataforma. Sin esta
//     fila, un backfill que copiara la ventana tal cual (`= aggregation_window_seconds`)
//     también pasaría — y dejaría el techo en 45, que es un techo POR DEBAJO del silencio
//     y apagaría de hecho la ventana híbrida entera.
//
// Las dos juntas fijan el `GREATEST(120, aggregation_window_seconds)` y ninguna sobra.
func TestIntegration_AggregationMax_BackfillFabricandoElEstadoAnterior(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// (1) EL ESTADO ANTERIOR: la columna no existe. `DROP COLUMN` se lleva por delante
	// también su CHECK; el replay de abajo lo recrea (regla 4 del patrón full-replay).
	if _, err := db.ExecContext(ctx,
		`ALTER TABLE public.tenant_settings DROP COLUMN IF EXISTS aggregation_max_seconds`); err != nil {
		t.Fatalf("fabricando el estado anterior (drop de la columna): %v", err)
	}

	viejoConVentanaLarga := uuid.NewString()
	viejoConVentanaPorDefecto := uuid.NewString()
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(),
			`DELETE FROM public.tenant_settings WHERE tenant_id IN ($1, $2)`,
			viejoConVentanaLarga, viejoConVentanaPorDefecto); err != nil {
			t.Logf("limpiando tenant_settings del test de backfill: %v", err)
		}
	})
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.tenant_settings (tenant_id, page_size, aggregation_window_seconds)
		VALUES ($1, 5, 300), ($2, 5, 45)
	`, viejoConVentanaLarga, viejoConVentanaPorDefecto); err != nil {
		t.Fatalf("sembrando las filas del estado anterior: %v", err)
	}

	// (2) EL ARRANQUE QUE APLICA LA 0076 ENCIMA. Sin invalidar el hash, Migrate diría
	// Skipped y este test no probaría nada (ver invalidaElHashDelEsquema).
	invalidaElHashDelEsquema(t, db)
	res, err := migrations.Migrate(ctx, db)
	if err != nil {
		t.Fatalf("reejecutando la migración sobre el estado anterior: %v", err)
	}
	if res.Skipped {
		t.Fatal("Migrate devolvió Skipped=true: el full-replay NO corrió y este test no probó el backfill")
	}

	// (3) EL BARRIDO DEL CRITERIO: ni un NULL en toda la tabla.
	var nulos int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM public.tenant_settings WHERE aggregation_max_seconds IS NULL`).Scan(&nulos); err != nil {
		t.Fatalf("contando techos sin rellenar: %v", err)
	}
	if nulos != 0 {
		t.Fatalf("quedan %d filas con aggregation_max_seconds NULL tras migrar; el backfill no las alcanzó", nulos)
	}

	if got := techoCrudo(t, db, viejoConVentanaLarga); got != 300 {
		t.Fatalf("el tenant con la ventana en 300 s tiene el techo en %d; ESPERADO 300 "+
			"(GREATEST(120, ventana)). Con 120, la migración le habría RECORTADO su ventana de 300 a 120 "+
			"en silencio: su configuración explícita neutralizada por un default de plataforma", got)
	}
	if got := techoCrudo(t, db, viejoConVentanaPorDefecto); got != 120 {
		t.Fatalf("el tenant con la ventana por defecto (45 s) tiene el techo en %d; ESPERADO 120. "+
			"Un 45 aquí significa que el backfill copió la ventana tal cual y el techo quedó POR DEBAJO "+
			"del silencio, apagando de hecho la ventana híbrida", got)
	}
}

// TestIntegration_AggregationMax_SegundoArranqueNoPisaLaEleccionDelTenant es la otra
// mitad del criterio (g): la migración es idempotente, y «idempotente» aquí no es solo
// «no revienta» — es que un tenant que BAJÓ su techo no lo ve volver al default en el
// próximo reinicio.
//
// 🔴 ES LO QUE DISTINGUE EL BACKFILL CON GUARDA DE UN `UPDATE` A SECAS. Un backfill sin
// `WHERE aggregation_max_seconds IS NULL` pasaría el test de arriba igual de bien y
// pisaría la elección del tenant en CADA arranque del servidor, que es un cambio de
// conducta recurrente y silencioso. Este es el test que lo caza.
func TestIntegration_AggregationMax_SegundoArranqueNoPisaLaEleccionDelTenant(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// Un tenant que eligió un techo BAJO (60 s), por debajo del default de plataforma.
	_, tenant := conTechoDeAgregacion(t, db, 60)
	// Y otro que nunca nombró la columna: tiene que seguir naciendo con el DEFAULT del
	// esquema, o sea que el `SET DEFAULT` sobrevive al replay.
	_, tenantSinNombrarla := conVentanaDeAgregacion(t, db, 45)

	invalidaElHashDelEsquema(t, db)
	res, err := migrations.Migrate(ctx, db)
	if err != nil {
		t.Fatalf("segundo arranque (full-replay): %v", err)
	}
	if res.Skipped {
		t.Fatal("Migrate devolvió Skipped=true: el segundo arranque NO reejecutó nada y este test sale hueco")
	}

	if got := techoCrudo(t, db, tenant); got != 60 {
		t.Fatalf("tras el segundo arranque el techo del tenant = %d; ESPERADO 60. Si volvió a 120, el "+
			"backfill perdió su guarda `WHERE … IS NULL` y pisa la elección del tenant en CADA arranque", got)
	}
	if got := techoCrudo(t, db, tenantSinNombrarla); got != 120 {
		t.Fatalf("la fila que no nombró la columna tiene el techo en %d; ESPERADO 120 (el DEFAULT del "+
			"esquema). Un valor distinto significa que el `SET DEFAULT` no sobrevivió al replay", got)
	}
}

// TestIntegration_AggregationMax_LaColumnaTieneLaFormaDeLa0076 es el test aburrido y el
// primero que se pone rojo si alguien toca la migración: la columna existe con la forma
// que el SELECT de OCHO columnas de GetTenantSettings da por hecha, y su CHECK MUERDE.
//
// SALIDAS ESPERADAS (information_schema.columns, tabla tenant_settings):
//
//	column_name             | data_type | is_nullable | column_default
//	aggregation_max_seconds | integer   | NO          | 120
//
// Y además:
//
//	INSERT … (aggregation_max_seconds) VALUES (-1)
//	  → ERROR: viola tenant_settings_aggregation_max_check
func TestIntegration_AggregationMax_LaColumnaTieneLaFormaDeLa0076(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	var tipo, nullable string
	var porDefecto sql.NullString
	err := db.QueryRowContext(ctx, `
		SELECT data_type, is_nullable, column_default
		  FROM information_schema.columns
		 WHERE table_schema = 'public'
		   AND table_name   = 'tenant_settings'
		   AND column_name  = 'aggregation_max_seconds'
	`).Scan(&tipo, &nullable, &porDefecto)
	if err != nil {
		t.Fatalf("la columna aggregation_max_seconds no existe o no se pudo describir: %v", err)
	}
	if tipo != "integer" || nullable != "NO" {
		t.Fatalf("aggregation_max_seconds = %s/%s; ESPERADO integer/NO", tipo, nullable)
	}
	if !porDefecto.Valid || porDefecto.String != "120" {
		t.Fatalf("column_default = %v; ESPERADO 120. Sin default, toda fila nueva que no nombre la "+
			"columna reventaría por NOT NULL", porDefecto)
	}

	// EL CHECK MUERDE. Un negativo no es «esperar poco»: no significa nada.
	tenant := uuid.NewString()
	t.Cleanup(func() {
		if _, derr := db.ExecContext(context.Background(),
			`DELETE FROM public.tenant_settings WHERE tenant_id = $1`, tenant); derr != nil {
			t.Logf("limpiando tenant_settings de %s: %v", tenant, derr)
		}
	})
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.tenant_settings (tenant_id, page_size, aggregation_max_seconds)
		VALUES ($1, 5, -1)
	`, tenant); err == nil {
		t.Fatal("un techo NEGATIVO entró en la tabla; ESPERADO que tenant_settings_aggregation_max_check " +
			"lo rechazara")
	}
}
