// Package runtime define las interfaces de frontera del motor de flujos hacia
// el resto de la plataforma. El motor depende del Gateway SOLO por interfaces
// estrechas (no del struct concreto), para mantener la frontera y testear con
// dobles (design.md §2).
//
// En T0 solo están las interfaces; la orquestación viva (OnIncoming → resolver
// clave → single-flight → cargar/persistir → empujar, y Start por API) llega
// en T4.
package runtime

import (
	"context"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
)

// Sender es la salida del motor hacia el Gateway: enviar texto o un adjunto al
// contacto. Sus firmas encajan exactamente con (*gatewaygrpc.Server).SendText y
// .SendMedia, de modo que el Gateway las implementa sin adaptador.
type Sender interface {
	SendText(ctx context.Context, sessionID, to, text string) (*cloudlinkv1.Ack, error)
	// SendMedia despacha un adjunto (Plan 017 §4.2/§6.1): el binario NO viaja por
	// gRPC, va la URL prefirmada (presignedURL) que el Edge descarga y sube a
	// WhatsApp. kind ("document"|"image") elige la rama DocumentMessage/ImageMessage.
	SendMedia(ctx context.Context, sessionID, to, presignedURL, filename, mime, caption, kind string) (*cloudlinkv1.Ack, error)
}

// Presigner genera la URL prefirmada de DESCARGA (GET) de un objeto del almacén.
// El runtime la consume al despachar un nodo media: presigna la MediaRef.Key y
// pasa la URL al Sender (design.md §4.2/§9.C). Interfaz ESTRECHA (solo el download
// que 017 usa) para testear con dobles y no acoplar el runtime al adapter S3/R2;
// la satisface objectstore.PresignClient (superset con GenerateUploadURL, Plan 018).
type Presigner interface {
	GenerateDownloadURL(ctx context.Context, key string) (url string, expiresAt time.Time, err error)
}

// Vocabulario INTERNO del motor para el eje que gobierna la reacción (Plan 020 ·
// T1). Se declaran como literales en runtime (no se importa fleet) para no acoplar
// el motor al gateway: el resolver entrega el valor ya resuelto como string.
//
// ⚠️ Desde el Plan 046 · T1.1 el dato que la BD aporta ya NO es fleet_sessions.role
// sino fleet_sessions.profile (active|passive): el resolver agrega esa columna y la
// traduce a ESTOS literales. Los nombres y los valores se conservan a propósito —
// son el CONTRATO de TenantResolver y de los dobles de test, y cambiarlos aquí
// obligaría a reescribir aserciones de comportamiento sin cambiar una sola decisión.
// Mueren con el DROP de la columna role, en el plan futuro que lo haga; hasta
// entonces "bot" significa exactamente profile='active'.
const (
	roleBot     = "bot"     // ejecuta el motor de flujos (dispara triggers / auto-responde).
	rolePassive = "passive" // solo escucha/transporta: NO dispara triggers ni auto-responde.
)

// Motivos por los que un entrante NO llega al motor reactivo. Son la etiqueta del
// contador wapp_flow_reactive_blocked_total (cardinalidad FIJA: cuatro valores) y la
// respuesta a «¿por qué no contesta?». Comparten contador porque responden esa MISMA
// pregunta, pero NO son la misma clase de hecho y quien lea la métrica tiene que
// separarlos —la etiqueta sola no lo dice—: los tres primeros son cortes
// DELIBERADOS —dos son configuración (rol, números propios) y uno es contención de la
// conversación—, el entrante NO debía entrar, así que ninguno es un error: se cuentan,
// no se alarman. El cuarto no.
const (
	reasonPassive   = "passive"    // la sesión receptora está marcada passive (T1).
	reasonSelfLoop  = "self_loop"  // el remitente es un número propio del tenant (T2).
	reasonRateLimit = "rate_limit" // la conversación agotó su cupo de auto-respuestas (T0).
	// reasonSaturation es el ÚNICO motivo que no es una decisión sino una PÉRDIDA: el
	// pool de entrantes no dio cupo dentro del incomingTimeout y un mensaje real de un
	// cliente —que SÍ debía entrar al motor— se descartó. Sobre este motivo sí se
	// alarma: es la señal de degradación del servicio, no de política (Plan 027 · Ola 1
	// · T5 dejó el descarte solo en el log; aquí pasa a contarse).
	reasonSaturation = "saturation"
)

// TenantResolver resuelve el tenant_id y el ROL (bot|passive, Plan 020 · T1) de
// la sesión receptora a partir del session_id, porque el hook OnIncoming solo
// entrega session_id (design.md §10.A). Devuelve ambos en UNA llamada (una query
// por entrante). Lo implementa el resolver Postgres (o un doble en tests). Un rol
// vacío o desconocido se trata como bot (no-regresión). Ante error, tenant/rol
// vacíos y el llamante aborta el avance sin tocar el motor reactivo.
type TenantResolver interface {
	ResolveTenant(ctx context.Context, sessionID string) (tenantID string, role string, err error)
}

// SelfNumberLister devuelve el CONJUNTO de números propios (self_pn, E.164
// normalizado) de las sesiones de un tenant (Plan 020 · T2). Lo consume la guarda
// anti-self-loop de HandleIncoming: si el remitente de un entrante casa uno de
// estos números, es una sesión propia del tenant hablando y NO se auto-responde
// (rompe el bucle sesión↔sesión del Plan 019). Se define en el paquete runtime
// (interfaz estrecha) para NO acoplar el motor al paquete fleet: lo implementa un
// resolver Postgres (o un doble en tests). Devuelve la lista tal cual (puede traer
// duplicados entre edges); la guarda compara por igualdad exacta.
type SelfNumberLister interface {
	SelfNumbers(ctx context.Context, tenantID string) ([]string, error)
}

// IngestDeduper deduplica los mensajes ENTRANTES ante la semántica at-least-once
// del outbox durable del Edge (Plan 028 · T6, ADR-0003): tras una reconexión el
// mismo mensaje de WhatsApp puede reenviarse (los MISMOS bytes). Seen registra de
// forma persistente e IDEMPOTENTE la clave (session_id, wa_message_id) del entrante
// y devuelve true si YA se había visto (⇒ el runtime lo ignora ANTES de tocar el
// motor: sin re-procesar efectos ni auto-responder). Se declara en el paquete
// runtime (interfaz estrecha, como SelfNumberLister) para NO acoplar el motor al
// paquete de ingesta: lo implementa *ingest.PostgresDeduper (o un doble en tests).
// La idempotencia previa por last_wa_message_id es CONSECUTIVA (solo la re-entrega
// inmediata); esta cubre además los duplicados INTERCALADOS y los reenvíos que
// disparan/escapan un flujo (caminos que no tocan last_wa_message_id). nil (sin
// WithIngestDeduper) desactiva la dedupe persistente: no-regresión total (queda
// solo la consecutiva).
type IngestDeduper interface {
	Seen(ctx context.Context, sessionID, waMessageID string) (bool, error)
}
