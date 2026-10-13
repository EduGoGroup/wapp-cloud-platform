package engine_test

// engine_render_test.go — el render que comparten Enter, EnterPrimed y las
// transiciones de Step: nodos "message" inline, nodos de salida y sus errores.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
)

// Un "message" es inline: ni consulta el Registry (aunque alguien registre un módulo
// con ese tipo) ni resuelve contenido.
func TestEnter_MessageNodesAreInline(t *testing.T) {
	impostor := stubModule{
		nodeType: model.NodeTypeMessage,
		waits:    true,
		render:   func(model.Node, model.Content) []string { return []string{"IMPOSTOR"} },
	}
	e := newEngine([]modules.Module{impostor}, engine.WithContentSource(failingSource()))

	st, outs, err := e.Enter(context.Background(), chainFlow(), model.Conversation{})
	if err != nil {
		t.Fatalf("Enter: %v (un message no resuelve contenido)", err)
	}
	if got := texts(outs); !slices.Equal(got, []string{"Hola.", "Bienvenido.", "Adiós."}) {
		t.Errorf("salidas = %q, quiero el Text de cada nodo", got)
	}
	if !st.Finished() {
		t.Errorf("CurrentNode = %q, quiero el centinela", st.CurrentNode)
	}
}

// banner es un módulo de SALIDA que declara un adjunto con la clave del nodo. Su
// tipo no es "media": el engine entra por capacidad, no por nombre.
func banner(emitErr error) emittingModule {
	return emittingModule{
		stubModule: stubModule{
			nodeType: typeBanner,
			render: func(node model.Node, _ model.Content) []string {
				if node.Text == "" {
					return nil
				}
				return []string{node.Text}
			},
		},
		emit: func(node model.Node, _ model.Content) (*model.MediaRef, error) {
			if emitErr != nil {
				return nil, emitErr
			}
			if node.Content == nil {
				return nil, nil
			}
			return &model.MediaRef{Key: node.Content.Key, Filename: "f.pdf", Mime: "application/pdf", Kind: "document", Caption: "hola"}, nil
		},
	}
}

func bannerNode(text, key string, next *string) model.Node {
	node := model.Node{Type: typeBanner, Text: text, Next: next}
	if key != "" {
		node.Content = &model.ContentRef{Source: "static", Key: key}
	}
	return node
}

// Un nodo de salida emite el texto de su Render, después su adjunto, y sigue por Next
// sin detenerse.
func TestEnter_OutputNodeEmitsTextThenMediaAndChains(t *testing.T) {
	flow := model.Flow{FlowID: "envio", Version: 1, Initial: "b1", Nodes: map[string]model.Node{
		"b1":   bannerNode("mira esto", "wapp/media/x.pdf", ptr("done")),
		"done": {Type: model.NodeTypeMessage, Text: "listo"},
	}}
	st, outs, err := newEngine([]modules.Module{banner(nil)}).Enter(context.Background(), flow, model.Conversation{})
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	if !st.Finished() {
		t.Errorf("CurrentNode = %q, quiero el centinela (un nodo de salida no espera)", st.CurrentNode)
	}
	if len(outs) != 3 {
		t.Fatalf("salidas = %+v, quiero texto del render, adjunto y texto terminal", outs)
	}
	if outs[0].Text != "mira esto" || outs[0].Media != nil {
		t.Errorf("outs[0] = %+v, quiero el texto del Render", outs[0])
	}
	want := model.MediaRef{Key: "wapp/media/x.pdf", Filename: "f.pdf", Mime: "application/pdf", Kind: "document", Caption: "hola"}
	if outs[1].Media == nil || *outs[1].Media != want || outs[1].Text != "" {
		t.Errorf("outs[1] = %+v, quiero solo el adjunto declarado, sin interpretar", outs[1])
	}
	if outs[2].Text != "listo" || outs[2].Media != nil {
		t.Errorf("outs[2] = %+v, quiero el texto terminal", outs[2])
	}
}

func TestEnter_OutputNodes(t *testing.T) {
	t.Run("without Next ends the flow", func(t *testing.T) {
		flow := model.Flow{FlowID: "envio", Version: 1, Initial: "b1", Nodes: map[string]model.Node{
			"b1": bannerNode("", "wapp/media/y.pdf", nil),
		}}
		st, outs, err := newEngine([]modules.Module{banner(nil)}).Enter(context.Background(), flow, model.Conversation{})
		if err != nil {
			t.Fatalf("Enter: %v", err)
		}
		if !st.Finished() || len(outs) != 1 || outs[0].Media == nil {
			t.Errorf("nodo = %q, salidas = %+v; quiero el fin y un solo adjunto", st.CurrentNode, outs)
		}
	})

	t.Run("nil media and empty render emit nothing", func(t *testing.T) {
		flow := model.Flow{FlowID: "envio", Version: 1, Initial: "b1", Nodes: map[string]model.Node{
			"b1": bannerNode("", "", nil),
		}}
		st, outs, err := newEngine([]modules.Module{banner(nil)}).Enter(context.Background(), flow, model.Conversation{})
		if err != nil || !st.Finished() || outs != nil {
			t.Errorf("nodo = %q, salidas = %+v, err = %v; quiero el fin sin salidas", st.CurrentNode, outs, err)
		}
	})
}

// La capacidad MediaEmitter es opcional, y solo se consulta en los nodos de salida.
func TestEnter_MediaCapabilityIsOptional(t *testing.T) {
	t.Run("output module without the capability just chains", func(t *testing.T) {
		plain := stubModule{nodeType: typeBanner}
		flow := model.Flow{FlowID: "envio", Version: 1, Initial: "b1", Nodes: map[string]model.Node{
			"b1": {Type: typeBanner, Prompt: "aviso", Next: ptr("b2")},
			"b2": {Type: typeBanner, Prompt: "otro"},
		}}
		st, outs, err := newEngine([]modules.Module{plain}).Enter(context.Background(), flow, model.Conversation{})
		if err != nil || !st.Finished() || !slices.Equal(texts(outs), []string{"aviso", "otro"}) {
			t.Errorf("nodo = %q, salidas = %q, err = %v", st.CurrentNode, texts(outs), err)
		}
	})

	t.Run("interactive module is never asked for media", func(t *testing.T) {
		interactive := banner(errors.New("no debe llamarse"))
		interactive.waits = true
		flow := model.Flow{FlowID: "envio", Version: 1, Initial: "b1", Nodes: map[string]model.Node{
			"b1": bannerNode("elige", "wapp/media/z.pdf", nil),
		}}
		st, outs, err := newEngine([]modules.Module{interactive}).Enter(context.Background(), flow, model.Conversation{})
		if err != nil || st.CurrentNode != "b1" || !slices.Equal(texts(outs), []string{"elige"}) {
			t.Errorf("nodo = %q, salidas = %+v, err = %v; quiero el render y la espera", st.CurrentNode, outs, err)
		}
	})
}

func TestEnter_Errors(t *testing.T) {
	errDescriptor := errors.New("descriptor inválido")
	mods := []modules.Module{choice(), banner(errDescriptor)}

	cases := map[string]struct {
		flow       model.Flow
		opts       []engine.Option
		wantDetail string
		wantCause  error
		wantOuts   []string
		wantNode   string
	}{
		"initial node does not exist": {
			flow:       model.Flow{Initial: "ghost", Nodes: map[string]model.Node{"a": {Type: typeChoice}}},
			wantDetail: `nodo "ghost" no existe en la definición`,
			wantNode:   "ghost",
		},
		"chain points to a missing node": {
			flow: model.Flow{Initial: "m1", Nodes: map[string]model.Node{
				"m1": {Type: model.NodeTypeMessage, Text: "Hola.", Next: ptr("ghost")},
			}},
			wantDetail: `nodo "ghost" no existe en la definición`,
			wantOuts:   []string{"Hola."},
			wantNode:   "ghost",
		},
		"unknown node type": {
			flow: model.Flow{Initial: "m1", Nodes: map[string]model.Node{
				"m1": {Type: model.NodeTypeMessage, Text: "Hola.", Next: ptr("n")},
				"n":  {Type: "carousel"},
			}},
			wantDetail: `nodo "n": tipo desconocido "carousel"`,
			wantOuts:   []string{"Hola."},
			wantNode:   "n",
		},
		"content cannot be resolved": {
			flow:       choiceFlow(typeChoice),
			opts:       []engine.Option{engine.WithContentSource(failingSource())},
			wantDetail: `resolver contenido de "root": catálogo no disponible`,
			wantCause:  errCatalogDown,
			wantNode:   "root",
		},
		// El texto del Render ya salió cuando el adjunto falla.
		"media cannot be emitted": {
			flow: model.Flow{Initial: "b1", Nodes: map[string]model.Node{
				"b1": bannerNode("mira esto", "k", nil),
			}},
			wantDetail: `emitir media de "b1": descriptor inválido`,
			wantCause:  errDescriptor,
			wantOuts:   []string{"mira esto"},
			wantNode:   "b1",
		},
		// Dos nodos: se visitan tres (len+1) y se corta.
		"message cycle": {
			flow: model.Flow{Initial: "m1", Nodes: map[string]model.Node{
				"m1": {Type: model.NodeTypeMessage, Text: "uno", Next: ptr("m2")},
				"m2": {Type: model.NodeTypeMessage, Text: "dos", Next: ptr("m1")},
			}},
			wantDetail: `cadena de mensajes demasiado larga (¿ciclo?) desde "m2"`,
			wantOuts:   []string{"uno", "dos", "uno"},
			wantNode:   "m2",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			st, outs, err := newEngine(mods, tc.opts...).Enter(context.Background(), tc.flow, model.Conversation{})
			assertInvalidFlow(t, err, tc.wantDetail)
			if tc.wantCause != nil && !errors.Is(err, tc.wantCause) {
				t.Errorf("err = %v, quiero que también envuelva la causa %v", err, tc.wantCause)
			}
			if got := texts(outs); !slices.Equal(got, tc.wantOuts) {
				t.Errorf("salidas = %q, quiero las acumuladas hasta el fallo: %q", got, tc.wantOuts)
			}
			if st.CurrentNode != tc.wantNode {
				t.Errorf("CurrentNode = %q, quiero el nodo que falló: %q", st.CurrentNode, tc.wantNode)
			}
		})
	}
}

// Una cadena que visita cada nodo UNA vez no es un ciclo, por larga que sea.
func TestEnter_ChainVisitingEveryNodeOnceIsNotACycle(t *testing.T) {
	const length = 40
	nodes := make(map[string]model.Node, length)
	want := make([]string, 0, length)
	for i := range length {
		node := model.Node{Type: model.NodeTypeMessage, Text: fmt.Sprintf("paso %d", i)}
		if i < length-1 {
			node.Next = ptr(fmt.Sprintf("m%d", i+1))
		}
		nodes[fmt.Sprintf("m%d", i)] = node
		want = append(want, node.Text)
	}
	flow := model.Flow{FlowID: "largo", Version: 1, Initial: "m0", Nodes: nodes}

	st, outs, err := newEngine(nil).Enter(context.Background(), flow, model.Conversation{})
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	if !st.Finished() || !slices.Equal(texts(outs), want) {
		t.Errorf("nodo = %q, %d salidas; quiero el fin y las %d", st.CurrentNode, len(outs), length)
	}
}
