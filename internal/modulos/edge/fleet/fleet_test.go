package fleet

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
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

// TestDefaultProfile_OnlyTheEmptyBecomesPassive: el perfil por defecto es el de la columna
// (pasivo, D-07: privacidad por defecto) y SOLO convierte el vacío. Un perfil desconocido pasa
// intacto: no se «arregla» a pasivo ni a activo.
func TestDefaultProfile_OnlyTheEmptyBecomesPassive(t *testing.T) {
	cases := map[Profile]Profile{
		"":             ProfilePassive,
		ProfilePassive: ProfilePassive,
		ProfileActive:  ProfileActive,
		"bot":          "bot",
		" ":            " ",
	}
	for in, want := range cases {
		if got := defaultProfile(in); got != want {
			t.Errorf("defaultProfile(%q) = %q, quería %q", in, got, want)
		}
	}
}

// oldRuleSelfPn es la regla del viejo escrita a mano (internal/gateway/fleet/fleet.go @ 809345b,
// normalizeSelfPn → internal/flujos/contact.Normalize con phone_e164): recorrer las runas,
// conservar solo los dígitos ASCII '0'..'9', y es inválido si no queda ninguno o si quedan más
// de 15. Es el oráculo de equivalencia; el paquete viejo no se importa.
func oldRuleSelfPn(value string) (digits string, ok bool) {
	digits = asciiDigits(value)
	if digits == "" || len(digits) > 15 {
		return "", false
	}
	return digits, true
}

// asciiDigits recorre las runas de value y conserva solo los dígitos ASCII.
func asciiDigits(value string) string {
	var b strings.Builder
	for _, c := range value {
		if c >= '0' && c <= '9' {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// r convierte un punto de código en cadena; así el corpus nombra cada carácter invisible o no
// ASCII por su número en vez de llevarlo crudo en el fuente.
func r(c rune) string { return string(c) }

// selfPnCorpus es el corpus ADVERSARIO del self_pn (reglas de F3 §0, hallazgo 40 de F1). want es
// el valor canónico esperado, escrito a mano; "" significa que el número no normaliza.
func selfPnCorpus() []struct{ name, input, want string } {
	const plain = "573001112233"
	arabicIndic573 := r(0x0665) + r(0x0667) + r(0x0663) // ٥٧٣
	fullwidth012 := r(0xFF10) + r(0xFF11) + r(0xFF12)   // ０１２
	return []struct{ name, input, want string }{
		{"already_canonical", plain, plain},
		{"plus_spaces_and_dash", "+56 9 8446-7443", "56984467443"},
		{"parentheses_and_dots", "(56) 9.8446.7443", "56984467443"},
		{"leading_zeros_are_kept", "0056984467443", "0056984467443"},
		{"whatsapp_jid", plain + "@s.whatsapp.net", plain},
		// Trampa heredada: el sufijo de dispositivo ":5" de un JID NO se descarta, sus dígitos se
		// suman al número. Se afirma tal cual: quien llama entrega el número, no el JID.
		{"whatsapp_jid_with_device_keeps_its_digit", plain + ":5@s.whatsapp.net", plain + "5"},
		{"repeated_separators", "a@@b", ""},
		{"repeated_separators_around_digits", "57@@300--111  2233", plain},
		{"letters_only", "sin-digitos", ""},
		{"empty", "", ""},
		{"only_spaces", "   ", ""},
		// Dígitos que no son ASCII: no son dígitos para la regla. Solos no dejan nada; mezclados,
		// quedan solo los ASCII.
		{"arabic_indic_only", arabicIndic573, ""},
		{"arabic_indic_mixed_with_ascii", "57" + arabicIndic573 + "3001112233", plain},
		{"fullwidth_only", fullwidth012, ""},
		{"fullwidth_mixed_with_ascii", fullwidth012 + plain, plain},
		// Espacios Unicode e invisibles, en los bordes y por dentro: se descartan como cualquier
		// otro carácter que no sea un dígito ASCII.
		{"nbsp_u00a0_edges_and_inside", r(0x00A0) + "573" + r(0x00A0) + "001112233" + r(0x00A0), plain},
		{"em_space_u2003_edges_and_inside", r(0x2003) + "573001" + r(0x2003) + "112233" + r(0x2003), plain},
		{"zero_width_space_u200b_edges_and_inside", r(0x200B) + "5730011" + r(0x200B) + "12233" + r(0x200B), plain},
		{"bom_ufeff_edges_and_inside", r(0xFEFF) + "57300111223" + r(0xFEFF) + "3" + r(0xFEFF), plain},
		{"only_invisibles", r(0x200B) + r(0xFEFF) + r(0x00A0) + r(0x2003), ""},
		// UTF-8 inválido: cada byte suelto se lee como U+FFFD y se descarta.
		{"invalid_utf8", "\xff573\xfe001112233\xc3", plain},
		{"invalid_utf8_only", "\xff\xfe", ""},
		// El máximo de E.164 son 15 dígitos, contados después de limpiar.
		{"fifteen_digits", "123456789012345", "123456789012345"},
		{"fifteen_digits_with_separators", "+12 345 678 901 2345", "123456789012345"},
		{"sixteen_digits", "1234567890123456", ""},
		{"sixteen_digits_with_separators", "+1234 5678 9012 3456", ""},
	}
}

// TestNormalizeSelfPn_MatchesTheOldRule es la equivalencia viejo ↔ nuevo del self_pn sobre el
// corpus adversario: en TODA entrada el normalizador nuevo (nucleo/contact) da lo que daba la
// regla vieja (flujos/contact), y además el valor esperado está fijado a mano, para que un
// cambio de normalización se vea aquí y se decida. De esa salida sale el índice ciego
// fleet_sessions.self_pn_bidx: una diferencia de un byte deja sin casar lo ya guardado.
func TestNormalizeSelfPn_MatchesTheOldRule(t *testing.T) {
	for _, c := range selfPnCorpus() {
		t.Run(c.name, func(t *testing.T) {
			got, err := normalizeSelfPn(c.input)
			oldValue, oldOK := oldRuleSelfPn(c.input)
			if got != oldValue || (err == nil) != oldOK {
				t.Fatalf("DIVERGE del viejo: nuevo = (%q, err=%v), viejo = (%q, ok=%v)", got, err, oldValue, oldOK)
			}
			if got != c.want {
				t.Errorf("normalizeSelfPn = %q, quería %q", got, c.want)
			}
			if c.want == "" {
				requireSelfPnError(t, c.input, err)
				return
			}
			if err != nil {
				t.Fatalf("normalizeSelfPn: error inesperado %v", err)
			}
			// Idempotencia: lo ya normalizado no cambia al volver a pasar.
			if again, err := normalizeSelfPn(got); err != nil || again != got {
				t.Errorf("normalizeSelfPn del valor ya normalizado = (%q, %v), quería (%q, nil)", again, err, got)
			}
		})
	}
}

// requireSelfPnError afirma el error de un número que no normaliza: envuelve
// contact.ErrInvalidRef y NO lleva dentro el valor recibido, que es PII y acaba en los logs.
func requireSelfPnError(t *testing.T, input string, err error) {
	t.Helper()
	if !errors.Is(err, contact.ErrInvalidRef) {
		t.Fatalf("err = %v, quería uno que envuelva contact.ErrInvalidRef", err)
	}
	if digits := asciiDigits(input); len(digits) > 2 && strings.Contains(err.Error(), digits) {
		t.Errorf("el error lleva dentro los dígitos del número recibido: %v", err)
	}
}
