package contact

import (
	"errors"
	"strings"
	"testing"
)

// exigeErrorDeRef comprueba lo que todo error de referencia promete: envuelve ErrInvalidRef y su
// texto es, byte a byte, el observable (viaja al cliente HTTP en un 400).
func exigeErrorDeRef(t *testing.T, err error, texto string) {
	t.Helper()
	if err == nil {
		t.Fatalf("quiere el error %q; no hubo error", texto)
	}
	if !errors.Is(err, ErrInvalidRef) {
		t.Errorf("el error no envuelve ErrInvalidRef: %v", err)
	}
	if err.Error() != texto {
		t.Errorf("texto observable = %q; quiere %q", err.Error(), texto)
	}
}

// Los valores literales de los kinds viajan a contacts.kind y forman la clave de deduplicación.
func TestKinds_ValoresLiterales(t *testing.T) {
	for _, c := range []struct{ nombre, got, quiere string }{
		{"KindPhoneE164", KindPhoneE164, "phone_e164"},
		{"KindWALID", KindWALID, "wa_lid"},
		{"KindWAUsername", KindWAUsername, "wa_username"},
	} {
		t.Run(c.nombre, func(t *testing.T) {
			if c.got != c.quiere {
				t.Errorf("%s = %q; quiere el literal %q", c.nombre, c.got, c.quiere)
			}
		})
	}
}

// R-10: el texto base de ErrInvalidRef es observable y no cambia.
func TestErrInvalidRef_Texto(t *testing.T) {
	if got, quiere := ErrInvalidRef.Error(), "contact_ref inválida"; got != quiere {
		t.Fatalf("ErrInvalidRef = %q; quiere %q", got, quiere)
	}
}

// R-01: los tres kinds soportados son válidos.
func TestValidateKind_Soportados(t *testing.T) {
	for _, kind := range []string{KindPhoneE164, KindWALID, KindWAUsername} {
		t.Run(kind, func(t *testing.T) {
			if err := ValidateKind(kind); err != nil {
				t.Errorf("ValidateKind(%q) = %v; quiere nil", kind, err)
			}
		})
	}
}

// R-01: cualquier otro kind es desconocido, con ErrInvalidRef y el texto exacto; la
// comparación es exacta y sensible a mayúsculas.
func TestValidateKind_Desconocidos(t *testing.T) {
	for _, c := range []struct{ nombre, kind, texto string }{
		{"vacío", "", `contact_ref inválida: kind desconocido ""`},
		{"abreviatura phone", "phone", `contact_ref inválida: kind desconocido "phone"`},
		{"abreviatura lid", "lid", `contact_ref inválida: kind desconocido "lid"`},
		{"otro kind", "email", `contact_ref inválida: kind desconocido "email"`},
		{"sensible a mayúsculas", "PHONE_E164", `contact_ref inválida: kind desconocido "PHONE_E164"`},
		{"comparación exacta: blanco pegado", "wa_lid ", `contact_ref inválida: kind desconocido "wa_lid "`},
	} {
		t.Run(c.nombre, func(t *testing.T) {
			exigeErrorDeRef(t, ValidateKind(c.kind), c.texto)
		})
	}
}

// R-02: phone_e164 conserva solo los dígitos ASCII y descarta todo lo demás.
func TestNormalize_Telefono(t *testing.T) {
	for _, c := range []struct{ nombre, entrada, quiere string }{
		{"E.164 con +", "+14155552671", "14155552671"},
		{"con espacios, guiones y paréntesis", "+1 (415) 555-2671", "14155552671"},
		{"con puntos", "44.20.7946.0018", "442079460018"},
		{"ya normalizado no cambia", "573001112233", "573001112233"},
		{"letras y símbolos se descartan", "tel: 573001112233 (cel)", "573001112233"},
		{"los ceros a la izquierda se conservan", "00573001112233", "00573001112233"},
		{"15 dígitos, el máximo, es válido", "123456789012345", "123456789012345"},
	} {
		t.Run(c.nombre, func(t *testing.T) {
			got, err := Normalize(KindPhoneE164, c.entrada)
			if err != nil {
				t.Fatalf("Normalize(phone_e164, %q): error inesperado: %v", c.entrada, err)
			}
			if got != c.quiere {
				t.Errorf("Normalize(phone_e164, %q) = %q; quiere %q", c.entrada, got, c.quiere)
			}
		})
	}
}

// R-03: dos formatos del mismo número dan el mismo value; es la base de la deduplicación y del
// índice ciego.
func TestNormalize_Telefono_MismoNumeroEnDistintoFormato(t *testing.T) {
	const quiere = "573001112233"
	for _, entrada := range []string{
		"573001112233",
		"+57 300 111 2233",
		"57-300-111-2233",
		"(57) 300.111.2233",
	} {
		got, err := Normalize(KindPhoneE164, entrada)
		if err != nil || got != quiere {
			t.Errorf("Normalize(phone_e164, %q) = %q, %v; quiere %q", entrada, got, err, quiere)
		}
	}
}

// R-04: sin ningún dígito, o con más de 15, el phone es inválido. El texto dice la cuenta de
// dígitos, no el número; con error el value devuelto es "".
func TestNormalize_Telefono_Invalido(t *testing.T) {
	const sinDigitos = "contact_ref inválida: phone_e164 sin dígitos"
	for _, c := range []struct{ nombre, entrada, texto string }{
		{"vacío", "", sinDigitos},
		{"sin dígitos", "abc", sinDigitos},
		{"solo separadores", "+ - ()", sinDigitos},
		{"dígitos no ASCII no cuentan", "٥٧٣", sinDigitos},
		{"16 dígitos excede el máximo", "1234567890123456",
			"contact_ref inválida: phone_e164 con 16 dígitos excede el máximo 15"},
		{"20 dígitos excede el máximo", "12345678901234567890",
			"contact_ref inválida: phone_e164 con 20 dígitos excede el máximo 15"},
		{"cuenta dígitos, no caracteres", "+57 300 111 2233 4455",
			"contact_ref inválida: phone_e164 con 16 dígitos excede el máximo 15"},
	} {
		t.Run(c.nombre, func(t *testing.T) {
			got, err := Normalize(KindPhoneE164, c.entrada)
			exigeErrorDeRef(t, err, c.texto)
			if got != "" {
				t.Errorf("con error el value debe ser \"\"; dio %q", got)
			}
		})
	}
}

// R-05: wa_lid se queda con la parte de usuario: sin blancos de borde y sin lo que siga al
// primero de "@", "_" o ":".
func TestNormalize_LID(t *testing.T) {
	for _, c := range []struct{ nombre, entrada, quiere string }{
		{"con el servidor @lid", "123456789012345@lid", "123456789012345"},
		{"solo la parte de usuario", "123456789012345", "123456789012345"},
		{"con sufijo de dispositivo", "123456789012345:2@lid", "123456789012345"},
		{"con sufijo de agente", "123456789012345_1@lid", "123456789012345"},
		{"con agente y dispositivo", "123456789012345_1:2@lid", "123456789012345"},
		{"con blancos de borde", "  981054321@lid  ", "981054321"},
		{"con un blanco pegado al sufijo descartado", "981054321 :2@lid", "981054321"},
		{"cualquier servidor, no solo @lid", "981054321@s.whatsapp.net", "981054321"},
	} {
		t.Run(c.nombre, func(t *testing.T) {
			got, err := Normalize(KindWALID, c.entrada)
			if err != nil {
				t.Fatalf("Normalize(wa_lid, %q): error inesperado: %v", c.entrada, err)
			}
			if got != c.quiere {
				t.Errorf("Normalize(wa_lid, %q) = %q; quiere %q", c.entrada, got, c.quiere)
			}
		})
	}
}

// R-06: un wa_lid que queda vacío o no numérico es inválido; el texto da la longitud en bytes
// de la parte de usuario, nunca la parte misma.
func TestNormalize_LID_Invalido(t *testing.T) {
	const vacio = "contact_ref inválida: wa_lid vacío"
	for _, c := range []struct{ nombre, entrada, texto string }{
		{"vacío", "", vacio},
		{"solo blancos", "   ", vacio},
		{"solo el servidor", "@lid", vacio},
		{"solo el sufijo de dispositivo", ":2@lid", vacio},
		{"solo el sufijo de agente", "_1@lid", vacio},
		{"letras", "abc@lid",
			"contact_ref inválida: wa_lid con parte de usuario no numérica (longitud 3)"},
		{"letras entre dígitos", "12ab34@lid",
			"contact_ref inválida: wa_lid con parte de usuario no numérica (longitud 6)"},
		{"sin servidor y no numérico", "abcdef123",
			"contact_ref inválida: wa_lid con parte de usuario no numérica (longitud 9)"},
		{"la longitud es en bytes: dígitos no ASCII", "٥٧٣@lid",
			"contact_ref inválida: wa_lid con parte de usuario no numérica (longitud 6)"},
	} {
		t.Run(c.nombre, func(t *testing.T) {
			got, err := Normalize(KindWALID, c.entrada)
			exigeErrorDeRef(t, err, c.texto)
			if got != "" {
				t.Errorf("con error el value debe ser \"\"; dio %q", got)
			}
		})
	}
}

// R-07: wa_username va en minúsculas y sin blancos de borde, y no pierde sufijos.
func TestNormalize_Username(t *testing.T) {
	for _, c := range []struct{ nombre, entrada, quiere string }{
		{"minúsculas y recorte", "  JuanPerez  ", "juanperez"},
		{"ya normalizado no cambia", "juanperez", "juanperez"},
		{"no quita sufijos: _ y . son parte del nombre", "ANA_Lopez.99", "ana_lopez.99"},
		{"minúsculas de letras no ASCII", "  ÁNGEL  ", "ángel"},
	} {
		t.Run(c.nombre, func(t *testing.T) {
			got, err := Normalize(KindWAUsername, c.entrada)
			if err != nil {
				t.Fatalf("Normalize(wa_username, %q): error inesperado: %v", c.entrada, err)
			}
			if got != c.quiere {
				t.Errorf("Normalize(wa_username, %q) = %q; quiere %q", c.entrada, got, c.quiere)
			}
		})
	}
}

// R-07: un wa_username que queda vacío es inválido; su texto incluye el value recibido entre
// comillas (%q), que por construcción son solo blancos.
func TestNormalize_Username_Vacio(t *testing.T) {
	for _, c := range []struct{ nombre, entrada, texto string }{
		{"vacío", "", `contact_ref inválida: wa_username vacío: ""`},
		{"solo espacios", "   ", `contact_ref inválida: wa_username vacío: "   "`},
		{"solo tabulador y salto de línea", "\t\n", `contact_ref inválida: wa_username vacío: "\t\n"`},
	} {
		t.Run(c.nombre, func(t *testing.T) {
			got, err := Normalize(KindWAUsername, c.entrada)
			exigeErrorDeRef(t, err, c.texto)
			if got != "" {
				t.Errorf("con error el value debe ser \"\"; dio %q", got)
			}
		})
	}
}

// R-08: con un kind desconocido Normalize da el mismo error que ValidateKind, y su texto lleva
// el kind, no el value.
func TestNormalize_KindDesconocido(t *testing.T) {
	for _, c := range []struct{ nombre, kind, value, texto string }{
		{"otro kind", "email", "ana@example.com", `contact_ref inválida: kind desconocido "email"`},
		{"sensible a mayúsculas", "PHONE_E164", "573001112233",
			`contact_ref inválida: kind desconocido "PHONE_E164"`},
		{"kind vacío", "", "valor-sensible", `contact_ref inválida: kind desconocido ""`},
	} {
		t.Run(c.nombre, func(t *testing.T) {
			got, err := Normalize(c.kind, c.value)
			exigeErrorDeRef(t, err, c.texto)
			if got != "" {
				t.Errorf("con error el value debe ser \"\"; dio %q", got)
			}
			if strings.Contains(err.Error(), c.value) {
				t.Errorf("el error contiene el value %q: %q", c.value, err.Error())
			}
			if verr := ValidateKind(c.kind); verr == nil || verr.Error() != err.Error() {
				t.Errorf("Normalize y ValidateKind deben dar el mismo error: %v vs %v", err, verr)
			}
		})
	}
}

// R-11, a medias: los errores de phone_e164 y wa_lid NUNCA contienen el value recibido (puede ser
// un teléfono, PII, y el error sube a logs y al cliente). La excepción es wa_username vacío, que
// incluye el blanco entre comillas porque no es PII.
func TestNormalize_ErrorNoContieneElValue(t *testing.T) {
	for _, c := range []struct{ nombre, kind, value string }{
		{"phone sin dígitos", KindPhoneE164, "abc-xyz"},
		{"phone excede el máximo", KindPhoneE164, "12345678901234567890"},
		{"phone con separadores que excede", KindPhoneE164, "+57 300 111 2233 4455"},
		{"lid no numérico", KindWALID, "abcdef123"},
		{"lid con letras entre dígitos", KindWALID, "12ab34@lid"},
	} {
		t.Run(c.nombre, func(t *testing.T) {
			_, err := Normalize(c.kind, c.value)
			if err == nil {
				t.Fatalf("Normalize(%q, %q) no dio error", c.kind, c.value)
			}
			if strings.Contains(err.Error(), c.value) {
				t.Errorf("el error filtra el value crudo (PII): %q contiene %q", err.Error(), c.value)
			}
		})
	}

	t.Run("excepción: wa_username vacío incluye el blanco entre comillas", func(t *testing.T) {
		_, err := Normalize(KindWAUsername, "   ")
		if err == nil || !strings.Contains(err.Error(), `"   "`) {
			t.Fatalf("el error de wa_username vacío debe incluir el blanco entre comillas; dio %v", err)
		}
	})
}

// R-09: NewRef normaliza el value y deja el kind tal cual.
func TestNewRef_Normaliza(t *testing.T) {
	for _, c := range []struct {
		nombre, kind, value string
		quiere              Ref
	}{
		{"teléfono", KindPhoneE164, "+1 415-555-2671", Ref{Kind: KindPhoneE164, Value: "14155552671"}},
		{"lid", KindWALID, "123456789012345:2@lid", Ref{Kind: KindWALID, Value: "123456789012345"}},
		{"username", KindWAUsername, "  JuanPerez ", Ref{Kind: KindWAUsername, Value: "juanperez"}},
	} {
		t.Run(c.nombre, func(t *testing.T) {
			got, err := NewRef(c.kind, c.value)
			if err != nil {
				t.Fatalf("NewRef(%q, %q): error inesperado: %v", c.kind, c.value, err)
			}
			if got != c.quiere {
				t.Errorf("NewRef(%q, %q) = %+v; quiere %+v", c.kind, c.value, got, c.quiere)
			}
		})
	}
}

// R-09: si Normalize falla, NewRef devuelve la Ref cero y el mismo error, con el mismo texto.
func TestNewRef_Invalida(t *testing.T) {
	for _, c := range []struct{ nombre, kind, value string }{
		{"kind desconocido", "nope", "x"},
		{"phone sin dígitos", KindPhoneE164, "abc"},
		{"phone que excede el máximo", KindPhoneE164, "1234567890123456"},
		{"lid no numérico", KindWALID, "abc@lid"},
		{"username vacío", KindWAUsername, "  "},
	} {
		t.Run(c.nombre, func(t *testing.T) {
			got, err := NewRef(c.kind, c.value)
			_, nerr := Normalize(c.kind, c.value)
			if err == nil || nerr == nil {
				t.Fatalf("NewRef y Normalize deben fallar con (%q, %q): %v, %v", c.kind, c.value, err, nerr)
			}
			exigeErrorDeRef(t, err, nerr.Error())
			if got != (Ref{}) {
				t.Errorf("con error la Ref debe ser la cero; dio %+v", got)
			}
		})
	}
}

// Ref es comparable y NewRef da la misma Ref para dos formatos del mismo número: es lo que
// deduplica (tenant, kind, value). El valor cero de Ref no es una referencia válida.
func TestRef_ComparableYValorCero(t *testing.T) {
	a, errA := NewRef(KindPhoneE164, "+57 300 111 2233")
	b, errB := NewRef(KindPhoneE164, "573001112233")
	if errA != nil || errB != nil {
		t.Fatalf("NewRef: %v, %v", errA, errB)
	}
	if a != b {
		t.Errorf("dos formatos del mismo número deben dar la misma Ref: %+v != %+v", a, b)
	}
	vistas := map[Ref]struct{}{a: {}, b: {}}
	if len(vistas) != 1 {
		t.Errorf("Ref debe servir de clave de mapa y deduplicar: %d claves; quiere 1", len(vistas))
	}
	otroKind := Ref{Kind: KindWALID, Value: a.Value}
	if otroKind == a {
		t.Errorf("el mismo Value con otro Kind es otra Ref: %+v == %+v", otroKind, a)
	}
	if err := ValidateKind(Ref{}.Kind); !errors.Is(err, ErrInvalidRef) {
		t.Errorf("el kind de la Ref cero no es válido: ValidateKind = %v", err)
	}
}
