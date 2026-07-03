package engine_test

import (
	"context"
	"errors"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
)

// fakeEmit es un módulo de SALIDA (no interactivo) que DECLARA un adjunto vía
// modules.MediaEmitter. Su Type es "banner" —NO "media"— a propósito: prueba que
// renderFrom trata a CUALQUIER módulo con WaitsForInput()==false como "emite y
// avanza" y transporta su MediaRef en Output.Media SIN switch por node.Type. El
// mecanismo es GENÉRICO por capacidad (igual que el módulo real "media" de T3).
type fakeEmit struct {
	err error // si != nil, EmitMedia lo devuelve (prueba el error controlado)
}

func (fakeEmit) Type() string                                  { return "banner" }
func (fakeEmit) WaitsForInput() bool                           { return false }
func (fakeEmit) Render(_ model.Node, _ model.Content) []string { return nil }

func (fakeEmit) Step(_ model.Node, conv model.Conversation, _ string) modules.Result {
	return modules.Result{Vars: conv.Vars}
}

func (f fakeEmit) EmitMedia(node model.Node, _ model.Content) (*model.MediaRef, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &model.MediaRef{
		Key:      node.Content.Key,
		Filename: "f.pdf",
		Mime:     "application/pdf",
		Kind:     "document",
		Caption:  "hola",
	}, nil
}

func newEngineWithEmit(mod fakeEmit) *engine.Engine {
	reg := modules.NewRegistry()
	reg.Register(mod)
	return engine.New(reg)
}

func bannerNode(key string, next *string) model.Node {
	return model.Node{Type: "banner", Content: &model.ContentRef{Source: "static", Key: key}, Next: next}
}

// TestEmitNodeChainsAndCarriesMedia comprueba §9.A/§9.C con el seam genérico: un
// nodo de salida (banner) emite su MediaRef en Output.Media y ENCADENA por Next
// hasta un message terminal, sin detenerse (no interactivo). El engine no conoce
// el tipo: entra por capacidad.
func TestEmitNodeChainsAndCarriesMedia(t *testing.T) {
	done := "done"
	flow := model.Flow{
		FlowID:  "envio-pdf",
		Version: 1,
		Initial: "b1",
		Nodes: map[string]model.Node{
			"b1":   bannerNode("wapp/media/x.pdf", &done),
			"done": {Type: model.NodeTypeMessage, Text: "listo", Next: nil},
		},
	}

	st, outs, err := newEngineWithEmit(fakeEmit{}).Enter(context.Background(), flow, model.Conversation{})
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	if !st.Finished() {
		t.Fatalf("tras emitir y encadenar a message terminal esperaba Finished; node=%q", st.CurrentNode)
	}
	if len(outs) != 2 {
		t.Fatalf("esperaba 2 outputs (adjunto + texto terminal), got=%d: %+v", len(outs), outs)
	}
	if outs[0].Media == nil {
		t.Fatalf("outs[0] debe transportar el adjunto en Output.Media, got=%+v", outs[0])
	}
	if outs[0].Media.Key != "wapp/media/x.pdf" || outs[0].Text != "" {
		t.Fatalf("Output.Media mal formado o con texto suelto: %+v", outs[0])
	}
	if outs[1].Media != nil || outs[1].Text != "listo" {
		t.Fatalf("outs[1] debe ser el texto terminal sin adjunto, got=%+v", outs[1])
	}
}

// TestEmitTerminalNode comprueba que un nodo de salida SIN Next termina el flujo
// (centinela) tras emitir su adjunto.
func TestEmitTerminalNode(t *testing.T) {
	flow := model.Flow{
		FlowID:  "envio-pdf-fin",
		Version: 1,
		Initial: "b1",
		Nodes:   map[string]model.Node{"b1": bannerNode("wapp/media/y.pdf", nil)},
	}

	st, outs, err := newEngineWithEmit(fakeEmit{}).Enter(context.Background(), flow, model.Conversation{})
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	if !st.Finished() {
		t.Fatalf("nodo de salida sin Next debe terminar el flujo; node=%q", st.CurrentNode)
	}
	if len(outs) != 1 || outs[0].Media == nil {
		t.Fatalf("esperaba 1 output con adjunto, got=%+v", outs)
	}
}

// TestEmitErrorIsControlled comprueba que un error del emisor (descriptor inválido)
// se propaga CONTROLADO por renderFrom (envuelve ErrInvalidFlow), no un pánico.
func TestEmitErrorIsControlled(t *testing.T) {
	flow := model.Flow{
		FlowID:  "envio-pdf-err",
		Version: 1,
		Initial: "b1",
		Nodes:   map[string]model.Node{"b1": bannerNode("", nil)},
	}
	badErr := model.ErrInvalidFlow
	_, _, err := newEngineWithEmit(fakeEmit{err: badErr}).Enter(context.Background(), flow, model.Conversation{})
	if err == nil || !errors.Is(err, model.ErrInvalidFlow) {
		t.Fatalf("esperaba error controlado (ErrInvalidFlow), got: %v", err)
	}
}
