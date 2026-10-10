// Porta internal/flujos/runtime/welcome.go @ e0159171

package runtime

import (
	"context"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// welcome.go es LA BIENVENIDA ÚNICA (Plan 044 · Ola 1.8 · T1.8-2, D6): un saliente FIJO DEL
// SISTEMA —«estamos procesando»— que el Cloud le manda AL CLIENTE al primer mensaje de una
// conversación, y otra vez si el contacto vuelve tras un silencio largo. Jamás en cada turno.
//
// En rojo declara el puerto y su opción; la mecánica no exportada nace en el verde (F8-04b)
// y los tests de la ola siguiente la prueban por HandleIncoming.
//
// # Promesas
//
// Gate (RT-20), fail-closed:
//
//   - WL-1 · La mecánica está activa solo con las TRES: WithWelcomeStore, WithEntitlements y
//     la feature `llm_intake` del tenant. Es la MISMA feature que abre el hilo y la ventana de
//     captación, y es su único interruptor: no hay columna de apagado.
//   - WL-2 · Si falta cualquiera, o el resolver falla (WARN «runtime: no se pudo resolver la
//     feature llm_intake; no se manda bienvenida»), no se manda nada y NO SE ESCRIBE NADA: el
//     gate va antes de TouchContact.
//
// Registro del contacto:
//
//   - WL-3 · En CADA entrante que pasa las guardas de borde, con el candado de la conversación
//     tomado y ANTES de cargar el estado, se llama a TouchContact con el instante del reloj
//     del runtime. También en los turnos que avanzan una conversación viva: el silencio se
//     mide contra el ÚLTIMO mensaje del contacto.
//   - WL-4 · Ese instante se toma UNA vez por turno: es el mismo que se escribe en
//     TouchContact, con el que se mide el silencio y con el que se sella MarkWelcomed.
//   - WL-5 · Si TouchContact falla: WARN «runtime: no se pudo registrar la actividad del
//     contacto; no se manda bienvenida» y el turno sigue sin bienvenida.
//
// Cuándo se manda:
//
//   - WL-6 · Solo en los DOS caminos en que el turno NO avanza una conversación viva: el
//     LIMBO (no hay estado) y el REINICIO (el reloj del evento venció y soltó la
//     conversación). Va ANTES de resolver el disparo: es un acuse de recibo.
//   - WL-7 · Nunca en un avance (no pisa un menú ni un carrito a medias), ni en las sueltas
//     de mitad de conversación (el menú huérfano, el estado terminal), ni en un Start por API.
//   - WL-8 · Toca si NUNCA se saludó (WelcomedAt cero), o si el mensaje ANTERIOR del contacto
//     (LastIncomingAt de la marca previa) queda a WelcomeSilence o más de este. El ancla es
//     el último mensaje del contacto, no la última bienvenida. El borde es inclusivo: a
//     exactamente N, sale. Con WelcomeSilence 0 sale siempre.
//   - WL-9 · El texto es fijo, no lo escribe ningún LLM: TenantSettings.WelcomeText o, si
//     está vacío, store.DefaultWelcomeText. La cadena vacía significa «el texto de
//     plataforma», no «sin bienvenida».
//   - WL-10 · La configuración del tenant se lee solo en esos dos caminos. Si no se puede
//     leer: no se saluda (WARN «runtime: no se pudo leer la config del tenant; no se manda
//     bienvenida»).
//
// Entrega y sello (el runbook de fleet_sessions.greeted_at):
//
//   - WL-11 · Se marca (MarkWelcomed, con la marca previa como testigo) SOLO si el Ack del
//     Edge vuelve con ok = true. Si el envío falla, o el Edge lo rechaza, NO se marca y el
//     siguiente mensaje del contacto reintenta. WARN «runtime: el envío de la bienvenida
//     falló; NO se marca y el próximo mensaje reintenta» / «runtime: el Edge rechazó la
//     bienvenida; NO se marca y el próximo mensaje reintenta».
//   - WL-12 · Entregada y marcada: Info «runtime: bienvenida entregada al contacto».
//     Entregada y la marca falla: ERROR «runtime: la bienvenida se entregó pero no se pudo
//     marcar; el cliente recibirá un duplicado». Entregada y otro turno marcó primero
//     (MarkWelcomed false sin error): WARN «runtime: otro turno marcó la bienvenida primero;
//     este envío fue un duplicado».
//   - WL-13 · Best-effort integral: ningún fallo de la bienvenida devuelve error ni impide
//     que el turno siga hacia el disparo.
//
// Lo que NO hace:
//
//   - WL-14 · 🔴 No entra en el análisis: no se escribe en el hilo del evento (ni marcada
//     como fuera de turno), no se ofrece al agregador y no mueve su ventana.
//   - WL-15 · No suma a la racha de auto-respuestas y no cobra token del limitador (send.go).
//   - WL-16 · Los logs llevan solo ids opacos (tenant, sesión, contacto): ni el número ni el
//     texto.

// WelcomeStore es el puerto del estado de la bienvenida (Plan 044 · T1.8-2): «a esta
// conversación ya la saludé» y «cuándo habló el contacto por última vez». Lo satisfacen
// *store.PostgresRepository y *store.MemoryRepository.
//
// Se declara aquí, y no se importa el tipo concreto, por lo de siempre en este paquete: el
// motor declara lo que necesita, no de quién lo obtiene.
type WelcomeStore interface {
	// TouchContact registra que el contacto acaba de escribir en el instante now y devuelve
	// la marca ANTERIOR a este turno (las dos fechas en cero = nunca habló, nunca se le
	// saludó).
	TouchContact(ctx context.Context, key store.Key, now time.Time) (store.WelcomeMark, error)
	// MarkWelcomed sella la bienvenida como entregada en now, con centinela sobre la marca
	// leída (witness): solo marca si nadie la movió desde entonces. false SIN error = otro
	// turno ganó la carrera.
	MarkWelcomed(ctx context.Context, key store.Key, witness store.WelcomeMark, now time.Time) (bool, error)
}

// WithWelcomeStore cablea la bienvenida única. Sin ella (nil) el motor NO manda ninguna
// bienvenida y NO escribe una sola fila de su estado: idéntico a antes de que existiera
// (RT-12).
//
// Va SIEMPRE con WithEntitlements: el gate por tenant es fail-closed, así que con el resolver
// a nil queda inerte aunque se cablee (WL-1).
func WithWelcomeStore(w WelcomeStore) Option {
	panic(pendiente.Implementar("runtime.WithWelcomeStore"))
}
