//go:build integracion

package procesos

import (
	"testing"
)

// P3 · la tabla adversaria de los pasos 1–5 (reglas.md §2, hallazgo 40 de F1): lo que entra por la
// puerta del entrante —teléfono, LID, nombre de perfil, opción de menú— con separadores repetidos,
// dígitos no ASCII y espacios Unicode. Se afirma lo que hace el binario VIEJO, medido en T9.15; si
// el nuevo difiere, es un hallazgo y no se ajusta la tabla.

// p3ErrNoRefs es el error con el que el viejo pierde un entrante del que no saca ninguna ref de
// contacto (campo «error» de la línea p3MsgIncomingFailed).
const p3ErrNoRefs = "runtime: resolver contacto: contact: se requiere al menos una contact_ref"

// p3Adversary es un caso de identidad adversaria: el entrante que se manda con la palabra clave y
// lo que hace el viejo con él.
type p3Adversary struct {
	name string
	in   sealedIncoming // sin WaID ni Text: los pone la tabla
	// to es el destino al que el servidor contesta: el contacto que resultó. Vacío si el entrante
	// se pierde (ninguna ref válida → ERROR p3MsgIncomingFailed con p3ErrNoRefs).
	to string
	// clean es la identidad limpia a la que equivale: un segundo entrante con ella y la opción «2»
	// recibe el texto de Soporte, lo que solo pasa si cae en el MISMO contacto (el que tiene el
	// menú abierto). No se usa si to es vacío.
	clean sealedIncoming
	// sealedName dice si la fila del contacto debe quedar con el sobre del nombre poblado.
	sealedName bool
}

// p3AdversaryTable son los casos. Los comentarios dicen la regla del viejo que cada uno fija.
var p3AdversaryTable = []p3Adversary{
	{ // from_pn se queda con los dígitos ASCII: los dos «@» y el servidor del JID se tiran.
		name: "from_pn con separador repetido",
		in:   sealedIncoming{From: "573001110002@@s.whatsapp.net", FromPn: "573001110002@@s.whatsapp.net"},
		to:   "573001110002", clean: sealedIncoming{From: "573001110002@s.whatsapp.net", FromPn: "573001110002"},
	},
	{ // los dígitos no ASCII no se convierten: se tiran, y el resultado es OTRO número, sin aviso.
		name: "from_pn con dígitos no ASCII intercalados",
		in:   sealedIncoming{From: "57١٢٣3001110003@s.whatsapp.net", FromPn: "57١٢٣3001110003"},
		to:   "573001110003", clean: sealedIncoming{From: "573001110003@s.whatsapp.net", FromPn: "573001110003"},
	},
	{ // U+00A0 y U+2003 no son dígitos: se ignoran.
		name: "from_pn con espacios Unicode",
		in:   sealedIncoming{From: "573001110004@s.whatsapp.net", FromPn: "57 300 111 0004"},
		to:   "573001110004", clean: sealedIncoming{From: "573001110004@s.whatsapp.net", FromPn: "573001110004"},
	},
	{ // sin un solo dígito ASCII la ref se descarta, y entonces manda el from en claro.
		name: "from_pn solo con dígitos no ASCII y un from utilizable",
		in:   sealedIncoming{From: "573001110005@s.whatsapp.net", FromPn: "١٢٣"},
		to:   "573001110005", clean: sealedIncoming{From: "573001110005@s.whatsapp.net", FromPn: "573001110005"},
	},
	{ // ni from_pn ni from dejan una ref: el entrante se pierde.
		name: "from_pn solo con dígitos no ASCII y sin from utilizable",
		in:   sealedIncoming{From: "١٢٣@s.whatsapp.net", FromPn: "١٢٣"},
	},
	{ // from_lid corta en el primer «@»: «…@@lid» es el LID de antes del «@», y se contesta a «…@lid».
		name: "from_lid con separador repetido",
		in:   sealedIncoming{From: "987650006@@lid", FromLid: "987650006@@lid"},
		to:   "987650006@lid", clean: sealedIncoming{From: "987650006@lid", FromLid: "987650006@lid"},
	},
	{ // un LID cuya parte de usuario no son dígitos ASCII se descarta, y su from tampoco sirve.
		name: "from_lid con dígitos no ASCII",
		in:   sealedIncoming{From: "١٢٣@lid", FromLid: "١٢٣@lid"},
	},
	{ // el nombre de perfil no se recorta: con espacios Unicode alrededor se sella igual.
		name: "push_name con espacios Unicode alrededor",
		in:   sealedIncoming{From: "573001110008@s.whatsapp.net", FromPn: "573001110008", PushName: " Ana "},
		to:   "573001110008", clean: sealedIncoming{From: "573001110008@s.whatsapp.net", FromPn: "573001110008"}, sealedName: true,
	},
	{ // ni siquiera uno hecho SOLO de espacios Unicode cuenta como vacío: también se sella.
		name: "push_name solo de espacios Unicode",
		in:   sealedIncoming{From: "573001110009@s.whatsapp.net", FromPn: "573001110009", PushName: "  "},
		to:   "573001110009", clean: sealedIncoming{From: "573001110009@s.whatsapp.net", FromPn: "573001110009"}, sealedName: true,
	},
}

// p3MenuOptions son las opciones de menú adversarias: el texto que se manda con el menú abierto y
// la respuesta del viejo. La opción se recorta (TrimSpace) y se busca como clave exacta.
var p3MenuOptions = []struct {
	name, pn, option, reply string
}{
	{"opción con espacios alrededor", "573001110011", " 1 ", p3SalesText},
	{"opción con espacios Unicode alrededor", "573001110012", " 2 ", p3SupportText},
	{"opción con un dígito no ASCII", "573001110013", "١", p3InvalidOption},
	{"opción con separador repetido", "573001110014", "1@@1", p3InvalidOption},
}

// p3Adversaries recorre las dos tablas con la sesión activa y devuelve cuántos entrantes se
// perdieron, que es el número de líneas ERROR p3MsgIncomingFailed que el test padre debe esperar.
// Cada caso usa un contacto propio y a lo sumo tres respuestas del flujo: el tope de auto-respuestas
// por conversación (ráfaga de 3, una cada 2 s) cortaría la cuarta.
func p3Adversaries(t *testing.T, sc p3Scene) (lost int) {
	t.Helper()
	for i, tc := range p3AdversaryTable {
		if tc.to == "" {
			lost++
		}
		t.Run(tc.name, func(t *testing.T) { p3RunAdversary(t, sc, i, tc) })
	}
	for i, tc := range p3MenuOptions {
		t.Run(tc.name, func(t *testing.T) {
			from := tc.pn + "@s.whatsapp.net"
			sc.Edge.sendSealedIncoming(t, sealedIncoming{From: from, WaID: p3WaID("P3-OPT", i, "A"), Text: p3Keyword, FromPn: tc.pn})
			sc.expectText(t, tc.pn, p3Welcome)
			sc.expectText(t, tc.pn, p3MenuPrompt)
			sc.Edge.sendSealedIncoming(t, sealedIncoming{From: from, WaID: p3WaID("P3-OPT", i, "B"), Text: tc.option, FromPn: tc.pn})
			sc.expectText(t, tc.pn, tc.reply)
		})
	}
	sc.expectNoPendingText(t, "al terminar la tabla adversaria")
	return lost
}

// p3RunAdversary ejecuta un caso de p3AdversaryTable. Si el entrante se pierde: su línea ERROR con
// p3ErrNoRefs, ningún texto y ni una fila más en contacts. Si llega: la bienvenida y el menú van al
// destino esperado, la identidad limpia cae en el mismo contacto (su «2» recibe Soporte), contacts
// crece en una sola fila y el sobre del nombre queda como dice el caso.
func p3RunAdversary(t *testing.T, sc p3Scene, i int, tc p3Adversary) {
	const countContacts = `SELECT count(*) FROM public.contacts WHERE tenant_id = $1::uuid`
	before := consultaEntero(t, sc.DB, countContacts, sc.Tenant)
	known := p3ContactIDs(t, sc)
	in := tc.in
	in.WaID, in.Text = p3WaID("P3-ADV", i, "A"), p3Keyword
	sc.Edge.sendSealedIncoming(t, in)

	if tc.to == "" {
		line := edgeEsperarLinea(t, sc.S, p3MsgIncomingFailed, "wa_message_id", in.WaID)
		if line["level"] != "ERROR" || line["error"] != p3ErrNoRefs {
			t.Errorf("la línea del entrante perdido es %v; quería ERROR con %q", line, p3ErrNoRefs)
		}
		sc.expectNoPendingText(t, "entrante sin ninguna ref de contacto")
		if n := consultaEntero(t, sc.DB, countContacts, sc.Tenant); n != before {
			t.Errorf("contacts pasó de %d a %d filas con un entrante que se pierde", before, n)
		}
		return
	}

	sc.expectText(t, tc.to, p3Welcome)
	sc.expectText(t, tc.to, p3MenuPrompt)
	clean := tc.clean
	clean.WaID, clean.Text = p3WaID("P3-ADV", i, "B"), "2"
	sc.Edge.sendSealedIncoming(t, clean)
	sc.expectText(t, tc.to, p3SupportText)

	if n := consultaEntero(t, sc.DB, countContacts, sc.Tenant); n != before+1 {
		t.Errorf("contacts pasó de %d a %d filas; quería una más (la identidad adversaria y la limpia son un contacto)", before, n)
	}
	rows := p3ContactRows(t, sc, p3NewContactID(t, sc, known))
	if len(rows) != 1 {
		t.Fatalf("el contacto nuevo tiene %d filas, quería 1", len(rows))
	}
	if got := rows[0].nameSealed(t); got != tc.sealedName {
		t.Errorf("sobre del nombre poblado = %v, quería %v", got, tc.sealedName)
	}
	if len(p3IncomingErrors(sc.S, in.WaID)) != 0 || len(p3IncomingErrors(sc.S, clean.WaID)) != 0 {
		t.Errorf("el caso dejó líneas ERROR %q en el log", p3MsgIncomingFailed)
	}
}
