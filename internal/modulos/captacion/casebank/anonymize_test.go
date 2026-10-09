package casebank_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/casebank"
)

// anonymize_test.go — las promesas del barrido de PII de T5.3. Reglas leídas de
// internal/casebank/anonimizar_test.go (13 tests), no portadas. El corpus
// adversario y la equivalencia con el viejo van en anonymize_corpus_test.go.
//
// 🔴 EL PAR QUE HACE QUE ESTO NO SEA DECORADO: los tests de abajo van SIEMPRE en
// dos mitades — «esto se tapa» y «esto NO se toca»—. Un anonimizador que
// devolviera "[TELEFONO]" para cualquier entrada pasaría la primera mitad entera
// y es exactamente lo que rompería el banco de casos: sin cantidades no hay nada
// que evaluar en P4.

// testNames es la lista con la que se arman los anonimizadores de este fichero.
// Incluye un nombre acentuado a propósito: el límite de palabra de Go (`\b`) es
// ASCII y se equivocaría en el borde de «Fusión».
func testNames() []string { return []string{"Ambar", "Herminia", "Fusión", "Ana", "Ana María"} }

func newTestAnonymizer() casebank.Anonymizer {
	return casebank.NewAnonymizer(testNames()...)
}

// countDigits cuenta los dígitos ASCII que quedan en un texto ya anonimizado. Se
// escribe aquí y no se reusa nada del paquete: un test que llamara a la MISMA
// función que el código bajo prueba usa para decidir no estaría comprobando nada.
func countDigits(s string) int {
	n := 0
	for _, r := range s {
		if r >= '0' && r <= '9' {
			n++
		}
	}
	return n
}

// TestMarksAndClasses_LiteralValues: las marcas acaban en la tabla y las clases en
// los informes de curación; sus valores son los del paquete viejo.
func TestMarksAndClasses_LiteralValues(t *testing.T) {
	marks := map[string]string{
		casebank.MarkJID:   "[JID]",
		casebank.MarkPhone: "[TELEFONO]",
		casebank.MarkName:  "[NOMBRE]",
	}
	for got, want := range marks {
		if got != want {
			t.Errorf("marca = %q, se esperaba %q", got, want)
		}
	}
	classes := map[casebank.Class]string{
		casebank.ClassJID:   "jid",
		casebank.ClassPhone: "telefono",
		casebank.ClassName:  "nombre",
	}
	for got, want := range classes {
		if string(got) != want {
			t.Errorf("clase = %q, se esperaba %q", got, want)
		}
	}
}

// TestAnonymize_JID cubre los cinco dominios de WhatsApp que el patrón conoce.
func TestAnonymize_JID(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"individual", "escribe a 584121234567@s.whatsapp.net ya", "escribe a [JID] ya"},
		{"group", "el grupo 120363012345678901@g.us", "el grupo [JID]"},
		{"c.us", "puente: 5491133334444@c.us", "puente: [JID]"},
		{"lid", "oculto: 98765432101234@lid", "oculto: [JID]"},
		{"broadcast", "lista status@broadcast", "lista [JID]"},
		{"at the end without space", "manda a 584121234567@s.whatsapp.net", "manda a [JID]"},
		{"upper case domain", "manda a 584121234567@S.WhatsApp.NET", "manda a [JID]"},
		{"with device", "desde 584121234567:12@s.whatsapp.net", "desde [JID]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := newTestAnonymizer().Anonymize(c.in); got != c.want {
				t.Errorf("Anonymize(%q) = %q; se esperaba %q", c.in, got, c.want)
			}
		})
	}
}

// TestAnonymize_EmailIsNotAJID_Untouched fija el agujero DECLARADO: el patrón
// exige uno de los dominios de WhatsApp, así que un correo pasa entero. Está
// escrito como test para que el día que alguien «mejore» el patrón a un `@`
// genérico se entere de que está cambiando el alcance, no arreglando un fallo.
func TestAnonymize_EmailIsNotAJID_Untouched(t *testing.T) {
	const in = "mi correo es ambar.perez@gmail.com"
	// El NOMBRE sí cae (está en la lista); el correo, no.
	const want = "mi correo es [NOMBRE].perez@gmail.com"
	got := newTestAnonymizer().Anonymize(in)
	if got != want {
		t.Errorf("Anonymize(%q) = %q; se esperaba %q: este anonimizador NO cubre correos", in, got, want)
	}
	if strings.Contains(got, casebank.MarkJID) {
		t.Errorf("Anonymize(%q) = %q; un correo NO es un JID y no debe marcarse como tal", in, got)
	}
}

// TestAnonymize_Phones es la mitad «esto se tapa»: los formatos con y sin `+`,
// con espacios, guiones, puntos y paréntesis.
func TestAnonymize_Phones(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plus and spaces", "llámame al +58 412 123 4567 porfa", "llámame al [TELEFONO] porfa"},
		{"dashes", "mi número es 0412-123-4567", "mi número es [TELEFONO]"},
		{"plain", "anota 04121234567", "anota [TELEFONO]"},
		// El paréntesis de apertura queda FUERA: la racha empieza en dígito.
		{"parentheses", "fijo (0212) 555 6677", "fijo ([TELEFONO]"},
		{"dots", "es el 412.123.4567", "es el [TELEFONO]"},
		{"international", "+34911223344 es el mío", "[TELEFONO] es el mío"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := newTestAnonymizer().Anonymize(c.in)
			if got != c.want {
				t.Errorf("Anonymize(%q) = %q; se esperaba %q", c.in, got, c.want)
			}
			if left := countDigits(got); left != 0 {
				t.Errorf("Anonymize(%q) = %q; quedaron %d dígitos del número", c.in, got, left)
			}
		})
	}
}

// TestAnonymize_PhoneDigitBounds: lo que decide es el CONTEO de dígitos, de 8 a
// 15, y los separadores no cuentan.
func TestAnonymize_PhoneDigitBounds(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"7 digits survive", "1234567", "1234567"},
		{"8 digits fall", "12345678", "[TELEFONO]"},
		{"15 digits fall", "123456789012345", "[TELEFONO]"},
		{"16 digits survive", "1234567890123456", "1234567890123456"},
		{"7 digits spread survive", "1 2 3 4 5 6 7", "1 2 3 4 5 6 7"},
		{"8 digits spread fall", "1 2 3 4 5 6 7 8", "[TELEFONO]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := newTestAnonymizer().Anonymize(c.in); got != c.want {
				t.Errorf("Anonymize(%q) = %q; se esperaba %q", c.in, got, c.want)
			}
		})
	}
}

// TestAnonymize_OrderQuantitiesSurvive es LA OTRA MITAD, y es la que de verdad
// decide si este banco de casos sirve para algo: si el anonimizador se come «10 o
// 12 porciones» o «paquete de 30», el dataset ya no puede evaluar a P4, que es la
// etapa que vive de esos números.
func TestAnonymize_OrderQuantitiesSurvive(t *testing.T) {
	untouched := []string{
		"de 10 o 12 porciones",
		"de 25 o 30 porciones",
		"un paquete de tequeños congelados de 30",
		"para el 22/07",
		"Serían 2 tortas",
		"5 kg de harina",
		"pedido 887",
	}
	a := newTestAnonymizer()
	for _, text := range untouched {
		t.Run(text, func(t *testing.T) {
			if got := a.Anonymize(text); got != text {
				t.Errorf("Anonymize(%q) = %q; una cantidad del pedido NO es un teléfono", text, got)
			}
			if r := a.Remains(text); len(r) != 0 {
				t.Errorf("Remains(%q) = %v; el barrido no puede delatar una cantidad del pedido", text, r)
			}
		})
	}
}

// TestAnonymize_SeparatorsThatOnceEscaped es el test de REGRESIÓN del defecto
// medido el 2026-08-27: con la barra fuera de la clase de separadores,
// `0412/1234567` salía INTACTO de `Anonymize` y —lo grave— `Remains` devolvía `[]`
// sobre él. Un barrido que dice «limpio» sobre un teléfono completo es peor que no
// tener barrido.
//
// 🔴 LAS DOS MITADES SON OBLIGATORIAS y por eso están en el mismo test: el
// defecto se manifestaba en las dos y una sola de ellas no lo habría cazado
// entero. Quien añada un separador nuevo a la clase añade su caso AQUÍ.
func TestAnonymize_SeparatorsThatOnceEscaped(t *testing.T) {
	cases := []struct {
		name, in string
	}{
		{"slash", "llamame al 0412/1234567"},
		{"slash with international prefix", "+58/412/123/4567"},
		{"underscore", "anota 0412_123_4567"},
		{"line feed", "mi numero:\n0412\n1234567"},
		{"carriage return", "mi numero:\r\n0412\r\n1234567"},
		{"tab", "anota 0412\t123\t4567"},
		{"mixed separators", "el (0212)/555_66-77"},
		{"repeated separators", "el +58--412  //  123..4567"},
	}
	a := newTestAnonymizer()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// (a) LA REDACCIÓN.
			got := a.Anonymize(c.in)
			if !strings.Contains(got, casebank.MarkPhone) {
				t.Errorf("Anonymize(%q) = %q; el teléfono NO se tapó", c.in, got)
			}
			if left := countDigits(got); left != 0 {
				t.Errorf("Anonymize(%q) = %q; quedaron %d dígitos del número", c.in, got, left)
			}
			// (b) EL BARRIDO, que es la mitad que más importa: éste corre sobre
			// texto que nadie redactó, y decir «cero hallazgos» aquí sería afirmar
			// que un teléfono completo no es PII.
			remains := a.Remains(c.in)
			if len(remains) != 1 {
				t.Fatalf("Remains(%q) = %v; se esperaba UN hallazgo, el teléfono entero", c.in, remains)
			}
			if remains[0].Class != casebank.ClassPhone {
				t.Errorf("Remains(%q) delató un %q; se esperaba %q", c.in, remains[0].Class, casebank.ClassPhone)
			}
		})
	}
}

// TestAnonymize_KnownFalsePositive_LongDateIsRedacted deja escrito en forma de
// test el falso positivo que la cabecera del anonimizador declara: una fecha con
// separadores admitidos suma 8 dígitos y se redacta. Se acepta y se fija: en un
// barrido de PII, tapar una fecha es barato y dejar un número no lo es.
func TestAnonymize_KnownFalsePositive_LongDateIsRedacted(t *testing.T) {
	cases := []struct{ in, want string }{
		{"el 2026 08 27 a las 10", "el [TELEFONO] a las 10"},
		{"para el 22/07/2026", "para el [TELEFONO]"},
		{"del 01-02-2026", "del [TELEFONO]"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := newTestAnonymizer().Anonymize(c.in); got != c.want {
				t.Errorf("Anonymize(%q) = %q; se esperaba el falso positivo DECLARADO: %q", c.in, got, c.want)
			}
		})
	}
}

// TestAnonymize_ShortOrderDateSurvives acota el falso positivo de arriba: lo que
// se pierde son las fechas de OCHO dígitos, no toda fecha.
func TestAnonymize_ShortOrderDateSurvives(t *testing.T) {
	a := newTestAnonymizer()
	for _, in := range []string{"para el 22/07", "el 3/8", "entre el 10 y el 12"} {
		t.Run(in, func(t *testing.T) {
			if got := a.Anonymize(in); got != in {
				t.Errorf("Anonymize(%q) = %q; una fecha corta del pedido NO es un teléfono", in, got)
			}
		})
	}
}

// TestAnonymize_Names cubre lo que la lista sí conoce, incluida la adyacencia —dos
// nombres pegados— que un patrón que CONSUMA el separador dejaría a medias.
func TestAnonymize_Names(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain", "habló con Ambar", "habló con [NOMBRE]"},
		{"lower case", "habló con ambar", "habló con [NOMBRE]"},
		{"upper case", "habló con AMBAR", "habló con [NOMBRE]"},
		{"accented", "el negocio Fusión cerró", "el negocio [NOMBRE] cerró"},
		{"accented upper case", "el negocio FUSIÓN cerró", "el negocio [NOMBRE] cerró"},
		{"two names in a row", "Ambar Herminia hablaron", "[NOMBRE] [NOMBRE] hablaron"},
		{"the longest wins", "pregunta por Ana María", "pregunta por [NOMBRE]"},
		{"at the start", "Ambar pidió tortas", "[NOMBRE] pidió tortas"},
		{"next to punctuation", "¿(Ambar)?", "¿([NOMBRE])?"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := newTestAnonymizer().Anonymize(c.in); got != c.want {
				t.Errorf("Anonymize(%q) = %q; se esperaba %q", c.in, got, c.want)
			}
		})
	}
}

// TestAnonymize_NameInsideAnotherWord_Untouched es la otra mitad: sin límite de
// palabra, «ambarina» y «Anacleto» se convertirían en «[NOMBRE]ina» y
// «[NOMBRE]cleto», que además de absurdo destruiría el texto del pedido. El límite
// es Unicode: una letra acentuada o un dígito pegados también lo son.
func TestAnonymize_NameInsideAnotherWord_Untouched(t *testing.T) {
	untouched := []string{
		"una torta ambarina", "Anacleto trajo el pedido", "herminiana",
		"Fusiónes", "laFusión", "Anaí", "ñAmbar", "Ambar2", "2Ambar",
	}
	for _, text := range untouched {
		t.Run(text, func(t *testing.T) {
			if got := newTestAnonymizer().Anonymize(text); got != text {
				t.Errorf("Anonymize(%q) = %q; el nombre está DENTRO de otra palabra", text, got)
			}
		})
	}
}

// TestAnonymize_WordBoundaryAppliesToPhonesAndJIDs: pegados a una letra o a un
// dígito, ni el teléfono ni el JID se tocan.
func TestAnonymize_WordBoundaryAppliesToPhonesAndJIDs(t *testing.T) {
	untouched := []string{"tel04121234567", "04121234567bs", "ñ04121234567", "user@lidia", "ñ584121234567@lid"}
	a := newTestAnonymizer()
	for _, text := range untouched {
		t.Run(text, func(t *testing.T) {
			if got := a.Anonymize(text); got != text {
				t.Errorf("Anonymize(%q) = %q; pegado a una letra no se toca", text, got)
			}
		})
	}
}

// TestAnonymize_NoNameList_NoNameIsRedacted fija la limitación grande y
// declarada: no hay reconocimiento de entidades. Un nombre que nadie declaró pasa
// entero, y las otras dos mitades siguen funcionando. Vale igual para el valor
// cero del tipo y para una lista de entradas vacías.
func TestAnonymize_NoNameList_NoNameIsRedacted(t *testing.T) {
	const in = "Ambar escribió desde 584121234567@s.whatsapp.net al 04121234567"
	const want = "Ambar escribió desde [JID] al [TELEFONO]"
	empties := map[string]casebank.Anonymizer{
		"no names":     casebank.NewAnonymizer(),
		"blank names":  casebank.NewAnonymizer("", "   ", "\t"),
		"zero value":   {},
		"nil variadic": casebank.NewAnonymizer([]string(nil)...),
	}
	for name, a := range empties {
		t.Run(name, func(t *testing.T) {
			if got := a.Anonymize(in); got != want {
				t.Errorf("Anonymize(%q) = %q; se esperaba %q (SIN lista no hay NER)", in, got, want)
			}
			if names := a.Names(); names != nil {
				t.Errorf("Names() = %v; se esperaba nil", names)
			}
			for _, f := range a.Remains(in) {
				if f.Class == casebank.ClassName {
					t.Errorf("Remains delató un nombre (%+v) sin lista de nombres", f)
				}
			}
			if n := len(a.Remains(in)); n != 2 {
				t.Errorf("Remains(%q) devolvió %d hallazgos, se esperaban 2 (JID y teléfono)", in, n)
			}
		})
	}
}

// TestNewAnonymizer_NamesAreLiteralText: los signos de un nombre no son
// metacaracteres, y los espacios de sus extremos no cuentan.
func TestNewAnonymizer_NamesAreLiteralText(t *testing.T) {
	a := casebank.NewAnonymizer("  A.B ", "C*", "(D)")
	cases := []struct{ in, want string }{
		{"vino A.B ayer", "vino [NOMBRE] ayer"},
		{"vino AxB ayer", "vino AxB ayer"},
		{"vino C* ayer", "vino [NOMBRE] ayer"},
		{"vino CCC ayer", "vino CCC ayer"},
		{"vino (D) ayer", "vino [NOMBRE] ayer"},
	}
	for _, c := range cases {
		if got := a.Anonymize(c.in); got != c.want {
			t.Errorf("Anonymize(%q) = %q; se esperaba %q", c.in, got, c.want)
		}
	}
}

// TestAnonymizer_Names_LongestFirstAndACopy: el orden es el de la alternancia (de
// más largo a más corto, estable) y lo devuelto es una copia.
func TestAnonymizer_Names_LongestFirstAndACopy(t *testing.T) {
	a := casebank.NewAnonymizer("Ana", "Ana María", "Bob", " Zed ")
	want := []string{"Ana María", "Ana", "Bob", "Zed"}
	got := a.Names()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Names() = %q; se esperaba %q", got, want)
	}
	got[0] = "pisado"
	if again := a.Names(); !reflect.DeepEqual(again, want) {
		t.Errorf("tocar lo devuelto por Names() cambió al anonimizador: %q", again)
	}
	// Y el orden de ENTRADA no importa: el largo gana aunque llegue el último.
	if out := a.Anonymize("pregunta por Ana María"); out != "pregunta por [NOMBRE]" {
		t.Errorf("Anonymize = %q; quedó un trozo del nombre largo suelto", out)
	}
}

// TestAnonymize_JIDBeforeThePhoneItCarries es una aserción sobre el ORDEN, que es
// parte de la corrección y no una preferencia: un JID lleva el número dentro, así
// que la pasada de teléfonos, si corriera primero, lo partiría en
// `[TELEFONO]@s.whatsapp.net` — perdiendo la marca buena y DEJANDO EL DOMINIO.
func TestAnonymize_JIDBeforeThePhoneItCarries(t *testing.T) {
	const in = "desde 584121234567@s.whatsapp.net"
	const want = "desde [JID]"
	if got := newTestAnonymizer().Anonymize(in); got != want {
		t.Fatalf("Anonymize(%q) = %q; se esperaba %q", in, got, want)
	}
}

// TestAnonymize_NothingToRedact_SameText: lo que no hay que tapar sale byte a
// byte, el vacío incluido.
func TestAnonymize_NothingToRedact_SameText(t *testing.T) {
	a := newTestAnonymizer()
	for _, in := range []string{"", "   ", "\n", "quiero una torta de 10 o 12 porciones para el miércoles"} {
		if got := a.Anonymize(in); got != in {
			t.Errorf("Anonymize(%q) = %q; no había nada que tapar", in, got)
		}
	}
}

// TestRemains_FindsTheThreeClasses es EL CONTROL NEGATIVO sin el cual «la semilla
// pasa el barrido» no probaría nada: un barrido que no mirase devolvería cero
// hallazgos siempre y satisfaría igual aquel test.
func TestRemains_FindsTheThreeClasses(t *testing.T) {
	const dirty = "Ambar escribió al +58 412 123 4567 desde 584121234567@s.whatsapp.net"
	want := []casebank.Finding{
		{Class: casebank.ClassName, Text: "Ambar", Start: 0, End: 5},
		{Class: casebank.ClassPhone, Text: "+58 412 123 4567", Start: 19, End: 35},
		// UN hallazgo, no dos: el JID se queda con el teléfono que lleva dentro.
		{Class: casebank.ClassJID, Text: "584121234567@s.whatsapp.net", Start: 42, End: 69},
	}
	got := newTestAnonymizer().Remains(dirty)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Remains(%q) =\n   %+v\nse esperaba, en ORDEN DE APARICIÓN,\n   %+v", dirty, got, want)
	}
}

// TestRemains_CleanText_EmptyNotNil es la mitad complementaria.
func TestRemains_CleanText_EmptyNotNil(t *testing.T) {
	const clean = "quiero una torta de 10 o 12 porciones para el miércoles"
	r := newTestAnonymizer().Remains(clean)
	if len(r) != 0 {
		t.Errorf("Remains(%q) = %v; se esperaba vacío", clean, r)
	}
	if r == nil {
		t.Error("Remains devolvió nil; promete un slice vacío")
	}
}
