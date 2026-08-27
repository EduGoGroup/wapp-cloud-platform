package intakes

import (
	"testing"
	"time"
)

// sello_poda_test.go — LA DECISIÓN DE QUÉ `literal_pruned_at` SE PUBLICA, SIN BASE
// DE DATOS (Plan 044 · Ola 4 · Tanda 4).
//
// Es un test INTERNO (package intakes, no intakes_test) por lo mismo que
// filtro_args_test.go: sellarPodada es privada y es EL CABLEADO entre la poda —que
// se ejecuta DESPUÉS de cerrar el cursor— y lo que la lectura devuelve.
//
// 🔴 POR QUÉ NO BASTABA EL TEST DE INTEGRACIÓN. Lo que cubre esta ruta de verdad es
// TestLiteralPG_PodaAlVencerElTTL (literal_integration_test.go), pero se SALTA sin
// WAPP_TEST_DB_DSN, que es el caso normal de un `go test` pelado: un `--- SKIP` no
// prueba nada y el rc=0 lo cuenta igual que un PASS. Con base delante SÍ corre
// —`make test-integration` levanta un postgres:16 efímero y la exporta—, y ahí es
// donde se comprueba la coherencia de relojes. Esto es la red que corre SIEMPRE,
// tenga o no Docker quien ejecute el gate. sellarPodada no
// toca ni la BD ni el reloj —recibe el instante ya sellado—, así que sus tres casos
// se pueden afirmar aquí, siempre, sin Postgres delante.

// revisionesDePrueba son dos revisiones leídas, ninguna con sello: el estado en que
// scanRevisions deja `out` en la lectura que dispara la poda, porque la columna
// todavía es NULL cuando el cursor la leyó.
func revisionesDePrueba() []Revision {
	return []Revision{
		{IntakeID: "i-1", RevisionNo: 1, Kind: RevisionKindCart},
		{IntakeID: "i-1", RevisionNo: 2, Kind: RevisionKindInterpreted},
	}
}

// TestSellarPodada_LosTresCasos recorre los tres estados en los que puede quedar una
// revisión al leerla, que son los tres que el consumidor tiene que poder distinguir.
func TestSellarPodada_LosTresCasos(t *testing.T) {
	sello := time.Date(2026, 8, 27, 14, 3, 11, 0, time.UTC)
	antes := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)

	casos := []struct {
		nombre    string
		enColumna time.Time
		sellado   time.Time
		quiere    time.Time
	}{
		{
			// No vencida: no se programa poda, así que a esta función no llega
			// ningún sello. La revisión sale sin marca y el consumidor lee «nunca
			// hubo texto», que es la verdad.
			nombre: "no vencida, columna NULL: no se publica nada",
		},
		{
			// EL CASO QUE ESTE ARREGLO EXISTE PARA CERRAR: la poda acaba de sellar
			// la fila y la columna que leyó el cursor era NULL. Antes de esto la
			// respuesta salía muda y la SIGUIENTE lectura decía otra cosa.
			nombre:  "vencida, columna NULL: se publica el sello que acaba de ponerse",
			sellado: sello,
			quiere:  sello,
		},
		{
			// Ya podada en una lectura anterior: la columna manda y el sello no la
			// mueve. Si esta regla se cae, `literal_pruned_at` deja de ser el
			// instante en que el texto se destruyó y pasa a ser «la última vez que
			// alguien miró», que no es un dato de auditoría.
			nombre:    "ya podada, columna poblada: la columna manda",
			enColumna: antes,
			sellado:   sello,
			quiere:    antes,
		},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			revs := revisionesDePrueba()
			revs[1].LiteralPrunedAt = c.enColumna

			sellarPodada(revs, 2, c.sellado)

			if !revs[1].LiteralPrunedAt.Equal(c.quiere) {
				t.Fatalf("literal_pruned_at = %v, quiero %v", revs[1].LiteralPrunedAt, c.quiere)
			}
			// Y la hermana no se entera: se poda UNA revisión, no la solicitud.
			// Sin esta mitad, un sellarPodada que marcara toda la lista pasaría el
			// caso de arriba y convertiría en «podadas» revisiones que nunca
			// tuvieron texto.
			if !revs[0].LiteralPrunedAt.IsZero() {
				t.Fatalf("se selló también la revisión 1: %v", revs[0].LiteralPrunedAt)
			}
		})
	}
}

// TestSellarPodada_UnaRevisionQueNoEstaNoRompeNada: la lista de podas se arma en el
// cursor y se aplica después, así que nada garantiza a nivel de tipos que el número
// exista en `out`. Que no cuadre no puede costar un panic en la ruta que sirve la
// bandeja del dueño.
func TestSellarPodada_UnaRevisionQueNoEstaNoRompeNada(t *testing.T) {
	revs := revisionesDePrueba()
	sellarPodada(revs, 99, time.Now())
	for i, r := range revs {
		if !r.LiteralPrunedAt.IsZero() {
			t.Fatalf("la revisión %d se selló con un revision_no que no era el suyo", i+1)
		}
	}
	sellarPodada(nil, 1, time.Now()) // y sobre una lista vacía tampoco
}
