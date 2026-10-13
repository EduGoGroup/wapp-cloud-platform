package runtime_test

// events_menu_guards_test.go prueba tres guardas de events_menu.go que no hablan: el sello del
// menú pendiente que FALTA en Vars, y las dos puertas del menú que se quedan sin cupo anti-loop
// (enviar la oferta y retomar el evento elegido). Trozo de events_menu_test.go (E-13).

import (
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
)

// TestEvents_PendingMenuWithoutSealCountsAsSealedToNoEvent (EV-7): un menú pendiente cuyo
// sello falta en Vars se lee como sellado con "" —ningún evento—, así que sobre una
// conversación sin evento activo sigue mandando y su número despacha.
func TestEvents_PendingMenuWithoutSealCountsAsSealedToNoEvent(t *testing.T) {
	h := eventsFallbackHarness(t)
	h.say("wa-1", "hola-zzq")
	st, found := h.state()
	if _, sealed := st.Vars[eventsVarPendingMenuSeal]; !found || !sealed || st.EventID != "" {
		t.Fatalf("estado = (%+v, %v), quería la oferta pendiente, sellada y sin evento activo", st, found)
	}
	delete(st.Vars, eventsVarPendingMenuSeal)
	if err := h.repo.Save(t.Context(), st); err != nil {
		t.Fatalf("quitar el sello del menú pendiente: %v", err)
	}

	h.say("wa-2", "1")

	eventsRequireLifecycle(t, h, runtime.EffectEventStarted, 1)
	cart := eventsAliveOfKind(t, h, trigger.EventKindCart)
	if st, _ := h.state(); st.EventID != cart.ID || st.FlowID != eventsCartFlow {
		t.Errorf("estado = %+v, el «1» debía parir el carrito y dejar la conversación en él", st)
	}
	if texts := h.texts(); len(texts) != 2 || texts[1] != menuFlow(eventsCartFlow).Nodes["root"].Prompt {
		t.Errorf("textos = %q, quería la oferta y después la pantalla del flujo del carrito", texts)
	}
}

// TestEvents_OfferWithoutQuotaIsSilentAndLeavesNoPendingMenu (EV-7, RT-8): sin cupo anti-loop
// la oferta no se envía NI queda pendiente: un menú guardado que el cliente nunca vio haría
// que su número siguiente significara algo que nadie le enseñó.
func TestEvents_OfferWithoutQuotaIsSilentAndLeavesNoPendingMenu(t *testing.T) {
	h := eventsFallbackHarness(t)
	h.limiter.Limit(0)

	h.say("wa-1", "hola-zzq")

	if texts := h.texts(); len(texts) != 0 {
		t.Errorf("textos = %q, sin cupo no sale la oferta", texts)
	}
	if st, found := h.state(); found {
		t.Errorf("estado = %+v, sin cupo no queda ningún menú pendiente", st)
	}
	if reasons := h.blockedReasons(); len(reasons) != 1 || reasons[0] != "rate_limit" {
		t.Errorf("motivos de corte = %v, quería un rate_limit", reasons)
	}
	if calls := h.limiter.Calls(); len(calls) != 1 {
		t.Errorf("tokens pedidos = %d, quería 1: el de la oferta, y ninguno más por el mismo turno", len(calls))
	}
}

// TestEvents_RescueWithoutQuotaResumesNothing (EV-7, RT-8): el token del rescate se cobra
// ANTES de conmutar. Sin cupo, elegir un evento de la lista no lo retoma: ni efecto, ni
// puntero, ni Touch, ni un solo saliente.
func TestEvents_RescueWithoutQuotaResumesNothing(t *testing.T) {
	h, ev := eventsRescueHarness(t, eventsWithCartLines())
	h.say("wa-2", "2")
	if texts := h.texts(); len(texts) != 2 || texts[1] != eventsRescueList {
		t.Fatalf("textos = %q, quería la lista de lo que se puede retomar:\n%q", texts, eventsRescueList)
	}
	h.limiter.Limit(2) // los dos que ya se gastaron: el del rescate es el tercero
	h.clock.Advance(time.Minute)

	h.say("wa-3", "1")

	eventsRequireLifecycle(t, h, runtime.EffectEventSwitched, 0)
	if st, _ := h.state(); st.EventID != "" || st.FlowID != "" {
		t.Errorf("estado = %+v, sin cupo la conversación no vuelve al evento", st)
	}
	if texts := h.texts(); len(texts) != 2 {
		t.Errorf("textos = %q, sin cupo no sale ni el resumen ni la pantalla del flujo", texts)
	}
	if reasons := h.blockedReasons(); len(reasons) != 1 || reasons[0] != "rate_limit" {
		t.Errorf("motivos de corte = %v, quería un rate_limit", reasons)
	}
	if row := eventsRow(t, h, ev.ID); row.Status != events.StatusOpen || !row.LastActivityAt.Equal(harnessStart) {
		t.Errorf("evento = %+v, quería open y con el last_activity_at de antes (%v): sin cupo no se toca", row, harnessStart)
	}
}
