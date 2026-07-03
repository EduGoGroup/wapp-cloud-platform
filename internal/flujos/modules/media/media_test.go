package media_test

import (
	"errors"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/media"
)

// TestModuleContract fija el contrato del módulo con el Registry/engine: tipo
// "media", NO interactivo (nodo de salida) y Render sin texto propio (el texto va
// en el caption del MediaRef, §9.I). Es plantilla-compatible con menu/survey/cart.
func TestModuleContract(t *testing.T) {
	m := media.New()
	if got := m.Type(); got != "media" {
		t.Fatalf("Type()=%q, quiero \"media\"", got)
	}
	if m.WaitsForInput() {
		t.Fatalf("WaitsForInput()=true, quiero false (nodo de salida, no interactivo)")
	}
	if outs := m.Render(model.Node{}, model.Content{}); outs != nil {
		t.Fatalf("Render debe devolver nil (el texto va en Caption), got=%q", outs)
	}

	// El módulo satisface las interfaces esperadas por el engine (contrato T4).
	var _ modules.Module = m
	var _ modules.MediaEmitter = m
}

// mediaNode construye un nodo media con el descriptor inline dado (los campos
// vacíos se omiten del ContentRef).
func mediaNode(key, filename, mime, kind, caption string) model.Node {
	return model.Node{
		Type: media.NodeTypeMedia,
		Content: &model.ContentRef{
			Source:   "static",
			Key:      key,
			Filename: filename,
			Mime:     mime,
			Kind:     kind,
			Caption:  caption,
		},
	}
}

// TestEmitMediaValido comprueba que un descriptor completo se parsea a un MediaRef
// exacto, para document e image, con y sin caption. Recorta espacios en los campos
// clave; NO recorta el caption (puede llevar espacios/emoji a propósito).
func TestEmitMediaValido(t *testing.T) {
	cases := []struct {
		name string
		node model.Node
		want model.MediaRef
	}{
		{
			name: "documento pdf con caption",
			node: mediaNode("wapp/media/lista-precios.pdf", "Lista de precios.pdf", "application/pdf", "document", "Acá va la lista 📄"),
			want: model.MediaRef{Key: "wapp/media/lista-precios.pdf", Filename: "Lista de precios.pdf", Mime: "application/pdf", Kind: "document", Caption: "Acá va la lista 📄"},
		},
		{
			name: "imagen png sin caption",
			node: mediaNode("wapp/media/orden-291798.png", "orden.png", "image/png", "image", ""),
			want: model.MediaRef{Key: "wapp/media/orden-291798.png", Filename: "orden.png", Mime: "image/png", Kind: "image", Caption: ""},
		},
		{
			name: "recorta espacios en key/filename/mime/kind",
			node: mediaNode("  wapp/media/x.pdf ", " x.pdf ", " application/pdf ", " document ", " deja este "),
			want: model.MediaRef{Key: "wapp/media/x.pdf", Filename: "x.pdf", Mime: "application/pdf", Kind: "document", Caption: " deja este "},
		},
	}

	m := media.New()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ref, err := m.EmitMedia(tc.node, model.Content{})
			if err != nil {
				t.Fatalf("EmitMedia: error inesperado: %v", err)
			}
			if ref == nil {
				t.Fatalf("EmitMedia devolvió ref nil sin error")
			}
			if *ref != tc.want {
				t.Fatalf("MediaRef = %+v, quiero %+v", *ref, tc.want)
			}
		})
	}
}

// TestEmitMediaInvalido comprueba que un descriptor ausente o incompleto produce
// un ERROR CONTROLADO (envuelve model.ErrInvalidFlow), nunca un pánico ni un ref.
func TestEmitMediaInvalido(t *testing.T) {
	cases := []struct {
		name string
		node model.Node
	}{
		{name: "sin content", node: model.Node{Type: media.NodeTypeMedia}},
		{name: "sin key", node: mediaNode("", "x.pdf", "application/pdf", "document", "")},
		{name: "key solo espacios", node: mediaNode("   ", "x.pdf", "application/pdf", "document", "")},
		{name: "sin filename", node: mediaNode("k", "", "application/pdf", "document", "")},
		{name: "sin mime", node: mediaNode("k", "x.pdf", "", "document", "")},
		{name: "sin kind", node: mediaNode("k", "x.pdf", "application/pdf", "", "")},
		{name: "kind desconocido", node: mediaNode("k", "x.pdf", "application/pdf", "video", "")},
	}

	m := media.New()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ref, err := m.EmitMedia(tc.node, model.Content{})
			if err == nil {
				t.Fatalf("esperaba error controlado, obtuve ref=%+v", ref)
			}
			if !errors.Is(err, model.ErrInvalidFlow) {
				t.Fatalf("el error debe envolver model.ErrInvalidFlow, got: %v", err)
			}
			if ref != nil {
				t.Fatalf("ante descriptor inválido el ref debe ser nil, got=%+v", *ref)
			}
		})
	}
}
