package runtime

// webhook_sink_revision_test.go — el `revision_no` que viaja al puente CRM sale
// del efecto y llega ENTERO al cuerpo que se encola en webhook_outbox
// (Plan 044 · T4.10, mitad 1, criterios (a) y (b)).
//
// Se mira el CUERPO, no el log ni la struct: lo que el puente aplica es el JSON de
// webhook_outbox.payload, y el worker entrega este campo tal cual —lo custodia
// integrations/contract_body_test.go, que exige que nada encolado se reescriba
// entre la cola y el POST—.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
)

// cartClosedEffectRev es el efecto de cierre con el revision_no que anota
// cart.Projector.closeIntake. `rev any` y no `int` a propósito: por el efecto
// puede llegar un int (camino en-proceso) o un float64 (si el payload cruzó un
// round-trip JSON), y las dos formas tienen que producir el mismo número.
func cartClosedEffectRev(rev any) modules.Effect {
	eff := cartClosedEffect()
	eff.Payload["revision_no"] = rev
	return eff
}

// revisionDelCuerpo decodifica el JSON encolado como mapa —no como
// intakePushTemplate— para leer el campo por su NOMBRE DE CABLE: si alguien
// renombra la etiqueta json, un decode tipado lo taparía y el puente se quedaría
// sin la clave que el schema declara requerida.
func revisionDelCuerpo(t *testing.T, body json.RawMessage) (any, bool) {
	t.Helper()
	var cuerpo map[string]any
	if err := json.Unmarshal(body, &cuerpo); err != nil {
		t.Fatalf("el cuerpo encolado no es JSON válido: %v", err)
	}
	v, hay := cuerpo["revision_no"]
	return v, hay
}

// encolaConGateAbierto corre el sink por el camino de entrega y devuelve el ÚNICO
// cuerpo encolado.
func encolaConGateAbierto(t *testing.T, eff modules.Effect) json.RawMessage {
	t.Helper()
	gate := &fakeGate{open: map[string]bool{"t-1": true}}
	q := &fakeQueuer{}
	sink := NewWebhookSink(discardWebhookLogger(), "cart_closed", q, gate)

	if err := sink.Handle(context.Background(), EffectContext{TenantID: "t-1", ContactID: "c-opaco"}, eff); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(q.calls) != 1 {
		t.Fatalf("se encoló %d veces, quiero 1", len(q.calls))
	}
	return q.calls[0].payload
}

// TestWebhookSink_ElCuerpoEncoladoLlevaElRevisionNoDelEfecto: la N-ésima revisión
// viaja como N. Se prueba con 4 —no con 1 ni con 2— porque cualquier número fijo
// que alguien reintroduzca chocaría con él.
func TestWebhookSink_ElCuerpoEncoladoLlevaElRevisionNoDelEfecto(t *testing.T) {
	for _, caso := range []struct {
		nombre string
		valor  any
	}{
		{"en-proceso (int)", 4},
		{"round-trip JSON (float64)", float64(4)},
		{"int64", int64(4)},
	} {
		t.Run(caso.nombre, func(t *testing.T) {
			got, hay := revisionDelCuerpo(t, encolaConGateAbierto(t, cartClosedEffectRev(caso.valor)))
			if !hay {
				t.Fatal("el cuerpo encolado no lleva revision_no: el schema lo declara requerido")
			}
			if got != float64(4) {
				t.Fatalf("revision_no en el cuerpo = %#v, quiero 4. El puente hace UPSERT por "+
					"(intake_id, revision_no): con el número equivocado el CRM se queda con el "+
					"estado anterior de la solicitud y nadie ve un error", got)
			}
		})
	}
}

// TestWebhookSink_SinRevisionNoEmiteCeroYNoUno fija la decisión del caso ausente
// (T4.10): un efecto que llegue al sink sin el número —hoy imposible por el camino
// del carrito, mañana posible en cuanto otro productor entregue por aquí— NO
// vuelve a emitir un 1 de respaldo.
//
// 🔴 El 0 es el único valor que el schema congelado rechaza (`revision_no:
// integer, minimum 1`), así que un push sin revisión no puede confundirse con un
// estado legítimo: o el puente lo rechaza y se ve, o queda bajo un número con el
// que ningún push legítimo va a colisionar. Un 1 falso, en cambio, es
// indistinguible de la primera revisión de verdad y el puente lo aplicaría —o lo
// descartaría como duplicado— sin sospechar nada.
//
// Y se ENCOLA igualmente: dejar la entrega fuera cambiaría un defecto visible por
// la pérdida silenciosa del pedido, que en esta mitad no tiene re-empuje que la
// recupere.
func TestWebhookSink_SinRevisionNoEmiteCeroYNoUno(t *testing.T) {
	eff := cartClosedEffect()
	delete(eff.Payload, "revision_no") // el fixture lo trae; aquí se prueba justo su AUSENCIA

	got, hay := revisionDelCuerpo(t, encolaConGateAbierto(t, eff))
	if !hay {
		t.Fatal("el campo no puede desaparecer del cuerpo: el schema lo declara requerido y el " +
			"worker no lo completa (solo completa buyer_data, variables y customer_note)")
	}
	if got == float64(1) {
		t.Fatal("🔴 sin revision_no en el efecto el sink emitió un 1: es exactamente el número " +
			"falso que T4.10 retiró. Un 1 de respaldo es indistinguible de la primera revisión real")
	}
	if got != float64(0) {
		t.Fatalf("revision_no en el cuerpo = %#v, quiero 0 (el valor que el schema rechaza)", got)
	}
}

// TestWebhookSink_ElRevisionNoNoLoInventaElSink cierra la puerta por el otro lado:
// el número del cuerpo es EL DEL EFECTO, sea cual sea, y el sink no lo corrige, ni
// lo acota, ni lo reordena. Si alguna vez hay que validarlo, se valida donde se
// produce (el store numera) y no aquí, donde solo se sabría inventar.
func TestWebhookSink_ElRevisionNoNoLoInventaElSink(t *testing.T) {
	for _, quiero := range []int{1, 2, 3, 17} {
		got, _ := revisionDelCuerpo(t, encolaConGateAbierto(t, cartClosedEffectRev(quiero)))
		if got != float64(quiero) {
			t.Errorf("revision_no en el cuerpo = %#v, quiero %d", got, quiero)
		}
	}
}
