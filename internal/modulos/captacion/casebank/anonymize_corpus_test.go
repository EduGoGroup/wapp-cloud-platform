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
// 🔴 LAS FILAS MARCADAS «DIVERGE DEL VIEJO» YA NO SON LO QUE DEVOLVÍA EL VIEJO:
// son agujeros de PII que el viejo dejaba pasar y que el paquete nuevo CIERRA
// (hallazgo 1 de F7, decisión de Jhoan del 2026-10-08). El comentario de cada una
// dice qué devolvía el viejo.
//
// 🔴 OTRAS FILAS FIJAN UN AGUJERO, NO UN ACIERTO: un teléfono con un espacio
// duro u otro separador exótico por medio PASA ENTERO. Están aquí para que el día que alguien cierre
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

// corpusCase es una fila del corpus: la entrada y lo que `Anonymize` devuelve.
type corpusCase struct{ in, want string }

// adversarialCorpus es el corpus entero. Es una función para que lo recorran el
// test de equivalencia y el de la propiedad `Remains(Anonymize(x))`.
func adversarialCorpus() []corpusCase {
	return []corpusCase{
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
		// DIVERGE DEL VIEJO (hallazgo 1 de F7, decisión de Jhoan 2026-10-08): el viejo
		// devolvía la entrada INTACTA, los dos números en claro.
		{"04121234567 04149876543", "[TELEFONO] [TELEFONO]"},
		{"04121234567, 04149876543", "[TELEFONO], [TELEFONO]"},
		{"04121234567 y 04149876543", "[TELEFONO] y [TELEFONO]"},
		// DIVERGE DEL VIEJO (ídem): el viejo devolvía la entrada INTACTA.
		{"0412-1234567\n0414-9876543", "[TELEFONO]\n[TELEFONO]"},
		// pegados a texto
		{"tel04121234567", "tel04121234567"},
		{"04121234567bs", "04121234567bs"},
		{"tel:04121234567", "tel:[TELEFONO]"},
		{"04121234567.", "[TELEFONO]."},
		{"\u00f104121234567", "\u00f104121234567"},
		{"04121234567\u00f1", "04121234567\u00f1"},
		{"n\u00b004121234567", "n\u00b0[TELEFONO]"},
		// dígitos no ASCII. DIVERGEN DEL VIEJO las siete filas (hallazgo 1 de F7,
		// decisión de Jhoan 2026-10-08): el viejo devolvía las seis primeras INTACTAS
		// y, de la séptima, "[TELEFONO] ٣" (ahora el «٣» es un trozo más del número,
		// como lo sería un «3»).
		{"٠٤١٢١٢٣٤٥٦٧", "[TELEFONO]"},
		{"０４１２１２３４５６７", "[TELEFONO]"},
		{"0412１234567", "[TELEFONO]"},
		{"１04121234567", "[TELEFONO]"},
		{"04121234567٠", "[TELEFONO]"},
		{"0412 ١٢٣ 4567", "[TELEFONO]"},
		{"+58 412 123 4567 ٣", "[TELEFONO]"},
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
		// DIVERGE DEL VIEJO (hallazgo 1 de F7, decisión de Jhoan 2026-10-08): el viejo
		// devolvía "[TELEFONO]@s.whatsapp.net584121234567@s.whatsapp.net".
		{twoGluedJIDs, "[JID][JID]"},
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
}

// TestAnonymize_AdversarialCorpus_SameAsOld: cada entrada sale como salía del
// viejo —salvo las filas marcadas «DIVERGE DEL VIEJO»— y las dos mitades no se
// contradicen: el barrido delata algo si y solo si la redacción cambia el texto.
func TestAnonymize_AdversarialCorpus_SameAsOld(t *testing.T) {
	a := casebank.NewAnonymizer(corpusNames()...)
	for i, c := range adversarialCorpus() {
		t.Run(fmt.Sprintf("%03d_%+q", i, c.in), func(t *testing.T) {
			got := a.Anonymize(c.in)
			if got != c.want {
				t.Errorf("Anonymize(%+q) = %+q; se esperaba %+q", c.in, got, c.want)
			}
			remains := a.Remains(c.in)
			if changed := got != c.in; changed != (len(remains) != 0) {
				t.Errorf("las dos mitades se contradicen sobre %+q: Anonymize lo cambia = %t, Remains = %+v",
					c.in, changed, remains)
			}
		})
	}
}

// jidAfterAccentedName es la ÚNICA excepción declarada a la propiedad de abajo: un
// JID cuya parte local empieza por un signo de su clase (`.`) pegado a un nombre
// de la lista que acaba en letra no ASCII.
const jidAfterAccentedName = "Jos\u00e9.maria@lid"

// TestAnonymize_ThenRemains_EmptyOverTheWholeCorpus es el invariante de la
// cabecera de anonymize.go convertido en test: para TODA entrada del corpus, lo
// que sale de `Anonymize` no le deja nada al barrido. Las excepciones se enumeran
// aquí, una a una; hoy es una sola, y tiene su propio test.
func TestAnonymize_ThenRemains_EmptyOverTheWholeCorpus(t *testing.T) {
	declaredExceptions := map[string]bool{jidAfterAccentedName: true}
	corpus := adversarialCorpus()
	inputs := make([]string, 0, len(corpus)+1)
	inputs = append(inputs, jidAfterAccentedName)
	for _, c := range corpus {
		inputs = append(inputs, c.in)
	}
	a := casebank.NewAnonymizer(corpusNames()...)
	for i, in := range inputs {
		t.Run(fmt.Sprintf("%03d_%+q", i, in), func(t *testing.T) {
			out := a.Anonymize(in)
			again := a.Remains(out)
			if declaredExceptions[in] {
				if len(again) == 0 {
					t.Errorf("Remains(Anonymize(%+q)) está vacío: ya no es una excepción, quítala de la lista", in)
				}
				return
			}
			if len(again) != 0 {
				t.Errorf("Remains(Anonymize(%+q)) = %+v sobre %+q; sobre lo ya redactado tiene que estar vacío", in, again, out)
			}
		})
	}
}

// TestAnonymize_JIDAfterAccentedName_LeavesAJIDBehind fija la excepción: «é» no es
// de la clase de la parte local, así que el JID candidato es «.maria@lid», y
// pegado a una letra no es JID (igual que «ñ584121234567@lid»). La pasada de
// nombres tapa «José» y su marca deja al descubierto un límite que antes no
// existía: barrido después, «.maria@lid» SÍ es un JID. No sale ningún número —un
// número ahí lo taparía la pasada de teléfonos—, pero la propiedad no es
// universal y se dice. Conducta heredada del viejo, medida en el paquete nuevo
// (las tres pasadas son las suyas); no la cambia ninguno de los tres arreglos.
func TestAnonymize_JIDAfterAccentedName_LeavesAJIDBehind(t *testing.T) {
	a := casebank.NewAnonymizer(corpusNames()...)
	const wantOut = "[NOMBRE].maria@lid"
	out := a.Anonymize(jidAfterAccentedName)
	if out != wantOut {
		t.Fatalf("Anonymize(%+q) = %q; se esperaba %q", jidAfterAccentedName, out, wantOut)
	}
	want := []casebank.Finding{{Class: casebank.ClassJID, Text: ".maria@lid", Start: 8, End: 18}}
	if got := a.Remains(out); !reflect.DeepEqual(got, want) {
		t.Errorf("Remains(%q) = %+v; se esperaba %+v", out, got, want)
	}
}

// twoGluedJIDs son dos JID sin nada entre ellos.
const twoGluedJIDs = "584121234567@s.whatsapp.net584121234567@s.whatsapp.net"

// TestAnonymize_GluedJIDs_AllRedacted: una cadena de JID pegados cae entera, con
// una marca por JID, y el barrido los delata uno a uno sobre el texto sin redactar.
//
// 🔴 DIVERGE DEL VIEJO A PROPÓSITO (hallazgo 1 de F7, decisión de Jhoan del
// 2026-10-08). El viejo devolvía
// «[TELEFONO]@s.whatsapp.net584121234567@s.whatsapp.net» —el segundo número EN
// CLARO— y, barrida esa salida, un JID: ninguno de los dos pasaba el límite de
// palabra, porque cada uno tiene al otro pegado.
func TestAnonymize_GluedJIDs_AllRedacted(t *testing.T) {
	a := casebank.NewAnonymizer(corpusNames()...)
	const jid = "584121234567@s.whatsapp.net"
	cases := []struct {
		name, in, want string
		findings       int
	}{
		{"two", twoGluedJIDs, "[JID][JID]", 2},
		{"three", jid + "584149876543@g.us" + jid, "[JID][JID][JID]", 3},
		{"two inside a sentence", "de " + twoGluedJIDs + ", gracias", "de [JID][JID], gracias", 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := a.Anonymize(c.in)
			if out != c.want {
				t.Fatalf("Anonymize(%q) = %q; se esperaba %q", c.in, out, c.want)
			}
			if left := countDigits(out); left != 0 {
				t.Errorf("Anonymize(%q) = %q; quedaron %d dígitos en claro", c.in, out, left)
			}
			if again := a.Remains(out); len(again) != 0 {
				t.Errorf("Remains(%q) = %+v; se esperaba vacío", out, again)
			}
			got := a.Remains(c.in)
			if len(got) != c.findings {
				t.Fatalf("Remains(%q) = %+v; se esperaban %d hallazgos", c.in, got, c.findings)
			}
			for _, f := range got {
				if f.Class != casebank.ClassJID {
					t.Errorf("Remains(%q) delató un %q (%+v); se esperaba un JID", c.in, f.Class, f)
				}
			}
		})
	}
	want := []casebank.Finding{
		{Class: casebank.ClassJID, Text: jid, Start: 0, End: 27},
		{Class: casebank.ClassJID, Text: jid, Start: 27, End: 54},
	}
	if got := a.Remains(twoGluedJIDs); !reflect.DeepEqual(got, want) {
		t.Errorf("Remains(%q) = %+v; se esperaba %+v", twoGluedJIDs, got, want)
	}
}

// TestAnonymize_GluedJIDs_ChainStuckToAWord_Untouched es la otra mitad: el límite
// de palabra se le sigue exigiendo a la cadena por sus dos extremos. Pegada a una
// letra por fuera, ninguno de sus eslabones es JID.
func TestAnonymize_GluedJIDs_ChainStuckToAWord_Untouched(t *testing.T) {
	a := casebank.NewAnonymizer(corpusNames()...)
	for _, in := range []string{"a@lidb@lidz", "\u00f1a@lidb@lid"} {
		t.Run(in, func(t *testing.T) {
			if got := a.Anonymize(in); got != in {
				t.Errorf("Anonymize(%+q) = %+q; la cadena está pegada a una letra y no se toca", in, got)
			}
			if r := a.Remains(in); len(r) != 0 {
				t.Errorf("Remains(%+q) = %+v; se esperaba vacío", in, r)
			}
		})
	}
}

// TestRemains_AdversarialCorpus_SameAsOld fija los hallazgos ENTEROS —clase,
// texto e índices de byte— que devolvía el viejo: los índices con no-ASCII por
// delante, el JID que se queda con el teléfono que lleva dentro, el nombre largo
// antes que su prefijo y los agujeros en los que el barrido dice «limpio». Las
// filas marcadas «DIVERGE DEL VIEJO» son agujeros que el paquete nuevo cierra.
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
		// DIVERGE DEL VIEJO (hallazgo 1 de F7, decisión de Jhoan 2026-10-08): el viejo
		// devolvía «limpio» sobre dos teléfonos seguidos.
		{"04121234567 04149876543", []casebank.Finding{
			{Class: phone, Text: "04121234567", Start: 0, End: 11},
			{Class: phone, Text: "04149876543", Start: 12, End: 23},
		}},
		// DIVERGE DEL VIEJO (hallazgo 1 de F7, decisión de Jhoan 2026-10-08): el viejo
		// devolvía «limpio». El segundo número lleva espacios duros (U+00A0), que
		// siguen fuera de alcance: ése es el agujero que queda.
		{"０４１２１２３４５６７ y 0412 123 4567", []casebank.Finding{
			{Class: phone, Text: "０４１２１２３４５６７", Start: 0, End: 33},
		}},
		// Pegado a una letra no es JID ni teléfono: el barrido dice «limpio».
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
