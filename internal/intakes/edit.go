package intakes

import (
	"context"
	"fmt"
	"strings"
)

// edit.go es la EDICIÓN MANUAL de las líneas de una solicitud (Plan 041 · T4.10,
// REQ-36 / D-041.26): el dueño añade, quita o corrige líneas de un presupuesto SIN
// LLM de por medio, y cada edición deja una revisión `corrected` firmada por
// `owner`.
//
// Es la contraparte no-conversacional del carrito: el cliente arma el pedido por
// WhatsApp y el dueño lo re-presupuesta a mano cuando hay que ponerle precio a algo
// que el catálogo no tenía (la escena del queso extra, D-041.26 §e). Sin esta
// puerta, `confirmed → pending_approval` llevaría a un estado editable que nadie
// puede editar salvo por el pipeline del Plan 044, que es de pago y puede no
// existir.

// ReservedSKUPrefix es el prefijo de los skus que pone LA PLATAFORMA (hoy solo la
// línea de envío, ShippingSKU). Ninguna línea que mande el dueño puede empezar por
// él: son filas del sistema y esta puerta no las toca — ni las crea, ni las pisa,
// ni las borra.
//
// El literal se declara AQUÍ por la MISMA razón que ShippingSKU (ver shipping.go):
// el dueño del prefijo es el módulo cart (cart.SystemSKUPrefix, D-041.2) y el cart
// ya importa este paquete, así que importarlo de vuelta sería un ciclo. La atadura
// entre los dos literales la muerde un test del lado del cart, que es quien manda.
const ReservedSKUPrefix = "_"

// MaxEditableItems acota cuántas líneas puede tener una solicitud tras una edición
// manual. No es una regla de negocio —nadie ha dicho que un pedido no pueda tener
// 300 líneas—: es la cota que impide que un PUT convierta una solicitud en una
// bandeja de miles de filas que después hay que exportar y sumar. 200 es el mismo
// orden de magnitud que MaxPageSize, y un pedido humano no se le acerca.
const MaxEditableItems = 200

// LineDefect es UN defecto de UNA línea de la edición: en qué posición del cuerpo
// está, qué campo y qué le pasa. Viaja al 400 tal cual.
//
// Los defectos se ACUMULAN y se devuelven todos juntos (mismo criterio que el
// validador del import, T3.1): quien llena un formulario de diez líneas tiene que
// ver los diez errores de una vez, no descubrirlos de uno en uno a base de
// reintentos.
type LineDefect struct {
	// Index es la posición 0-based de la línea en la lista que mandó el llamante.
	Index int `json:"index"`
	// Field es el campo defectuoso (`sku`, `label`, `qty`, `unit_price`).
	Field string `json:"field"`
	// Message dice qué pasa, en la voz del que tiene que arreglarlo.
	Message string `json:"message"`
}

// InvalidItemsError es el rechazo de una edición por líneas mal formadas. NO se
// escribe NADA cuando se devuelve: la edición es todo-o-nada, porque aplicar «las
// líneas que sí valían» dejaría el presupuesto en un estado que el dueño no pidió y
// que ni siquiera vio.
type InvalidItemsError struct {
	Defects []LineDefect
}

// Error implementa error.
func (e *InvalidItemsError) Error() string {
	return fmt.Sprintf("la edición tiene %d líneas inválidas", len(e.Defects))
}

// TooManyItemsError es el rechazo por pasarse de MaxEditableItems.
type TooManyItemsError struct {
	Count int
	Max   int
}

// Error implementa error.
func (e *TooManyItemsError) Error() string {
	return fmt.Sprintf("la edición trae %d líneas y el máximo es %d", e.Count, e.Max)
}

// NotEditableError es el rechazo de una edición sobre una solicitud que NO está en
// `pending_approval`. Lleva el estado actual porque quien lo recibe necesita saber
// qué hacer: mover la solicitud a `pending_approval` (D-041.26) y reintentar.
//
// Es un error DISTINTO de TransitionError aunque los dos hablen de estados: aquél
// rechaza un movimiento del ciclo de vida, éste rechaza una escritura de datos. Si
// compartieran tipo, el llamante no podría distinguir «no puedes ir ahí» de «no
// puedes editar aquí».
type NotEditableError struct {
	Status string
}

// Error implementa error.
func (e *NotEditableError) Error() string {
	return fmt.Sprintf("una solicitud en %q no se puede editar a mano", e.Status)
}

// EditableStatus es el ÚNICO estado desde el que se editan líneas a mano: el
// presupuesto por aprobar (D-041.26). Editar un `confirmed` cambiaría lo que el
// cliente ya aceptó sin que nadie se lo dijera, y editar un terminal reescribiría
// historia.
const EditableStatus = StatusPendingApproval

// ValidateEditableItems comprueba las líneas de una edición manual y devuelve
// TODOS sus defectos (*InvalidItemsError) o *TooManyItemsError. Es PURA: no toca
// BD ni reloj.
//
// Lo que NO valida, a propósito:
//
//   - El SANEO del texto (label/customization). Lo hace la PUERTA por la que entra
//     el texto libre, con la regla única de cart.SanitizeNote (D-041.19): aquí
//     llega ya limpio. Este paquete no puede importar el cart (sería un ciclo) y
//     duplicar la regla daría dos contratos para la misma columna.
//   - Que el sku EXISTA en el catálogo del tenant. La edición manual es
//     precisamente la puerta para lo que el catálogo no tiene todavía, y la
//     revalidación contra el catálogo es otra tarea (T4.9, D-041.25).
//   - Dos líneas con el mismo sku. Son legítimas: D-041.20 parte una línea en dos
//     cuando llevan personalizaciones distintas, y el pedido de dos empanadas por
//     separado es dos líneas.
func ValidateEditableItems(items []Item) error {
	if len(items) > MaxEditableItems {
		return &TooManyItemsError{Count: len(items), Max: MaxEditableItems}
	}

	defects := make([]LineDefect, 0, len(items))
	for i, it := range items {
		defects = append(defects, lineDefects(i, it)...)
	}
	if len(defects) > 0 {
		return &InvalidItemsError{Defects: defects}
	}
	return nil
}

// lineDefects reúne los defectos de UNA línea. Devuelve todos los que tenga, no el
// primero: una línea con el sku vacío Y la cantidad en cero tiene dos problemas.
func lineDefects(i int, it Item) []LineDefect {
	var out []LineDefect
	add := func(field, msg string) {
		out = append(out, LineDefect{Index: i, Field: field, Message: msg})
	}

	switch {
	case strings.TrimSpace(it.SKU) == "":
		add("sku", "el sku es obligatorio: es lo que identifica al artículo en el pedido")
	case strings.HasPrefix(it.SKU, ReservedSKUPrefix):
		add("sku", "el sku empieza por "+ReservedSKUPrefix+", que está reservado para las líneas que pone wApp (el envío): esas no se editan por aquí")
	}
	if strings.TrimSpace(it.Label) == "" {
		add("label", "la etiqueta es obligatoria: es lo que se lee en el pedido, en la comanda y en el CSV")
	}
	if it.Qty < 1 {
		add("qty", "la cantidad tiene que ser 1 o más; para quitar la línea, mándala fuera de la lista")
	}
	if it.UnitPrice < 0 {
		add("unit_price", "el precio no puede ser negativo (0 sí: es un artículo de regalo)")
	}
	return out
}

// ReplaceItems SUSTITUYE las líneas de cliente de una solicitud en
// `pending_approval` por las que manda el dueño, y deja constancia con una revisión
// `corrected` (REQ-36 / D-041.26).
//
// Es un REEMPLAZO del conjunto y no tres operaciones (añadir/quitar/corregir), y
// eso es una decisión, no una comodidad:
//
//   - Las tres operaciones de REQ-36 se expresan con un solo conjunto nuevo, y una
//     edición del dueño es UN acto ⇒ UNA revisión. Con operaciones sueltas, mover
//     una línea de 2 a 3 unidades y añadir el queso serían dos revisiones de un
//     mismo cambio de opinión, y la auditoría contaría dos negociaciones donde hubo
//     una.
//   - Una API por línea necesitaría una clave por línea, y no la hay: dos líneas
//     pueden compartir sku y diferenciarse solo por la personalización (D-041.20).
//     Direccionar «la segunda hamburguesa» por índice es exactamente el contrato
//     que se rompe cuando dos pestañas editan a la vez.
//
// La LÍNEA DE ENVÍO no viaja en `items` ni se ve afectada: es de la plataforma
// (ShippingSKU, D-041.11), sobrevive intacta a la edición y sigue contando en el
// total. Un sku reservado en la entrada se rechaza en la validación, así que esta
// puerta no puede duplicarla, borrarla ni pisarle el precio que el dueño le puso.
//
// Errores: *InvalidItemsError / *TooManyItemsError (líneas mal formadas),
// ErrNotFound (no es del tenant), *NotEditableError (la solicitud no está en
// `pending_approval`) y ErrConflict (alguien la movió entre la lectura y la
// escritura).
func (s *Service) ReplaceItems(ctx context.Context, tenantID, intakeID string, items []Item) (Detail, error) {
	if err := ValidateEditableItems(items); err != nil {
		return Detail{}, err
	}

	// El recurso se resuelve ANTES que el estado, igual que en SetStatus: una
	// solicitud ajena responde 404 y no revela por el código de error que existe.
	current, err := s.store.Get(ctx, tenantID, intakeID)
	if err != nil {
		return Detail{}, err
	}
	if from := NormalizeStatus(current.Status); from != EditableStatus {
		return Detail{}, &NotEditableError{Status: from}
	}

	return s.store.ReplaceItems(ctx, tenantID, intakeID, items, StoredVariants(EditableStatus))
}

// correctedRevision arma la revisión que deja una edición manual, a partir del
// estado YA PERSISTIDO de la solicitud (líneas y total leídos después de escribir).
//
// Vive aquí y no en cada store para que las dos implementaciones no puedan
// divergir: un MemoryStore que escribiera otra foto haría que los tests de handler
// dijeran algo falso sobre producción.
//
// La foto incluye TODAS las líneas, también la de envío, y por eso `total` cuadra
// con la suma de `items`. Es la diferencia con la revisión del carrito
// (CartRevisionPayload), que retrata lo que armó el CLIENTE antes de que la
// plataforma le colgara su línea: aquélla responde «qué pidió», ésta responde «cómo
// quedó el presupuesto tras la corrección», y un payload cuyo total no sumara sus
// propias líneas no respondería ninguna de las dos.
func correctedRevision(intakeID string, total float64, items []Item) (Revision, error) {
	payload, err := CorrectedRevisionPayload(total, revisionLinesOf(items))
	if err != nil {
		return Revision{}, err
	}
	return Revision{
		IntakeID: intakeID,
		Kind:     RevisionKindCorrected,
		Payload:  payload,
		// Rol, nunca una persona (CERO PII): quién lo hizo con nombre y apellidos
		// vive en la bitácora de auditoría, que es donde se pregunta eso.
		CreatedBy: RevisionByOwner,
	}, nil
}

// revisionLinesOf congela las líneas de la solicitud en la forma del payload. No
// lleva added_at ni personalización: la revisión ya está fechada entera, y
// RevisionLine es contrato versionado (añadirle un campo exige subir
// RevisionPayloadVersion).
func revisionLinesOf(items []Item) []RevisionLine {
	out := make([]RevisionLine, 0, len(items))
	for _, it := range items {
		out = append(out, RevisionLine{
			SKU: it.SKU, Label: it.Label, Qty: it.Qty, UnitPrice: it.UnitPrice,
		})
	}
	return out
}

// systemItems son las líneas que puso LA PLATAFORMA (prefijo reservado): las que
// SOBREVIVEN a una edición manual. Es la contracara del `left(sku,1) <> '_'` del
// DELETE del store Postgres, y está aquí para que las dos implementaciones no
// puedan discrepar sobre qué es una línea del sistema.
func systemItems(items []Item) []Item {
	out := make([]Item, 0, 1)
	for _, it := range items {
		if strings.HasPrefix(it.SKU, ReservedSKUPrefix) {
			out = append(out, it)
		}
	}
	return out
}

// editedTotal es la suma de las líneas tal como quedan tras la edición. Existe
// como función para que el MemoryStore no reimplemente la aritmética del UPDATE
// del store Postgres.
func editedTotal(items []Item) float64 {
	var total float64
	for _, it := range items {
		total += float64(it.Qty) * it.UnitPrice
	}
	return total
}
