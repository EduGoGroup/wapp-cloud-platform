package publicapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/catalogimport"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
	flowstore "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// Modos del import (D-041.6). validate no escribe NADA; apply re-valida y escribe.
const (
	importModeValidate = "validate"
	importModeApply    = "apply"
)

// defaultCatalogRef es la ref de tenant_content a la que va el catálogo cuando el
// llamante no dice otra. No es un valor cualquiera: es el que usan los flujos del
// e2e y el que tres tests del motor dan por hecho, así que un default distinto
// dejaría el import escribiendo en una ref que nadie lee.
const defaultCatalogRef = "catalogo"

// CatalogVersionWriter es el puerto MÍNIMO del versionado que el import consume:
// archivar el contenido vigente y escribir el nuevo en un solo acto. Lo satisface
// *store.PostgresRepository (y el MemoryRepository en tests). Acotado al tenant
// (INV-8): el tenant sale del token, jamás del cuerpo.
//
// Va SEPARADO de TenantContentStore aunque el mismo objeto satisfaga los dos: el
// PUT genérico de tenant-content no versiona (D-041.8) y no tiene por qué exigir
// un método que no usa.
type CatalogVersionWriter interface {
	ReplaceTenantContentVersioned(ctx context.Context, tenantID, ref string, blob []byte, source string) (archived int, err error)
}

// catalogImportResponse es la respuesta de las dos modalidades. Se responde el
// MISMO objeto en validate y en apply —con Applied como única diferencia
// semántica— para que la pantalla pinte el diff con un solo camino de código.
type catalogImportResponse struct {
	Mode string `json:"mode"`
	Ref  string `json:"ref"`
	// Applied dice si el catálogo se escribió de verdad. En validate es SIEMPRE
	// false: es la garantía de que mirar no cambia nada.
	Applied bool `json:"applied"`
	// Items es cuántos artículos trae el documento subido. Es la cifra con la que
	// el operador reconoce que subió el archivo que quería antes de leer el diff.
	Items int                `json:"items"`
	Diff  catalogimport.Diff `json:"diff"`
	// ArchivedVersion es el número con el que quedó guardado el catálogo ANTERIOR.
	// Se omite cuando no se archivó nada: en validate (no se escribe) y en el
	// primer import de una ref (no había nada que archivar).
	ArchivedVersion int `json:"archived_version,omitempty"`
	// Document es el documento LEÍDO Y NORMALIZADO, listo para volver a enviarse tal
	// cual a POST /api/v1/catalog/import. Lo rellena el camino TABULAR y no el JSON:
	// quien sube un JSON ya lo tiene, y devolvérselo duplicaría el peso de cada
	// respuesta para no decirle nada que no supiera.
	//
	// EXISTE PARA QUE LA CONFIRMACIÓN EN DOS PASOS SIGA SIENDO FIEL. La pantalla del
	// import enseña el diff del validate y luego pide confirmación; para garantizar
	// que se aplica EXACTAMENTE lo que se enseñó, el paso 2 tiene que llevar consigo
	// el documento. Con un JSON eso es texto y cabe en un campo oculto; con un .xlsx
	// no, porque es binario. Sin esto, las salidas serían pedir el archivo otra vez
	// (y entonces el operador puede subir otro distinto del que confirmó), inflarlo a
	// base64 contra dos techos de bytes, o guardar el archivo entre pasos, que es
	// meterle estado a un BFF que no lo tiene.
	//
	// De regalo, es la traducción de la planilla a JSON: quien empezó en Excel se
	// lleva el contrato de su propio catálogo en vez de quedarse encerrado en la hoja.
	Document *catalogimport.CatalogImport `json:"document,omitempty"`
}

// catalogImportErrors es el cuerpo del 400 por documento inválido: el código
// estable que la pantalla distingue, más TODOS los defectos con su ubicación
// (T3.1). Es el motivo de que el 400 no use writeError: una lista de problemas
// accionables no cabe en un `{"error":"..."}`.
type catalogImportErrors struct {
	Error  string                           `json:"error"`
	Errors []catalogimport.ImportFieldError `json:"errors"`
}

// catalogImportHandler devuelve el handler de
// POST /api/v1/catalog/import?mode=validate|apply&ref=<ref> (D-041.6): recibe el
// documento de catálogo como JSON CRUDO en el cuerpo (el JSON es portátil, INV-05:
// no lleva tenant ni ref) y, según el modo, enseña qué cambiaría o lo aplica.
//
// EL MODO POR DEFECTO ES validate, y es una decisión de seguridad, no de
// comodidad: quien olvide el parámetro ve el diff, no se encuentra el catálogo
// reemplazado. Un modo desconocido es 400 —no se adivina— porque "aply" tecleado
// a las prisas no puede degradar a "no hagas nada" en silencio ni, mucho menos,
// a "escribe".
//
// APPLY RE-VALIDA STATELESS. No hay ticket, ni sesión, ni "confirma lo que
// validaste": el apply vuelve a leer, a validar y a diffear el documento que le
// llega. Así el estado del servidor no depende de una llamada anterior y dos
// pantallas abiertas no pueden confirmar la validación de la otra.
//
// El diff se calcula SIEMPRE (también en apply) porque es lo que se responde: la
// pantalla enseña lo mismo que confirmó. Respuestas:
//
//   - 200 con {mode, ref, applied, items, diff, archived_version?}.
//   - 400 con {error:"validation_failed", errors:[…]} si el documento no valida;
//     400 con {error} si el modo es desconocido.
//   - 401 sin identidad; 403 sin la feature catalog_import (lo corta el gate,
//     antes de este handler); 413 si el documento excede el techo de bytes;
//     500 en fallo del store.
func catalogImportHandler(cs TenantContentStore, vw CatalogVersionWriter, limits catalogimport.Limits) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target, ok := catalogImportTargetFrom(w, r, cs, vw, flowstore.VersionSourceImportJSON)
		if !ok {
			return
		}
		doc, code, errBody := decodeImportBody(r, limits)
		if errBody != nil {
			writeJSON(w, code, errBody)
			return
		}
		finishCatalogImport(w, r, cs, vw, target, doc)
	})
}

// catalogImportTarget es el destino resuelto de un import: de quién, a dónde y con
// qué procedencia se escribe. El tenant sale SIEMPRE del token (INV-8); la ref y el
// modo, del query; la procedencia la fija el camino (JSON o planilla), no el
// llamante — que un cliente pudiera declarar de dónde vino su catálogo convertiría
// la columna `source` de las versiones en una etiqueta que no significa nada.
type catalogImportTarget struct {
	tenantID string
	mode     string
	ref      string
	source   string
	// echoDocument pide que la respuesta lleve el documento ya normalizado (ver
	// catalogImportResponse.Document). Lo enciende el camino que lo necesita —la
	// planilla—, no el modo ni el llamante: si dependiera de un parámetro de la URL,
	// el peso de la respuesta lo decidiría quien llama.
	echoDocument bool
}

// catalogImportTargetFrom resuelve identidad, dependencias y query de un import, y
// responde ÉL MISMO cuando algo falta (ok=false ⇒ la respuesta ya está escrita).
//
// Los dos caminos del import comparten este preámbulo ENTERO, y por eso está aquí y
// no copiado en cada handler: escrito dos veces, bastaría con que un día uno de los
// dos dejara de comprobar el tenant para que un import escribiera donde no debe.
func catalogImportTargetFrom(w http.ResponseWriter, r *http.Request,
	cs TenantContentStore, vw CatalogVersionWriter, source string) (catalogImportTarget, bool) {
	id, ok := httpapi.IdentityFromContext(r.Context())
	if !ok || id.TenantID == "" {
		writeError(w, http.StatusUnauthorized, "autenticación requerida")
		return catalogImportTarget{}, false
	}
	if cs == nil || vw == nil {
		writeError(w, http.StatusInternalServerError, "store de contenido no configurado")
		return catalogImportTarget{}, false
	}
	mode, ref, msg := importParams(r)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return catalogImportTarget{}, false
	}
	return catalogImportTarget{tenantID: id.TenantID, mode: mode, ref: ref, source: source}, true
}

// finishCatalogImport es el tramo COMÚN de los dos imports, y empieza donde acaba la
// diferencia entre ellos: con el documento ya validado. Calcula el diff contra el
// catálogo vigente y, en apply, archiva la versión anterior y escribe la nueva.
//
// El diff se calcula SIEMPRE (también en apply) porque es lo que se responde: la
// pantalla enseña lo mismo que se confirmó. Que un JSON y una planilla con el mismo
// catálogo produzcan el mismo diff, la misma versión y el mismo blob no es una
// coincidencia afortunada: es que a partir de aquí es literalmente el mismo código.
func finishCatalogImport(w http.ResponseWriter, r *http.Request,
	cs TenantContentStore, vw CatalogVersionWriter, t catalogImportTarget, doc catalogimport.CatalogImport) {
	current, warning, err := currentCatalog(r.Context(), cs, t.tenantID, t.ref)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "no se pudo leer el catálogo vigente")
		return
	}
	diff := catalogimport.DiffCatalog(current, doc.Catalog)
	if warning != "" {
		diff.CurrentWarnings = append(diff.CurrentWarnings, warning)
	}
	resp := catalogImportResponse{Mode: t.mode, Ref: t.ref, Items: countItems(doc), Diff: diff}
	if t.echoDocument {
		// También en apply, y no solo en validate: la respuesta de este endpoint es
		// UN objeto con Applied como única diferencia semántica (ver
		// catalogImportResponse), y un campo que aparece y desaparece según el modo
		// obligaría a la pantalla a tener dos formas de leer lo mismo. Devolverlo en
		// apply además le da su JSON a quien aplica la planilla de una sola vez.
		resp.Document = &doc
	}
	if t.mode == importModeValidate {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// El blob que se escribe es doc.Catalog serializado TAL CUAL: misma forma v2
	// que consume el motor, sin traducción intermedia (D-041.5). Lo que queda
	// guardado es el documento ya normalizado por el validador, no los bytes que
	// llegaron: sin `format`, sin `source` y sin campos ajenos.
	blob, err := json.Marshal(doc.Catalog)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "no se pudo serializar el catálogo")
		return
	}
	archived, err := vw.ReplaceTenantContentVersioned(r.Context(), t.tenantID, t.ref, blob, t.source)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "no se pudo aplicar el catálogo")
		return
	}
	resp.Applied = true
	resp.ArchivedVersion = archived
	writeJSON(w, http.StatusOK, resp)
}

// importParams resuelve el modo y la ref del query string. Devuelve el mensaje de
// error (vacío = todo bien) en vez de escribir la respuesta, para que el handler
// conserve un solo punto de salida por caso y no infle su complejidad.
func importParams(r *http.Request) (mode, ref, msg string) {
	mode = r.URL.Query().Get("mode")
	if mode == "" {
		mode = importModeValidate
	}
	if mode != importModeValidate && mode != importModeApply {
		return "", "", "mode debe ser " + importModeValidate + " o " + importModeApply
	}
	ref = r.URL.Query().Get("ref")
	if ref == "" {
		ref = defaultCatalogRef
	}
	return mode, ref, ""
}

// decodeImportBody lee el documento con el techo de bytes aplicado ANTES de
// deserializar y lo valida. Devuelve el documento tipado y, si algo falla, el
// status + el cuerpo de error ya armado (nil = todo bien). El cuerpo es `any`
// porque los dos fallos posibles tienen forma distinta: el techo de bytes
// responde un error simple y el documento inválido, la lista completa de
// defectos.
func decodeImportBody(r *http.Request, limits catalogimport.Limits) (catalogimport.CatalogImport, int, any) {
	raw, err := catalogimport.ReadLimited(r.Body, limits)
	if err != nil {
		if errors.Is(err, catalogimport.ErrDocumentTooLarge) {
			return catalogimport.CatalogImport{}, http.StatusRequestEntityTooLarge,
				map[string]string{"error": "el documento excede el tamaño máximo"}
		}
		return catalogimport.CatalogImport{}, http.StatusBadRequest,
			map[string]string{"error": "no se pudo leer el cuerpo"}
	}
	doc, verr := catalogimport.Validate(raw, limits)
	if verr != nil {
		return catalogimport.CatalogImport{}, http.StatusBadRequest,
			catalogImportErrors{Error: "validation_failed", Errors: verr.Errors}
	}
	return doc, 0, nil
}

// currentCatalog resuelve el lado VIEJO del diff: el catálogo vigente del tenant,
// parseado con el MISMO parser que usa el motor (D-041.7).
//
// Tres desenlaces, y los tres importan:
//
//   - No hay contenido en esa ref ⇒ catálogo vacío y sin aviso. Es el primer
//     import y todo el documento sale como `added`: correcto y esperable, no un
//     fallo (hueco #7 de la ola). Jamás un 500.
//   - Hay contenido pero no se puede interpretar como catálogo (blob de otra
//     cosa, sin categorías, JSON que no es un objeto) ⇒ catálogo vacío MÁS UN
//     AVISO. El diff dirá "todo nuevo" y eso, sin explicación, se leería como
//     "no pierdo nada": el aviso es lo que impide esa lectura.
//   - El store falla de verdad ⇒ error. Aquí NO se degrada a "catálogo vacío":
//     enseñar "todo nuevo, nada se pierde" porque la BD no respondió sería
//     empujar al operador a confirmar un reemplazo a ciegas.
func currentCatalog(ctx context.Context, cs TenantContentStore, tenantID, ref string) (cart.Catalog, string, error) {
	blob, err := cs.GetTenantContent(ctx, tenantID, ref)
	if err != nil {
		if errors.Is(err, flowstore.ErrTenantContentNotFound) {
			return cart.Catalog{}, "", nil
		}
		return cart.Catalog{}, "", err
	}
	cat, ok := parseCatalogBlob(blob)
	if !ok {
		return cart.Catalog{}, unreadableCurrentWarning, nil
	}
	return cat, "", nil
}

// parseCatalogBlob interpreta el blob vigente como catálogo con el MISMO parser
// del motor. Devuelve ok=false cuando no hay nada comparable: el blob no es un
// objeto JSON, o lo es pero no tiene forma de catálogo.
//
// NO devuelve el error del parser, y no es descuido: aquí "ilegible" no es un
// fallo del import sino una CARACTERÍSTICA del lado viejo, y lo único que el
// llamante puede hacer con ese detalle es avisar. Propagarlo obligaría a decidir
// más arriba entre un error que sí corta (el store caído) y otro que no, con los
// dos del mismo tipo: exactamente la confusión que este booleano elimina.
func parseCatalogBlob(blob []byte) (cart.Catalog, bool) {
	var raw map[string]any
	if err := json.Unmarshal(blob, &raw); err != nil || raw == nil {
		return cart.Catalog{}, false
	}
	cat, err := cart.ParseCatalog(model.Content{Raw: raw})
	if err != nil {
		return cart.Catalog{}, false
	}
	return cat, true
}

// unreadableCurrentWarning es el aviso que acompaña a un diff calculado contra
// nada porque el contenido vigente de esa ref no es un catálogo interpretable.
const unreadableCurrentWarning = "catálogo vigente: la ref tiene contenido, pero no se pudo interpretar como catálogo; " +
	"la comparación se hizo contra un catálogo vacío y por eso todo aparece como nuevo"

// countItems suma los artículos del documento (todas las categorías).
func countItems(doc catalogimport.CatalogImport) int {
	n := 0
	for _, c := range doc.Catalog.Categories {
		n += len(c.Items)
	}
	return n
}
