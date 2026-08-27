package pipeline

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/stages"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
)

// ════════════════════════════════════════════════════════════════════════════
// LA POLÍTICA DE REINTENTOS DEL **JOB** (Plan 044 · Ola 2 · T2.5)
// ════════════════════════════════════════════════════════════════════════════
//
// La SEDE la dejó T2.1 (migración 0078: `attempts`, `next_attempt_at`, el índice y
// el `WHERE next_attempt_at <= now()` del claim). Esto es LO OTRO: cuánto se empuja
// la marca, con qué curva y cuántos intentos antes de `failed`.
//
// 🔴 EL BACKOFF SE IMPLEMENTA EMPUJANDO LA MARCA, NO DURMIENDO EL WORKER. Es
// doctrina literal de la 0046:87 y la 0078 la repite. Aquí no hay un solo
// `time.Sleep` en el camino del fallo: un sleep retiene la goroutine, no sobrevive
// al reinicio, no lo ve nadie desde fuera y castiga a los jobs que SÍ podían correr.
// Lo único que duerme en este paquete es el ticker del poll, que es otra cosa: es
// la cadencia de «¿hay algo?», no el castigo de nadie.
//
// # SON DOS POLÍTICAS Y NO UNA, Y NINGUNA SUSTITUYE A LA OTRA
//
// P3 ya trae la SUYA, que es del ÍTEM (T2.3, REQ-03): una llamada más un reintento a
// temperatura 0.3 y, si persiste, el ítem queda aislado con marca y los demás siguen.
// Ésta es la DEL JOB, y solo llega cuando la etapa entera devuelve error. Confundirlas
// tiene consecuencia medible: reintentar el JOB por un ítem envenenado tiraría las
// 22–32 s que costó cada uno de los otros N−1.
// ════════════════════════════════════════════════════════════════════════════

// Las CAUSAS de un tropiezo. Son un vocabulario CERRADO y viajan como CAMPO del log
// (`causa=`), nunca embebidas en la frase del mensaje.
//
// 🔴 POR QUÉ CAMPO Y NO FRASE. Un solo texto para dos estados miente en la mitad de
// los casos, y el log es lo único que queda a las semanas. «El pipeline falló y
// reintenta» no distingue «el modelo escribió mal un JSON» (el Edge está sano, no hay
// nada que revisar) de «no se pudo hablar con el Edge» (hay que ir a mirar la máquina):
// son dos investigaciones distintas y quien lee el log a las tres semanas no puede
// adivinar cuál. Con el campo, un `causa=infra` se filtra y se cuenta.
const (
	// CausaCalidad es «el modelo respondió y su salida no era interpretable»
	// (`llm.ErrLLMQuality`). El proveedor funciona y el cable funciona.
	CausaCalidad = "calidad"
	// CausaInfra es todo lo demás que puede volver a intentarse: el Edge caído, el
	// socket cerrado, el plazo agotado, la base que no contesta, la KEK que no
	// desenvuelve. Transitorio por hipótesis.
	CausaInfra = "infra"
	// CausaJobInvalido es el job que NO PUEDE salir bien por muchas veces que se
	// intente. No se reintenta ni una vez. Hoy son DOS:
	//
	//   - el que llega sin sobre del literal (`stages.ErrSinLiteral`);
	//   - 🆕 el que revienta contra una VIOLACIÓN DE INTEGRIDAD de Postgres (clase
	//     SQLSTATE 23: 23505 del único parcial, 23502 de un NOT NULL, 23503 de una FK,
	//     23514 de un CHECK). El dato que la provoca no cambia entre intentos.
	CausaJobInvalido = "job_invalido"
)

// causaDe clasifica el error de una etapa. Es la ÚNICA función que decide de qué
// familia es un fallo, y por eso el resto del worker no repite `errors.Is` por su
// cuenta: dos clasificaciones distintas del mismo error es la forma clásica de que
// una política se aplique a medias.
//
// El orden importa y es el mismo que usa `llmvia.motivoDe`: la calidad va PRIMERO,
// porque los adaptadores envuelven `llm.ErrLLMQuality` dentro de errores más gordos y
// cualquier rama más ancha se lo tragaría.
//
// 🔧 LA RAMA `job_invalido` (D-044.46, T4.0). Hasta el 2026-08-27 esto era un if/else
// que mandaba a `CausaInfra` TODO lo que no fuera calidad, y la factura se midió: el job
// `6c5aac22` chocó contra `intakes_event_id_uidx` y se reintentó **10 veces durante 29
// minutos** (22:48 → 23:17) para morir igual. Una violación de integridad NO CEDE
// REINTENTANDO —lo dice el propio `IsPermanentFailure` y lo decía ya el comentario del
// id derivado de `draft.go`—, así que gastar en ella el cupo de infra solo alarga la
// espera del cliente. Es la clase 23 ENTERA y no solo el 23505: un NOT NULL o una FK
// rota tampoco se curan repitiendo la misma escritura.
//
// ⚠️ Un 23505 de NUMERACIÓN de revisiones no llega hasta aquí: `intakes.InsertRevision`
// lo reintenta él mismo releyendo el máximo (5 intentos) y solo escala cuando ya no es
// una carrera. Que ESE caso muera sin reintento del job es lo correcto.
func causaDe(err error) string {
	if errors.Is(err, llm.ErrLLMQuality) {
		return CausaCalidad
	}
	if errors.Is(err, stages.ErrSinLiteral) || postgres.IsPermanentFailure(err) {
		return CausaJobInvalido
	}
	return CausaInfra
}

// Defaults de la política. Están como constantes exportadas y no escondidas dentro de
// `conDefaults` para que un operador que lea el log de arranque pueda comparar el
// número que ve con el que dice el código sin abrir un depurador.
const (
	// CadenciaPorDefecto es cada cuánto pregunta el worker «¿hay algo que hacer?».
	// Calco de `integrations.WorkerConfig.PollInterval` (5 s), que lleva meses en
	// campo. No es un plazo de nada: es el retardo MÁXIMO en arrancar un job que ya
	// estaba listo, y 5 s frente a un pipeline de minutos es ruido.
	CadenciaPorDefecto = 5 * time.Second

	// BackoffBasePorDefecto es el primer castigo: 30 s.
	//
	// DE DÓNDE SALE, QUE ES LO QUE HAY QUE PODER AUDITAR: son los mismos 30 s de
	// `integrations.backoffDuration` (D-042.4), y además coinciden con algo medido
	// AQUÍ — una llamada de lote ocupa la plaza única 22–32 s (veredicto §1.4). Un
	// backoff más corto que UNA llamada reencolaría el job antes de que el intento
	// anterior hubiera terminado de soltar la plaza en el caso del timeout, y el
	// reintento competiría con los jobs frescos por el mismo recurso.
	BackoffBasePorDefecto = 30 * time.Second

	// BackoffTopePorDefecto es el techo de la curva: 5 minutos.
	//
	// 🔴 AQUÍ SÍ ME SEPARO DEL GEMELO, Y A PROPÓSITO: `webhook_outbox` topa en 1 HORA
	// porque su destinatario es el endpoint de un CRM ajeno, que puede estar de
	// mantenimiento toda la noche y al que no espera nadie mirando el teléfono. El
	// destinatario de aquí es un cliente que escribió por WhatsApp y la métrica reina
	// del plan es «7 h 28 min → < 5 min». Un job que ya lleva 5 minutos esperando ha
	// roto la promesa; estirar la espera hasta una hora no compra ninguna
	// probabilidad extra de éxito y sí convierte «tarde» en «mañana».
	//
	// ⚠️ ESTE NÚMERO ES UNA ELECCIÓN, no una medición: no hay distribución medida de
	// cuánto dura una caída de Edge. Lo que la elección compra es acotado y decible:
	// con la curva de abajo, un Edge caído consume los 10 intentos en ~33 min en vez
	// de en ~3 h.
	BackoffTopePorDefecto = 5 * time.Minute

	// MaxIntentosInfraPorDefecto es el techo cuando la causa es transitoria: 10.
	// Calco de `integrations.WorkerConfig.MaxAttempts`.
	MaxIntentosInfraPorDefecto = 10

	// MaxIntentosCalidadPorDefecto es el techo cuando el modelo devolvió basura: 3.
	//
	// ES MÁS BAJO QUE EL DE INFRA A PROPÓSITO, Y LA RAZÓN ES EL MECANISMO: P2 y P4
	// llaman a `llm.TemperatureGreedy` (0.0), así que repetir el MISMO prompt sobre el
	// MISMO literal tiende a producir la MISMA salida — y volver a fallar cuesta otra
	// llamada de lote de 22–32 s de la plaza única. Diez intentos por calidad serían
	// diez veces la misma respuesta mala y cinco minutos de plaza robados a los jobs
	// que sí podían salir. Con 3 quedan dos repeticiones por si la no-determinación del
	// servidor local (batching, KV-cache) cambia algo, y después el job muere con su
	// causa escrita y el dueño lo ve en la bandeja.
	//
	// 🔴 NO CONFUNDIR CON EL REINTENTO DE P3: aquél es del ÍTEM, sube la temperatura a
	// 0.3 y no llega hasta aquí (P3 aísla el ítem y devuelve el job sano). Éste solo
	// entra cuando la etapa ENTERA no pudo producir artefacto.
	MaxIntentosCalidadPorDefecto = 3
)

// espera calcula cuánto se empuja `next_attempt_at` tras el intento `intento`
// (1-based: el número del intento que ACABA de fallar, misma convención que
// `integrations.Worker.fail`).
//
// LA FORMA ES UN CALCO DELIBERADO de `integrations.backoffDuration`: exponencial base
// 2 desde `base`, topada en `tope`, con jitter ±20 % desde `crypto/rand`. Se copia la
// FORMA y no se reutiliza la función —es unexported en su paquete y sus constantes son
// las suyas— porque dos políticas con destinatarios distintos no deben compartir
// perilla: subir el tope del CRM no debe alargar la espera de un cliente de WhatsApp.
//
// EL JITTER NO ES ADORNO: sin él, N jobs castigados por la MISMA caída de Edge vuelven
// exactamente a la vez y reproducen la tormenta contra una plaza que solo atiende a
// uno. La fuente es `crypto/rand` y no el reloj por lo que dice el comentario del
// gemelo: `time.Now().UnixNano() % N` degenera en dos valores en darwin/arm64, donde
// la resolución del reloj es de 1 µs.
func espera(intento int, base, tope time.Duration) time.Duration {
	if intento < 1 {
		intento = 1
	}
	// 2^12 × 30 s ya excede cualquier tope razonable; el clamp existe para que el
	// desplazamiento no desborde si alguien pone un techo de intentos alto.
	shift := min(intento-1, 12)
	d := min(base*time.Duration(1<<uint(shift)), tope)

	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Sin jitter antes que sin backoff. Que no haya jitter agrupa reintentos;
		// que no haya backoff es la tormenta.
		return d
	}
	jitter := 0.8 + float64(binary.BigEndian.Uint16(b[:])%400)/1000.0
	return time.Duration(float64(d) * jitter)
}
