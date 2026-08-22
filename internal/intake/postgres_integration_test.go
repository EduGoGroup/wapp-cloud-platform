// postgres_integration_test.go — Plan 044 · Ola 1 · T1.1, criterio «test de
// INTEGRACIÓN» (tasks.md:261), contra Postgres REAL.
//
// ⏳ NINGUNO DE ESTOS TESTS SE HA EJECUTADO. Se escribieron en un entorno sin Go, sin
// Docker y sin Postgres, así que ninguno está declarado como pasado. Cada aserción
// lleva escrita LA SALIDA QUE SE ESPERA para que quien los corra sepa qué debería ver.
//
// # POR QUÉ NO BASTA LA SUITE DE UNIDAD, DICHO CON LO QUE CADA TEST MIDE
//
// El agregador se prueba hoy contra `intake.MemoryStore`, un doble que REPLICA A MANO
// cuatro cosas que en producción las hace Postgres y nadie más:
//
//   - el ÍNDICE ÚNICO PARCIAL `intake_jobs_ventana_viva_uidx` (una ventana viva por
//     tupla, y solo mientras está 'aggregating');
//   - la INFERENCIA del `ON CONFLICT (…) WHERE status = 'aggregating'` sobre ese
//     índice —que si el predicado no coincide EXACTAMENTE no compila en ejecución y
//     da «there is no unique or exclusion constraint matching…»—;
//   - el `||` de jsonb, que CONCATENA arrays (no los anida ni los sustituye);
//   - la subconsulta de `putSourceTextSQL` y su guard `source_text_enc IS NULL`.
//
// Un doble desincronizado de ese SQL deja una suite VERDE que no prueba nada. Esto es
// lo que cierra ese hueco.
//
// # CÓMO SE CORRE
//
// Igual que el resto de la casa: sin build tag, por el NOMBRE del fichero
// (`*_integration_test.go`) y con la variable `WAPP_TEST_DB_DSN`. Sin ella los tests
// se SALTAN solos (`make test` sigue verde); con `WAPP_TEST_REQUIRE_DB` puesta, un
// salto pasa a ser un fallo. `make test-integration` levanta el Postgres efímero.
//
// # CERO FK, Y POR ESO NO SE SIEMBRA NINGÚN TENANT
//
// `intake_jobs` no tiene NI UNA foreign key: `tenant_id`/`session_id`/`contact_id`
// son TEXT y `event_id` es UUID sin `REFERENCES` (FK LÓGICA, 0072 sección A). Así que
// las claves de estos tests son UUID inventados y cada test limpia LO SUYO por
// `tenant_id`, que es único por test: la suite corre serializada contra una base
// compartida y nadie puede pisar a nadie.
package intake_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
)

// dsnEnv habilita los tests de integración con BD real (igual que en flujos/store,
// flujos/events y lease: la casa tiene UNA variable para esto).
const dsnEnv = "WAPP_TEST_DB_DSN"

// openTestDB abre la conexión de test o salta si no hay BD configurada. Es copia
// LITERAL del helper de internal/flujos/events/store_integration_test.go: no se
// inventa un mecanismo nuevo.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		if os.Getenv("WAPP_TEST_REQUIRE_DB") != "" {
			t.Fatalf("%s no definido pero WAPP_TEST_REQUIRE_DB exige BD: la integración DEBE correr", dsnEnv)
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

// claveDeVentana fabrica una WindowKey ÚNICA por test y programa su limpieza. El
// tenant es un UUID nuevo cada vez: es lo que hace que dos tests no puedan verse.
func claveDeVentana(t *testing.T, db *sql.DB) intake.WindowKey {
	t.Helper()
	k := intake.WindowKey{
		TenantID:  uuid.NewString(),
		SessionID: "sess-" + uuid.NewString(),
		ContactID: "c-" + uuid.NewString(),
		EventID:   uuid.NewString(),
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(),
			`DELETE FROM public.intake_jobs WHERE tenant_id = $1`, k.TenantID); err != nil {
			t.Logf("limpiando intake_jobs de %s: %v", k.TenantID, err)
		}
	})
	return k
}

// filaJob es lo que la BD guardó de verdad, leído con SQL DIRECTO y no por el store.
// La distinción es el fichero entero: un store puede devolver lo que quiera; estas
// columnas son lo que hay.
type filaJob struct {
	id         string
	status     string
	messageTS  sql.NullTime
	sourceRefs []byte
	textEnc    []byte
	textDEK    []byte
	textKEKID  sql.NullString
}

// jobsDeLaTupla lee TODAS las filas de una tupla de ventana, las más viejas primero.
func jobsDeLaTupla(t *testing.T, db *sql.DB, k intake.WindowKey) []filaJob {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `
		SELECT id::text, status, message_ts, source_refs,
		       source_text_enc, source_text_dek, source_text_kek_id
		  FROM public.intake_jobs
		 WHERE tenant_id = $1 AND session_id = $2 AND contact_id = $3 AND event_id = $4::uuid
		 ORDER BY created_at, id
	`, k.TenantID, k.SessionID, k.ContactID, k.EventID)
	if err != nil {
		t.Fatalf("leer intake_jobs de la tupla: %v", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			t.Errorf("cerrar filas de intake_jobs: %v", cerr)
		}
	}()

	var out []filaJob
	for rows.Next() {
		var f filaJob
		if serr := rows.Scan(&f.id, &f.status, &f.messageTS, &f.sourceRefs,
			&f.textEnc, &f.textDEK, &f.textKEKID); serr != nil {
			t.Fatalf("escanear intake_jobs: %v", serr)
		}
		out = append(out, f)
	}
	if rerr := rows.Err(); rerr != nil {
		t.Fatalf("iterar intake_jobs: %v", rerr)
	}
	return out
}

// refsDe decodifica `source_refs`. Falla si NO es un array plano de strings, y eso es
// parte de la aserción: si el `||` anidara en vez de concatenar, la fila traería
// `[["a"],["b"]]` y este Unmarshal daría error en vez de un falso verde.
func refsDe(t *testing.T, raw []byte) []string {
	t.Helper()
	var refs []string
	if err := json.Unmarshal(raw, &refs); err != nil {
		t.Fatalf("source_refs = %s; esperado un ARRAY PLANO de strings (p. ej. [\"wamid.a\",\"wamid.b\"]). "+
			"Un error aquí suele significar que el `||` de jsonb anidó en vez de concatenar: %v", raw, err)
	}
	return refs
}

// ---------------------------------------------------------------------------
// (1) EL `ON CONFLICT` INFIERE DE VERDAD EL ÍNDICE PARCIAL
// ---------------------------------------------------------------------------

// TestIntegration_OpenOrAppend_DosMensajesUnaSolaVentana es EL test del `ON CONFLICT`
// y no se puede escribir contra un doble: lo que se afirma es que Postgres INFIERE el
// índice `intake_jobs_ventana_viva_uidx` a partir del predicado
// `WHERE status = 'aggregating'` escrito en la sentencia.
//
// 🔴 SI EL PREDICADO NO CASARA, este test no fallaría en una aserción: fallaría en el
// primer `OpenOrAppend` con «there is no unique or exclusion constraint matching the
// ON CONFLICT specification». Ese error NO PUEDE OCURRIR contra el doble en memoria, y
// por eso T1.1 pide integración.
//
// SALIDAS ESPERADAS:
//   - filas de la tupla ........ 1  (dos Append, UNA ventana)
//   - status ................... "aggregating"
//   - source_refs .............. ["wamid.uno","wamid.dos"]  (LAS DOS, EN ORDEN)
//   - message_ts ............... 2026-08-22T10:00:00Z  (el del PRIMERO, no el del segundo)
func TestIntegration_OpenOrAppend_DosMensajesUnaSolaVentana(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	jobs := intake.NewPostgres(db)
	k := claveDeVentana(t, db)

	primero := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	segundo := primero.Add(7 * time.Second)

	if err := jobs.OpenOrAppend(ctx, intake.Append{Key: k, MessageTS: primero, Refs: []string{"wamid.uno"}}); err != nil {
		t.Fatalf("OpenOrAppend (abre la ventana): %v — un error aquí que mencione «no unique or exclusion "+
			"constraint matching the ON CONFLICT specification» significa que el predicado de la sentencia "+
			"y el del índice parcial de la 0072 han dejado de ser idénticos", err)
	}
	if err := jobs.OpenOrAppend(ctx, intake.Append{Key: k, MessageTS: segundo, Refs: []string{"wamid.dos"}}); err != nil {
		t.Fatalf("OpenOrAppend (amplía la ventana): %v", err)
	}

	filas := jobsDeLaTupla(t, db, k)
	if len(filas) != 1 {
		t.Fatalf("filas de la tupla = %d; ESPERADO 1 — dos mensajes seguidos son UNA ventana (D-044.3). "+
			"Dos filas significa que el ON CONFLICT no mordió", len(filas))
	}
	f := filas[0]

	if f.status != intake.StatusAggregating {
		t.Fatalf("status = %q; ESPERADO %q — el camino del entrante NUNCA cierra una ventana (D-044.26)",
			f.status, intake.StatusAggregating)
	}

	refs := refsDe(t, f.sourceRefs)
	quiero := []string{"wamid.uno", "wamid.dos"}
	if len(refs) != 2 || refs[0] != quiero[0] || refs[1] != quiero[1] {
		t.Fatalf("source_refs = %v; ESPERADO %v — las DOS referencias y EN ORDEN. El orden es contrato: "+
			"es el rastro que sobrevive al vaciado del sobre en estado terminal (INV-13)", refs, quiero)
	}

	if !f.messageTS.Valid {
		t.Fatalf("message_ts = NULL; ESPERADO %v — lo pone el INSERT del PRIMER mensaje", primero)
	}
	if !f.messageTS.Time.UTC().Equal(primero) {
		t.Fatalf("message_ts = %v; ESPERADO %v (el del PRIMER mensaje), NO %v (el del segundo). "+
			"`message_ts` está en el INSERT y NO en el DO UPDATE justo para esto: es la BASE DE FECHAS "+
			"del presupuesto (D-044.9), y anclarla en el segundo mensaje resuelve «para el jueves» contra "+
			"el instante equivocado", f.messageTS.Time.UTC(), primero, segundo)
	}
}

// ---------------------------------------------------------------------------
// (2) EL ÍNDICE MUERDE, Y ES PARCIAL — LAS DOS MITADES
// ---------------------------------------------------------------------------

// TestIntegration_ElIndiceEsPARCIAL_UnaVentanaCerradaDejaAbrirOtra es la mitad que un
// índice ÚNICO TOTAL rompería, y por eso va primero: con el total, un cliente NO
// PODRÍA VOLVER A PEDIR sobre la misma conversación — el segundo `OpenOrAppend`
// reventaría con violación de unicidad en vez de abrir una ventana nueva.
//
// SALIDAS ESPERADAS:
//   - tras el primer Append + CloseWindow ..... 1 fila, status "pending"
//   - tras el segundo Append .................. 2 filas: ["pending", "aggregating"]
//   - la NUEVA lleva SOLO ["wamid.dos"] ....... la vieja NO se amplió
func TestIntegration_ElIndiceEsPARCIAL_UnaVentanaCerradaDejaAbrirOtra(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	jobs := intake.NewPostgres(db)
	k := claveDeVentana(t, db)

	if err := jobs.OpenOrAppend(ctx, intake.Append{
		Key: k, MessageTS: time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC), Refs: []string{"wamid.uno"},
	}); err != nil {
		t.Fatalf("OpenOrAppend (primera ventana): %v", err)
	}
	cerrada, err := jobs.CloseWindow(ctx, k)
	if err != nil {
		t.Fatalf("CloseWindow: %v", err)
	}
	if !cerrada {
		t.Fatal("CloseWindow = false; ESPERADO true — esta llamada es la que cierra la ventana viva")
	}

	// La MISMA tupla vuelve a pedir. Con el índice parcial esto abre una fila NUEVA.
	if err := jobs.OpenOrAppend(ctx, intake.Append{
		Key: k, MessageTS: time.Date(2026, 8, 22, 11, 0, 0, 0, time.UTC), Refs: []string{"wamid.dos"},
	}); err != nil {
		t.Fatalf("OpenOrAppend (segunda ventana): %v — un error de unicidad AQUÍ significa que el índice "+
			"perdió su `WHERE status = 'aggregating'` y pasó a ser TOTAL: el cliente no podría volver a "+
			"pedir sobre el mismo evento", err)
	}

	filas := jobsDeLaTupla(t, db, k)
	if len(filas) != 2 {
		t.Fatalf("filas de la tupla = %d; ESPERADO 2 — la cerrada y la nueva. El parcial vigila las VIVAS "+
			"y deja el histórico en paz (0072 · D.2)", len(filas))
	}
	// Se buscan POR STATUS y no por posición: `created_at` las ordena bien en la
	// práctica, pero atar la aserción a ese orden convertiría un empate de reloj en un
	// rojo que no habla de lo que este test mide.
	var cerradaFila, vivaFila *filaJob
	for i := range filas {
		switch filas[i].status {
		case intake.StatusPending:
			cerradaFila = &filas[i]
		case intake.StatusAggregating:
			vivaFila = &filas[i]
		}
	}
	if cerradaFila == nil || vivaFila == nil {
		t.Fatalf("status de las dos filas = %v; ESPERADO una en %q y otra en %q",
			estados(filas), intake.StatusPending, intake.StatusAggregating)
	}
	if refs := refsDe(t, vivaFila.sourceRefs); len(refs) != 1 || refs[0] != "wamid.dos" {
		t.Fatalf("source_refs de la ventana NUEVA (aggregating) = %v; ESPERADO [\"wamid.dos\"] — la "+
			"referencia del segundo pedido NO puede acabar en la ventana ya cerrada", refs)
	}
	if refs := refsDe(t, cerradaFila.sourceRefs); len(refs) != 1 || refs[0] != "wamid.uno" {
		t.Fatalf("source_refs de la ventana CERRADA (pending) = %v; ESPERADO [\"wamid.uno\"] — cerrarla la "+
			"sacó del índice, así que ya no puede recibir referencias nuevas", refs)
	}
}

// TestIntegration_ElIndiceUnicoMuerde_DosVentanasVivasALaVez es la otra mitad, y va
// A MANO —sin pasar por el store— a propósito: `OpenOrAppend` NUNCA produce esta
// situación porque su `ON CONFLICT` la absorbe. Lo que se prueba aquí es que la
// REGLA vive en Postgres y no en el código Go: aunque alguien escribiera un segundo
// escritor mañana, la base seguiría impidiendo dos ventanas vivas para una tupla.
//
// Es el «precio aceptado y escrito» de la 0072 · D.2: dos ventanas vivas dejan de ser
// un caso raro que duplicaba en silencio y pasan a ser un ERROR DE BASE DE DATOS.
//
// SALIDAS ESPERADAS:
//   - primer INSERT a mano ..... OK, 1 fila
//   - segundo INSERT a mano .... ERROR cuyo texto contiene "intake_jobs_ventana_viva_uidx"
//     (pq: duplicate key value violates unique constraint …)
func TestIntegration_ElIndiceUnicoMuerde_DosVentanasVivasALaVez(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	k := claveDeVentana(t, db)

	const insertACrudo = `
		INSERT INTO public.intake_jobs (tenant_id, session_id, contact_id, event_id, status)
		VALUES ($1, $2, $3, $4::uuid, 'aggregating')`

	if _, err := db.ExecContext(ctx, insertACrudo, k.TenantID, k.SessionID, k.ContactID, k.EventID); err != nil {
		t.Fatalf("primer INSERT a mano: %v — ESPERADO que pasara sin más", err)
	}
	_, err := db.ExecContext(ctx, insertACrudo, k.TenantID, k.SessionID, k.ContactID, k.EventID)
	if err == nil {
		t.Fatal("el segundo INSERT a mano PASÓ; ESPERADO un error de unicidad. Sin ese índice, dos ventanas " +
			"vivas para la misma tupla conviven y el cliente acaba con DOS presupuestos del mismo pedido")
	}
	if !strings.Contains(err.Error(), "intake_jobs_ventana_viva_uidx") {
		t.Fatalf("el segundo INSERT falló con %v; ESPERADO que el error nombrara "+
			"«intake_jobs_ventana_viva_uidx». Que falle por OTRA constraint no prueba lo que este test dice "+
			"probar", err)
	}
}

// ---------------------------------------------------------------------------
// (3) EL `||` DE JSONB CONCATENA: NI ANIDA, NI SUSTITUYE
// ---------------------------------------------------------------------------

// TestIntegration_SourceRefsSeConcatenanPlano ataca las DOS formas equivocadas de
// juntar arrays, que dan resultados distintos y ninguna da error:
//
//	sustituir → ["wamid.tres"]                      (se pierden las anteriores)
//	anidar    → [["wamid.uno","wamid.dos"],[...]]   (el worker no sabe leerlas)
//	concatenar→ ["wamid.uno","wamid.dos","wamid.tres"]   ← lo correcto
//
// Se usa un primer Append con DOS refs (el caso del mensaje con media, Plan 017: su
// `wa_message_id` más la referencia del adjunto) porque es el que distingue de verdad
// el anidado del plano.
//
// SALIDAS ESPERADAS:
//   - jsonb_typeof(source_refs) ...... "array"
//   - jsonb_array_length(source_refs)  3
//   - source_refs .................... ["wamid.uno","media.uno","wamid.tres"]
func TestIntegration_SourceRefsSeConcatenanPlano(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	jobs := intake.NewPostgres(db)
	k := claveDeVentana(t, db)

	if err := jobs.OpenOrAppend(ctx, intake.Append{
		Key: k, MessageTS: time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC),
		Refs: []string{"wamid.uno", "media.uno"},
	}); err != nil {
		t.Fatalf("OpenOrAppend (con dos refs): %v", err)
	}
	if err := jobs.OpenOrAppend(ctx, intake.Append{Key: k, Refs: []string{"wamid.tres"}}); err != nil {
		t.Fatalf("OpenOrAppend (tercera ref): %v", err)
	}

	var tipo string
	var largo int
	if err := db.QueryRowContext(ctx, `
		SELECT jsonb_typeof(source_refs), jsonb_array_length(source_refs)
		  FROM public.intake_jobs
		 WHERE tenant_id = $1 AND status = 'aggregating'`, k.TenantID).Scan(&tipo, &largo); err != nil {
		t.Fatalf("leer la forma de source_refs: %v — un error de jsonb_array_length aquí significa que la "+
			"columna dejó de ser un array", err)
	}
	if tipo != "array" {
		t.Fatalf("jsonb_typeof(source_refs) = %q; ESPERADO \"array\"", tipo)
	}
	if largo != 3 {
		t.Fatalf("jsonb_array_length(source_refs) = %d; ESPERADO 3. Un 2 significa que el DO UPDATE "+
			"SUSTITUYÓ en vez de concatenar; un 2 con elementos que son arrays significa que ANIDÓ", largo)
	}

	filas := jobsDeLaTupla(t, db, k)
	if len(filas) != 1 {
		t.Fatalf("filas de la tupla = %d; ESPERADO 1", len(filas))
	}
	refs := refsDe(t, filas[0].sourceRefs)
	quiero := []string{"wamid.uno", "media.uno", "wamid.tres"}
	if fmt.Sprint(refs) != fmt.Sprint(quiero) {
		t.Fatalf("source_refs = %v; ESPERADO %v", refs, quiero)
	}
}

// ---------------------------------------------------------------------------
// (4) EL GUARD DE CloseWindow — EL QUE LA SUITE DE UNIDAD NO CUBRE
// ---------------------------------------------------------------------------

// TestIntegration_CloseWindow_ElSegundoCierreNoTocaFila es el que faltaba, y hay que
// decir por qué faltaba: la suite de unidad afirma la idempotencia POR EL FILTRO DE LA
// LECTURA (mira cuántas ventanas quedan abiertas), no por el GUARD. Son cosas
// distintas — un `UPDATE` sin `AND status = 'aggregating'` dejaría la lectura igual de
// contenta y, sin embargo, volvería a tocar la fila.
//
// Y eso importa porque de ese guard cuelga que el FLUSH POR INTENT y el FLUSH POR
// VENTANA no puedan producir DOS jobs del mismo pedido: el segundo en llegar tiene que
// afectar 0 filas.
//
// Se llama a CloseWindow DIRECTAMENTE y no por `Sweep`: el barrido tiene su propio
// reloj y sus propias guardas, y meterlas por medio haría que un verde aquí pudiera
// venir de que el barrido ni siquiera llegó a llamar.
//
// SALIDAS ESPERADAS:
//   - primer CloseWindow ....... true   (esta llamada fue la que cerró)
//   - segundo CloseWindow ...... false, err == nil   (no-op, NO es un error)
//   - status final ............. "pending"  (una sola fila, sin duplicar)
//   - updated_at ............... IDÉNTICO antes y después del segundo cierre
//
// 🔴 La aserción de `updated_at` es la que separa «idempotente» de «no toca la fila».
// Sin ella, un UPDATE sin guard pasaría este test en verde.
func TestIntegration_CloseWindow_ElSegundoCierreNoTocaFila(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	jobs := intake.NewPostgres(db)
	k := claveDeVentana(t, db)

	if err := jobs.OpenOrAppend(ctx, intake.Append{
		Key: k, MessageTS: time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC), Refs: []string{"wamid.uno"},
	}); err != nil {
		t.Fatalf("OpenOrAppend: %v", err)
	}

	primera, err := jobs.CloseWindow(ctx, k)
	if err != nil {
		t.Fatalf("CloseWindow (primera): %v", err)
	}
	if !primera {
		t.Fatal("CloseWindow (primera) = false; ESPERADO true — es la llamada que cierra")
	}

	antes := actualizadoEn(t, db, k)

	segunda, err := jobs.CloseWindow(ctx, k)
	if err != nil {
		t.Fatalf("CloseWindow (segunda) = error %v; ESPERADO nil — un segundo cierre es un NO-OP, no un fallo", err)
	}
	if segunda {
		t.Fatal("CloseWindow (segunda) = true; ESPERADO false — si las dos llamadas dicen «yo la cerré», " +
			"el flush por intent y el flush por ventana producen DOS jobs del mismo pedido")
	}

	despues := actualizadoEn(t, db, k)
	if !despues.Equal(antes) {
		t.Fatalf("updated_at cambió con el segundo cierre: antes=%v después=%v; ESPERADO que fueran "+
			"IDÉNTICOS. Que cambie significa que el UPDATE tocó la fila, o sea que perdió su guard "+
			"`AND status = 'aggregating'`", antes, despues)
	}

	filas := jobsDeLaTupla(t, db, k)
	if len(filas) != 1 || filas[0].status != intake.StatusPending {
		t.Fatalf("filas = %d con status %v; ESPERADO 1 fila en %q", len(filas), estados(filas), intake.StatusPending)
	}
}

// actualizadoEn lee `updated_at` de la ÚNICA fila de la tupla. Falla si hay más de una:
// en los tests que lo usan eso sería el propio defecto.
func actualizadoEn(t *testing.T, db *sql.DB, k intake.WindowKey) time.Time {
	t.Helper()
	var ts time.Time
	if err := db.QueryRowContext(context.Background(), `
		SELECT updated_at FROM public.intake_jobs
		 WHERE tenant_id = $1 AND session_id = $2 AND contact_id = $3 AND event_id = $4::uuid
	`, k.TenantID, k.SessionID, k.ContactID, k.EventID).Scan(&ts); err != nil {
		t.Fatalf("leer updated_at (¿hay más de una fila para la tupla?): %v", err)
	}
	return ts
}

// estados aplana los status para un mensaje de error legible.
func estados(filas []filaJob) []string {
	out := make([]string, 0, len(filas))
	for _, f := range filas {
		out = append(out, f.status)
	}
	return out
}

// ---------------------------------------------------------------------------
// (5) PutSourceText — LAS TRES PIEZAS, Y EL GUARD QUE NO SOBRESCRIBE
// ---------------------------------------------------------------------------

// TestIntegration_PutSourceText_EscribeLasTresYNoSobrescribe cubre las dos mitades del
// sobre en la MISMA fila, porque son inseparables: escribirlo entero, y no volver a
// escribirlo encima.
//
// Las tres columnas son NULLables en la 0072 A PROPÓSITO (durante 'aggregating' están
// legítimamente vacías), así que Postgres NO puede sostener el «las tres o ninguna»:
// vive en el código. Esto lo comprueba contra la tabla real.
//
// SALIDAS ESPERADAS:
//   - PutSourceText (1.ª) ...... true, err == nil
//   - source_text_enc .......... los bytes que se pasaron (aquí, sin cifrar de verdad:
//     este test mide la ESCRITURA, no la cripto — el cifrado real lo mide el test del
//     compositor, source_composer_integration_test.go)
//   - source_text_dek .......... los bytes que se pasaron
//   - source_text_kek_id ....... "k1"
//   - PutSourceText (2.ª, otro sobre) ...... false, err == nil   ← el guard
//   - source_text_enc ............. SIGUE siendo el primero, byte a byte
//   - PutSourceText con sobre incompleto ... false, err != nil   ← se rechaza ANTES de tocar la base
func TestIntegration_PutSourceText_EscribeLasTresYNoSobrescribe(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	jobs := intake.NewPostgres(db)
	k := claveDeVentana(t, db)

	if err := jobs.OpenOrAppend(ctx, intake.Append{
		Key: k, MessageTS: time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC), Refs: []string{"wamid.uno"},
	}); err != nil {
		t.Fatalf("OpenOrAppend: %v", err)
	}

	// Sobre a medias: se rechaza SIN tocar la base. Va antes del cierre para que, si
	// llegara a escribir, la fila viva quedara marcada y el resto del test lo delatara.
	if ok, err := jobs.PutSourceText(ctx, k, intake.SourceText{Enc: []byte("solo-enc")}); ok || err == nil {
		t.Fatalf("PutSourceText con sobre incompleto = (%v, %v); ESPERADO (false, error). Media escritura "+
			"deja una fila INDESCIFRABLE y no hay copia de la DEK en ningún otro sitio", ok, err)
	}

	// El sobre solo se puede escribir sobre una ventana YA CERRADA (status 'pending').
	if _, err := jobs.CloseWindow(ctx, k); err != nil {
		t.Fatalf("CloseWindow: %v", err)
	}

	primero := intake.SourceText{Enc: []byte("sobre-uno-bytes"), DEK: []byte("dek-uno-32-bytes"), KEKID: "k1"}
	escrito, err := jobs.PutSourceText(ctx, k, primero)
	if err != nil {
		t.Fatalf("PutSourceText (primera): %v", err)
	}
	if !escrito {
		t.Fatal("PutSourceText (primera) = false; ESPERADO true — la ventana está en `pending` y su sobre " +
			"está vacío, que son las dos condiciones de la sentencia")
	}

	f := unicaFila(t, db, k)
	if string(f.textEnc) != string(primero.Enc) {
		t.Fatalf("source_text_enc = %q; ESPERADO %q", f.textEnc, primero.Enc)
	}
	if string(f.textDEK) != string(primero.DEK) {
		t.Fatalf("source_text_dek = %q; ESPERADO %q — sin DEK la fila no se puede descifrar", f.textDEK, primero.DEK)
	}
	if !f.textKEKID.Valid || f.textKEKID.String != primero.KEKID {
		t.Fatalf("source_text_kek_id = %v; ESPERADO %q — sin él la fila queda FUERA del barrido de rotación "+
			"de KEK (Plan 012) y nadie sabría con cuál desenvolverla", f.textKEKID, primero.KEKID)
	}

	// EL GUARD `source_text_enc IS NULL`: un segundo sobre NO pisa al primero.
	segundo := intake.SourceText{Enc: []byte("sobre-DOS-bytes"), DEK: []byte("dek-dos-32-bytes"), KEKID: "k2"}
	reescrito, err := jobs.PutSourceText(ctx, k, segundo)
	if err != nil {
		t.Fatalf("PutSourceText (segunda) = error %v; ESPERADO nil — «no había dónde escribir» es "+
			"idempotencia, no un fallo", err)
	}
	if reescrito {
		t.Fatal("PutSourceText (segunda) = true; ESPERADO false — el guard `source_text_enc IS NULL` " +
			"existe para impedir que el texto de la ventana de ahora se escriba encima de otro sobre")
	}

	f = unicaFila(t, db, k)
	if string(f.textEnc) != string(primero.Enc) {
		t.Fatalf("source_text_enc tras el segundo intento = %q; ESPERADO que SIGUIERA siendo %q",
			f.textEnc, primero.Enc)
	}
	if f.textKEKID.String != primero.KEKID {
		t.Fatalf("source_text_kek_id tras el segundo intento = %q; ESPERADO %q — un sobre con la DEK de uno "+
			"y el key_id de otro es una fila que no abre nadie", f.textKEKID.String, primero.KEKID)
	}
}

// unicaFila devuelve LA fila de la tupla y falla si no hay exactamente una.
func unicaFila(t *testing.T, db *sql.DB, k intake.WindowKey) filaJob {
	t.Helper()
	filas := jobsDeLaTupla(t, db, k)
	if len(filas) != 1 {
		t.Fatalf("filas de la tupla = %d; ESPERADO exactamente 1 (%v)", len(filas), estados(filas))
	}
	return filas[0]
}
