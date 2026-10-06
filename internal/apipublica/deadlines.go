// Porta internal/publicapi/publicapi.go @ 115a4ba (defaultDiagnosticsTTL, defaultDBTimeout, dbCtx,
// margenDeEscritura, SendBudgetFrom, sendCtx y dbTimedOut504, líneas 287-428).
//
// deadlines.go — LOS PLAZOS DE LA CARA: el de cada lectura a BD, el presupuesto de una petición
// de envío y la traducción del plazo vencido a 504. En la spec FX era `plazos.go` (05 E-11).
//
// En el rojo solo existía SendBudgetFrom: el resto (dbCtx, sendCtx, dbTimedOut504 y sus
// constantes) es NO exportado y nació con el verde (05 E-4, P6), con su test.

package apipublica

import (
	"context"
	"errors"
	"net/http"
	"time"

	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"
)

// defaultDiagnosticsTTL es la retención del bundle de diagnóstico cuando el TTL cableado llega
// en cero (mismo criterio defensivo que los umbrales de salud). Lo consume diagnostics.go (D5).
const defaultDiagnosticsTTL = 30 * time.Minute

// defaultDBTimeout es el SUELO del plazo de las lecturas a BD de estos handlers: el
// valor que se usa cuando el DBTimeout de un área llega en cero o negativo. Es el mismo
// número que config.PublicAPIDBTimeout trae por defecto, y está aquí a propósito:
// los docstrings de los dos prometen que «<=0 cae al default» y el cargador de
// config NO normaliza, así que la promesa la tiene que cumplir el consumidor. Sin
// esto, unas Deps con el campo sin cablear —un test, un arranque futuro que lo
// olvide— darían un context.WithTimeout de 0 y TODA lectura moriría en el acto.
const defaultDBTimeout = 1500 * time.Millisecond

// dbCtx deriva del contexto de la petición el contexto ACOTADO con el que se
// consulta a Postgres (Plan 050 · Ola 3, T3.2/T3.3). El llamante DEBE hacer
// `defer cancel()`. Un timeout <= 0 cae a defaultDBTimeout.
//
// POR QUÉ HACE FALTA UN RELOJ PROPIO y no basta r.Context(): el contexto de un
// handler HTTP no trae plazo. El WriteTimeout del http.Server NO interrumpe al
// handler ni cancela su contexto —solo hace fallar el Write posterior—, de modo que
// una consulta contra una base lenta espera indefinidamente y el cliente se queda
// con la conexión cerrada, sin cuerpo y sin una sola línea de log. El razonamiento
// completo, con el incidente que lo costó, ya está escrito UNA vez y no se copia
// aquí (una copia diverge del original en cuanto uno de los dos cambie):
//
//   - internal/modulos/edge/grpc/send.go (awaitAck) — el incidente del 2026-08-06.
//   - internal/platform/config/config.go (GRPCAckTimeout y PublicAPIDBTimeout) — la
//     invariante contra el WriteTimeout: los relojes del envío son SECUENCIALES, así
//     que contra los 10s cuenta la SUMA. Son TRES, no dos: ese comentario decía dos
//     y la cuenta era falsa hasta que T5.4 la rehizo (ver allí).
//   - SendBudgetFrom, aquí abajo — el techo que hace que esa suma ya no pueda dejar
//     al cliente sin respuesta.
//
// Solo lo usan LECTURAS. Las escrituras y las transacciones quedan fuera a
// propósito: 1,5s está calibrado para una consulta previa al envío, y aplicárselo a
// un import de catálogo o a un ReplaceItems los abortaría a media transacción bajo
// carga — un cambio de comportamiento con riesgo, no una mejora.
func dbCtx(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		timeout = defaultDBTimeout
	}
	return context.WithTimeout(ctx, timeout)
}

// writeMargin (margenDeEscritura en la cara vieja) es lo que SendBudgetFrom reserva del
// WriteTimeout para que el handler pueda serializar y escribir su respuesta después de
// rendirse. Es el único número nuevo del arreglo de REQ-050.19, y lo es a propósito: es el
// parámetro de una fórmula, no una copia de una suma. Un presupuesto escrito como constante
// suelta habría sido la cuarta cifra que alguien tiene que rehacer a mano cada vez que se
// mueva un reloj — exactamente el mecanismo por el que la invariante de config.go se
// desincronizó y acabó documentando un margen que no existía.
//
// POR QUÉ 1s Y NO MÁS. Este valor es, exactamente, la franja de envíos que cambian de
// desenlace: los que tardan entre el presupuesto y el WriteTimeout hoy alcanzan a
// responder y a partir de ahora se cortan. Estrecharla es el objetivo, y el trabajo
// que tiene que caber dentro es minúsculo —serializar ~150 bytes de JSON y escribirlos
// en una conexión ya abierta—: el camino feliz completo de este endpoint, medido de
// extremo a extremo en el e2e de T5.4, tarda 0,63 ms. 1s son tres órdenes de magnitud
// de holgura sobre eso, suficientes para absorber un GC o un scheduler cargado, y la
// mitad de los ~2s que el comentario viejo de GRPCAckTimeout daba por supuestos.
//
// 🔴 Lo que NO se puede hacer es bajarlo a cero: sin margen, el handler se rendiría
// justo cuando el deadline de escritura vence y volveríamos a la respuesta vacía.
const writeMargin = 1 * time.Second

// SendBudgetFrom DERIVA el presupuesto de una petición de envío del WriteTimeout del servidor
// HTTP que la sirve (Plan 050 · Ola 5 · T5.4, cierra REQ-050.19): writeTimeout menos el margen
// de escritura de 1 s (10 s ⇒ 9 s). Un writeTimeout que no deja sitio al margen (≤ 1 s, cero o
// negativo) devuelve 0 = SIN presupuesto: es preferible a un plazo ya vencido al nacer, que
// abortaría TODOS los envíos en el acto. Nunca devuelve un valor negativo.
//
// Lo llama el arranque en el mismo sitio que construye el http.Server, y el resultado viaja en
// MessagesDeps.SendBudget: así el presupuesto y el WriteTimeout no pueden desincronizarse.
//
// Es la respuesta al defecto que documenta config.PublicAPIDBTimeout: los relojes de abajo
// —guarda de tenant, empuje, espera del ack— son SECUENCIALES y su suma (19,5s) se pasa del
// WriteTimeout (10s), de modo que un Edge saturado dejaba al cliente con la conexión cerrada y
// sin cuerpo.
//
// 🔴 SE DERIVA, NO SE INVENTA. Ningún reloj existente se mueve (INV-050.6): lo que se añade es
// QUIÉN ESPERA. El presupuesto es un techo por encima de los tres, y por ser el más corto es el
// que gana. Al derivarlo del WriteTimeout, mover el WriteTimeout lo arrastra y la aritmética no
// puede volver a mentir; los relojes de abajo pueden incluso crecer sin que el cliente se quede
// sin respuesta, porque quien manda es el techo y no la suma.
func SendBudgetFrom(writeTimeout time.Duration) time.Duration {
	if writeTimeout <= writeMargin {
		return 0
	}
	return writeTimeout - writeMargin
}

// sendCtx deriva el contexto ACOTADO de una petición de envío. El llamante DEBE hacer
// `defer cancel()`. Un presupuesto <=0 devuelve el contexto tal cual (sin plazo): no
// hay suelo local a propósito, porque un suelo aquí sería justo la constante suelta
// que SendBudgetFrom existe para no crear — el valor viene derivado de quien conoce el
// WriteTimeout, y ese es el arranque.
//
// ⚠️ Cubre el handler ENTERO —guarda de tenant incluida—, no solo el envío: la suma
// que se pasaba del WriteTimeout incluye la guarda. Que el plazo de dbCtx (1,5s) sea
// mucho más corto no lo hace redundante: dbCtx acota UNA consulta, esto acota la
// petición.
//
// ⚠️ Cancelar este contexto NO cancela el envío ya en vuelo: la goroutine del Send del
// Registry sobrevive y el comando puede salir hacia el Edge DESPUÉS de que el llamante
// haya recibido su error (session.Push, «Enmienda 1, regla 1»; verificado en el e2e de
// T5.4). Eso es lo que hace seguro este plazo —no se pierde ningún mensaje— y lo que
// obliga a que los textos de error de writeSendError digan «no se sabe si salió» en
// vez de invitar a reintentar.
func sendCtx(ctx context.Context, budget time.Duration) (context.Context, context.CancelFunc) {
	if budget <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, budget)
}

// dbTimedOut504 traduce el VENCIMIENTO del plazo de dbCtx: si err es el deadline,
// registra el hecho (Warn) con los campos dados, responde 504 con reason (`motivo` en la
// cara vieja) y devuelve true para que el handler corte. Con cualquier otro error —o con
// ninguno— devuelve false sin escribir ni registrar nada, y el handler sigue con el
// desenlace que ya tenía: esta función NO altera el comportamiento de ningún error
// preexistente. Una cancelación (context.Canceled) NO es un vencimiento.
//
// 504 y no 500 porque el llamante necesita distinguir «no pude» de «no me dio
// tiempo»: lo segundo es transitorio y reintentar sirve. Y a diferencia del 504 del
// Ack (writeSendError / msgStreamClosed), aquí NO hay ambigüedad sobre lo que pasó:
// estas consultas ocurren ANTES de tocar al Edge, así que nada salió hacia WhatsApp
// y reintentar no puede duplicarle un mensaje a nadie. Por eso estos textos SÍ
// pueden decir «reintenta»: es una acción que el llamante puede ejecutar de verdad,
// no un consejo que no le sirve de nada.
//
// Los campos del log (fields, `campos` en la cara vieja) son SIEMPRE identificadores
// opacos (tenant_id, session_id, command_id): CERO PII —ni destino ni texto—, como el
// resto de los logs de esta capa. Un logger nil no es un error: se responde igual, solo
// que mudo.
func dbTimedOut504(w http.ResponseWriter, log sharedlogger.Logger, err error, reason string, fields ...any) bool {
	if !errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if log != nil {
		log.Warn("lectura a BD vencida: se responde 504", fields...)
	}
	writeError(w, http.StatusGatewayTimeout, reason)
	return true
}
