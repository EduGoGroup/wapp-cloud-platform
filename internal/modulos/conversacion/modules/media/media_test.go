package media_test

import (
	"errors"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/media"
)

// Aserciones de compilación: media es un modules.Module y un modules.MediaEmitter (y
// su valor cero también).
var (
	_ modules.Module       = media.New()
	_ modules.MediaEmitter = media.New()
	_ modules.Module       = media.Module{}
)

// invalidFlowPrefix es el texto del error base más el separador de la envoltura.
const invalidFlowPrefix = "definición de flujo inválida: "

// mediaNode construye un nodo media con el descriptor inline dado.
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

// El tipo de nodo y los kinds son observables: viajan en el JSON del flujo y al Edge.
func TestConstants(t *testing.T) {
	if media.NodeTypeMedia != "media" {
		t.Errorf("NodeTypeMedia = %q, quiero \"media\"", media.NodeTypeMedia)
	}
	if media.KindDocument != "document" || media.KindImage != "image" {
		t.Errorf("kinds = %q, %q; quiero \"document\", \"image\"", media.KindDocument, media.KindImage)
	}
}

func TestModule_Identity(t *testing.T) {
	m := media.New()
	if got := m.Type(); got != "media" {
		t.Errorf("Type() = %q, quiero \"media\"", got)
	}
	if m.WaitsForInput() {
		t.Error("WaitsForInput() = true, quiero false: es un nodo de salida")
	}
	if m.ProducesDurableContent() {
		t.Error("ProducesDurableContent() = true, quiero false: media no proyecta nada durable")
	}
}

// Render no produce texto: el texto va en el Caption del MediaRef.
func TestModule_Render_IsAlwaysNil(t *testing.T) {
	m := media.New()
	node := mediaNode("k", "f.pdf", "application/pdf", media.KindDocument, "pie")
	node.Prompt = "un prompt"
	for _, content := range []model.Content{{}, {Prompt: "resuelto"}} {
		if out := m.Render(node, content); out != nil {
			t.Errorf("Render = %q, quiero nil", out)
		}
	}
}

// Step es una permanencia neutra que devuelve el MISMO mapa de Vars, sin copiar.
func TestModule_Step_IsNeutral(t *testing.T) {
	m := media.New()

	vars := map[string]any{"k": "v"}
	res := m.Step(mediaNode("k", "f.pdf", "application/pdf", media.KindImage, ""), model.Conversation{EventID: "ev-1", Vars: vars}, "lo que sea")
	if res.Next != nil || len(res.Outputs) != 0 || len(res.Effects) != 0 || res.Query != nil || res.Outcome != model.OutcomeUndeclared {
		t.Errorf("Result = %+v, quiero sin transición, salidas, efectos, consulta ni desenlace", res)
	}
	res.Vars["probe"] = true
	if _, same := vars["probe"]; !same || len(res.Vars) != 2 {
		t.Errorf("Vars = %v, quiero el mismo mapa de la conversación (sin copia)", res.Vars)
	}

	if res := m.Step(model.Node{}, model.Conversation{}, ""); res.Vars != nil {
		t.Errorf("Vars = %v, quiero nil cuando la conversación no trae Vars", res.Vars)
	}
}

// Un descriptor completo se parsea a un MediaRef exacto: los cuatro campos clave
// recortados, el caption tal cual, y el contenido resuelto ignorado.
func TestModule_EmitMedia_Valid(t *testing.T) {
	cases := []struct {
		name string
		node model.Node
		want model.MediaRef
	}{
		{
			name: "pdf document with caption",
			node: mediaNode("wapp/media/lista-precios.pdf", "Lista de precios.pdf", "application/pdf", "document", "Acá va la lista 📄"),
			want: model.MediaRef{Key: "wapp/media/lista-precios.pdf", Filename: "Lista de precios.pdf", Mime: "application/pdf", Kind: "document", Caption: "Acá va la lista 📄"},
		},
		{
			name: "png image without caption",
			node: mediaNode("wapp/media/orden-291798.png", "orden.png", "image/png", "image", ""),
			want: model.MediaRef{Key: "wapp/media/orden-291798.png", Filename: "orden.png", Mime: "image/png", Kind: "image"},
		},
		{
			name: "key fields are trimmed, caption is not",
			node: mediaNode("  wapp/media/x.pdf ", " x.pdf ", " application/pdf ", " document ", " deja este "),
			want: model.MediaRef{Key: "wapp/media/x.pdf", Filename: "x.pdf", Mime: "application/pdf", Kind: "document", Caption: " deja este "},
		},
		{
			name: "unicode spaces are trimmed too",
			node: mediaNode("\u00a0k\u3000", "\tf.png\n", "\u2003image/png", "image\u00a0", "\u00a0"),
			want: model.MediaRef{Key: "k", Filename: "f.png", Mime: "image/png", Kind: "image", Caption: "\u00a0"},
		},
		{
			name: "kind and mime are not cross checked",
			node: mediaNode("k", "f.pdf", "application/pdf", "image", ""),
			want: model.MediaRef{Key: "k", Filename: "f.pdf", Mime: "application/pdf", Kind: "image"},
		},
	}
	m := media.New()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ref, err := m.EmitMedia(tc.node, model.Content{Prompt: "ignorado", Raw: map[string]any{"key": "otra"}})
			if err != nil {
				t.Fatalf("EmitMedia: error inesperado: %v", err)
			}
			if ref == nil || *ref != tc.want {
				t.Fatalf("MediaRef = %+v, quiero %+v", ref, tc.want)
			}
		})
	}
}

// El tipo del nodo y el Source del descriptor no se validan aquí.
func TestModule_EmitMedia_IgnoresNodeTypeAndSource(t *testing.T) {
	node := model.Node{Type: model.NodeTypeMenu, Content: &model.ContentRef{
		Source: "json", Ref: "x", Key: "k", Filename: "f", Mime: "m", Kind: media.KindDocument,
	}}
	ref, err := media.New().EmitMedia(node, model.Content{})
	if err != nil || ref == nil || ref.Key != "k" {
		t.Errorf("EmitMedia = %+v, %v; quiero el MediaRef sin error", ref, err)
	}
}

// Un descriptor ausente o incompleto da un error controlado con su texto literal,
// envuelto en model.ErrInvalidFlow, y ningún MediaRef. Gana el PRIMER defecto.
func TestModule_EmitMedia_Invalid(t *testing.T) {
	cases := []struct {
		name string
		node model.Node
		want string
	}{
		{name: "no content", node: model.Node{Type: media.NodeTypeMedia}, want: "nodo media sin content (descriptor inline requerido)"},
		{name: "no key", node: mediaNode("", "x.pdf", "application/pdf", "document", ""), want: "nodo media sin key"},
		{name: "blank key", node: mediaNode(" \u00a0 ", "x.pdf", "application/pdf", "document", ""), want: "nodo media sin key"},
		{name: "no filename", node: mediaNode("k", "", "application/pdf", "document", ""), want: "nodo media sin filename"},
		{name: "blank filename", node: mediaNode("k", "\t", "application/pdf", "document", ""), want: "nodo media sin filename"},
		{name: "no mime", node: mediaNode("k", "x.pdf", "", "document", ""), want: "nodo media sin mime"},
		{name: "no kind", node: mediaNode("k", "x.pdf", "application/pdf", "", ""), want: `nodo media con kind "" inválido (document|image)`},
		{name: "unknown kind", node: mediaNode("k", "x.pdf", "application/pdf", "video", ""), want: `nodo media con kind "video" inválido (document|image)`},
		{name: "kind is case sensitive", node: mediaNode("k", "x.pdf", "application/pdf", "Document", ""), want: `nodo media con kind "Document" inválido (document|image)`},
		{name: "kind is quoted already trimmed", node: mediaNode("k", "x.pdf", "application/pdf", "  audio ", ""), want: `nodo media con kind "audio" inválido (document|image)`},
		{name: "zero width space is not trimmed", node: mediaNode("k", "x.pdf", "application/pdf", "image\u200b", ""), want: "nodo media con kind \"image\\u200b\" inválido (document|image)"},
		{name: "empty descriptor reports key first", node: mediaNode("", "", "", "", ""), want: "nodo media sin key"},
		{name: "filename is reported before mime and kind", node: mediaNode("k", "", "", "video", ""), want: "nodo media sin filename"},
		{name: "mime is reported before kind", node: mediaNode("k", "f", "", "video", ""), want: "nodo media sin mime"},
	}
	m := media.New()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ref, err := m.EmitMedia(tc.node, model.Content{})
			if err == nil {
				t.Fatalf("esperaba un error controlado, obtuve ref = %+v", ref)
			}
			if !errors.Is(err, model.ErrInvalidFlow) {
				t.Errorf("el error %q no envuelve model.ErrInvalidFlow", err)
			}
			if got, want := err.Error(), invalidFlowPrefix+tc.want; got != want {
				t.Errorf("error = %q, quiero %q", got, want)
			}
			if ref != nil {
				t.Errorf("MediaRef = %+v, quiero nil ante un descriptor inválido", *ref)
			}
		})
	}
}
