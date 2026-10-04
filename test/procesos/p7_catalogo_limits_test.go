//go:build integracion

package procesos

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Los dos topes del import de P7: el de artículos (WAPP_IMPORT_MAX_ITEMS, 500 por defecto) y el de
// bytes (WAPP_TENANT_CONTENT_MAX_BYTES, 1 MiB por defecto), con los dos lados de cada frontera.

const (
	// p7LimitRef es la ref donde se aplican los catálogos que están justo en el tope.
	p7LimitRef = "p7-tope"
	// Los motivos del tope de artículos, por cada puerta.
	p7ErrItemsJSON    = "el catálogo trae 501 artículos y el máximo por importación es 500: divide la carga en varios archivos."
	p7ErrItemsTabular = "la planilla trae 501 artículos y el máximo por importación es 500: divide la carga en varias planillas."
	// p7ErrDocBytes y p7ErrFileBytes son los mensajes del techo de bytes, por cada puerta; el de
	// tenant-content es el del PUT genérico, que comparte el número.
	p7ErrDocBytes     = "el documento excede el tamaño máximo de 1048576 bytes"
	p7ErrFileBytes    = "el archivo excede el tamaño máximo de 1048576 bytes"
	p7ErrContentBytes = "excede el tamaño máximo de 1048576 bytes"
	// p7EnvelopeSlack es el margen que el servidor da al sobre multipart por encima del techo del
	// archivo (internal/publicapi/catalogtabular.go: multipartEnvelopeSlack).
	p7EnvelopeSlack = 8 << 10
)

// p7ManyItems arma n artículos válidos (JSON crudo) con códigos y SKUs correlativos desde from.
func p7ManyItems(from, n int) []string {
	items := make([]string, n)
	for i := range items {
		items[i] = p7Item(fmt.Sprint(from+i), fmt.Sprintf("ART-%d", from+i), "Artículo", "1")
	}
	return items
}

// p7ManyRows arma n filas válidas de planilla con códigos y SKUs correlativos.
func p7ManyRows(n int) [][]string {
	rows := make([][]string, n)
	for i := range rows {
		rows[i] = p7Row("1|Bebidas", "", fmt.Sprint(i), fmt.Sprintf("ART-%d", i), "Artículo", "1")
	}
	return rows
}

// itemLimit afirma el tope de artículos por las dos puertas, con los dos lados de la frontera: 500
// justos se importan; 501 se rechazan con UN solo defecto, el del tope —la validación no sigue: ni un
// defecto de los artículos, aunque vengan rotos—. El tope es del documento entero, no por categoría.
// En la planilla las filas en blanco no cuentan, y el defecto es de la planilla, no de una fila.
func (w *p7World) itemLimit(t *testing.T) {
	sc, tenant := w.sc, w.admin.tenant

	exact := p7Decode(t, w.importJSON(t, w.admin, "apply", p7LimitRef, p7Doc(p7Cat("1", "Bebidas", p7ManyItems(0, p7MaxItems)...))))
	if !exact.Applied || exact.Items != p7MaxItems || len(exact.Diff.Added) != p7MaxItems {
		t.Fatalf("500 artículos justos por JSON: aplicado %v con %d artículos y %d altas\n%s", exact.Applied, exact.Items, len(exact.Diff.Added), exact.reasons())
	}
	blob := p7Content(t, sc, tenant, p7LimitRef)
	before := p7Mark(t, sc, tenant)

	broken := make([]string, p7MaxItems+1)
	for i := range broken {
		broken[i] = `{"code":"` + fmt.Sprint(i) + `"}`
	}
	over := []p7Rejection{
		{"json_501", p7Doc(p7Cat("1", "Bebidas", p7ManyItems(0, p7MaxItems+1)...)), "r0/catalog", []string{p7ErrItemsJSON}},
		{"json_501_broken_items", p7Doc(`{"code":"1","label":"Bebidas","items":[` + strings.Join(broken, ",") + `]}`), "r0/catalog", []string{p7ErrItemsJSON}},
		{"json_501_across_categories", p7Doc(p7Cat("1", "Bebidas", p7ManyItems(0, 250)...) + "," + p7Cat("2", "Postres", p7ManyItems(250, 251)...)), "r0/catalog", []string{p7ErrItemsJSON}},
	}
	for _, tc := range over {
		t.Run(tc.name, func(t *testing.T) { w.wantRejected(t, w.importJSON(t, w.admin, "apply", p7LimitRef, tc.doc), tc) })
	}
	t.Run("tabular_501", func(t *testing.T) {
		tc := p7Rejection{wheres: "r0/planilla", says: []string{p7ErrItemsTabular}}
		w.wantRejected(t, w.importFile(t, w.admin, "apply", p7LimitRef, p7Sheet(t, p7ManyRows(p7MaxItems+1)...)), tc)
	})
	if after := p7Mark(t, sc, tenant); after != before {
		t.Fatalf("un catálogo por encima del tope escribió o archivó contenido")
	}

	// 500 filas llenas y 39 en blanco intercaladas: las de blanco no cuentan, y el catálogo es el mismo.
	rows := p7ManyRows(p7MaxItems)
	padded := make([][]string, 0, len(rows)+40)
	for i, row := range rows {
		if i%13 == 0 {
			padded = append(padded, p7Row("\u00a0"))
		}
		padded = append(padded, row)
	}
	sheet := p7Decode(t, w.importFile(t, w.admin, "apply", p7LimitRef, p7Sheet(t, padded...)))
	if !sheet.Applied || sheet.Items != p7MaxItems || sheet.ArchivedVersion != 1 || sheet.diff() != "||||500" {
		t.Fatalf("500 filas justas (y filas en blanco) por planilla: aplicado %v, %d artículos, versión %d, diff %q\n%s",
			sheet.Applied, sheet.Items, sheet.ArchivedVersion, sheet.diff(), sheet.reasons())
	}
	if got := p7Content(t, sc, tenant, p7LimitRef); got != blob {
		t.Errorf("las 500 filas de la planilla escribieron otro blob que los 500 artículos del JSON")
	}
}

// p7DocOfSize fabrica un documento VÁLIDO de exactamente n bytes, rellenando la descripción de su
// único artículo (`documentoDe`, internal/catalogimport/limits_test.go): lo único que puede hacerlo
// fallar es su tamaño. Falla (t.Fatalf) si n es menor que el documento sin relleno.
func p7DocOfSize(t *testing.T, n int) []byte {
	t.Helper()
	const template = `{"format":"wapp.catalog_import","version":1,"catalog":{"categories":[` +
		`{"code":"1","label":"Bebidas","items":[{"code":"1","sku":"CAFE","label":"Café","price":2500,"description":"%s"}]}]}}`
	base := len(template) - len("%s")
	if n < base {
		t.Fatalf("no se puede fabricar un documento de %d bytes: el mínimo es %d", n, base)
	}
	doc := []byte(strings.Replace(template, "%s", strings.Repeat("a", n-base), 1))
	if len(doc) != n {
		t.Fatalf("el documento fabricado mide %d bytes y se pedían %d", len(doc), n)
	}
	return doc
}

// p7SheetOfSize fabrica una planilla válida de exactamente n bytes, rellenando la descripción.
func p7SheetOfSize(t *testing.T, n int) []byte {
	t.Helper()
	base := len(p7Sheet(t, p7Row("1|Bebidas", "", "1", "CAFE", "Café", "2500", "")))
	sheet := p7Sheet(t, p7Row("1|Bebidas", "", "1", "CAFE", "Café", "2500", strings.Repeat("a", n-base)))
	if len(sheet) != n {
		t.Fatalf("la planilla fabricada mide %d bytes y se pedían %d", len(sheet), n)
	}
	return sheet
}

// byteLimit afirma el techo de bytes, que es el MISMO número para el import JSON, para el archivo de la
// planilla y para el PUT genérico de tenant-content: 1 MiB justo entra y un byte más es 413. El 413
// del JSON y el del PUT son un error simple con `max_bytes`; el de la planilla va con la forma de los
// defectos (`validation_failed`, campo `archivo`), y nombra el techo del ARCHIVO también cuando lo que
// se pasó fue el sobre multipart (techo + 8 KiB). Se mira en validate y nada escribe.
func (w *p7World) byteLimit(t *testing.T) {
	const ref = "p7-techo"
	before := p7Mark(t, w.sc, w.admin.tenant)

	fits := p7Decode(t, w.importJSON(t, w.admin, "validate", ref, p7DocOfSize(t, p7MaxBytes)))
	if fits.Mode != "validate" || fits.Items != 1 || fits.diff() != "|CAFE|||0" {
		t.Errorf("un documento de 1 MiB justo: %+v; quería que valide", fits)
	}
	r := w.importJSON(t, w.admin, "validate", ref, p7DocOfSize(t, p7MaxBytes+1))
	if over := p7Decode(t, r); r.Codigo != http.StatusRequestEntityTooLarge || over.Error != p7ErrDocBytes || over.MaxBytes != p7MaxBytes || len(over.Errors) != 0 {
		t.Errorf("un documento de 1 MiB + 1: HTTP %d %s; quería 413 «%s» con max_bytes", r.Codigo, recortar(r.Cuerpo), p7ErrDocBytes)
	}
	w.sheetByteLimit(t, ref)

	// Los dos PUT van por send y no por call: con un cuerpo de 1 MiB, un 429 puede verse como un
	// error de transporte, y solo send lo reintenta.
	put := func(n int) respuesta {
		const envelope = `{"a":""}`
		body := []byte(`{"a":"` + strings.Repeat("x", n-len(envelope)) + `"}`)
		return w.send(t, w.admin, p7ActContent, http.MethodPut, p7RouteContent+"/"+ref, "application/json", body).respuesta
	}
	if r = put(p7MaxBytes + 1); !p9ErrorIs(r, http.StatusRequestEntityTooLarge, p7ErrContentBytes) {
		t.Errorf("PUT de tenant-content de 1 MiB + 1: HTTP %d %s; quería 413", r.Codigo, recortar(r.Cuerpo))
	}
	if after := p7Mark(t, w.sc, w.admin.tenant); after != before {
		t.Errorf("mirar un documento en el techo, o pasarse de él, escribió contenido")
	}
	if r = put(p7MaxBytes); r.Codigo != http.StatusOK {
		t.Errorf("PUT de tenant-content de 1 MiB justo: HTTP %d %s; quería 200", r.Codigo, recortar(r.Cuerpo))
	}
	if n := consultaEntero(t, w.sc.DB, `SELECT length(content->>'a') FROM public.tenant_content WHERE tenant_id = $1 AND ref = $2`, w.admin.tenant, ref); n != p7MaxBytes-len(`{"a":""}`) {
		t.Errorf("el PUT de 1 MiB guardó un valor de %d caracteres", n)
	}
}

// sheetByteLimit afirma el techo de bytes del archivo de la planilla, mirando: 1 MiB justo valida; un
// byte más, y también lo que se pasa del sobre multipart, es 413 con el defecto del archivo.
func (w *p7World) sheetByteLimit(t *testing.T, ref string) {
	t.Helper()
	fits := p7Decode(t, w.importFile(t, w.admin, "validate", ref, p7SheetOfSize(t, p7MaxBytes)))
	if fits.Mode != "validate" || fits.Items != 1 || fits.diff() != "|CAFE|||0" {
		t.Errorf("una planilla de 1 MiB justo: %+v; quería que valide\n%s", fits, fits.reasons())
	}
	for name, size := range map[string]int{"file_ceiling": p7MaxBytes + 1, "envelope_ceiling": p7MaxBytes + 2*p7EnvelopeSlack} {
		r := w.importFile(t, w.admin, "validate", ref, p7SheetOfSize(t, size))
		over := p7Decode(t, r)
		if r.Codigo != http.StatusRequestEntityTooLarge || over.Error != p7ValidationFailed || over.wheres() != "r0/archivo" || over.reasons() != p7ErrFileBytes {
			t.Errorf("%s (%d bytes): HTTP %d %s; quería 413 con el defecto del archivo", name, size, r.Codigo, recortar(r.Cuerpo))
		}
	}
}
