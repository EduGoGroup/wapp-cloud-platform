// backoff_integration_test.go — Plan 044 · Ola 2 · T2.1, LA SEDE DEL BACKOFF
// (migración 0078) contra Postgres REAL.
//
// # QUÉ SE PRUEBA AQUÍ Y NO EN machine_integration_test.go
//
// Aquel fichero prueba la MÁQUINA (quién puede pasar a qué estado). Este prueba el
// RELOJ: que exista dónde escribir «vuelve luego» y que el claim lo respete. Son
// dos preguntas distintas y la segunda no tiene ni una línea de Go — vive entera en
// el `WHERE next_attempt_at <= now()` y en el `ORDER BY` de `claimNextSQL`, así que
// un doble en memoria la reescribiría a mano y la suite pasaría a probar el doble.
//
// # LO QUE ESTE FICHERO NO PRUEBA, PORQUE TODAVÍA NO EXISTE
//
// La POLÍTICA de reintentos —cuánto se empuja la marca, con qué curva, cuántos
// intentos antes de `failed`— es de T2.5. Aquí no hay un solo test que la afirme, y
// eso es deliberado: la Ola 2 · T2.1 deja la SEDE y el claim que la respeta. Quien
// escriba T2.5 encontrará las columnas y estos tests, no una política a medias.
//
// Se corre como el resto de la casa: por el NOMBRE del fichero, con
// `WAPP_TEST_DB_DSN` (sin ella se salta solo). `make test-integration` lo levanta.
// Los helpers `openTestDB` y `claveDeVentana` viven en postgres_integration_test.go,
// mismo paquete de test.
package intake_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
)

// ---------------------------------------------------------------------------
// HELPERS
// ---------------------------------------------------------------------------

// sembrarPendiente fabrica un job `pending` con SU created_at y SU next_attempt_at
// puestos a mano. Se escribe con SQL DIRECTO y no por el store a propósito: el store
// no tiene —ni tendrá en T2.1— ningún método que mueva la marca (eso es T2.5), y un
// test que solo pudiera crear jobs con el DEFAULT no podría distinguir «vencido» de
// «por vencer», que es justo lo que hay que probar.
//
// El `ctx` va PRIMERO y antes del `*testing.T`: lo exigen a la vez `contextcheck` y
// `revive/context-as-argument`.
func sembrarPendiente(ctx context.Context, t *testing.T, db *sql.DB,
	k intake.WindowKey, creado, marca time.Time) string {
	t.Helper()
	var id string
	if err := db.QueryRowContext(ctx, `
		INSERT INTO public.intake_jobs
		       (tenant_id, session_id, contact_id, event_id, status,
		        created_at, updated_at, next_attempt_at)
		VALUES ($1, $2, $3, $4::uuid, 'pending', $5, $5, $6)
		RETURNING id::text
	`, k.TenantID, k.SessionID, k.ContactID, k.EventID, creado, marca).Scan(&id); err != nil {
		t.Fatalf("sembrar el job pending (creado=%s, marca=%s): %v", creado, marca, err)
	}
	return id
}

// backoffDeLaFila lee las DOS columnas de la 0078 con SQL directo. No pasa por el
// store porque el store no las expone: lo que se afirma es lo que la BASE guardó.
//
// 🔴 EL «¿YA VENCIÓ?» LO CALCULA POSTGRES, no Go. Traerse la marca y compararla con
// `time.Now()` compararía DOS RELOJES —el del motor y el del proceso—, y el claim
// resuelve su `<= now()` con UNO solo: el del motor. Con los dos a segundos de
// distancia el test podría contradecir al claim sin que nada estuviera roto.
func backoffDeLaFila(ctx context.Context, t *testing.T, db *sql.DB, id string) (intentos int, marca time.Time, vencida bool) {
	t.Helper()
	if err := db.QueryRowContext(ctx, `
		SELECT attempts, next_attempt_at, next_attempt_at <= now()
		  FROM public.intake_jobs WHERE id = $1::uuid
	`, id).Scan(&intentos, &marca, &vencida); err != nil {
		t.Fatalf("leer el backoff de la fila %s: %v", id, err)
	}
	return intentos, marca, vencida
}

// estadoDeLaFila devuelve el `status` crudo.
func estadoDeLaFila(ctx context.Context, t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var st string
	if err := db.QueryRowContext(ctx,
		`SELECT status FROM public.intake_jobs WHERE id = $1::uuid`, id).Scan(&st); err != nil {
		t.Fatalf("leer el status de la fila %s: %v", id, err)
	}
	return st
}

// exigeColaSinAjenos es la PRECONDICIÓN de todo test que asierta sobre ClaimNext, y
// sin ella la mitad de este fichero mentiría en cualquiera de los dos sentidos.
//
// 🔴 LA COLA ES GLOBAL: `ClaimNext` no filtra por tenant (a propósito — el worker
// atiende a todos). Si otro test dejó un `pending` vivo, un «no debería reclamar
// nada» saldría ROJO sin que el guard estuviera roto, y un «debería reclamar el mío»
// se llevaría la fila equivocada. Esto lo dice ANTES, con el tenant ajeno delante,
// en vez de dejar que el fallo aparezca disfrazado de otra cosa.
func exigeColaSinAjenos(ctx context.Context, t *testing.T, db *sql.DB, mio string) {
	t.Helper()
	var n int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM public.intake_jobs WHERE status = 'pending' AND tenant_id <> $1
	`, mio).Scan(&n); err != nil {
		t.Fatalf("contando jobs pending de otros tenants: %v", err)
	}
	if n != 0 {
		t.Fatalf("hay %d jobs `pending` de OTROS tenants en la cola. La cola es GLOBAL (ClaimNext no "+
			"filtra por tenant), así que este test no puede afirmar nada sobre lo que se reclama: "+
			"algún test anterior no limpió lo suyo", n)
	}
}

// invalidaElHashDelEsquema fuerza que el SIGUIENTE Migrate reejecute TODOS los
// `structure/*.sql` en vez de saltárselos.
//
// 🔴 SIN ESTO, UN TEST QUE «SIMULA EL SEGUNDO ARRANQUE» SALE HUECO. `Migrate` compara
// versión Y hash de contenido (`isUpToDate`, schema.go): con los dos iguales devuelve
// `Skipped=true` y NO TOCA LA BASE, así que el test estaría afirmando que no pasa
// nada... porque no se ejecutó nada. Se escribe una fila NUEVA en
// `public.schema_version` con un hash imposible —`readSchemaVersion` lee la última
// por `id DESC`— y el replay entra por el camino de verdad. Lo prueba el propio
// llamante asertando `Skipped == false`. Mismo mecanismo que en
// internal/flujos/store y internal/integrations: no se inventa uno nuevo.
func invalidaElHashDelEsquema(ctx context.Context, t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.schema_version (version, content_hash, description)
		VALUES ('0.0.0-test', 'hash-invalidado-por-0078-test', 'forzar full-replay en test de backfill')
	`); err != nil {
		t.Fatalf("invalidando el hash de schema_version: %v", err)
	}
}

// ---------------------------------------------------------------------------
// (1) LAS DOS COLUMNAS EXISTEN, Y CON LA FORMA EXACTA
// ---------------------------------------------------------------------------

// TestIntegration_Backoff_LasDosColumnasExistenConSuFormaExacta mira el CATÁLOGO, no
// una fila. Los tres de abajo no son adorno y cada uno protege algo distinto:
//
//   - el TIPO, porque `next_attempt_at` comparada contra `now()` tiene que ser
//     TIMESTAMPTZ: un TIMESTAMP sin zona convertiría el claim en una comparación
//     entre dos husos y el fallo sería silencioso y estacional;
//   - el NOT NULL, porque una fila con la marca a NULL sería INVISIBLE al claim
//     (`NULL <= now()` no es cierto) — un job perdido para siempre y sin error;
//   - el DEFAULT, porque es lo ÚNICO que hace reclamable a un job recién cerrado y a
//     los que ya estaban en `pending` cuando se aplicó la migración.
//
// SALIDA ESPERADA: attempts = integer NOT NULL DEFAULT 0;
// next_attempt_at = timestamp with time zone NOT NULL DEFAULT now();
// e intake_jobs_claim_idx sobre (status, next_attempt_at).
func TestIntegration_Backoff_LasDosColumnasExistenConSuFormaExacta(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	casos := []struct {
		columna    string
		tipo       string
		porDefecto string
	}{
		{"attempts", "integer", "0"},
		{"next_attempt_at", "timestamp with time zone", "now()"},
	}
	for _, c := range casos {
		var tipo, admiteNulo string
		var porDefecto sql.NullString
		if err := db.QueryRowContext(ctx, `
			SELECT data_type, is_nullable, column_default
			  FROM information_schema.columns
			 WHERE table_schema = 'public' AND table_name = 'intake_jobs' AND column_name = $1
		`, c.columna).Scan(&tipo, &admiteNulo, &porDefecto); err != nil {
			t.Fatalf("intake_jobs.%s NO EXISTE en el catálogo (%v). La 0078 es la sede del backoff: "+
				"sin ella T2.5 no tiene dónde escribir «vuelve luego» y D-044.43 no se puede cumplir",
				c.columna, err)
		}
		if tipo != c.tipo {
			t.Fatalf("intake_jobs.%s es %q; ESPERADO %q", c.columna, tipo, c.tipo)
		}
		if admiteNulo != "NO" {
			t.Fatalf("intake_jobs.%s admite NULL; ESPERADO NOT NULL. Una marca a NULL deja la fila "+
				"INVISIBLE al claim (`NULL <= now()` no es cierto): el job se pierde sin un solo error",
				c.columna)
		}
		if !porDefecto.Valid || !strings.Contains(porDefecto.String, c.porDefecto) {
			t.Fatalf("intake_jobs.%s tiene DEFAULT %q; ESPERADO uno que contenga %q",
				c.columna, porDefecto.String, c.porDefecto)
		}
	}

	var def string
	if err := db.QueryRowContext(ctx, `
		SELECT indexdef FROM pg_indexes
		 WHERE schemaname = 'public' AND indexname = 'intake_jobs_claim_idx'
	`).Scan(&def); err != nil {
		t.Fatalf("no existe intake_jobs_claim_idx (%v). El intake_jobs_window_idx de la 0072 NO sirve "+
			"a esta pregunta: empieza por la tupla de ventana y el claim no filtra por ninguna de sus "+
			"columnas", err)
	}
	if !strings.Contains(def, "status") || !strings.Contains(def, "next_attempt_at") {
		t.Fatalf("intake_jobs_claim_idx = %q; ESPERADO sobre (status, next_attempt_at)", def)
	}
}

// ---------------------------------------------------------------------------
// (2) LA MARCA FUTURA RETIENE EL JOB
// ---------------------------------------------------------------------------

// TestIntegration_Backoff_PendingConMarcaFuturaNoEsReclamable es EL test del backoff,
// y el único que se pone rojo si alguien borra el `AND next_attempt_at <= now()` del
// claim. Sin ese predicado, un job devuelto a `pending` porque el provider está caído
// se vuelve a reclamar en el acto, vuelve a fallar, y el bucle gira a la velocidad
// del error — que no es un reintento, es una tormenta.
//
// SALIDAS ESPERADAS: ClaimNext ... (_, false, nil), y la fila sigue en 'pending'.
func TestIntegration_Backoff_PendingConMarcaFuturaNoEsReclamable(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	k := claveDeVentana(t, db)
	jobs := intake.NewPostgres(db)

	ahora := time.Now().UTC()
	id := sembrarPendiente(ctx, t, db, k, ahora.Add(-10*time.Minute), ahora.Add(time.Hour))

	exigeColaSinAjenos(ctx, t, db, k.TenantID)
	j, ok, err := jobs.ClaimNext(ctx)
	if err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if ok {
		t.Fatalf("ClaimNext se llevó el job %s con next_attempt_at UNA HORA EN EL FUTURO; ESPERADO "+
			"(_, false, nil). El backoff NO existe si el claim no mira la marca: el job castigado "+
			"vuelve al ruedo en el acto y el reintento se convierte en un bucle a la velocidad del error",
			j.ID)
	}
	if st := estadoDeLaFila(ctx, t, db, id); st != "pending" {
		t.Fatalf("la fila quedó en %q; ESPERADO 'pending' — un claim que no aplica NO debe tocar la fila", st)
	}
}

// ---------------------------------------------------------------------------
// (3) LA MARCA VENCIDA LO SUELTA
// ---------------------------------------------------------------------------

// TestIntegration_Backoff_PendingConMarcaVencidaSiEsReclamable es la mitad gemela, y
// sin ella el test (2) se podría satisfacer con un claim que no reclamara NUNCA nada.
// Las dos juntas fijan el `<=`: la marca RETIENE hasta que vence, y al vencer SUELTA.
//
// SALIDAS ESPERADAS: ClaimNext ... (el job sembrado, true, nil), fila en 'processing'.
func TestIntegration_Backoff_PendingConMarcaVencidaSiEsReclamable(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	k := claveDeVentana(t, db)
	jobs := intake.NewPostgres(db)

	ahora := time.Now().UTC()
	id := sembrarPendiente(ctx, t, db, k, ahora.Add(-10*time.Minute), ahora.Add(-time.Minute))

	exigeColaSinAjenos(ctx, t, db, k.TenantID)
	j, ok, err := jobs.ClaimNext(ctx)
	if err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if !ok {
		t.Fatalf("ClaimNext no encontró nada; ESPERADO el job %s, cuya marca venció hace un minuto. "+
			"Un claim que retiene lo vencido para la cola entera, y el cliente no recibe su presupuesto", id)
	}
	if j.ID != id {
		t.Fatalf("ClaimNext se llevó %s; ESPERADO %s", j.ID, id)
	}
	if st := estadoDeLaFila(ctx, t, db, id); st != "processing" {
		t.Fatalf("la fila quedó en %q; ESPERADO 'processing'", st)
	}
}

// ---------------------------------------------------------------------------
// (4) ENTRE DOS VENCIDOS, GANA LA MARCA MÁS ANTIGUA
// ---------------------------------------------------------------------------

// TestIntegration_Backoff_EntreDosVencidosGanaLaMarcaMasAntigua fija el ORDER BY, y
// su montaje es la mitad del test.
//
// 🔴 LOS DOS JOBS LLEVAN created_at Y next_attempt_at CRUZADOS A PROPÓSITO. Si los
// dos órdenes coincidieran —que es lo normal en producción, porque el DEFAULT de la
// marca se evalúa en el mismo INSERT que created_at— este test saldría verde con el
// `ORDER BY created_at` de antes de la 0078 y no probaría nada. Cruzándolos, los dos
// criterios señalan a filas DISTINTAS y solo uno de los dos puede ganar:
//
//   - `elQueEsperoMas`  : creado hace 10 min, marca vencida hace 1 min  (gana por created_at)
//   - `elDeMarcaVieja`  : creado hace  1 min, marca vencida hace 10 min (gana por next_attempt_at)
//
// SALIDA ESPERADA: ClaimNext se lleva `elDeMarcaVieja`.
func TestIntegration_Backoff_EntreDosVencidosGanaLaMarcaMasAntigua(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	k := claveDeVentana(t, db)
	jobs := intake.NewPostgres(db)

	ahora := time.Now().UTC()
	elQueEsperoMas := sembrarPendiente(ctx, t, db, k,
		ahora.Add(-10*time.Minute), ahora.Add(-time.Minute))
	elDeMarcaVieja := sembrarPendiente(ctx, t, db, k,
		ahora.Add(-time.Minute), ahora.Add(-10*time.Minute))

	exigeColaSinAjenos(ctx, t, db, k.TenantID)
	j, ok, err := jobs.ClaimNext(ctx)
	if err != nil || !ok {
		t.Fatalf("ClaimNext = (%t, %v); ESPERADO un job, con DOS vencidos en la cola", ok, err)
	}
	if j.ID != elDeMarcaVieja {
		t.Fatalf("ClaimNext se llevó %s; ESPERADO %s, el de la MARCA más antigua.\n"+
			"Si se llevó %s (el de created_at más antiguo), el claim sigue ordenando por la fecha de "+
			"creación: un job castigado a esperar adelantaría a otro que lleva media hora vencido, y el "+
			"backoff dejaría de ser una cola por turno para ser una cola por antigüedad de la ventana",
			j.ID, elDeMarcaVieja, elQueEsperoMas)
	}
}

// ---------------------------------------------------------------------------
// (5) LAS FILAS QUE YA ESTABAN — LO QUE UNA BASE RECIÉN MIGRADA NO PUEDE PROBAR
// ---------------------------------------------------------------------------

// TestIntegration_Backoff_FilasPreexistentesQuedanReclamables es el que prueba que el
// DEFAULT pobló LAS FILAS VIEJAS, y su montaje es todo el test.
//
// 🔴 EN UNA BASE RECIÉN MIGRADA ESTE CRITERIO SALE VERDE POR CERO FILAS. Cuando la
// suite arranca, `intake_jobs` está vacía: la 0078 no toca nada, cualquier barrido
// pasa, y nadie ha probado que un job que ya llevaba días en `pending` sobreviva a la
// migración. Por eso aquí se FABRICA el estado anterior: se TIRAN las dos columnas,
// se siembran filas como las que hay hoy en UAT, y solo entonces se reejecuta la
// migración encima.
//
// 🔴 Y NO SE DA POR SUPUESTO QUE `ADD COLUMN … NOT NULL DEFAULT now()` LAS POBLA.
// Postgres ≥ 11 lo hace, pero un DEFAULT no gobierna a las filas que ya existían en
// el caso general —esa es la trampa que esta casa ya pagó— y la variante que alguien
// escribiría «para no reescribir la tabla» (columna NULLable + backfill aparte)
// dejaría esas filas con la marca a NULL, o sea INVISIBLES al claim: jobs de clientes
// reales perdidos para siempre, sin un error en ningún log. Lo que se afirma abajo no
// es que la columna exista: es que ClaimNext SE LOS LLEVA.
//
// SALIDAS ESPERADAS: Migrate ... Skipped=false; las dos filas viejas con attempts=0 y
// next_attempt_at <= now(); ClaimNext se lleva una de ellas.
func TestIntegration_Backoff_FilasPreexistentesQuedanReclamables(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	k := claveDeVentana(t, db)
	jobs := intake.NewPostgres(db)

	// (1) EL ESTADO ANTERIOR: las columnas no existen. `DROP COLUMN` se lleva por
	// delante también intake_jobs_claim_idx, que cuelga de next_attempt_at; el replay
	// de abajo lo recrea, y el test (1) es quien vigila que vuelva.
	if _, err := db.ExecContext(ctx, `
		ALTER TABLE public.intake_jobs
		    DROP COLUMN IF EXISTS attempts,
		    DROP COLUMN IF EXISTS next_attempt_at
	`); err != nil {
		t.Fatalf("fabricando el estado anterior (drop de las dos columnas): %v", err)
	}

	// (2) DOS JOBS COMO LOS QUE HAY HOY EN LA BASE, escritos SIN las columnas nuevas
	// —no se puede de otra forma: acaban de dejar de existir—. Son dos y no uno para
	// que el claim tenga que ELEGIR y no le valga con encontrar la única que hay.
	//
	// Las DOS van al MISMO tenant, y no es comodidad: `exigeColaSinAjenos` (abajo) mide
	// la contaminación POR TENANT, así que un segundo tenant propio se contaría como
	// ajeno y el test se acusaría a sí mismo. Comparten además la tupla entera, que es
	// legal: el índice único de la 0072 es PARCIAL (`WHERE status = 'aggregating'`) y
	// estas dos son `pending` — la propia 0072 documenta que una tupla acumula varias
	// filas `pending` a lo largo del día.
	ahora := time.Now().UTC()
	sembrarLegado := func(creado time.Time, cual string) string {
		var id string
		if err := db.QueryRowContext(ctx, `
			INSERT INTO public.intake_jobs
			       (tenant_id, session_id, contact_id, event_id, status, created_at, updated_at)
			VALUES ($1, $2, $3, $4::uuid, 'pending', $5, $5)
			RETURNING id::text
		`, k.TenantID, k.SessionID, k.ContactID, k.EventID, creado).Scan(&id); err != nil {
			t.Fatalf("sembrando el job legado %s: %v", cual, err)
		}
		return id
	}
	viejoA := sembrarLegado(ahora.Add(-2*time.Hour), "A")
	viejoB := sembrarLegado(ahora.Add(-time.Hour), "B")

	// (3) EL ARRANQUE QUE APLICA LA 0078 ENCIMA.
	invalidaElHashDelEsquema(ctx, t, db)
	res, err := migrations.Migrate(ctx, db)
	if err != nil {
		t.Fatalf("reejecutando las migraciones sobre el estado anterior: %v", err)
	}
	if res.Skipped {
		t.Fatal("Migrate devolvió Skipped=true: el full-replay NO corrió y este test no probó nada " +
			"(isUpToDate exige versión Y hash; ver invalidaElHashDelEsquema)")
	}

	// (4) LAS FILAS VIEJAS SALIERON POBLADAS.
	for _, id := range []string{viejoA, viejoB} {
		intentos, marca, vencida := backoffDeLaFila(ctx, t, db, id)
		if intentos != 0 {
			t.Fatalf("el job legado %s salió con attempts=%d; ESPERADO 0: no se ha intentado nunca",
				id, intentos)
		}
		if !vencida {
			t.Fatalf("el job legado %s salió con next_attempt_at=%s, que Postgres NO considera vencida; "+
				"ESPERADO una marca ya vencida. Una fila que llevaba horas esperando no puede quedar "+
				"castigada por haber entrado antes que la columna", id, marca)
		}
	}

	// (5) Y LO QUE DE VERDAD IMPORTA: EL CLAIM SE LOS LLEVA. Sin esto, los cuatro
	// asertos de arriba solo dirían que la columna existe.
	exigeColaSinAjenos(ctx, t, db, k.TenantID)
	j, ok, cerr := jobs.ClaimNext(ctx)
	if cerr != nil || !ok {
		t.Fatalf("ClaimNext = (%t, %v) con DOS jobs legados en `pending`; ESPERADO que se llevara uno. "+
			"Las filas anteriores a la migración quedaron INVISIBLES al worker: jobs de clientes reales "+
			"perdidos sin un solo error", ok, cerr)
	}
	if j.ID != viejoA {
		t.Fatalf("ClaimNext se llevó %s; ESPERADO %s, el legado MÁS ANTIGUO. Las dos filas comparten "+
			"marca al milisegundo (el DEFAULT las pobló con el now() de la MISMA transacción de "+
			"migración), así que quien decide es el desempate `created_at` del ORDER BY: sin él, el orden "+
			"entre las filas heredadas es arbitrario", j.ID, viejoA)
	}
}
