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
)

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
