// customer_note_leak_test.go es la prueba de que la indicación del PEDIDO no llega
// al outbox de eventos (defecto A2 del cierre del Plan 041 / INV-04).
//
// Es el hermano de buyer_data_leak_test.go, y existe porque aquel no bastaba. La
// asimetría de note_added se cumplía al pie de la letra —el ámbito "order" publica
// el LARGO del texto, nunca el texto— y el literal entraba igualmente en
// flow_events por la otra puerta: dentro del payload de cart_closed, que es
// kindPersist y por tanto SÍ se escribe. El requisito cumplía su letra y no su
// propósito. Confirmado en producción durante el e2e del 041: un cart_closed con
// «Dejarlo en portería» en claro, mientras el RUT salía 0 de 16.
//
// La diferencia con el caso del comprador es que aquí NO se puede callar el efecto
// entero: cart_closed lleva las líneas y el total, que son negocio legítimo y
// tienen que quedar registrados. Lo que se poda es UNA clave, y por eso el test
// comprueba las tres cosas: que el literal no está, que el evento sí está con su
// contenido de negocio, y que la nota llegó a su columna.
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

// La indicación del cliente es la PII de manual: una dirección. El literal es raro
// para poder buscarlo como subcadena sin falsos positivos.
const notaDelPedidoEnElSink = "dejarlo en porteria calle Mayor 14 qqz"

// TestCustomerNote_NoLlegaAFlowEvents conduce la compra completa con indicación de
// pedido a través del sink real y comprueba las TRES mitades del trato:
//
//	(a) el literal de la nota NO aparece en ninguna fila de flow_events;
//	(b) el evento cart_closed SÍ se escribió, con sus líneas y su total;
//	(c) la nota sí acabó guardada, en la columna de la solicitud.
//
// La (b) y la (c) importan tanto como la (a): un sink que se tragara el efecto
// entero pasaría la (a) y habría perdido a la vez el registro del cierre y la
// indicación que quien prepara el pedido necesita leer.
func TestCustomerNote_NoLlegaAFlowEvents(t *testing.T) {
	repo := store.NewMemoryRepository()
	sink := runtime.NewPersistSink(repo,
		cart.NewProjector(repo, intakes.NewMemoryStore(), sinEnvío{}, intakes.NewMemoryStore()))

	m := cart.New()
	vars := map[string]any{modules.VarContentRaw: catálogoDelCarrito()}
	ec := persistEC()
	ctx := context.Background()

	// 1 → Bebidas · 1 → Café · 2 → Agregar · 1 → cantidad · 2 → Finalizar ·
	// 3 → indicación para todo el pedido · el texto · 1 → Confirmar (cierra).
	guion := []string{"1", "1", "2", "1", "2", "3", notaDelPedidoEnElSink, "1"}
	for i, entrada := range guion {
		res := m.Step(model.Node{}, model.Conversation{Vars: vars}, entrada)
		vars = res.Vars
		for _, eff := range res.Effects {
			if err := sink.Handle(ctx, ec, eff); err != nil {
				t.Fatalf("Handle del efecto %q (paso %d): %v", eff.Name, i+1, err)
			}
		}
	}

	// (a) Barrido del outbox: se serializan TODAS las filas y se busca el literal.
	eventos := repo.FlowEvents()
	if len(eventos) == 0 {
		t.Fatalf("el recorrido no escribió ni un flow_event: el barrido no probaría nada")
	}
	blob, err := json.Marshal(eventos)
	if err != nil {
		t.Fatalf("serializando los flow_events: %v", err)
	}
	if strings.Contains(string(blob), notaDelPedidoEnElSink) {
		t.Fatalf("FUGA: la indicación del pedido acabó en public.flow_events "+
			"(outbox append-only EN CLARO y sin poda):\n%s", blob)
	}

	// (b) …pero el cierre SÍ está, y con su contenido de negocio intacto.
	var cerrado *store.FlowEvent
	for i := range eventos {
		if eventos[i].Name == cart.EffectCartClosed {
			cerrado = &eventos[i]
		}
	}
	if cerrado == nil {
		t.Fatalf("no se escribió el evento %q: podar una clave no es dejar de registrar el cierre",
			cart.EffectCartClosed)
	}
	if _, hay := cerrado.Payload["customer_note"]; hay {
		t.Fatalf("la clave customer_note sigue en el payload persistido: %v", cerrado.Payload)
	}
	if cerrado.Payload["items"] == nil || cerrado.Payload["total"] == nil {
		t.Fatalf("la poda se llevó por delante las líneas o el total: %v", cerrado.Payload)
	}

	// (c) …y la nota sí se guardó por su camino, en la solicitud que el carrito cerró.
	solicitudes := repo.Intakes()
	if len(solicitudes) != 1 {
		t.Fatalf("solicitudes proyectadas: %d, esperaba 1", len(solicitudes))
	}
	if solicitudes[0].CustomerNote != notaDelPedidoEnElSink {
		t.Fatalf("la indicación no llegó a la solicitud (quien prepara no la leería): %q",
			solicitudes[0].CustomerNote)
	}
}

// TestPersistSink_PrivateKeys_PodaLaClavePeroProyectaEntero aísla la regla del sink
// del recorrido del carrito, igual que su equivalente de KindPrivate: la clave
// declarada no se escribe, el resto del payload sí, y el proyector recibe el efecto
// COMPLETO. Ese último punto es el que hace utilizable el mecanismo — si el
// proyector viera el payload podado, la clave no tendría dónde ir.
func TestPersistSink_PrivateKeys_PodaLaClavePeroProyectaEntero(t *testing.T) {
	repo := store.NewMemoryRepository()
	espía := &proyectorFisgón{}
	sink := runtime.NewPersistSink(repo, espía)

	eff := modules.Effect{
		Kind:        "persist",
		Name:        "algo_de_negocio_con_una_pizca_de_pii",
		PrivateKeys: []string{"secreto"},
		Payload:     map[string]any{"publico": "sí", "secreto": "no-debe-persistirse"},
	}
	if err := sink.Handle(context.Background(), persistEC(), eff); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	evs := repo.FlowEvents()
	if len(evs) != 1 {
		t.Fatalf("flow_events escritos: %d, esperaba 1", len(evs))
	}
	if _, hay := evs[0].Payload["secreto"]; hay {
		t.Fatalf("la clave declarada privada se escribió igual: %v", evs[0].Payload)
	}
	if evs[0].Payload["publico"] != "sí" {
		t.Fatalf("la poda se llevó lo que no era suyo: %v", evs[0].Payload)
	}
	if espía.ultimo.Payload["secreto"] != "no-debe-persistirse" {
		t.Fatalf("el proyector debe recibir el efecto ENTERO: %v", espía.ultimo.Payload)
	}

	// El Payload ORIGINAL no se toca: lo comparten todos los sinks del fan-out (es el
	// mismo puntero), así que vaciarlo aquí se lo quitaría al webhook del CRM.
	if eff.Payload["secreto"] != "no-debe-persistirse" {
		t.Fatalf("PublicPayload MUTÓ el payload compartido: %v", eff.Payload)
	}
}

// proyectorFisgón guarda el último efecto recibido para poder afirmar QUÉ le llegó,
// no solo cuántos. Acepta cualquier nombre.
type proyectorFisgón struct{ ultimo modules.Effect }

func (proyectorFisgón) Handles(string) bool { return true }

func (p *proyectorFisgón) Project(_ context.Context, _ modules.EffectMeta, eff modules.Effect) error {
	p.ultimo = eff
	return nil
}
