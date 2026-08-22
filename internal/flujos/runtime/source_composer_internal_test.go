// source_composer_internal_test.go — Plan 044 · Ola 1 · T1.4. EL GUARDIÁN DEL
// «MECANISMO ÚNICO», ESCRITO DONDE SE PUEDE LEER LA TABLA.
//
// ⏳ NO SE HA EJECUTADO: escrito en un entorno sin Go. Cada test lleva su MUTACIÓN,
// y la mutación está elegida para que COMPILE.
//
// # POR QUÉ ESTE FICHERO EXISTE (revisión 2026-08-22)
//
// El único guardián de que las dos clases de contexto —`summary` y
// `message_out_of_turn`— pasen por UN SOLO camino era un `grep` escrito DENTRO de un
// comentario (source_composer.go, en la cabecera). Un `grep` que no ejecuta nadie no
// es un guardián: es una intención. Si mañana alguien parte `ComposeSourceText` en
// dos ramas, o añade una tercera clase de contexto con su propio `if`, los tests de
// source_composer_test.go siguen todos verdes.
//
// Esto es la parte que solo se puede escribir desde DENTRO del paquete, porque
// `contextKinds`, `contextLabel` y `speakerOf` no se exportan (y no deben
// exportarse: son detalle del compositor, no contrato con nadie).
//
// # QUÉ COMPRA Y QUÉ NO. La frase honesta
//
// COMPRA que `contextKinds` sea LA FUENTE: que las clases que son contexto sean
// exactamente las dos declaradas, con exactamente esos rótulos, y que el reparto de
// TODO el vocabulario de `entry_kind` sea el que T1.4 fija. Y compra, sobre todo,
// que una clase NUEVA no pueda entrar por la puerta de atrás: el test que recorre la
// tabla somete a CUALQUIER fila —incluida una que no existe todavía— a los mismos
// asertos que hoy pasan las dos conocidas. Añadir una fila es heredar la suite;
// escribir una rama, no.
//
// NO COMPRA que el código tenga una sola rama. Si alguien duplica el camino y las
// dos copias se comportan idénticamente, todo esto sigue verde — eso solo lo vería
// una inspección de la FORMA del código (un AST, o el `grep` que nadie corre), no
// una ejecución. Se acepta a conciencia: una duplicación que no diverge no ha roto
// nada todavía, y el día que diverja la caza
// TestT14_LasDosClasesDeContexto_MISMAEntradaMISMOSALIDA (source_composer_test.go).
package runtime

import (
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
)

// TestContextKinds_EsLaTablaCOMPLETA_YNADAMAS fija el censo: qué clases son
// contexto, con qué rótulo, y —lo que de verdad muerde— que no haya ninguna más ni
// ninguna menos.
//
// Los rótulos se escriben AQUÍ a mano en vez de leerse del mapa, por el mismo motivo
// que en el fichero externo: comparar la tabla consigo misma no prueba nada. Estos
// dos literales son el contrato con el prompt de P2 (D-044.24), y cambiarlos tiene
// que costar tocar dos sitios.
//
// MUTACIÓN (compila): en source_composer.go, añadir una tercera fila a la tabla,
//
//	events.KindDecision: "decisión del cliente",
//
// Es la avería exacta que T1.4 prohíbe por escrito —«una `decision` es estructura,
// no prosa, y no entra al source_text»— y hasta hoy no la veía nadie: la `decision`
// llega con Text vacío en el caso normal, así que los tests de arriba no la notan.
// El día que una `decision` traiga texto (un render nuevo, un payload distinto), se
// colaría al prompt como contexto sin que ningún test se moviera.
//
// MUTACIÓN 2 (compila): cambiar el rótulo del saliente por el del resumen,
//
//	events.KindMessageOutOfTurn: "resumen del sistema",
//
// Los dos rótulos pasan a ser el mismo y el LLM deja de poder distinguir lo que
// resumió el sistema de lo que dijo el negocio por su cuenta.
func TestContextKinds_EsLaTablaCOMPLETA_YNADAMAS(t *testing.T) {
	quiero := map[events.EntryKind]string{
		events.KindSummary:          "resumen del sistema",
		events.KindMessageOutOfTurn: "mensaje del negocio fuera de turno",
	}

	if len(contextKinds) != len(quiero) {
		t.Fatalf("contextKinds tiene %d filas y las clases de contexto son %d: %v",
			len(contextKinds), len(quiero), contextKinds)
	}
	for kind, rotulo := range quiero {
		got, ok := contextKinds[kind]
		if !ok {
			t.Fatalf("la clase de contexto %q no está en la tabla; si alguien la trata en una rama aparte, "+
				"ha abierto el segundo camino que T1.4 prohíbe", kind)
		}
		if got != rotulo {
			t.Fatalf("el rótulo de %q es %q y el contrato con el prompt de P2 dice %q", kind, got, rotulo)
		}
	}
	// Y NINGUNA DE MÁS, dicho desde el otro lado. Con el `len` y el bucle de arriba
	// ya no cabe una clase intrusa, pero se recorre igual la tabla real: el mensaje
	// de error nombra a la intrusa, que es lo que hace falta leer cuando esto se
	// ponga rojo dentro de seis meses. Un fallo que solo dice «2 != 3» obliga a
	// reconstruir el porqué.
	for kind := range contextKinds {
		if _, esperada := quiero[kind]; !esperada {
			t.Fatalf("la tabla trae una clase de contexto que nadie declaró: %q", kind)
		}
	}
	// El rótulo VACÍO no es un rótulo. Se dice aparte porque es la mutación más
	// tentadora («total, la clave ya dice qué es») y la que deja al automensaje de
	// rescate con un par de corchetes vacíos delante de una lista de productos.
	for kind, rotulo := range contextKinds {
		if strings.TrimSpace(rotulo) == "" {
			t.Fatalf("la clase %q entra al prompt SIN rótulo; D-044.24 dice que el rótulo no es decorativo", kind)
		}
	}
}

// TestClasificacionDelVocabulario_TresDestinosYNiUnoMas recorre el vocabulario
// ENTERO de `entry_kind` —los cuatro grados que la 0072 admite, más uno inventado—
// y fija a cuál de los TRES destinos va cada uno. Es el reparto de T1.4 escrito como
// tabla, que es como está escrito en el docstring del compositor.
//
// MUTACIÓN (compila): en source_composer.go, en ComposeSourceText, sustituir el
// `default:` con su `continue` por
//
//	default:
//		if e.Text == "" {
//			continue
//		}
//		literal = append(literal, speakerOf(e.Role)+": "+e.Text)
//		out.Messages++
//
// Es el fail-closed convertido en fail-OPEN: lo desconocido entra al hilo literal
// como si lo hubiera escrito el cliente. Las dos últimas filas de la tabla se ponen
// rojas.
// Los tres destinos posibles de una entrada del hilo. Viven fuera de la función de
// test porque los helpers extraídos (gocyclo) también los nombran.
const (
	aContexto = "contexto"
	aLiteral  = "literal"
	aNada     = "nada"
)

func TestClasificacionDelVocabulario_TresDestinosYNiUnoMas(t *testing.T) {
	casos := []struct {
		nombre  string
		kind    events.EntryKind
		destino string
	}{
		{"el turno del cliente y la respuesta del negocio", events.KindMessage, aLiteral},
		{"el resumen del sistema (ADR-0029 E-4)", events.KindSummary, aContexto},
		{"el saliente fuera de turno (D-044.24)", events.KindMessageOutOfTurn, aContexto},
		{"la decisión estructurada (no es prosa)", events.KindDecision, aNada},
		{"un grado que nadie ha inventado todavía", events.EntryKind("grado_del_futuro"), aNada},
	}

	for _, c := range casos {
		verificaElDestinoDelGrado(t, c.nombre, c.kind, c.destino)
	}
}

// verificaElDestinoDelGrado somete UN grado al criterio literal de la tarea («nunca se
// lee el cuerpo sin mirar antes entry_kind»): el texto es el MISMO para los cinco y lo
// único que decide es el grado. Va extraída del bucle por gocyclo.
func verificaElDestinoDelGrado(t *testing.T, nombre string, kind events.EntryKind, destino string) {
	t.Helper()
	const cuerpo = "2 tortas de chocolate"
	got := ComposeSourceText([]events.ThreadEntry{
		{Seq: 1, Role: events.RoleClient, Kind: kind, Text: cuerpo},
	})

	enContexto := strings.Contains(got.Context, cuerpo)
	enLiteral := strings.Contains(got.Literal, cuerpo)

	switch destino {
	case aContexto:
		if !enContexto || enLiteral {
			t.Fatalf("%s (%q): tenía que ir SOLO al contexto; contexto=%v literal=%v", nombre, kind, enContexto, enLiteral)
		}
		verificaLosContadores(t, nombre, kind, got, 1, 0)
	case aLiteral:
		if enContexto || !enLiteral {
			t.Fatalf("%s (%q): tenía que ir SOLO al hilo literal; contexto=%v literal=%v", nombre, kind, enContexto, enLiteral)
		}
		verificaLosContadores(t, nombre, kind, got, 0, 1)
	case aNada:
		if enContexto || enLiteral || got.Text != "" {
			t.Fatalf("%s (%q): NO puede aparecer en el source_text y apareció:\n%s", nombre, kind, got.Text)
		}
		verificaLosContadores(t, nombre, kind, got, 0, 0)
	}

	verificaLaCoherenciaDeContextLabel(t, nombre, kind, destino, got)
}

// verificaLosContadores exige el reparto de volumen: el contexto NO es actividad del
// cliente (REQ-10b (c)).
func verificaLosContadores(t *testing.T, nombre string, kind events.EntryKind, got Composed, wantCtx, wantMsg int) {
	t.Helper()
	if got.ContextEntries != wantCtx || got.Messages != wantMsg {
		t.Fatalf("%s (%q): contadores = ctx:%d msg:%d; ESPERADO ctx:%d msg:%d — el contexto NO es "+
			"actividad del cliente (REQ-10b (c))", nombre, kind, got.ContextEntries, got.Messages, wantCtx, wantMsg)
	}
}

// verificaLaCoherenciaDeContextLabel exige que la respuesta de `contextLabel` case con
// el destino: es LA función por la que pasan las dos clases, así que si dijera «sí» a
// algo que no acaba en el contexto —o al revés— habría dos criterios y no uno.
func verificaLaCoherenciaDeContextLabel(t *testing.T, nombre string, kind events.EntryKind, destino string, got Composed) {
	t.Helper()
	label, esContexto := contextLabel(kind)
	if esContexto != (destino == aContexto) {
		t.Fatalf("%s (%q): contextLabel dice esContexto=%v y el destino es %q", nombre, kind, esContexto, destino)
	}
	if esContexto && !strings.Contains(got.Context, "["+label+"] ") {
		t.Fatalf("%s (%q): el rótulo que devuelve contextLabel (%q) no es el que sale en el bloque:\n%s",
			nombre, kind, label, got.Context)
	}
}

// TestTodaClaseDeContexto_HEREDA_LosMismosHechos es la pieza que hace que la tabla
// sea de verdad el mecanismo: recorre `contextKinds` —no una lista escrita a mano— y
// somete a CADA fila, la conozca este test o no, a los hechos que T1.4 promete.
//
// 🔑 ESTO ES LO QUE COMPRA EL «AÑADIR UNA FILA, NO ESCRIBIR UNA RAMA». El día que
// exista una tercera clase de contexto, quien la añada a la tabla la mete a la vez en
// esta suite sin tocar un test; quien la trate en una rama aparte se queda fuera de
// ella, y la tabla-censo de arriba se pone roja por la clase que falta. Las dos
// mitades juntas son el guardián que el `grep` del comentario quería ser.
//
// MUTACIÓN (compila): en source_composer.go, en el brazo `case esContexto:`,
// sustituir
//
//	contexto = append(contexto, "["+label+"] "+e.Text)
//
// por
//
//	if e.Kind == events.KindSummary {
//		contexto = append(contexto, "["+label+"] "+e.Text)
//	} else {
//		literal = append(literal, e.Text)
//	}
//
// Es EL segundo camino: una de las dos clases sigue siendo contexto y la otra se
// cuela en el hilo literal sin rótulo — el automensaje de rescate convertido en
// pedido del cliente, que es el accidente concreto que D-044.24 nombra.
func TestTodaClaseDeContexto_HEREDA_LosMismosHechos(t *testing.T) {
	if len(contextKinds) == 0 {
		t.Fatal("no hay ni una clase de contexto: sin tabla no hay mecanismo que guardar")
	}

	const cuerpo = "Tenías: 2 tortas de chocolate y 1 paquete de tequeños."
	const delCliente = "quiero además un café"

	for kind, label := range contextKinds {
		got := ComposeSourceText([]events.ThreadEntry{
			{Seq: 1, Role: events.RoleSystem, Kind: kind, Text: cuerpo},
			{Seq: 2, Role: events.RoleClient, Kind: events.KindMessage, Text: delCliente},
		})

		// (a) va al bloque de contexto, ROTULADO con lo suyo.
		if quiero := "[" + label + "] " + cuerpo; !strings.Contains(got.Context, quiero) {
			t.Fatalf("clase %q: el contexto no llega rotulado.\nse esperaba: %q\nbloque:\n%s", kind, quiero, got.Context)
		}
		// (b) NO va al hilo literal, que es de donde salen las evidence (REQ-13).
		if strings.Contains(got.Literal, cuerpo) {
			t.Fatalf("clase %q: el contexto se coló en el hilo literal:\n%s", kind, got.Literal)
		}
		// (c) no cuenta volumen, y el mensaje del cliente sí.
		if got.Messages != 1 {
			t.Fatalf("clase %q: Messages=%d; solo la fila del cliente cuenta volumen (REQ-10b (c))", kind, got.Messages)
		}
		if got.ContextEntries != 1 {
			t.Fatalf("clase %q: ContextEntries=%d; tenía que ser 1", kind, got.ContextEntries)
		}
		// (d) el bloque de contexto va ANTES del literal, y los dos completos.
		iCtx := strings.Index(got.Text, sourceContextHeader)
		iLit := strings.Index(got.Text, sourceLiteralHeader)
		if iCtx < 0 || iLit < 0 || iCtx > iLit {
			t.Fatalf("clase %q: los bloques no están en su sitio (ctx=%d lit=%d):\n%s", kind, iCtx, iLit, got.Text)
		}
		// (e) y un hilo hecho SOLO de esta clase no es un source_text que valga la
		// pena: sin una línea del cliente, `Empty` dice que sí. Es la guarda que
		// impide mandarle al LLM un prompt donde lo único que hay son productos que
		// listamos nosotros.
		solo := ComposeSourceText([]events.ThreadEntry{
			{Seq: 1, Role: events.RoleSystem, Kind: kind, Text: cuerpo},
		})
		if !solo.Empty() {
			t.Fatalf("clase %q: un hilo hecho solo de contexto tiene que dar Empty()==true (Messages=%d)", kind, solo.Messages)
		}
	}
}

// TestSpeakerOf_LaAsimetriaEsDELIBERADA fija la traducción del rol a la palabra que
// lee el LLM. Hasta el 2026-08-22 `speakerOf` NO TENÍA NI UN TEST: una mutación que
// devolviera siempre "cliente" dejaba la suite entera verde.
//
// Lo que se fija es la ASIMETRÍA, que el propio docstring de la función declara
// deliberada: SOLO `client` es «cliente»; todo lo demás —`business`, `system`, y
// cualquier rol que se invente después— cae a «negocio». Es fail-closed por el mismo
// lado que el resto del fichero: ante la duda de quién habló, la respuesta segura es
// la que NO convierte texto ajeno en pedido del cliente.
//
// MUTACIÓN (compila): en source_composer.go, sustituir el cuerpo de speakerOf por
//
//	return "cliente"
//
// (`role` queda como parámetro sin usar, que en Go compila).
//
// MUTACIÓN 2 (compila, y es la sutil): sustituir
//
//	if role == events.RoleClient {
//
// por
//
//	if role != events.RoleBusiness {
//
// Parece equivalente y no lo es: `system` y los roles desconocidos cruzan al lado
// inseguro. Solo las dos últimas filas de la tabla la ven.
func TestSpeakerOf_LaAsimetriaEsDELIBERADA(t *testing.T) {
	casos := []struct {
		nombre string
		role   events.Role
		voz    string
	}{
		{"la voz del cliente, la única que lo es", events.RoleClient, "cliente"},
		{"la voz del negocio", events.RoleBusiness, "negocio"},
		{"la plataforma, que con entry_kind='message' no debería aparecer nunca", events.RoleSystem, "negocio"},
		{"un rol que nadie ha inventado todavía", events.Role("un_rol_del_futuro"), "negocio"},
	}
	for _, c := range casos {
		if got := speakerOf(c.role); got != c.voz {
			t.Fatalf("%s (%q): speakerOf devolvió %q y tenía que devolver %q — lo desconocido cae del lado "+
				"que NO convierte texto ajeno en pedido del cliente", c.nombre, c.role, got, c.voz)
		}
	}
}
