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
package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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

// listThreadSQL lee las ÚLTIMAS `limit` entradas del evento y las devuelve en
// ORDEN CRONOLÓGICO.
//
// Por qué la subconsulta y no un simple `ORDER BY seq LIMIT`: el recorte tiene que
// morder por el PRINCIPIO del hilo, no por el final. Un hilo largo que se recorte
// por la cola perdería justo los mensajes de la ráfaga que acaba de cerrar la
// ventana —lo único que el pipeline necesita sí o sí—, y se quedaría con el saludo
// de hace dos horas. Con `ORDER BY seq DESC LIMIT n` dentro y `ORDER BY seq` fuera,
// lo que se pierde es lo más viejo, que es lo que se puede perder.
//
// No filtra por `entry_kind`: quien clasifica es el llamante (REQ-10b). Filtrar
// aquí sería tomar por él la decisión que la tarea le encarga, y además dejaría
// FUERA sin querer al `message_out_of_turn` —el coste que la constante de ese grado
// ya anunció: «quien escriba el primer lector tiene que decidir a conciencia si
// quiere una clase o las dos»—. Este lector quiere las dos, y las distingue.
const listThreadSQL = `
SELECT seq, role, entry_kind, payload, body_enc, body_dek, body_kek_id
  FROM (
        SELECT seq, role, entry_kind, payload, body_enc, body_dek, body_kek_id
          FROM public.conversation_event_messages
         WHERE event_id = $1::uuid
         ORDER BY seq DESC
         LIMIT $2
       ) t
 ORDER BY seq
`

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
func (s *Store) ListThread(ctx context.Context, eventID string, limit int) (out []ThreadEntry, err error) {
	if s == nil || s.db == nil || eventID == "" {
		return nil, nil
	}
	if limit <= 0 {
		return nil, nil
	}
	if s.cipher == nil {
		return nil, ErrNoCipher
	}

	rows, err := s.db.QueryContext(ctx, listThreadSQL, eventID, limit)
	if err != nil {
		return nil, fmt.Errorf("events: leer el hilo del evento %s: %w", eventID, err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			out, err = nil, fmt.Errorf("events: cerrar el hilo del evento %s: %w", eventID, cerr)
		}
	}()

	for rows.Next() {
		var (
			e                 ThreadEntry
			payload, enc, dek []byte
			kekID             sql.NullString
		)
		if serr := rows.Scan(&e.Seq, &e.Role, &e.Kind, &payload, &enc, &dek, &kekID); serr != nil {
			return nil, fmt.Errorf("events: scan de una entrada del hilo del evento %s: %w", eventID, serr)
		}
		text, terr := s.entryText(payload, enc, dek, kekID)
		if terr != nil {
			// El error NO lleva el cuerpo, ni siquiera un trozo: nombra evento y seq.
			return nil, fmt.Errorf("events: resolver la entrada %d del hilo del evento %s: %w", e.Seq, eventID, terr)
		}
		e.Text = text
		out = append(out, e)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, fmt.Errorf("events: iterar el hilo del evento %s: %w", eventID, rerr)
	}
	return out, nil
}

// listPastedSQL lee SOLO las transcripciones que pegó el dueño en este evento
// (Plan 044 · Ola 4 · T4.6). Es la única consulta del árbol que filtra por
// `origin`, y ese filtro es todo su motivo de existir: el dedupe del `text` de
// `/reanalyze` es por `(event_id, origin, hash del texto saneado)` y las filas de
// `whatsapp` no compiten en él — un cliente que escribiera por WhatsApp la misma
// frase que el dueño pega NO debe impedir que la transcripción se guarde, porque
// son dos hechos distintos.
//
// 🔴 EL HASH NO ESTÁ EN LA SENTENCIA, Y NO PUEDE ESTARLO. No hay columna donde
// guardarlo: el CHECK de grado (`conversation_event_messages_grade_chk`) obliga a
// `payload IS NULL` en toda fila `message`, así que la única sede posible sería una
// columna nueva. Se descartó: el cuerpo va CIFRADO con DEK fresca por fila (nonce
// distinto cada vez), de modo que dos filas con el mismo texto tienen `body_enc`
// distintos y no hay forma de compararlas en SQL. El dedupe se resuelve en memoria,
// descifrando las pocas filas que esta consulta devuelve.
//
// No lleva LIMIT: las filas `owner_pasted` de un evento son las veces que el dueño
// pegó texto en ese pedido —unidades, no cientos—, y un LIMIT que mordiera dejaría
// pasar un duplicado en silencio, que es justo lo que esto existe para impedir.
const listPastedSQL = `
SELECT seq, body_enc, body_dek, body_kek_id
  FROM public.conversation_event_messages
 WHERE event_id = $1::uuid
   AND entry_kind = 'message'
   AND origin = $2
 ORDER BY seq
`

// ListPastedByOwner devuelve, DESCIFRADAS, las transcripciones que el dueño pegó en
// este evento. Es lo que el dedupe de T4.6 compara contra el texto entrante.
//
// Igual que ListThread: sin cipher falla con ErrNoCipher en vez de devolver el hilo
// a medias, y una entrada indescifrable ABORTA la lectura. Aquí la consecuencia de
// tragarse un fallo sería un DUPLICADO —la fila que no se pudo leer no se compara,
// así que el texto se volvería a escribir—, y el criterio dice «repetir la llamada
// con el mismo `text` ⇒ sigue habiendo una».
func (s *Store) ListPastedByOwner(ctx context.Context, eventID string) (out []string, err error) {
	if s == nil || s.db == nil || eventID == "" {
		return nil, nil
	}
	if s.cipher == nil {
		return nil, ErrNoCipher
	}

	rows, err := s.db.QueryContext(ctx, listPastedSQL, eventID, string(OriginOwnerPasted))
	if err != nil {
		return nil, fmt.Errorf("events: leer las transcripciones pegadas del evento %s: %w", eventID, err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			out, err = nil, fmt.Errorf("events: cerrar las transcripciones pegadas del evento %s: %w", eventID, cerr)
		}
	}()

	for rows.Next() {
		var (
			seq      int
			enc, dek []byte
			kekID    sql.NullString
		)
		if serr := rows.Scan(&seq, &enc, &dek, &kekID); serr != nil {
			return nil, fmt.Errorf("events: scan de una transcripción pegada del evento %s: %w", eventID, serr)
		}
		// Se pasa `payload` en nil a propósito: estas filas son nivel 2 por el CHECK
		// de grado, así que el brazo del payload de entryText no puede ejecutarse. Se
		// reusa la MISMA función que ListThread para que el descifrado tenga un solo
		// borde, que es lo que pide REQ-10c.
		text, terr := s.entryText(nil, enc, dek, kekID)
		if terr != nil {
			// El error NO lleva el cuerpo: nombra evento y seq.
			return nil, fmt.Errorf("events: descifrar la transcripción %d del evento %s: %w", seq, eventID, terr)
		}
		out = append(out, text)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, fmt.Errorf("events: iterar las transcripciones pegadas del evento %s: %w", eventID, rerr)
	}
	return out, nil
}

// entryText resuelve el GRADO de una entrada a texto plano. Es UNA función para
// los cuatro `entry_kind` a propósito: la diferencia que atiende es de NIVEL
// (ADR-0034), que es una propiedad de la FILA —o trae cuerpo cifrado, o trae
// payload, nunca las dos, y eso lo impone
// `conversation_event_messages_grade_chk`—, no de la clase que el llamante decide.
//
// 🔑 Que el resumen se renderice con Summary.Render() —el MISMO texto que el
// cliente leyó al reanudar— y no con el JSON en bruto no es cosmética: hace que el
// contexto que ve el LLM esté escrito en el mismo idioma que el resto del hilo. Un
// `{"kind":"cart","lines":[…]}` metido en un prompt es una invitación a que el
// modelo lo trate como datos ya validados.
func (s *Store) entryText(payload, enc, dek []byte, kekID sql.NullString) (string, error) {
	if len(enc) > 0 {
		// NIVEL 2: cuerpo cifrado. Se descifra con la KEK que envolvió ESTA fila
		// (body_kek_id), no con la current: tras una rotación parcial coexisten filas
		// de varias KEK (Plan 012).
		return s.cipher.Decrypt(enc, dek, kekID.String)
	}
	if len(payload) == 0 {
		return "", nil
	}
	// NIVEL 1: estructura EN CLARO. Solo el resumen tiene render; lo que no lo sea
	// —una `decision`— devuelve cadena vacía por el default de Render, y el
	// compositor lo descarta. Un payload ilegible NO es un error de lectura del
	// hilo: es una entrada que no aporta texto, y tratarla como fallo dejaría sin
	// presupuesto a un cliente por una fila vieja mal formada.
	var sum Summary
	if err := json.Unmarshal(payload, &sum); err != nil {
		//nolint:nilerr // degradación intencional: una entrada de nivel 1 que no sea
		// un resumen legible NO aporta texto, y eso no es un fallo de LECTURA del
		// hilo. Propagar el error dejaría a un cliente sin presupuesto por una fila
		// vieja mal formada. El error tampoco se loguea: su mensaje cita el fragmento
		// que no supo leer, y ese fragmento es contenido (mismo criterio que
		// intakes/buyerdata.go con json.Marshal).
		return "", nil
	}
	return sum.Render(), nil
}
