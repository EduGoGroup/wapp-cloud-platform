package intakes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// discard.go es el DESCARTE MANUAL del pedido huérfano (Plan 041 · T4.8, D-041.18 /
// D-041.21 / D-041.22): la acción con la que el DUEÑO limpia de su bandeja las
// solicitudes que ya no van a ninguna parte. Destino siempre `abandoned`.
//
// Es la ÚNICA puerta por la que un `expired` legado sale de su estado (D-041.16: el
// reloj está derogado, pero las filas que quedaron dentro tienen que poder
// limpiarse). Por eso consulta CanDiscard y JAMÁS CanTransition: meter
// `expired → abandoned` en el mapa de transiciones lo abriría en
// POST /intakes/{id}/status y en el <select> de la consola, que es exactamente lo
// que el dueño del producto cerró.
//
// Tres cosas que gobiernan el diseño entero, y ninguna es de estilo:
//
//   - Es IRREVERSIBLE y no hay papelera (D-041.22). De ahí que sea por LOTES
//     EXPLÍCITOS de ids y no por filtro: quien descarta nombra lo que descarta.
//   - Un rechazo NO revierte los demás. Cada solicitud del lote es su propia unidad
//     de trabajo; la respuesta cuenta una por una qué pasó.
//   - Es IDEMPOTENTE: repetir el mismo lote deja el mismo estado final y no escribe
//     una segunda revisión. Eso es lo que hace que reintentar tras un fallo a medio
//     lote sea seguro.
//
// ⚠️ LO QUE ESTA TAREA NO ENTREGA, y de quién es: el CIERRE DEL EVENTO
// conversacional (REQ-32e, criterios (g) y (h) del plan) y el filtro `orphan=true`
// del listado (criterio (e)). Los dos son del **Plan 043 · T4.3**, y no por
// prioridad sino porque su materia prima no existe: `public.conversation_events` no
// está en NINGÚN repo del ecosistema y `flow_state` no tiene `event_id`
// (043/design.md:1005-1020 y :1118 los crean). Tampoco se puede "hacer el 043
// primero": 043/tasks.md:396 necesita el estado `abandoned` que publica ESTE plan —
// es un ciclo, no un orden. El filtro `orphan` NO se aproxima a propósito: es el que
// PRESELECCIONA lo que se va a borrar, y una aproximación que frena es aceptable
// mientras que una que elige víctimas no lo es.

// MaxDiscardBatch acota cuántas solicitudes puede descartar UNA llamada. No es una
// regla de negocio: es lo que impide que un solo POST convierta una bandeja entera
// en `abandoned` de un golpe, sobre una acción sin vuelta atrás. Es además el mismo
// orden de magnitud que MaxPageSize, así que el dueño puede descartar como mucho lo
// que cabe en la página que está mirando.
const MaxDiscardBatch = 200

// Razones por las que UNA solicitud del lote no se descartó. Viajan tal cual al
// wire (`skipped[].reason`), así que son contrato: renombrarlas rompe a quien las
// interprete.
const (
	// DiscardSkipNotFound — no existe, o existe pero es de OTRO tenant. Las dos
	// cosas dan la misma razón a propósito (INV-8): distinguirlas confirmaría la
	// existencia de un id ajeno, que es justo lo que el aislamiento no puede filtrar.
	DiscardSkipNotFound = "not_found"
	// DiscardSkipAlreadyDiscarded — ya estaba en `abandoned`. Es la cara visible de
	// la idempotencia: no es un error, es "esto ya estaba hecho".
	DiscardSkipAlreadyDiscarded = "already_discarded"
	// DiscardSkipNotOpen — está en un estado desde el que no se descarta (CanDiscard
	// dice que no). Un `confirmed` se CANCELA, no se descarta.
	DiscardSkipNotOpen = "not_open"
	// DiscardSkipLiveEvent — hay conversación viva detrás: eso se cancela por su
	// propia puerta (Plan 043), no se descarta desde la bandeja.
	DiscardSkipLiveEvent = "live_event"
)

// ErrEmptyDiscardBatch lo devuelve Discard cuando el lote llega sin ids. No se
// trata como "no hay nada que hacer": un lote vacío es casi siempre una UI que
// perdió su selección, y responder 200 con dos listas vacías le diría al dueño que
// se descartó lo que había seleccionado.
var ErrEmptyDiscardBatch = errors.New("el lote de descarte llegó sin solicitudes")

// TooLargeBatchError es el rechazo de un lote que pasa de MaxDiscardBatch.
type TooLargeBatchError struct {
	Count int
	Max   int
}

// Error implementa error.
func (e *TooLargeBatchError) Error() string {
	return fmt.Sprintf("el lote trae %d solicitudes y el máximo es %d", e.Count, e.Max)
}

// DiscardSkip es UNA solicitud del lote que no se descartó, con el porqué.
type DiscardSkip struct {
	IntakeID string
	Reason   string
}

// DiscardResult es el resultado del lote: lo que se descartó y lo que no, con su
// razón. Las dos listas son siempre no-nil (`[]`, nunca `null`): "no se descartó
// nada" y "no sé qué pasó" son respuestas distintas y quien pinta la pantalla hace
// cosas distintas con cada una.
type DiscardResult struct {
	Discarded []string
	Skipped   []DiscardSkip
}

// DiscardOutcome es lo que el store encontró y/o hizo con UNA solicitud. Lleva
// HECHOS, no razones: la traducción a `skipped[].reason` la hace el dominio
// (Service.Discard), que es quien conoce la política.
type DiscardOutcome struct {
	// Discarded dice si esta llamada escribió el `abandoned` (y su revisión).
	Discarded bool
	// Status es el estado ACTUAL de la solicitud, ya normalizado. Con Discarded en
	// true es el estado del que VENÍA; con false, aquel en el que se quedó.
	Status string
	// LiveCart dice si hay conversación viva ligada a la solicitud. Ver
	// Store.Discard para qué significa exactamente "viva" hoy y por qué es
	// PROVISIONAL.
	LiveCart bool
}

// DiscardableStatuses son las claves tal como están ALMACENADAS desde las que se
// puede descartar. Se DERIVAN de CanDiscard —no se listan a mano— para que no haya
// dos fuentes de verdad sobre qué es descartable: el día que `discardable` cambie,
// el WHERE del UPDATE cambia con él.
//
// Pasa por StoredVariants porque el filtro tiene que alcanzar también las claves
// legadas (hoy ninguna de las descartables tiene alias, pero el `closed` del cart
// demuestra que eso pasa). Orden determinista: viaja a un `= ANY($n)` y a los tests.
func DiscardableStatuses() []string {
	out := make([]string, 0, len(known))
	for status := range known {
		if CanDiscard(status) {
			out = append(out, StoredVariants(status)...)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// Discard DESCARTA a mano un lote de solicitudes del tenant, dejándolas en
// `abandoned` (D-041.18). Devuelve, una por una, qué se descartó y qué no con su
// razón; solo devuelve error cuando NO se puede contestar el lote entero:
// ErrEmptyDiscardBatch, *TooLargeBatchError (⇒ 400) o un fallo de infraestructura.
//
// Los ids REPETIDOS se colapsan al primero. Sin eso, el segundo se contestaría
// `already_discarded` por obra del primero y la respuesta tendría el mismo id en las
// dos listas — un cuerpo que se contradice a sí mismo. El límite de
// MaxDiscardBatch se mide ANTES de colapsar: es una cota del cuerpo que llega, no de
// las filas que se tocan.
//
// ⚠️ Si la infraestructura falla a MEDIO lote, lo ya escrito QUEDA escrito (cada
// solicitud es su propia transacción) y esta función corta con error. Es deliberado:
// deshacer los descartes anteriores exigiría una transacción sobre el lote entero, y
// entonces un solo id problemático revertiría el trabajo bueno — justo lo que
// "un rechazo no revierte los otros" prohíbe. Lo que hace segura esa situación es la
// idempotencia: repetir el mismo lote devuelve `already_discarded` para lo ya hecho
// y termina el resto.
func (s *Service) Discard(ctx context.Context, tenantID string, intakeIDs []string) (DiscardResult, error) {
	switch {
	case len(intakeIDs) == 0:
		return DiscardResult{}, ErrEmptyDiscardBatch
	case len(intakeIDs) > MaxDiscardBatch:
		return DiscardResult{}, &TooLargeBatchError{Count: len(intakeIDs), Max: MaxDiscardBatch}
	}

	discardable := DiscardableStatuses()
	res := DiscardResult{Discarded: []string{}, Skipped: []DiscardSkip{}}

	for _, id := range uniqueInOrder(intakeIDs) {
		out, err := s.store.Discard(ctx, tenantID, id, discardable)
		switch {
		case errors.Is(err, ErrNotFound):
			res.Skipped = append(res.Skipped, DiscardSkip{IntakeID: id, Reason: DiscardSkipNotFound})
		case err != nil:
			return DiscardResult{}, err
		case out.Discarded:
			res.Discarded = append(res.Discarded, id)
		default:
			res.Skipped = append(res.Skipped, DiscardSkip{IntakeID: id, Reason: discardSkipReason(out)})
		}
	}
	return res, nil
}

// discardSkipReason traduce los HECHOS que devolvió el store a la razón que se le
// cuenta al llamante. El orden de las ramas es el contrato:
//
//  1. `abandoned` gana a todo: ya está descartada, y decirle "hay conversación
//     viva" a quien repite un lote sería mentirle sobre por qué no pasó nada.
//  2. El estado manda sobre la conversación: una `confirmed` con carrito vivo no se
//     descarta porque está confirmada, no porque haya alguien hablando.
//  3. Solo cuando el estado SÍ era descartable la razón es la conversación viva.
func discardSkipReason(out DiscardOutcome) string {
	switch {
	case out.Status == StatusAbandoned:
		return DiscardSkipAlreadyDiscarded
	case !CanDiscard(out.Status):
		return DiscardSkipNotOpen
	case out.LiveCart:
		return DiscardSkipLiveEvent
	default:
		// El store no escribió y el estado era descartable sin conversación viva:
		// alguien la movió entre el candado y la escritura y volvió a un estado
		// descartable. No es alcanzable con el CAS de hoy, y si lo fuera, `not_open`
		// es la respuesta que hace que el llamante relea en vez de dar por hecho.
		return DiscardSkipNotOpen
	}
}

// uniqueInOrder devuelve los ids sin repetir, conservando el ORDEN de llegada: la
// respuesta se lee junto al lote que se mandó, así que reordenarla obligaría a
// buscar cada id.
func uniqueInOrder(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// discardRevisionPayload es la forma del payload de la revisión `discarded`.
//
// Es la ÚNICA de las formas de revisión que NO congela las líneas, y la razón es
// concreta: `cart` y `corrected` retratan actos que CAMBIAN las líneas, así que su
// payload es la última foto que sobrevive de lo que había antes. El descarte no toca
// ni una línea —el pedido sigue entero en intake_items, criterio (d) del plan—, así
// que copiarlas aquí duplicaría datos sin añadir un solo hecho.
//
// Lo que sí registra, y que no queda en ningún otro sitio, es `from_status`: la
// columna `status` solo dirá `abandoned`, y saber si el dueño limpió una `open`
// huérfana o una `expired` del reloj derogado es la diferencia entre dos historias
// distintas del embudo.
type discardRevisionPayload struct {
	Version    int     `json:"version"`
	FromStatus string  `json:"from_status"`
	Total      float64 `json:"total"`
}

// DiscardedRevisionPayload arma el payload de la revisión de DESCARTE MANUAL
// (`{"version":1,"from_status":…,"total":…}`).
func DiscardedRevisionPayload(fromStatus string, total float64) (json.RawMessage, error) {
	raw, err := json.Marshal(discardRevisionPayload{
		Version:    RevisionPayloadVersion,
		FromStatus: NormalizeStatus(fromStatus),
		Total:      total,
	})
	if err != nil {
		return nil, fmt.Errorf("intakes: serializar payload de la revisión de descarte: %w", err)
	}
	return raw, nil
}

// discardedRevision arma la revisión que deja UN descarte efectivo. Vive aquí y no
// en cada store para que las dos implementaciones no puedan divergir: un MemoryStore
// que escribiera otra foto haría que los tests de handler dijeran algo falso sobre
// producción (mismo criterio que correctedRevision).
//
// `created_by` es `owner` y no `system` porque esto es EXACTAMENTE lo contrario de
// una muerte por reloj: es una persona decidiendo. Es un ROL, nunca un usuario
// (CERO PII): quién lo hizo con nombre y apellidos vive en la bitácora de auditoría.
func discardedRevision(intakeID, fromStatus string, total float64) (Revision, error) {
	payload, err := DiscardedRevisionPayload(fromStatus, total)
	if err != nil {
		return Revision{}, err
	}
	return Revision{
		IntakeID:  intakeID,
		Kind:      RevisionKindDiscarded,
		Payload:   payload,
		CreatedBy: RevisionByOwner,
	}, nil
}
