// conversation_ttl_integration_test.go prueba contra Postgres real los criterios (a),
// (b) y (d) de T4.4 (Plan 046 · Ola 4, D-046.12, REQ-19): que conversation_ttl_seconds
// deja de nacer en 0 y que el backfill NO puede vivir dentro de la migración.
//
// Va contra la BD y no solo contra el gemelo porque las tres cosas que mide solo
// existen en el esquema: el DEFAULT de una columna, la diferencia entre «no hay fila»
// y «hay fila», y el comportamiento del runner FULL-REPLAY al reejecutar la estructura.
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

// ttlConversacionalDeLaBD lee la columna CRUDA, no el struct de Go. La distinción es
// el criterio (b) entero: el struct pasa por DefaultTenantSettings y por el mapeo del
// repositorio, así que leerlo de ahí no distinguiría «la BD puso 7200» de «Go rellenó
// el hueco». Solo el SELECT responde por el DEFAULT del esquema.
func ttlConversacionalDeLaBD(t *testing.T, db *sql.DB, tenantID string) int {
	t.Helper()
	var secs int
	if err := db.QueryRowContext(context.Background(),
		`SELECT conversation_ttl_seconds FROM public.tenant_settings WHERE tenant_id = $1`,
		tenantID).Scan(&secs); err != nil {
		t.Fatalf("leer conversation_ttl_seconds de %s: %v", tenantID, err)
	}
	return secs
}

// borrarTenantSettings deja la tabla como estaba: estos tests miden un DEFAULT global,
// y la suite corre serializada contra una base compartida.
func borrarTenantSettings(t *testing.T, db *sql.DB, tenantID string) {
	t.Helper()
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(),
			`DELETE FROM public.tenant_settings WHERE tenant_id = $1`, tenantID); err != nil {
			t.Logf("limpiando tenant_settings de %s: %v", tenantID, err)
		}
	})
}

// TestIntegration_GetTenantSettings_ConversacionSinFila es el criterio (a) contra
// Postgres: el tenant que nunca configuró nada obtiene 2 h.
//
// 📌 Este camino NO lee el DEFAULT de la columna —no hay fila que leer—: devuelve
// DefaultTenantSettings por ErrNoRows. Por eso T4.4 tiene DOS mitades y la migración
// sola no bastaba: en UAT los tenants sin fila son 2 de 3.
func TestIntegration_GetTenantSettings_ConversacionSinFila(t *testing.T) {
	db := openTestDB(t)
	repo := store.NewPostgresRepository(db)

	got, err := repo.GetTenantSettings(context.Background(), uuid.NewString())
	if err != nil {
		t.Fatalf("GetTenantSettings (sin fila): %v", err)
	}
	if got.ConversationTTL != 2*time.Hour {
		t.Fatalf("ConversationTTL sin fila = %v, quiero 2h (7200s). Un 0 aquí es el "+
			"hallazgo de privacidad del Plan 046: el estado del cliente no caduca nunca",
			got.ConversationTTL)
	}
}

// TestIntegration_TenantNuevo_NaceCon7200EnLaBD es el criterio (b): una fila insertada
// SIN nombrar la columna recibe el DEFAULT del esquema.
//
// 💥 MUTACIÓN: quitar el ALTER de la migración 0067 ⇒ la fila nace en 0 y esto se pone
// ROJO. Es la mitad SQL de la tarea, la única que este test mide.
func TestIntegration_TenantNuevo_NaceCon7200EnLaBD(t *testing.T) {
	db := openTestDB(t)
	tenant := uuid.NewString()
	borrarTenantSettings(t, db, tenant)

	// La columna NO se nombra a propósito: nombrarla probaría el INSERT, no el DEFAULT.
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO public.tenant_settings (tenant_id, page_size) VALUES ($1, 5)`,
		tenant); err != nil {
		t.Fatalf("insertar tenant nuevo: %v", err)
	}

	if secs := ttlConversacionalDeLaBD(t, db, tenant); secs != 7200 {
		t.Fatalf("un tenant nuevo nació con conversation_ttl_seconds = %d, quiero 7200: "+
			"el DEFAULT de la columna sigue en el 0 que puso la 0034", secs)
	}
}

// TestIntegration_BackfillConversationTTL_RespetaElValorPropioYSobreviveAlReplay es el
// criterio (d), y es la aserción que distingue esta tarea de la versión que el acta
// proponía.
//
// 🔴 QUÉ VIGILA, EXACTAMENTE: que el backfill viva en el runbook y NO dentro de la
// migración. El runner de este repo es hash-based FULL-REPLAY, así que un UPDATE
// incondicional en un structure/*.sql no corre «una vez»: corre en CADA arranque que
// recalcule el hash. Y como `0` es un valor LEGÍTIMO («sin vencimiento»), un tenant que
// lo eligiera —o que eligiera cualquier otro valor propio— vería su decisión pisada
// para siempre, sin error y sin rastro.
//
// 💥 MUTACIÓN PRESCRITA POR EL CRITERIO (d): mover el UPDATE del runbook
// (docs/runbooks/backfill-046-conversation-ttl.sql) DENTRO de la 0067, sin su guarda
// `WHERE conversation_ttl_seconds = 0` ⇒ la segunda fase de este test se pone ROJA: los
// 900 vuelven a 7200 en el replay. Con el reparto correcto, se quedan en 900.
func TestIntegration_BackfillConversationTTL_RespetaElValorPropioYSobreviveAlReplay(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	propio := uuid.NewString()   // eligió 900s: ni el default viejo ni el nuevo.
	heredado := uuid.NewString() // arrastra el 0 de la 0034, que nadie eligió.
	borrarTenantSettings(t, db, propio)
	borrarTenantSettings(t, db, heredado)

	for _, caso := range []struct {
		tenant string
		secs   int
	}{{propio, 900}, {heredado, 0}} {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO public.tenant_settings (tenant_id, page_size, conversation_ttl_seconds)
			VALUES ($1, 5, $2)
		`, caso.tenant, caso.secs); err != nil {
			t.Fatalf("sembrar tenant %s con %ds: %v", caso.tenant, caso.secs, err)
		}
	}

	// ── Fase 1 · el backfill del runbook, literal (guarda incluida) ──────────────
	if _, err := db.ExecContext(ctx, `
		UPDATE public.tenant_settings
		   SET conversation_ttl_seconds = 7200
		 WHERE conversation_ttl_seconds = 0
	`); err != nil {
		t.Fatalf("ejecutar el backfill del runbook: %v", err)
	}

	if secs := ttlConversacionalDeLaBD(t, db, heredado); secs != 7200 {
		t.Fatalf("el 0 heredado quedó en %d, quiero 7200: el backfill no hizo su trabajo", secs)
	}
	if secs := ttlConversacionalDeLaBD(t, db, propio); secs != 900 {
		t.Fatalf("el valor propio quedó en %d, quiero 900: la guarda `WHERE = 0` del "+
			"runbook no está protegiendo al tenant que sí eligió", secs)
	}

	// ── Fase 2 · el SEGUNDO ARRANQUE CON FULL-REPLAY DE VERDAD ──────────────────
	//
	// 🔴 NO BASTA CON LLAMAR A Migrate, Y ESTE TEST NACIÓ HUECO POR ESO. isUpToDate
	// compara versión Y hash (migrations/schema.go:81-83), y openTestDB ya aplicó la
	// estructura al abrir la conexión: para cuando llegamos aquí el hash de la BD ya
	// coincide con el de los ficheros, así que un Migrate a secas es un NO-OP y no
	// reejecuta nada. Medido: con el UPDATE metido a propósito dentro de la 0067, la
	// versión anterior de este test seguía en VERDE.
	//
	// Se invalida el hash registrado, que es EXACTAMENTE lo que ocurre en producción
	// cuando alguien toca un structure/*.sql: la BD deja de estar al día y el runner
	// reejecuta TODA la estructura. Solo así el replay es real y la aserción de abajo
	// significa algo.
	if _, err := db.ExecContext(ctx,
		`UPDATE public.schema_version SET content_hash = 'forzar-replay-t44'`); err != nil {
		t.Fatalf("invalidar el hash del esquema para forzar el replay: %v", err)
	}
	res, err := migrations.Migrate(ctx, db)
	if err != nil {
		t.Fatalf("segundo arranque (full-replay de migraciones): %v", err)
	}
	if res.Skipped {
		t.Fatalf("el runner se saltó la reejecución (skipped=true) pese al hash invalidado: "+
			"este test no está probando el full-replay, y sin replay la aserción de abajo "+
			"pasaría siempre. Resultado: %+v", res)
	}

	if secs := ttlConversacionalDeLaBD(t, db, propio); secs != 900 {
		t.Fatalf("tras el full-replay el valor propio es %d, quiero 900. ⇒ ALGUIEN METIÓ EL "+
			"UPDATE DEL BACKFILL DENTRO DE UNA MIGRACIÓN: el runner es hash-based full-replay, "+
			"así que ese UPDATE no corre una vez sino en cada arranque, y pisa la elección del "+
			"cliente para siempre. El backfill va en docs/runbooks/backfill-046-conversation-ttl.sql",
			secs)
	}
	if secs := ttlConversacionalDeLaBD(t, db, heredado); secs != 7200 {
		t.Fatalf("tras el full-replay el heredado es %d, quiero 7200: el replay deshizo el backfill", secs)
	}
}
