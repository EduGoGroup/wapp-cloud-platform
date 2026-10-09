package casebank_test

import (
	"reflect"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/casebank"
)

// anonymize_phones_test.go — las promesas de anonymize_phones.go que el viejo NO
// tenía: varios teléfonos seguidos se redactan todos.
//
// 🔴 TODO ESTE FICHERO DIVERGE DEL VIEJO A PROPÓSITO (hallazgo 1 de F7, decisión
// de Jhoan del 2026-10-08). El viejo fundía los números en una racha de más de 15
// dígitos y la dejaba pasar ENTERA, con el barrido diciendo «limpio». Como en
// anonymize_test.go, cada promesa va en dos mitades: «esto se tapa» y «esto NO se
// toca».

// TestAnonymize_PhonesInARow_AllRedacted: una racha con separadores y más de 15
// dígitos son varios números, y no queda ni un dígito de ninguno.
func TestAnonymize_PhonesInARow_AllRedacted(t *testing.T) {
	cases := []struct {
		name, in, want string
		findings       int
	}{
		{"space", "04121234567 04149876543", "[TELEFONO] [TELEFONO]", 2},
		{"line feed", "0412-1234567\n0414-9876543", "[TELEFONO]\n[TELEFONO]", 2},
		{"carriage return and line feed", "04121234567\r\n04149876543", "[TELEFONO]\r\n[TELEFONO]", 2},
		{"slash", "0412-1234567/0414-9876543", "[TELEFONO]/[TELEFONO]", 2},
		{"three", "04121234567 04149876543 02125556677", "[TELEFONO] [TELEFONO] [TELEFONO]", 3},
		// El corte NO es voraz: «0412 123 4567 0414» suma 15 y dejaría «987 6543»
		// (7 dígitos) en claro. Se elige el reparto que no deja nada.
		{"every group spaced", "0412 123 4567 0414 987 6543", "[TELEFONO] [TELEFONO]", 2},
		{"inside a sentence", "llama al 04121234567 04149876543, gracias", "llama al [TELEFONO] [TELEFONO], gracias", 2},
		{"plus on the first", "+584121234567 04149876543", "[TELEFONO] [TELEFONO]", 2},
	}
	a := newTestAnonymizer()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := a.Anonymize(c.in)
			if got != c.want {
				t.Errorf("Anonymize(%q) = %q; se esperaba %q", c.in, got, c.want)
			}
			if left := countDigits(got); left != 0 {
				t.Errorf("Anonymize(%q) = %q; quedaron %d dígitos en claro", c.in, got, left)
			}
			remains := a.Remains(c.in)
			if len(remains) != c.findings {
				t.Fatalf("Remains(%q) = %+v; se esperaban %d hallazgos", c.in, remains, c.findings)
			}
			for _, f := range remains {
				if f.Class != casebank.ClassPhone {
					t.Errorf("Remains(%q) delató un %q; se esperaba %q", c.in, f.Class, casebank.ClassPhone)
				}
			}
			if again := a.Remains(got); len(again) != 0 {
				t.Errorf("Remains(%q) = %+v; sobre lo ya redactado tiene que estar vacío", got, again)
			}
		})
	}
}

// TestAnonymize_OnePhoneWithInnerSeparators_StaysOne es la otra mitad: hasta 15
// dígitos, los separadores de dentro no parten nada. UNA marca, UN hallazgo.
func TestAnonymize_OnePhoneWithInnerSeparators_StaysOne(t *testing.T) {
	cases := []struct{ in, want string }{
		{"0412 123 45 67", "[TELEFONO]"},
		{"+58 412-123.45.67", "[TELEFONO]"},
		{"+58 (412) 123 45 67", "[TELEFONO]"},
		{"1 2 3 4 5 6 7 8 9 0 1 2 3 4 5", "[TELEFONO]"},
	}
	a := newTestAnonymizer()
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := a.Anonymize(c.in); got != c.want {
				t.Errorf("Anonymize(%q) = %q; se esperaba %q: es UN teléfono", c.in, got, c.want)
			}
			if r := a.Remains(c.in); len(r) != 1 || r[0].Text != c.in {
				t.Errorf("Remains(%q) = %+v; se esperaba UN hallazgo con el número entero", c.in, r)
			}
		})
	}
}

// TestAnonymize_LongDigitRunWithoutSeparators_Untouched: más de 15 dígitos
// SEGUIDOS no son un teléfono ni varios —un número de pedido, de cuenta—, y pasan
// como pasaban en el viejo. Pegados a un teléfono, el teléfono cae y ellos no.
func TestAnonymize_LongDigitRunWithoutSeparators_Untouched(t *testing.T) {
	cases := []struct{ in, want string }{
		{"1234567890123456", "1234567890123456"},
		{"pedido 12345678901234567890", "pedido 12345678901234567890"},
		{"+12345678901234567890", "+12345678901234567890"},
		{"12345678901234567890 2 tortas", "12345678901234567890 2 tortas"},
		{"12345678901234567890 04121234567", "12345678901234567890 [TELEFONO]"},
		{"04121234567 12345678901234567890", "[TELEFONO] 12345678901234567890"},
	}
	a := newTestAnonymizer()
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := a.Anonymize(c.in)
			if got != c.want {
				t.Errorf("Anonymize(%q) = %q; se esperaba %q", c.in, got, c.want)
			}
			if changed := got != c.in; changed != (len(a.Remains(c.in)) != 0) {
				t.Errorf("las dos mitades se contradicen sobre %q: Anonymize lo cambia = %t, Remains = %+v",
					c.in, changed, a.Remains(c.in))
			}
		})
	}
}

// TestRemains_PhonesInARow_HowTheRunIsCut fija el REPARTO, que en `Anonymize` no
// se ve (dos repartos distintos dan las mismas marcas): el que deja menos dígitos
// en claro; a igualdad, el de menos teléfonos; a igualdad, el primero lo más
// largo posible. El trozo que no cabe en ningún teléfono se queda fuera.
func TestRemains_PhonesInARow_HowTheRunIsCut(t *testing.T) {
	phone := func(text string, start int) casebank.Finding {
		return casebank.Finding{Class: casebank.ClassPhone, Text: text, Start: start, End: start + len(text)}
	}
	cases := []struct {
		name, in string
		want     []casebank.Finding
	}{
		// 11 + 11: el único reparto que tapa los 22 dígitos.
		{"no digit left out", "0412 123 4567 0414 987 6543", []casebank.Finding{
			phone("0412 123 4567", 0), phone("0414 987 6543", 14),
		}},
		// 12 + 12 y 8 + 8 + 8 tapan lo mismo: gana el de menos teléfonos.
		{"fewer phones on a tie", "1234 5678 1234 5678 1234 5678", []casebank.Finding{
			phone("1234 5678 1234", 0), phone("5678 1234 5678", 15),
		}},
		// 12 + 8 y 8 + 12 empatan en todo: el primero, lo más largo posible.
		{"longest first on a tie", "12345678 1234 12345678", []casebank.Finding{
			phone("12345678 1234", 0), phone("12345678", 14),
		}},
		// «12» no cabe con los 14 de detrás (serían 16) ni llega a 8 solo.
		{"a short piece stays out", "12 04121234567890", []casebank.Finding{phone("04121234567890", 3)}},
		// 15 exactos caben en un teléfono; el suelo de 8 vale también aquí.
		// Los 7 del medio no llegan a 8 solos ni caben con los 9 de detrás (16).
		{"ceiling and floor inside the run", "123456789012345 1234567 123456789", []casebank.Finding{
			phone("123456789012345", 0), phone("123456789", 24),
		}},
		// El `+` solo entra con el primer trozo.
		{"plus sign", "+584121234567 04149876543", []casebank.Finding{
			phone("+584121234567", 0), phone("04149876543", 14),
		}},
	}
	a := newTestAnonymizer()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := a.Remains(c.in); !reflect.DeepEqual(got, c.want) {
				t.Errorf("Remains(%q) =\n   %+v\nse esperaba\n   %+v", c.in, got, c.want)
			}
		})
	}
}
