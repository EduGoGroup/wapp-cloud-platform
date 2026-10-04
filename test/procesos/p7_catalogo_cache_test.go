//go:build integracion

package procesos

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// La caché del índice del catálogo (internal/intake/catalogo) vista desde la puerta, y la URL
// prefirmada de media. El pipeline de captación cruza cada pedido con el catálogo del tenant a través
// de un índice que se cachea por empresa y se invalida por CONTENIDO: aquí se afirma que el borrador
// que sigue a un import —y a un PUT a mano— se cotiza con el catálogo nuevo, sin reiniciar el servidor.

const (
	// Los contactos de los tres borradores: antes del import, después, y después del PUT a mano.
	p7PnBefore = "573007770001"
	p7PnImport = "573007770002"
	p7PnManual = "573007770003"

	// p7NewTequenosSKU es el SKU con el que el catálogo importado vende los tequeños.
	p7NewTequenosSKU = "TEQ-30@@B"
)

// p7ShippingLine es la línea de envío, la última de todo borrador.
const p7ShippingLine = "shipping|" + p4ShippingSKU + "|Envío por confirmar|1|-|:0|-"

// p7NewCatalog es el catálogo que se importa sobre el del escenario (draftCatalog): la torta de
// vainilla sube de 3900 a 4100, los tequeños cambian de SKU (con un separador repetido) y de precio,
// las presentaciones de la torta de chocolate suben, y los panes desaparecen.
func p7NewCatalog() map[string]any {
	item := func(code, sku, label string, price float64) map[string]any {
		return map[string]any{"code": code, "sku": sku, "label": label, "price": price}
	}
	choc := item("1", "TORTA-CHOC", "Torta de chocolate", 2100)
	choc["variants"] = []any{
		map[string]any{"code": "10", "label": "10 porciones", "price": 2200},
		map[string]any{"code": "12", "label": "12 porciones", "price": 2500},
		map[string]any{"code": "25", "label": "25 porciones", "price": 3900},
	}
	return map[string]any{
		"format": "wapp.catalog_import", "version": 1,
		"catalog": map[string]any{"categories": []any{
			map[string]any{"code": "1", "label": "Tortas", "items": []any{choc, item("2", "TORTA-VAIN", "Torta de vainilla", 4100)}},
			map[string]any{"code": "2", "label": "Congelados", "items": []any{item("1", p7NewTequenosSKU, "Tequeños congelados", 510)}},
		}},
	}
}

// draftLines crea un borrador con la ráfaga canónica del contacto y devuelve el id de la solicitud y
// el resumen de su revisión (p7RevisionLines). Afirma que el pipeline corrió entero (cinco inferencias).
func (w *p7World) draftLines(t *testing.T, contact string) (id, lines string) {
	t.Helper()
	calls := len(w.sc.Script.Calls(""))
	id = createDraft(t, w.sc, contact)
	if n := len(w.sc.Script.Calls("")) - calls; n != 5 {
		t.Errorf("el borrador de %s provocó %d inferencias, quería 5 (P2, tres P3 y P4)", contact, n)
	}
	return id, p7RevisionLines(t, w.sc, id)
}

// p7RevisionLines lee la revisión 1 de una solicitud y la resume: sus líneas, una por renglón, y debajo,
// entre paréntesis, las presentaciones que ofrece la primera («sku=precio», separadas por comas).
func p7RevisionLines(t *testing.T, sc *draftScene, id string) string {
	t.Helper()
	payload := p4RevisionPayload(t, sc, id)
	if len(payload.Lines) == 0 {
		t.Fatalf("la revisión de %s no trae líneas", id)
	}
	opts := make([]string, 0, len(payload.Lines[0].VariantOptions))
	for _, o := range payload.Lines[0].VariantOptions {
		opts = append(opts, fmt.Sprintf("%s=%v", o.SKU, o.Price))
	}
	return strings.Join(p4Summaries(payload), "\n") + "\n(" + strings.Join(opts, ",") + ")"
}

// indexCache afirma que la caché del índice ve el catálogo nuevo, por los dos caminos que cambian el
// contenido —y que desde la caché son indistinguibles: ninguno deja versión en tenant_content—:
//
//  1. Con el catálogo del escenario, un borrador se cotiza con sus precios y sus SKU.
//  2. Tras POST /api/v1/catalog/import en apply, el borrador SIGUIENTE usa el precio y el SKU nuevos.
//  3. Tras un PUT a mano de tenant-content que cambia UN dígito sin cambiar el tamaño del documento
//     (y que no versiona), el borrador siguiente usa ese precio.
//
// Los borradores ya creados no cambian: sus líneas guardan lo que se cotizó entonces. Todo ocurre
// contra el mismo proceso del servidor.
func (w *p7World) indexCache(t *testing.T) {
	oldLines := strings.Join([]string{
		"matched|TORTA-CHOC|Torta de chocolate|1|-|:0|exact/1",
		"matched|TORTA-VAIN|Torta de vainilla|1|3900|:0|exact/1",
		"matched|TEQ-30|Tequeños congelados|1|490|package:30|exact/1",
		p7ShippingLine}, "\n") + "\n(TORTA-CHOC#10=2100,TORTA-CHOC#12=2400)"
	newLines := strings.Join([]string{
		"matched|TORTA-CHOC|Torta de chocolate|1|-|:0|exact/1",
		"matched|TORTA-VAIN|Torta de vainilla|1|4100|:0|exact/1",
		"matched|" + p7NewTequenosSKU + "|Tequeños congelados|1|510|package:30|exact/1",
		p7ShippingLine}, "\n") + "\n(TORTA-CHOC#10=2200,TORTA-CHOC#12=2500)"

	first, got := w.draftLines(t, p7PnBefore)
	if got != oldLines {
		t.Fatalf("el borrador con el catálogo del escenario:\n%s\nquería\n%s", got, oldLines)
	}

	w.importNewCatalog(t)
	second, got := w.draftLines(t, p7PnImport)
	if got != newLines {
		t.Errorf("el borrador tras el import NO usa el catálogo recién importado:\n%s\nquería\n%s", got, newLines)
	}

	w.editCatalogByHand(t)
	third, got := w.draftLines(t, p7PnManual)
	if want := strings.Replace(newLines, "|4100|", "|4900|", 1); got != want {
		t.Errorf("el borrador tras el PUT a mano NO usa el catálogo editado:\n%s\nquería\n%s", got, want)
	}

	if first == second || second == third || first == third {
		t.Fatalf("los tres borradores no son tres solicitudes: %s, %s, %s", first, second, third)
	}
	for id, want := range map[string]string{first: oldLines, second: newLines} {
		if got = p7RevisionLines(t, w.sc, id); got != want {
			t.Errorf("el borrador %s, ya creado, cambió con el catálogo:\n%s\nquería\n%s", id, got, want)
		}
	}
	if got = p9Scalar(t, w.sc.DB, `SELECT count(*)::text || '|' || count(*) FILTER (WHERE status = 'pending_approval')::text FROM public.intakes WHERE tenant_id = $1`,
		w.admin.tenant); got != "3|3" {
		t.Errorf("las solicitudes de la empresa = %q, quería tres en pending_approval", got)
	}
}

// importNewCatalog aplica p7NewCatalog sobre la ref del escenario (la de por defecto) y afirma su diff
// contra el catálogo de draftCatalog y la versión que archiva.
func (w *p7World) importNewCatalog(t *testing.T) {
	t.Helper()
	r := w.calls.call(t, w.admin, p7ActContent, http.MethodPost, p7RouteImport+p7Query("apply", ""), p7NewCatalog())
	res := p7Decode(t, r)
	if r.Codigo != http.StatusOK || !res.Applied || res.Ref != draftCatalogRef || res.ArchivedVersion != 1 || res.Items != 3 ||
		res.diff() != "TORTA-VAIN:3900>4100|"+p7NewTequenosSKU+"|PAN-١٢٣,PAN@@1,PAN\u00a02,TEQ-30|TORTA-CHOC|0" {
		t.Fatalf("importar el catálogo nuevo: HTTP %d, versión %d, diff %q\n%s", r.Codigo, res.ArchivedVersion, res.diff(), recortar(r.Cuerpo))
	}
	if got := p7Versions(t, w.sc, w.admin.tenant, draftCatalogRef); got != "1:import_json" {
		t.Errorf("las versiones del catálogo = %q, quería la 1 (el catálogo del escenario, archivado)", got)
	}
}

// editCatalogByHand cambia UN dígito del catálogo vigente (4100 → 4900) por el PUT genérico de
// tenant-content, sin cambiar el tamaño del documento, y afirma que ese PUT no versiona.
func (w *p7World) editCatalogByHand(t *testing.T) {
	t.Helper()
	current := w.send(t, w.admin, "", http.MethodGet, p7RouteContent+"/"+draftCatalogRef, "", nil)
	edited := strings.Replace(string(current.Cuerpo), "4100", "4900", 1)
	if current.Codigo != http.StatusOK || edited == string(current.Cuerpo) || len(edited) != len(current.Cuerpo) {
		t.Fatalf("leer el catálogo vigente para editarlo a mano: HTTP %d\n%s", current.Codigo, recortar(current.Cuerpo))
	}
	if put := w.send(t, w.admin, p7ActContent, http.MethodPut, p7RouteContent+"/"+draftCatalogRef, "application/json", []byte(edited)); put.Codigo != http.StatusOK {
		t.Fatalf("PUT a mano del catálogo: HTTP %d\n%s", put.Codigo, recortar(put.Cuerpo))
	}
	if got := p7Versions(t, w.sc, w.admin.tenant, draftCatalogRef); got != "1:import_json" {
		t.Errorf("las versiones del catálogo tras el PUT a mano = %q: el PUT genérico no versiona", got)
	}
}

// p7Upload es la respuesta 200 de POST /api/v1/media/upload-url.
type p7Upload struct {
	URL       string `json:"url"`
	Key       string `json:"key"`
	ExpiresAt string `json:"expires_at"`
}

// mediaUploadURL afirma la URL prefirmada: un PUT de 15 minutos contra el endpoint del doble de S3,
// por PATH-STYLE (el bucket va en la ruta, no en el host: trampas T-3 y T-4), firmado en local —el
// doble no recibe ninguna petición— y con la key namespaceada por la empresa del token. El nombre se
// reduce a su base y lo que no es [A-Za-z0-9._-] pasa a `_`: un separador repetido, un espacio U+00A0
// o unos dígitos no ASCII no llegan a la key. El `mime` no se valida. Sin nombre o sin mime, 400.
func (w *p7World) mediaUploadURL(t *testing.T) {
	sc := w.sc
	endpoint, err := url.Parse(sc.S.S3.URL())
	if err != nil {
		t.Fatalf("la URL del doble de S3: %v", err)
	}
	names := []struct{ filename, mime, suffix string }{
		{"lista precios.pdf", "application/pdf", "-lista_precios.pdf"},
		{"a@@b.pdf", "a@@b", "-a__b.pdf"},
		{p7Arabic + ".pdf", p7Arabic, "-___.pdf"},
		{"lista" + p7NBSP + "precios.pdf", "x", "-lista_precios.pdf"},
		{p7NBSP + "carta.PDF" + p7IDSP, p7NBSP + "application/pdf" + p7NBSP, "-carta.PDF"},
		{"../../etc/passwd", "x", "-passwd"},
		{`C:\dir\a b.pdf`, "x", "-a_b.pdf"},
		{"..", "x", "-file"},
		{p7ZWSP, "x", "-_"},
	}
	for _, tc := range names {
		start := time.Now()
		r := w.calls.call(t, w.admin, p7ActMedia, http.MethodPost, p7RouteUpload, map[string]string{"filename": tc.filename, "mime": tc.mime})
		var up p7Upload
		r.JSON(t, &up)
		if r.Codigo != http.StatusOK {
			t.Errorf("upload-url de %q: HTTP %d %s", tc.filename, r.Codigo, recortar(r.Cuerpo))
			continue
		}
		p7CheckUpload(t, up, endpoint, sc.Tenant, tc.suffix, start)
	}

	other := w.calls.call(t, w.other, p7ActMedia, http.MethodPost, p7RouteUpload, map[string]string{"filename": "a.pdf", "mime": "application/pdf"})
	var up p7Upload
	other.JSON(t, &up)
	if other.Codigo != http.StatusOK || !strings.HasPrefix(up.Key, "wapp/media/"+w.other.tenant+"/") {
		t.Errorf("upload-url de la otra empresa: HTTP %d con la key %q; quería su propio espacio de nombres", other.Codigo, up.Key)
	}

	const required = "filename y mime son requeridos"
	bad := map[string]string{
		`{"filename":"","mime":""}`:                           required,
		`{"filename":"a.pdf"}`:                                required,
		`{"mime":"application/pdf"}`:                          required,
		`{"filename":` + p7Quote(p7NBSP) + `,"mime":"x"}`:     required,
		`{"filename":"a.pdf","mime":` + p7Quote(p7IDSP) + `}`: required,
		`esto no es json`:                                     "cuerpo JSON inválido",
	}
	for body, want := range bad {
		if r := w.send(t, w.admin, p7ActMedia, http.MethodPost, p7RouteUpload, "application/json", []byte(body)); !p9ErrorIs(r.respuesta, http.StatusBadRequest, want) {
			t.Errorf("upload-url con %s: HTTP %d %s; quería 400 «%s»", body, r.Codigo, recortar(r.Cuerpo), want)
		}
	}
	if got := sc.S.S3.Peticiones(); len(got) != 1 || got[0].Metodo != http.MethodHead {
		t.Errorf("el doble de S3 recibió %+v: las URLs se firman en local, sin red", got)
	}
}

// p7CheckUpload afirma una respuesta de upload-url: la key (`wapp/media/<empresa>/<uuid>-<nombre>`),
// la URL path-style contra el endpoint del doble con la firma SigV4 de un PutObject de 900 s, y la
// expiración a 15 minutos de la petición.
func p7CheckUpload(t *testing.T, up p7Upload, endpoint *url.URL, tenant, suffix string, start time.Time) {
	t.Helper()
	prefix := "wapp/media/" + tenant + "/"
	id := strings.TrimSuffix(strings.TrimPrefix(up.Key, prefix), suffix)
	if !strings.HasPrefix(up.Key, prefix) || !strings.HasSuffix(up.Key, suffix) || !identidadUUID.MatchString(id) {
		t.Errorf("la key %q no es %s<uuid>%s", up.Key, prefix, suffix)
	}
	signed, err := url.Parse(up.URL)
	if err != nil {
		t.Fatalf("la URL prefirmada %q no se puede leer: %v", up.URL, err)
	}
	if signed.Scheme != endpoint.Scheme || signed.Host != endpoint.Host {
		t.Errorf("la URL prefirmada va a %s://%s; quería el endpoint del doble (%s), con el bucket en la RUTA y no en el host", signed.Scheme, signed.Host, endpoint.Host)
	}
	if want := "/" + bucketServidor + "/" + up.Key; signed.Path != want {
		t.Errorf("la ruta de la URL prefirmada = %q, quería %q (path-style)", signed.Path, want)
	}
	q := signed.Query()
	want := map[string]string{"X-Amz-Algorithm": "AWS4-HMAC-SHA256", "X-Amz-Expires": "900", "X-Amz-SignedHeaders": "host", "x-id": "PutObject"}
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("la URL prefirmada trae %s=%q, quería %q", k, q.Get(k), v)
		}
	}
	if len(q.Get("X-Amz-Signature")) != 64 || !strings.HasSuffix(q.Get("X-Amz-Credential"), "/us-east-1/s3/aws4_request") {
		t.Errorf("la URL prefirmada no trae una firma SigV4 de la región del arnés: %s", signed.RawQuery)
	}
	expires, err := time.Parse(time.RFC3339, up.ExpiresAt)
	if err != nil {
		t.Fatalf("expires_at %q no es RFC 3339: %v", up.ExpiresAt, err)
	}
	if lo, hi := start.Add(15*time.Minute-2*time.Second), time.Now().Add(15*time.Minute+2*time.Second); expires.Before(lo) || expires.After(hi) {
		t.Errorf("expires_at = %s, quería 15 minutos después de la petición (entre %s y %s)", expires, lo, hi)
	}
}
