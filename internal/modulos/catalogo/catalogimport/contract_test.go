package catalogimport_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo/catalogimport"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/config"
)

// contract_test.go cubre lo que contract.go promete por sí solo: las constantes del
// contrato, los topes por defecto, la forma JSON de cada tipo y los tres textos de
// ImportValidationError. Que un tope no positivo caiga al default se observa donde
// el tope se aplica (ReadLimited y Validate, en validator_test.go).

// compactJSON serializa un valor en una línea, o falla el test.
func compactJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal(%T) = error %v", v, err)
	}
	return string(b)
}

func TestImportConstants_AreTheContract(t *testing.T) {
	if catalogimport.ImportFormat != "wapp.catalog_import" {
		t.Errorf("ImportFormat = %q; la marca del documento es \"wapp.catalog_import\"", catalogimport.ImportFormat)
	}
	if catalogimport.ImportVersion != 1 {
		t.Errorf("ImportVersion = %d; el contrato publicado es la versión 1", catalogimport.ImportVersion)
	}
	if catalogimport.DefaultMaxJSONBytes != int64(1<<20) {
		t.Errorf("DefaultMaxJSONBytes = %d; el techo por defecto es 1 MiB", catalogimport.DefaultMaxJSONBytes)
	}
	if catalogimport.DefaultMaxItems != 500 {
		t.Errorf("DefaultMaxItems = %d; el tope por defecto son 500 artículos", catalogimport.DefaultMaxItems)
	}
}

func TestDefaultLimits_AreTheDefaultConstants(t *testing.T) {
	want := catalogimport.Limits{
		MaxJSONBytes: catalogimport.DefaultMaxJSONBytes,
		MaxItems:     catalogimport.DefaultMaxItems,
	}
	if got := catalogimport.DefaultLimits(); got != want {
		t.Errorf("DefaultLimits() = %+v; se esperaba %+v", got, want)
	}
}

// TestDefaultLimits_MatchPlatformConfigDefaults amarra las dos parejas de números:
// la configuración los lee del entorno y el validador los aplica, y una divergencia
// silenciosa haría que el .env.example documentara un límite y el código impusiera
// otro. Las variables van a "0" a propósito: fuerzan el camino del default sin
// depender del entorno de quien corre el test.
func TestDefaultLimits_MatchPlatformConfigDefaults(t *testing.T) {
	t.Setenv("WAPP_TENANT_CONTENT_MAX_BYTES", "0")
	t.Setenv("WAPP_IMPORT_MAX_ITEMS", "0")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("no se pudo cargar la configuración: %v", err)
	}
	if cfg.Import.MaxItems != catalogimport.DefaultMaxItems {
		t.Errorf("config dice %d artículos y el validador %d", cfg.Import.MaxItems, catalogimport.DefaultMaxItems)
	}
	if cfg.TenantContent.MaxBytes != catalogimport.DefaultMaxJSONBytes {
		t.Errorf("config dice %d bytes y el validador %d", cfg.TenantContent.MaxBytes, catalogimport.DefaultMaxJSONBytes)
	}
}

// TestCatalogImport_JSONTagsAreExact sujeta las etiquetas JSON del documento: las
// leen la consola, la plantilla que se descarga y el prompt que se le da al LLM. El
// literal va escrito a mano: compararlo con lo que produce el mismo struct no
// probaría nada.
func TestCatalogImport_JSONTagsAreExact(t *testing.T) {
	doc := catalogimport.CatalogImport{
		Format:  catalogimport.ImportFormat,
		Version: catalogimport.ImportVersion,
		Source:  &catalogimport.ImportSource{Kind: "llm", Model: "m", Hint: "h"},
		Catalog: catalogimport.ImportBody{Categories: []catalogimport.ImportCategory{{
			Code:          "1",
			Label:         "L",
			Subcategories: []catalogimport.ImportSubcategory{{Code: "s", Label: "S"}},
			Items: []catalogimport.ImportItem{{
				Code: "1", SKU: "A", Label: "a", Price: 1.5, Description: "d", Subcategory: "s",
				Tags:       []string{"t"},
				Attributes: map[string]string{"k": "v"},
				Variants:   []catalogimport.ImportVariant{{Code: "V", Label: "vl", Price: 2}},
				Components: []catalogimport.ImportComponent{{SKU: "C", Qty: 3}},
			}},
		}}},
	}
	const want = `{"format":"wapp.catalog_import","version":1,"source":{"kind":"llm","model":"m","hint":"h"},` +
		`"catalog":{"categories":[{"code":"1","label":"L","subcategories":[{"code":"s","label":"S"}],` +
		`"items":[{"code":"1","sku":"A","label":"a","price":1.5,"description":"d","subcategory":"s","tags":["t"],` +
		`"attributes":{"k":"v"},"variants":[{"code":"V","label":"vl","price":2}],"components":[{"sku":"C","qty":3}]}]}]}}`
	if got := compactJSON(t, doc); got != want {
		t.Errorf("documento = %s\nse esperaba %s", got, want)
	}

	var back catalogimport.CatalogImport
	if err := json.Unmarshal([]byte(want), &back); err != nil {
		t.Fatalf("el documento no se relee con sus propias etiquetas: %v", err)
	}
	if !reflect.DeepEqual(back, doc) {
		t.Errorf("ida y vuelta = %+v\nse esperaba %+v", back, doc)
	}
}

// TestCatalogImport_OmitsOnlyTheOptionalFields: lo opcional desaparece del JSON
// cuando está vacío y lo obligatorio viaja siempre, también en su valor cero.
func TestCatalogImport_OmitsOnlyTheOptionalFields(t *testing.T) {
	cases := map[string]struct {
		value any
		want  string
	}{
		"zero document":   {catalogimport.CatalogImport{}, `{"format":"","version":0,"catalog":{"categories":null}}`},
		"empty source":    {catalogimport.ImportSource{}, `{}`},
		"zero body":       {catalogimport.ImportBody{}, `{"categories":null}`},
		"zero category":   {catalogimport.ImportCategory{}, `{"code":"","label":"","items":null}`},
		"zero item":       {catalogimport.ImportItem{}, `{"code":"","sku":"","label":"","price":0}`},
		"zero variant":    {catalogimport.ImportVariant{}, `{"code":"","label":"","price":0}`},
		"implicit qty":    {catalogimport.ImportComponent{SKU: "C"}, `{"sku":"C"}`},
		"zero subcat":     {catalogimport.ImportSubcategory{}, `{"code":"","label":""}`},
		"no defects list": {catalogimport.ImportValidationError{}, `{"errors":null}`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := compactJSON(t, c.value); got != c.want {
				t.Errorf("JSON = %s; se esperaba %s", got, c.want)
			}
		})
	}
}

// TestImportFieldError_JSONShape: la fila y los índices son opcionales (cada camino
// rellena los suyos); el campo y el motivo viajan siempre.
func TestImportFieldError_JSONShape(t *testing.T) {
	category, item := 0, 2
	full := catalogimport.ImportFieldError{Row: 3, CategoryIndex: &category, ItemIndex: &item, Field: "price", Reason: "r"}
	if got, want := compactJSON(t, full), `{"row":3,"category_index":0,"item_index":2,"field":"price","reason":"r"}`; got != want {
		t.Errorf("defecto completo = %s; se esperaba %s", got, want)
	}

	header := catalogimport.ImportFieldError{Field: "format", Reason: "r"}
	if got, want := compactJSON(t, header), `{"field":"format","reason":"r"}`; got != want {
		t.Errorf("defecto de cabecera = %s; se esperaba %s (sin fila ni índices)", got, want)
	}

	list := catalogimport.ImportValidationError{Errors: []catalogimport.ImportFieldError{header}}
	if got, want := compactJSON(t, list), `{"errors":[{"field":"format","reason":"r"}]}`; got != want {
		t.Errorf("lista de defectos = %s; se esperaba %s", got, want)
	}
}

func TestImportValidationError_ErrorSummarizes(t *testing.T) {
	defects := func(reasons ...string) *catalogimport.ImportValidationError {
		e := &catalogimport.ImportValidationError{}
		for _, r := range reasons {
			e.Errors = append(e.Errors, catalogimport.ImportFieldError{Field: "f", Reason: r})
		}
		return e
	}
	cases := map[string]struct {
		err  *catalogimport.ImportValidationError
		want string
	}{
		"nil receiver": {nil, "el documento de import no es válido"},
		"no defects":   {defects(), "el documento de import no es válido"},
		"one defect":   {defects("uno."), "el documento de import tiene 1 problema: uno."},
		"many defects": {defects("uno.", "dos.", "tres."), "el documento de import tiene 3 problemas; el primero: uno."},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := c.err.Error(); got != c.want {
				t.Errorf("Error() = %q; se esperaba %q", got, c.want)
			}
		})
	}
}

// TestImportValidationError_TravelsAsAnError: es un error y no una lista suelta para
// que el llamante lo propague y lo recupere con errors.As.
func TestImportValidationError_TravelsAsAnError(t *testing.T) {
	original := &catalogimport.ImportValidationError{
		Errors: []catalogimport.ImportFieldError{{Field: "sku", Reason: "repetido."}},
	}
	wrapped := fmt.Errorf("importando el catálogo: %w", original)

	var got *catalogimport.ImportValidationError
	if !errors.As(wrapped, &got) {
		t.Fatalf("errors.As no encontró *ImportValidationError dentro de %v", wrapped)
	}
	if got != original {
		t.Errorf("errors.As devolvió otro valor: %+v", got)
	}
}
