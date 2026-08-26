package stages

// match_cota_test.go — EL TEST INTERNO DE LAS COTAS.
//
// Va en el paquete `stages` y no en `stages_test` a propósito: lo que aquí se
// comprueba es la ARITMÉTICA que autoriza a saltarse un candidato sin compararlo, y
// esa aritmética no es API. Desde fuera solo se puede observar su consecuencia —lo
// hace el test diferencial contra el oráculo lineal—, y una consecuencia correcta
// puede esconder una cota rota que todavía no ha perdido ningún match.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EduGoGroup/wapp-shared/textmatch"
)

// TestDistanciaMaxima_EsLaTablaDeD04445: la tabla de D-044.45 dicha con literales
// escritos a mano. Si alguien mueve el umbral, esto se pone rojo ANTES de que el
// match empiece a casar cosas que no debía.
func TestDistanciaMaxima_EsLaTablaDeD04445(t *testing.T) {
	tabla := map[int]int{
		3:  0, // «pan»: ni una errata
		4:  0, // «café»
		5:  0, // «torta», «pizza»
		6:  0, // «ñoquis» ⇒ por esto NO casa «noquis»
		7:  1, // desde aquí sí cabe UNA
		8:  1, // «tequeños»
		19: 2, // «tequeños congelados»
		20: 3,
		40: 6,
	}
	for runas, esperado := range tabla {
		require.Equalf(t, esperado, distanciaMaxima(runas),
			"con %d runas y umbral %.2f caben %d ediciones", runas, textmatch.DefaultFuzzyThreshold, esperado)
	}
}

// corpusDeCotas son los pares con los que se contrasta la cota contra la distancia
// de verdad: palabras del dominio, erratas de las que de verdad se teclean
// (transposición, ñ→n, letra de más), y textos sin nada que ver.
func corpusDeCotas() []string {
	return []string{
		"", "a", "pan", "pon", "torta", "tarta", "ñoquis", "noquis",
		"tequeños congelados", "tequenos congelados", "teqeuños congelados",
		"torta chocolate humedo crema choc", "torta de chocolate",
		"hamburguesa", "hamburgueza", "hamburguesas", "amburguesa",
		"jugo natural de naranja exprimido", "jugo natural de naraja exprimido",
		"bicicleta de montaña rodado 26", "alfajor de maicena premium 1234",
		"whatsapp", "whastapp", "aaaaaaaaaa", "aaaaaaaaab",
	}
}

// TestCota_NUNCAPasaDeLaDistanciaReal es LA PROPIEDAD de la que depende todo el
// prefiltro: una cota INFERIOR nunca puede valer más que la distancia que acota. Si
// valiera más, `descartaPorCota` empezaría a tirar pares que sí casan y las líneas
// saldrían `unmatched` sin que nada lo dijera.
//
// El corpus va contra sí mismo (n×n) para que entren también los pares iguales y los
// que no tienen nada que ver.
func TestCota_NUNCAPasaDeLaDistanciaReal(t *testing.T) {
	corpus := corpusDeCotas()
	apretadas, sueltas := 0, 0
	for _, a := range corpus {
		pa := perfilDe(a)
		for _, b := range corpus {
			cota := pa.cota(perfilDe(b))
			real := textmatch.EditDistance(a, b)
			require.LessOrEqualf(t, cota, real,
				"🔴 la cota (%d) pasó de la distancia real (%d) para %q vs %q: el prefiltro perdería matches",
				cota, real, a, b)
			switch {
			case cota == real && real > 0:
				apretadas++
			case cota < real:
				sueltas++
			}
		}
	}

	// 🔴 META-TEST: un `cota <= real` es trivialmente cierto si la cota fuera siempre
	// 0. Estos dos números dicen que la cota SE MUEVE: unas veces acierta la
	// distancia exacta y otras se queda corta, que es lo que una cota hace.
	require.Greater(t, apretadas, 20, "la cota tiene que ALCANZAR la distancia real en bastantes pares")
	require.Greater(t, sueltas, 20, "y quedarse corta en otros: si no, no es una cota, es la distancia")
}

// TestDescartaPorCota_NoDescartaNADAQueLaCascadaCASE es la otra mitad: sobre el
// mismo corpus, ningún par que la cascada por defecto declare Match puede haber sido
// descartado por el prefiltro.
//
// Es la misma propiedad que el test diferencial mira desde fuera, dicha aquí sobre
// la función que decide. Las dos hacen falta: ésta caza la aritmética rota aunque el
// catálogo del diferencial no tuviera el par que la destapa.
func TestDescartaPorCota_NoDescartaNADAQueLaCascadaCASE(t *testing.T) {
	corpus := corpusDeCotas()
	c := CascadaPorDefecto()
	casados, descartados := 0, 0
	for _, a := range corpus {
		na := textmatch.Normalize(a)
		pa, la := perfilDe(na), len([]rune(na))
		for _, b := range corpus {
			nb := textmatch.Normalize(b)
			r, err := c.Compare(context.Background(), na, nb)
			require.NoError(t, err)
			descarta := descartaPorCota(la, len([]rune(nb)), pa, perfilDe(nb))
			if r.Outcome == textmatch.OutcomeMatch {
				casados++
				require.Falsef(t, descarta,
					"🔴 la cascada casa %q con %q (%.4f) y el prefiltro lo había descartado", a, b, r.Confidence)
				continue
			}
			if descarta {
				descartados++
			}
		}
	}
	require.Greater(t, casados, 25, "el corpus tiene que producir matches de verdad")
	require.Greater(t, descartados, 300, "y el prefiltro tiene que estar descartando de verdad")
}
