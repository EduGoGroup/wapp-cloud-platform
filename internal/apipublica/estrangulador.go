package apipublica

import (
	"net/http"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// Compuesto es el estrangulador: el handler público que pone la cara nueva DELANTE del mux del
// `publicapi` viejo mientras dura la reconstrucción (D-10). Se borra en F10, cuando la cara
// vieja ya no se construye (RX.6.c).
//
// Por qué dos muxes y no uno: la cara vieja se construye con su propio *http.ServeMux (el
// arranque viejo, intacto) y la nueva con el suyo; registrar las dos en un mux común obligaría
// a quitar de la vieja cada ruta que se muda, y eso es editar el código viejo (E-1).
//
// Por qué no un "/" de reserva en la nueva que llame a la vieja: un "/" casa con TODO camino y
// TODO método, así que la nueva nunca respondería su 405 (un DELETE a una ruta solo-GET de la
// nueva caería al "/" y de ahí a la vieja, que respondería 404 con una ruta que sí existe), y
// preguntar a la nueva «¿casas?» daría siempre ("nueva", "/"): Resolver no podría decir qué cara
// sirve cada patrón. Por eso el Compuesto pregunta a cada mux con Handler (sin servir) y sirve
// con el que casa.
//
// Gana la nueva: si las dos caras casan con la petición, la sirve la nueva. Así mudar una ruta es
// solo registrarla en la nueva; la vieja la sigue teniendo registrada, pero ya no la alcanza.
//
// Una petición que ninguna cara conoce termina en el 404 PROPIO del mux viejo (estado 404,
// cuerpo exactamente "404 page not found\n"), y una con método equivocado en un 405 con la
// cabecera Allow de la cara que conoce la ruta: son las dos respuestas que
// internal/arranque/huellatest usa para dar una ruta por NO montada. Cualquier otra respuesta la
// haría pasar por montada.
type Compuesto struct {
	// Las dos caras nacen con el verde (en rojo serían campos muertos).
}

// Componer construye el Compuesto con la cara nueva delante de vieja (el mux del publicapi
// viejo). Un nil en cualquiera de los dos hace panic AL CONSTRUIR, con un mensaje propio (no el
// de pendiente): es un fallo de cableado del arranque, no de una petición, y no debe esperar a
// la primera petición para aparecer.
func Componer(nueva *Cara, vieja *http.ServeMux) *Compuesto {
	panic(pendiente.Implementar("apipublica.Componer"))
}

// ServeHTTP sirve r con UNA de las dos caras, por este orden:
//
//  1. si nueva.Handler(r) da un patrón ≠ "" → nueva.ServeHTTP(w, r);
//  2. si no, si vieja.Handler(r) da un patrón ≠ "" → vieja.ServeHTTP(w, r);
//  3. si ninguna casa: si la nueva CONOCE LA RUTA (con otro método) → nueva.ServeHTTP(w, r), que
//     responde su 405 con su Allow; si no → vieja.ServeHTTP(w, r), que responde su 404 o su 405.
//
// «Conoce la ruta»: algún método de los que aparecen en nueva.Patrones() (más los patrones sin
// método) casa con r.URL.Path; se prueba preguntando a nueva.Handler por una copia superficial de
// r con ese otro Method. Solo corre en el camino de error (método equivocado); su coste está sin
// medir y es irrelevante fuera de ese camino.
//
// Siempre con EL MISMO r (sin copia ni clon): el mux que sirve rellena r.Pattern en ese puntero,
// y la métrica que envuelve al Compuesto lo lee después. No añade middleware, cabeceras ni
// escritura propias: la respuesta es exactamente la de la cara elegida.
func (x *Compuesto) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	panic(pendiente.Implementar("apipublica.Compuesto.ServeHTTP"))
}

// Resolver dice qué cara serviría r y con qué patrón, SIN servirla (no invoca handler alguno):
// ("nueva", p) si la regla (1) de ServeHTTP casa con el patrón p; ("vieja", p) si casa la (2);
// ("", "") si ninguna (el camino de error, 404 o 405, no es una resolución). Es lo que usan la
// huella del arranque y el candado de mudanzas para saber qué cara sirve cada patrón.
func (x *Compuesto) Resolver(r *http.Request) (cara, patron string) {
	panic(pendiente.Implementar("apipublica.Compuesto.Resolver"))
}
