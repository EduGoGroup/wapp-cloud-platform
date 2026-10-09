// Porta internal/intake/pipeline/backoff.go @ 56097aa

package pipeline

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/stages"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
)

// ════════════════════════════════════════════════════════════════════════════
// LA POLÍTICA DE REINTENTOS DEL **JOB** (Plan 044 · Ola 2 · T2.5)
// ════════════════════════════════════════════════════════════════════════════
//
// La SEDE la dejó la migración 0078 (`attempts`, `next_attempt_at`, el índice y el
// `WHERE next_attempt_at <= now()` del reclamo). Esto es LO OTRO: cuánto se empuja la
// marca, con qué curva y cuántos intentos antes de `failed`.
//
// 🔴 EL BACKOFF SE IMPLEMENTA EMPUJANDO LA MARCA, NO DURMIENDO EL WORKER. Aquí no hay un
// solo `time.Sleep` en el camino del fallo: un sleep retiene la goroutine, no sobrevive
// al reinicio, no lo ve nadie desde fuera y castiga a los jobs que SÍ podían correr. Lo
// único que duerme en este paquete es el ticker del sondeo, que es otra cosa: es la
// cadencia de «¿hay algo?», no el castigo de nadie.
//
// # SON DOS POLÍTICAS Y NO UNA, Y NINGUNA SUSTITUYE A LA OTRA
//
// P3 ya trae la SUYA, que es del ÍTEM: una llamada más un reintento y, si persiste, el
// ítem queda aislado con marca y los demás siguen. Ésta es la DEL JOB, y solo llega
// cuando la etapa entera devuelve error. Confundirlas tiene consecuencia medible:
// reintentar el JOB por un ítem envenenado tiraría las 22–32 s que costó cada uno de los
// otros N−1.
//
// # LA POLÍTICA, QUE ES LO QUE ESTE FICHERO PROMETE (vista desde el worker)
//
// El error de una etapa —o el de abrir el sobre— se clasifica en UNA causa, en este
// orden (la calidad va PRIMERO, porque los adaptadores envuelven `llm.ErrLLMQuality`
// dentro de errores más gordos y una rama más ancha delante se lo tragaría):
//
//  1. `errors.Is(err, llm.ErrLLMQuality)`, a cualquier profundidad ⇒ CauseQuality.
//  2. `errors.Is(err, stages.ErrNoLiteral)` o `postgres.IsPermanentFailure(err)` (la
//     clase SQLSTATE 23 ENTERA: 23505, 23502, 23503, 23514) ⇒ CauseInvalidJob.
//  3. Todo lo demás ⇒ CauseInfra. Incluye la clase 40 de Postgres (deadlock 40P01,
//     serialización 40001): el conflicto es de concurrencia, no de dato, y reejecutar
//     converge.
//
// Y de la causa sale el desenlace del job:
//
//   - CauseInvalidJob ⇒ `failed` EN EL ACTO: ni un reintento, `attempts` no se toca. La
//     factura que lo motivó está medida: el job `6c5aac22` chocó contra
//     `intakes_event_id_uidx` y se reintentó 10 veces durante 29 minutos para morir igual
//     (D-044.46).
//   - CauseQuality y CauseInfra ⇒ el intento que acaba de fallar es `Attempts + 1`
//     (1-based). Si alcanza el techo de su causa (Config.MaxQualityAttempts o
//     Config.MaxInfraAttempts) el job va a `failed`; si no, vuelve a `pending` con el
//     intento COBRADO y `next_attempt_at = ahora + castigo`.
//   - El castigo del intento N es exponencial base 2 desde Config.BackoffBase
//     (`base × 2^(N−1)`), topado en Config.BackoffCap, con un jitter multiplicativo en
//     [0,8; 1,2). El exponente se acota a 12 para que el desplazamiento no desborde con
//     un techo de intentos alto. El jitter NO ES ADORNO: sin él, N jobs castigados por la
//     MISMA caída de Edge vuelven exactamente a la vez contra una plaza que solo atiende
//     a uno. Sale de `crypto/rand` y no del reloj (`time.Now().UnixNano() % N` degenera
//     en dos valores en darwin/arm64).
// ════════════════════════════════════════════════════════════════════════════

// Las CAUSAS de un tropiezo. Son un vocabulario CERRADO y viajan como CAMPO del log
// (`causa=`) y del motivo de muerte del job, nunca embebidas en la frase del mensaje.
//
// 🔴 POR QUÉ CAMPO Y NO FRASE. «El pipeline falló y reintenta» no distingue «el modelo
// escribió mal un JSON» (el Edge está sano, no hay nada que revisar) de «no se pudo
// hablar con el Edge» (hay que ir a mirar la máquina): son dos investigaciones distintas
// y quien lee el log a las tres semanas no puede adivinar cuál. Con el campo, un
// `causa=infra` se filtra y se cuenta.
const (
	// CauseQuality (antes `CausaCalidad`) es «el modelo respondió y su salida no era
	// interpretable» (`llm.ErrLLMQuality`). El proveedor funciona y el cable funciona.
	CauseQuality = "calidad"
	// CauseInfra (antes `CausaInfra`) es todo lo demás que puede volver a intentarse: el
	// Edge caído, el socket cerrado, el plazo agotado, la base que no contesta, la KEK que
	// no desenvuelve. Transitorio por hipótesis.
	CauseInfra = "infra"
	// CauseInvalidJob (antes `CausaJobInvalido`) es el job que NO PUEDE salir bien por
	// muchas veces que se intente, y no se reintenta ni una vez: el que llega sin literal
	// (`stages.ErrNoLiteral`) y el que revienta contra una VIOLACIÓN DE INTEGRIDAD de
	// Postgres (clase SQLSTATE 23). El dato que la provoca no cambia entre intentos.
	CauseInvalidJob = "job_invalido"
)

// causeOf (antes `causaDe`) clasifica el error de una etapa. Es la ÚNICA función que
// decide de qué familia es un fallo, y por eso el resto del worker no repite `errors.Is`
// por su cuenta: dos clasificaciones distintas del mismo error es la forma clásica de que
// una política se aplique a medias.
//
// ⚠️ Un 23505 de NUMERACIÓN de revisiones no llega hasta aquí: `intakes.InsertRevision`
// lo reintenta él mismo releyendo el máximo y solo escala cuando ya no es una carrera.
func causeOf(err error) string {
	if errors.Is(err, llm.ErrLLMQuality) {
		return CauseQuality
	}
	if errors.Is(err, stages.ErrNoLiteral) || postgres.IsPermanentFailure(err) {
		return CauseInvalidJob
	}
	return CauseInfra
}

// Los valores por defecto de la política. Están como constantes exportadas y no
// escondidas dentro de Config para que un operador que lea el log de arranque pueda
// comparar el número que ve con el que dice el código sin abrir un depurador.
const (
	// DefaultCadence (antes `CadenciaPorDefecto`) es cada cuánto pregunta el worker «¿hay
	// algo que hacer?»: 5 s, calco del sondeo del worker de webhooks. No es un plazo de
	// nada: es el retardo MÁXIMO en arrancar un job que ya estaba listo.
	DefaultCadence = 5 * time.Second

	// DefaultBackoffBase (antes `BackoffBasePorDefecto`) es el primer castigo: 30 s. Son
	// los mismos 30 s del backoff de webhooks (D-042.4) y coinciden con algo medido aquí:
	// una llamada de lote ocupa la plaza única 22–32 s. Un castigo más corto que UNA
	// llamada reencolaría el job antes de que el intento anterior hubiera soltado la plaza.
	DefaultBackoffBase = 30 * time.Second

	// DefaultBackoffCap (antes `BackoffTopePorDefecto`) es el techo de la curva: 5 minutos.
	//
	// 🔴 AQUÍ SE SEPARA DEL GEMELO A PROPÓSITO: `webhook_outbox` topa en 1 HORA porque su
	// destinatario es el endpoint de un CRM ajeno. El de aquí es un cliente que escribió
	// por WhatsApp y la métrica reina es «< 5 min»: estirar la espera hasta una hora no
	// compra probabilidad de éxito y sí convierte «tarde» en «mañana».
	//
	// ⚠️ ES UNA ELECCIÓN, no una medición: con esta curva un Edge caído consume los 10
	// intentos en ~33 min en vez de en ~3 h.
	DefaultBackoffCap = 5 * time.Minute

	// DefaultMaxInfraAttempts (antes `MaxIntentosInfraPorDefecto`) es el techo cuando la
	// causa es transitoria: 10.
	DefaultMaxInfraAttempts = 10

	// DefaultMaxQualityAttempts (antes `MaxIntentosCalidadPorDefecto`) es el techo cuando
	// el modelo devolvió basura: 3.
	//
	// ES MÁS BAJO QUE EL DE INFRA A PROPÓSITO: P2 y P4 llaman a temperatura 0, así que
	// repetir el MISMO prompt sobre el MISMO literal tiende a producir la MISMA salida — y
	// volver a fallar cuesta otra llamada de lote de la plaza única. Con 3 quedan dos
	// repeticiones por si la no-determinación del servidor local cambia algo.
	//
	// 🔴 NO CONFUNDIR CON EL REINTENTO DE P3: aquél es del ÍTEM y no llega hasta aquí.
	DefaultMaxQualityAttempts = 3
)

// maxBackoffShift acota el exponente de la curva: 2^12 × 30 s ya excede cualquier tope
// razonable, y el clamp existe para que el desplazamiento no desborde si alguien pone un
// techo de intentos alto.
const maxBackoffShift = 12

// backoffFor (antes `espera`) calcula cuánto se empuja `next_attempt_at` tras el intento
// `attempt` (1-based: el número del intento que ACABA de fallar).
//
// LA FORMA ES UN CALCO DELIBERADO del backoff de webhooks: exponencial base 2 desde
// `base`, topada en `ceiling`, con jitter ±20 % desde `crypto/rand`. Se copia la FORMA y
// no se reutiliza la función porque dos políticas con destinatarios distintos no deben
// compartir perilla: subir el tope del CRM no debe alargar la espera de un cliente de
// WhatsApp.
func backoffFor(attempt int, base, ceiling time.Duration) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	shift := min(attempt-1, maxBackoffShift)
	d := min(base*time.Duration(1<<uint(shift)), ceiling)

	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Sin jitter antes que sin backoff. Que no haya jitter agrupa reintentos; que no
		// haya backoff es la tormenta.
		return d
	}
	jitter := 0.8 + float64(binary.BigEndian.Uint16(b[:])%400)/1000.0
	return time.Duration(float64(d) * jitter)
}
