package publicapi

import (
	"fmt"
	"net/http"
)

// limits.go concentra la FORMA de una sola respuesta: el 413 de los techos de
// cuerpo de la API pública (Plan 041 · A5, REQ-16).
//
// El rojo A5 decía que el 413 «no nombra la cifra», y la razón por la que estuvo
// abierto es que los techos no son uno: la config de intents, las variables de
// empresa, el blob de tenant-content, el import JSON, el import tabular (dos
// techos: el sobre multipart y el archivo) y el cuerpo del callback CRM. Cambiar
// uno solo los desalinea, así que el criterio vive AQUÍ, en un sitio, y cada
// handler pone únicamente su sujeto y SU número.
//
// CRITERIO ÚNICO, en dos mitades:
//
//   - La FRASE es la misma en los siete: «<sujeto> excede el tamaño máximo de N
//     bytes». Es lo que REQ-16 pide literalmente —un mensaje que nombre el
//     límite— y lo que un operador lee sin abrir el código. El sujeto se conserva
//     tal cual lo tenía cada endpoint (la config, el cuerpo, el contenido, el
//     documento, el archivo): decirle «el documento» a quien subió una planilla
//     sería peor que no decir nada.
//   - El CAMPO `max_bytes` acompaña a la frase para que la UI no tenga que
//     parsear prosa. Es el mismo patrón que el 403 del gate de features
//     ({"error":"feature_not_enabled","feature":"…"}, entitlements/middleware.go):
//     el cuerpo de error de esta API es {"error": …} y admite campos extra cuando
//     hay un dato que la pantalla necesita. NO se inventa un envoltorio nuevo.
//
// La cifra va en BYTES, sin traducir a KiB/MiB: es la unidad de la variable de
// entorno que la gobierna (WAPP_TENANT_CONTENT_MAX_BYTES), así que el número que
// se lee en el error es el mismo que se escribe en la configuración. Redondearlo
// obligaría a deshacer el redondeo para actuar.
//
// ÚNICA excepción de FORMA, deliberada: el import tabular envuelve TODOS sus
// fallos en `validation_failed` + lista, para que la pantalla que los pinta no
// tenga que saber por qué puerta entró el documento (catalogtabular.go:102-105).
// Ahí se reusa la FRASE (con su cifra) y se respeta el envoltorio: unificar el
// envoltorio dejaría a esa pantalla sin nada que enseñar en el 413.

// tooLargeBody es el cuerpo del 413: la prosa de siempre —ahora con la cifra— más
// el límite en un campo aparte. El orden de los campos es el del resto de errores
// de la API: `error` primero.
type tooLargeBody struct {
	Error    string `json:"error"`
	MaxBytes int64  `json:"max_bytes"`
}

// tooLargeMessage arma la frase del 413. Se expone aparte del cuerpo porque el
// import tabular la necesita suelta, para meterla en su propio envoltorio.
func tooLargeMessage(qué string, maxBytes int64) string {
	return fmt.Sprintf("%s excede el tamaño máximo de %d bytes", qué, maxBytes)
}

// tooLarge arma el cuerpo del 413 (útil donde el handler no escribe la respuesta
// él mismo, sino que devuelve el cuerpo ya armado a su llamante).
func tooLarge(qué string, maxBytes int64) tooLargeBody {
	return tooLargeBody{Error: tooLargeMessage(qué, maxBytes), MaxBytes: maxBytes}
}

// writeTooLarge responde el 413 con el cuerpo canónico.
func writeTooLarge(w http.ResponseWriter, qué string, maxBytes int64) {
	writeJSON(w, http.StatusRequestEntityTooLarge, tooLarge(qué, maxBytes))
}

// errorBody es el cuerpo de error CANÓNICO de la API pública: {"error": prosa}.
// Existe para que un handler que devuelve su cuerpo al llamante (en vez de
// escribirlo) use exactamente la misma forma que writeError.
func errorBody(msg string) map[string]string {
	return map[string]string{"error": msg}
}
