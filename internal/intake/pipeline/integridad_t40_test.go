// integridad_t40_test.go — EL CUARTO PUNTO DE T4.0 (Plan 044, D-044.46): una
// violación de integridad de Postgres NO se reintenta ni una vez.
//
// LA FACTURA MEDIDA, que es lo que este fichero impide que vuelva: el job `6c5aac22`
// chocó contra `intakes_event_id_uidx` (23505) la noche del 26-08 y `causaDe` —un
// if/else que mandaba a `infra` todo lo que no fuera calidad— lo devolvió a la cola
// DIEZ veces a lo largo de VEINTINUEVE MINUTOS (22:48 → 23:17) para morir igual. El
// dato que provoca una violación de integridad no cambia entre intentos: repetir la
// misma escritura no la cura, solo alarga la espera del cliente que escribió.
//
// 🔴 EL ERROR NO ES UN `errors.New`, y por la misma razón que `errorDeRedReal` levanta
// un puerto muerto de verdad: lo que la política clasifica es una FAMILIA. Aquí se usa
// el `*pgconn.PgError` que devuelve el driver, envuelto con el MISMO `%w` con el que lo
// envuelven los repositorios en producción, para que la clasificación tenga que
// atravesar el envoltorio real y no uno inventado para el test.
package pipeline

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
)

// choqueDeIntegridad reproduce el fallo tal como sale de una etapa que escribe: el
// código SQLSTATE de la clase 23 dentro del error del driver, envuelto por el store y
// otra vez por la etapa (dos capas de `%w`, que es lo que hay en el camino real
// `draft.Run` → `store.UpsertIntake` → pgx).
func choqueDeIntegridad(code, constraint string) error {
	delDriver := &pgconn.PgError{Code: code, ConstraintName: constraint}
	delStore := fmt.Errorf("store: upsert solicitud: %w", delDriver)
	return fmt.Errorf("draft: crear la solicitud del job %s: %w", "6c5aac22", delStore)
}

// TestWorker_ViolacionDeIntegridad_MuereSinReintentar es el criterio (d) de T4.0,
// recorrido sobre la clase 23 ENTERA y no solo sobre el 23505 que se midió.
//
// Se afirman las tres cosas que distinguen «murió» de «murió después de insistir»:
//
//   - el job queda `failed` en la PRIMERA vuelta;
//   - el motivo dice `causa=job_invalido` (el vocabulario cerrado del log, no una
//     frase);
//   - la etapa se llamó UNA sola vez, y `attempts` quedó en 0 reintentos cobrados. Sin
//     esto el test pasaría igual con una política que reintenta y acaba muriendo.
func TestWorker_ViolacionDeIntegridad_MuereSinReintentar(t *testing.T) {
	casos := []struct {
		nombre     string
		code       string
		constraint string
	}{
		{"unique_violation 23505 (el del hallazgo #24)", "23505", "intakes_event_id_uidx"},
		{"not_null_violation 23502", "23502", ""},
		{"foreign_key_violation 23503", "23503", "intakes_event_id_fkey"},
		{"check_violation 23514", "23514", "intakes_event_id_required_chk"},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			b := nuevoBanco(t, Config{})
			b.draft.guion = []guionEtapa{{err: choqueDeIntegridad(c.code, c.constraint)}}
			id := b.sembrarSano("")

			// UNA sola vuelta, y el reloj NO avanza: si el job hubiera vuelto a la cola
			// con backoff, la siguiente no lo reclamaría y el conteo de llamadas se
			// quedaría en 1 igualmente. Por eso se drena varias veces y se avanza el
			// reloj: si el arreglo no estuviera, aquí se verían más llamadas.
			for vuelta := 0; vuelta < 3; vuelta++ {
				b.w.Drenar(context.Background())
				b.rel.avanzar(BackoffTopePorDefecto * 2)
			}

			f := b.ver(t, id)
			if f.Status != intake.StatusFailed {
				t.Fatalf("un choque de integridad mata el job: quedó %q (attempts=%d)", f.Status, f.Attempts)
			}
			if !strings.Contains(f.Error, "causa="+CausaJobInvalido) {
				t.Fatalf("la causa de muerte debe ser `job_invalido` y decirlo: %q", f.Error)
			}
			if got := b.draft.count(); got != 1 {
				t.Fatalf("la etapa se llamó %d veces; un %s no se reintenta NI UNA VEZ", got, c.code)
			}
			if f.Attempts != 0 {
				t.Fatalf("se cobraron %d reintentos y no debía cobrarse ninguno", f.Attempts)
			}
		})
	}
}

// TestWorker_DeadlockDePostgres_SIGUE_SiendoInfra es la mitad que impide que el
// arreglo se pase de ancho, y no es una hipótesis: la clase 40 (deadlock 40P01,
// serialización 40001) es el caso OPUESTO — el conflicto es de concurrencia, no de
// dato, y reejecutar converge. Si `causaDe` clasificara «todo error de pgx» como job
// inválido, un deadlock esporádico mataría un pedido perfectamente sano.
func TestWorker_DeadlockDePostgres_SIGUE_SiendoInfra(t *testing.T) {
	b := nuevoBanco(t, Config{})
	b.draft.guion = []guionEtapa{{err: fmt.Errorf("store: cerrar solicitud: %w",
		&pgconn.PgError{Code: "40P01"})}}
	id := b.sembrarSano("")

	b.w.Drenar(context.Background())

	f := b.ver(t, id)
	if f.Status != intake.StatusPending {
		t.Fatalf("un deadlock vuelve a la cola con backoff: quedó %q", f.Status)
	}
	if f.Attempts != 1 {
		t.Fatalf("el intento debe quedar cobrado (attempts=1), quedó %d", f.Attempts)
	}
}
