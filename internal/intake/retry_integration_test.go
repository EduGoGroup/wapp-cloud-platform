// retry_integration_test.go — LA POLÍTICA DE BACKOFF EJECUTADA sobre Postgres REAL
// (Plan 044 · Ola 2 · T2.5).
//
// # QUÉ SE PRUEBA AQUÍ Y NO EN `internal/intake/pipeline`
//
// Aquel paquete prueba la POLÍTICA (qué curva, cuántos intentos, qué causa) contra un
// doble en memoria. Esto prueba LA SENTENCIA: que `Retry` mueva las TRES columnas
// juntas, que respete el guard `status = 'processing'`, y —lo que ninguna prueba en
// memoria puede afirmar— que el `attempts + 1` lo calcule el MOTOR. Nada de eso tiene
// una línea de Go: vive entero en `retrySQL`, y un doble lo reescribiría a mano.
//
// Se corre como el resto de la casa: por el NOMBRE del fichero, con `WAPP_TEST_DB_DSN`
// (sin ella se salta solo). Los helpers `openTestDB`, `claveDeVentana`,
// `sembrarPendiente`, `estadoDeLaFila`, `backoffDeLaFila` y `exigeColaSinAjenos` viven
// en los otros ficheros de integración de este mismo paquete de test.
package intake_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
)

// reclamarUno reclama el job que se acaba de sembrar y falla si no es ése. Sin esta
// comprobación, un test podría estar castigando a un job de otra escena y salir verde.
func reclamarUno(ctx context.Context, t *testing.T, jobs *intake.Postgres, id string) intake.ClaimedJob {
	t.Helper()
	j, ok, err := jobs.ClaimNext(ctx)
	if err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if !ok {
		t.Fatalf("ClaimNext no encontró nada; se esperaba el job %s", id)
	}
	if j.ID != id {
		t.Fatalf("ClaimNext se llevó %s; se esperaba %s", j.ID, id)
	}
	return j
}

// ---------------------------------------------------------------------------
// (1) LAS TRES ESCRITURAS, EN UNA SOLA SENTENCIA
// ---------------------------------------------------------------------------

// TestIntegration_Retry_MueveLasTresColumnasALaVez es el test de `retrySQL`: vuelta a
// `pending`, intento COBRADO y marca EMPUJADA, las tres o ninguna.
//
// Las tres afirmaciones son independientes y ninguna sobra:
//
//   - solo `pending` lo daría también `Release`, la arista SIN castigo — y con ella el
//     job vuelve al ruedo en el acto y el techo de intentos no se alcanza JAMÁS;
//   - solo `attempts+1` lo daría un backoff de cero;
//   - solo la marca dejaría un job que se castiga eternamente sin morir nunca.
//
// 🔬 MUTACIÓN EJECUTADA: quitar `attempts = attempts + 1` de `retrySQL`. RESULTADO:
// rojo — `attempts` sigue en 0 y el job nunca alcanzaría el techo.
func TestIntegration_Retry_MueveLasTresColumnasALaVez(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	k := claveDeVentana(t, db)
	jobs := intake.NewPostgres(db)

	ahora := time.Now().UTC()
	id := sembrarPendiente(ctx, t, db, k, ahora.Add(-10*time.Minute), ahora.Add(-time.Minute))
	exigeColaSinAjenos(ctx, t, db, k.TenantID)
	j := reclamarUno(ctx, t, jobs, id)

	if j.Attempts != 0 {
		t.Fatalf("el claim debe traer los intentos ya consumidos; trajo %d y la fila nace en 0", j.Attempts)
	}

	marca := ahora.Add(30 * time.Second)
	ok, err := jobs.Retry(ctx, id, marca)
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if !ok {
		t.Fatal("Retry sobre un job en `processing` debe aplicar; devolvió false")
	}

	if st := estadoDeLaFila(ctx, t, db, id); st != "pending" {
		t.Fatalf("la fila quedó en %q; se esperaba 'pending'", st)
	}
	intentos, guardada, vencida := backoffDeLaFila(ctx, t, db, id)
	if intentos != 1 {
		t.Fatalf("attempts quedó en %d; se esperaba 1 — sin esta cuenta el techo de intentos no llega nunca", intentos)
	}
	if vencida {
		t.Fatalf("la marca quedó VENCIDA (%s): el job es reclamable en el acto y el backoff no existe", guardada)
	}
	if guardada.Sub(marca).Abs() > time.Second {
		t.Fatalf("la marca guardada es %s; se esperaba ≈ %s", guardada, marca)
	}
}

// TestIntegration_Retry_ElContadorLoLLEVA_EL_MOTOR es lo que ninguna prueba en memoria
// puede afirmar: la suma se hace sobre el valor de la BASE, no sobre el que el worker
// leyó en su claim. Se fabrica la divergencia a mano —la fila avanza a 7 por detrás— y
// se exige que el resultado sea 8 y no 1.
//
// IMPORTA porque el worker toma la decisión «reintentar o matar» con el `Attempts` de su
// claim, que puede estar rancio: si además ESCRIBIERA desde ese valor, dos workers que
// se solapasen se pisarían la cuenta y el techo de intentos dejaría de ser un techo.
//
// 🔬 MUTACIÓN EJECUTADA: cambiar `attempts = attempts + 1` por `attempts = $3` con el
// valor que trae el llamante. RESULTADO: rojo — quedaría 1.
func TestIntegration_Retry_ElContadorLoLLEVA_EL_MOTOR(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	k := claveDeVentana(t, db)
	jobs := intake.NewPostgres(db)

	ahora := time.Now().UTC()
	id := sembrarPendiente(ctx, t, db, k, ahora.Add(-10*time.Minute), ahora.Add(-time.Minute))
	exigeColaSinAjenos(ctx, t, db, k.TenantID)
	reclamarUno(ctx, t, jobs, id)

	// Otro proceso mueve el contador por detrás, DESPUÉS del claim.
	if _, err := db.ExecContext(ctx,
		`UPDATE public.intake_jobs SET attempts = 7 WHERE id = $1::uuid`, id); err != nil {
		t.Fatalf("fabricar la divergencia: %v", err)
	}

	if _, err := jobs.Retry(ctx, id, ahora.Add(time.Minute)); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if intentos, _, _ := backoffDeLaFila(ctx, t, db, id); intentos != 8 {
		t.Fatalf("attempts quedó en %d; se esperaba 8 (7 de la base + 1). La suma la tiene que hacer "+
			"el MOTOR: desde el valor del claim, dos workers solapados se pisarían la cuenta", intentos)
	}
}

// ---------------------------------------------------------------------------
// (2) EL GUARD: SOLO SE CASTIGA A UN JOB TOMADO
// ---------------------------------------------------------------------------

// TestIntegration_Retry_NoAplicaSobreUnJobQueYaNoEstaEnProcessing fija el guard. Es el
// camino que el worker tiene que poder distinguir —y loguear— cuando otro worker le
// termina el job bajo los pies: `(false, nil)`, no un error.
//
// 🔬 MUTACIÓN EJECUTADA: quitar `AND status = 'processing'` de `retrySQL`. RESULTADO:
// rojo — un job `done` volvería a `pending`, resucitado, y con el sobre ya vaciado por
// INV-13 se reintentaría eternamente sin literal.
func TestIntegration_Retry_NoAplicaSobreUnJobQueYaNoEstaEnProcessing(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	k := claveDeVentana(t, db)
	jobs := intake.NewPostgres(db)

	ahora := time.Now().UTC()
	id := sembrarPendiente(ctx, t, db, k, ahora.Add(-10*time.Minute), ahora.Add(-time.Minute))
	exigeColaSinAjenos(ctx, t, db, k.TenantID)
	reclamarUno(ctx, t, jobs, id)

	if ok, err := jobs.Finish(ctx, id, ""); err != nil || !ok {
		t.Fatalf("Finish: (%v, %v)", ok, err)
	}

	ok, err := jobs.Retry(ctx, id, ahora.Add(time.Minute))
	if err != nil {
		t.Fatalf("Retry sobre un job terminado NO es un error, es una transición que no aplica: %v", err)
	}
	if ok {
		t.Fatal("Retry resucitó un job `done`: los terminales son ABSORBENTES")
	}
	if st := estadoDeLaFila(ctx, t, db, id); st != "done" {
		t.Fatalf("la fila quedó en %q; se esperaba 'done'", st)
	}
}

// TestIntegration_Retry_SinMarcaSeRechaza: una marca cero es el año 1, o sea el pasado.
// El job volvería a ser reclamable EN EL ACTO y el backoff sería un no-op silencioso
// —indistinguible desde fuera de un reintento inmediato legítimo—, así que se rechaza
// en Go antes de tocar la base.
func TestIntegration_Retry_SinMarcaSeRechaza(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	jobs := intake.NewPostgres(db)

	ok, err := jobs.Retry(ctx, "00000000-0000-0000-0000-000000000000", time.Time{})
	if err == nil {
		t.Fatal("una marca cero deja el backoff en el pasado y tiene que rechazarse")
	}
	if ok {
		t.Fatal("Retry devolvió true con una marca inválida")
	}
	if !strings.Contains(err.Error(), "pasado") {
		t.Fatalf("el error tiene que decir POR QUÉ se rechaza: %q", err)
	}
}

// ---------------------------------------------------------------------------
// (3) EL CICLO ENTERO: CASTIGO ⇒ RETENIDO ⇒ VENCE ⇒ VUELVE
// ---------------------------------------------------------------------------

// TestIntegration_Retry_ElCicloCompletoDelCastigo es la prueba de que las dos piezas
// —la política que empuja y el claim que respeta— encajan de verdad. Sin ella cada
// mitad podría estar bien por separado y el conjunto no funcionar.
//
// 🔬 MUTACIÓN EJECUTADA: en `retrySQL`, `next_attempt_at = now()` en vez de `$2`.
// COMPILA (sobra un parámetro, así que además hay que quitarlo del Exec). RESULTADO:
// rojo — el job es reclamable inmediatamente después del castigo.
func TestIntegration_Retry_ElCicloCompletoDelCastigo(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	k := claveDeVentana(t, db)
	jobs := intake.NewPostgres(db)

	ahora := time.Now().UTC()
	id := sembrarPendiente(ctx, t, db, k, ahora.Add(-10*time.Minute), ahora.Add(-time.Minute))
	exigeColaSinAjenos(ctx, t, db, k.TenantID)
	reclamarUno(ctx, t, jobs, id)

	// Castigado media hora.
	if _, err := jobs.Retry(ctx, id, ahora.Add(30*time.Minute)); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if _, ok, err := jobs.ClaimNext(ctx); err != nil || ok {
		t.Fatalf("el job castigado NO debe reclamarse: (ok=%v, err=%v)", ok, err)
	}

	// La marca vence (se mueve al pasado con SQL directo: el reloj que manda es el
	// del motor, y traerse la marca a Go para compararla sería comparar dos relojes).
	if _, err := db.ExecContext(ctx,
		`UPDATE public.intake_jobs SET next_attempt_at = now() - interval '1 second' WHERE id = $1::uuid`,
		id); err != nil {
		t.Fatalf("vencer la marca: %v", err)
	}
	j := reclamarUno(ctx, t, jobs, id)
	if j.Attempts != 1 {
		t.Fatalf("el claim debe traer el intento ya cobrado; trajo %d", j.Attempts)
	}
}

// ---------------------------------------------------------------------------
// (4) UN JOB `failed` NO IMPIDE QUE EL SIGUIENTE MENSAJE ABRA JOB NUEVO
// ---------------------------------------------------------------------------

// TestIntegration_UnJobFailedNoImpideAbrirElSiguiente es el criterio literal de T2.5, y
// su mecanismo hay que decirlo para que se entienda por qué el test es tan corto: el
// único índice único de la tabla, `intake_jobs_ventana_viva_uidx` (0072), es PARCIAL —
// `WHERE status = 'aggregating'`—, así que una fila terminal no ocupa el sitio de nadie.
//
// El test lo comprueba por EJECUCIÓN y no leyendo la migración: se mata un job y se
// vuelve a abrir ventana sobre la MISMA tupla. Si algún día alguien quitase el `WHERE`
// del índice «para simplificar», esto se pondría rojo y no un `ON CONFLICT` en
// producción — que es donde se descubriría si no.
//
// 🔬 MUTACIÓN EJECUTADA: recrear el índice sin su `WHERE status = 'aggregating'` (con
// SQL directo dentro de una variante del test). RESULTADO: rojo, `duplicate key value
// violates unique constraint`.
func TestIntegration_UnJobFailedNoImpideAbrirElSiguiente(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	k := claveDeVentana(t, db)
	jobs := intake.NewPostgres(db)

	ahora := time.Now().UTC()
	muerto := sembrarPendiente(ctx, t, db, k, ahora.Add(-10*time.Minute), ahora.Add(-time.Minute))
	exigeColaSinAjenos(ctx, t, db, k.TenantID)
	reclamarUno(ctx, t, jobs, muerto)
	if ok, err := jobs.Fail(ctx, muerto, "causa=calidad stage=p2: agotados los 3 intentos"); err != nil || !ok {
		t.Fatalf("Fail: (%v, %v)", ok, err)
	}

	// El siguiente mensaje del MISMO contacto, sobre la MISMA tupla de ventana.
	if err := jobs.OpenOrAppend(ctx, intake.Append{Key: k, MessageTS: ahora}); err != nil {
		t.Fatalf("el job `failed` impidió abrir una ventana nueva sobre la misma tupla: %v", err)
	}

	var vivas int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM public.intake_jobs
		 WHERE tenant_id = $1 AND session_id = $2 AND contact_id = $3 AND event_id = $4::uuid
		   AND status = 'aggregating'
	`, k.TenantID, k.SessionID, k.ContactID, k.EventID).Scan(&vivas); err != nil {
		t.Fatalf("contar ventanas vivas: %v", err)
	}
	if vivas != 1 {
		t.Fatalf("se esperaba EXACTAMENTE una ventana viva tras el `failed`, hay %d", vivas)
	}

	var idMuerto sql.NullString
	if err := db.QueryRowContext(ctx,
		`SELECT status FROM public.intake_jobs WHERE id = $1::uuid`, muerto).Scan(&idMuerto); err != nil {
		t.Fatalf("releer el job muerto: %v", err)
	}
	if idMuerto.String != "failed" {
		t.Fatalf("el job muerto cambió de estado al abrirse el siguiente: quedó %q", idMuerto.String)
	}
}
