package casebank_test

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/casebank"
)

// anonymize_corpus_test.go — el CORPUS ADVERSARIO del anonimizador (hallazgo 40) y
// la equivalencia viejo ↔ nuevo.
//
// Los valores esperados NO están deducidos del contrato: son lo que devolvió el
// paquete VIEJO (internal/casebank @ 8d875ab) al ejecutarlo sobre estas mismas
// entradas, con la misma lista de nombres. Este paquete no puede importar el
// viejo (candado de fronteras, 05 E-8), así que se fijan a mano. Quien cambie una
// fila está cambiando la conducta respecto al viejo, y lo tiene que decir.
//
// 🔴 VARIAS FILAS FIJAN UN AGUJERO, NO UN ACIERTO: un teléfono en dígitos
// árabes-índicos o de ancho completo, con un espacio duro por medio, o dos
// teléfonos seguidos, PASAN ENTEROS. Están aquí para que el día que alguien cierre
// uno se entere de que cambia el alcance (y lo quite de la cabecera de
// anonymize.go), no para darlos por buenos.

// corpusNames es la lista del corpus: un nombre compuesto y su prefijo, dos
// acentuados, uno con un signo que en una expresión regular sería un
// metacarácter, y dos entradas vacías que se descartan.
func corpusNames() []string {
	return []string{"Ambar", "Herminia", "Fusión", "Ana", "Ana María", "José", "A.B", "  ", ""}
}

// TestNewAnonymizer_CorpusNames_TrimmedAndLongestFirst: las vacías se caen y el
// orden es de más largo a más corto EN BYTES («Fusión» son 7, por delante de
// «Ambar»), estable entre iguales.
func TestNewAnonymizer_CorpusNames_TrimmedAndLongestFirst(t *testing.T) {
	want := []string{"Ana María", "Herminia", "Fusión", "Ambar", "José", "Ana", "A.B"}
	if got := casebank.NewAnonymizer(corpusNames()...).Names(); !reflect.DeepEqual(got, want) {
		t.Errorf("Names() = %q; el viejo devolvía %q", got, want)
	}
}

// TestAnonymize_AdversarialCorpus_SameAsOld: cada entrada sale como salía del
// viejo, y las dos mitades no se contradicen —el barrido delata algo si y solo si
// la redacción cambia el texto—. Tampoco queda nada que barrer en lo ya
// redactado, salvo en el contraejemplo de
// TestAnonymize_TwoGluedJIDs_LeavesAJIDBehind.
func TestAnonymize_AdversarialCorpus_SameAsOld(t *testing.T) {
	corpus := []struct{ in, want string }{
		// vacíos y solo espacios
		{"", ""},
		{"   ", "   "},
		{"\n", "\n"},
		// teléfonos: separadores repetidos
		{"+58--412--123--4567", "[TELEFONO]"},
		{"0412  //  1234567", "[TELEFONO]"},
		{"0412..1234567", "[TELEFONO]"},
		{"(0212)) ((555)) 6677", "([TELEFONO]"},
		{"++58 412 123 4567", "+[TELEFONO]"},
		{"0412-123-4567-", "[TELEFONO]-"},
		{"-0412-123-4567", "-[TELEFONO]"},
		{"llama al (0412) 123.45.67.", "llama al ([TELEFONO]."},
		{"0412\t123\t4567", "[TELEFONO]"},
		{"0412 \r\n 1234567", "[TELEFONO]"},
		// conteo
		{"1234567", "1234567"},
		{"12345678", "[TELEFONO]"},
		{"123456789012345", "[TELEFONO]"},
		{"1234567890123456", "1234567890123456"},
		{"1 2 3 4 5 6 7 8", "[TELEFONO]"},
		{"1 2 3 4 5 6 7", "1 2 3 4 5 6 7"},
		// dos teléfonos
		{"04121234567 04149876543", "04121234567 04149876543"},
		{"04121234567, 04149876543", "[TELEFONO], [TELEFONO]"},
		{"04121234567 y 04149876543", "[TELEFONO] y [TELEFONO]"},
		{"0412-1234567\n0414-9876543", "0412-1234567\n0414-9876543"},
		// pegados a texto
		{"tel04121234567", "tel04121234567"},
		{"04121234567bs", "04121234567bs"},
		{"tel:04121234567", "tel:[TELEFONO]"},
		{"04121234567.", "[TELEFONO]."},
		{"\u00f104121234567", "\u00f104121234567"},
		{"04121234567\u00f1", "04121234567\u00f1"},
		{"n\u00b004121234567", "n\u00b0[TELEFONO]"},
		// dígitos no ASCII
		{"\u0660\u0664\u0661\u0662\u0661\u0662\u0663\u0664\u0665\u0666\u0667", "\u0660\u0664\u0661\u0662\u0661\u0662\u0663\u0664\u0665\u0666\u0667"},
		{"\uff10\uff14\uff11\uff12\uff11\uff12\uff13\uff14\uff15\uff16\uff17", "\uff10\uff14\uff11\uff12\uff11\uff12\uff13\uff14\uff15\uff16\uff17"},
		{"0412\uff11234567", "0412\uff11234567"},
		{"\uff1104121234567", "\uff1104121234567"},
		{"04121234567\u0660", "04121234567\u0660"},
		{"0412 \u0661\u0662\u0663 4567", "0412 \u0661\u0662\u0663 4567"},
		{"+58 412 123 4567 \u0663", "[TELEFONO] \u0663"},
		// espacios Unicode y separadores exóticos
		{"0412\u00a0123\u00a04567", "0412\u00a0123\u00a04567"},
		{"0412\u202f1234567", "0412\u202f1234567"},
		{"0412\u20091234567", "0412\u20091234567"},
		{"0412\u30001234567", "0412\u30001234567"},
		{"0412\u20131234567", "0412\u20131234567"},
		{"0412\u20141234567", "0412\u20141234567"},
		{"0412\u00b71234567", "0412\u00b71234567"},
		{"04121234\u00a0567", "[TELEFONO]\u00a0567"},
		{"0412\u200b1234567", "0412\u200b1234567"},
		// nombres
		{"Fusi\u00f3n", "[NOMBRE]"},
		{"\u00bfFusi\u00f3n?", "\u00bf[NOMBRE]?"},
		{"FUSI\u00d3N cerr\u00f3", "[NOMBRE] cerr\u00f3"},
		{"fusi\u00f3n", "[NOMBRE]"},
		{"fusion", "fusion"},
		{"Fusi\u00f3nes", "Fusi\u00f3nes"},
		{"laFusi\u00f3n", "laFusi\u00f3n"},
		{"Fusio\u0301n", "Fusio\u0301n"},
		{"Ana\u0301 llam\u00f3", "[NOMBRE]\u0301 llam\u00f3"},
		{"Jos\u00e9,", "[NOMBRE],"},
		{"Jos\u00e9phine", "Jos\u00e9phine"},
		{"Jos\u00e9e", "Jos\u00e9e"},
		{"xJos\u00e9", "xJos\u00e9"},
		{"JOS\u00c9", "[NOMBRE]"},
		{"Jos\u00e9 Jos\u00e9", "[NOMBRE] [NOMBRE]"},
		{"Jos\u00e9Jos\u00e9", "Jos\u00e9Jos\u00e9"},
		{"Ana Mar\u00eda", "[NOMBRE]"},
		{"Ana  Mar\u00eda", "[NOMBRE]  Mar\u00eda"},
		{"Ana\u00a0Mar\u00eda", "[NOMBRE]\u00a0Mar\u00eda"},
		{"Ana_Mar\u00eda", "[NOMBRE]_Mar\u00eda"},
		{"Ana-Mar\u00eda", "[NOMBRE]-Mar\u00eda"},
		{"ANA MAR\u00cdA", "[NOMBRE]"},
		{"Ana Mar\u00edana", "Ana Mar\u00edana"},
		{"Ana\nMar\u00eda", "[NOMBRE]\nMar\u00eda"},
		{"Ambar2", "Ambar2"},
		{"2Ambar", "2Ambar"},
		{"Ambar.", "[NOMBRE]."},
		{"(Ambar)", "([NOMBRE])"},
		{"Ambar/Herminia", "[NOMBRE]/[NOMBRE]"},
		{"Ambar'Herminia", "[NOMBRE]'[NOMBRE]"},
		{"ambar@ambar", "[NOMBRE]@[NOMBRE]"},
		{"\u00c1mbar", "\u00c1mbar"},
		{"A.B", "[NOMBRE]"},
		{"AxB", "AxB"},
		{"a.b vino", "[NOMBRE] vino"},
		{"A.Bc", "A.Bc"},
		{"Ana\u0661", "Ana\u0661"},
		{"Ana\u3000Ambar", "[NOMBRE]\u3000[NOMBRE]"},
		// JIDs
		{"x584121234567@s.whatsapp.net", "[JID]"},
		{"584121234567@s.whatsapp.netx", "[TELEFONO]@s.whatsapp.netx"},
		{"@s.whatsapp.net", "@s.whatsapp.net"},
		{"a@@g.us", "a@@g.us"},
		{"584121234567@@s.whatsapp.net", "[TELEFONO]@@s.whatsapp.net"},
		{"584121234567@S.WHATSAPP.NET", "[JID]"},
		{"\u00f1584121234567@lid", "\u00f1584121234567@lid"},
		{"user@lidia", "user@lidia"},
		{"grupo:120363012345678901@g.us", "[JID]"},
		{"584121234567:12@s.whatsapp.net", "[JID]"},
		{"status@broadcast", "[JID]"},
		{"584121234567@broadcast.", "[JID]."},
		{"(584121234567@c.us)", "([JID])"},
		{"584121234567@s.whatsapp.net584121234567@s.whatsapp.net", "[TELEFONO]@s.whatsapp.net584121234567@s.whatsapp.net"},
		{"584121234567@s.whatsapp.net,584121234568@g.us", "[JID],[JID]"},
		{"a@lid@g.us", "[JID]@g.us"},
		{"584121234567 @s.whatsapp.net", "[TELEFONO] @s.whatsapp.net"},
		{"584121234567@ s.whatsapp.net", "[TELEFONO]@ s.whatsapp.net"},
		{"ambar.perez@gmail.com", "[NOMBRE].perez@gmail.com"},
		{"Ambar@g.us", "[JID]"},
		{"Jos\u00e9@lid", "[NOMBRE]@lid"},
		{"584121234567@s.whatsapp.net de Ambar al +58 412 123 4567", "[JID] de [NOMBRE] al [TELEFONO]"},
		{"\uff10\uff14\uff11\uff12@lid", "\uff10\uff14\uff11\uff12@lid"},
		{"584121234567@s\u2024whatsapp\u2024net", "[TELEFONO]@s\u2024whatsapp\u2024net"},
	}
	a := casebank.NewAnonymizer(corpusNames()...)
	for i, c := range corpus {
		t.Run(fmt.Sprintf("%03d_%+q", i, c.in), func(t *testing.T) {
			got := a.Anonymize(c.in)
			if got != c.want {
				t.Errorf("Anonymize(%+q) = %+q; el viejo devolvía %+q", c.in, got, c.want)
			}
			remains := a.Remains(c.in)
			if changed := got != c.in; changed != (len(remains) != 0) {
				t.Errorf("las dos mitades se contradicen sobre %+q: Anonymize lo cambia = %t, Remains = %+v",
					c.in, changed, remains)
			}
			if again := a.Remains(got); len(again) != 0 && c.in != twoGluedJIDs {
				t.Errorf("Remains(Anonymize(%+q)) = %+v; sobre lo ya redactado tiene que estar vacío", c.in, again)
			}
		})
	}
}

// twoGluedJIDs son dos JID sin nada entre ellos.
const twoGluedJIDs = "584121234567@s.whatsapp.net584121234567@s.whatsapp.net"

// TestAnonymize_TwoGluedJIDs_LeavesAJIDBehind fija, con los valores del viejo, el
// contraejemplo de «Remains(Anonymize(x)) está vacío siempre», que la cabecera del
// fichero viejo afirmaba y NO es verdad (medido en F7, hallazgo 40).
//
// 🔴 ES UNA FUGA, NO UNA CURIOSIDAD: ninguno de los dos JID pasa el límite de
// palabra (el primero tiene un dígito detrás, y el patrón no vuelve a intentarlo
// dentro de lo ya consumido), así que la pasada de JID no tapa nada; la de
// teléfonos se lleva el número de delante, y lo que queda —el segundo número con
// su dominio— sale EN CLARO de `Anonymize`. Al barrer esa salida, el `@` que
// ahora precede al resto sí es un límite y el barrido lo delata. Se conserva la
// conducta del viejo; cerrarlo es cambiar el alcance del anonimizador.
func TestAnonymize_TwoGluedJIDs_LeavesAJIDBehind(t *testing.T) {
	a := casebank.NewAnonymizer(corpusNames()...)
	const wantOut = "[TELEFONO]@s.whatsapp.net584121234567@s.whatsapp.net"
	out := a.Anonymize(twoGluedJIDs)
	if out != wantOut {
		t.Fatalf("Anonymize(%q) = %q; el viejo devolvía %q", twoGluedJIDs, out, wantOut)
	}
	want := []casebank.Finding{
		{Class: casebank.ClassJID, Text: "s.whatsapp.net584121234567@s.whatsapp.net", Start: 11, End: 52},
	}
	if got := a.Remains(out); !reflect.DeepEqual(got, want) {
		t.Errorf("Remains(%q) = %+v; el viejo devolvía %+v", out, got, want)
	}
}

// TestRemains_AdversarialCorpus_SameAsOld fija los hallazgos ENTEROS —clase,
// texto e índices de byte— que devolvía el viejo: los índices con no-ASCII por
// delante, el JID que se queda con el teléfono que lleva dentro, el nombre largo
// antes que su prefijo y los agujeros en los que el barrido dice «limpio».
func TestRemains_AdversarialCorpus_SameAsOld(t *testing.T) {
	const (
		jid   = casebank.ClassJID
		phone = casebank.ClassPhone
		name  = casebank.ClassName
	)
	corpus := []struct {
		in   string
		want []casebank.Finding
	}{
		{"Ambar escribió al +58 412 123 4567 desde 584121234567@s.whatsapp.net", []casebank.Finding{
			{Class: name, Text: "Ambar", Start: 0, End: 5},
			{Class: phone, Text: "+58 412 123 4567", Start: 19, End: 35},
			{Class: jid, Text: "584121234567@s.whatsapp.net", Start: 42, End: 69},
		}},
		{"Fusión llamó al 0412-123-4567", []casebank.Finding{
			{Class: name, Text: "Fusión", Start: 0, End: 7},
			{Class: phone, Text: "0412-123-4567", Start: 18, End: 31},
		}},
		// Un solo dato: el JID gana al teléfono que lleva dentro.
		{"584121234567@s.whatsapp.net", []casebank.Finding{
			{Class: jid, Text: "584121234567@s.whatsapp.net", Start: 0, End: 27},
		}},
		// Y al nombre que lleva dentro.
		{"Ambar@g.us", []casebank.Finding{{Class: jid, Text: "Ambar@g.us", Start: 0, End: 10}}},
		{"Ana María y Ana", []casebank.Finding{
			{Class: name, Text: "Ana María", Start: 0, End: 10},
			{Class: name, Text: "Ana", Start: 13, End: 16},
		}},
		{"x 0412--123--4567 x", []casebank.Finding{{Class: phone, Text: "0412--123--4567", Start: 2, End: 17}}},
		// El JID pegado por la derecha no es JID: queda el teléfono de delante.
		{"584121234567@s.whatsapp.netx", []casebank.Finding{{Class: phone, Text: "584121234567", Start: 0, End: 12}}},
		// 🔴 Los agujeros: el barrido dice «limpio».
		{"04121234567 04149876543", []casebank.Finding{}},
		{"０４１２１２３４５６７ y 0412 123 4567", []casebank.Finding{}},
		{"ñ584121234567@lid", []casebank.Finding{}},
		{"", []casebank.Finding{}},
	}
	a := casebank.NewAnonymizer(corpusNames()...)
	for i, c := range corpus {
		t.Run(fmt.Sprintf("%02d_%+q", i, c.in), func(t *testing.T) {
			got := a.Remains(c.in)
			if got == nil {
				t.Fatalf("Remains(%+q) = nil; promete un slice vacío, no nil", c.in)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("Remains(%+q) =\n   %+v\nel viejo devolvía\n   %+v", c.in, got, c.want)
			}
			for _, f := range got {
				if c.in[f.Start:f.End] != f.Text {
					t.Errorf("hallazgo %+v: Text no es texto[Start:End] (%+q)", f, c.in[f.Start:f.End])
				}
			}
		})
	}
}
