package indice_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo/indice"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
)

// cache_errors_test.go es el trozo de cache_test.go (E-13) con los errores de
// Obtener. Los documentos y la fuente falsa están en cache_test.go.

// ---------------------------------------------------------------------------
// ERRORES: LA CACHÉ NO GUARDA NADA A MEDIAS
// ---------------------------------------------------------------------------

func TestObtener_ReadError(t *testing.T) {
	cause := errors.New("postgres caído")
	f := newFakeSource()
	f.publish("t1", docV1)
	f.fail(cause)
	c := newCache(t, f, 0)

	idx, err := c.Obtener(context.Background(), "t1")
	if !errors.Is(err, cause) {
		t.Fatalf("Obtener = %v; se esperaba un error que envuelva la causa", err)
	}
	if want := `catalogo: leer el documento del tenant "t1": postgres caído`; err.Error() != want {
		t.Errorf("error = %q; se esperaba %q", err.Error(), want)
	}
	if idx != nil || c.Tamano() != 0 {
		t.Errorf("tras un error de lectura hay índice (%v) o entrada (Tamano = %d)", idx != nil, c.Tamano())
	}
	assertStats(t, c, indice.Estadisticas{}, "un error de lectura no cuenta nada")

	f.fail(nil)
	if again := get(t, c, "t1"); again.Articulos() != 2 {
		t.Errorf("el siguiente job no se recuperó: %d artículos", again.Articulos())
	}
}

// TestObtener_BrokenDocumentLeavesNoIndex: el error sale con el tenant escrito y la
// caché no guarda nada — el siguiente job vuelve a intentarlo.
func TestObtener_BrokenDocumentLeavesNoIndex(t *testing.T) {
	const notAnObject = `catalogo: tenant "t1": el documento del catálogo no es un objeto JSON: `
	cases := []struct {
		name, doc, prefix string
		is                error
	}{
		{"truncated JSON", `{"categories":`, notAnObject, nil},
		{"empty bytes", ``, notAnObject, nil},
		{"a JSON list", `[]`, notAnObject, nil},
		{"JSON null", `null`, `catalogo: tenant "t1": `, model.ErrInvalidFlow},
		{"empty object", `{}`, `catalogo: tenant "t1": `, model.ErrInvalidFlow},
		{"categories is not a list", `{"categories":"x"}`, `catalogo: tenant "t1": `, model.ErrInvalidFlow},
		{"no categories", `{"categories":[]}`, `catalogo: tenant "t1": `, model.ErrInvalidFlow},
		{"too many articles", string(sizedDocument(t, 2001)), `catalogo: tenant "t1": `, indice.ErrCatalogoDemasiadoGrande},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeSource()
			f.publish("t1", tc.doc)
			c := newCache(t, f, 0)

			idx, err := c.Obtener(context.Background(), "t1")
			if err == nil {
				t.Fatalf("Obtener = nil; un documento roto es un error")
			}
			if !strings.HasPrefix(err.Error(), tc.prefix) {
				t.Errorf("error = %q; se esperaba que empezara por %q", err.Error(), tc.prefix)
			}
			if tc.is != nil && !errors.Is(err, tc.is) {
				t.Errorf("error = %v; se esperaba que envolviera %v", err, tc.is)
			}
			if idx != nil || c.Tamano() != 0 {
				t.Errorf("un documento roto dejó índice (%v) o entrada (Tamano = %d)", idx != nil, c.Tamano())
			}
			assertStats(t, c, indice.Estadisticas{}, "un documento roto no cuenta como construcción")

			f.publish("t1", docV1)
			if again := get(t, c, "t1"); again.Articulos() != 2 {
				t.Errorf("el siguiente job no se recuperó: %d artículos", again.Articulos())
			}
			assertStats(t, c, indice.Estadisticas{Construcciones: 1}, "el documento arreglado se indexa")
		})
	}
}

// TestObtener_BrokenDocumentKeepsThePreviousIndex: si el tenant ya tenía un índice
// y su documento se rompe, el job falla pero la caché se queda como estaba. Al
// volver el contenido anterior es un acierto: no se reconstruye.
func TestObtener_BrokenDocumentKeepsThePreviousIndex(t *testing.T) {
	f := newFakeSource()
	f.publish("t1", docV1)
	c := newCache(t, f, 0)
	idx := get(t, c, "t1")

	f.publish("t1", `{"categories":`)
	if _, err := c.Obtener(context.Background(), "t1"); err == nil {
		t.Fatalf("Obtener con el documento roto = nil; se esperaba un error")
	}
	if c.Tamano() != 1 {
		t.Errorf("Tamano() = %d; el índice anterior del tenant sigue en la caché", c.Tamano())
	}
	assertStats(t, c, indice.Estadisticas{Construcciones: 1}, "el intento fallido no cuenta")

	f.publish("t1", docV1)
	if back := get(t, c, "t1"); back != idx {
		t.Errorf("al volver el contenido anterior se reconstruyó; el índice seguía valiendo")
	}
	assertStats(t, c, indice.Estadisticas{Construcciones: 1, Aciertos: 1}, "volver al contenido indexado es un acierto")
}
