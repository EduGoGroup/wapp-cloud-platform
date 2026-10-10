// Porta internal/publicapi/catalogimport.go @ 724f3035 (catalogImportResponse,
// catalogImportTarget, finishCatalogImport, currentCatalog, parseCatalogBlob,
// unreadableCurrentWarning y countItems).
//
// catalogimport_apply.go — trozo de catalogimport.go (05 E-13): EL TRAMO COMÚN de las dos
// puertas del import de catálogo (I14, el JSON, e I15, la planilla), que empieza donde acaba la
// diferencia entre ellas: con el documento ya validado. Aquí viven la respuesta, el diff contra
// el catálogo vigente y el apply con su versionado. Solo declaraciones movidas; no exporta nada,
// y sus promesas son las del contrato de MountCatalogImport.

package apipublica

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo/catalogimport"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

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

// catalogImportFinish (finishCatalogImport en la cara vieja) es el tramo COMÚN de los dos
// imports, y empieza donde acaba la diferencia entre ellos: con el documento ya validado.
// Calcula el diff contra el catálogo vigente y, en apply, archiva la versión anterior y escribe
// la nueva.
//
// El diff se calcula SIEMPRE (también en apply) porque es lo que se responde: la
// pantalla enseña lo mismo que se confirmó. Que un JSON y una planilla con el mismo
// catálogo produzcan el mismo diff, la misma versión y el mismo blob no es una
// coincidencia afortunada: es que a partir de aquí es literalmente el mismo código.
func catalogImportFinish(w http.ResponseWriter, r *http.Request,
	cs CatalogImportContentReader, vw CatalogImportVersionWriter, t catalogImportTarget, doc catalogimport.CatalogImport) {
	current, warning, err := catalogImportCurrentCatalog(r.Context(), cs, t.tenantID, t.ref)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "no se pudo leer el catálogo vigente")
		return
	}
	diff := catalogimport.DiffCatalog(current, doc.Catalog)
	if warning != "" {
		diff.CurrentWarnings = append(diff.CurrentWarnings, warning)
	}
	resp := catalogImportResponse{Mode: t.mode, Ref: t.ref, Items: catalogImportCountItems(doc), Diff: diff}
	if t.echoDocument {
		// También en apply, y no solo en validate: la respuesta de este endpoint es
		// UN objeto con Applied como única diferencia semántica (ver
		// catalogImportResponse), y un campo que aparece y desaparece según el modo
		// obligaría a la pantalla a tener dos formas de leer lo mismo. Devolverlo en
		// apply además le da su JSON a quien aplica la planilla de una sola vez.
		resp.Document = &doc
	}
	if t.mode == catalogImportModeValidate {
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

// catalogImportCurrentCatalog (currentCatalog en la cara vieja) resuelve el lado VIEJO del
// diff: el catálogo vigente del tenant, parseado con el MISMO parser que usa el motor
// (D-041.7).
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
func catalogImportCurrentCatalog(ctx context.Context, cs CatalogImportContentReader, tenantID, ref string) (catalogo.Catalog, string, error) {
	blob, err := cs.GetTenantContent(ctx, tenantID, ref)
	if err != nil {
		if errors.Is(err, store.ErrTenantContentNotFound) {
			return catalogo.Catalog{}, "", nil
		}
		return catalogo.Catalog{}, "", err
	}
	cat, ok := catalogImportParseBlob(blob)
	if !ok {
		return catalogo.Catalog{}, catalogImportUnreadableCurrentWarning, nil
	}
	return cat, "", nil
}

// catalogImportParseBlob (parseCatalogBlob en la cara vieja) interpreta el blob vigente como
// catálogo con el MISMO parser del motor. Devuelve ok=false cuando no hay nada comparable: el
// blob no es un objeto JSON, o lo es pero no tiene forma de catálogo.
//
// NO devuelve el error del parser, y no es descuido: aquí "ilegible" no es un
// fallo del import sino una CARACTERÍSTICA del lado viejo, y lo único que el
// llamante puede hacer con ese detalle es avisar. Propagarlo obligaría a decidir
// más arriba entre un error que sí corta (el store caído) y otro que no, con los
// dos del mismo tipo: exactamente la confusión que este booleano elimina.
func catalogImportParseBlob(blob []byte) (catalogo.Catalog, bool) {
	var raw map[string]any
	if err := json.Unmarshal(blob, &raw); err != nil || raw == nil {
		return catalogo.Catalog{}, false
	}
	cat, err := catalogo.ParseCatalog(model.Content{Raw: raw})
	if err != nil {
		return catalogo.Catalog{}, false
	}
	return cat, true
}

// catalogImportUnreadableCurrentWarning (unreadableCurrentWarning en la cara vieja) es el aviso
// que acompaña a un diff calculado contra nada porque el contenido vigente de esa ref no es un
// catálogo interpretable.
const catalogImportUnreadableCurrentWarning = "catálogo vigente: la ref tiene contenido, pero no se pudo interpretar como catálogo; " +
	"la comparación se hizo contra un catálogo vacío y por eso todo aparece como nuevo"

// catalogImportCountItems (countItems en la cara vieja) suma los artículos del documento (todas
// las categorías).
func catalogImportCountItems(doc catalogimport.CatalogImport) int {
	n := 0
	for _, c := range doc.Catalog.Categories {
		n += len(c.Items)
	}
	return n
}
