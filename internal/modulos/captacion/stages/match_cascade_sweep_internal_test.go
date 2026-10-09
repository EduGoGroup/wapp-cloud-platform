package stages

// match_cascade_sweep_internal_test.go — EL TEST INTERNO DE LAS COTAS.
//
// Va en el paquete `stages` y no en `stages_test` a propósito: lo que aquí se comprueba
// es la ARITMÉTICA que autoriza a saltarse un candidato sin compararlo, y esa aritmética
// no es API. Desde fuera solo se puede observar su consecuencia —lo hace el diferencial
// contra el oráculo lineal de match_cascade_sweep_test.go—, y una consecuencia correcta
// puede esconder una cota rota que todavía no ha perdido ningún match.

import (
	"context"
	"testing"
	"unicode/utf8"

	"github.com/EduGoGroup/wapp-shared/textmatch"
)

// TestMaxDistance_IsTheTableOfD04445: la tabla de D-044.45 con literales escritos a
// mano. Si alguien mueve el umbral, esto se pone rojo ANTES de que el match empiece a
// casar cosas que no debía.
func TestMaxDistance_IsTheTableOfD04445(t *testing.T) {
	table := map[int]int{
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
	for runes, want := range table {
		if got := maxDistance(runes); got != want {
			t.Errorf("con %d runas caben %d ediciones; se esperaban %d", runes, got, want)
		}
	}
}

// boundCorpus son los pares con los que se contrasta la cota contra la distancia de
// verdad: palabras del dominio, erratas de las que de verdad se teclean (transposición,
// ñ→n, letra de más) y textos sin nada que ver.
func boundCorpus() []string {
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

// TestBound_NeverExceedsTheRealDistance es LA PROPIEDAD de la que depende todo el
// prefiltro: una cota INFERIOR nunca puede valer más que la distancia que acota. Si
// valiera más, discardedByBound empezaría a tirar pares que sí casan y las líneas
// saldrían `unmatched` sin que nada lo dijera. El corpus va contra sí mismo (n×n) para
// que entren también los pares iguales y los que no tienen nada que ver.
func TestBound_NeverExceedsTheRealDistance(t *testing.T) {
	corpus := boundCorpus()
	tight, loose := 0, 0
	for _, a := range corpus {
		pa := profileOf(a)
		for _, b := range corpus {
			bound := pa.bound(profileOf(b))
			realDist := textmatch.EditDistance(a, b)
			if bound > realDist {
				t.Fatalf("la cota (%d) pasó de la distancia real (%d) para %q vs %q: el prefiltro perdería matches",
					bound, realDist, a, b)
			}
			switch {
			case bound == realDist && realDist > 0:
				tight++
			case bound < realDist:
				loose++
			}
		}
	}
	// META-TEST: un `cota <= real` es trivialmente cierto si la cota fuera siempre 0.
	// Estos dos números dicen que la cota SE MUEVE: unas veces acierta la distancia
	// exacta y otras se queda corta, que es lo que una cota hace.
	if tight <= 20 {
		t.Fatalf("la cota solo ALCANZA la distancia real en %d pares; tienen que ser más de 20", tight)
	}
	if loose <= 20 {
		t.Fatalf("la cota solo se queda corta en %d pares: si no, no es una cota, es la distancia", loose)
	}
}

// TestDiscardedByBound_NeverDiscardsWhatTheCascadeMatches es la otra mitad: sobre el
// mismo corpus, ningún par que la cascada por defecto declare Match puede haber sido
// descartado por el prefiltro. Caza la aritmética rota aunque el catálogo del
// diferencial no tuviera el par que la destapa.
func TestDiscardedByBound_NeverDiscardsWhatTheCascadeMatches(t *testing.T) {
	corpus := boundCorpus()
	cascade := DefaultCascade()
	matched, discarded := 0, 0
	for _, a := range corpus {
		na := textmatch.Normalize(a)
		pa, la := profileOf(na), utf8.RuneCountInString(na)
		for _, b := range corpus {
			nb := textmatch.Normalize(b)
			r, err := cascade.Compare(context.Background(), na, nb)
			if err != nil {
				t.Fatalf("la cascada por defecto no falla: %v", err)
			}
			discard := discardedByBound(la, utf8.RuneCountInString(nb), pa, profileOf(nb))
			if r.Outcome == textmatch.OutcomeMatch {
				matched++
				if discard {
					t.Fatalf("la cascada casa %q con %q (%.4f) y el prefiltro lo había descartado", a, b, r.Confidence)
				}
				continue
			}
			if discard {
				discarded++
			}
		}
	}
	if matched <= 25 {
		t.Fatalf("el corpus solo produjo %d matches; tiene que producir matches de verdad", matched)
	}
	if discarded <= 300 {
		t.Fatalf("el prefiltro solo descartó %d pares; tiene que estar descartando de verdad", discarded)
	}
}
