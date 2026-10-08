// Porta internal/intakes/discard.go @ 64c181a.
//
// discard.go es el DESCARTE MANUAL del pedido huérfano (D-041.18 / D-041.21 /
// D-041.22): la acción con la que el DUEÑO limpia de su bandeja las solicitudes que
// ya no van a ninguna parte. Destino siempre `abandoned`.
//
// Es la ÚNICA puerta por la que un `expired` legado sale de su estado (el reloj está
// derogado, pero las filas que quedaron dentro tienen que poder limpiarse). Por eso
// consulta CanDiscard y JAMÁS CanTransition: meter `expired → abandoned` en el mapa
// de transiciones lo abriría en el selector de estado, que es exactamente lo que se
// cerró.
//
// Tres cosas gobiernan el diseño entero:
//
//   - Es IRREVERSIBLE y no hay papelera. De ahí que sea por LOTES EXPLÍCITOS de ids
//     y no por filtro: quien descarta nombra lo que descarta.
//   - Un rechazo NO revierte los demás. Cada solicitud del lote es su propia unidad
//     de trabajo; la respuesta cuenta una por una qué pasó.
//   - Es IDEMPOTENTE: repetir el mismo lote deja el mismo estado final y no escribe
//     una segunda revisión. Eso hace seguro reintentar tras un fallo a medio lote.

package intakes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// MaxDiscardBatch acota cuántas solicitudes puede descartar UNA llamada: 200. No es
// una regla de negocio: es lo que impide que un solo POST convierta una bandeja
// entera en `abandoned` de un golpe, sobre una acción sin vuelta atrás. Es el mismo
// orden de magnitud que MaxPageSize, así que el dueño puede descartar como mucho lo
// que cabe en la página que está mirando.
const MaxDiscardBatch = 200

// Razones por las que UNA solicitud del lote no se descartó. Viajan tal cual al
// wire (`skipped[].reason`), así que son contrato: renombrarlas rompe a quien las
// interprete.
const (
	// DiscardSkipNotFound ("not_found") — no existe, o existe pero es de OTRO
	// tenant. Las dos cosas dan la misma razón a propósito (INV-8): distinguirlas
	// confirmaría la existencia de un id ajeno.
	DiscardSkipNotFound = "not_found"
	// DiscardSkipAlreadyDiscarded ("already_discarded") — ya estaba en `abandoned`.
	// Es la cara visible de la idempotencia: no es un error, es «esto ya estaba
	// hecho».
	DiscardSkipAlreadyDiscarded = "already_discarded"
	// DiscardSkipNotOpen ("not_open") — está en un estado desde el que no se
	// descarta (CanDiscard dice que no). Un `confirmed` se CANCELA, no se descarta.
	DiscardSkipNotOpen = "not_open"
	// DiscardSkipLiveEvent ("live_event") — el evento conversacional que ESTA
	// solicitud declara sigue `open`: eso se cancela por su propia puerta (la
	// cancelación del evento, que abandona la solicitud de paso — AbandonByEvent),
	// no se descarta desde la bandeja.
	DiscardSkipLiveEvent = "live_event"
)

// ErrEmptyDiscardBatch lo devuelve Discard cuando el lote llega sin ids. No se
// trata como «no hay nada que hacer»: un lote vacío es casi siempre una UI que
// perdió su selección, y responder 200 con dos listas vacías le diría al dueño que
// se descartó lo que había seleccionado. Su texto es observable.
var ErrEmptyDiscardBatch = errors.New("el lote de descarte llegó sin solicitudes")

// TooLargeBatchError es el rechazo de un lote que pasa de MaxDiscardBatch. Lleva
// cuántos ids llegaron y el máximo aplicado. Se devuelve como PUNTERO.
type TooLargeBatchError struct {
	Count int
	Max   int
}

// Error devuelve "el lote trae <Count> solicitudes y el máximo es <Max>". Es un
// texto observable y se conserva byte a byte.
func (e *TooLargeBatchError) Error() string {
	return fmt.Sprintf("el lote trae %d solicitudes y el máximo es %d", e.Count, e.Max)
}

// DiscardSkip es UNA solicitud del lote que no se descartó, con el porqué (una de
// las cuatro DiscardSkip*).
type DiscardSkip struct {
	IntakeID string
	Reason   string
}

// DiscardResult es el resultado del lote: lo que se descartó y lo que no, con su
// razón. Las dos listas que devuelve Discard son siempre no-nil (`[]`, nunca
// `null`): «no se descartó nada» y «no sé qué pasó» son respuestas distintas y quien
// pinta la pantalla hace cosas distintas con cada una.
type DiscardResult struct {
	Discarded []string
	Skipped   []DiscardSkip
}

// DiscardOutcome es lo que el store encontró y/o hizo con UNA solicitud. Lleva
// HECHOS, no razones: la traducción a `skipped[].reason` la hace el dominio
// (Service.Discard), que es quien conoce la política.
type DiscardOutcome struct {
	// Discarded dice si esta llamada escribió el `abandoned` (y su revisión, y el
	// cierre del contenedor si lo hubiera).
	Discarded bool
	// Status es el estado ACTUAL de la solicitud, ya normalizado. Con Discarded en
	// true es el estado del que VENÍA; con false, aquel en el que se quedó.
	Status string
	// LiveEvent dice si el evento conversacional que ESTA solicitud declara
	// (intakes.event_id) sigue `open`. Una solicitud legada sin event_id no tiene
	// evento vivo que mirar y es descartable.
	LiveEvent bool
}

// DiscardableStatuses son las claves tal como están ALMACENADAS desde las que se
// puede descartar: hoy, exactamente `expired` y `open`. Se DERIVAN de CanDiscard
// —no se listan a mano— para que no haya dos fuentes de verdad sobre qué es
// descartable: el día que eso cambie, el filtro de la escritura cambia con él.
//
// Pasa por StoredVariants porque el filtro tiene que alcanzar también las claves
// legadas (hoy ninguna de las descartables tiene alias). El resultado va ORDENADO
// y sin repetidos —viaja a un `= ANY($n)` y a los tests—, y cada llamada devuelve
// un slice propio: mutarlo no afecta a la siguiente.
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
// `abandoned` (D-041.18). Devuelve, una por una y EN EL ORDEN DE LLEGADA, qué se
// descartó y qué no con su razón.
//
// Solo devuelve error cuando NO se puede contestar el lote entero, y entonces el
// resultado es el valor cero:
//
//   - ErrEmptyDiscardBatch si el lote llega sin ids (`nil` o vacío);
//   - *TooLargeBatchError si trae más de MaxDiscardBatch (justo MaxDiscardBatch
//     entra). El límite se mide ANTES de colapsar repetidos: es una cota del cuerpo
//     que llega, no de las filas que se tocan. En los dos casos el store ni se toca;
//   - un fallo de infraestructura del store (cualquier error que no sea
//     ErrNotFound), devuelto intacto.
//
// Los ids REPETIDOS se colapsan al primero: cada id se le pide al store UNA vez y
// se contesta UNA vez. Sin eso, el segundo se contestaría `already_discarded` por
// obra del primero y la respuesta tendría el mismo id en las dos listas.
//
// A cada id se le pide el descarte al store (Store.Discard) con
// DiscardableStatuses() como estados descartables, y lo que el store cuenta se
// traduce así, EN ESTE ORDEN de prioridad:
//
//  1. ErrNotFound ⇒ `not_found` (inexistente o de otro tenant: la misma razón).
//  2. Discarded ⇒ va a la lista de descartadas.
//  3. Status `abandoned` ⇒ `already_discarded`. Gana a todo: decirle «hay
//     conversación viva» a quien repite un lote sería mentirle sobre por qué no pasó
//     nada.
//  4. Un estado desde el que no se descarta (CanDiscard falso) ⇒ `not_open`. El
//     estado manda sobre el evento: una `confirmed` cuyo evento siga vivo no se
//     descarta porque está confirmada, no porque haya alguien hablando.
//  5. Estado descartable con LiveEvent ⇒ `live_event`.
//  6. Estado descartable, sin evento vivo y sin haberse escrito ⇒ `not_open`: es la
//     respuesta que hace que el llamante relea en vez de dar por hecho.
//
// ⚠️ Si la infraestructura falla a MEDIO lote, lo ya escrito QUEDA escrito (cada
// solicitud es su propia transacción) y esta función corta con error, sin pedir los
// ids que faltaban. Es deliberado: deshacer los anteriores exigiría una transacción
// sobre el lote entero, y entonces un solo id problemático revertiría el trabajo
// bueno. Lo que hace segura esa situación es la idempotencia: repetir el mismo lote
// devuelve `already_discarded` para lo ya hecho y termina el resto.
//
// NO NOTIFICA AL CLIENTE, y no por olvido: el descarte es higiene interna del dueño
// y avisar de eso sería una forma rara de despedirse. Por eso pasa por el store
// directamente y no por SetStatus, que sí avisa. Tampoco empuja al CRM.
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
//  2. El estado manda sobre el evento: una `confirmed` cuyo evento siga vivo no se
//     descarta porque está confirmada, no porque haya alguien hablando.
//  3. Solo cuando el estado SÍ era descartable la razón es el evento vivo (el que
//     ESTA solicitud declara — DT-043.2 saldada, ver DiscardOutcome.LiveEvent).
func discardSkipReason(out DiscardOutcome) string {
	switch {
	case out.Status == StatusAbandoned:
		return DiscardSkipAlreadyDiscarded
	case !CanDiscard(out.Status):
		return DiscardSkipNotOpen
	case out.LiveEvent:
		return DiscardSkipLiveEvent
	default:
		// El store no escribió y el estado era descartable sin evento vivo:
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

// discardRevisionPayload es la forma del payload de la revisión `discarded`. El
// orden de sus campos es el orden de las claves en el JSON, que es contrato.
type discardRevisionPayload struct {
	Version    int     `json:"version"`
	FromStatus string  `json:"from_status"`
	Total      float64 `json:"total"`
}

// DiscardedRevisionPayload arma el payload de la revisión de DESCARTE MANUAL:
// `{"version":1,"from_status":…,"total":…}`, con esas tres claves y en ese orden, y
// la versión de RevisionPayloadVersion.
//
// Es la ÚNICA de las formas de revisión que NO congela las líneas: el descarte no
// toca ni una —el pedido sigue entero en sus líneas—, así que copiarlas duplicaría
// datos sin añadir un hecho. Lo que sí registra, y no queda en ningún otro sitio, es
// `from_status`: la columna de estado solo dirá `abandoned`, y saber si el dueño
// limpió una `open` huérfana o una `expired` del reloj derogado es la diferencia
// entre dos historias distintas del embudo.
//
// `fromStatus` se NORMALIZA (NormalizeStatus): la clave legada `closed` se guarda
// como `confirmed`; lo que no es un alias se guarda tal cual, sin recortar ni plegar
// mayúsculas. El total se serializa como lo hace encoding/json.
//
// Si el total no es serializable (NaN, ±Inf) devuelve un error que envuelve al de
// JSON con el prefijo "intakes: serializar payload de la revisión de descarte: ".
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
