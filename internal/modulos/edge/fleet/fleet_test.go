//go:build pendiente

package fleet

import (
	"reflect"
	"slices"
	"testing"
)

// Las promesas de Repository las fija la suite fleethelpertest.ContratoRepository, contra
// Memoria en fleethelpertest/memoria_test.go y contra el adaptador Postgres en los procesos de
// F9. Este fichero es interno al paquete (no puede importar la suite: sería un ciclo) y cubre
// lo que fleet.go tiene de puro: los literales, las dos validaciones y Degraded.

// TestObservableLiterals: los valores que viajan a la columna, a la API y al Edge no cambian ni
// un byte, y el tope de dispositivos es 4.
func TestObservableLiterals(t *testing.T) {
	states := map[State]string{StateOnline: "online", StateOffline: "offline", StateLoggedOut: "loggedout"}
	for got, want := range states {
		if string(got) != want {
			t.Errorf("estado %q, quería el literal %q", got, want)
		}
	}
	if len(states) != 3 {
		t.Errorf("los tres estados no son distintos entre sí: %v", states)
	}
	if ProfileActive != "active" || ProfilePassive != "passive" {
		t.Errorf("perfiles %q y %q, quería los literales active y passive", ProfileActive, ProfilePassive)
	}
	if DeviceLimit != 4 {
		t.Errorf("DeviceLimit = %d, quería 4 (REQ-D4)", DeviceLimit)
	}
}

// TestSentinels_ObservableText: el texto de los dos centinelas llega a la API tal cual.
func TestSentinels_ObservableText(t *testing.T) {
	if got, want := ErrInvalidState.Error(), "estado de sesión inválido (usar offline|loggedout)"; got != want {
		t.Errorf("ErrInvalidState dice %q, quería %q", got, want)
	}
	if got, want := ErrInvalidProfile.Error(), "perfil de sesión inválido (usar active|passive)"; got != want {
		t.Errorf("ErrInvalidProfile dice %q, quería %q", got, want)
	}
}

// TestValidAdminState: un admin solo puede fijar offline|loggedout. Online no (es derivado del
// stream y no se falsea), y la comparación es exacta.
func TestValidAdminState(t *testing.T) {
	cases := []struct {
		name  string
		state State
		want  bool
	}{
		{"offline", StateOffline, true},
		{"loggedout", StateLoggedOut, true},
		{"online_is_derived", StateOnline, false},
		{"zero_value", "", false},
		{"upper_case", "OFFLINE", false},
		{"padded", " offline", false},
		{"unknown", "banned", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ValidAdminState(c.state); got != c.want {
				t.Errorf("ValidAdminState(%q) = %v, quería %v", c.state, got, c.want)
			}
		})
	}
}

// TestValidProfile: solo active|passive son perfiles. El valor cero no lo es, ni los valores
// del eje retirado (role: bot|human), y la comparación es exacta.
func TestValidProfile(t *testing.T) {
	cases := []struct {
		name    string
		profile Profile
		want    bool
	}{
		{"active", ProfileActive, true},
		{"passive", ProfilePassive, true},
		{"zero_value", "", false},
		{"retired_role_bot", "bot", false},
		{"retired_role_human", "human", false},
		{"upper_case", "ACTIVE", false},
		{"padded", "passive ", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ValidProfile(c.profile); got != c.want {
				t.Errorf("ValidProfile(%q) = %v, quería %v", c.profile, got, c.want)
			}
		})
	}
}

// TestHealthSnapshot_Degraded: basta una de tres condiciones —motivo presente, "degraded" o
// "dead"— y cualquier otra cosa es un socket sano. El snapshot cero (un Edge que aún no sabe su
// salud) NO está degradado: se porta tal cual.
func TestHealthSnapshot_Degraded(t *testing.T) {
	cases := []struct {
		name string
		h    HealthSnapshot
		want bool
	}{
		{"zero_value_is_not_degraded", HealthSnapshot{}, false},
		{"connected", HealthSnapshot{WhatsappState: "connected"}, false},
		{"connecting", HealthSnapshot{WhatsappState: "connecting"}, false},
		{"degraded", HealthSnapshot{WhatsappState: "degraded"}, true},
		{"dead", HealthSnapshot{WhatsappState: "dead"}, true},
		{"connected_with_reason", HealthSnapshot{WhatsappState: "connected", DegradedReason: "dek_load_timeout"}, true},
		{"reason_without_state", HealthSnapshot{DegradedReason: "dek_load_timeout"}, true},
		{"degraded_with_reason", HealthSnapshot{WhatsappState: "degraded", DegradedReason: "x"}, true},
		{"upper_case_state_does_not_count", HealthSnapshot{WhatsappState: "DEAD"}, false},
		{"unknown_state", HealthSnapshot{WhatsappState: "reconnecting"}, false},
		// Nada más que el motivo y el estado del socket entra en la cuenta.
		{"other_fields_do_not_count", HealthSnapshot{WhatsappState: "connected", IntentCircuit: "open", OutboxDepth: 9000, WorkerTaskset: "solapada"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.h.Degraded(); got != c.want {
				t.Errorf("Degraded() de %+v = %v, quería %v", c.h, got, c.want)
			}
		})
	}
}

// TestZeroValues_MeanUnknown fija lo que dice el cero de cada tipo, que es «no lo sé» y nunca
// «está bien»: la Session cero no tiene un estado admitido ni un perfil conocido, su bloque del
// worker es nil (no un cero medido) y la TenantProfiles cero trae el mapa nil y versión 0.
func TestZeroValues_MeanUnknown(t *testing.T) {
	var s Session
	if ValidAdminState(s.State) || ValidProfile(s.Profile) {
		t.Errorf("la Session cero trae un estado o un perfil válidos: %q, %q", s.State, s.Profile)
	}
	if s.IntentP50Ms != nil || s.StuckHeads != nil || s.StuckHeadPolls != nil ||
		s.FailedSealDispatch != nil || s.FailedSealBudget != nil || s.IntentOmittedByReason != nil {
		t.Errorf("la Session cero trae medidas del worker: %+v", s)
	}
	// Leer un desglose nil da el cero sin panic; iterarlo da cero vueltas.
	if n := s.IntentOmittedByReason["breaker"]; n != 0 || len(s.IntentOmittedByReason) != 0 {
		t.Errorf("el desglose nil dice %d para una clave y tiene %d claves", n, len(s.IntentOmittedByReason))
	}
	var tp TenantProfiles
	if tp.Sessions != nil || tp.Version != 0 {
		t.Errorf("la TenantProfiles cero es %+v", tp)
	}
}

// TestRepository_IsTheClosedListOfTenMethods: el puerto son estos diez métodos, con estos
// nombres. Uno más obliga a implementarlo a todo decorador; la lectura de un solo consumidor
// (ProfilesByTenant) se queda en la implementación.
func TestRepository_IsTheClosedListOfTenMethods(t *testing.T) {
	port := reflect.TypeFor[Repository]()
	got := make([]string, 0, port.NumMethod())
	for i := range port.NumMethod() {
		got = append(got, port.Method(i).Name)
	}
	want := []string{
		"CountLiveBySelfPn", "Get", "List", "MarkLoggedOut", "MarkOffline", "MarkOnline",
		"SaveHealth", "SetProfile", "SetSelfPn", "SetState",
	}
	if !slices.Equal(got, want) {
		t.Errorf("fleet.Repository tiene los métodos %v, quería %v", got, want)
	}
}
