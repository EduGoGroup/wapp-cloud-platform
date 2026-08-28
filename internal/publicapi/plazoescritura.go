package publicapi

import (
	"net/http"
	"time"

	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/pipeline"
)

// plazoescritura.go — EL PLAZO DE ESCRITURA DE LA RUTA QUE ESPERA A UN MODELO.
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 EL DEFECTO QUE CIERRA, MEDIDO EN UAT EL 2026-08-28
// ════════════════════════════════════════════════════════════════════════════
//
// `POST /api/v1/intakes/{id}/quote-suggestion` NO PODÍA RESPONDER NUNCA con un caso
// real. La redacción con el modelo detrás tardó 24,8 s / 28,4 s / 29,7 s / 35,5 s
// (n=4, contra 127.0.0.1:8103 en el propio VPS, con el modelo ya cargado), y el
// http.Server de la API pública sirve con `WriteTimeout = 10 s`
// (internal/bootstrap/http.go). En Go ese plazo NO INTERRUMPE AL HANDLER: el handler
// termina, el servidor da el 200 y lo registra (`status=200 duration_ms=29687`), pero
// la respuesta ya no cabe por el cable y el cliente ve `curl: (52) Empty reply from
// server`. Un 400 de la MISMA ruta —rápido— salía en 0,22 s sin problema: no era el
// montaje, era el reloj.
//
// ════════════════════════════════════════════════════════════════════════════
// POR QUÉ NO SE SUBE EL `writeTimeout` GLOBAL
// ════════════════════════════════════════════════════════════════════════════
//
// Porque esa constante tiene DOS consecuencias que nadie ve desde aquí, y ninguna de
// las dos la ha pedido nadie:
//
//  1. De ella se DERIVA `pub.SendBudget` (`publicapi.SendBudgetFrom(writeTimeout)`,
//     bootstrap/http.go:97), el techo de la petición de envío de mensajes (Plan 050 ·
//     T5.4, REQ-050.19). Subirla habría movido el presupuesto de envío de paso.
//  2. 🔴 LA COMPARTEN LOS DOS SERVIDORES HTTP del cloud: el público
//     (bootstrap/http.go:122) y el de ADMIN (bootstrap.go:1008, el :8100 con
//     /healthz y /admin/*). Subirla habría relajado también el plazo de escritura de
//     toda la superficie de administración, que no tiene nada que ver con esto.
//
// Lo que se mueve aquí es el plazo de ESA ruta y de ninguna otra. Que no le hace nada
// al presupuesto de envío no es una deducción: está MEDIDO sobre la misma conexión en
// TestPlazoPorRuta_NoMueveElPresupuestoDeEnvio (plazoescritura_test.go).
//
// ⚠️ Lo que este plazo NO es: un timeout de petición. No acota al handler ni cancela
// su contexto; solo dice hasta cuándo puede el servidor escribir en la conexión. Lo
// que acota el trabajo es el plazo de la llamada al modelo (abajo) y, aguas arriba, el
// `dbCtx` de las lecturas. Un handler que se cuelgue más de este plazo vuelve a dejar
// al cliente sin cuerpo — solo que ahora el listón está donde el trabajo cabe.

// margenDeRedacción es lo que se le añade al plazo de la llamada al modelo para fijar
// el plazo de escritura de la ruta. Cubre TODO lo que el handler hace fuera de esa
// llamada y que NO tiene plazo propio: la lectura de la solicitud con sus líneas, la
// del historial aprobado del tenant, la de la semilla de estilo, el armado del
// few-shot, el verificador de precios, el render y la serialización del JSON.
//
// POR QUÉ 12 s. Ese trabajo son milisegundos —lecturas indexadas por tenant y una
// plantilla de texto—, y la medición de campo lo confirma: los 35,5 s del peor caso
// son la inferencia, no la cómoda. 12 s son tres órdenes de magnitud de holgura sobre
// lo que hay que absorber, y llevan el plazo a un número redondo (60 s) que se puede
// decir en voz alta al operar.
//
// 🔴 NO es un p99 de nada, y no se puede leer como una promesa de que la respuesta
// llega en 60 s: es el margen sobre la ÚNICA parte del handler que tiene techo.
const margenDeRedacción = 12 * time.Second

// plazoDeEscrituraDeLaRedacción es el plazo de escritura de
// `POST /api/v1/intakes/{id}/quote-suggestion`. Hoy 60 s.
//
// 🔴 SE DERIVA, NO SE INVENTA — la misma doctrina que `SendBudgetFrom`, y por el mismo
// motivo escrito allí: una cifra copiada a mano en otro fichero es la que nadie rehace
// cuando el reloj del que dependía se mueve. El sumando es
// `pipeline.PlazoPorLlamadaSuelo` (48 s), que es EXACTAMENTE el plazo que el bootstrap
// le pone a la llamada al modelo de esta ruta
// (`quotetext.ConPlazo(pipeline.PlazoPorLlamadaSuelo)`, bootstrap.go). Si ese suelo
// sube —su propio bloque de cabecera dice que 48 s es un SUELO y que el número honesto
// es ≥ 48 s—, este plazo sube con él y no hay aritmética que mantener.
//
// ⚠️ HUECO DECLARADO: el enlace es por el mismo símbolo, no por el mismo valor. Si
// alguien cablea el servicio con `ConPlazo(otraCosa)` en el bootstrap, esta derivación
// deja de cubrirlo y nadie se entera. Cerrarlo pediría o bien que el plazo viajase en
// `Deps` desde quien lo cablea (con el riesgo de cero-valor que ya obligó a un test de
// AST para `SendBudget`), o bien un test de cableado sobre bootstrap.go. Ninguna de las
// dos entra en el alcance de este arreglo.
const plazoDeEscrituraDeLaRedacción = pipeline.PlazoPorLlamadaSuelo + margenDeRedacción

// conPlazoDeRedacción extiende el plazo de escritura de la conexión ANTES de empezar el
// trabajo lento, y solo para la petición que atraviesa este middleware.
//
// POR QUÉ ENVUELVE LA RUTA ENTERA (y no va dentro del handler, ni dentro de
// `protectRead`): el controlador de respuesta llega a la conexión desenvolviendo
// `Unwrap()` envoltorio a envoltorio, y de los envoltorios que hay en medio solo
// `metrics.statusRecorder` lo implementa. `accessLog` mete el suyo
// (`respuestaObservada`, accesslog.go) DENTRO de `protectRead`, así que un middleware
// colgado por debajo recibiría un ResponseWriter que corta la cadena y el plazo no se
// pondría. Aquí arriba la cadena está intacta — y si algún día deja de estarlo, no
// falla en silencio: se registra (ver abajo) y muere
// TestQuoteSuggestion_PlazoDeEscritura_SoloEsaRuta.
//
// El plazo se pone también para el 401, el 403 y el 400 de la ruta, que responden en
// milisegundos y no lo necesitan. Es el precio de ponerlo donde la cadena está intacta,
// y no cuesta nada: un plazo de escritura amplio en una respuesta inmediata no cambia
// nada salvo cuánto tardaría en rendirse un cliente que dejó de leer.
//
// ⚠️ SI ALGÚN DÍA ESTO SE APLICA A UNA RUTA CON CUERPO (las candidatas naturales son
// `approve` y `request-info`, que mandan JSON), hay que saber esto antes: mientras a
// `net/http` le quede cuerpo de petición SIN LEER, no arranca la lectura de fondo con
// la que detecta que el cliente cerró, así que `r.Context()` NO se cancela y una
// llamada abortada parece eterna desde el servidor. Medido aquí, con el mismo handler
// y el cliente abortando a los 200 ms: sin drenar `r.Body`, CERO cancelaciones en
// 1,5 s; drenándolo primero, cancelación detectada en 200 ms. Lo descubrió el frente
// del BFF, y le costó un test que medía cancelaciones y mentía en la dirección
// peligrosa —decir que la petición sigue viva cuando el cliente ya se fue—.
//
// A ESTA ruta no le afecta hoy: `quote-suggestion` es un POST SIN cuerpo (el handler
// no lo lee porque no hay nada que leer, ver quotesuggestion.go).
//
// El fallo NO aborta la petición: sin plazo extendido la ruta se comporta como hoy
// —mal para el caso lento, bien para el rápido—, y cortar la petición cambiaría un
// defecto de entrega por una caída. Se registra a Warn porque es exactamente la avería
// que estuvo invisible: el servidor creyéndose entregado mientras el cliente no recibe
// nada.
func conPlazoDeRedacción(log sharedlogger.Logger, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(plazoDeEscrituraDeLaRedacción)); err != nil {
			if log != nil {
				log.Warn("no se pudo extender el plazo de escritura de la sugerencia de cotización: "+
					"la respuesta larga volverá a no caber por el cable",
					"path", r.URL.Path, "plazo", plazoDeEscrituraDeLaRedacción.String(), "error", err)
			}
		}
		h.ServeHTTP(w, r)
	})
}
