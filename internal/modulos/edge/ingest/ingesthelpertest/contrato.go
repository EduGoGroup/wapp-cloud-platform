// Package ingesthelpertest es la suite de contrato del dedupe de entrantes (ingest.Deduper) y su
// doble en memoria, Memoria (D-F3-1: el doble vivía en el paquete de producción viejo,
// dedupe.go). Ningún código de producción lo importa: arrastra "testing".
//
//   - contrato.go: la entrada. Montaje, ContratoDeduper y la tabla de casos.
//   - memoria.go: Memoria, el Deduper en memoria que usan los tests de los consumidores.
//
// La suite la corren las dos implementaciones: Memoria en unitario (memoria_test.go) e
// ingest.PostgresDeduper en los procesos de F9, con el arnés de testcontainers (F3-05).
//
// Nuevo: no tiene fichero viejo. Los casos salen de plan/F3-edge/diseno.md §2 y de los tests
// viejos de internal/ingest @ 8896f13 (TestMemoryDeduper_PrimerAvistamientoLuegoDuplicado,
// TestPostgresDeduper_SeenIdempotente), leídos, no portados.
package ingesthelpertest

import (
	"context"
	"sync"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/ingest"
)

// Montaje es lo que cada implementación entrega a la suite para UN caso: ContratoDeduper llama a
// nuevo una vez por caso.
type Montaje struct {
	// Deduper es la implementación bajo prueba.
	Deduper ingest.Deduper
	// SessionA y SessionB son dos session_id distintos, no vacíos y de los que el Deduper NO ha
	// visto ningún mensaje. La tabla no tiene tenant ni claves foráneas: con Postgres basta con
	// que sean únicos por caso (la base se comparte entre casos).
	SessionA, SessionB string
}

// ContratoDeduper ejecuta las promesas de ingest.Deduper contra la implementación que devuelve
// nuevo, con un Montaje limpio por caso (nuevo se llama una vez por t.Run). No salta nada.
//
// El puerto es su propio observador (Seen dice lo que había): el Montaje no trae ni siembra ni
// observador.
//
// Lo que la suite NO afirma, a propósito:
//
//   - la poda por retención: es de ingest.PostgresDeduper (la Memoria no poda). La prueba su
//     test de fichero y, contra la base, el proceso de F9;
//   - el camino de error (false junto al error): la Memoria nunca falla. Lo prueba el test de
//     fichero de ingest.PostgresDeduper.
func ContratoDeduper(t *testing.T, nuevo func(t *testing.T) Montaje) {
	t.Helper()
	if nuevo == nil {
		t.Fatal("ingesthelpertest.ContratoDeduper: nuevo es nil; hace falta una función que devuelva un Montaje")
	}
	for _, c := range cases() {
		t.Run(c.name, func(t *testing.T) {
			m := nuevo(t)
			validateMontaje(t, m)
			c.run(t, m)
		})
	}
}

// contractCase es una promesa del dedupe: su nombre (el del t.Run) y la función que la afirma.
type contractCase struct {
	name string
	run  func(t *testing.T, m Montaje)
}

// cases es la tabla de la suite. El comentario de cada fila es la promesa que fija.
func cases() []contractCase {
	return []contractCase{
		{"FirstSighting_False", caseFirstSighting},                           // nuevo ⇒ false
		{"SameKeyAgain_True", caseSameKeyAgain},                              // repetido ⇒ true, y lo sigue siendo
		{"OtherMessageInTheSameSession_False", caseOtherMessage},             // la clave incluye el mensaje
		{"SameMessageInAnotherSession_False", caseOtherSession},              // la clave incluye la sesión
		{"InterleavedResend_True", caseInterleavedResend},                    // no es solo la re-entrega inmediata
		{"ConcurrentSameKey_ExactlyOneFirstSighting", caseConcurrentSameKey}, // en paralelo, un solo «nuevo»
	}
}

// validateMontaje exige lo que la suite da por hecho de un Montaje.
func validateMontaje(t *testing.T, m Montaje) {
	t.Helper()
	switch {
	case m.Deduper == nil:
		t.Fatal("Montaje.Deduper es nil")
	case m.SessionA == "" || m.SessionB == "":
		t.Fatalf("Montaje: SessionA (%q) y SessionB (%q) no pueden ser vacíos", m.SessionA, m.SessionB)
	case m.SessionA == m.SessionB:
		t.Fatalf("Montaje: SessionA y SessionB son el mismo (%q); deben ser distintos", m.SessionA)
	}
}

// Los wa_message_id de la suite.
const (
	messageA = "wamid.contract-a"
	messageB = "wamid.contract-b"
)

func caseFirstSighting(t *testing.T, m Montaje) {
	requireSeen(t, m, m.SessionA, messageA, false)
}

func caseSameKeyAgain(t *testing.T, m Montaje) {
	requireSeen(t, m, m.SessionA, messageA, false)
	requireSeen(t, m, m.SessionA, messageA, true)
	requireSeen(t, m, m.SessionA, messageA, true)
}

func caseOtherMessage(t *testing.T, m Montaje) {
	requireSeen(t, m, m.SessionA, messageA, false)
	requireSeen(t, m, m.SessionA, messageB, false)
}

func caseOtherSession(t *testing.T, m Montaje) {
	requireSeen(t, m, m.SessionA, messageA, false)
	requireSeen(t, m, m.SessionB, messageA, false)
	// Cada sesión recuerda lo suyo.
	requireSeen(t, m, m.SessionA, messageA, true)
	requireSeen(t, m, m.SessionB, messageA, true)
}

// caseInterleavedResend es el caso que justifica el paquete: el reenvío de A llega DESPUÉS de B.
// La guarda consecutiva del runtime (last_wa_message_id) no lo cortaría; el dedupe sí.
func caseInterleavedResend(t *testing.T, m Montaje) {
	requireSeen(t, m, m.SessionA, messageA, false)
	requireSeen(t, m, m.SessionA, messageB, false)
	requireSeen(t, m, m.SessionA, messageA, true)
	requireSeen(t, m, m.SessionA, messageB, true)
}

// caseConcurrentSameKey pregunta por la misma clave desde varias goroutines: exactamente una la
// ve como nueva. Bajo -race, un Deduper sin proteger se ve aquí.
func caseConcurrentSameKey(t *testing.T, m Montaje) {
	const callers = 16
	type result struct {
		seen bool
		err  error
	}
	var wg sync.WaitGroup
	results := make(chan result, callers)
	for range callers {
		wg.Go(func() {
			seen, err := m.Deduper.Seen(context.Background(), m.SessionA, messageA)
			results <- result{seen, err}
		})
	}
	wg.Wait()
	close(results)
	firstSightings := 0
	for r := range results {
		if r.err != nil {
			t.Errorf("Seen en paralelo: error inesperado %v", r.err)
		}
		if !r.seen {
			firstSightings++
		}
	}
	if firstSightings != 1 {
		t.Errorf("%d llamadas en paralelo vieron la clave como nueva, quería exactamente 1", firstSightings)
	}
}

// requireSeen afirma que Seen devuelve (want, nil).
func requireSeen(t *testing.T, m Montaje, sessionID, waMessageID string, want bool) {
	t.Helper()
	got, err := m.Deduper.Seen(context.Background(), sessionID, waMessageID)
	if err != nil {
		t.Fatalf("Seen(%q, %q): error inesperado %v", sessionID, waMessageID, err)
	}
	if got != want {
		t.Errorf("Seen(%q, %q) = %v, quería %v (false = primer avistamiento, true = duplicado)",
			sessionID, waMessageID, got, want)
	}
}
