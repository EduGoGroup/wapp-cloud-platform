package intake

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// machine.go — LA MÁQUINA DE ESTADOS de `intake_jobs` (Plan 044 · Ola 2 · T2.1).
//
// El resto del paquete (store.go / postgres.go / memory.go) es LA COLA: abre
// ventanas, las amplía y las cierra. Esto es lo que pasa DESPUÉS: el worker del
// pipeline toma un job cerrado, lo lleva por sus etapas y lo termina.
//
// # POR QUÉ ES UN PUERTO APARTE Y NO CUATRO MÉTODOS MÁS EN JobStore
//
// `JobStore` dice de sí mismo «CUATRO operaciones y ninguna más, a propósito» y el
// motivo está escrito allí: el sink corre EN LÍNEA con el mensaje del cliente y no
// puede tener delante un método que le permita leer ni bloquear (D-044.26).
// Colgar aquí `ClaimNext` —que LEE, que bloquea filas y que devuelve el sobre del
// literal— habría convertido ese puerto estrecho en uno ancho, y la garantía
// dejaría de ser estructural para pasar a ser una advertencia en un comentario.
//
// Las DOS interfaces las implementa el MISMO `*Postgres`, que es lo correcto: hay
// una sola tabla y un solo pool. Lo que se separa no es la implementación, es lo
// que cada llamante PUEDE PEDIR.
//
// # LA MÁQUINA, DICHA ENTERA
//
//	aggregating ──CloseWindow──▶ pending ──ClaimNext──▶ processing
//	                               ▲                        │
//	                               └────── Release ─────────┤ SaveStage (p2→p3→p4→match→draft)
//	                                                        │
//	                                                        ├──Finish──▶ done    ┐ TERMINALES
//	                                                        └──Fail────▶ failed  ┘ ABSORBENTES
//
// Todas las transiciones son un `UPDATE … WHERE status = …` de UNA sola sentencia:
// el guard de estado ES la exclusión mutua, no hay lectura previa que se pueda
// quedar rancia entre el SELECT y el UPDATE, y una transición que llega tarde
// afecta 0 filas y devuelve `false` SIN error — que es la misma convención que ya
// usan `CloseWindow` y `PutSourceText` en este paquete.
//
// # 🔴 INV-13: LA ÚNICA EXCEPCIÓN AL «NADA SE BORRA»
//
// La máquina no borra nada —el rastro queda consultable por SQL— salvo UNA cosa: al
// entrar en `done` o `failed`, LAS TRES columnas del sobre del literal
// (`source_text_enc`, `source_text_dek`, `source_text_kek_id`) se ponen a NULL EN LA
// MISMA SENTENCIA del guard. Ni en un barrido posterior ni en una segunda sentencia:
// hacerlo aparte deja SIEMPRE una ventana en la que la fila está terminada y
// todavía guarda el literal del cliente, y esa ventana es exactamente lo que INV-13
// existe para que no exista. Es la misma forma con la que
// `integrations.MarkWebhookDelivered` vacía su `payload` al cerrar el claim.
//
// `source_refs` NO se toca: los `wa_message_id` son identificadores opacos del
// protocolo y son el rastro que sobrevive. Y la copia de registro del texto tampoco
// se pierde, porque nunca estuvo aquí: la fuente canónica es
// `conversation_event_messages` (Plan 043 · D-043.13).

// Etapas de `intake_jobs.stage`, vocabulario CERRADO por el CHECK
// `intake_jobs_stage_check` de la 0072. Una etapa fuera de esta lista NO llega a
// Postgres: se rechaza en Go (ver Artifact.Validate), porque un error de CHECK
// devuelve un mensaje del motor y no dice cuál era la etapa esperada.
const (
	StageP2    = "p2"    // ideas principales (T2.2)
	StageP3    = "p3"    // especificaciones por ítem (T2.3)
	StageP4    = "p4"    // normalización (T2.4)
	StageMatch = "match" // cruce con el catálogo
	StageDraft = "draft" // borrador listo
)

// stageOrder es el ORDEN de la máquina, `p2→p3→p4→match→draft`, y no es
// decorativo: es lo que impide que una reanudación RETROCEDA. El guard de
// saveStageSQL compara posiciones en esta misma secuencia.
//
// 🔴 Está DUPLICADO en el SQL (`ARRAY['p2','p3','p4','match','draft']`) porque
// `array_position` necesita el array dentro de la sentencia. La duplicación la
// custodia TestStageOrder_CoincideConElArrayDelSQL, que es un test de simetría
// entre este slice y esa constante: si alguien añade una etapa aquí y no allí, el
// guard dejaría de conocerla y `array_position` devolvería NULL — y una
// comparación con NULL no es TRUE, así que el UPDATE afectaría 0 filas y el worker
// vería «transición perdida» sin ninguna pista de por qué.
var stageOrder = []string{StageP2, StageP3, StageP4, StageMatch, StageDraft}

// StageIndex devuelve la posición de una etapa en la máquina, o -1 si no pertenece
// al vocabulario. Es la forma de preguntar «¿esto es una etapa?» sin repetir la
// lista por el código.
func StageIndex(stage string) int {
	for i, s := range stageOrder {
		if s == stage {
			return i
		}
	}
	return -1
}

// IsTerminal dice si un estado es ABSORBENTE. Se nombra aquí y no se comprueba con
// `status == "done" || status == "failed"` suelto por el código para que el día que
// haya un tercer terminal no haya que buscarlos.
func IsTerminal(status string) bool {
	return status == StatusDone || status == StatusFailed
}

// Artifact es la salida de UNA etapa, tal como se persiste en el objeto JSONB
// `intake_jobs.artifacts` bajo la clave de su etapa.
//
// El contrato entre etapas se persiste (design §3.2): es lo que hace la reanudación
// gratis —un job que vuelve a la cola con `{"p2":…}` ya escrito no repite P2— y por
// eso el artefacto tiene que estar ENTERO o no estar. Nunca a medias.
type Artifact struct {
	// Stage es la etapa a la que pertenece. Es también la CLAVE bajo la que se
	// guarda el payload y el valor al que salta la columna `stage`.
	Stage string
	// Payload es el JSON versionado de la etapa (`{"version":1,…}`). Llega ya
	// serializado: este paquete no conoce la forma de ninguna etapa —eso es de
	// T2.2/T2.3/T2.4— y no debe conocerla.
	Payload json.RawMessage
}

// Validate es LA PUERTA de «un artefacto inválido JAMÁS se persiste». Devuelve nil
// solo si el artefacto puede escribirse sin dejar la fila peor de lo que estaba.
//
// # QUÉ COMPRUEBA, Y QUÉ NO
//
// Comprueba la FORMA, que es lo único que este paquete puede saber:
//
//  1. la etapa pertenece al vocabulario CERRADO de la 0072 (si no, el UPDATE moriría
//     con un error de CHECK del motor, ilegible para el operador);
//  2. el payload es JSON válido (un payload roto convierte `artifacts` en un objeto
//     que ningún lector puede decodificar, y el `||` de jsonb ni siquiera lo
//     rechazaría: fallaría el cast `::jsonb` con el texto crudo en el mensaje de
//     error — literal del cliente en un log, que es lo que ADR-0034 prohíbe);
//  3. el payload es un OBJETO JSON, no un array ni un escalar (`artifacts` es
//     `{"p2":{…},"p3":{…}}`: un `3` o un `[…]` ahí dentro rompe a todo lector);
//  4. lleva `version` numérica y >= 1 — los artefactos son VERSIONADOS por diseño
//     (§3.2, `{"version":1,...}`) y sin ese campo un artefacto viejo es
//     indistinguible de uno nuevo el día que la forma cambie.
//
// NO comprueba NADA del dominio: que cada `evidence` sea subcadena del literal, que
// haya al menos un `want`, que las cantidades sean coherentes. Eso lo valida el
// worker de cada etapa ANTES de llamar aquí (T2.2–T2.4), y esta puerta no lo
// sustituye ni lo simula.
//
// Devuelve error —y no un bool como SourceText.Complete— porque «inválido» tiene
// cinco causas distintas y el worker tiene que poder decir CUÁL en su log sin
// volcar el payload.
func (a Artifact) Validate() error {
	if StageIndex(a.Stage) < 0 {
		return fmt.Errorf("intake: etapa %q fuera del vocabulario %v", a.Stage, stageOrder)
	}
	if len(a.Payload) == 0 {
		return fmt.Errorf("intake: artefacto de la etapa %q vacío", a.Stage)
	}
	// Se decodifica a un mapa y no a `any`: eso comprueba de una vez que es JSON
	// válido Y que es un objeto. Un array o un escalar fallan aquí con un error de
	// tipo, no pasan a una segunda comprobación que alguien podría borrar.
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(a.Payload, &obj); err != nil {
		// 🔴 El error NO cita el payload: puede contener literal del cliente
		// (ADR-0034). Solo dice la etapa y la causa.
		return fmt.Errorf("intake: artefacto de la etapa %q no es un objeto JSON válido: %w", a.Stage, err)
	}
	raw, ok := obj["version"]
	if !ok {
		return fmt.Errorf("intake: artefacto de la etapa %q sin campo `version` (los artefactos son versionados, design §3.2)", a.Stage)
	}
	var version int
	if err := json.Unmarshal(raw, &version); err != nil || version < 1 {
		return fmt.Errorf("intake: artefacto de la etapa %q con `version` inválida: se espera un entero >= 1", a.Stage)
	}
	return nil
}

// ClaimedJob es un job TOMADO: todo lo que el worker necesita para seguir, y nada
// que tenga que ir a buscar después.
//
// Lleva el sobre y los artefactos A PROPÓSITO: el claim es UNA sentencia con
// `RETURNING`, así que reanudar no cuesta una lectura extra. Si el claim devolviera
// solo el id, cada job costaría dos viajes y —peor— habría un instante entre el
// claim y la lectura en el que otro proceso podría haberlo terminado.
type ClaimedJob struct {
	ID  string
	Key WindowKey
	// Stage es la ÚLTIMA etapa cuyo artefacto se persistió, o "" si aún ninguna.
	// ES LA REANUDACIÓN: el worker se salta todo lo que esté en `Artifacts` y
	// arranca en la siguiente.
	Stage string
	// MessageTS es el instante del PRIMER mensaje de la ventana (D-044.9). Es la
	// BASE DE FECHAS de P4 —«el miércoles que viene» se resuelve contra esto— y por
	// eso un job reanudado dos días después no cambia de fecha: no se lee el reloj,
	// se lee esta columna.
	MessageTS time.Time
	// SourceRefs son los `wa_message_id` de la ventana. Sobreviven al terminal.
	SourceRefs []string
	// SourceText es el sobre del literal, con lo que P2/P3/P4 trabajan. 🔴 En un job
	// que ya pasó por un terminal esto viene VACÍO por INV-13 — pero un job terminal
	// no se reclama nunca, así que un claim con sobre vacío significa otra cosa: que
	// el compositor del flush no llegó a escribirlo (T1.4 devolvió false). El worker
	// tiene que distinguirlo y fallar el job, no descifrar la nada.
	SourceText SourceText
	// Artifacts es lo ya persistido, por etapa. Vacío en el primer claim.
	Artifacts map[string]json.RawMessage
	// Attempts son los intentos YA CONSUMIDOS antes de éste, tal como los cuenta
	// la columna `attempts` de la 0078. El intento que empieza con este claim es
	// `Attempts + 1` — misma convención 1-based que `integrations.Worker.fail`.
	//
	// 🔴 VIAJA EN EL CLAIM Y NO EN UNA LECTURA APARTE porque la decisión que
	// alimenta —«¿este tropiezo se reintenta o mata el job?»— hay que tomarla
	// mientras el job sigue en `processing`: `Fail` tiene el guard
	// `status = 'processing'`, así que preguntarlo DESPUÉS de devolverlo a
	// `pending` llegaría tarde y afectaría 0 filas. Lo añadió T2.5 (la política);
	// la columna la dejó puesta T2.1 (la sede).
	Attempts int
}

// PipelineStore es el puerto del WORKER del pipeline (Ola 2), y es deliberadamente
// distinto de JobStore: aquí sí se lee, aquí sí se bloquean filas y aquí sí viaja el
// sobre del literal. Nada de esto puede ocurrir en línea con el mensaje del cliente.
//
// La convención de retorno es la del paquete: `(false, nil)` significa «la
// transición no aplicó» —otro worker se adelantó, o el job ya estaba terminado— y NO
// es un fallo. `error` queda para lo que sí lo es: la base caída, un artefacto
// inválido, una clave mal formada.
type PipelineStore interface {
	// ClaimNext toma el job `pending` más antiguo y lo pasa a `processing`, en UNA
	// sentencia. Devuelve false sin error cuando no hay nada que tomar.
	//
	// El guard vive en el SELECT interno (`WHERE status = 'pending'` + FOR UPDATE
	// SKIP LOCKED): un segundo claim del MISMO job no lo encuentra, porque ya no
	// está en `pending`. Eso es «doble-claim pierde uno».
	ClaimNext(ctx context.Context) (ClaimedJob, bool, error)
	// SaveStage persiste el artefacto de una etapa y mueve `stage` a esa etapa, en
	// UNA sentencia y solo si el job sigue `processing`. Un artefacto inválido no
	// llega a la base: se rechaza antes con error.
	//
	// Es idempotente hacia adelante y CERRADO hacia atrás: repetir la etapa actual
	// vale (una reanudación puede volver a producirla), retroceder no.
	SaveStage(ctx context.Context, jobID string, a Artifact) (bool, error)
	// Release devuelve un job `processing` a `pending` sin tocar nada más. Es la
	// ARISTA DE VUELTA que hace posible la reanudación: el proveedor caído deja el
	// job en la cola, con sus artefactos y su sobre intactos, para el siguiente
	// intento (T2.5).
	//
	// 🔴 NO vacía el sobre. Solo los terminales lo hacen (INV-13); un Release que
	// borrara el literal dejaría el job vivo y sin con qué continuar.
	Release(ctx context.Context, jobID string) (bool, error)
	// Retry es la OTRA arista de vuelta: devuelve el job a `pending` COBRÁNDOLE el
	// intento —`attempts + 1`— y EMPUJANDO `next_attempt_at` hasta `next`. Es la
	// única forma de reencolar un job que acaba de fallar sin que el claim se lo
	// lleve otra vez en el acto.
	//
	// 🔴 LA DIFERENCIA CON Release ES EL CASTIGO, Y NO ES COSMÉTICA. `Release`
	// devuelve el job intacto (apagado ordenado del worker: el job no falló, es
	// que el proceso se va) y `Retry` lo devuelve castigado (el intento se
	// consumió y hay que esperar). Usar `Release` donde toca `Retry` reproduce
	// exactamente la tormenta que la 0078 existe para impedir: reclamar, fallar y
	// volver a reclamar a la velocidad del error, con `attempts` clavado en 0 para
	// siempre — así que el techo de intentos no llegaría NUNCA y el job no moriría
	// jamás.
	//
	// `next` lo calcula el llamante (la política es del worker, no de la máquina) y
	// es un instante del reloj de GO, no del motor. Es la única marca de esta tabla
	// que no pone Postgres, y por eso el COMMENT de la 0078 aclara que el `<= now()`
	// del claim sí lo resuelve el motor: dos relojes a segundos de distancia
	// desplazan el reintento unos segundos, que es ruido frente a un backoff que
	// empieza en 30 s. Compararlos para decidir algo sí sería un defecto; empujar
	// una marca con uno y leerla con el otro, no.
	//
	// 🔴 NO ESCRIBE `error`. Esa columna es la causa de muerte de un job `failed`
	// (COMMENT de la 0072) y escribirla en cada tropiezo dejaría un job que después
	// termina bien con una causa de muerte que no ocurrió. La causa del reintento va
	// al log estructurado del worker, con su `causa=` (calidad|infra). El día que
	// haga falta consultarla por SQL, lo que toca es una columna `last_error` propia
	// —la gemela de `webhook_outbox`— y no reusar ésta.
	Retry(ctx context.Context, jobID string, next time.Time) (bool, error)
	// Finish lleva el job a `done` y VACÍA LAS TRES columnas del sobre en la misma
	// sentencia (INV-13). `intakeID` es el borrador creado (Ola 3): vacío deja la
	// columna como estaba.
	//
	// 🔴 El id del borrador viaja AQUÍ y no en un UPDATE posterior porque `done` es
	// ABSORBENTE: una segunda sentencia sobre un job ya terminado afectaría 0 filas
	// y el `intake_id` no se escribiría nunca.
	Finish(ctx context.Context, jobID, intakeID string) (bool, error)
	// Fail lleva el job a `failed` con su causa, y VACÍA LAS TRES columnas del sobre
	// igual que Finish (INV-13). `stage` NO se borra: es dónde murió, y eso es
	// rastro, no búfer.
	//
	// 🔴 `reason` es texto de OPERADOR y no debe llevar literal del cliente: quien
	// llama tiene esa responsabilidad (COMMENT de la columna en la 0072). Aquí solo
	// se exige que no venga vacía —un job muerto sin causa no es diagnosticable.
	Fail(ctx context.Context, jobID, reason string) (bool, error)
}

// La implementación Postgres satisface el puerto, comprobado en compilación. NO hay
// doble en memoria de PipelineStore, y es una decisión: los cuatro guards de esta
// máquina viven en SQL (el `status =` del claim, el `array_position` del avance, el
// vaciado del sobre en la misma sentencia) y un doble en Go los REESCRIBIRÍA a mano
// —que es justo la deriva contra la que avisa el comentario de MemoryStore—. Los
// tests de esta máquina son de integración, contra Postgres real, porque es ahí
// donde está lo que hay que probar.
var _ PipelineStore = (*Postgres)(nil)
