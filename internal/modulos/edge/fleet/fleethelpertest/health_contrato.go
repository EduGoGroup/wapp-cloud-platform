package fleethelpertest

// Los casos de SaveHealth: la salud REAL del socket de WhatsApp (Plan 031, ADR-0023), separada
// del estado del link, la marca degraded_since y el bloque del worker del cajero de intents
// (Plan 051 · T4.3), donde nil significa «este Edge no lo sabe» y no se colapsa con el cero.

import (
	"context"
	"reflect"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet"
)

// int64Ptr devuelve un puntero al valor dado, para armar snapshots.
func int64Ptr(v int64) *int64 { return &v }

// requireInt64 exige que el puntero esté PRESENTE y valga lo esperado. Las dos mitades van
// juntas a propósito: distinguir «no lo sé» (nil) de un valor MEDIDO es el corazón de T4.3, y
// comprobar solo una dejaría pasar justo el fallo que duele (un 0 medido degradado a nil, o un
// nil leído como 0).
func requireInt64(t *testing.T, field string, got *int64, want int64) {
	t.Helper()
	switch {
	case got == nil:
		t.Errorf("%s = nil, quería un puntero a %d: un valor medido no puede volver como «no lo sé»", field, want)
	case *got != want:
		t.Errorf("%s = %d, quería %d", field, *got, want)
	}
}

// requireWorkerUnknown exige que el bloque del worker entero esté en DESCONOCIDO: texto vacío,
// punteros nil y desglose nil.
func requireWorkerUnknown(t *testing.T, s fleet.Session) {
	t.Helper()
	if s.WorkerTaskset != "" {
		t.Errorf("worker_taskset = %q, quería vacío: desconocido nunca se lee como \"disjunta\"", s.WorkerTaskset)
	}
	if s.IntentOmittedByReason != nil {
		t.Errorf("intent_omitted_by_reason = %v, quería nil (no reportado)", s.IntentOmittedByReason)
	}
	pointers := map[string]*int64{
		"intent_p50_ms": s.IntentP50Ms, "stuck_heads": s.StuckHeads, "stuck_head_polls": s.StuckHeadPolls,
		"failed_seal_dispatch": s.FailedSealDispatch, "failed_seal_budget": s.FailedSealBudget,
	}
	for field, got := range pointers {
		if got != nil {
			t.Errorf("%s = %d, quería nil: sin reportar no es un cero medido", field, *got)
		}
	}
}

// caseHealthLeavesLink: SaveHealth escribe la verdad del socket y NO toca el link: ni el
// estado, ni el perfil, ni el número, ni las marcas de la conexión. Un socket muerto con el
// stream vivo sigue siendo una sesión online.
func caseHealthLeavesLink(t *testing.T, m Montaje) {
	tenant, edge, session := seedTenant(t, m), unique("edge"), unique("session")
	markOnline(t, m, tenant, edge, session)
	setProfile(t, m, tenant, session, fleet.ProfileActive)
	setSelfPn(t, m, tenant, edge, session, "573001112233")
	before := mustGet(t, m, tenant, edge, session)

	saveHealth(t, m, tenant, edge, session, fleet.HealthSnapshot{
		WhatsappState: "dead", DegradedReason: "socket_closed", LastEventAgeS: 42, DekLoadDurationMs: 310,
		IntentCircuit: "half_open", OutboxDepth: 7, BinaryVersion: "v1.2.3", UptimeS: 86400,
	})

	after := mustGet(t, m, tenant, edge, session)
	if after.State != fleet.StateOnline || after.Profile != fleet.ProfileActive || after.SelfPn != "573001112233" {
		t.Errorf("SaveHealth tocó el link: state=%q profile=%q self_pn=%q", after.State, after.Profile, after.SelfPn)
	}
	if !after.LastConnectedAt.Equal(before.LastConnectedAt) || !after.LastSeenAt.Equal(before.LastSeenAt) {
		t.Errorf("SaveHealth movió las marcas de la conexión: last_connected_at %v → %v, last_seen_at %v → %v",
			before.LastConnectedAt, after.LastConnectedAt, before.LastSeenAt, after.LastSeenAt)
	}
	if after.LastHealthAt.IsZero() {
		t.Error("SaveHealth dejó last_health_at a cero")
	}
	got := fleet.HealthSnapshot{
		WhatsappState: after.WhatsappState, DegradedReason: after.DegradedReason, LastEventAgeS: after.LastEventAgeS,
		DekLoadDurationMs: after.DekLoadDurationMs, IntentCircuit: after.IntentCircuit, OutboxDepth: after.OutboxDepth,
		BinaryVersion: after.BinaryVersion, UptimeS: after.UptimeS,
	}
	want := fleet.HealthSnapshot{
		WhatsappState: "dead", DegradedReason: "socket_closed", LastEventAgeS: 42, DekLoadDurationMs: 310,
		IntentCircuit: "half_open", OutboxDepth: 7, BinaryVersion: "v1.2.3", UptimeS: 86400,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("la salud del socket no volvió entera:\n got  %+v\n want %+v", got, want)
	}
}

// caseDegradedSince: degraded_since marca cuándo la sesión ENTRÓ en degradado. Se fija al
// entrar, se conserva mientras siga degradada (aunque cambie el motivo o pase a dead) y se
// limpia al salir; una segunda entrada vuelve a fijarla.
func caseDegradedSince(t *testing.T, m Montaje) {
	tenant, edge, session := seedTenant(t, m), unique("edge"), unique("session")
	markOnline(t, m, tenant, edge, session)

	saveHealth(t, m, tenant, edge, session, fleet.HealthSnapshot{WhatsappState: "connected"})
	if s := mustGet(t, m, tenant, edge, session); !s.DegradedSince.IsZero() {
		t.Errorf("una sesión sana tiene degraded_since = %v", s.DegradedSince)
	}

	saveHealth(t, m, tenant, edge, session, fleet.HealthSnapshot{WhatsappState: "degraded", DegradedReason: "dek_load_timeout"})
	entered := mustGet(t, m, tenant, edge, session)
	if entered.DegradedSince.IsZero() {
		t.Fatal("al entrar en degradado no se fijó degraded_since")
	}
	if entered.DegradedReason != "dek_load_timeout" {
		t.Errorf("degraded_reason = %q, quería dek_load_timeout", entered.DegradedReason)
	}

	// Sigue degradada con otros dos partes: la marca es la de la ENTRADA.
	for _, h := range []fleet.HealthSnapshot{{WhatsappState: "dead"}, {WhatsappState: "connected", DegradedReason: "otro_motivo"}} {
		saveHealth(t, m, tenant, edge, session, h)
		if s := mustGet(t, m, tenant, edge, session); !s.DegradedSince.Equal(entered.DegradedSince) {
			t.Errorf("degraded_since se movió de %v a %v mientras la sesión seguía degradada (%+v)", entered.DegradedSince, s.DegradedSince, h)
		}
	}

	saveHealth(t, m, tenant, edge, session, fleet.HealthSnapshot{WhatsappState: "connecting"})
	healthy := mustGet(t, m, tenant, edge, session)
	if !healthy.DegradedSince.IsZero() || healthy.DegradedReason != "" {
		t.Errorf("al salir del degradado quedó degraded_since = %v, degraded_reason = %q", healthy.DegradedSince, healthy.DegradedReason)
	}

	saveHealth(t, m, tenant, edge, session, fleet.HealthSnapshot{WhatsappState: "dead"})
	if s := mustGet(t, m, tenant, edge, session); s.DegradedSince.IsZero() {
		t.Error("la segunda entrada en degradado no fijó degraded_since")
	}
}

// caseHealthUnknown: la salud de una sesión que aún no se registró es un no-op sin error, y no
// crea la fila: el próximo Heartbeat, tras el registro, la fijará.
func caseHealthUnknown(t *testing.T, m Montaje) {
	tenant, edge, session := seedTenant(t, m), unique("edge"), unique("session")
	saveHealth(t, m, tenant, edge, session, fleet.HealthSnapshot{WhatsappState: "connected"})
	if _, found, err := m.Repository.Get(context.Background(), tenant, edge, session); err != nil || found {
		t.Errorf("tras SaveHealth de una sesión desconocida, Get = (found=%v, err=%v), quería (false, nil)", found, err)
	}
}

// caseWorkerUnknown: un parte que NO SABE el bloque del worker (Edge viejo) se guarda como
// desconocido y no como cero. Tampoco un desglose vacío es un «cero medido»: se lee nil.
func caseWorkerUnknown(t *testing.T, m Montaje) {
	tenant, edge, session := seedTenant(t, m), unique("edge"), unique("session")
	markOnline(t, m, tenant, edge, session)
	requireWorkerUnknown(t, mustGet(t, m, tenant, edge, session)) // nunca reportó

	saveHealth(t, m, tenant, edge, session, fleet.HealthSnapshot{WhatsappState: "connected", BinaryVersion: "v0.12.0"})
	s := mustGet(t, m, tenant, edge, session)
	requireWorkerUnknown(t, s)
	if s.BinaryVersion != "v0.12.0" {
		t.Errorf("binary_version = %q, quería v0.12.0", s.BinaryVersion)
	}

	saveHealth(t, m, tenant, edge, session, fleet.HealthSnapshot{WhatsappState: "connected", IntentOmittedByReason: map[string]int64{}})
	requireWorkerUnknown(t, mustGet(t, m, tenant, edge, session))
}

// caseWorkerRoundTrip: el bloque del worker viaja entero. Un CERO MEDIDO vuelve como puntero a
// 0, los dos sellos no se mezclan (T3.12: solo el de despacho implica duplicados) y los motivos
// se guardan uno a uno, sin sumarse (INV-051.3). Lo guardado no comparte respaldo con el
// llamante.
func caseWorkerRoundTrip(t *testing.T, m Montaje) {
	tenant, edge, session := seedTenant(t, m), unique("edge"), unique("session")
	markOnline(t, m, tenant, edge, session)

	reasons := map[string]int64{"fastlane": 7, "presupuesto": 2, "breaker": 1}
	p50 := int64Ptr(1450)
	saveHealth(t, m, tenant, edge, session, fleet.HealthSnapshot{
		WhatsappState: "connected", IntentCircuit: "open", WorkerTaskset: "solapada", IntentP50Ms: p50,
		IntentOmittedByReason: reasons, StuckHeads: int64Ptr(3), StuckHeadPolls: int64Ptr(11),
		FailedSealDispatch: int64Ptr(0), FailedSealBudget: int64Ptr(4),
	})
	// Mutar lo que tiene el llamante no puede cambiar lo ya guardado.
	reasons["breaker"] = 999
	*p50 = 1

	s := mustGet(t, m, tenant, edge, session)
	if s.WorkerTaskset != "solapada" || s.IntentCircuit != "open" {
		t.Errorf("worker_taskset = %q, intent_circuit = %q; quería solapada y open", s.WorkerTaskset, s.IntentCircuit)
	}
	requireInt64(t, "intent_p50_ms", s.IntentP50Ms, 1450)
	requireInt64(t, "stuck_heads", s.StuckHeads, 3)
	requireInt64(t, "stuck_head_polls", s.StuckHeadPolls, 11)
	requireInt64(t, "failed_seal_dispatch", s.FailedSealDispatch, 0)
	requireInt64(t, "failed_seal_budget", s.FailedSealBudget, 4)
	want := map[string]int64{"fastlane": 7, "presupuesto": 2, "breaker": 1}
	if !reflect.DeepEqual(s.IntentOmittedByReason, want) {
		t.Errorf("intent_omitted_by_reason = %v, quería %v: ni gana ni pierde claves, ni se suma", s.IntentOmittedByReason, want)
	}
}

// caseWorkerStale: cuando el parte del worker se vuelve rancio el Edge manda sus señales a cero
// A PROPÓSITO, y el repositorio tiene que BORRAR lo anterior: conservar un "disjunta" viejo es
// publicar una salud inventada.
func caseWorkerStale(t *testing.T, m Montaje) {
	tenant, edge, session := seedTenant(t, m), unique("edge"), unique("session")
	markOnline(t, m, tenant, edge, session)
	saveHealth(t, m, tenant, edge, session, fleet.HealthSnapshot{
		WhatsappState: "connected", WorkerTaskset: "disjunta", IntentP50Ms: int64Ptr(820),
		IntentOmittedByReason: map[string]int64{"fastlane": 5}, StuckHeads: int64Ptr(1), StuckHeadPolls: int64Ptr(2),
		FailedSealDispatch: int64Ptr(3), FailedSealBudget: int64Ptr(4),
	})
	if s := mustGet(t, m, tenant, edge, session); s.WorkerTaskset != "disjunta" {
		t.Fatalf("precondición: worker_taskset = %q, quería disjunta", s.WorkerTaskset)
	}

	saveHealth(t, m, tenant, edge, session, fleet.HealthSnapshot{WhatsappState: "connected"})
	requireWorkerUnknown(t, mustGet(t, m, tenant, edge, session))
}
