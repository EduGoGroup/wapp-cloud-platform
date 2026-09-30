package arranque

// mudanzas.go — qué rutas del :8103 sirve ya la cara NUEVA (internal/apipublica).
//
// Fichero nuevo del arranque nuevo (no es copia del viejo: F0 · TX.4, D-10). La cara
// HTTP nueva crece por olas delante del publicapi viejo (estrangulador,
// apipublica.Componer en http.go), y cada fase muda SUS rutas según el mapa de
// documentations/reorganizacion-modular/plan/FX-cara-http/mapa-de-rutas.md, cuya
// copia ejecutable es testdata/mapa.tsv (id · listener · patrón · fase).
//
// El candado (mudanzas_test.go) exige que la cara nueva sirva EXACTAMENTE las filas
// del mapa con fase ≤ FaseActual y la vieja el resto, que no quede una familia
// partida entre las dos caras ni un comodín de la nueva solapando un literal que
// sigue en la vieja. Por eso la tarea `conmutar(<m>)` de cada fase sube FaseActual
// en el MISMO commit que monta las rutas: la tabla y el cableado avanzan juntos.

import "github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"

// FaseActual es la última fase de la reconstrucción cuyas rutas del :8103 sirve la
// cara nueva: 0 en F0 (la cara nace vacía y todo cae al publicapi viejo), 2 tras
// conmutar acceso, … 8 tras conmutar conversacion. La fase de una fila del mapa se
// escribe «F<n>»; la fila pertenece a la cara nueva si n ≤ FaseActual.
const FaseActual = 0

// caraNueva construye la cara nueva con las rutas de las fases ≤ FaseActual. En F0
// no monta ninguna: cada fase añadirá aquí su apipublica.Montar<Área>.
func caraNueva() *apipublica.Cara {
	return apipublica.Nueva()
}
