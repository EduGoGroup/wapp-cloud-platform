//go:build pendiente

package engine_test

// engine_enter_primed_test.go — EnterPrimed: la pre-carga por la señal de intención
// y su barrido cuando nadie la consume (REQ-18).
//
// 🔒 QUÉ SE ESTÁ PROTEGIENDO. VarIntentParams y VarIntentName llevan TEXTO EXTRAÍDO
// DEL MENSAJE DEL CLIENTE. Si nadie las consume y nadie las barre, acaban en claro
// en el JSONB de public.flow_state. La fuga es la del DISCO, no la del struct.

import (
	"context"
	"maps"
	"reflect"
	"slices"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
)

// signalVars arma unas Vars con la señal de intención MÁS una variable de negocio:
// el barrido quita las dos claves y no se lleva nada más.
func signalVars() map[string]any {
	return map[string]any{
		modules.VarIntentParams: map[string]any{"producto": "empanada de pino"},
		modules.VarIntentName:   "comprar",
		"previous_order":        "abc-123",
	}
}

func orderFlow() model.Flow {
	return model.Flow{FlowID: "compra", Version: 4, Initial: "root", Nodes: map[string]model.Node{
		"root": {Type: typeOrder, Prompt: "catálogo"},
	}}
}

// order devuelve un módulo interactivo con capacidad Primer.
func order(prime func(model.Node, model.Content, map[string]any) (modules.Result, bool)) primingModule {
	return primingModule{stubModule: stubModule{nodeType: typeOrder, waits: true, durable: true}, prime: prime}
}

// neverPrimes falla el test si el engine llega a llamar a Prime.
func neverPrimes(t *testing.T) primingModule {
	return order(func(model.Node, model.Content, map[string]any) (modules.Result, bool) {
		t.Error("Prime se llamó y no debía")
		return modules.Result{}, false
	})
}

// Sin señal, EnterPrimed es Enter: mismo estado, mismas salidas, efectos nil.
func TestEnterPrimed_WithoutSignalEqualsEnter(t *testing.T) {
	e := newEngine([]modules.Module{choice(), neverPrimes(t)})
	for name, flow := range map[string]model.Flow{"interactive": choiceFlow(typeChoice), "chain": chainFlow(), "primer": orderFlow()} {
		in := model.Conversation{TenantID: "tenant-a", Vars: map[string]any{"previous_order": "abc-123"}}
		entered, enterOuts, err := e.Enter(context.Background(), flow, in)
		if err != nil {
			t.Fatalf("%s: Enter: %v", name, err)
		}
		primed, primedOuts, effects, err := e.EnterPrimed(context.Background(), flow, in)
		if err != nil {
			t.Fatalf("%s: EnterPrimed: %v", name, err)
		}
		if !reflect.DeepEqual(primed, entered) || !reflect.DeepEqual(primedOuts, enterOuts) || effects != nil {
			t.Errorf("%s: EnterPrimed = %+v / %v / %v; quiero lo mismo que Enter y efectos nil", name, primed, primedOuts, effects)
		}
	}
}

func TestEnterPrimed_ModuleConsumesTheSignal(t *testing.T) {
	var gotContent model.Content
	var gotNode model.Node
	var gotVars map[string]any
	mod := order(func(node model.Node, resolved model.Content, vars map[string]any) (modules.Result, bool) {
		gotNode, gotContent, gotVars = node, resolved, vars
		return modules.Result{
			Next:    ptr("elsewhere"), // el módulo se queda en el nodo inicial igualmente
			Vars:    map[string]any{"cart": "primed"},
			Outputs: []string{"¿Confirmas 1 empanada de pino?"},
			Effects: []modules.Effect{{Kind: "event", Name: "item_added"}},
		}, true
	})
	var tenants []string
	src := sourceFunc(func(_ context.Context, tenantID string, node model.Node) (model.Content, error) {
		tenants = append(tenants, tenantID)
		return model.Content{Prompt: "resuelto:" + node.Prompt}, nil
	})
	caller := signalVars()
	in := model.Conversation{TenantID: "tenant-a", FlowID: "viejo", CurrentNode: "otro", Vars: caller}

	st, outs, effects, err := newEngine([]modules.Module{mod}, engine.WithContentSource(src)).EnterPrimed(context.Background(), orderFlow(), in)
	if err != nil {
		t.Fatalf("EnterPrimed: %v", err)
	}
	if st.CurrentNode != "root" || st.FlowID != "compra" || st.FlowVersion != 4 {
		t.Errorf("estado = %+v, quiero el nodo inicial del flujo", st)
	}
	if !maps.Equal(st.Vars, map[string]any{"cart": "primed"}) {
		t.Errorf("Vars = %v, quiero las que devolvió Prime", st.Vars)
	}
	if !slices.Equal(texts(outs), []string{"¿Confirmas 1 empanada de pino?"}) {
		t.Errorf("salidas = %q, quiero las de Prime, sin Render", texts(outs))
	}
	if len(effects) != 1 || effects[0].Name != "item_added" {
		t.Errorf("efectos = %v, quiero los que declaró Prime", effects)
	}
	// Al Primer le llegan el contenido ya resuelto del nodo inicial y las Vars con la señal.
	if gotNode.Type != typeOrder || gotContent.Prompt != "resuelto:catálogo" || !slices.Equal(tenants, []string{"tenant-a"}) {
		t.Errorf("Prime vio nodo %+v y contenido %+v (tenants %v)", gotNode, gotContent, tenants)
	}
	if !reflect.DeepEqual(gotVars, signalVars()) {
		t.Errorf("Prime vio las Vars %v, quiero las recibidas con la señal", gotVars)
	}
	if !reflect.DeepEqual(caller, signalVars()) || in.CurrentNode != "otro" {
		t.Errorf("el estado recibido cambió: %+v", in)
	}
}

// Rareza portada: si el módulo consume y NO limpia, el engine no barre por él.
func TestEnterPrimed_HandledVarsAreTheModulesAsIs(t *testing.T) {
	mod := order(func(_ model.Node, _ model.Content, vars map[string]any) (modules.Result, bool) {
		return modules.Result{Vars: vars}, true
	})
	st, outs, effects, err := newEngine([]modules.Module{mod}).EnterPrimed(context.Background(), orderFlow(), model.Conversation{Vars: signalVars()})
	if err != nil || outs != nil || effects != nil {
		t.Fatalf("salidas = %v, efectos = %v, err = %v", outs, effects, err)
	}
	if !reflect.DeepEqual(st.Vars, signalVars()) {
		t.Errorf("Vars = %v, quiero las del módulo tal cual (limpiar la señal es cosa suya)", st.Vars)
	}
}

// Las seis ramas por las que nadie consume la señal: en todas se barre.
func TestEnterPrimed_UnconsumedSignalIsStripped(t *testing.T) {
	declines := order(func(model.Node, model.Content, map[string]any) (modules.Result, bool) {
		// Lo que devuelva junto a handled=false no cuenta.
		return modules.Result{
			Vars:    map[string]any{"cart": "ignored"},
			Outputs: []string{"ignorada"},
			Effects: []modules.Effect{{Kind: "event", Name: "ignored"}},
		}, false
	})
	onlyName := signalVars()
	delete(onlyName, modules.VarIntentParams)

	cases := map[string]struct {
		mods       []modules.Module
		opts       []engine.Option
		flow       model.Flow
		vars       map[string]any
		wantDetail string // "" = sin error
		wantOuts   []string
		wantNode   string
	}{
		"only the intent name travels": {
			mods: []modules.Module{neverPrimes(t)}, flow: orderFlow(), vars: onlyName,
			wantOuts: []string{"catálogo"}, wantNode: "root",
		},
		"initial node does not exist": {
			mods: []modules.Module{neverPrimes(t)}, flow: model.Flow{Initial: "ghost"}, vars: signalVars(),
			wantDetail: `nodo "ghost" no existe en la definición`, wantNode: "ghost",
		},
		"module is not registered": {
			flow: orderFlow(), vars: signalVars(),
			wantDetail: `nodo "root": tipo desconocido "order"`, wantNode: "root",
		},
		"initial message node": {
			flow: chainFlow(), vars: signalVars(),
			wantOuts: []string{"Hola.", "Bienvenido.", "Adiós."}, wantNode: model.NodeTerminal,
		},
		"module has no Primer capability": {
			mods: []modules.Module{choice()}, flow: choiceFlow(typeChoice), vars: signalVars(),
			wantOuts: []string{rootPrompt}, wantNode: "root",
		},
		// El módulo SÍ sabría consumir, pero Prime nunca llega a correr.
		"content cannot be resolved": {
			mods: []modules.Module{neverPrimes(t)}, opts: []engine.Option{engine.WithContentSource(failingSource())},
			flow: orderFlow(), vars: signalVars(),
			wantDetail: `resolver contenido de "root": catálogo no disponible`, wantNode: "root",
		},
		"Prime declines": {
			mods: []modules.Module{declines}, flow: orderFlow(), vars: signalVars(),
			wantOuts: []string{"catálogo"}, wantNode: "root",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			caller := maps.Clone(tc.vars)
			st, outs, effects, err := newEngine(tc.mods, tc.opts...).EnterPrimed(context.Background(), tc.flow, model.Conversation{Vars: caller})
			if tc.wantDetail == "" && err != nil {
				t.Fatalf("EnterPrimed: %v", err)
			}
			if tc.wantDetail != "" {
				assertInvalidFlow(t, err, tc.wantDetail)
			}
			if !maps.Equal(st.Vars, map[string]any{"previous_order": "abc-123"}) {
				t.Errorf("Vars = %v: la señal que nadie consumió acabaría en claro en flow_state (REQ-18); "+
					"quiero solo previous_order", st.Vars)
			}
			if !slices.Equal(texts(outs), tc.wantOuts) || st.CurrentNode != tc.wantNode || effects != nil {
				t.Errorf("nodo = %q, salidas = %q, efectos = %v; quiero %q, %q y efectos nil",
					st.CurrentNode, texts(outs), effects, tc.wantNode, tc.wantOuts)
			}
			if st.FlowID != tc.flow.FlowID || st.FlowVersion != tc.flow.Version {
				t.Errorf("estado = %+v, quiero el flujo y la versión de la definición", st)
			}
			if !reflect.DeepEqual(caller, tc.vars) {
				t.Errorf("mapa del llamante = %v, quiero intacto: %v", caller, tc.vars)
			}
		})
	}
}
