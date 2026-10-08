// Porta internal/publicapi/intakes.go @ ed60c24 (1073 líneas: la edición manual de líneas,
// líneas 522-730).
//
// intakes_items.go — G4, LA EDICIÓN MANUAL DE LAS LÍNEAS DE UN PRESUPUESTO (PUT
// /api/v1/intakes/{id}/items; Plan 041 · T4.10, REQ-36 / D-041.26), que es también la acción
// «Corregir» del Plan 044 (T4.4, D-044.48 §1). Es un trozo de intakes.go partido por tema
// (05 E-13).
//
// 🔤 El saneo del texto libre lo hacía la cara vieja con cart.SanitizeNote y cart.MaxNoteRunes:
// las dos viven ahora en internal/modulos/solicitudes/intakes (note.go, D-F6-4), con la misma
// regla y el mismo límite.
//
// No exporta nada y nació con el verde (05 E-4, P6): sus promesas están en el contrato de
// MountIntakes y las prueba intakes_items_test.go.

package apipublica

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// intakeEditItemDTO (editIntakeItemDTO en la cara vieja) es UNA línea tal como la manda el dueño
// al editar a mano (T4.10). Es la MISMA forma que devuelve el detalle (intakeItemDTO) menos lo
// que no le toca poner: `added_at` lo fecha la BD.
//
// `unit_price` viaja en el cuerpo y NO se resuelve contra el catálogo, y esa es la
// decisión de fondo de esta puerta: la edición manual existe precisamente para
// cobrar lo que el catálogo NO tiene todavía (la escena del queso extra,
// D-041.26 §e). Resolver el precio contra el catálogo dejaría al dueño sin poder
// hacer lo único que esta ruta existe para hacer, y además exigiría adivinar CUÁL
// de las refs de contenido del tenant es «su» catálogo. Quien quiera el precio del
// catálogo lo lee de ahí y lo manda: es su UI la que tiene el catálogo delante.
type intakeEditItemDTO struct {
	SKU           string  `json:"sku"`
	Label         string  `json:"label"`
	Customization string  `json:"customization"`
	Qty           int     `json:"qty"`
	UnitPrice     float64 `json:"unit_price"`
}

// intakeEditItemsRequest (editIntakeItemsRequest en la cara vieja) es el cuerpo de PUT
// /api/v1/intakes/{id}/items: el conjunto COMPLETO de líneas de cliente que debe quedar.
//
// `Items` es un puntero para distinguir «no mandaste la clave» (cuerpo mal formado
// ⇒ 400) de «mandaste la lista vacía» (quitar todas las líneas ⇒ se aplica). La
// diferencia importa: sin ella, un cuerpo `{}` por un fallo de la UI vaciaría el
// presupuesto en silencio.
//
// `as_correction` es la ACCIÓN «Corregir» del 044 (T4.4, D-044.48 §1): el mismo PUT
// con un campo más. Es OPCIONAL y su ausencia significa exactamente lo que significaba
// antes de que existiera —la edición manual del 041, sin conducta nueva—, así que un
// cliente del 041 que nunca lo mande no ve cambiar nada. No es puntero porque aquí las
// dos ausencias (clave que falta y `false` explícito) sí quieren decir lo mismo.
type intakeEditItemsRequest struct {
	Items        *[]intakeEditItemDTO `json:"items"`
	AsCorrection bool                 `json:"as_correction"`
}

// intakeEditModeOf (editModeDe en la cara vieja) traduce el campo del wire al modo del dominio.
// Está aparte —y no en línea en el handler— para que el único sitio donde se decide «esto es una
// corrección» sea uno, y para que el test que fija la conducta pueda nombrarlo.
func intakeEditModeOf(asCorrection bool) intakes.EditMode {
	if asCorrection {
		return intakes.EditAsCorrection
	}
	return intakes.EditPlain
}

// intakeInvalidItemsResponse (invalidItemsResponse en la cara vieja) es el cuerpo del 400 por
// líneas mal formadas: TODOS los defectos de una vez, con su posición y su campo (mismo criterio
// que el validador del import). Quien llena diez líneas no puede descubrir sus errores de uno en
// uno.
type intakeInvalidItemsResponse struct {
	Error  string               `json:"error"`
	Errors []intakes.LineDefect `json:"errors"`
}

// intakeNotEditableResponse (notEditableResponse en la cara vieja) es el cuerpo del 422 de una
// edición sobre una solicitud que no está por aprobar: dónde está y desde dónde SÍ se edita, que
// es lo que el llamante necesita para arreglarlo (mover a `pending_approval`, D-041.26) sin
// adivinar.
type intakeNotEditableResponse struct {
	Error      string   `json:"error"`
	Status     string   `json:"status"`
	EditableIn []string `json:"editable_in"`
}

// intakePutItemsHandler (putIntakeItemsHandler en la cara vieja) sirve PUT
// /api/v1/intakes/{id}/items: la EDICIÓN MANUAL de las líneas de un presupuesto por su dueño
// (REQ-36 / D-041.26), sin LLM de por medio. Responde el detalle completo —con la revisión
// `corrected` recién escrita— para que la consola repinte sin un segundo GET.
//
// PUT y no POST: el cuerpo es el conjunto COMPLETO de líneas de cliente que debe
// quedar, así que mandar dos veces el mismo cuerpo deja la solicitud igual. Lo que
// NO es idempotente es la AUDITORÍA: cada PUT deja su revisión, porque dos
// ediciones son dos actos del dueño aunque el resultado coincida (misma regla que
// InsertRevision).
//
// Es TAMBIÉN la acción «Corregir» del Plan 044 (T4.4), y no una ruta aparte: con
// `"as_correction": true` la misma escritura deja además la señal few-shot del
// D-044.11 en la revisión. La decisión y su porqué están en D-044.48 §1 y en
// intakes/edit.go; lo que importa aquí es que el campo es opcional y que sin él este
// handler hace lo mismo que antes de que existiera.
//
// Códigos: 200 con el detalle; 400 si el cuerpo o las líneas están mal; 404 si la
// solicitud no es del tenant (nunca 403: confirmaría que existe); 422 si no está en
// `pending_approval`; 409 si alguien la movió entre la lectura y la escritura.
func intakePutItemsHandler(svc IntakeService, feats entitlements.Resolver, now func() time.Time) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}

		var req intakeEditItemsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "cuerpo JSON inválido")
			return
		}
		if req.Items == nil {
			writeError(w, http.StatusBadRequest, "items es obligatorio (manda [] para dejar la solicitud sin líneas)")
			return
		}

		items, defects := intakeDecodeEditItems(*req.Items)
		if len(defects) > 0 {
			writeJSON(w, http.StatusBadRequest, intakeInvalidItemsResponse{
				Error: "invalid_items", Errors: intakeMergeItemDefects(defects, items),
			})
			return
		}

		detail, err := svc.ReplaceItems(r.Context(), id.TenantID, r.PathValue("id"), items, intakeEditModeOf(req.AsCorrection))
		if err != nil {
			intakeWriteEditItemsError(w, err)
			return
		}
		// Por el MISMO camino que el GET, y no por un writeJSON suelto: el cuerpo es
		// el mismo, así que el gate por campo tiene que ser el mismo. Repintar la
		// consola con lo que devuelve el PUT no puede enseñar lo que el GET tapa.
		intakeWriteDetail(r.Context(), w, feats, id.TenantID, detail, now())
	})
}

// intakeDecodeEditItems (decodeEditItems en la cara vieja) traduce las líneas del wire a líneas
// de dominio SANEANDO su texto libre por la MISMA puerta que el carrito: intakes.SanitizeNote
// (D-041.19). No se copia la regla —se llama—, porque `customization` es una columna con UN
// contrato y ya tiene dos productores (el cart y el pipeline del 044); éste es el tercero.
//
// La etiqueta pasa por el mismo saneo que la personalización, y no por parecido:
// las dos acaban en una CELDA del CSV y en una LÍNEA de la comanda, así que un
// salto de línea o un carácter invisible rompen exactamente lo mismo.
//
// El único defecto que produce el saneo es el LARGO (SanitizeNote no trunca a
// propósito: recortar «…y sin maní» pierde justo el alérgeno). El resto de la
// validación —sku, cantidad, precio— es del dominio, que no se fía de esta puerta.
func intakeDecodeEditItems(raw []intakeEditItemDTO) ([]intakes.Item, []intakes.LineDefect) {
	items := make([]intakes.Item, 0, len(raw))
	var defects []intakes.LineDefect

	for i, in := range raw {
		it := intakes.Item{SKU: strings.TrimSpace(in.SKU), Qty: in.Qty, UnitPrice: in.UnitPrice}

		label, err := intakes.SanitizeNote(in.Label)
		if err != nil {
			defects = append(defects, intakes.LineDefect{
				Index: i, Field: "label",
				Message: "la etiqueta pasa del máximo de " + strconv.Itoa(intakes.MaxNoteRunes) + " caracteres",
			})
		}
		custom, err := intakes.SanitizeNote(in.Customization)
		if err != nil {
			defects = append(defects, intakes.LineDefect{
				Index: i, Field: "customization",
				Message: "la personalización pasa del máximo de " + strconv.Itoa(intakes.MaxNoteRunes) + " caracteres",
			})
		}

		it.Label, it.Customization = label, custom
		items = append(items, it)
	}
	return items, defects
}

// intakeMergeItemDefects (mergeItemDefects en la cara vieja) añade a los defectos del saneo los
// que ve el dominio, ordenados por línea. Es lo que hace que un cuerpo con la etiqueta demasiado
// larga Y la cantidad en cero se conteste UNA vez con los dos problemas, en vez de mandar al
// llamante a descubrirlos por turnos.
func intakeMergeItemDefects(defects []intakes.LineDefect, items []intakes.Item) []intakes.LineDefect {
	var invalid *intakes.InvalidItemsError
	if err := intakes.ValidateEditableItems(items); errors.As(err, &invalid) {
		defects = append(defects, invalid.Defects...)
	}
	slices.SortStableFunc(defects, func(a, b intakes.LineDefect) int { return a.Index - b.Index })
	return defects
}

// intakeWriteEditItemsError (writeEditItemsError en la cara vieja) traduce el fallo del dominio
// al código y al cuerpo que le sirven a quien llama. La política de códigos vive aquí y no en el
// dominio: el dominio dice QUÉ pasó, el transporte decide cómo se cuenta.
func intakeWriteEditItemsError(w http.ResponseWriter, err error) {
	var (
		invalid     *intakes.InvalidItemsError
		tooMany     *intakes.TooManyItemsError
		notEditable *intakes.NotEditableError
	)
	switch {
	case errors.Is(err, intakes.ErrNotFound):
		writeError(w, http.StatusNotFound, "solicitud no encontrada")
	case errors.As(err, &invalid):
		writeJSON(w, http.StatusBadRequest, intakeInvalidItemsResponse{
			Error: "invalid_items", Errors: invalid.Defects,
		})
	case errors.As(err, &tooMany):
		writeError(w, http.StatusBadRequest,
			"la edición trae "+strconv.Itoa(tooMany.Count)+" líneas y el máximo es "+strconv.Itoa(tooMany.Max))
	case errors.As(err, &notEditable):
		writeJSON(w, http.StatusUnprocessableEntity, intakeNotEditableResponse{
			Error:      "not_editable",
			Status:     notEditable.Status,
			EditableIn: []string{intakes.EditableStatus},
		})
	case errors.Is(err, intakes.ErrConflict):
		writeError(w, http.StatusConflict, "la solicitud cambió de estado; recárgala y reintenta")
	default:
		writeError(w, http.StatusInternalServerError, "no se pudieron guardar las líneas de la solicitud")
	}
}
