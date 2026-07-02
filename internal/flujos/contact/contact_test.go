package contact

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateKind(t *testing.T) {
	valid := []string{KindPhoneE164, KindWALID, KindWAUsername}
	for _, k := range valid {
		if err := ValidateKind(k); err != nil {
			t.Errorf("ValidateKind(%q) = %v, want nil", k, err)
		}
	}
	for _, k := range []string{"", "phone", "lid", "email", "PHONE_E164"} {
		if err := ValidateKind(k); err == nil {
			t.Errorf("ValidateKind(%q) = nil, want ErrInvalidRef", k)
		} else if !errors.Is(err, ErrInvalidRef) {
			t.Errorf("ValidateKind(%q) error = %v, want wrap de ErrInvalidRef", k, err)
		}
	}
}

func TestNormalize_Phone(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"e164 con +", "+14155552671", "14155552671"},
		{"con espacios y guiones", "+1 415-555-2671", "14155552671"},
		{"con paréntesis", "+1 (415) 555 2671", "14155552671"},
		{"ya normalizado", "573001112233", "573001112233"},
		{"con puntos", "44.20.7946.0018", "442079460018"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Normalize(KindPhoneE164, c.in)
			if err != nil {
				t.Fatalf("Normalize(phone, %q) error inesperado: %v", c.in, err)
			}
			if got != c.want {
				t.Fatalf("Normalize(phone, %q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestNormalize_Phone_MismoValorDistintoFormato(t *testing.T) {
	// Dos formatos del MISMO número deben normalizar al MISMO value (base del
	// dedup por (tenant, kind, value)).
	a, err := Normalize(KindPhoneE164, "+1 (415) 555-2671")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Normalize(KindPhoneE164, "14155552671")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("dos formatos del mismo número normalizan distinto: %q != %q", a, b)
	}
}

func TestNormalize_Phone_Invalido(t *testing.T) {
	cases := []string{
		"",                 // vacío
		"abc",              // sin dígitos
		"+ - ()",           // solo separadores
		"1234567890123456", // 16 dígitos, excede E.164
	}
	for _, in := range cases {
		if _, err := Normalize(KindPhoneE164, in); err == nil {
			t.Errorf("Normalize(phone, %q) = nil, want ErrInvalidRef", in)
		} else if !errors.Is(err, ErrInvalidRef) {
			t.Errorf("Normalize(phone, %q) error = %v, want wrap de ErrInvalidRef", in, err)
		}
	}
}

func TestNormalize_LID(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"con servidor @lid", "123456789012345@lid", "123456789012345"},
		{"solo user", "123456789012345", "123456789012345"},
		{"con dispositivo", "123456789012345:2@lid", "123456789012345"},
		{"con agente y dispositivo", "123456789012345_1:2@lid", "123456789012345"},
		{"con espacios de borde", "  987654321@lid  ", "987654321"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Normalize(KindWALID, c.in)
			if err != nil {
				t.Fatalf("Normalize(lid, %q) error inesperado: %v", c.in, err)
			}
			if got != c.want {
				t.Fatalf("Normalize(lid, %q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestNormalize_LID_Invalido(t *testing.T) {
	for _, in := range []string{"", "@lid", "abc@lid", "12ab34@lid"} {
		if _, err := Normalize(KindWALID, in); err == nil {
			t.Errorf("Normalize(lid, %q) = nil, want ErrInvalidRef", in)
		} else if !errors.Is(err, ErrInvalidRef) {
			t.Errorf("Normalize(lid, %q) error = %v, want wrap de ErrInvalidRef", in, err)
		}
	}
}

func TestNormalize_Username(t *testing.T) {
	got, err := Normalize(KindWAUsername, "  JuanPerez  ")
	if err != nil {
		t.Fatalf("Normalize(username) error inesperado: %v", err)
	}
	if got != "juanperez" {
		t.Fatalf("Normalize(username) = %q, want %q", got, "juanperez")
	}
	if _, err := Normalize(KindWAUsername, "   "); err == nil {
		t.Fatal("Normalize(username, espacios) = nil, want ErrInvalidRef")
	}
}

func TestNormalize_KindDesconocido(t *testing.T) {
	if _, err := Normalize("email", "[email protected]"); !errors.Is(err, ErrInvalidRef) {
		t.Fatalf("Normalize(kind desconocido) error = %v, want wrap de ErrInvalidRef", err)
	}
}

func TestNewRef(t *testing.T) {
	ref, err := NewRef(KindPhoneE164, "+1 415-555-2671")
	if err != nil {
		t.Fatalf("NewRef error inesperado: %v", err)
	}
	if ref.Kind != KindPhoneE164 || ref.Value != "14155552671" {
		t.Fatalf("NewRef = %+v, want {phone_e164 14155552671}", ref)
	}

	if _, err := NewRef("nope", "x"); err == nil {
		t.Fatal("NewRef con kind inválido debería fallar")
	}
}

// TestErrInvalidRef_Mensaje protege el prefijo del error base (contrato de logs).
func TestErrInvalidRef_Mensaje(t *testing.T) {
	if !strings.Contains(ErrInvalidRef.Error(), "contact_ref") {
		t.Fatalf("mensaje de ErrInvalidRef inesperado: %q", ErrInvalidRef.Error())
	}
}

// TestNormalize_ErrorNoFiltraValue verifica la higiene de logs (design.md
// §8/§10.I): el mensaje de error de un value inválido NUNCA embebe el value
// crudo (PII), porque sube tal cual a runtime_engine.go como ("error", err).
func TestNormalize_ErrorNoFiltraValue(t *testing.T) {
	cases := []struct {
		name  string
		kind  string
		value string
	}{
		{"phone sin dígitos", KindPhoneE164, "abc-xyz"},
		{"phone excede máximo", KindPhoneE164, "12345678901234567890"},
		{"lid no numérico", KindWALID, "abcdef123"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Normalize(tc.kind, tc.value)
			if err == nil {
				t.Fatalf("Normalize(%q, %q) = nil, quiero error", tc.kind, tc.value)
			}
			if strings.Contains(err.Error(), tc.value) {
				t.Fatalf("el error filtra el value crudo (PII): %q contiene %q", err.Error(), tc.value)
			}
		})
	}
}
