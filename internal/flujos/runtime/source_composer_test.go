// source_composer_test.go — Plan 044 · Ola 1 · T1.4.
//
// ⏳ NINGUNO DE ESTOS TESTS SE HA EJECUTADO. Se escribieron en un entorno sin Go,
// sin red y sin Postgres, así que ninguno está declarado como pasado. Lo que sí
// está escrito es CÓMO ponerlos rojos: cada test lleva su MUTACIÓN concreta, y la
// mutación está elegida para que COMPILE — una mutación que no compila no prueba
// nada, solo dice que el compilador funciona.
//
// 🔑 EL PAR DE TESTS QUE PRUEBA QUE EL MECANISMO ES UNO SOLO. Los dos primeros son
// EL MISMO ESCENARIO con la única diferencia de la clase de contexto: un `summary`
// en uno y un `message_out_of_turn` en el otro. Los dos afirman EXACTAMENTE los
// mismos hechos, por el mismo helper, y —esto es lo que de verdad lo prueba— UNA
// SOLA MUTACIÓN (vaciar la tabla `contextKinds`) los pone rojos a los dos. Si algún
// día hiciera falta una mutación distinta para cada uno, es que alguien abrió el
// segundo camino que T1.4 prohíbe.
//
// 🔧 …Y POR QUÉ ESE PAR NO BASTABA (revisión 2026-08-22). Dos gemelos verdes son
// compatibles con dos ramas duplicadas: cada una satisface a su test y nadie
// compara una con otra. El guardián de verdad se añadió en tres piezas y las tres
// hacen falta:
//
//  1. TestT14_LasDosClasesDeContexto_MISMAEntradaMISMOSALIDA — el MISMO cuerpo de
//     texto por las dos clases, y el `source_text` resultante idéntico byte a byte
//     salvo el rótulo. Caza la DIVERGENCIA, que es la avería que hace daño.
//  2. source_composer_internal_test.go — desde dentro del paquete: que
//     `contextKinds` sea la única fuente, que TODA clase de contexto conocida esté
//     en ella y ninguna fuera, y que una clase NUEVA herede automáticamente los
//     asertos de las viejas.
//  3. Los rótulos y las cabeceras, que ahora se afirman ENTEROS (ver el bloque de
//     constantes de abajo). Antes se podían vaciar, intercambiar o recortar sin
//     poner rojo nada, y el rótulo es literalmente lo que D-044.24 compra.
//
// Lo que ni con las tres se compra —y hay que decirlo— es que el código tenga UNA
// rama: una duplicación que se comporte igual pasa. Lo que se compra es que el día
// que diverja se sepa.
//
// El hilo llega por un DOBLE (`hiloFalso`) y no por Postgres: aquí no hay base. Lo
// que ese doble NO ejerce está dicho al final del fichero, en la lista de lo que
// queda sin verificar.
package runtime_test

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	flowruntime "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// ---------------------------------------------------------------------------
// EL ESCENARIO DEL CRITERIO, ESCRITO UNA VEZ
// ---------------------------------------------------------------------------

const (
	compMsg1 = "quiero 2 tortas de chocolate"
	compMsg2 = "para el jueves si se puede"
	compMsg3 = "y un paquete de tequeños de 30"
	// compContexto REPITE LITERALMENTE el primer mensaje, y eso es el punto del
	// criterio: un resumen repite a propósito lo que el cliente ya dijo. Si alguien
	// deduplica por texto, el que se cae es el ORIGINAL —el contexto va primero,
	// porque se inyecta al cambiar de evento— y con él la evidencia real.
	compContexto = "Esto es lo que ya habías decidido en tu pedido:\n" + compMsg1
)

// ---------------------------------------------------------------------------
// EL CONTRATO, COPIADO A MANO. NO SE IMPORTA, Y ESO ES EL PUNTO.
// ---------------------------------------------------------------------------
//
// 🔴 ESTAS OCHO CADENAS SON LA MITAD DE T1.4 QUE NADIE AFIRMABA. Hasta el
// 2026-08-22 los tests comprobaban que el TEXTO del contexto estuviera en el bloque
// de contexto, y nada más: ni que llevara su rótulo, ni cuál. Con eso, estas dos
// mutaciones dejaban la suite ENTERA en verde:
//
//	// vaciar los rótulos
//	var contextKinds = map[events.EntryKind]string{
//	    events.KindSummary:          "",
//	    events.KindMessageOutOfTurn: "",
//	}
//
//	// o intercambiarlos
//	var contextKinds = map[events.EntryKind]string{
//	    events.KindSummary:          "mensaje del negocio fuera de turno",
//	    events.KindMessageOutOfTurn: "resumen del sistema",
//	}
//
// Y D-044.24 dice literalmente que EL RÓTULO NO ES DECORATIVO: es lo único que le
// dice al LLM que la lista de productos del automensaje de rescate la escribimos
// NOSOTROS. Un rótulo vacío es un corchete vacío delante de una lista de productos;
// dos rótulos intercambiados le atribuyen al sistema lo que dijo el negocio y al
// revés. Las dos cosas rompen justo lo que la marca compra, y las dos pasaban.
//
// Lo mismo con la CABECERA: se comprobaba por el prefijo "### CONTEXTO PREVIO", así
// que la instrucción de verdad —«— NO es lo que el cliente está pidiendo ###»— se
// podía borrar entera sin que nada se pusiera rojo. Esa frase no es un adorno del
// separador: es la línea que el prompt de P2 lee.
//
// ⚠️ SE COPIAN A MANO A PROPÓSITO. Las constantes de source_composer.go no se
// exportan, pero aunque se exportaran no se usarían aquí: un test que leyera el
// rótulo de la misma tabla que produce el rótulo comprobaría que una variable es
// igual a sí misma. El valor literal escrito aquí es lo que convierte «cambiar el
// rótulo» en un cambio VISIBLE — y cambiarlo es cambiar el contrato con el prompt de
// P2, así que tiene que costar tocar dos sitios y leer este párrafo.
const (
	rotuloSummary    = "resumen del sistema"
	rotuloFueraTurno = "mensaje del negocio fuera de turno"

	cabeceraContexto = "### CONTEXTO PREVIO — NO es lo que el cliente está pidiendo ###"
	cierreContexto   = "### FIN DEL CONTEXTO PREVIO ###"
	cabeceraLiteral  = "### MENSAJES DE LA CONVERSACIÓN (literal, en orden) ###"
	cierreLiteral    = "### FIN DE LOS MENSAJES ###"

	// Las dos palabras del hilo literal (speakerOf). Van aquí por el mismo motivo:
	// son lo que lee el LLM para saber quién habló.
	vozCliente = "cliente"
	vozNegocio = "negocio"
)

// hiloConContexto arma el hilo del criterio: UNA entrada de contexto (la que
// distinga el test) y TRES mensajes del cliente, uno de ellos repetido por el
// contexto.
func hiloConContexto(contexto events.ThreadEntry) []events.ThreadEntry {
	return []events.ThreadEntry{
		contexto,
		{Seq: 2, Role: events.RoleClient, Kind: events.KindMessage, Text: compMsg1},
		{Seq: 3, Role: events.RoleClient, Kind: events.KindMessage, Text: compMsg2},
		{Seq: 4, Role: events.RoleClient, Kind: events.KindMessage, Text: compMsg3},
	}
}

// verificaElMismoResultado son los hechos que T1.4 promete, escritos UNA vez para
// las dos clases de contexto. Que las dos clases compartan estas afirmaciones no es
// ahorro de tecleo: es la definición operativa de «el mismo resultado por el mismo
// camino».
//
// ⚠️ Lo que NO se afirma aquí es que el TEXTO del bloque de contexto sea idéntico
// entre las dos clases: no lo es ni debe serlo —un resumen y un automensaje dicen
// cosas distintas y llevan rótulos distintos—. Lo idéntico es el TRATO.
//
// 🔧 `rotuloPropio` y `rotuloAjeno` se añadieron el 2026-08-22: son la mitad de
// D-044.24 que no se afirmaba en ningún sitio (ver el bloque de constantes de
// arriba). Pedir los DOS —el que tiene que estar y el que tiene que NO estar— es lo
// que hace que INTERCAMBIARLOS ponga rojos los dos tests a la vez; con solo el
// propio, un intercambio dejaría cada test rojo por separado pero un rótulo
// duplicado en las dos filas pasaría inadvertido.
func verificaElMismoResultado(t *testing.T, c flowruntime.Composed, textoDelContexto, rotuloPropio, rotuloAjeno string) {
	t.Helper()

	// (criterio) LOS TRES MENSAJES ORIGINALES SOBREVIVEN. Ninguno descartado por
	// duplicado, aunque el contexto repita el primero palabra por palabra.
	for _, m := range []string{compMsg1, compMsg2, compMsg3} {
		if !strings.Contains(c.Literal, m) {
			t.Fatalf("el hilo literal perdió un mensaje original: %q no está en\n%s", m, c.Literal)
		}
	}

	// (criterio · efecto c) EL CONTADOR DE VOLUMEN VALE 3, NO 4.
	if c.Messages != 3 {
		t.Fatalf("el contador de mensajes del hilo vale %d; tenía que valer 3 (el contexto NO es actividad del cliente)", c.Messages)
	}
	if c.ContextEntries != 1 {
		t.Fatalf("entradas de contexto = %d; tenía que ser 1", c.ContextEntries)
	}

	// (criterio · efecto a) EL CONTEXTO VA EN SU BLOQUE, ROTULADO Y SEPARADO.
	if !strings.Contains(c.Context, textoDelContexto) {
		t.Fatalf("el texto del contexto no está en el bloque de contexto:\n%s", c.Context)
	}
	if strings.Contains(c.Literal, textoDelContexto) {
		t.Fatalf("el texto del contexto se coló en el HILO LITERAL, que es de donde salen las evidence:\n%s", c.Literal)
	}

	// (D-044.24) EL RÓTULO, Y ES SU RÓTULO. Se exige la forma ENTERA —corchetes,
	// rótulo, espacio y el texto pegado detrás— y no un `Contains` del rótulo suelto:
	// un rótulo que apareciera en otra línea, o un texto sin rótulo delante, pasarían
	// el `Contains` y no dirían nada al LLM.
	quiero := "[" + rotuloPropio + "] " + textoDelContexto
	if !strings.Contains(c.Context, quiero) {
		t.Fatalf("la entrada de contexto no llega ROTULADA con lo suyo.\nse esperaba: %q\nbloque:\n%s", quiero, c.Context)
	}
	// Y NO LLEVA EL DE LA OTRA CLASE. Éste es el aserto que pone rojo un
	// intercambio de rótulos, que es la mutación que más silenciosamente rompe
	// D-044.24: el LLM seguiría viendo dos bloques bien formados, con la atribución
	// invertida.
	if strings.Contains(c.Context, rotuloAjeno) {
		t.Fatalf("el bloque de contexto lleva el rótulo de la OTRA clase (%q):\n%s", rotuloAjeno, c.Context)
	}

	verificaLaEstructuraDelTexto(t, c, textoDelContexto)
}

// verificaLaEstructuraDelTexto comprueba que el `source_text` tenga sus DOS bloques,
// con sus cabeceras enteras, en orden y sin solaparse. Va extraída de
// verificaElMismoResultado por gocyclo: el criterio no cambia ni una coma.
func verificaLaEstructuraDelTexto(t *testing.T, c flowruntime.Composed, textoDelContexto string) {
	t.Helper()

	// (criterio · efecto e) NINGUNA `evidence` PUEDE APUNTAR AL CONTEXTO. La
	// evidence es una subcadena del bloque literal (REQ-13); si el contexto no está
	// ahí dentro, no hay subcadena posible que lo cite. Eso es lo que se afirma —y
	// se afirma así porque las evidence las produce la Ola 2, que todavía no existe.
	//
	// 🔴 LAS CABECERAS SE BUSCAN ENTERAS. Antes se buscaban por prefijo
	// ("### CONTEXTO PREVIO"), con lo que la instrucción real —el «— NO es lo que el
	// cliente está pidiendo ###»— se podía borrar sin poner rojo nada. Esa frase es
	// la línea que el prompt de P2 lee: es la cabecera, no un comentario sobre ella.
	iCtx := strings.Index(c.Text, cabeceraContexto)
	iFinCtx := strings.Index(c.Text, cierreContexto)
	iLit := strings.Index(c.Text, cabeceraLiteral)
	iFinLit := strings.Index(c.Text, cierreLiteral)
	switch {
	case iCtx < 0:
		t.Fatalf("falta la cabecera COMPLETA del bloque de contexto (%q):\n%s", cabeceraContexto, c.Text)
	case iFinCtx < 0:
		t.Fatalf("falta el cierre del bloque de contexto (%q):\n%s", cierreContexto, c.Text)
	case iLit < 0:
		t.Fatalf("falta la cabecera COMPLETA del hilo literal (%q):\n%s", cabeceraLiteral, c.Text)
	case iFinLit < 0:
		t.Fatalf("falta el cierre del hilo literal (%q):\n%s", cierreLiteral, c.Text)
	case iCtx >= iFinCtx || iFinCtx >= iLit || iLit >= iFinLit:
		t.Fatalf("los bloques están desordenados o solapados (ctx=%d fin=%d lit=%d finlit=%d):\n%s",
			iCtx, iFinCtx, iLit, iFinLit, c.Text)
	}
	if pos := strings.Index(c.Text, textoDelContexto); pos < 0 || pos > iFinCtx {
		t.Fatalf("el texto del contexto aparece FUERA de su bloque (pos=%d, fin del bloque=%d)", pos, iFinCtx)
	}
}

// TestT14_ElSummaryEsCONTEXTO es el criterio literal de T1.4 con la PRIMERA clase
// de contexto: 3 filas `message` + 1 `summary` que repite lo dicho.
//
// MUTACIÓN (compila, y pone rojo ESTE test y el siguiente A LA VEZ — que es lo que
// prueba que el mecanismo es uno solo): en source_composer.go, dejar la tabla
// vacía:
//
//	var contextKinds = map[events.EntryKind]string{}
//
// MUTACIÓN 2 (compila, y es la deduplicación que REQ-10b prohíbe): en
// source_composer.go, dentro del bucle de ComposeSourceText, justo después de
//
//	label, esContexto := contextLabel(e.Kind)
//
// añadir
//
//	if !esContexto && strings.Contains(strings.Join(contexto, "\n"), e.Text) { continue }
//
// El primer mensaje del cliente desaparece por «duplicado» del resumen y el
// contador cae a 2: exactamente el fallo que la marca existe para evitar.
//
// MUTACIÓN 3 (compila, y pone rojo EL CONTADOR y nada más — es la única que ataca
// REQ-10b (c) por su cuenta): en source_composer.go, dentro del brazo
// `case esContexto:` de ComposeSourceText, añadir junto al `out.ContextEntries++`
// la línea
//
//	out.Messages++
//
// El contexto pasa a contar como actividad del cliente: `Messages` vale 4 en vez de
// 3 y el aserto del volumen se pone rojo. Es el defecto exacto que REQ-10b (c)
// prohíbe —«un resumen no es actividad del cliente»— y el que haría que la métrica
// de volumen de la O5 contara pedidos que nadie hizo.
//
// ⚠️ LA MUTACIÓN 3 QUE HABÍA AQUÍ ERA INALCANZABLE. Decía «cambiar
// `case e.Kind == events.KindMessage:` por
// `case e.Kind == events.KindMessage || e.Kind == events.KindSummary:`». Compila, y
// no pone rojo NADA: el `switch` evalúa `case esContexto:` PRIMERO, y para un
// `summary` ese primer caso ya es verdadero, así que la rama tocada nunca se
// alcanza. Se anota en vez de borrarla porque el error es fácil de repetir: en un
// switch sin expresión, mutar un `case` posterior al que ya captura el valor es
// mutar código muerto.
func TestT14_ElSummaryEsCONTEXTO(t *testing.T) {
	hilo := hiloConContexto(events.ThreadEntry{
		Seq: 1, Role: events.RoleSystem, Kind: events.KindSummary, Text: compContexto,
	})
	verificaElMismoResultado(t, flowruntime.ComposeSourceText(hilo), compContexto,
		rotuloSummary, rotuloFueraTurno)
}

// TestT14_ElSalienteFueraDeTurnoEsCONTEXTO_MISMOCAMINO es el MISMO test con la
// SEGUNDA clase (D-044.24). No cambia ni una afirmación: cambia la fila del hilo.
//
// 🔴 Y el texto elegido para el saliente no es cualquiera: es un automensaje de
// rescate, que LISTA PRODUCTOS. Sin rótulo, el LLM extraería esa lista como pedido
// del cliente — el fallo concreto que D-044.24 nombra.
//
// MUTACIÓN: la MISMA que el test de arriba (vaciar `contextKinds`). Que una sola
// mutación ponga rojos los dos es la evidencia de que las dos clases pasan por el
// mismo sitio.
//
// MUTACIÓN 2 (compila, y pone rojo SOLO éste): en source_composer.go, quitar de
// `contextKinds` la fila del saliente, dejando la tabla con una sola clase:
//
//	var contextKinds = map[events.EntryKind]string{
//		events.KindSummary: "resumen del sistema",
//	}
//
// El automensaje de rescate deja de ser contexto: cae al `default` fail-closed y
// DESAPARECE del `source_text`. `ContextEntries` vale 0 y este test se pone rojo
// mientras el del `summary` sigue verde — que es la asimetría que hace falta para
// distinguir «se rompió D-044.24» de «se rompió el mecanismo entero».
//
// ⚠️ LA MUTACIÓN 2 QUE HABÍA AQUÍ ERA INALCANZABLE, igual que su gemela del test de
// arriba: añadir `|| e.Kind == events.KindMessageOutOfTurn` al
// `case e.Kind == events.KindMessage:` no toca nada, porque el `case esContexto:`
// que va delante ya captura al saliente. Compilaba y no ponía rojo nada.
func TestT14_ElSalienteFueraDeTurnoEsCONTEXTO_MISMOCAMINO(t *testing.T) {
	const rescate = "¿Seguimos con tu pedido? Tenías: 2 tortas de chocolate y 1 paquete de tequeños."
	hilo := hiloConContexto(events.ThreadEntry{
		Seq: 1, Role: events.RoleBusiness, Kind: events.KindMessageOutOfTurn, Text: rescate,
	})
	verificaElMismoResultado(t, flowruntime.ComposeSourceText(hilo), rescate,
		rotuloFueraTurno, rotuloSummary)
}

// TestT14_LasDosClasesDeContexto_MISMAEntradaMISMOSALIDA es el guardián EJECUTABLE
// del «mecanismo único», y sustituye a un `grep` que vivía dentro de un comentario
// (source_composer.go:35) y que no ejecutaba nadie.
//
// # QUÉ HACE, Y EN QUÉ SE DIFERENCIA DE LOS DOS TESTS DE ARRIBA
//
// Los dos de arriba son GEMELOS: el mismo escenario con distinta clase. Eso prueba
// que las dos clases funcionan, pero no que compartan camino — dos ramas duplicadas
// los dejarían igual de verdes. Éste mete el MISMO CUERPO DE TEXTO por las dos
// clases y exige que el `source_text` resultante sea IDÉNTICO BYTE A BYTE una vez
// se normaliza el rótulo, que es lo único que tiene derecho a diferir. Cualquier
// divergencia —un espacio, un salto de línea, un orden distinto, un corchete que se
// escape— cae aquí.
//
// # 🔴 QUÉ GARANTIZA Y QUÉ NO. Dicho, porque la diferencia importa
//
// GARANTIZA que las dos clases se COMPORTAN igual hoy y que seguirán haciéndolo:
// el día que alguien parta la función en dos ramas y una diverja en un dato, este
// test se pone rojo. Esa es la avería que el comentario del `grep` temía —«dos
// caminos gemelos que divergen en un dato son la forma clásica de que uno de los dos
// se quede atrás»— y es la que de verdad hace daño.
//
// NO GARANTIZA que el código tenga una sola rama. Si alguien duplica el camino y
// las dos copias se comportan EXACTAMENTE igual, esto sigue verde. Eso no se puede
// comprobar ejecutando: haría falta mirar la forma del código (AST, o el `grep` que
// nadie corre). Se acepta a conciencia — una duplicación que no diverge no ha roto
// nada todavía, y el día que diverja es cuando este test la caza.
//
// La otra mitad del guardián —que `contextKinds` sea la ÚNICA fuente y que TODA
// clase de contexto conocida esté en ella y ninguna fuera— vive en
// source_composer_internal_test.go, que puede leer la tabla.
//
// MUTACIÓN (compila, y es LA divergencia): en source_composer.go, dentro del brazo
// `case esContexto:`, sustituir
//
//	contexto = append(contexto, "["+label+"] "+e.Text)
//
// por
//
//	if e.Kind == events.KindMessageOutOfTurn {
//		contexto = append(contexto, "("+label+") "+e.Text)
//	} else {
//		contexto = append(contexto, "["+label+"] "+e.Text)
//	}
//
// Son dos caminos que hacen «casi» lo mismo: paréntesis en vez de corchetes para
// una de las dos clases. Los dos tests gemelos de arriba se pondrían rojos por el
// aserto del rótulo, sí — pero éste es el que dice POR QUÉ: el trato dejó de ser
// el mismo.
//
// MUTACIÓN 2 (compila, y es la divergencia SILENCIOSA que los gemelos NO ven): en
// el mismo brazo, sustituir
//
//	if e.Text == "" {
//		continue
//	}
//
// por
//
//	if e.Text == "" || e.Kind == events.KindMessageOutOfTurn && len(e.Text) > 40 {
//		continue
//	}
//
// Un recorte «razonable» aplicado a una sola clase. El cuerpo de este test mide más
// de 40 caracteres, así que el saliente desaparece del contexto y las dos salidas
// dejan de coincidir.
func TestT14_LasDosClasesDeContexto_MISMAEntradaMISMOSALIDA(t *testing.T) {
	// EL MISMO CUERPO por las dos puertas. Es un automensaje de rescate —lista
	// productos— porque es el texto cuyo trato importa: si una de las dos clases lo
	// tratara distinto, la que se equivocara convertiría esa lista en pedido.
	const cuerpo = "Tenías: 2 tortas de chocolate y 1 paquete de tequeños."
	const marca = "[<<ROTULO>>]"

	casos := []struct {
		nombre string
		kind   events.EntryKind
		role   events.Role
		propio string
		ajeno  string
	}{
		{"summary", events.KindSummary, events.RoleSystem, rotuloSummary, rotuloFueraTurno},
		{"message_out_of_turn", events.KindMessageOutOfTurn, events.RoleBusiness, rotuloFueraTurno, rotuloSummary},
	}

	// normalizados guarda cada `source_text` con SU rótulo sustituido por una marca
	// común: lo que queda es el TRATO, sin lo único que tiene derecho a diferir.
	normalizados := make([]string, 0, len(casos))
	for _, c := range casos {
		got := flowruntime.ComposeSourceText(hiloConContexto(events.ThreadEntry{
			Seq: 1, Role: c.role, Kind: c.kind, Text: cuerpo,
		}))
		// Primero, los hechos de T1.4 uno por uno (los mismos del helper de siempre).
		verificaElMismoResultado(t, got, cuerpo, c.propio, c.ajeno)
		// Y después, el texto normalizado para compararlo con el de la otra clase.
		normalizados = append(normalizados, strings.Replace(got.Text, "["+c.propio+"]", marca, 1))
	}

	if normalizados[0] != normalizados[1] {
		t.Fatalf("las dos clases de contexto NO reciben el mismo trato — hay dos caminos, no uno.\n"+
			"--- %s ---\n%s\n--- %s ---\n%s",
			casos[0].nombre, normalizados[0], casos[1].nombre, normalizados[1])
	}
}

// TestT14_SinContexto_NoHayBloqueDeContexto: un hilo sin contexto no imprime un
// encabezado vacío. Un bloque rotulado y vacío le diría al modelo que hubo
// antecedentes que no ve, y se los inventaría.
//
// MUTACIÓN (compila): en source_composer.go, cambiar
//
//	if out.Context != "" {
//
// por
//
//	if true {
func TestT14_SinContexto_NoHayBloqueDeContexto(t *testing.T) {
	c := flowruntime.ComposeSourceText([]events.ThreadEntry{
		{Seq: 1, Role: events.RoleClient, Kind: events.KindMessage, Text: compMsg1},
	})
	if c.ContextEntries != 0 || c.Context != "" {
		t.Fatalf("sin entradas de contexto no puede haber bloque: entradas=%d contexto=%q", c.ContextEntries, c.Context)
	}
	if strings.Contains(c.Text, "### CONTEXTO PREVIO") {
		t.Fatalf("se imprimió un bloque de contexto vacío:\n%s", c.Text)
	}
}

// TestT14_LoDesconocidoNoEntraComoLiteral: el reparto es FAIL-CLOSED. Una
// `decision` —que es estructura, no prosa— y un `entry_kind` que nadie ha
// inventado todavía NO entran al hilo literal. Entrar por defecto es exactamente
// cómo un texto nuestro acabaría contando como pedido del cliente.
//
// MUTACIÓN (compila): en source_composer.go cambiar
//
//	case e.Kind == events.KindMessage:
//
// por
//
//	default:
//
// y borrar el `default:` de abajo con su `continue` (queda un switch con dos
// brazos: contexto o literal). Con eso, TODO lo que no sea contexto entra como
// mensaje del cliente.
func TestT14_LoDesconocidoNoEntraComoLiteral(t *testing.T) {
	c := flowruntime.ComposeSourceText([]events.ThreadEntry{
		{Seq: 1, Role: events.RoleClient, Kind: events.KindDecision, Text: `{"line":"torta","qty":2}`},
		{Seq: 2, Role: events.RoleClient, Kind: events.EntryKind("grado_del_futuro"), Text: "lo que sea"},
		{Seq: 3, Role: events.RoleClient, Kind: events.KindMessage, Text: compMsg1},
	})
	if c.Messages != 1 {
		t.Fatalf("mensajes = %d; solo la fila `message` cuenta como hilo literal", c.Messages)
	}
	if strings.Contains(c.Text, "grado_del_futuro") || strings.Contains(c.Text, "lo que sea") ||
		strings.Contains(c.Text, `"qty":2`) {
		t.Fatalf("entró al source_text algo que no es ni literal ni contexto:\n%s", c.Text)
	}
}

// TestT14_ElHiloLiteralDiceQUIENHablo es `speakerOf` visto desde fuera, y hasta el
// 2026-08-22 NO EXISTÍA: ni un solo test afirmaba que el hilo literal dijera
// `cliente:` o `negocio:`. Una mutación que devolviera siempre "cliente" dejaba la
// suite entera verde — y eso es justamente lo contrario del criterio del fichero,
// porque convertir en «cliente» lo que dijo el negocio es la forma más directa de
// que el LLM lea nuestro propio texto como pedido.
//
// Se prueba por el resultado (`Composed.Literal`) y no llamando a `speakerOf`,
// porque la función no se exporta y porque lo que importa no es la función: es la
// línea que acaba dentro del prompt. La versión que SÍ llama a `speakerOf` directa
// vive en source_composer_internal_test.go, que es de package runtime.
//
// # LA ASIMETRÍA ES DELIBERADA, Y POR ESO SE FIJA
//
// SOLO `client` es «cliente». Todo lo demás cae a «negocio»: `business`, `system`
// —que con entry_kind='message' no debería aparecer nunca— y cualquier rol que no
// exista todavía. Es fail-closed por el mismo lado que el resto del fichero: ante la
// duda de quién habló, la respuesta segura es la que NO convierte texto ajeno en
// pedido del cliente.
//
// MUTACIÓN (compila): en source_composer.go, en speakerOf, sustituir el cuerpo por
//
//	return "cliente"
//
// (el parámetro `role` queda sin usar, que en Go compila). Todo el hilo pasa a ser
// voz del cliente y se ponen rojas las dos últimas líneas.
//
// MUTACIÓN 2 (compila, y es la MISMA pérdida por el lado contrario): sustituir
//
//	if role == events.RoleClient {
//
// por
//
//	if role != events.RoleBusiness {
//
// La diferencia solo se ve en el rol DESCONOCIDO —y en `system`—, que pasa de
// «negocio» (seguro) a «cliente» (el lado que inventa pedidos). Es exactamente la
// clase de cambio que parece equivalente al leerlo y no lo es, y sin la fila del rol
// inventado de abajo no la detectaría nadie.
func TestT14_ElHiloLiteralDiceQUIENHablo(t *testing.T) {
	c := flowruntime.ComposeSourceText([]events.ThreadEntry{
		{Seq: 1, Role: events.RoleClient, Kind: events.KindMessage, Text: compMsg1},
		{Seq: 2, Role: events.RoleBusiness, Kind: events.KindMessage, Text: "¿Para cuándo la necesitas?"},
		{Seq: 3, Role: events.RoleSystem, Kind: events.KindMessage, Text: "reintento automático"},
		{Seq: 4, Role: events.Role("un_rol_del_futuro"), Kind: events.KindMessage, Text: "vaya usted a saber"},
	})

	quiero := []string{
		vozCliente + ": " + compMsg1,
		vozNegocio + ": " + "¿Para cuándo la necesitas?",
		vozNegocio + ": " + "reintento automático",
		vozNegocio + ": " + "vaya usted a saber",
	}
	lineas := strings.Split(c.Literal, "\n")
	if len(lineas) != len(quiero) {
		t.Fatalf("el hilo literal tenía que tener %d líneas, tiene %d:\n%s", len(quiero), len(lineas), c.Literal)
	}
	for i, l := range quiero {
		if lineas[i] != l {
			t.Fatalf("línea %d del hilo literal = %q; se esperaba %q", i, lineas[i], l)
		}
	}
	// Y dicho como afirmación, no como comparación de cadenas: SOLO la fila del
	// cliente se atribuye al cliente. Si esto falla, alguien ha movido la asimetría
	// al lado inseguro.
	if n := strings.Count(c.Literal, vozCliente+": "); n != 1 {
		t.Fatalf("hay %d líneas atribuidas al CLIENTE y solo una fila lo es; "+
			"lo desconocido tiene que caer del lado del negocio:\n%s", n, c.Literal)
	}
}

// ---------------------------------------------------------------------------
// EL CAMINO CABLEADO: leer, cifrar, guardar — y CUÁNDO
// ---------------------------------------------------------------------------

// hiloFalso es el doble del lector del hilo. Cuenta sus lecturas porque el CUÁNDO
// es la mitad de esta tarea (D-044.26): si este contador crece durante un Observe,
// el literal está viajando por el camino del entrante.
type hiloFalso struct {
	entradas []events.ThreadEntry
	err      error
	lecturas int
}

func (h *hiloFalso) ListThread(_ context.Context, _ string, limit int) ([]events.ThreadEntry, error) {
	h.lecturas++
	if h.err != nil {
		return nil, h.err
	}
	if limit > 0 && len(h.entradas) > limit {
		// Mismo recorte que listThreadSQL: se quedan las MÁS RECIENTES.
		return h.entradas[len(h.entradas)-limit:], nil
	}
	return h.entradas, nil
}

// kpDePrueba es un KeyProvider con KEK fija (32B): el cifrado de estos tests es el
// de verdad, no un doble. Lo que se prueba con él es que el sobre se pueda ABRIR
// después — un test que solo comprobara «hay bytes» pasaría con basura dentro.
func kpDePrueba(t *testing.T) crypto.KeyProvider {
	t.Helper()
	master := make([]byte, 32)
	for i := range master {
		master[i] = byte(i + 7)
	}
	kp, err := crypto.NewEnvKeyProvider(crypto.KeyringConfig{
		MasterB64: base64.StdEncoding.EncodeToString(master),
	})
	if err != nil {
		t.Fatalf("KeyProvider de prueba: %v", err)
	}
	return kp
}

// compEntorno monta el agregador CON el compositor cableado, que es como corre en
// producción desde T1.4.
type compEntorno struct {
	sink   *flowruntime.IntakeAggregator
	jobs   *intake.MemoryStore
	hilo   *hiloFalso
	cipher *crypto.FieldCipher
	clock  *aggReloj
}

func nuevoCompEntorno(t *testing.T, ventana time.Duration, entradas []events.ThreadEntry) *compEntorno {
	t.Helper()
	clock := nuevoAggReloj()
	jobs := intake.NewMemoryStore(clock.now)
	cfg := store.NewMemoryRepository()
	s := store.DefaultTenantSettings(aggTenant)
	s.AggregationWindow = ventana
	cfg.SetTenantSettings(s)
	ents := entitlements.NewFake()
	ents.Enable(aggTenant, entitlements.FeatureLLMIntake)

	hilo := &hiloFalso{entradas: entradas}
	cipher := crypto.NewFieldCipher(kpDePrueba(t))
	comp := flowruntime.NewSourceTextComposer(aggLogger(), hilo, jobs, cipher)

	return &compEntorno{
		sink: flowruntime.NewIntakeAggregator(aggLogger(), jobs, cfg, ents,
			flowruntime.WithAggregatorClock(clock.now),
			flowruntime.WithSourceComposer(comp)),
		jobs:   jobs,
		hilo:   hilo,
		cipher: cipher,
		clock:  clock,
	}
}

func (e *compEntorno) observa(ctx context.Context, waID string) {
	e.sink.Observe(ctx, flowruntime.IncomingRef{
		Key:         aggKey(),
		WaMessageID: waID,
		MessageTS:   e.clock.now(),
	})
}

// unicoJob devuelve la única fila y falla si hay otra cantidad.
func (e *compEntorno) unicoJob(t *testing.T) intake.Job {
	t.Helper()
	jobs := e.jobs.Jobs()
	if len(jobs) != 1 {
		t.Fatalf("se esperaba UN job, hay %d", len(jobs))
	}
	return jobs[0]
}

// TestT14_ElCompositorNoCorreEnElCaminoDelEntrante es el CUÁNDO de D-044.26 hecho
// una afirmación contable: durante los tres Observe, el hilo no se lee NI UNA VEZ y
// el sobre no se escribe NI UNA VEZ. Todo eso ocurre al flush.
//
// MUTACIÓN (compila): en aggregator.go, dentro de Observe, justo antes del
// `if s.intentTriggers(ref.Intent) {`, añadir
//
//	_ = s.compose.ComposeAtFlush(ctx, ref.Key)
//
// Con eso el literal viaja en línea con el mensaje y los dos contadores crecen.
func TestT14_ElCompositorNoCorreEnElCaminoDelEntrante(t *testing.T) {
	ctx := context.Background()
	e := nuevoCompEntorno(t, 45*time.Second, hiloConContexto(events.ThreadEntry{
		Seq: 1, Role: events.RoleSystem, Kind: events.KindSummary, Text: compContexto,
	}))

	e.observa(ctx, "wa-1")
	e.clock.avanza(2 * time.Second)
	e.observa(ctx, "wa-2")
	e.clock.avanza(2 * time.Second)
	e.observa(ctx, "wa-3")

	if e.hilo.lecturas != 0 {
		t.Fatalf("el hilo se leyó %d veces EN LÍNEA con el mensaje; el presupuesto es CERO lecturas", e.hilo.lecturas)
	}
	c := e.jobs.Counters()
	if c.PutSourceText != 0 {
		t.Fatalf("el sobre se escribió %d veces en el camino del entrante; se llena AL FLUSH", c.PutSourceText)
	}
	if c.Reads != 0 {
		t.Fatalf("hubo %d lecturas de intake_jobs en el camino del entrante; el presupuesto es CERO", c.Reads)
	}
	if c.OpenOrAppend != 3 {
		t.Fatalf("escrituras de ventana = %d; una por entrante y ninguna más", c.OpenOrAppend)
	}
	if j := e.unicoJob(t); len(j.SourceText.Enc) != 0 {
		t.Fatalf("la ventana abierta tiene sobre; durante `aggregating` las tres columnas están legítimamente vacías")
	}
}

// TestT14_AlFlush_ElSobreEsDeTresPiezasYSeAbre: al cerrar la ventana el literal se
// compone, se cifra con el keyring y se guarda ENTERO. Se comprueba abriéndolo.
//
// MUTACIÓN (compila, y deja la fila indescifrable sin que ningún otro test lo note):
// en internal/intake/memory.go, dentro de PutSourceText, sustituir
//
//	j.SourceText = env
//
// por
//
//	j.SourceText = SourceText{Enc: env.Enc}
//
// MUTACIÓN 2 (compila, y prueba que el cable de bootstrap importa): en
// aggregator.go, dentro de closeWindow, comentar la llamada a
// s.compose.ComposeAtFlush — el job cierra igual y el sobre se queda a NULL.
//
// MUTACIÓN 3 (compila, y solo la ve el literal esperado): en source_composer.go, en
// el constructor del texto de ComposeSourceText, quitar el
//
//	b.WriteString("\n")
//
// que va justo detrás de `b.WriteString(sourceContextFooter)`. El cierre del bloque
// de contexto y la cabecera del hilo literal quedan pegados en la misma línea. Con
// la comparación tautológica que había aquí antes —`plano != ComposeSourceText(...)
// .Text`, que compara una función pura consigo misma— esto pasaba en verde: los dos
// lados de la igualdad cambiaban a la vez.
func TestT14_AlFlush_ElSobreEsDeTresPiezasYSeAbre(t *testing.T) {
	ctx := context.Background()
	entradas := hiloConContexto(events.ThreadEntry{
		Seq: 1, Role: events.RoleSystem, Kind: events.KindSummary, Text: compContexto,
	})
	e := nuevoCompEntorno(t, 45*time.Second, entradas)

	e.observa(ctx, "wa-1")
	e.observa(ctx, "wa-2")
	e.observa(ctx, "wa-3")
	e.clock.avanza(46 * time.Second)
	if n := e.sink.Sweep(ctx); n != 1 {
		t.Fatalf("el barrido tenía que cerrar UNA ventana, cerró %d", n)
	}

	j := e.unicoJob(t)
	if j.Status != intake.StatusPending {
		t.Fatalf("status = %q; la ventana tenía que quedar en pending", j.Status)
	}
	if !j.SourceText.Complete() {
		t.Fatalf("el sobre no está entero (enc=%d dek=%d kek=%q): son las tres o ninguna",
			len(j.SourceText.Enc), len(j.SourceText.DEK), j.SourceText.KEKID)
	}
	plano, err := e.cipher.Decrypt(j.SourceText.Enc, j.SourceText.DEK, j.SourceText.KEKID)
	if err != nil {
		t.Fatalf("el sobre guardado no se puede abrir: %v", err)
	}
	// 🔴 CONTRA UN LITERAL ESCRITO A MANO, no contra ComposeSourceText(entradas).Text.
	// Lo que había aquí antes era `plano != ComposeSourceText(entradas).Text`, que es
	// TAUTOLÓGICO: compara una función pura consigo misma sobre la misma entrada, así
	// que pasa siempre que el compositor sea determinista — incluso si compusiera
	// basura, y aunque cambiara el formato entero del sobre. Este literal es el
	// formato REAL que va a leer el prompt de P2, escrito una vez, y por eso
	// cualquier cambio de forma (un salto de línea, un orden, un separador) tiene que
	// pasar por aquí.
	esperado := cabeceraContexto + "\n" +
		"[" + rotuloSummary + "] " + compContexto + "\n" +
		cierreContexto + "\n" +
		cabeceraLiteral + "\n" +
		vozCliente + ": " + compMsg1 + "\n" +
		vozCliente + ": " + compMsg2 + "\n" +
		vozCliente + ": " + compMsg3 + "\n" +
		cierreLiteral
	if plano != esperado {
		t.Fatalf("lo guardado no es el sobre que T1.4 promete.\n--- guardado ---\n%s\n--- esperado ---\n%s",
			plano, esperado)
	}
	// (criterio · efecto e, la mitad de source_refs) EL COMPOSITOR NO TOCA LAS REFS.
	// Las tres son las de los tres entrantes; el contexto no aporta ninguna.
	if len(j.SourceRefs) != 3 {
		t.Fatalf("source_refs = %v; el contexto NO produce referencias", j.SourceRefs)
	}
}

// TestT14_ElHiloNoSeLeeHastaElFlush_YElContextoNoAportaNiRefsNiSegundoPase es el
// efecto (d) —un resumen no es actividad del cliente— probado por donde SÍ se puede
// probar.
//
// # 🔧 QUÉ HABÍA AQUÍ ANTES Y POR QUÉ SE RETIRÓ (2026-08-22)
//
// Había un test llamado TestT14_ElContextoNoMueveLaVentanaDeSilencio que era
// DECORATIVO: inyectaba contexto en el hilo a mitad de ventana y luego comprobaba
// que la ventana cerraba a su hora. Su palanca no podía mover el resultado, y no por
// poco: el hilo NO SE LEE hasta el flush, así que en el instante en que el barrido
// decide, el contenido del doble es literalmente invisible para el agregador.
// Habría pasado igual con el hilo vacío, con el hilo lleno o sin compositor
// cableado. Un test que no puede fallar es peor que ninguno, porque ocupa la casilla
// del que sí probaría eso.
//
// # QUÉ AFIRMA ÉSTE, Y CON QUÉ DIENTES
//
// La palanca es `hilo.lecturas`, que es un contador REAL del doble, y con ella el
// mismo escenario pasa a fijar tres cosas que hoy no fija nadie:
//
//  1. EL BARRIDO DECIDE SIN LEER EL HILO. A los 44 s el barrido mira la ventana,
//     decide que no toca y se va: cero lecturas. Si alguien hiciera que la decisión
//     dependiera del hilo —«ciérrala antes si hay un resumen», que es una idea que
//     suena bien— este contador lo delata.
//  2. SE COMPONE UNA VEZ, NO DOS. Al cerrar, exactamente UNA lectura.
//  3. EL CONTEXTO NO APORTA REFERENCIAS. `source_refs` lleva UNA: la del único
//     entrante. Las cuatro entradas del hilo —una de contexto y tres mensajes ya
//     escritos— no añaden ninguna, porque las referencias las pone `Observe` y las
//     filas del hilo no pasan por ahí.
//
// Y el sobre se abre al final para comprobar que el contexto ESTABA de verdad: sin
// eso, «una lectura» podría ser la lectura de un hilo vacío y el test volvería a no
// decir nada.
//
// ⚠️ LO QUE SIGUE SIN PROBARSE, dicho en vez de callado: que una fila de contexto no
// pueda llegar nunca a `Observe`. Eso es estructural —los productores de thread.go
// escriben en el hilo, no en el agregador— y se verifica leyendo el censo de
// llamantes de `Observe` (uno: `observeForAggregation`), no ejecutando.
//
// MUTACIÓN (compila, y muerde el punto 1): en aggregator.go, en Sweep, dentro del
// bucle y ANTES del `if !s.due(...)`, añadir
//
//	_ = s.compose.ComposeAtFlush(ctx, job.Key)
//
// El barrido pasa a componer cada ventana que MIRA en vez de cada ventana que
// CIERRA: a los 44 s ya hay una lectura del hilo, y en producción eso es descifrar
// el hilo entero de cada ventana abierta cada 5 segundos.
//
// MUTACIÓN 2 (compila, y muerde el punto 2): en aggregator.go, en closeWindow,
// duplicar la llamada al compositor añadiendo justo encima del `if err :=
// s.compose.ComposeAtFlush(...)` la línea
//
//	_ = s.compose.ComposeAtFlush(ctx, job.Key)
//
// La segunda composición no rompe nada visible —el guard del sobre la descarta en
// silencio, ver TestT14_NoSobrescribeUnSobreYaEscrito— pero paga dos veces la
// lectura y el descifrado del hilo. Es la clase de derroche que solo se ve contando.
func TestT14_ElHiloNoSeLeeHastaElFlush_YElContextoNoAportaNiRefsNiSegundoPase(t *testing.T) {
	ctx := context.Background()
	e := nuevoCompEntorno(t, 45*time.Second, nil)
	primerTS := e.clock.now()

	e.observa(ctx, "wa-1")

	// El Plan 043 inyecta un resumen en el hilo MIENTRAS la ventana está abierta, y
	// los productores de thread.go van escribiendo el turno. El hilo se llena; la
	// ventana ni se entera.
	e.hilo.entradas = hiloConContexto(events.ThreadEntry{
		Seq: 1, Role: events.RoleSystem, Kind: events.KindSummary, Text: compContexto,
	})

	e.clock.avanza(44 * time.Second)
	if n := e.sink.Sweep(ctx); n != 0 {
		t.Fatalf("la ventana se cerró ANTES de tiempo (%d cierres)", n)
	}
	// (1) EL BARRIDO DECIDIÓ SIN MIRAR EL HILO.
	if e.hilo.lecturas != 0 {
		t.Fatalf("el barrido leyó el hilo %d veces para decidir si tocaba cerrar; la decisión es del RELOJ "+
			"y el hilo solo se lee al flush (D-044.26)", e.hilo.lecturas)
	}
	j := e.unicoJob(t)
	if !j.MessageTS.Equal(primerTS) {
		t.Fatalf("message_ts = %v; el ancla tenía que seguir siendo el primer mensaje (%v)", j.MessageTS, primerTS)
	}
	if j.Status != intake.StatusAggregating {
		t.Fatalf("status = %q; a los 44 s la ventana sigue abierta", j.Status)
	}

	e.clock.avanza(2 * time.Second) // 46 s > 45 s
	if n := e.sink.Sweep(ctx); n != 1 {
		t.Fatalf("la ventana tenía que cerrar por silencio a los 45 s; cerró %d", n)
	}
	// (2) UNA SOLA COMPOSICIÓN.
	if e.hilo.lecturas != 1 {
		t.Fatalf("el hilo se leyó %d veces al cerrar; tenía que ser UNA", e.hilo.lecturas)
	}

	j = e.unicoJob(t)
	// (3) EL CONTEXTO NO PONE REFERENCIAS. Las pone el entrante, y hubo uno.
	if len(j.SourceRefs) != 1 {
		t.Fatalf("source_refs = %v; el hilo tenía CUATRO entradas y ninguna aporta referencia: "+
			"las referencias las pone Observe", j.SourceRefs)
	}
	// Y EL CONTEXTO ESTABA DE VERDAD. Sin esto, «una lectura» podría ser la lectura
	// de un hilo vacío y las tres afirmaciones de arriba se sostendrían igual.
	plano, err := e.cipher.Decrypt(j.SourceText.Enc, j.SourceText.DEK, j.SourceText.KEKID)
	if err != nil {
		t.Fatalf("el sobre guardado no se puede abrir: %v", err)
	}
	if !strings.Contains(plano, "["+rotuloSummary+"] "+compContexto) {
		t.Fatalf("el contexto inyectado a mitad de ventana no llegó ROTULADO al sobre:\n%s", plano)
	}

	// Y un barrido más no vuelve a componer: la ventana ya no está viva.
	if n := e.sink.Sweep(ctx); n != 0 {
		t.Fatalf("el barrido volvió a cerrar %d ventanas ya cerradas", n)
	}
	if e.hilo.lecturas != 1 {
		t.Fatalf("el hilo se volvió a leer tras el cierre (%d lecturas): componer es UNA vez por ventana", e.hilo.lecturas)
	}
}

// TestT14_HiloSoloDeContexto_NoEscribeSobre: si al cerrar no hay NI UNA línea del
// cliente, no se escribe nada. Un source_text hecho solo de contexto es un prompt
// donde lo único que hay son productos que listamos NOSOTROS y ninguna frase del
// cliente que los contradiga — el accidente que D-044.24 describe.
//
// MUTACIÓN (compila): en source_composer.go, dentro de ComposeAtFlush, cambiar
//
//	if composed.Empty() {
//
// por
//
//	if false {
func TestT14_HiloSoloDeContexto_NoEscribeSobre(t *testing.T) {
	ctx := context.Background()
	e := nuevoCompEntorno(t, 45*time.Second, []events.ThreadEntry{
		{Seq: 1, Role: events.RoleSystem, Kind: events.KindSummary, Text: compContexto},
		{Seq: 2, Role: events.RoleBusiness, Kind: events.KindMessageOutOfTurn, Text: "¿Seguimos con tu pedido?"},
	})

	e.observa(ctx, "wa-audio") // una nota de voz: abre ventana y no deja texto en el hilo
	e.clock.avanza(46 * time.Second)
	if n := e.sink.Sweep(ctx); n != 1 {
		t.Fatalf("la ventana tenía que cerrar igual, cerró %d", n)
	}

	j := e.unicoJob(t)
	if j.Status != intake.StatusPending {
		t.Fatalf("status = %q; cerrar la ventana no depende de que haya literal", j.Status)
	}
	if len(j.SourceText.Enc) != 0 || len(j.SourceText.DEK) != 0 || j.SourceText.KEKID != "" {
		t.Fatalf("se escribió un sobre SIN una sola línea del cliente: %d/%d/%q",
			len(j.SourceText.Enc), len(j.SourceText.DEK), j.SourceText.KEKID)
	}
}

// TestT14_UnFalloDelCompositorNoRevierteElCierre: componer es lo ÚLTIMO y su fallo
// no deshace nada. El job se queda en `pending` con el sobre vacío, que es una
// forma legítima en la 0072, y el barrido lo cuenta como cerrado.
//
// MUTACIÓN (compila): en aggregator.go, dentro de closeWindow, cambiar
//
//	if err := s.compose.ComposeAtFlush(ctx, job.Key); err != nil {
//		s.log.Error(...)
//	}
//
// por la misma condición con un `return false` detrás del Error.
func TestT14_UnFalloDelCompositorNoRevierteElCierre(t *testing.T) {
	ctx := context.Background()
	e := nuevoCompEntorno(t, 45*time.Second, hiloConContexto(events.ThreadEntry{
		Seq: 1, Role: events.RoleSystem, Kind: events.KindSummary, Text: compContexto,
	}))
	e.jobs.FailPutWith(errors.New("la base dijo que no"))

	e.observa(ctx, "wa-1")
	e.clock.avanza(46 * time.Second)
	if n := e.sink.Sweep(ctx); n != 1 {
		t.Fatalf("un fallo del compositor NO puede impedir el cierre; cierres = %d", n)
	}
	j := e.unicoJob(t)
	if j.Status != intake.StatusPending {
		t.Fatalf("status = %q; la transición ya estaba sellada antes de componer", j.Status)
	}
	if len(j.SourceText.Enc) != 0 {
		t.Fatalf("el sobre no puede haberse escrito: PutSourceText falló")
	}
}

// TestT14_UnFalloAlLeerElHiloNoRevierteElCierre: mismo contrato, otra causa. Si el
// hilo no se puede leer —o no se puede descifrar— la ventana ya está cerrada y el
// job sigue su camino sin literal.
//
// MUTACIÓN (compila): en source_composer.go, dentro de ComposeAtFlush, cambiar
//
//	if err != nil {
//		return fmt.Errorf("compositor: leer el hilo del evento %s: %w", key.EventID, err)
//	}
//
// por un `panic(err)`. El test pasa de rojo a PÁNICO, que es igual de informativo:
// lo que se afirma es que este camino NO puede tumbar el barrido.
//
// 🔧 EL ASERTO DE LA LECTURA INTENTADA se añadió el 2026-08-22 y es el que hace que
// esto sea un test. Sin él, «cerró una» y «quedó pending» pasaban también con el
// compositor SIN CABLEAR —con el noopSourceComposer, que no lee nada y no falla
// nunca—, así que el test no distinguía «el compositor falló y el cierre aguantó»
// de «no había compositor». Contando la lectura se sabe que el camino se recorrió
// de verdad hasta el punto donde revienta.
//
// MUTACIÓN 2 (compila, y solo la ve el aserto nuevo): en source_composer_test.go,
// en nuevoCompEntorno, quitar `flowruntime.WithSourceComposer(comp)` de la
// construcción del sink (`comp` se queda sin usar, así que hay que sustituirlo por
// `_ = comp`). El agregador vuelve al noop documentado y este test seguía verde
// antes del arreglo.
func TestT14_UnFalloAlLeerElHiloNoRevierteElCierre(t *testing.T) {
	ctx := context.Background()
	e := nuevoCompEntorno(t, 45*time.Second, nil)
	e.hilo.err = errors.New("KEK ausente del keyring")

	e.observa(ctx, "wa-1")
	e.clock.avanza(46 * time.Second)
	if n := e.sink.Sweep(ctx); n != 1 {
		t.Fatalf("un fallo al leer el hilo NO puede impedir el cierre; cierres = %d", n)
	}
	// SE INTENTÓ LEER. Es la diferencia entre «el compositor falló» y «no hay
	// compositor»: los dos dejan el job en pending con el sobre vacío.
	if e.hilo.lecturas != 1 {
		t.Fatalf("el compositor tenía que haber intentado leer el hilo UNA vez; lecturas=%d "+
			"(con 0, este test pasa también con el compositor sin cablear y no prueba nada)", e.hilo.lecturas)
	}
	if j := e.unicoJob(t); j.Status != intake.StatusPending {
		t.Fatalf("status = %q; tenía que quedar pending", j.Status)
	}
}

// TestT14_NoSobrescribeUnSobreYaEscrito: componer dos veces la misma ventana no
// pisa lo guardado. El guard vive en el store (`source_text_enc IS NULL`) y es lo
// que impide que el texto de una ventana caiga sobre el job de otra.
//
// MUTACIÓN (compila): en internal/intake/memory.go, dentro de PutSourceText,
// cambiar
//
//	if j == nil || len(j.SourceText.Enc) > 0 {
//
// por
//
//	if j == nil {
func TestT14_NoSobrescribeUnSobreYaEscrito(t *testing.T) {
	ctx := context.Background()
	e := nuevoCompEntorno(t, 45*time.Second, hiloConContexto(events.ThreadEntry{
		Seq: 1, Role: events.RoleSystem, Kind: events.KindSummary, Text: compContexto,
	}))

	e.observa(ctx, "wa-1")
	e.clock.avanza(46 * time.Second)
	e.sink.Sweep(ctx)
	primero := e.unicoJob(t).SourceText

	// Segundo intento sobre la MISMA ventana, con OTRO hilo debajo.
	e.hilo.entradas = []events.ThreadEntry{
		{Seq: 9, Role: events.RoleClient, Kind: events.KindMessage, Text: "esto es de otra ventana"},
	}
	escrito, err := e.jobs.PutSourceText(ctx, aggKey(), intake.SourceText{
		Enc: []byte("otro"), DEK: []byte("otra"), KEKID: "1",
	})
	if err != nil {
		t.Fatalf("un segundo intento no es un error, es un no-op: %v", err)
	}
	if escrito {
		t.Fatalf("se sobrescribió un sobre ya escrito")
	}
	if got := e.unicoJob(t).SourceText; string(got.Enc) != string(primero.Enc) {
		t.Fatalf("el sobre cambió bajo los pies del job")
	}
}

// ---------------------------------------------------------------------------
// ⏳ LO QUE ESTOS TESTS NO VERIFICAN, DICHO EN VEZ DE CALLADO
// ---------------------------------------------------------------------------
//
//   - NADA DE ESTO SE HA EJECUTADO. No hay Go en el entorno donde se escribió.
//   - EL SQL DE `ListThread` NO SE EJERCITA: el hilo llega por `hiloFalso`. Que la
//     subconsulta recorte por el principio, que el `payload` en claro se renderice
//     con Summary.Render y que el cuerpo cifrado se abra con la KEK de SU fila
//     —no con la current— solo lo puede fijar un test de integración contra
//     Postgres. Lo mismo vale para `putSourceTextSQL`: la elección de la ÚLTIMA
//     ventana `pending` de la tupla y el guard `source_text_enc IS NULL` están
//     replicados en el doble, y un doble que se desincronice del SQL convierte una
//     suite verde en una suite que no prueba nada.
//   - EL BARRIDO SQL DE «no queda literal en claro en ninguna tabla» (parte del
//     criterio de T1.4) EXIGE UNA BASE. No se ha corrido.
//   - QUE EL CÓDIGO TENGA UNA SOLA RAMA no lo prueba nada de aquí, y no se puede
//     probar ejecutando. Lo que se prueba es que las dos clases se COMPORTEN igual
//     (TestT14_LasDosClasesDeContexto_MISMAEntradaMISMOSALIDA) y que la tabla sea la
//     única fuente declarada (source_composer_internal_test.go). Una duplicación de
//     camino que no diverja pasa las dos; el día que diverja, no.
//   - QUE EL RÓTULO SIRVA DE ALGO EN EL PROMPT. Aquí se fija que el rótulo LLEGA,
//     entero y en su sitio. Que un LLM lo respete —que no extraiga ítems del bloque
//     de contexto— es de la Ola 2 y se mide con evaluaciones, no con asserts.
