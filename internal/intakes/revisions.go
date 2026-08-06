package intakes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Clases de revisión (columna `kind` de public.intake_revisions, migración 0045).
// El conjunto está CERRADO por un CHECK en la tabla: añadir una clase exige una
// migración, que es exactamente la fricción que se quiere — una revisión de clase
// desconocida no es un dato nuevo, es un bug que llegó a la BD.
const (
	// RevisionKindCart — la solicitud tal como la cerró el carrito numérico. Es la
	// revisión 1 de todo pedido conversacional (Plan 016/041).
	RevisionKindCart = "cart"
	// RevisionKindInterpreted — lo que el pipeline LLM entendió del texto del
	// cliente (Plan 044).
	RevisionKindInterpreted = "interpreted"
	// RevisionKindCorrected — la corrección del dueño sobre una revisión previa.
	RevisionKindCorrected = "corrected"
	// RevisionKindApproved — la versión que el dueño aprobó y que se le presupuestó
	// al cliente.
	RevisionKindApproved = "approved"
	// RevisionKindCRM — lo que devolvió el puente (ADR-0032).
	RevisionKindCRM = "crm"
	// RevisionKindDiscarded — rastro del descarte MANUAL de un pedido huérfano
	// (D-041.18). Es lo que distingue en el embudo un `abandoned` por descarte del
	// dueño de uno por cancelación del evento, sin un segundo valor de status.
	RevisionKindDiscarded = "discarded"
	// RevisionKindRevalidated — rastro del gancho de revalidación del rescate
	// (D-041.25): qué precios cambiaron, qué líneas se retiraron y el texto exacto
	// con el que se le avisó al cliente.
	RevisionKindRevalidated = "revalidated"
)

// Autores de una revisión (columna `created_by`). Es un ROL, no una persona: aquí
// NUNCA va un identificador de usuario, un número ni un nombre (CERO PII).
const (
	RevisionBySystem = "system"
	RevisionByOwner  = "owner"
	RevisionByCRM    = "crm"
)

// RevisionPayloadVersion es la versión del esquema del payload que escribe ESTE
// código. Viaja DENTRO del blob (`{"version":1,…}`) y no en una columna aparte
// para que el payload siga siendo autodescriptivo cuando alguien lo copie a un
// export, a un webhook o a un ticket: un lector que no entienda la versión debe
// poder saberlo mirando solo lo que tiene delante.
const RevisionPayloadVersion = 1

// ErrEmptyRevisionPayload lo devuelven los stores cuando se intenta escribir una
// revisión sin payload. No se sustituye por un `{}` silencioso: una revisión vacía
// es un relato que afirma "aquí no pasó nada" sobre un acto que sí ocurrió.
var ErrEmptyRevisionPayload = errors.New("la revisión no lleva payload")

// Revision es una versión auditada de la solicitud: qué se negoció y quién lo
// produjo (ADR-0031 §3). NO sustituye a las líneas (Items), que siguen siendo la
// verdad de lo VENDIDO; esto es el borrador y el rastro de cómo se llegó a él.
//
// CERO PII: Payload y RenderedText son dato de negocio en claro. Lo que identifique
// a una persona vive cifrado en intake_buyer_data, jamás aquí.
type Revision struct {
	// IntakeID es la solicitud a la que pertenece la revisión.
	IntakeID string
	// RevisionNo es el correlativo POR SOLICITUD (1..N). Lo asigna el store al
	// escribir: el llamante NO lo elige, porque solo el store puede saber cuál es
	// el siguiente sin carrera.
	RevisionNo int
	// Kind es una de las clases RevisionKind*.
	Kind string
	// Payload es el estado completo de la revisión, versionado ({"version":1,…}).
	Payload json.RawMessage
	// RenderedText es el texto EXACTO que se le mandó al cliente en esta revisión,
	// si lo hubo. Vacío cuando la revisión no produjo mensaje.
	RenderedText string
	// CreatedBy es uno de los RevisionBy* (rol, nunca una persona).
	CreatedBy string
	// CreatedAt lo fija la BD al escribir.
	CreatedAt time.Time
}

// RevisionWriter es el puerto de ESCRITURA de revisiones. Lo satisfacen *Postgres
// (producción) y *MemoryStore (tests).
//
// Está separado del Store de lectura a propósito: quien escribe revisiones es el
// productor del acto (el proyector del carrito hoy; el pipeline LLM, el descarte y
// la revalidación después), y ninguno de ellos necesita —ni debe tener— la bandeja
// entera de solicitudes del tenant.
type RevisionWriter interface {
	// InsertRevision persiste la revisión asignándole el siguiente revision_no de
	// esa solicitud (1 para la primera) y devuelve la revisión ya numerada y
	// fechada. rev.RevisionNo se IGNORA en la entrada.
	//
	// No es idempotente: dos llamadas con el mismo contenido crean dos revisiones,
	// porque dos actos iguales sobre el mismo pedido son dos hechos distintos y la
	// auditoría debe verlos. Quien necesite "una sola vez" lo resuelve aguas
	// arriba, no aquí.
	InsertRevision(ctx context.Context, rev Revision) (Revision, error)
}

// RevisionLine es una línea tal como queda CONGELADA dentro del payload de una
// revisión. No es intakes.Item: una revisión es una FOTO, y por eso guarda el
// precio con el que se negoció aunque el catálogo cambie después. Las etiquetas
// JSON son parte del contrato del payload versionado — cambiarlas exige subir
// RevisionPayloadVersion.
type RevisionLine struct {
	SKU       string  `json:"sku"`
	Label     string  `json:"label"`
	Qty       int     `json:"qty"`
	UnitPrice float64 `json:"unit_price"`
}

// cartRevisionPayload es la forma canónica del payload de RevisionKindCart.
type cartRevisionPayload struct {
	Version int            `json:"version"`
	Total   float64        `json:"total"`
	Items   []RevisionLine `json:"items"`
}

// CartRevisionPayload arma el payload de la revisión de CIERRE DE CARRITO
// (`{"version":1,"total":…,"items":[…]}`, design §3).
//
// Una lista vacía se serializa `[]` y no `null`: quien lea el payload debe poder
// distinguir "se cerró sin líneas" de "aquí no se registró la lista", y `null` no
// dice cuál de las dos es.
func CartRevisionPayload(total float64, lines []RevisionLine) (json.RawMessage, error) {
	if lines == nil {
		lines = []RevisionLine{}
	}
	raw, err := json.Marshal(cartRevisionPayload{
		Version: RevisionPayloadVersion,
		Total:   total,
		Items:   lines,
	})
	if err != nil {
		return nil, fmt.Errorf("intakes: serializar payload de la revisión del carrito: %w", err)
	}
	return raw, nil
}
