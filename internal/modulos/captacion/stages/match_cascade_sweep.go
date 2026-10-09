// Porta internal/intake/stages/match_cascada.go @ 4cd9cfb

package stages

import (
	"context"
	"unicode/utf8"

	"github.com/EduGoGroup/wapp-shared/textmatch"
)

// match_cascade_sweep.go — EL BARRIDO Y SU PREFILTRO: el escalón del medio de la cascada
// de match_cascade.go, partido de él por tamaño (E-13). No tiene exportados: lo que
// promete lo dicen DefaultCascade y LengthMargin.
//
// # POR QUÉ HAY UN PREFILTRO Y NO SE COMPARA CONTRA TODO
//
// Con el catálogo en su techo (2.000 artículos, D-044.44) y un pedido en el suyo (10
// ítems, D-044.39), comparar todo son 20.000 distancias de edición por pedido. MEDIDO en
// el paquete viejo: **20,3 ms por ítem**, contra un criterio de 5 ms. El barrido ingenuo
// NO cabe, y no por poco.
//
// Lo que sí cabe es descartar por dos COTAS INFERIORES de la distancia, las dos exactas
// y las dos O(1) sobre datos ya calculados:
//
//  1. **Longitud**: `dist >= |la − lb|`. Una edición cambia la longitud como mucho en
//     uno.
//  2. **Composición**: `dist >= ⌈Σ|cA(r) − cB(r)| / 2⌉`, donde `c(r)` es cuántas veces
//     aparece la runa `r`. Cada edición mueve el histograma como mucho en 2 —una
//     sustitución quita una runa y pone otra, un alta o una baja mueven una—, y una
//     TRANSPOSICIÓN no lo mueve nada, así que la cota vale también para la OSA que usa
//     `textmatch.EditDistance`.
//
// Si CUALQUIERA de las dos cotas ya pasa de maxDistance, el par no puede dar Match y no
// se compara. Nunca al revés: una cota inferior jamás descarta un match —eso es lo que
// la palabra INFERIOR significa— y lo custodian dos tests, el diferencial contra el
// oráculo lineal y el de la propiedad `cota <= distancia real`.
//
// El barrido vuelve a probar `Exact` sobre cada candidato, y esa redundancia con el
// escalón por clave es DELIBERADA: el día que el índice y `Exact` dejaran de opinar lo
// mismo, el resultado seguiría siendo correcto (más lento, pero correcto).

// maxDistance es cuántas ediciones caben todavía dentro del umbral para dos textos cuyo
// más largo mide `longest` runas: `sim = 1 − dist/longest >= t` equivale a
// `dist <= (1−t)·longest`, y como la distancia es entera, al suelo.
//
// Es la tabla de D-044.45 dicha en código: con 7 runas cabe UNA errata (1,05 ⇒ 1) y con
// 6 no cabe ninguna (0,9 ⇒ 0), que es por lo que «ñoquis» no casa «noquis».
func maxDistance(longest int) int {
	return int(LengthMargin * float64(longest))
}

// profileBuckets son los cubos del histograma: 26 letras, uno para los dígitos, uno para
// el espacio y uno para TODO LO DEMÁS (ñ, símbolos, letras no latinas).
//
// Fundir runas distintas en un cubo solo puede BAJAR la suma de diferencias, o sea
// AFLOJAR la cota: sigue siendo una cota inferior, solo que menos apretada. Lo que no
// puede es apretarla de más, que es lo único que rompería el prefiltro.
const profileBuckets = 29

// profile es el histograma de runas de un texto YA NORMALIZADO.
//
// Los contadores son `uint8` y SATURAN en 255. Saturar tampoco rompe nada por el mismo
// motivo: una diferencia recortada es una diferencia menor, y una cota menor sigue
// siendo inferior.
type profile [profileBuckets]uint8

// profileOf cuenta las runas de un texto normalizado.
func profileOf(s string) profile {
	var p profile
	for _, r := range s {
		i := profileBuckets - 1
		switch {
		case r >= 'a' && r <= 'z':
			i = int(r - 'a')
		case r >= '0' && r <= '9':
			i = 26
		case r == ' ':
			i = 27
		}
		if p[i] < 255 {
			p[i]++
		}
	}
	return p
}

// bound devuelve ⌈Σ|diferencias| / 2⌉, la cota inferior por composición.
func (p profile) bound(q profile) int {
	sum := 0
	for i := range p {
		if p[i] > q[i] {
			sum += int(p[i] - q[i])
			continue
		}
		sum += int(q[i] - p[i])
	}
	return (sum + 1) / 2
}

// sweep barre las etiquetas y se queda con la MEJOR, no con la primera.
//
// Con 2.000 artículos, «la primera que pase de 0,85» puede ser un 0,86 mientras diez
// posiciones más abajo hay un 0,99. El empate se rompe por orden de documento, así que
// el resultado sigue siendo determinista.
func (s *Match) sweep(ctx context.Context, sc *scanner, text string) (finding, bool, error) {
	target := textmatch.Normalize(text)
	runes := utf8.RuneCountInString(target)
	fingerprint := profileOf(target)

	best, bestConf, bestStrategy := -1, 0.0, ""
	for i, cand := range sc.norm {
		if discardedByBound(runes, sc.runes[i], fingerprint, sc.profiles[i]) {
			continue
		}
		r, err := s.cmp.Compare(ctx, target, cand)
		if err != nil {
			return finding{}, false, err
		}
		if r.Outcome == textmatch.OutcomeMatch && r.Confidence > bestConf {
			best, bestConf, bestStrategy = i, r.Confidence, r.Strategy
		}
	}
	if best < 0 {
		return finding{}, false, nil
	}
	// La estrategia la declara el propio Result, no este bucle: el escalón que resolvió
	// el par lo sabe él, y copiarlo evita que la procedencia diga «fuzzy» el día que la
	// cascada gane un escalón determinista más.
	return finding{
		match:      sc.idx.En(best),
		provenance: MatchProvenance{Strategy: bestStrategy, Confidence: bestConf},
	}, true, nil
}

// discardedByBound responde si el par NO PUEDE dar Match, usando las dos cotas
// inferiores de la distancia.
//
// Es la única función que decide sin comparar, así que es la única que podría perder un
// match. Por eso las dos desigualdades van en el mismo sitio y con el mismo tope.
func discardedByBound(la, lb int, pa, pb profile) bool {
	longest := la
	if lb > longest {
		longest = lb
	}
	limit := maxDistance(longest)

	diff := la - lb
	if diff < 0 {
		diff = -diff
	}
	if diff > limit {
		return true
	}
	return pa.bound(pb) > limit
}
