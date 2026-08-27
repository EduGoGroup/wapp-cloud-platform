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

// ════════════════════════════════════════════════════════════════════════════
// LA ACCIÓN «CORREGIR» DEL 044 (T4.4, D-044.48 §1)
// ════════════════════════════════════════════════════════════════════════════
//
// `correct` NO es una ruta nueva: es ESTE PUT con un campo más. Dos puertas
// distintas dejando la misma revisión `corrected` es «la clase de duplicado que este
// plan ya pagó en el hallazgo #24» (D-044.46), así que la decisión fue una puerta y
// un camino. El campo es opcional y sin él esta operación se comporta EXACTAMENTE
// como antes de T4.4 — cero regresión para el 041, que es requisito duro.
//
// Lo que el campo activa son dos cosas, y conviene decir qué vale cada una:
//
//   - la SEÑAL FEW-SHOT (D-044.11), que se produce y se guarda en el payload de la
//     revisión. Hoy NO TIENE CONSUMIDOR: es de la Ola 5 (ver CorrectionSignal).
//   - la «vuelta a `pending_approval`», que es un NO-OP y no un descuido. Ver
//     EditMode.

// EditMode dice si la edición que entra es la corrección declarada del 044 o la
// edición manual rutinaria del 041. Es un parámetro TIPADO y no un bool desnudo por
// lo mismo que StatusNotice (notifier.go) y ShippingPolicy (shipping.go): son dos
// momentos distintos con dos reglas distintas, el llamante es quien sabe cuál es, y
// un bool en la llamada no dice cuál de los dos es `true` al leerla.
//
// 🔴 LO QUE NO CAMBIA CON EL MODO, Y ES LA MITAD DE LA TAREA:
//
//   - el ESTADO desde el que se edita sigue siendo EditableStatus. `as_correction`
//     NO amplía los estados editables ni inventa transiciones.
//   - la REVISIÓN sigue siendo una, de clase `corrected` y firmada por `owner`.
//   - el EMPUJE AL CRM se dispara igual con modo y sin modo, porque cuelga de que
//     NAZCA una revisión y no de este campo (ver Service.ReplaceItems).
type EditMode int

const (
	// EditPlain es el `PUT …/items` del Plan 041 tal cual: sin señal y sin conducta
	// nueva. Es el CERO del tipo a propósito, igual que NoticeToClient: el valor por
	// descuido es el que ya existía, nunca el que estrena conducta.
	EditPlain EditMode = iota
	// EditAsCorrection es el `correct` del 044 (`"as_correction": true`): la misma
	// escritura, con la señal few-shot dentro de la revisión.
	EditAsCorrection
)

// esCorrección responde si este modo deja señal. Método —y no una comparación suelta
// en el store— para que la regla viva junto al tipo, igual que StatusNotice.silencia.
func (m EditMode) esCorrección() bool { return m == EditAsCorrection }

// últimaRevisión responde «¿cuál es la revisión de número más alto de esta solicitud,
// y de qué clase?» — el borrador que la corrección está reemplazando. Cero y cadena
// vacía cuando no hay ninguna, que no es un error: es una solicitud que nadie retrató
// todavía.
//
// Es una FUNCIÓN y no un dato porque solo se pregunta cuando hace falta: en el camino
// del 041 no se llega a llamarla, y así ese camino no paga ni una sentencia de más.
type últimaRevisión func() (no int, kind string, err error)

// señalDeCorrección es LA REGLA de la señal few-shot, y vive aquí una sola vez.
//
// 🔴 POR QUÉ LA GUARDA ESTÁ SOLO EN ESTE SITIO. La primera versión de esta pieza la
// tenía dos veces —aquí y en cada store—, y una mutación lo demostró: quitarla de esta
// función no ponía rojo ni un test, porque los stores ya la habían comprobado antes de
// llamar. Una defensa duplicada tapa a los tests de conducta y convierte su verde en
// una afirmación sobre la copia que sobrevivió. Ahora los stores aportan SOLO su
// consulta (cada uno sabe leerla bajo su propio candado) y quién decide es esto.
//
// La consulta se resuelve CON EL CANDADO YA TOMADO y no antes: entre el Get del
// Service y el `FOR UPDATE` del store cabe otra escritura, y una señal que apuntara a
// la revisión equivocada sería peor que ninguna.
func señalDeCorrección(mode EditMode, última últimaRevisión) (CorrectionSignal, error) {
	if !mode.esCorrección() {
		return CorrectionSignal{}, nil
	}
	no, kind, err := última()
	if err != nil {
		return CorrectionSignal{}, err
	}
	return CorrectionSignal{
		AsCorrection:       true,
		CorrectsRevisionNo: no,
		CorrectsKind:       kind,
	}, nil
}

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
// `mode` es el campo `as_correction` del 044 (ver EditMode). EditPlain es la
// conducta del 041, byte a byte.
//
// 🔴 EL ESTADO EN EL QUE QUEDA LA SOLICITUD, QUE ES LA PREGUNTA FINA DE T4.4. El
// enunciado dice que `correct` «vuelve a `pending_approval`». Aquí no se transiciona
// a ninguna parte, y no es un olvido: **la solicitud ya está en `pending_approval`**,
// porque es el ÚNICO estado desde el que este PUT escribe (EditableStatus, arriba).
// Los tres desenlaces posibles, dichos enteros:
//
//   - `pending_approval` ⇒ la corrección se aplica y la solicitud SIGUE ahí. La
//     «vuelta» ya es cierta por construcción. Llamar a SetStatus sería pedir
//     `pending_approval → pending_approval`, que CanTransition rechaza de plano
//     (`from == to` no es una transición, status.go) — y un 422 por hacer bien lo
//     que se pidió.
//   - `needs_info` ⇒ 422 `not_editable`, con o sin el campo. Es lo mismo que
//     responde hoy y no se toca: para corregir un pedido al que se le pidió
//     información, el dueño lo devuelve a `pending_approval` por el selector de
//     estado —esa transición SÍ es legal (status.go)— y entonces corrige.
//   - cualquier otro ⇒ 422 `not_editable`, exactamente como el 041.
//
// Ampliar los estados editables porque venga `as_correction` habría sido inventar
// una transición nueva por la puerta de atrás y cambiarle las precondiciones a una
// ruta del 041 en producción. No se hace: T4.4 añade un campo, no una máquina de
// estados.
//
// EL EMPUJE AL CRM cuelga de que NAZCA una revisión (T4.10 §2: «toda revisión
// posterior al cierre re-empuja»), NO del modo: un PUT del 041 sin el campo cambia
// las líneas y el total del pedido, y un CRM que no se entere se queda con un
// documento que ya no es verdad. Ver PushRevisionToCRM.
//
// Errores: *InvalidItemsError / *TooManyItemsError (líneas mal formadas),
// ErrNotFound (no es del tenant), *NotEditableError (la solicitud no está en
// `pending_approval`) y ErrConflict (alguien la movió entre la lectura y la
// escritura).
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
	return detail, nil
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
// `signal` es la señal few-shot cuando la edición se declaró corrección del 044, y
// viene vacía en el camino del 041 (ver señalDeCorrección).
func correctedRevision(intakeID string, total float64, items []Item, signal CorrectionSignal) (Revision, error) {
	payload, err := CorrectedRevisionPayload(total, revisionLinesOf(items), signal)
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
