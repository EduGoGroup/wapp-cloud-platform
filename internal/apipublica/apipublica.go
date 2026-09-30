// Package apipublica es la cara `/api/v1` NUEVA de wApp (D-10): la única cara HTTP pública del
// árbol reconstruido, levantada delante del `internal/publicapi` viejo en vez de moverlo.
//
// Crece por olas: cada fase de la reconstrucción (F2…F8) muda aquí sus rutas según el mapa
// (plan/FX-cara-http/mapa-de-rutas.md) y el arranque nuevo llama a un `Montar<Área>` más. En F0
// no registra nada. Hasta que cierre F8 convive con la cara vieja a través del estrangulador
// (estrangulador.go): lo que esta cara registra lo sirve ella; lo que NO registra cae al
// `publicapi` viejo, intacto. En F10 se borran el estrangulador y el paquete viejo.
//
// Las cadenas por ruta (Authenticate → RequirePermission → gate de feature → handler, con su
// access-log y su auditoría) viven AQUÍ, en el fichero de cada área, no en el arranque: el
// arranque solo compone las caras y las envuelve una vez con métrica y rate limit.
//
// No porta ningún fichero viejo: la Cara y el estrangulador nacen nuevos (RX.5.d los exime de la
// cabecera E-10).
package apipublica

import (
	"net/http"
	"slices"
)

// Cara es la cara nueva: un *http.ServeMux propio y la lista de patrones que se le registraron
// por Handle, en el orden en que se registraron. La lista existe porque un ServeMux no se puede
// enumerar: el candado de mudanzas comprueba con ella que la cara no sirve un patrón de más.
//
// Una Cara se construye con Nueva; su valor cero no está listo para usarse.
//
// Registrar (Handle) es cosa del arranque, antes de servir: la lista no lleva cerrojo propio,
// igual que el cableado de cualquier mux de la casa. El mux sí es seguro para servir en
// concurrencia, porque es un *http.ServeMux.
type Cara struct {
	mux      *http.ServeMux
	patrones []string
}

// Nueva devuelve una cara vacía: Patrones() da una lista de longitud 0 y toda petición que
// sirve es el 404 del ServeMux (estado 404, cuerpo exactamente "404 page not found\n").
func Nueva() *Cara {
	return &Cara{mux: http.NewServeMux()}
}

// Handle registra h bajo patron en el mux de la cara Y anota patron al final de la lista de
// Patrones(), con el texto exacto recibido. La sintaxis de patron es la de http.ServeMux
// ("GET /api/v1/x/{id}", "/f/", …).
//
// Un patrón en conflicto con otro ya registrado (o inválido, o un h nil) hace panic IGUAL que
// http.ServeMux.Handle, con el mismo mensaje (salvo las ubicaciones «registered at
// fichero:línea» que el propio ServeMux incluye): no lo esconde ni lo traduce, porque un
// conflicto es un fallo de cableado que debe romper el arranque (el mismo criterio que el viejo
// TestMuxRegistration_NoPanic).
func (c *Cara) Handle(patron string, h http.Handler) {
	// Primero el mux: si panica (conflicto, patrón inválido, h nil) el panic sube tal cual y el
	// patrón NO llega a la lista, que así solo contiene lo que la cara sirve de verdad.
	c.mux.Handle(patron, h)
	c.patrones = append(c.patrones, patron)
}

// Patrones devuelve una COPIA de la lista de patrones registrados, en orden de registro. Mutar
// el slice devuelto (asignar o añadir) no altera la cara: la siguiente llamada da la lista
// original. Una cara sin registros da longitud 0.
func (c *Cara) Patrones() []string {
	// Clone y no el slice propio: el candado de mudanzas (u otro llamador) podría ordenarlo o
	// ampliarlo, y eso no debe reescribir lo que la cara registró. Sin registros, Clone de nil
	// es nil: longitud 0, como promete el contrato.
	return slices.Clone(c.patrones)
}

// Handler devuelve lo mismo que http.ServeMux.Handler sobre el mux de la cara: el handler y el
// patrón que casan con r, o, si ninguno casa, el handler de error del mux (su 404, o su 405 con
// Allow si el camino existe con otro método) y el patrón "".
//
// Solo consulta: no invoca ningún handler (no escribe respuesta alguna) ni muta r (r.Pattern y
// los valores de PathValue siguen como estaban).
func (c *Cara) Handler(r *http.Request) (http.Handler, string) {
	// http.ServeMux.Handler solo busca: no rellena r.Pattern ni los PathValue (eso lo hace
	// ServeHTTP), y ante un no-casa devuelve el handler de error del mux con patrón "".
	return c.mux.Handler(r)
}

// ServeHTTP delega en el mux de la cara: el handler registrado sirve la petición (con
// r.Pattern y PathValue rellenos por el mux), y lo que no casa recibe el 404 o el 405 con Allow
// del propio ServeMux. No añade middleware, cabeceras ni escritura propias.
func (c *Cara) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mux.ServeHTTP(w, r)
}
