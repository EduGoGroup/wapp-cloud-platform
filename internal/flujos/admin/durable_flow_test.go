package admin_test

// durable_flow_test.go — Plan 054 · F3 (D-054.6/D-054.8). Dos frentes:
//
//  1. EngineDurableFlowChecker: el adapter que junta store.DefinitionReader con
//     (*engine.Engine).FlowProducesDurableContent (F1) — verificado contra el
//     motor y el store REALES (no un doble), incluido el fail-open de H2 ante un
//     flow_id inexistente.
//  2. stubDurableChecker/erroringDurableChecker: los dobles que triggers_test.go
//     y triggers_durable_test.go usan para ejercitar T2.6/T2.7 sin levantar un
//     Registry completo en cada caso.

import (
	"context"
	"errors"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/admin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/menu"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// errFalloDeliberado es el error que erroringDurableChecker devuelve en los tests
// que ejercitan la rama 500 (checker que no puede responder, DISTINTO de "flujo
// inexistente").
var errFalloDeliberado = errors.New("fallo deliberado del checker (test)")

// stubDurableChecker es un DurableFlowChecker de prueba: durable[flowID] dice si
// ese flow_id se considera con contenido durable. Un flow_id ausente del mapa se
// trata como NO durable — el mismo fail-open que EngineDurableFlowChecker aplica
// a un flujo inexistente (H2 del contrato de entrada de este frente) — así que un
// test no tiene que sembrar cada flow_id que toca de refilón.
type stubDurableChecker map[string]bool

func (s stubDurableChecker) FlowHasDurableContent(_ context.Context, _, flowID string) (bool, error) {
	return s[flowID], nil
}

// erroringDurableChecker siempre falla: ejercita la rama 500 de los tres
// handlers cuando el checker no puede responder (algo DISTINTO de "flujo
// inexistente", que es fail-open y nunca llega aquí).
type erroringDurableChecker struct{ err error }

func (e erroringDurableChecker) FlowHasDurableContent(_ context.Context, _, _ string) (bool, error) {
	return false, e.err
}

// newDurableFlowFixture siembra dos definiciones bajo ctxTenant: "menu-puro" (un
// nodo menu, NO durable) y "carrito-durable" (un nodo cart, durable —
// cart.Module.ProducesDurableContent() devuelve true a secas). No hace falta
// sembrar tenant_content: FlowProducesDurableContent solo mira n.Type contra el
// Registry, nunca resuelve Content.
func newDurableFlowFixture(t *testing.T) (*store.MemoryRepository, *engine.Engine) {
	t.Helper()
	repo := store.NewMemoryRepository()
	reg := modules.NewRegistry()
	reg.Register(menu.New())
	reg.Register(cart.New())
	eng := engine.New(reg)
	ctx := context.Background()
	if _, err := repo.InsertDefinition(ctx, ctxTenant, model.Flow{
		FlowID:  "menu-puro",
		Initial: "root",
		Nodes: map[string]model.Node{
			"root": {Type: model.NodeTypeMenu, Prompt: "1) A", Options: map[string]string{"1": "a"}},
			"a":    {Type: model.NodeTypeMessage, Text: "ok"},
		},
	}); err != nil {
		t.Fatalf("sembrar menu-puro: %v", err)
	}
	if _, err := repo.InsertDefinition(ctx, ctxTenant, model.Flow{
		FlowID:  "carrito-durable",
		Initial: "cart",
		Nodes: map[string]model.Node{
			"cart": {Type: "cart"},
		},
	}); err != nil {
		t.Fatalf("sembrar carrito-durable: %v", err)
	}
	return repo, eng
}

// TestEngineDurableFlowChecker_DurableYNoDurable: el predicado distingue el flujo
// con nodo cart del flujo de solo menú, resolviendo LatestDefinition de verdad.
func TestEngineDurableFlowChecker_DurableYNoDurable(t *testing.T) {
	repo, eng := newDurableFlowFixture(t)
	checker := admin.NewEngineDurableFlowChecker(repo, eng)

	durable, err := checker.FlowHasDurableContent(context.Background(), ctxTenant, "carrito-durable")
	if err != nil || !durable {
		t.Fatalf("carrito-durable: durable=%v err=%v, quiero true/nil", durable, err)
	}
	notDurable, err := checker.FlowHasDurableContent(context.Background(), ctxTenant, "menu-puro")
	if err != nil || notDurable {
		t.Fatalf("menu-puro: durable=%v err=%v, quiero false/nil", notDurable, err)
	}
}

// TestEngineDurableFlowChecker_FailOpen_FlujoInexistente (H2 del contrato de
// entrada de este frente): un flow_id que no existe en absoluto se trata como NO
// durable, SIN error. Este plan no introduce validación de existencia de flujo —
// el README dice que el CRUD «no resuelve la configuración por el usuario» — así
// que un flow_id inventado sigue sin bloquear nada por esta vía.
func TestEngineDurableFlowChecker_FailOpen_FlujoInexistente(t *testing.T) {
	repo, eng := newDurableFlowFixture(t)
	checker := admin.NewEngineDurableFlowChecker(repo, eng)

	durable, err := checker.FlowHasDurableContent(context.Background(), ctxTenant, "no-existe")
	if err != nil {
		t.Fatalf("fail-open: err=%v, quiero nil", err)
	}
	if durable {
		t.Fatalf("fail-open: durable=true, quiero false (flujo inexistente ⇒ no durable)")
	}
}
