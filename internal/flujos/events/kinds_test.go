package events_test

import (
	"context"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
)

// sembrarReglas mete reglas en un store de memoria y devuelve la oferta montada
// sobre él.
func sembrarReglas(t *testing.T, reglas ...trigger.Rule) *events.TriggerKindOffer {
	t.Helper()
	st := trigger.NewMemoryStore()
	for _, r := range reglas {
		if _, err := st.Insert(context.Background(), r); err != nil {
			t.Fatalf("sembrar regla %+v: %v", r, err)
		}
	}
	return events.NewTriggerKindOffer(st)
}

// tiposDe pregunta la oferta del tenant de test o falla.
func tiposDe(t *testing.T, o *events.TriggerKindOffer, sessionID string) []string {
	t.Helper()
	kinds, err := o.OfferedKinds(context.Background(), refDeTest().TenantID, sessionID)
	if err != nil {
		t.Fatalf("OfferedKinds: %v", err)
	}
	return kinds
}

// reglaEventStart arma una regla event_start habilitada.
func reglaEventStart(tenantID, keyword, eventKind string) trigger.Rule {
	return trigger.Rule{
		TenantID: tenantID, Kind: trigger.KindEventStart, Keyword: keyword,
		MatchType: trigger.MatchExact, EventKind: eventKind, Enabled: true,
	}
}

// TestOfferedKinds_DosPalabrasParaElMismoTipoSonUnaOpcion: «carrito» y «pedido»
// abren el mismo carrito. En el menú eso es UNA línea, no dos que hacen lo
// mismo.
func TestOfferedKinds_DosPalabrasParaElMismoTipoSonUnaOpcion(t *testing.T) {
	tid := refDeTest().TenantID
	o := sembrarReglas(t,
		reglaEventStart(tid, "carrito", "cart"),
		reglaEventStart(tid, "pedido", "cart"),
		reglaEventStart(tid, "encuesta", "survey"),
	)

	got := tiposDe(t, o, "")
	if len(got) != 2 || got[0] != "cart" || got[1] != "survey" {
		t.Fatalf("quiero [cart survey] sin repetir; got %v", got)
	}
}

// TestOfferedKinds_LaReglaApagadaNoOfreceNada: ListByKind no filtra por enabled
// —eso es del llamante— y una regla deshabilitada no se puede disparar, así que
// tampoco se ofrece.
func TestOfferedKinds_LaReglaApagadaNoOfreceNada(t *testing.T) {
	tid := refDeTest().TenantID
	apagada := reglaEventStart(tid, "documentos", "media")
	apagada.Enabled = false
	o := sembrarReglas(t, reglaEventStart(tid, "carrito", "cart"), apagada)

	if got := tiposDe(t, o, ""); len(got) != 1 || got[0] != "cart" {
		t.Fatalf("la regla apagada no ofrece su tipo; got %v", got)
	}
}

// TestOfferedKinds_LaReglaSinTipoNoOfreceNada: un event_start sin event_kind es
// una regla mal dada de alta (el CRUD la rechaza en T2.1). Si alguna sobrevive
// en BD, no puede producir una opción que no sabe a qué despacha.
func TestOfferedKinds_LaReglaSinTipoNoOfreceNada(t *testing.T) {
	tid := refDeTest().TenantID
	o := sembrarReglas(t, reglaEventStart(tid, "carrito", "cart"), reglaEventStart(tid, "algo", ""))

	if got := tiposDe(t, o, ""); len(got) != 1 || got[0] != "cart" {
		t.Fatalf("una regla sin event_kind no ofrece nada; got %v", got)
	}
}

// TestOfferedKinds_SoloLosOtrosKindsNoOfrecenTipos: keyword, fallback, escape y
// llm siguen existiendo y no paren eventos. La oferta mira SOLO event_start.
func TestOfferedKinds_SoloLosOtrosKindsNoOfrecenTipos(t *testing.T) {
	tid := refDeTest().TenantID
	o := sembrarReglas(t,
		trigger.Rule{TenantID: tid, Kind: trigger.KindKeyword, Keyword: "hola", MatchType: trigger.MatchExact, FlowID: "f1", Enabled: true},
		trigger.Rule{TenantID: tid, Kind: trigger.KindFallback, FlowID: "f2", Enabled: true},
		trigger.Rule{TenantID: tid, Kind: trigger.KindEventStop, Keyword: "salir", MatchType: trigger.MatchExact, Enabled: true},
	)

	if got := tiposDe(t, o, ""); len(got) != 0 {
		t.Fatalf("sin reglas event_start no hay tipos ofrecidos; got %v", got)
	}
}

// TestOfferedKinds_LaReglaDeOtraSesionNoSeOfreceAqui: una regla acotada a una
// sesión no forma parte de la oferta de otra (Plan 020 · T4). Con sessionID
// vacío solo cuentan las globales.
func TestOfferedKinds_LaReglaDeOtraSesionNoSeOfreceAqui(t *testing.T) {
	tid := refDeTest().TenantID
	deOtraSesion := reglaEventStart(tid, "documentos", "media")
	deOtraSesion.SessionID = "sesion-B"
	o := sembrarReglas(t, reglaEventStart(tid, "carrito", "cart"), deOtraSesion)

	if got := tiposDe(t, o, "sesion-A"); len(got) != 1 || got[0] != "cart" {
		t.Fatalf("la regla de sesion-B no se ofrece en sesion-A; got %v", got)
	}
	if got := tiposDe(t, o, "sesion-B"); len(got) != 2 {
		t.Fatalf("en sesion-B se ofrecen la global y la suya; got %v", got)
	}
}

// TestOfferedKinds_LaReglaDeOtroTenantNoExiste (INV-8): el aislamiento no es una
// convención del llamante, se comprueba.
func TestOfferedKinds_LaReglaDeOtroTenantNoExiste(t *testing.T) {
	o := sembrarReglas(t, reglaEventStart("33333333-3333-3333-3333-333333333333", "carrito", "cart"))

	if got := tiposDe(t, o, ""); len(got) != 0 {
		t.Fatalf("la oferta de otro tenant no es visible; got %v", got)
	}
}
