// Porta internal/intake/anclaje/anclaje.go @ 8d875ab

// Package anclaje decide A QUÉ LÍNEA del presupuesto pertenece cada adjunto que
// mandó el cliente —una foto, un audio, un documento— y, cuando no hay certeza,
// decide NO DECIDIR: la referencia sube a nivel de SOLICITUD (Plan 044 · Ola 3 ·
// T3.3, REQ-29).
//
// # ES DETERMINISTA A PROPÓSITO: AQUÍ NO ENTRA EL LLM
//
// El pipeline ya gasta cuatro llamadas al modelo (P2→P3→P4 y la zona gris del
// matcher). Repartir cuatro fotos entre tres líneas no necesita una quinta: es una
// regla de orden y de palabras, y una regla se puede probar. Un modelo, además,
// SIEMPRE contesta —inventaría el ancla que esta tarea prohíbe inventar—.
//
// # LAS TRES REGLAS, EN ESTE ORDEN Y NO EN OTRO
//
//  1. 🔴 **El audio va SIEMPRE a nivel de solicitud**, con la etiqueta literal
//     AudioLabel. Nunca a una línea, ni aunque su propio mensaje nombre el
//     producto. No es una heurística: es REQ-29 («los audios del cliente van al
//     borrador como adjuntos SIN procesarse»). Por eso se comprueba ANTES que nada,
//     y por eso las otras dos reglas no llegan a verlo.
//  2. **Mención textual**: si el mensaje que TRAE el adjunto nombra a UNA sola línea
//     —por un token distintivo de su etiqueta—, el adjunto es de esa línea. «Así la
//     quiero, de chocolate» junto a la foto es la señal más fuerte que hay.
//  3. **Proximidad**: si no hay mención, se mira hacia atrás desde el mensaje del
//     adjunto buscando el mensaje de texto más cercano que sostenga la evidencia de
//     UNA sola línea. Ese es «hablar de la torta 1 y mandar dos fotos justo después».
//
// Y la cláusula de cierre, que es la que manda: **cualquier otra cosa es solicitud**.
// Ninguna evidencia cerca, evidencia de DOS líneas en el mismo mensaje, el adjunto
// llegó antes de que se hablara de nada, pasó demasiado tiempo — todo eso es
// «no lo sé», y «no lo sé» se pinta en la cabecera del borrador, no colgando de una
// línea que el dueño va a creerse.
//
// # 🔴 EL INVARIANTE CONTABLE: NI SE PIERDE NI SE DUPLICA
//
// Toda referencia de entrada sale EXACTAMENTE UNA VEZ, anclada a una línea o a nivel
// de solicitud. Es estructural y no una promesa del comentario: Distribute recorre las
// refs UNA vez y cada vuelta termina en UN solo `append` —los tres caminos acaban en
// `continue`—. Quien añada un cuarto destino, o un «y además déjala en la cabecera
// por si acaso», rompe TestDistribute_AccountingInvariant.
//
// # 🔴 LOS DOS RELOJES: AQUÍ NO SE LLAMA A time.Now()
//
// Este paquete no consulta ningún reloj. Los instantes ENTRAN, y tienen que venir
// TODOS del mismo sitio: el reloj del CLIENTE (`ts_unix` del entrante, que es el
// mismo que alimenta `intake_jobs.message_ts` y la base de fechas de P4). Mezclar
// aquí un `now()` del servidor o un `created_at` de Postgres con el `ts_unix` del
// teléfono sería comparar dos relojes: el error saldría a favor o en contra según el
// desfase del día, sería permanente y no daría ni una señal. Con los dos lados del
// mismo origen, la resta significa lo que dice.
//
// Corolario práctico: si el llamante NO tiene instantes (todos en cero), la ventana
// temporal no descarta nada y el reparto se decide solo por ORDEN (`Seq`), que es la
// forma degradada y sana. No hay un modo «adivina la hora».
//
// # QUÉ SE REUSÓ DEL PLAN 017 Y QUÉ NO, Y POR QUÉ
//
// 🔴 **La infraestructura de media del Plan 017 es de SALIDA, no de entrada, y no
// sirve para esto.** El `MediaRef` del Motor de Flujos describe un archivo que
// NOSOTROS mandamos: lleva `Filename`, `Mime` y `Caption` porque el Edge los fija al
// subirlo a WhatsApp, su `Kind` es el par `document|image` que se traduce al enum del
// proto —donde **audio no existe**— y no tiene instante, porque un archivo que se
// envía no tiene «cuándo lo mandó el cliente». Arrastrarlo hasta aquí acoplaría el
// pipeline de captación al dominio del Motor de Flujos para heredar tres campos que
// no se usan y perder los dos que sí. Lo que SÍ se hereda del 017 es su CONVENCIÓN:
// `Ref` es una key opaca del almacén con el prefijo `wapp/media/…`, sin URL ni
// credenciales (ADR-0007/0009).
//
// Lo que se reusa de verdad es el paquete `evidence` de este módulo: la regla de
// «esta frase aparece DE VERDAD en lo que escribió el cliente» ya está escrita,
// medida y con dueño, y aquí se llama —no se reimplementa—.
//
// # SIN LLAMANTE DE PRODUCCIÓN (deuda D-6)
//
// Hoy nadie llama a Distribute: la entrada del hilo no trae todavía ni media refs ni
// el instante de cada mensaje. El paquete se reconstruye igual, con sus reglas
// probadas, y NO se cablea a nada hasta que ese hueco se cierre.
package anclaje

import (
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// Clases de adjunto que este paquete distingue. El vocabulario NO es un CHECK de
// base de datos: es lo que llega del entrante, y por eso se compara en minúsculas y
// con tolerancia (se recortan los blancos de los extremos). Los valores son texto
// observable: se copian literales.
//
// 🔴 Son TRES los nombres que significan «audio», no uno: WhatsApp manda una nota de
// voz como `ptt` (push-to-talk) y un archivo de música como `audio`. Tratar solo
// `audio` dejaría que la nota de voz —que es justo el caso de Ambar, cuatro notas de
// voz en la conversación real— se colara hasta una línea.
//
// Cualquier otra clase —las tres de abajo, una desconocida o la cadena vacía— NO es
// audio y pasa por las reglas de mención y proximidad como una foto.
const (
	KindImage    = "image"
	KindAudio    = "audio"
	KindPTT      = "ptt"   // nota de voz de WhatsApp: ES audio
	KindVoice    = "voice" // alias que usan algunos puentes: ES audio
	KindVideo    = "video"
	KindDocument = "document"
)

// AudioLabel (antes `EtiquetaAudio`) es el texto LITERAL con el que el audio del
// cliente aparece en el borrador (tasks.md T3.3, REQ-29, design §7.5). Es una
// constante y no un literal suelto porque la pinta la bandeja y la comprueba el
// test: dos copias se desincronizan.
//
// ⚠️ `design.md` §7.4 la muestra recortada («🎙️ audio del cliente») dentro de un
// JSON de ejemplo. La forma buena es esta, la de T3.3 y REQ-29, que es la que el
// dueño lee y la que le dice qué tiene que hacer con ella.
const AudioLabel = "🎙️ audio del cliente — escúchalo"

// MediaRef es UN adjunto del cliente, tal como se persiste en
// `intake_revisions.payload` (design §7.4): `{"kind":…,"ref":…,"label":…}`, con
// `label` omitido cuando está vacío.
//
// 🔴 Seq y At NO se serializan, y no es un descuido: son las ENTRADAS de la
// heurística, no parte del contrato del borrador. El día que alguien las publique
// estará metiendo el reloj del cliente en un payload que se guarda en claro.
type MediaRef struct {
	// Ref es el identificador OPACO del objeto (la key `wapp/media/…` del Plan 017, o
	// el `wa_message_id` mientras el entrante no traiga una key propia, que es lo que
	// pasa hoy). Nunca una URL ni una credencial.
	Ref string `json:"ref"`
	// Kind es la clase del adjunto. Ver las constantes Kind*.
	Kind string `json:"kind"`
	// Label es la etiqueta que ve el dueño. Solo la llevan los audios (AudioLabel);
	// una foto anclada a su línea no necesita rótulo porque su contexto ES la línea.
	Label string `json:"label,omitempty"`
	// Seq es el número del turno que trajo el adjunto, en la misma escala que el
	// `Seq` de la entrada del hilo. Es el ORDEN, y es lo único imprescindible: sin
	// instantes el reparto sigue funcionando, sin orden no.
	Seq int `json:"-"`
	// At (antes `En`) es el instante del mensaje del CLIENTE que trajo el adjunto.
	// Cero = «no se sabe», y entonces la ventana temporal no descarta nada.
	At time.Time `json:"-"`
}

// Turn (antes `Turno`) es UN mensaje de la conversación, con su orden y su instante.
// Es la forma mínima de la entrada del hilo más el instante que a ese tipo le falta
// hoy.
//
// El texto que se pasa aquí tiene que ser el del CLIENTE. Un resumen del sistema o
// una coletilla del negocio no son sitio donde buscar la evidencia de una línea
// (REQ-10b, D-044.24): quien construya los turnos filtra por mensaje y por rol
// cliente ANTES de llamar, exactamente igual que hace el compositor del literal.
type Turn struct {
	Seq int
	// Text (antes `Texto`). Vacío, o solo blancos, es un turno SIN texto: el de un
	// adjunto suelto.
	Text string
	// At (antes `En`). Cero = «no se sabe».
	At time.Time
}

// Line (antes `Linea`) es una línea del presupuesto que PUEDE recibir adjuntos.
//
// Es a propósito una vista mínima y no el tipo de línea del borrador: este paquete
// no sabe de sku, ni de precio, ni de match, y no debe saberlo. Recibe un índice y
// las dos cuerdas de las que tira la heurística, y devuelve índices.
type Line struct {
	// Idx es el identificador de la línea para el llamante (su posición en el
	// borrador). Se devuelve tal cual en Distribution.ByLine; este paquete no lo
	// interpreta.
	Idx int
	// Evidence (antes `Evidencia`) es la frase que el cliente escribió y que sostiene
	// la línea (la `evidence` de P3/P4). Es lo que ancla la línea a UN mensaje concreto
	// de la conversación, y por eso es lo que hace posible la proximidad.
	//
	// Vacía ⇒ la línea no participa en la regla de proximidad. Es el caso de la línea
	// de envío, que no sale de ninguna frase del cliente.
	Evidence string
	// Label (antes `Etiqueta`) es el nombre del producto tal como se le va a pintar al
	// dueño («Torta chocolate húmedo + crema choc.»). De aquí salen los tokens
	// distintivos de la mención textual.
	Label string
}

// Distribution (antes `Reparto`) es el resultado: qué adjunto quedó en qué sitio.
type Distribution struct {
	// ByLine (antes `PorLinea`) va indexado por Line.Idx. Nunca es nil, y una línea
	// sin adjuntos NO aparece como clave.
	ByLine map[int][]MediaRef
	// Request (antes `Solicitud`) son los adjuntos de la cabecera: los audios
	// (siempre) y todo aquello de lo que no hubo certeza.
	Request []MediaRef
}

// Options (antes `Opciones`) son los dos topes de la regla de proximidad.
//
// 🔴 LOS DOS NÚMEROS NO ESTÁN MEDIDOS. No hay una muestra de conversaciones reales
// con adjuntos de la que salgan: son el lado CONSERVADOR de una regla cuyo error
// barato es «sube a la cabecera» y cuyo error caro es «cuelga de la línea
// equivocada». Estrecharlos manda más refs a la solicitud (seguro); ensancharlos
// ancla más (arriesgado). El día que haya muestra, se recalibran con ella y no a ojo.
type Options struct {
	// MaxMessagesBack (antes `MaxMensajesAtras`) es cuántos mensajes CON TEXTO se
	// miran hacia atrás, contando el del propio adjunto. Los mensajes sin texto —los
	// otros adjuntos de la misma ráfaga— NO gastan presupuesto: si no, mandar tres
	// fotos seguidas haría que la tercera «olvidara» de qué se estaba hablando.
	// Cero o negativo ⇒ DefaultMaxMessagesBack.
	MaxMessagesBack int
	// Window (antes `Ventana`) es cuánto tiempo hacia atrás se admite. Una foto que
	// llega dos horas después de hablar de la torta no es «justo después». Solo se
	// aplica cuando los DOS instantes son conocidos, y descarta lo que queda a MÁS de
	// Window: exactamente Window sigue dentro.
	// Cero o negativo ⇒ DefaultWindow.
	Window time.Duration
}

// Los valores por defecto (antes `MaxMensajesAtrasPorDefecto` y `VentanaPorDefecto`).
// Ver la advertencia de Options sobre de dónde salen.
const (
	DefaultMaxMessagesBack = 3
	DefaultWindow          = 5 * time.Minute
)

// Distribute (antes `Repartir`) reparte `refs` entre las líneas y la cabecera. Es
// PURA: no lee, no escribe, no consulta reloj, no registra nada y no modifica ninguna
// de sus entradas (los turnos se ordenan por Seq en una COPIA; pueden llegar
// desordenados).
//
// Cada ref recorre las tres reglas en orden y termina en UN solo destino:
//
//  1. Audio (`audio`, `ptt` o `voice`, sin distinguir mayúsculas y con los blancos de
//     los extremos recortados) ⇒ Request, con Label = AudioLabel, pisando el que
//     trajera. Las otras dos reglas no llegan a verlo.
//  2. Mención: se toma el turno cuyo Seq es el de la ref (si hay varios, el primero
//     en el orden de entrada; si no hay ninguno, no hay mención) y se parte su texto
//     en tokens: minúsculas, cortando por todo lo que no sea letra o dígito Unicode,
//     sin los de menos de 4 runas y sin las palabras vacías. Un token es DISTINTIVO
//     de una línea si está en su Label y en el de ninguna otra. Si el texto trae
//     tokens distintivos de EXACTAMENTE una línea ⇒ esa línea. Se compara por token
//     entero, nunca por subcadena, y los acentos cuentan. La mención NO mira ni la
//     ventana ni el presupuesto.
//  3. Proximidad: se camina hacia atrás por los turnos con Seq ≤ el de la ref, del
//     más cercano al más lejano y empezando por el suyo. Un turno a más de
//     Options.Window de la ref (con los dos instantes conocidos) corta la búsqueda,
//     tenga texto o no. Un turno sin texto no aporta ni gasta. Cada turno con texto
//     gasta uno de los Options.MaxMessagesBack; agotados, se corta. El primer turno
//     cuyo texto sostiene —con la regla de `evidence`— la Evidence de EXACTAMENTE
//     una línea ⇒ esa línea; si sostiene la de dos o más, se corta ahí (lo más
//     cercano ya fue ambiguo y lo de más atrás no puede aclararlo); si no sostiene
//     ninguna, se sigue. Una evidencia que cruza dos mensajes no la sostiene ninguno.
//
// Cualquier otra cosa ⇒ Request, con la ref tal cual llegó. Una mención ambigua (dos
// líneas nombradas) no ancla por mención, pero la ref sigue a la regla 3.
//
// Toda ref de entrada sale exactamente una vez. El orden de salida es el de ENTRADA,
// dentro de cada destino. No depende de recorrer ningún mapa, así que dos ejecuciones
// con la misma entrada dan la misma salida byte a byte — que es lo que permite
// afirmar el reparto en un test en vez de contarlo. Sin refs devuelve un reparto
// vacío y usable: ByLine no es nil.
func Distribute(turns []Turn, lines []Line, refs []MediaRef, opts Options) Distribution {
	panic(pendiente.Implementar("anclaje.Distribute"))
}
