// Porta internal/intake/store.go @ 8d875ab

// Package intake es la COLA del pipeline de captación por LLM (Plan 044 · Ola 1):
// la tabla `intake_jobs` (migración 0072) y el puerto estrecho por el que se
// escribe.
//
// Desempeña para el 044 el MISMO papel que `webhook_outbox` (0046) para el 042: es
// la fila que existe para que el trabajo caro lo haga otro, después. Por eso este
// paquete NO sabe nada de LLM, ni de cifrado, ni del hilo del evento — solo abre
// ventanas, les añade referencias y las cierra.
//
// ⚠️ NO CONFUNDIR CON `intakes` (en plural; `internal/modulos/solicitudes/intakes`,
// antes `internal/intakes`), que es el dominio de los BORRADORES/solicitudes que el
// cliente confirma. Aquí no hay borradores: hay una
// cola de trabajo. La FK lógica `intake_jobs.intake_id` es el puente entre los dos,
// y la escribe la Ola 3.
package intake

import (
	"context"
	"time"
)

// Estados de `intake_jobs.status`, vocabulario CERRADO por el CHECK
// `intake_jobs_status_check` de la migración 0072. Se nombran aquí —y no se
// escriben como literales sueltos por el código— porque el día que la máquina gane
// un estado, el sitio donde mirar es este y la constraint de la 0072.
const (
	// StatusAggregating es la VENTANA ABIERTA: el sink le está añadiendo
	// referencias de mensajes. Es el estado inicial (DEFAULT de la columna) y el
	// mayoritario de la tabla. Es también el ÚNICO que entra en el índice único
	// parcial `intake_jobs_ventana_viva_uidx`, y de ahí sale la garantía de que no
	// puede haber dos ventanas vivas para la misma tupla.
	StatusAggregating = "aggregating"
	// StatusPending es la VENTANA CERRADA esperando al worker de la Ola 2. Cerrar
	// una ventana es lo que LIBERA la tupla para que se pueda abrir otra: al salir
	// de 'aggregating' la fila sale del índice parcial.
	StatusPending = "pending"
	// StatusProcessing es el job TOMADO por un worker del pipeline (Ola 2). Es el
	// ÚNICO estado desde el que se avanza de etapa o se termina: todas las
	// transiciones de machine.go llevan `WHERE status = 'processing'`, y de ahí sale
	// —gratis, sin una comprobación aparte— que los terminales sean ABSORBENTES: ni
	// `done` ni `failed` son `processing`, así que ninguna transición los muerde.
	StatusProcessing = "processing"
	// StatusDone es el terminal FELIZ. Absorbente, y VACÍA el sobre del literal al
	// entrar (INV-13).
	StatusDone = "done"
	// StatusFailed es el terminal INFELIZ, con la causa en la columna `error`.
	// Absorbente también —un job envenenado no vuelve a la cola— y vacía el sobre
	// igual que `done`: lo que decide el vaciado es TERMINAR, no terminar bien.
	StatusFailed = "failed"
)

// WindowKey es la CLAVE DE VENTANA (D-044.3): los mensajes seguidos de un contacto
// sobre un mismo evento, por la misma sesión, son UNA sola ventana.
//
// Las cuatro columnas son exactamente las del índice único parcial
// `intake_jobs_ventana_viva_uidx` de la 0072, y ese orden importa: es el que el
// `ON CONFLICT` infiere.
//
// CERO PII: `ContactID` es OPACO (ADR-0017), no un teléfono; `EventID` es el UUID
// de `conversation_events.id` (FK LÓGICA, sin REFERENCES).
type WindowKey struct {
	TenantID  string
	SessionID string
	ContactID string
	EventID   string // UUID de conversation_events.id
}

// Valid dice si la clave puede identificar una ventana. Las cuatro columnas son
// NOT NULL en la 0072, así que una clave incompleta no es «una ventana rara»: es un
// INSERT que revienta. Se comprueba en Go —barato, sin red— antes de escribir.
func (k WindowKey) Valid() bool {
	return k.TenantID != "" && k.SessionID != "" && k.ContactID != "" && k.EventID != ""
}

// Append es UN entrante entrando a su ventana: la clave, el instante del mensaje y
// las referencias que aporta.
//
// 🔴 NO LLEVA TEXTO, y eso es el corazón de D-044.26. El literal del cliente NO
// viaja por el camino del entrante: el sobre `source_text_enc/_dek/_kek_id` nace
// NULL y se llena AL FLUSH (T1.4), leyendo `conversation_event_messages`, que es la
// fuente canónica (D-043.13). Si no se lee, no se puede concatenar, y no se puede
// añadir texto a un blob cifrado con una sentencia SQL.
type Append struct {
	Key WindowKey
	// MessageTS es el instante del mensaje del CLIENTE (ts_unix del entrante), no
	// el reloj del servidor. Solo se usa cuando este Append ABRE la ventana: en el
	// camino de «ya existía» no se toca (ver OpenOrAppend), y así la fila conserva
	// el ts del PRIMER mensaje sin que nadie tenga que leerla.
	MessageTS time.Time
	// Refs son los identificadores OPACOS del protocolo que este entrante aporta:
	// su `wa_message_id` y, si trae media, sus referencias (SIN descargar nada).
	// Van EN CLARO y sin problema: no son contenido.
	Refs []string
}

// SourceText es EL SOBRE DE TRES PIEZAS del literal compuesto al flush (Plan 044 ·
// T1.4; migración 0072, columnas `source_text_enc` / `source_text_dek` /
// `source_text_kek_id`).
//
// 🔴 LAS TRES O NINGUNA, y esa invariante vive AQUÍ, en el código, porque en la
// 0072 las tres columnas son NULLables a propósito —durante `aggregating` están
// legítimamente vacías, que es el estado normal de la tabla— y por tanto Postgres
// no la puede sostener como sí hace la 0071 con su trío NOT NULL. Sin `DEK` no hay
// con qué descifrar; sin `KEKID` la fila queda FUERA de la rotación de KEK del Plan
// 012, porque nadie sabría con cuál desenvolverla.
//
// El paquete sigue sin saber cifrar: aquí llegan bytes YA cifrados. Quien tiene el
// FieldCipher es el compositor del flush, y que este store no lo tenga es lo que
// hace IMPOSIBLE que el camino del entrante escriba literal (D-044.26) — no puede
// producir un sobre aunque quiera.
type SourceText struct {
	Enc   []byte
	DEK   []byte
	KEKID string
}

// Complete dice si el sobre está entero. Un sobre incompleto no se escribe: media
// escritura deja una fila indescifrable, que es peor que una fila vacía.
func (s SourceText) Complete() bool {
	return len(s.Enc) > 0 && len(s.DEK) > 0 && s.KEKID != ""
}

// Empty dice si el sobre está VACÍO ENTERO: ninguna de sus tres piezas. Un slice vacío no nil
// cuenta como ausente, igual que en Complete. Es la otra forma legítima de la fila —las tres
// columnas a NULL, la del hilo sin mensajes—: un sobre que no es ni Complete ni Empty está A
// MEDIAS, y ese es el que no se escribe (ver JobStore.CloseWithSourceText).
func (s SourceText) Empty() bool {
	return len(s.Enc) == 0 && len(s.DEK) == 0 && s.KEKID == ""
}

// OpenJob es una ventana VIVA tal como la ve el barrido de cierre. Es lo mínimo
// para decidir si le tocó la hora, y nada más: ni sobre, ni artefactos, ni error.
type OpenJob struct {
	ID  string
	Key WindowKey
	// LastActivity y CreatedAt son las DOS anclas de la ventana HÍBRIDA (Plan 044 ·
	// T1.8-1, D-044.43). La ventana cierra por lo que llegue ANTES:
	//
	//   silencio : now() - LastActivity >= aggregation_window_seconds  (45 s)
	//   techo    : now() - CreatedAt    >= aggregation_max_seconds     (120 s)
	//
	// 🔴 AQUÍ HUBO UN SOLO CAMPO, `Anchor`, Y ERA `COALESCE(message_ts, created_at)`.
	// Se retiró, no se renombró, y el motivo es que sostenía una regla distinta: con un
	// único ancla en el PRIMER mensaje, una ráfaga tecleada despacio se parte en dos
	// jobs. Dejar el nombre viejo apuntando a otra cosa habría hecho que todo comentario
	// que lo cita —y son varios— mintiera en silencio.
	//
	// 🔴 LAS DOS SON DEL RELOJ DE POSTGRES (`updated_at` y `created_at` de
	// `intake_jobs`, las dos NOT NULL en la 0072), NUNCA `message_ts`. `message_ts` lo
	// pone el Edge con el reloj del cliente, y compararlo contra el `now()` de Go o de
	// Postgres es comparar dos relojes: un desfase de minutos cerraría ventanas antes de
	// tiempo o no las cerraría nunca, sin error. `message_ts` sigue existiendo y sigue
	// siendo el instante del PRIMER mensaje (D-044.9) — pero ya no decide plazos.
	//
	// LastActivity la mueve el `DO UPDATE … updated_at = now()` del UPSERT, o sea CADA
	// mensaje del cliente de la ráfaga. Nada más la mueve: las entradas de contexto
	// (`summary`, salientes rotulados, la bienvenida de T1.8-2) van a OTRA tabla
	// (`conversation_event_messages`) y no hay ningún trigger sobre `intake_jobs`.
	LastActivity time.Time
	CreatedAt    time.Time
}

// JobStore es el puerto de `intake_jobs`. CINCO operaciones y ninguna más, a
// propósito: el tamaño del puerto es lo que impide que el sink en línea con el
// mensaje pueda hacer algo que D-044.26 prohíbe — aquí NO HAY UN SOLO MÉTODO DE
// LECTURA QUE EL SINK PUEDA LLAMAR. ListAggregating existe para el barrido, que
// corre FUERA del camino del entrante.
//
// La cuarta —PutSourceText, T1.4— es también de fuera de línea, y el sink no la
// puede usar aunque la tenga delante: pide un sobre YA cifrado y el sink no tiene
// cipher (ver SourceText). Lo mismo vale para la quinta —CloseWithSourceText,
// D-F7-9/D-F8-13—, que además pide el OpenJob que solo da ListAggregating: sin
// haber leído no hay con qué llamarla.
//
// 🔴 LA CLAVE INCOMPLETA SE RECHAZA EN LAS CUATRO ESCRITURAS (OpenOrAppend,
// CloseWindow, PutSourceText y CloseWithSourceText): con una WindowKey que no es
// Valid devuelven error —cada una con su texto— y no escriben nada. Vale para TODA
// implementación, el gemelo en memoria incluido (hallazgo 7 de F7); lo afirma
// intakehelpertest.ContratoQueue. En PutSourceText y en CloseWithSourceText la clave
// se mira antes que el sobre.
type JobStore interface {
	// OpenOrAppend abre la ventana si no existía y le añade las referencias del
	// mensaje si ya existía, en UNA SOLA SENTENCIA y sin ninguna lectura
	// (D-044.26). Es el ÚNICO método que se llama en línea con el entrante.
	OpenOrAppend(ctx context.Context, a Append) error
	// CloseWindow pasa la ventana viva de esa tupla a 'pending'. Devuelve true si
	// ESTA llamada fue la que la cerró. IDEMPOTENTE por el guard
	// `WHERE status='aggregating'`: un segundo cierre afecta 0 filas y devuelve
	// false sin error, que es lo que hace que el flush por intent y el flush por
	// ventana no puedan duplicar un job.
	CloseWindow(ctx context.Context, k WindowKey) (bool, error)
	// ListAggregating devuelve hasta `limit` ventanas vivas, las más antiguas
	// primero. Lo usa el barrido —NUNCA el camino del entrante—: es la lectura que
	// D-044.26 saca de línea con el mensaje.
	ListAggregating(ctx context.Context, limit int) ([]OpenJob, error)
	// PutSourceText escribe el sobre del literal sobre la ventana RECIÉN CERRADA de
	// esa tupla (T1.4). Devuelve true si esta llamada fue la que lo escribió.
	//
	// 🔴 IDENTIFICA LA FILA POR LA TUPLA Y NO POR ID, y hay que decir por qué,
	// porque una tupla puede tener VARIAS filas `pending` a lo largo del día (el
	// índice único de la 0072 es PARCIAL: solo cubre `aggregating`). La fila a la
	// que va el sobre es LA ÚLTIMA TOCADA de esa tupla en `pending` —que es la que
	// acaba de cerrar el barrido, porque cerrarla refrescó su `updated_at`— y solo
	// si su sobre está VACÍO. Ese segundo guard es lo que impide el accidente de
	// verdad: rellenar con el texto de la ventana de ahora un job viejo que se
	// quedó sin componer.
	//
	// El motivo de no llevar id es el contrato de SourceComposer, que recibe una
	// WindowKey (el compositor no lee `intake_jobs` y por tanto no conoce ids). Si
	// algún día ese contrato se amplía, este método debería estrecharse a `id` y
	// perder los dos ORDER BY.
	//
	// false SIN error significa «no había dónde escribir» (la fila ya tenía sobre, o
	// la ventana no está en `pending`): es un no-op, no un fallo.
	PutSourceText(ctx context.Context, k WindowKey, env SourceText) (bool, error)
	// CloseWithSourceText cierra la ventana `seen` Y le guarda su sobre en UNA SOLA
	// SENTENCIA, y solo si la ventana no cambió desde que se leyó. Devuelve true si
	// ESTA llamada fue la que la cerró.
	//
	// ✎ Divergencia deliberada del viejo (D-F7-9, D-F8-13). El viejo cierra con
	// CloseWindow y escribe el sobre después con PutSourceText: dos sentencias, y
	// entre las dos la fila ya está en `pending` SIN sobre, que es justo lo que el
	// worker reclama. Si la reclama en ese hueco procesa un job sin literal, y el
	// sobre que llega después cae sobre una fila que ya no está en `pending` (o, peor,
	// sobre otra `pending` de la misma tupla). Con una sentencia el hueco no existe:
	// la fila nunca es visible cerrada y sin su sobre.
	//
	// Lo que promete:
	//
	//  1. UNA sentencia y NINGUNA lectura: pasa a `pending` la fila de id `seen.ID`,
	//     le escribe las tres columnas del sobre y refresca su `updated_at`.
	//
	//  2. SOLO CIERRA LO QUE SE LEYÓ. La fila tiene que seguir en `aggregating` Y su
	//     `updated_at` tiene que ser EXACTAMENTE `seen.LastActivity`, el que devolvió
	//     ListAggregating. Un mensaje que entró después de leer mueve `updated_at`
	//     (OpenOrAppend) y por tanto la llamada devuelve (false, nil) y NO TOCA la
	//     fila: el sobre que trae se compuso sin ese mensaje y ya no la representa.
	//     La ventana sigue viva y la recoge el siguiente barrido, con el `seen` nuevo.
	//     Una fila ya cerrada, o un id que no existe, es (false, nil) también:
	//     idempotente, como CloseWindow.
	//
	//  3. EL SOBRE VA COMPLETO O VACÍO ENTERO. Completo (SourceText.Complete) se
	//     escribe tal cual. Vacío entero (SourceText.Empty) es el hilo sin mensajes:
	//     cierra igual y deja las tres columnas a NULL, que es una forma legítima de
	//     la fila en la 0072. A MEDIAS —ni lo uno ni lo otro— es error, y se rechaza
	//     ANTES de tocar la base: no se escribe nada y la ventana sigue viva.
	//
	//  4. Con `seen.Key` incompleta (WindowKey.Valid) o `seen.ID` vacío devuelve el
	//     error `intake: ventana incompleta al cerrar con el literal` y no escribe
	//     nada. La clave y el id se miran ANTES que el sobre, como en PutSourceText.
	//     El sobre a medias lleva el texto de cada implementación: en Postgres, el
	//     mismo de PutSourceText (`intake: sobre del literal incompleto (enc=%d dek=%d
	//     kek_id=%t): son las tres o ninguna`); en memoria, el de su gemelo.
	//
	//  5. NO TOCA NINGUNA OTRA FILA: ni la ventana de otra tupla, ni la de otro
	//     tenant, ni las otras `pending` de la MISMA tupla. Identifica por id, que es
	//     lo que PutSourceText no puede hacer (ver su comentario): quien la llama es
	//     el barrido, que sí leyó la fila.
	CloseWithSourceText(ctx context.Context, seen OpenJob, env SourceText) (bool, error)
}

// Las DOS implementaciones satisfacen el puerto, comprobado en compilación. Importa
// más de lo que parece: el doble en memoria existe para probar el agregador SIN
// Postgres, y un doble que se desincronice del puerto convierte una suite verde en
// una suite que no prueba nada.
var (
	_ JobStore = (*Postgres)(nil)
	_ JobStore = (*MemoryStore)(nil)
)
