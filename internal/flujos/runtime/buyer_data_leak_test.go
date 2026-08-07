// buyer_data_leak_test.go es la prueba de que los datos del comprador NO llegan al
// outbox de eventos (Plan 041 · T4.5, D-041.13 / INV-04).
//
// Por qué está en el paquete del runtime y no en el del carrito: la garantía no es
// del módulo —el módulo emite el efecto con el valor dentro, tiene que hacerlo— sino
// del PersistSink, que es quien decide qué se escribe en public.flow_events. La
// única forma honesta de comprobarlo es montar el sink de verdad, hacerle pasar la
// conversación entera y luego BUSCAR el valor en lo que quedó escrito.
//
// No hay ningún grep sobre el código en este archivo. Se serializa lo persistido y
// se busca el literal dentro.
package runtime_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// El valor que teclea el cliente es un literal RARO para poder buscarlo como
// subcadena sin falsos positivos.
const rutDelClienteEnElSink = "9.876.543-Z-qqz"

// catálogoDelCarrito es el snapshot mínimo que el carrito navega, con la MISMA
// forma que model.Content.Raw (lo siembra el engine antes del Step).
func catálogoDelCarrito() map[string]any {
	return map[string]any{
		"categories": []any{
			map[string]any{
				"code": "1", "label": "Bebidas",
				"items": []any{
					map[string]any{"code": "1", "sku": "cafe", "label": "Café", "price": 2.5},
				},
			},
		},
	}
}

// TestBuyerData_NoLlegaAFlowEvents conduce la compra COMPLETA con checklist a
// través del sink real y comprueba las dos mitades del trato:
//
//	(a) el valor del comprador NO aparece en ninguna fila de flow_events;
//	(b) sí acabó guardado, por su propio camino (el escritor de datos del comprador).
//
// La (b) importa tanto como la (a): un sink que se tragara el efecto entero también
// pasaría la (a), y habría perdido el dato del cliente.
func TestBuyerData_NoLlegaAFlowEvents(t *testing.T) {
	repo := store.NewMemoryRepository()
	buyer := intakes.NewMemoryStore()
	sink := runtime.NewPersistSink(repo,
		cart.NewProjector(repo, intakes.NewMemoryStore(), sinEnvío{}, buyer))

	m := cart.New()
	vars := map[string]any{
		modules.VarContentRaw: catálogoDelCarrito(),
		cart.VarBuyerFields: []store.BuyerField{
			{Key: "rut", Label: "RUT", Required: true},
		},
	}
	ec := persistEC()
	ctx := context.Background()

	// 1 → Bebidas · 1 → Café · 2 → Agregar · 1 → cantidad · 2 → Finalizar ·
	// 1 → Confirmar (abre el checklist) · el RUT (captura y cierra).
	guion := []string{"1", "1", "2", "1", "2", "1", rutDelClienteEnElSink}
	for i, entrada := range guion {
		res := m.Step(model.Node{}, model.Conversation{Vars: vars}, entrada)
		vars = res.Vars
		for _, eff := range res.Effects {
			if err := sink.Handle(ctx, ec, eff); err != nil {
				t.Fatalf("Handle del efecto %q (paso %d): %v", eff.Name, i+1, err)
			}
		}
	}

	// (a) Barrido del outbox: se serializan TODAS las filas y se busca el valor.
	eventos := repo.FlowEvents()
	if len(eventos) == 0 {
		t.Fatalf("el recorrido no escribió ni un flow_event: el barrido no probaría nada")
	}
	blob, err := json.Marshal(eventos)
	if err != nil {
		t.Fatalf("serializando los flow_events: %v", err)
	}
	if strings.Contains(string(blob), rutDelClienteEnElSink) {
		t.Fatalf("FUGA: el dato del comprador acabó en public.flow_events (outbox append-only EN CLARO):\n%s", blob)
	}
	for _, ev := range eventos {
		if ev.Name == cart.EffectBuyerDataCaptured {
			t.Fatalf("FUGA: se escribió una fila de flow_events con nombre %q", ev.Name)
		}
	}

	// (b) …y el dato sí se guardó por su camino, en la solicitud que el carrito abrió.
	solicitudes := repo.Intakes()
	if len(solicitudes) != 1 {
		t.Fatalf("solicitudes proyectadas: %d, esperaba 1", len(solicitudes))
	}
	guardado := buyer.BuyerDataOf(solicitudes[0].ID)
	if guardado["rut"] != rutDelClienteEnElSink {
		t.Fatalf("el dato del comprador no se guardó en la solicitud %s: %+v", solicitudes[0].ID, guardado)
	}
}

// TestPersistSink_KindPrivate_NoEscribeEventoPeroProyecta aísla la regla del sink
// del recorrido del carrito: un efecto con Kind modules.KindPrivate no produce fila
// en flow_events y SÍ llega al proyector. Es la regla de la plataforma, no del
// módulo, y por eso se prueba con un efecto sintético.
func TestPersistSink_KindPrivate_NoEscribeEventoPeroProyecta(t *testing.T) {
	repo := store.NewMemoryRepository()
	espía := &proyectorEspía{}
	sink := runtime.NewPersistSink(repo, espía)

	eff := modules.Effect{
		Kind:    modules.KindPrivate,
		Name:    "algo_personal",
		Payload: map[string]any{"value": "no-debe-persistirse"},
	}
	if err := sink.Handle(context.Background(), persistEC(), eff); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if evs := repo.FlowEvents(); len(evs) != 0 {
		t.Fatalf("un efecto KindPrivate escribió %d fila(s) en flow_events: %+v", len(evs), evs)
	}
	if espía.vistos != 1 {
		t.Fatalf("el proyector recibió %d efectos, esperaba 1: sin proyección el dato se pierde", espía.vistos)
	}
}

// TestPersistSink_KindNoPrivado_SigueEscribiendo es la otra mitad: la excepción es
// SOLO para KindPrivate. Sin este test, "no escribir nada nunca" también pasaría el
// anterior.
func TestPersistSink_KindNoPrivado_SigueEscribiendo(t *testing.T) {
	repo := store.NewMemoryRepository()
	sink := runtime.NewPersistSink(repo, &proyectorEspía{})

	for _, kind := range []string{"event", "persist", ""} {
		eff := modules.Effect{Kind: kind, Name: "algo_de_negocio", Payload: map[string]any{"a": 1}}
		if err := sink.Handle(context.Background(), persistEC(), eff); err != nil {
			t.Fatalf("Handle(kind=%q): %v", kind, err)
		}
	}
	if evs := repo.FlowEvents(); len(evs) != 3 {
		t.Fatalf("flow_events escritos: %d, esperaba 3 (uno por kind no privado)", len(evs))
	}
}

// proyectorEspía cuenta los efectos que le llegan. Acepta cualquier nombre: lo que
// se mide es si el sink delega, no qué materializa.
type proyectorEspía struct{ vistos int }

func (proyectorEspía) Handles(string) bool { return true }

func (p *proyectorEspía) Project(context.Context, modules.EffectMeta, modules.Effect) error {
	p.vistos++
	return nil
}
