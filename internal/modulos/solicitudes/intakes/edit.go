// Porta internal/intakes/edit.go @ 64c181a.
//
// edit.go es la EDICIÓN MANUAL de las líneas de una solicitud (REQ-36 / D-041.26):
// el dueño añade, quita o corrige líneas de un presupuesto SIN LLM de por medio, y
// cada edición deja una revisión `corrected` firmada por `owner`. Es la contraparte
// no conversacional del carrito: sin esta puerta, `confirmed → pending_approval`
// llevaría a un estado editable que nadie puede editar salvo por el pipeline LLM,
// que es de pago y puede no existir.
//
// La acción «corregir» NO es una puerta aparte: es ESTA con un campo más (EditMode).
// Dos puertas dejando la misma revisión `corrected` serían un duplicado, así que hay
// una puerta y un camino.

package intakes

import (
	"context"
	"fmt"
	"strings"
)

// ReservedSKUPrefix es el prefijo de los skus que pone LA PLATAFORMA (hoy solo la
// línea de envío, ShippingSKU). Ninguna línea que mande el dueño puede empezar por
// él: son filas del sistema y la edición no las toca — ni las crea, ni las pisa, ni
// las borra.
//
// El literal se declara AQUÍ porque el dueño del prefijo es el módulo del carrito,
// que ya importa este paquete: importarlo de vuelta sería un ciclo.
const ReservedSKUPrefix = "_"

// EditMode dice con qué intención se reemplazan las líneas de una solicitud. Es un
// parámetro TIPADO y no un bool desnudo: son dos momentos distintos con dos reglas
// distintas, el llamante es quien sabe cuál es, y un bool en la llamada no dice cuál
// de los dos es `true` al leerla.
//
// Lo que NO cambia con el modo: el estado desde el que se edita sigue siendo
// EditableStatus (`as_correction` no amplía los estados editables ni inventa
// transiciones); la revisión sigue siendo una, de clase `corrected` y firmada por
// `owner`; y el empuje al CRM se dispara igual con modo y sin modo, porque cuelga de
// que nazca una revisión y no de este campo.
type EditMode int

const (
	// EditPlain es el `PUT …/items` tal cual: sin señal y sin conducta nueva. Es el
	// CERO del tipo a propósito: el valor por descuido es el que ya existía, nunca el
	// que estrena conducta.
	EditPlain EditMode = iota
	// EditAsCorrection es el `correct` (`"as_correction": true`): la misma escritura,
	// con la señal few-shot dentro de la revisión.
	EditAsCorrection
)

// MaxEditableItems acota cuántas líneas puede traer una edición manual: 200. No es
// una regla de negocio —nadie ha dicho que un pedido no pueda tener 300 líneas—: es
// la cota que impide que un PUT convierta una solicitud en una bandeja de miles de
// filas que después hay que exportar y sumar. Es el mismo orden de magnitud que
// MaxPageSize, y un pedido humano no se le acerca.
const MaxEditableItems = 200

// LineDefect es UN defecto de UNA línea de la edición: en qué posición del cuerpo
// está, qué campo y qué le pasa. Viaja al 400 tal cual, así que sus tres etiquetas
// JSON (`index`, `field`, `message`) son contrato.
//
// Los defectos se ACUMULAN y se devuelven todos juntos: quien llena un formulario de
// diez líneas tiene que ver los diez errores de una vez, no descubrirlos de uno en
// uno a base de reintentos.
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
// que ni siquiera vio. Se devuelve como PUNTERO y así se recoge con errors.As.
type InvalidItemsError struct {
	Defects []LineDefect
}

// Error devuelve "la edición tiene <n> líneas inválidas", donde <n> es
// len(Defects): cuenta DEFECTOS, no líneas (una línea con dos problemas suma dos), y
// no pluraliza (con uno dice «1 líneas inválidas»). Es un texto observable y se
// conserva byte a byte.
func (e *InvalidItemsError) Error() string {
	return fmt.Sprintf("la edición tiene %d líneas inválidas", len(e.Defects))
}

// TooManyItemsError es el rechazo por pasarse de MaxEditableItems. Lleva cuántas
// líneas llegaron y el máximo aplicado. Se devuelve como PUNTERO.
type TooManyItemsError struct {
	Count int
	Max   int
}

// Error devuelve "la edición trae <Count> líneas y el máximo es <Max>". Es un texto
// observable y se conserva byte a byte.
func (e *TooManyItemsError) Error() string {
	return fmt.Sprintf("la edición trae %d líneas y el máximo es %d", e.Count, e.Max)
}

// NotEditableError es el rechazo de una edición sobre una solicitud que NO está en
// `pending_approval`. Lleva el estado actual (ya normalizado) porque quien lo recibe
// necesita saber qué hacer: mover la solicitud a `pending_approval` y reintentar.
//
// Es un error DISTINTO de TransitionError aunque los dos hablen de estados: aquél
// rechaza un movimiento del ciclo de vida, éste rechaza una escritura de datos. Si
// compartieran tipo, el llamante no podría distinguir «no puedes ir ahí» de «no
// puedes editar aquí». Se devuelve como PUNTERO.
type NotEditableError struct {
	Status string
}

// Error devuelve `una solicitud en "<Status>" no se puede editar a mano`, con el
// estado entrecomillado con %q (comillas y saltos de línea salen escapados). Es un
// texto observable y se conserva byte a byte.
func (e *NotEditableError) Error() string {
	return fmt.Sprintf("una solicitud en %q no se puede editar a mano", e.Status)
}

// EditableStatus es el ÚNICO estado desde el que se editan líneas a mano: el
// presupuesto por aprobar (`pending_approval`, D-041.26). Editar un `confirmed`
// cambiaría lo que el cliente ya aceptó sin que nadie se lo dijera, y editar un
// terminal reescribiría historia.
const EditableStatus = StatusPendingApproval

// ValidateEditableItems comprueba las líneas de una edición manual. Es PURA: no
// toca BD ni reloj. Devuelve nil si todas valen —también con la lista `nil` o
// VACÍA: quitar la última línea es una edición legítima—.
//
// Primero la COTA: con más de MaxEditableItems líneas devuelve *TooManyItemsError
// (Count = las que llegaron, Max = MaxEditableItems) SIN mirar ninguna línea; justo
// MaxEditableItems entra.
//
// Después, línea a línea, ACUMULA todos los defectos y los devuelve juntos en un
// *InvalidItemsError, en el orden de las líneas y, dentro de cada línea, en el orden
// sku → label → qty → unit_price. Una línea con varios problemas da varios
// defectos; una línea buena no da ninguno, y el Index de cada defecto es la posición
// de SU línea en la lista que llegó. Los defectos y sus mensajes, que son texto
// observable (viajan al 400):
//
//   - `sku` vacío o solo espacios (Unicode incluido: NBSP, espacio ideográfico):
//     "el sku es obligatorio: es lo que identifica al artículo en el pedido".
//   - `sku` que EMPIEZA por ReservedSKUPrefix: "el sku empieza por _, que está
//     reservado para las líneas que pone wApp (el envío): esas no se editan por
//     aquí". Los dos defectos de sku son excluyentes (una línea da como mucho uno),
//     y el prefijo se mira sobre el sku TAL CUAL: « _envio» (con un espacio delante),
//     «a_b» y el subrayado de ancho completo (U+FF3F) no son reservados.
//   - `label` vacía o solo espacios: "la etiqueta es obligatoria: es lo que se lee en
//     el pedido, en la comanda y en el CSV".
//   - `qty` menor que 1: "la cantidad tiene que ser 1 o más; para quitar la línea,
//     mándala fuera de la lista".
//   - `unit_price` negativo: "el precio no puede ser negativo (0 sí: es un artículo
//     de regalo)". El 0 pasa, y también NaN y +Inf, que no son menores que cero.
//
// Las runas invisibles que NO son espacio (U+200B, U+FEFF) cuentan como contenido:
// un sku o una etiqueta hechos solo de ellas pasan.
//
// Lo que NO valida, a propósito:
//
//   - El SANEO del texto (label/customization). Lo hace la PUERTA por la que entra
//     el texto libre, con la regla única de SanitizeNote: aquí llega ya limpio.
//   - Que el sku EXISTA en el catálogo del tenant. La edición manual es precisamente
//     la puerta para lo que el catálogo no tiene todavía.
//   - Dos líneas con el mismo sku. Son legítimas: D-041.20 parte una línea en dos
//     cuando llevan personalizaciones distintas.
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
// `corrected` (REQ-36 / D-041.26). Devuelve el detalle que devolvió el store.
//
// Es un REEMPLAZO del conjunto y no tres operaciones (añadir/quitar/corregir), y es
// una decisión: una edición del dueño es UN acto ⇒ UNA revisión, y una API por
// línea necesitaría una clave por línea que no existe (dos líneas pueden compartir
// sku y diferenciarse solo por la personalización).
//
// El ORDEN es contrato, y cada paso corta a los siguientes:
//
//  1. VALIDA las líneas (ValidateEditableItems). Si fallan devuelve ese error
//     (*InvalidItemsError / *TooManyItemsError) SIN leer ni escribir nada.
//  2. LEE la solicitud (Store.Get). El recurso se resuelve ANTES que el estado: una
//     solicitud ajena o inexistente responde ErrNotFound y no revela por el código
//     de error que existe.
//  3. Comprueba el ESTADO ya normalizado: si no es EditableStatus devuelve
//     *NotEditableError con el estado actual normalizado, sin escribir.
//  4. ESCRIBE (Store.ReplaceItems) pasando las líneas tal cual, `mode` tal cual y,
//     como estados esperados, StoredVariants(EditableStatus). Cualquier error del
//     store se devuelve intacto: ErrConflict significa que alguien la movió entre la
//     lectura y la escritura, y la edición NO se aplicó.
//
// La LÍNEA DE ENVÍO no viaja en `items` ni se ve afectada: es de la plataforma
// (ShippingSKU), sobrevive intacta a la edición y sigue contando en el total. Un sku
// reservado en la entrada se rechaza en el paso 1, así que esta puerta no puede
// duplicarla, borrarla ni pisarle el precio.
//
// `mode` es el campo `as_correction` (ver EditMode). EditPlain es la conducta de
// siempre, byte a byte. 🔴 El modo NO amplía los estados editables: con
// EditAsCorrection una solicitud en `needs_info` se rechaza igual
// (*NotEditableError). Y aquí NO se transiciona a ninguna parte: la «vuelta a
// `pending_approval`» de la corrección ya es cierta por construcción, porque es el
// único estado desde el que esta puerta escribe (pedir `pending_approval →
// pending_approval` sería una transición inválida).
//
// Tras escribir, y solo entonces, dos efectos que no pueden hacerla fallar:
//
//   - EL EMPUJE AL CRM (PushRevisionToCRM) con el número de la ÚLTIMA revisión del
//     detalle devuelto (LastRevision). Cuelga de que NAZCA una revisión, NO del modo:
//     sale igual con EditPlain que con EditAsCorrection. Si el detalle vuelve sin
//     revisiones NO se empuja (un push con revision_no 0 es el único valor que el
//     contrato del CRM rechaza). Sin CRMPusher cableado no pasa nada.
//   - LA MÉTRICA de corrección, con las líneas de ANTES (las que se leyeron en el
//     paso 2) y las que mandó el dueño.
//
// Errores: *InvalidItemsError / *TooManyItemsError, ErrNotFound, *NotEditableError,
// ErrConflict, o el fallo de infraestructura del store.
func (s *Service) ReplaceItems(ctx context.Context, tenantID, intakeID string, items []Item, mode EditMode) (Detail, error) {
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

	detail, err := s.store.ReplaceItems(ctx, tenantID, intakeID, items, StoredVariants(EditableStatus), mode)
	if err != nil {
		return Detail{}, err
	}

	// El número REAL de la revisión que el store acaba de numerar, leído del detalle
	// que ya está en la mano (los dos stores recargan las revisiones dentro de su
	// unidad de trabajo). Sin revisión no se empuja: un push con revision_no 0 es el
	// único valor que el schema del contrato rechaza, y el puente lo tiraría entero.
	if rev, ok := LastRevision(detail.Revisions); ok {
		s.PushRevisionToCRM(ctx, tenantID, detail, rev.RevisionNo)
	}

	// La métrica de design §10 (T5.2). Va con las líneas de ANTES —las que se acaban
	// de leer para validar el estado— y las que mandó el dueño: es la única forma de
	// saber cuántas cambiaron, porque el store devuelve el resultado, no el diff. Ver
	// correctionCount para por qué el denominador no es `len(items)`.
	s.publishCorrectionMetric(ctx, tenantID, detail.Intake, current.Items, items)
	return detail, nil
}
