// Porta internal/flujos/events/thread_reader.go @ 9d5a4b6
//
// thread_reader.go — LA LECTURA del hilo del evento (Plan 044 · Ola 1 · T1.4;
// REQ-10b, REQ-10c, D-044.3b, D-044.24, D-044.26).
//
// # POR QUÉ ESTO VIVE AQUÍ Y NO EN QUIEN LO CONSUME
//
// El hilo se escribe en este paquete y se CIFRA en este paquete (AppendMessage /
// AppendOutOfTurnMessage, nivel 2 del ADR-0034). Descifrarlo en otro sitio
// obligaría a repartir el FieldCipher por el árbol y a que un segundo paquete
// supiera qué columna es el sobre. Aquí el borde de descifrado es UNO y es el
// mismo que el de cifrado, que es literalmente lo que pide REQ-10c: «descifrarlo
// en el borde de la app y no dejarlo en claro en ningún otro sitio».
//
// # 🔴 ESTA PUERTA DEVUELVE TEXTO EN CLARO. LO QUE SE PUEDE HACER CON ÉL, Y LO QUE NO
//
// El literal que sale de aquí vive SOLO en memoria, entre esta lectura y el sobre
// que lo vuelve a cifrar (`intake_jobs.source_text_*`). NO puede ir a `flow_events`,
// NI a logs, NI a telemetría, NI a un campo nuevo (REQ-10c). Los errores de este
// fichero NUNCA citan el cuerpo: nombran el evento y el `seq`, que son
// identificadores.
//
// # EL GRADO SE RESUELVE AQUÍ; LA CLASE, NO
//
// Una entrada del historial tiene DOS niveles posibles (ADR-0034 §Decisión 1) y el
// lector no debería tener que saber cuál le tocó: el nivel 2 trae el cuerpo cifrado
// y el nivel 1 trae estructura en claro en `payload`. `ListThread` resuelve esa
// diferencia —descifra el uno, renderiza el otro— y entrega UN campo `Text`.
//
// Lo que NO decide este fichero es la CLASE de la entrada: si es hilo literal del
// cliente o si es contexto. Esa clasificación es de quien lee (REQ-10b: «el
// agregador clasifica cada fila por entry_kind») y por eso `Kind` viaja al lado del
// texto, exportado a propósito.
//
// D-17 (deuda que se porta tal cual): las dos lecturas cierran sus filas con el ritual
// `defer rows.Close()` que solo informa del fallo del cierre si no había ya otro error. D-1 y el
// homónimo DEK (la `body_dek` es la del envelope de PII de negocio, NO la DEK del ADR-0007): ver
// store.go.

package events

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// EntryKind es el vocabulario de `conversation_event_messages.entry_kind` VISTO
// POR QUIEN LEE.
//
// 🔑 Es un ALIAS del tipo interno (`entryKind`), no un tipo nuevo, y eso es el
// punto entero: hay UNA sola lista de valores en el árbol. La razón por la que el
// tipo interno no se exporta sigue siendo válida para ESCRIBIR —el grado no lo
// elige el llamante, lo clava cada método del store, y así un resumen no puede
// entrar disfrazado de mensaje del cliente (INV-11)—, pero LEER sin ver el
// `entry_kind` es exactamente el fallo que REQ-10b existe para impedir. Un alias
// da lo segundo sin abrir lo primero: no hay ningún `Append*` que acepte un
// EntryKind como parámetro.
type EntryKind = entryKind

// Los cuatro valores del vocabulario, para que quien lea pueda clasificar. Son las
// MISMAS constantes de events.go (mismo tipo, mismo valor), reexportadas: si
// alguien añade un quinto grado, aquí no hay nada que sincronizar salvo esta lista.
const (
	// KindMessage es el TEXTO LITERAL en turno: lo que el cliente escribió y lo que
	// el flujo le contestó. Es el ÚNICO grado que el Plan 044 trata como hilo
	// literal, y el único del que puede salir una `evidence`.
	KindMessage EntryKind = entryKindMessage
	// KindSummary es el resumen determinista que emitimos al cambiar de evento
	// (ADR-0029 E-4). CONTEXTO, nunca pedido (REQ-10b, D-044.3b).
	KindSummary EntryKind = entryKindSummary
	// KindDecision es la decisión estructurada del cliente (nivel 1, en claro). No
	// es prosa y no entra al `source_text` — ver la clasificación del compositor.
	KindDecision EntryKind = entryKindDecision
	// KindMessageOutOfTurn es el saliente que emitimos SIN turno entrante — el
	// automensaje de rescate, las coletillas. CONTEXTO, nunca pedido (D-044.24).
	KindMessageOutOfTurn EntryKind = entryKindMessageOutOfTurn
)

// ThreadEntry es UNA entrada del hilo, ya resuelta a texto plano.
//
// 🔴 `Text` NO SE PUEDE USAR SIN MIRAR ANTES `Kind`. El mismo campo trae lo que
// escribió el cliente, lo que resumió el sistema y lo que dijo el negocio por su
// cuenta; meterlos en el mismo saco es literalmente el fallo que REQ-10b describe
// —y con el saliente fuera de turno es peor, porque el rescate LISTA PRODUCTOS y
// un LLM los extraería como pedido del cliente (D-044.24)—.
type ThreadEntry struct {
	// Seq es el número de la entrada dentro del evento (sin huecos, ascendente). Es
	// un identificador: puede aparecer en logs y en errores.
	Seq int
	// Role es DE QUIÉN es la voz (client | business | system). No es la clase: un
	// `summary` y un `message` del negocio pueden compartir rol y son cosas
	// distintas. Quien clasifica es Kind.
	Role Role
	// Kind es el GRADO/marca de la entrada. Es lo que hay que mirar ANTES de tocar
	// Text.
	Kind EntryKind
	// Text es el contenido EN CLARO, resuelto según el grado: el cuerpo descifrado
	// si la entrada es de nivel 2, o el render del `payload` si es de nivel 1.
	// Vacío cuando la entrada no tiene nada legible que aportar (un `decision`, un
	// payload que no es un resumen, un cuerpo vacío).
	Text string
}

// ListThread devuelve el hilo de un evento en orden cronológico, DESCIFRADO en el
// borde (REQ-10c), con como mucho `limit` entradas (las más recientes).
//
// Sin cipher NO devuelve el hilo a medias: falla con ErrNoCipher. Devolver solo las
// entradas de nivel 1 sería peor que fallar — el llamante compondría un
// `source_text` hecho SOLO de contexto, sin una línea del cliente, que es la forma
// exacta del accidente que D-044.24 describe (el rescate lista productos y nadie
// los contradice).
//
// Una entrada que no se puede descifrar ABORTA la lectura con error. No se salta:
// un hilo al que le falta una frase del cliente en silencio produce un presupuesto
// mal hecho sin que nadie vea un fallo.
//
// Guardas, en este orden y SIN ir a la base: un store nil, un store sin base o un eventID vacío
// devuelven (nil, nil); limit <= 0 devuelve (nil, nil); sin cipher, ErrNoCipher. No acota por
// tenant ni valida que el id sea un UUID: eso es de quien llama.
//
// El recorte muerde por el PRINCIPIO: con más entradas que limit salen las `limit` MÁS RECIENTES,
// en orden cronológico (seq ascendente). No filtra por entry_kind ni por origin: salen los cuatro
// grados, cada uno con su Kind y su Role.
//
// Text, según el nivel de la fila: con cuerpo cifrado (message, message_out_of_turn), el cuerpo
// descifrado con la KEK de ESA fila (body_kek_id); con payload en claro, el Render() del Summary
// que el payload describe —el mismo texto que el cliente leyó—, que es "" para una decisión o
// cualquier estructura que no sea un resumen con líneas o respuestas; un payload ilegible como
// Summary también da "" y NO es un error; sin cuerpo y sin payload, "".
//
// Textos de error (literales, con el error de origen envuelto en %w; ninguno cita el cuerpo):
//   - "events: leer el hilo del evento %s: %w";
//   - "events: scan de una entrada del hilo del evento %s: %w";
//   - "events: resolver la entrada %d del hilo del evento %s: %w" (el seq y el evento);
//   - "events: iterar el hilo del evento %s: %w";
//   - "events: cerrar el hilo del evento %s: %w" si lo único que falla es el cierre (D-17).
func (s *Store) ListThread(ctx context.Context, eventID string, limit int) (out []ThreadEntry, err error) {
	panic(pendiente.Implementar("events.Store.ListThread"))
}

// ListPastedByOwner devuelve, DESCIFRADAS, las transcripciones que el dueño pegó en
// este evento. Es lo que el dedupe de T4.6 compara contra el texto entrante.
//
// Igual que ListThread: sin cipher falla con ErrNoCipher en vez de devolver el hilo
// a medias, y una entrada indescifrable ABORTA la lectura. Aquí la consecuencia de
// tragarse un fallo sería un DUPLICADO —la fila que no se pudo leer no se compara,
// así que el texto se volvería a escribir—, y el criterio dice «repetir la llamada
// con el mismo `text` ⇒ sigue habiendo una».
//
// Solo las filas entry_kind = 'message' con origin = 'owner_pasted' de ese evento, por seq
// ascendente y SIN tope. Un mensaje del cliente por WhatsApp con el mismo texto no sale. Guardas
// sin ir a la base: store nil, store sin base o eventID vacío devuelven (nil, nil); sin cipher,
// ErrNoCipher.
//
// Textos de error (literales, con el error de origen envuelto en %w; ninguno cita el cuerpo):
//   - "events: leer las transcripciones pegadas del evento %s: %w";
//   - "events: scan de una transcripción pegada del evento %s: %w";
//   - "events: descifrar la transcripción %d del evento %s: %w";
//   - "events: iterar las transcripciones pegadas del evento %s: %w";
//   - "events: cerrar las transcripciones pegadas del evento %s: %w" (D-17).
func (s *Store) ListPastedByOwner(ctx context.Context, eventID string) (out []string, err error) {
	panic(pendiente.Implementar("events.Store.ListPastedByOwner"))
}
