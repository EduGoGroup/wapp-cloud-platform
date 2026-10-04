// Porta internal/entitlements/middleware.go @ 9a77307

package entitlements

import (
	"net/http"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// RequireFeature devuelve un middleware net/http que exige la feature al tenant de la Identity
// autenticada (httpapi.IdentityFromContext; INV-8: el tenant sale del token, nunca de la
// petición). Es la forma canónica nº 1 del gate de features (design §D-040.5); la nº 2, el check
// in-code, está en el comentario de paquete.
//
// Se compone SIEMPRE después de Authenticate y de RequirePermission: el scope dice «puedes operar
// esto», la feature dice «tu plan lo incluye»; son dos preguntas distintas y ninguna sustituye a
// la otra. Con la feature (Has = true, nil) llama a next sin tocar la respuesta.
//
// Sin la feature corta con 403, Content-Type application/json y EXACTAMENTE el cuerpo
// {"error":"feature_not_enabled","feature":"<clave>"} (R-E4: un código estable que la UI reconoce
// sin parsear prosa, y la clave que faltó). Un override enabled=false cuenta como no tenerla
// (R-E2): el Resolver responde false y el gate corta igual.
//
// FAIL-CLOSED en los tres modos de no-resolución (R-E1): sin Identity en el contexto (o con una
// Identity sin tenant), con el Resolver devolviendo error, o con el Resolver nil. Los tres cortan
// con el MISMO 403 y el MISMO cuerpo, nunca con 500 ni dejando pasar, y sin panic: el llamante no
// debe distinguir «no lo tienes» de «no pude averiguarlo», y un 5xx invitaría a reintentar hasta
// colarse. Sin identidad o sin resolver no se pregunta al Resolver.
func RequireFeature(resolver Resolver, feature string) func(http.Handler) http.Handler {
	panic(pendiente.Implementar("entitlements.RequireFeature"))
}

// RequireAnyFeature es el gate de una capacidad que NO pertenece a una feature sino a varias:
// pasa el tenant que tenga AL MENOS UNA de las claves dadas (R-E3: con una basta). Nace del
// listado de eventos conversacionales (Plan 043 · T3.9b): la bandeja abarca los cuatro tipos de
// fábrica —menu, cart, survey, media—, cada uno una feature, y gatearla con una sola habría cegado
// a un tenant de solo encuestas sobre sus PROPIAS encuestas. Quien la use debe además filtrar el
// CONTENIDO por las features que el tenant sí tiene: pasar el gate por una no da derecho a ver
// las otras.
//
// Las claves se preguntan en el orden dado y la primera concedida abre (llama a next). Mismas
// reglas que RequireFeature: se compone después de Authenticate y RequirePermission, un override
// enabled=false cuenta como no tenerla (R-E2), y es FAIL-CLOSED en los tres modos de
// no-resolución (R-E1).
//
// R-E3, lo propio del plural:
//   - un error al preguntar por UNA clave CORTA en el acto con 403: no se sigue con las demás, o
//     un resolver medio caído se leería como «no tiene ninguna» solo a veces, según el orden;
//   - una lista VACÍA no abre: es «ninguna basta», no «todas valen».
//
// R-E4: todo corte responde 403, Content-Type application/json y EXACTAMENTE el cuerpo
// {"error":"feature_not_enabled","features":["<k1>","<k2>",…]}, con las claves en el orden dado;
// con la lista vacía, {"error":"feature_not_enabled"} (las dos claves del cuerpo son omitempty, y
// por eso el cuerpo singular de RequireFeature no lleva "features"). El cuerpo no dice cuál falló
// ni por qué: los tres modos de no-resolución y «ninguna concedida» responden lo mismo.
func RequireAnyFeature(resolver Resolver, features ...string) func(http.Handler) http.Handler {
	panic(pendiente.Implementar("entitlements.RequireAnyFeature"))
}
