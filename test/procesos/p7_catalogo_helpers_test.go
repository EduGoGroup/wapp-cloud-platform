//go:build integracion

package procesos

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// Las ayudas de P7: cómo se llama a las puertas del catálogo (JSON crudo y multipart, con el
// reintento del 429), cómo se arma un documento o una planilla, y la marca de estado con la que se
// afirma que una llamada NO escribió nada.

const (
	// Las puertas de P7.
	p7RouteImport   = "/api/v1/catalog/import"
	p7RouteTabular  = "/api/v1/catalog/import/tabular"
	p7RouteTemplate = "/api/v1/catalog/import/template"
	p7RoutePrompt   = "/api/v1/catalog/import/prompt"
	p7RouteContent  = "/api/v1/tenant-content"
	p7RouteUpload   = "/api/v1/media/upload-url"

	// Las dos acciones que auditan las escrituras de P7 (protect, internal/publicapi/publicapi.go):
	// el import —por sus dos puertas— y el PUT genérico comparten `content.write`.
	p7ActContent = "content.write"
	p7ActMedia   = "media.upload"

	// p7FormField es el campo del formulario multipart donde viaja la planilla.
	p7FormField = "file"
	// p7BOM es la marca con la que la plantilla CSV empieza (para Excel en Windows).
	p7BOM = "\xef\xbb\xbf"

	// p7MaxItems es el tope de artículos por importación: el arnés no pone WAPP_IMPORT_MAX_ITEMS, así
	// que manda el valor por defecto. p7MaxBytes es el techo de bytes del documento
	// (WAPP_TENANT_CONTENT_MAX_BYTES, tampoco puesta): 1 MiB.
	p7MaxItems = 500
	p7MaxBytes = 1 << 20

	// p7ValidationFailed es el código estable del 400 por documento o planilla inválidos.
	p7ValidationFailed = "validation_failed"
)

// p7Reply es una respuesta con sus cabeceras: la plantilla se afirma también por ellas.
type p7Reply struct {
	respuesta
	Header http.Header
}

// p7FieldError es un defecto de la lista del 400: en el import JSON va ubicado por índices de
// categoría y artículo (base 0; nil en un defecto de cabecera); en la planilla, por fila.
type p7FieldError struct {
	Row           int    `json:"row"`
	CategoryIndex *int   `json:"category_index"`
	ItemIndex     *int   `json:"item_index"`
	Field         string `json:"field"`
	Reason        string `json:"reason"`
}

// where resume la ubicación y el campo del defecto: «c0/i1/sku» en el import JSON («-» donde no hay
// índice) o «r3/precio» en la planilla.
func (e p7FieldError) where() string {
	if e.CategoryIndex == nil && e.ItemIndex == nil {
		return fmt.Sprintf("r%d/%s", e.Row, e.Field)
	}
	idx := func(p *int) string {
		if p == nil {
			return "-"
		}
		return fmt.Sprint(*p)
	}
	return fmt.Sprintf("c%s/i%s/%s", idx(e.CategoryIndex), idx(e.ItemIndex), e.Field)
}

// p7Result es la respuesta 200 de las dos puertas del import, y el cuerpo del 400 (Error y Errors).
type p7Result struct {
	Mode            string `json:"mode"`
	Ref             string `json:"ref"`
	Applied         bool   `json:"applied"`
	Items           int    `json:"items"`
	ArchivedVersion int    `json:"archived_version"`
	Diff            struct {
		PriceChanges []struct {
			SKU      string  `json:"sku"`
			Label    string  `json:"label"`
			OldPrice float64 `json:"old_price"`
			NewPrice float64 `json:"new_price"`
		} `json:"price_changes"`
		Added []struct {
			SKU   string `json:"sku"`
			Label string `json:"label"`
		} `json:"added"`
		Removed []struct {
			SKU   string `json:"sku"`
			Label string `json:"label"`
		} `json:"removed"`
		ChangedDetails  []string `json:"changed_details"`
		Unchanged       int      `json:"unchanged"`
		CurrentWarnings []string `json:"current_warnings"`
	} `json:"diff"`
	Document json.RawMessage `json:"document"`

	Error    string         `json:"error"`
	Feature  string         `json:"feature"`
	MaxBytes int64          `json:"max_bytes"`
	Errors   []p7FieldError `json:"errors"`
}

// diff resume el diff: «precios|altas|bajas|detalle|iguales», con los SKU en el orden de la respuesta.
func (r p7Result) diff() string {
	prices := make([]string, 0, len(r.Diff.PriceChanges))
	for _, p := range r.Diff.PriceChanges {
		prices = append(prices, fmt.Sprintf("%s:%v>%v", p.SKU, p.OldPrice, p.NewPrice))
	}
	added := make([]string, 0, len(r.Diff.Added))
	for _, a := range r.Diff.Added {
		added = append(added, a.SKU)
	}
	removed := make([]string, 0, len(r.Diff.Removed))
	for _, a := range r.Diff.Removed {
		removed = append(removed, a.SKU)
	}
	return fmt.Sprintf("%s|%s|%s|%s|%d", strings.Join(prices, ","), strings.Join(added, ","),
		strings.Join(removed, ","), strings.Join(r.Diff.ChangedDetails, ","), r.Diff.Unchanged)
}

// wheres resume la lista de defectos: la ubicación de cada uno, en el orden de la respuesta.
func (r p7Result) wheres() string {
	out := make([]string, 0, len(r.Errors))
	for _, e := range r.Errors {
		out = append(out, e.where())
	}
	return strings.Join(out, " ")
}

// reasons junta los motivos de la lista de defectos, uno por línea.
func (r p7Result) reasons() string {
	out := make([]string, 0, len(r.Errors))
	for _, e := range r.Errors {
		out = append(out, e.Reason)
	}
	return strings.Join(out, "\n")
}

// p7Decode decodifica el cuerpo de una respuesta del import (200 o 400). Falla (t.Fatalf) si no es JSON.
func p7Decode(t *testing.T, r respuesta) p7Result {
	t.Helper()
	var out p7Result
	r.JSON(t, &out)
	return out
}

// send hace una petición con el cuerpo CRUDO (los bytes tal cual, sin pasar por json.Marshal, que
// compactaría un documento y rechazaría uno mal escrito) y el Content-Type dado, como c. Devuelve la
// respuesta con sus cabeceras. Si la ruta audita (action no vacío) apunta la fila de audit_events que
// tiene que dejar, en el mismo mapa que (*p9World).call. Un 429 se reintenta sondeando, igual que
// allí: lo corta el limitador por credencial antes del handler, sin efecto ni auditoría.
//
// Existe porque (*p9World).call solo sabe mandar valores que json.Marshal acepte: no sirve para un
// multipart ni para un JSON roto a propósito. Las llamadas con cuerpo JSON normal van por call.
func (w *p7World) send(t *testing.T, c p9Caller, action, method, path, contentType string, body []byte) p7Reply {
	t.Helper()
	var reply p7Reply
	var lastErr error
	defer func() {
		if reply.Codigo == 0 && lastErr != nil {
			t.Logf("%s %s: el último error de transporte fue: %v", method, path, lastErr)
		}
	}()
	edgeEsperar(t, edgeTopeFila, method+" "+path+" sin 429", func() bool {
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(t.Context(), method, c.client.base+path, reader)
		if err != nil {
			t.Fatalf("construir %s %s: %v", method, path, err)
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		if c.client.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.client.token)
		}
		resp, err := c.client.cliente.Do(req)
		if err != nil {
			// Un 429 a una petición de cuerpo grande se ve a veces así: el servidor contesta y
			// cierra sin leer el cuerpo, y el cliente muere escribiendo (broken pipe) antes de
			// leer la respuesta. Se reintenta igual que el 429; si no era eso, vence el tope.
			lastErr = err
			w.calls.throttled++
			return false
		}
		read, readErr := io.ReadAll(io.LimitReader(resp.Body, maxCuerpoRespuesta))
		if closeErr := resp.Body.Close(); closeErr != nil && readErr == nil {
			readErr = closeErr
		}
		if readErr != nil {
			t.Fatalf("leer la respuesta de %s %s: %v", method, path, readErr)
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			w.calls.throttled++
			return false
		}
		reply = p7Reply{respuesta: respuesta{Codigo: resp.StatusCode, Cuerpo: read}, Header: resp.Header}
		return true
	})
	if action != "" {
		result := "success"
		if reply.Codigo >= http.StatusBadRequest {
			result = "failure"
		}
		w.calls.audit[c.tenant+"|"+action+"|"+result]++
	}
	return reply
}

// p7Query arma el query string del import: el modo y la ref («» = no se manda el parámetro).
func p7Query(mode, ref string) string {
	q := url.Values{}
	if mode != "" {
		q.Set("mode", mode)
	}
	if ref != "" {
		q.Set("ref", ref)
	}
	if len(q) == 0 {
		return ""
	}
	return "?" + q.Encode()
}

// importJSON sube un documento (los bytes tal cual) por POST /api/v1/catalog/import como c y devuelve
// la respuesta, apuntando su fila de auditoría (la ruta audita toda petición que pasa del middleware).
func (w *p7World) importJSON(t *testing.T, c p9Caller, mode, ref string, doc []byte) respuesta {
	t.Helper()
	return w.send(t, c, p7ActContent, http.MethodPost, p7RouteImport+p7Query(mode, ref), "application/json", doc).respuesta
}

// importFile sube un archivo (CSV o XLSX) por POST /api/v1/catalog/import/tabular como c, en el campo
// «file» de un multipart/form-data, y devuelve la respuesta.
func (w *p7World) importFile(t *testing.T, c p9Caller, mode, ref string, file []byte) respuesta {
	t.Helper()
	body, contentType := p7Multipart(t, p7FormField, file)
	return w.send(t, c, p7ActContent, http.MethodPost, p7RouteTabular+p7Query(mode, ref), contentType, body).respuesta
}

// p7Multipart arma un multipart/form-data con UN archivo en el campo dado y devuelve el cuerpo y su
// Content-Type (con el delimitador). Falla (t.Fatalf) si no se puede armar.
func p7Multipart(t *testing.T, field string, file []byte) (body []byte, contentType string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile(field, "catalogo.csv")
	if err != nil {
		t.Fatalf("armar el multipart: %v", err)
	}
	if _, err = fw.Write(file); err != nil {
		t.Fatalf("escribir el archivo en el multipart: %v", err)
	}
	if err = mw.Close(); err != nil {
		t.Fatalf("cerrar el multipart: %v", err)
	}
	return buf.Bytes(), mw.FormDataContentType()
}

// p7CSV escribe una planilla como la guardaría una hoja de cálculo, con el separador de columnas dado.
// Falla (t.Fatalf) si no se puede escribir.
func p7CSV(t *testing.T, comma rune, rows [][]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	cw := csv.NewWriter(&buf)
	cw.Comma = comma
	if err := cw.WriteAll(rows); err != nil {
		t.Fatalf("escribir el CSV: %v", err)
	}
	return buf.Bytes()
}

// p7Columns es la cabecera canónica de la planilla, escrita a mano: el nombre y el ORDEN de las
// columnas son contrato (internal/catalogimport/template_test.go, TestTemplateSheet_CabeceraYFormaDeLaTabla).
func p7Columns() []string {
	return []string{"categoria", "subcategoria", "codigo", "sku", "nombre", "precio",
		"descripcion", "tags", "atributos", "variantes", "componentes"}
}

// p7Row arma una fila en el orden de p7Columns, rellenando con celdas vacías las que no se dan.
func p7Row(cells ...string) []string {
	row := make([]string, len(p7Columns()))
	copy(row, cells)
	return row
}

// p7Sheet arma una planilla CSV (separada por comas) con la cabecera canónica y las filas dadas.
func p7Sheet(t *testing.T, rows ...[]string) []byte {
	t.Helper()
	return p7CSV(t, ',', append([][]string{p7Columns()}, rows...))
}

// p7Doc envuelve una lista de categorías (JSON crudo, sin los corchetes) en el sobre del contrato.
func p7Doc(categories string) []byte {
	return []byte(`{"format":"wapp.catalog_import","version":1,"catalog":{"categories":[` + categories + `]}}`)
}

// p7Cat arma una categoría (JSON crudo) con su código, su nombre y sus artículos (JSON crudo cada uno).
func p7Cat(code, label string, items ...string) string {
	return `{"code":` + p7Quote(code) + `,"label":` + p7Quote(label) + `,"items":[` + strings.Join(items, ",") + `]}`
}

// p7Item arma un artículo simple (JSON crudo): código, sku, nombre y precio. El precio va TAL CUAL
// (es JSON crudo: `120`, `"120"`, `1e3`), para que la tabla adversaria pueda escribirlo mal.
func p7Item(code, sku, label, rawPrice string) string {
	return `{"code":` + p7Quote(code) + `,"sku":` + p7Quote(sku) + `,"label":` + p7Quote(label) + `,"price":` + rawPrice + `}`
}

// p7Quote devuelve s como cadena JSON, sin escapar lo que no hace falta (los caracteres no ASCII
// viajan en UTF-8, como los escribiría una persona).
func p7Quote(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return `""`
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

// p7Mark es la marca de estado del contenido de una empresa (R9.5.c): TODAS las columnas que un
// import puede tocar, de las dos tablas. De tenant_content, por ref, el contenido y sus dos instantes;
// de tenant_content_versions, por ref y versión, la procedencia, el contenido y su instante. Dos
// marcas iguales ⇒ la operación de en medio no escribió ni archivó nada.
func p7Mark(t *testing.T, sc *draftScene, tenant string) string {
	t.Helper()
	const content = `SELECT coalesce(string_agg(ref || '=' || md5(content::text) || '@' || created_at::text || '/' || updated_at::text, ';' ORDER BY ref), '')
		FROM public.tenant_content WHERE tenant_id = $1`
	const versions = `SELECT coalesce(string_agg(ref || '#' || version::text || '=' || source || ':' || md5(content::text) || '@' || created_at::text, ';' ORDER BY ref, version), '')
		FROM public.tenant_content_versions WHERE tenant_id = $1`
	return p9Scalar(t, sc.DB, content, tenant) + " || " + p9Scalar(t, sc.DB, versions, tenant)
}

// p7Content lee el contenido vigente de una ref de la empresa, como texto JSON («» si no hay).
func p7Content(t *testing.T, sc *draftScene, tenant, ref string) string {
	t.Helper()
	return p9Scalar(t, sc.DB, `SELECT content::text FROM public.tenant_content WHERE tenant_id = $1 AND ref = $2`, tenant, ref)
}

// p7Versions resume las versiones archivadas de una ref: «versión:procedencia», en orden.
func p7Versions(t *testing.T, sc *draftScene, tenant, ref string) string {
	t.Helper()
	return p9Scalar(t, sc.DB, `SELECT coalesce(string_agg(version::text || ':' || source, ',' ORDER BY version), '')
		FROM public.tenant_content_versions WHERE tenant_id = $1 AND ref = $2`, tenant, ref)
}

// p7Stored resume lo que quedó guardado en una ref: un renglón por artículo, en orden de documento,
// «código de categoría/código/sku/nombre/precio», con los espacios raros a la vista (%q).
func p7Stored(t *testing.T, sc *draftScene, tenant, ref string) string {
	t.Helper()
	const query = `SELECT coalesce(string_agg(to_jsonb(cat.v->>'code')::text || '/' || to_jsonb(i.v->>'code')::text || '/' || to_jsonb(i.v->>'sku')::text
			|| '/' || to_jsonb(i.v->>'label')::text || '/' || (i.v->>'price'), E'\n' ORDER BY cat.n, i.n), '')
		FROM public.tenant_content c, jsonb_array_elements(c.content->'categories') WITH ORDINALITY cat(v, n),
			jsonb_array_elements(cat.v->'items') WITH ORDINALITY i(v, n)
		WHERE c.tenant_id = $1 AND c.ref = $2`
	return p9Scalar(t, sc.DB, query, tenant, ref)
}

// p7Art es un artículo tal como lo resume p7Stored: «código de categoría/código/sku/nombre/precio»,
// con los cuatro textos entre comillas. Sirve para escribir lo esperado con los espacios raros a la
// vista en el fuente (\u00a0), no pegados como caracteres invisibles.
func p7Art(category, code, sku, label, price string) string {
	return p7Quote(category) + "/" + p7Quote(code) + "/" + p7Quote(sku) + "/" + p7Quote(label) + "/" + price
}

// p7Arts junta varios artículos de p7Art como los devuelve p7Stored.
func p7Arts(arts ...string) string { return strings.Join(arts, "\n") }
