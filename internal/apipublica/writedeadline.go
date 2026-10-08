// Porta internal/publicapi/plazoescritura.go @ ed60c24 (136 líneas).
//
// writedeadline.go — EL PLAZO DE ESCRITURA DE LA RUTA QUE ESPERA A UN MODELO. En la spec FX era
// `plazoescritura.go` (05 E-11: nombres en inglés): `conPlazoDeRedacción` es aquí writeDeadline.
// No exporta nada: nace con el verde y su test es interno (05 E-4, P6).
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 EL DEFECTO QUE CIERRA, MEDIDO EN UAT EL 2026-08-28
// ════════════════════════════════════════════════════════════════════════════
//
// `POST /api/v1/intakes/{id}/quote-suggestion` NO PODÍA RESPONDER NUNCA con un caso real. La
// redacción con el modelo detrás tardó 24,8 s / 28,4 s / 29,7 s / 35,5 s (n=4, contra
// 127.0.0.1:8103 en el propio VPS, con el modelo ya cargado), y el http.Server de la API pública
// sirve con `WriteTimeout = 10 s`. En Go ese plazo NO INTERRUMPE AL HANDLER: el handler termina,
// el servidor da el 200 y lo registra (`status=200 duration_ms=29687`), pero la respuesta ya no
// cabe por el cable y el cliente ve `curl: (52) Empty reply from server`. Un 400 de la MISMA
// ruta —rápido— salía en 0,22 s sin problema: no era el montaje, era el reloj.
//
// ════════════════════════════════════════════════════════════════════════════
// POR QUÉ NO SE SUBE EL `writeTimeout` GLOBAL
// ════════════════════════════════════════════════════════════════════════════
//
// Porque esa constante tiene DOS consecuencias que nadie ve desde aquí, y ninguna de las dos la
// ha pedido nadie:
//
//  1. De ella se DERIVA el presupuesto de la petición de envío de mensajes (SendBudgetFrom,
//     deadlines.go; Plan 050 · T5.4, REQ-050.19). Subirla habría movido el presupuesto de envío
//     de paso.
//  2. 🔴 LA COMPARTEN LOS DOS SERVIDORES HTTP del cloud: el público y el de ADMIN (el :8100 con
//     /healthz y /admin/*). Subirla habría relajado también el plazo de escritura de toda la
//     superficie de administración, que no tiene nada que ver con esto.
//
// Lo que se mueve aquí es el plazo de ESA ruta y de ninguna otra.
//
// ⚠️ Lo que este plazo NO es: un timeout de petición. No acota al handler ni cancela su contexto;
// solo dice hasta cuándo puede el servidor escribir en la conexión. Lo que acota el trabajo es el
// plazo de la llamada al modelo (quotetext.WithTimeout). Un handler que se cuelgue más de este
// plazo vuelve a dejar al cliente sin cuerpo — solo que ahora el listón está donde el trabajo
// cabe.
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 EL PLAZO LLEGA POR PARÁMETRO (mapa §4.3) — la diferencia con la cara vieja
// ════════════════════════════════════════════════════════════════════════════
//
// En la cara vieja el plazo era una constante de este fichero, `plazoDeEscrituraDeLaRedacción =
// pipeline.PlazoPorLlamadaSuelo + margenDeRedacción` (48 s + 12 s). Ninguna de las tres se porta:
// la cara nueva no importa el `pipeline` ni conoce esas cifras. La suma la hace el ARRANQUE, con
// el MISMO plazo que le pasa a quotetext.WithTimeout, y la entrega en
// IntakeReportsDeps.QuoteWriteDeadline: sigue siendo DERIVADO, NO COPIADO —si el suelo de la
// llamada sube, este plazo sube con él—, y además cierra el hueco que el fichero viejo declaraba
// («el enlace es por el mismo símbolo, no por el mismo valor»): ahora quien cablea el servicio y
// quien fija el plazo son la misma mano.
//
// Lo que el margen tiene que cubrir, para quien lo calcule: TODO lo que el handler hace fuera de
// la llamada al modelo y que NO tiene plazo propio —la lectura de la solicitud con sus líneas, la
// del historial aprobado, la de la semilla de estilo, el armado del few-shot, el verificador de
// precios, el render y la serialización del JSON—. Son milisegundos; los 35,5 s del peor caso
// medido son la inferencia.

package apipublica

import (
	"net/http"
	"time"

	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"
)

// writeDeadline (conPlazoDeRedacción en la cara vieja) extiende el plazo de escritura de la
// conexión a now() + deadline ANTES de empezar el trabajo lento, y solo para la petición que
// atraviesa este middleware. Después sirve h, siempre y una sola vez.
//
// POR QUÉ ENVUELVE LA RUTA ENTERA (y no va dentro del handler, ni dentro de protectRead; T-6 de
// FX): el controlador de respuesta llega a la conexión desenvolviendo `Unwrap()` envoltorio a
// envoltorio. accessLog mete el suyo (observedResponse, chain.go) DENTRO de protectRead, y ese
// no desenvuelve: un middleware colgado por debajo recibiría un ResponseWriter que corta la
// cadena y el plazo no se pondría. Aquí arriba la cadena está intacta — y si algún día deja de
// estarlo, no falla en silencio: se registra (ver abajo) y muere
// TestQuoteSuggestion_WriteDeadlineReachesTheConnection.
//
// El plazo se pone también para el 401, el 403 y el 400 de la ruta, que responden en
// milisegundos y no lo necesitan. Es el precio de ponerlo donde la cadena está intacta, y no
// cuesta nada: un plazo de escritura amplio en una respuesta inmediata no cambia nada salvo
// cuánto tardaría en rendirse un cliente que dejó de leer.
//
// ⚠️ SI ALGÚN DÍA ESTO SE APLICA A UNA RUTA CON CUERPO (las candidatas naturales son la
// aprobación y la petición de información, que mandan JSON), hay que saber esto antes: mientras
// a `net/http` le quede cuerpo de petición SIN LEER, no arranca la lectura de fondo con la que
// detecta que el cliente cerró, así que `r.Context()` NO se cancela y una llamada abortada
// parece eterna desde el servidor. Medido con el mismo handler y el cliente abortando a los
// 200 ms: sin drenar `r.Body`, CERO cancelaciones en 1,5 s; drenándolo primero, cancelación
// detectada en 200 ms. A la sugerencia de cotización no le afecta: es un POST SIN cuerpo.
//
// El fallo NO aborta la petición: sin plazo extendido la ruta se comporta como antes del arreglo
// —mal para el caso lento, bien para el rápido—, y cortar la petición cambiaría un defecto de
// entrega por una caída. Se registra a Warn porque es exactamente la avería que estuvo
// invisible: el servidor creyéndose entregado mientras el cliente no recibe nada. Un log nil no
// es un error: se sirve igual, en silencio.
//
// El reloj entra por parámetro (now) para que el plazo exacto se pueda afirmar en un test; el
// arranque no lo cablea y MountIntakeReports pone time.Now.
func writeDeadline(log sharedlogger.Logger, now func() time.Time, deadline time.Duration, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := http.NewResponseController(w).SetWriteDeadline(now().Add(deadline)); err != nil {
			if log != nil {
				log.Warn("no se pudo extender el plazo de escritura de la sugerencia de cotización: "+
					"la respuesta larga volverá a no caber por el cable",
					"path", r.URL.Path, "plazo", deadline.String(), "error", err)
			}
		}
		h.ServeHTTP(w, r)
	})
}
