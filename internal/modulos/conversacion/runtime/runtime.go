// Porta internal/flujos/runtime/runtime.go @ e0159171

// Package runtime es la orquestación viva del motor de flujos: recibe cada entrante del Gateway,
// resuelve a qué tenant y a qué conversación pertenece, decide si el motor reactivo debe verlo,
// carga y persiste el estado, empuja la respuesta y reparte los efectos a sus sinks.
//
// Este fichero declara las FRONTERAS del motor hacia el resto de la plataforma. El motor depende
// del Gateway, del almacén de objetos, de la flota y de la ingesta SOLO por interfaces estrechas
// (nunca por el struct concreto), para mantener la frontera y poder probarlo con dobles: los de
// runtimehelpertest.
//
// Declara además el vocabulario interno de los dos ejes que deciden si un entrante llega al motor
// reactivo: el PERFIL de la sesión receptora y el MOTIVO por el que un entrante se queda fuera.
package runtime

import (
	"context"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
)

// Sender es la salida del motor hacia el Gateway: enviar texto o un adjunto al contacto.
//
// Sus firmas encajan EXACTAMENTE con los métodos SendText y SendMedia del servidor gRPC del
// Gateway, de modo que el Gateway lo implementa sin adaptador (trampa T-13 de la fase: si esas
// firmas cambian, lo que se rompe es este encaje).
//
// Las dos devuelven el Ack del Edge o un error; con error, el Ack es nil y el mensaje no se dio
// por despachado. Ninguna de las dos reintenta: el reintento, si lo hay, es de quien llama.
type Sender interface {
	// SendText despacha text al contacto to por la sesión sessionID.
	SendText(ctx context.Context, sessionID, to, text string) (*cloudlinkv1.Ack, error)
	// SendMedia despacha un adjunto (Plan 017 §4.2/§6.1): el binario NO viaja por gRPC, va la URL
	// prefirmada (presignedURL) que el Edge descarga y sube a WhatsApp. filename, mime y caption
	// acompañan al adjunto; kind ("document"|"image") elige la rama DocumentMessage/ImageMessage.
	SendMedia(ctx context.Context, sessionID, to, presignedURL, filename, mime, caption, kind string) (*cloudlinkv1.Ack, error)
}

// Presigner genera la URL prefirmada de DESCARGA (GET) de un objeto del almacén.
//
// El runtime la consume al despachar un nodo media: prefirma la clave del adjunto y pasa la URL
// al Sender. Es una interfaz ESTRECHA —solo la descarga, que es lo único que el motor usa— para
// probar con dobles y no acoplar el runtime al adaptador S3/R2; la satisface el cliente de
// prefirma del almacén de objetos, que además sabe prefirmar subidas (Plan 018).
//
// Devuelve la URL y el instante en que caduca; con error, la URL es vacía y el instante, cero.
type Presigner interface {
	GenerateDownloadURL(ctx context.Context, key string) (url string, expiresAt time.Time, err error)
}

// Vocabulario INTERNO del motor para el eje que gobierna la reacción (Plan 046 · T1.1). Se
// declaran como literales del runtime (no se importa la flota) para no acoplar el motor al
// Gateway: el TenantResolver entrega el valor ya resuelto como string.
//
// 📌 Hasta la migración 0064 estos literales eran "bot" y "passive", el vocabulario de la columna
// legada fleet_sessions.role. Al retirarse esa columna —no la consumía nadie— el motor pasó a
// hablar el ÚNICO eje que existe: el perfil. El corte es byte a byte el mismo; lo que cambió es
// cómo se llama.
const (
	// profileActive: la sesión ejecuta el motor de flujos (dispara triggers y auto-responde).
	profileActive = "active"
	// profilePassive: la sesión solo emite; NO dispara triggers ni auto-responde.
	profilePassive = "passive"
)

// Motivos por los que un entrante NO llega al motor reactivo. Son la etiqueta `reason` del
// contador wapp_flow_reactive_blocked_total —cardinalidad FIJA: estos cuatro valores y ninguno
// más— y la respuesta a «¿por qué no contesta?».
//
// Comparten contador porque responden esa MISMA pregunta, pero NO son la misma clase de hecho, y
// quien lea la métrica tiene que separarlos (la etiqueta sola no lo dice): los tres primeros son
// cortes DELIBERADOS —dos son configuración (perfil, números propios) y uno es contención de la
// conversación—; el entrante NO debía entrar, así que ninguno es un error: se cuentan, no se
// alarman. El cuarto no.
const (
	// reasonPassive: la sesión receptora tiene perfil pasivo (Plan 020 · T1).
	reasonPassive = "passive"
	// reasonSelfLoop: el remitente es un número propio del tenant (Plan 020 · T2).
	reasonSelfLoop = "self_loop"
	// reasonRateLimit: la conversación agotó su cupo de auto-respuestas (Plan 020 · T0).
	reasonRateLimit = "rate_limit"
	// reasonSaturation es el ÚNICO motivo que no es una decisión sino una PÉRDIDA: el pool de
	// entrantes no dio cupo dentro de su plazo y un mensaje real de un cliente —que SÍ debía
	// entrar al motor— se descartó. Sobre este motivo sí se alarma: es la señal de degradación
	// del servicio, no de política (Plan 027 · Ola 1 · T5 dejó el descarte solo en el log; desde
	// entonces además se cuenta).
	reasonSaturation = "saturation"
)

// TenantResolver resuelve el tenant_id y el PERFIL (active|passive) de la sesión receptora a
// partir del session_id, porque el entrante que entrega el Gateway solo trae session_id.
//
// Devuelve los dos en UNA llamada (una consulta por entrante). Lo implementa
// *PostgresTenantResolver, o un doble en tests.
//
// Lo que el motor hace con la respuesta:
//
//   - un perfil VACÍO o DESCONOCIDO se trata como activo (no-regresión): solo el literal
//     "passive" corta la reacción;
//   - ante un error, tenantID y profile vienen vacíos y el llamante aborta el avance sin tocar el
//     motor reactivo.
//
// Hasta la 0064 el segundo valor se llamaba rol y hablaba bot|passive (Plan 020 · T1).
type TenantResolver interface {
	ResolveTenant(ctx context.Context, sessionID string) (tenantID string, profile string, err error)
}

// SelfNumberChecker responde si UN número dado es propio de alguna sesión del tenant (Plan 020 ·
// T2). Lo consume la guarda anti-self-loop del entrante: si el remitente es un número propio, es
// una sesión del tenant hablando y NO se auto-responde (rompe el bucle sesión↔sesión del Plan
// 019). Se declara aquí, como interfaz estrecha, para NO acoplar el motor a la flota: lo
// implementa *PostgresSelfNumbers, o un doble en tests.
//
// 🔒 Hasta el Plan 046 · T4.1 esto era un listador: devolvía la lista entera de números del
// tenant EN CLARO y el motor la recorría comparando strings. Es un PREDICADO —no una lista— por
// dos razones que van juntas: (1) el número ya no se guarda en claro, se guarda cifrado más su
// índice ciego, así que «listar» obligaría a descifrar N teléfonos por entrante para tirarlos
// todos menos uno; y (2) —el motivo de fondo del Plan 046— así la agenda de teléfonos del tenant
// deja de viajar a la RAM del proceso: lo único que la cruza es un booleano.
//
// El número entra YA NORMALIZADO (la forma E.164 canónica del dominio de contactos: solo dígitos,
// sin «+», espacios, guiones ni paréntesis): la implementación calcula sobre él el índice ciego y
// NO vuelve a normalizar. La simetría entre quien escribe el índice y quien lo consulta es todo
// el contrato.
//
// Un error NO es un «no»: el llamante lo distingue de un false legítimo y decide él qué hacer con
// la guarda.
type SelfNumberChecker interface {
	IsSelfNumber(ctx context.Context, tenantID, normalizedNumber string) (bool, error)
}

// IngestDeduper deduplica los mensajes ENTRANTES ante la semántica at-least-once del outbox
// durable del Edge (Plan 028 · T6, ADR-0003): tras una reconexión el mismo mensaje de WhatsApp
// puede reenviarse (los MISMOS bytes).
//
// Seen registra de forma persistente e IDEMPOTENTE la clave (session_id, wa_message_id) del
// entrante y devuelve true si YA se había visto; en ese caso el runtime lo ignora ANTES de tocar
// el motor: sin re-procesar efectos ni auto-responder.
//
// Se declara aquí, como interfaz estrecha (igual que SelfNumberChecker), para NO acoplar el motor
// a la ingesta: lo implementa el deduplicador Postgres de la ingesta, o un doble en tests.
//
// La idempotencia previa por last_wa_message_id es CONSECUTIVA (solo la re-entrega inmediata);
// esta cubre además los duplicados INTERCALADOS y los reenvíos que disparan o escapan un flujo
// (caminos que no tocan last_wa_message_id). Un runtime construido sin deduplicador (nil) tiene
// desactivada la dedupe persistente: no-regresión total, queda solo la consecutiva.
type IngestDeduper interface {
	Seen(ctx context.Context, sessionID, waMessageID string) (bool, error)
}
