package cart

// consulta_test.go — el TERCER escalón visto desde el módulo (Plan 044 · Ola 3.5 ·
// T3.5-2): cuándo el carrito pide ayuda, qué acepta de vuelta y qué NO manda nunca
// a preguntar.
//
// Lo que aquí NO se prueba es el mecanismo: que el engine resuelva, siembre y
// vuelva a llamar UNA sola vez es suyo y se prueba en engine/consulta_test.go, con
// el engine de verdad. Aquí el engine se imita en cuatro líneas (turno) porque lo
// que se mide es la conducta del carrito dentro de un turno completo.

import (
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
)

// resolutorDoble responde a una Consulta. nil ⇒ nadie responde, que es la
// degradación «sin_resolutor» — y es el estado REAL de producción hasta que el
// resolutor contra el LLM se cablee, así que es el default de los tests.
type resolutorDoble func(c modules.Consulta) modules.Veredicto

// turno ejecuta UN turno del carrito tal como lo ejecuta engine.Step: primera
// pasada y, si el módulo pide consulta, re-entrada con el veredicto sembrado.
//
// Es una COPIA de engine.reentrarConVeredicto reducida a lo imprescindible. Se
// copia en vez de usar el engine de verdad porque montar engine.Step aquí exigiría
// una definición de flujo, un Registry y una fuente de contenido para acabar
// midiendo lo mismo. Si el mecanismo del engine cambia, quien lo cambie tiene que
// mirar aquí: por eso esta nota, y por eso el helper es corto a propósito.
func turno(m Module, conv model.Conversation, input string, r resolutorDoble) modules.Result {
	res := m.Step(model.Node{}, conv, input)
	if res.Consulta == nil {
		return res
	}
	v := modules.Veredicto{Motivo: modules.MotivoSinResolutor}
	if r != nil {
		v = r(*res.Consulta)
	}
	conv.Vars = modules.ConVeredicto(conv.Vars, v)
	res = m.Step(model.Node{}, conv, input)
	res.Vars = modules.StripConsultaVeredicto(res.Vars)
	return res
}

// enQuantity deja el carrito en el nivel de la CANTIDAD, que es el caso que
// justifica la tarea entera: la cascada determinista no puede convertir «dos» en 2
// y ese nivel ni siquiera entra en ella.
func enQuantity(t *testing.T, m Module) map[string]any {
	t.Helper()
	vars := seededVars()
	var st cartState
	for _, tecla := range []string{"1", "1", "2"} {
		st, _, vars = drive(t, m, vars, tecla)
	}
	if st.Level != LevelQuantity {
		t.Fatalf("precondición: esperaba %s y el carrito está en %+v", LevelQuantity, st)
	}
	return vars
}

// --- Cuándo pregunta --------------------------------------------------------

// TestCantidadEnPalabrasSeConsulta es el caso que da valor a T3.5-2: «mejor dos»
// en el nivel de la cantidad. La cascada no puede resolverlo (es aritmética del
// lenguaje, no similitud ortográfica) y hoy caía en «Escribe una cantidad válida».
func TestCantidadEnPalabrasSeConsulta(t *testing.T) {
	m := New()
	vars := enQuantity(t, m)

	res := m.Step(model.Node{}, model.Conversation{Vars: vars}, "mejor dos")
	if res.Consulta == nil {
		t.Fatal("el nivel de la cantidad debe elevar consulta ante texto que no es un número")
	}
	if res.Consulta.Clase != modules.ClaseCantidad {
		t.Fatalf("Clase = %q, quiero %q", res.Consulta.Clase, modules.ClaseCantidad)
	}
	if res.Consulta.Nivel != LevelQuantity || res.Consulta.Texto != "mejor dos" {
		t.Fatalf("la petición no lleva el contexto: %+v", *res.Consulta)
	}
	if len(res.Consulta.Opciones) != 0 {
		t.Fatalf("la cantidad no ofrece catálogo, got %v", res.Consulta.Opciones)
	}
	// 🔴 La primera pasada NO muta nada: ni pantalla, ni efectos, ni estado. Si
	// declarara algo aquí, el engine lo perdería al descartar el Result — y la
	// segunda pasada volvería a declararlo, duplicándolo.
	if len(res.Outputs) != 0 || len(res.Effects) != 0 {
		t.Fatalf("la pasada que PIDE no puede producir nada: outputs=%v efectos=%v", res.Outputs, res.Effects)
	}

	// Y con un resolutor que sí sabe, el pedido avanza con la cantidad correcta.
	res = turno(m, model.Conversation{Vars: vars}, "mejor dos", func(modules.Consulta) modules.Veredicto {
		return modules.Veredicto{Codigo: "2"}
	})
	st := loadState(res.Vars)
	if len(st.Lines) != 1 || st.Lines[0].Qty != 2 {
		t.Fatalf("el veredicto «2» debía agregar una línea de 2 unidades, got %+v", st.Lines)
	}
}

// TestOpcionAbreviadaSeConsultaConSuCatalogo: «finalizar» en el resumen es el
// ejemplo que la propia cabecera del pre-resolutor daba por perdido (la opción se
// llama «Confirmar y finalizar» y los prefijos abrevian por la derecha). Aquí se
// eleva, y la petición lleva las opciones que el cliente tiene en pantalla.
func TestOpcionAbreviadaSeConsultaConSuCatalogo(t *testing.T) {
	m := New()
	vars := seededVars()
	storeState(vars, cartState{Level: LevelSummary, Started: true,
		Lines: []cartLine{{SKU: "AGUA", Label: "Agua", Qty: 1, UnitPrice: 1}}})

	res := m.Step(model.Node{}, model.Conversation{Vars: vars}, "finalizar")
	if res.Consulta == nil || res.Consulta.Clase != modules.ClaseOpcion {
		t.Fatalf("el resumen debía elevar una consulta de opción, got %+v", res.Consulta)
	}
	if len(res.Consulta.Opciones) == 0 {
		t.Fatal("una consulta de opción sin catálogo no se puede validar de vuelta")
	}
	// Las etiquetas viajan porque son lo que el cliente vio; los códigos son lo
	// ÚNICO que se aceptará de vuelta.
	var visto bool
	for _, o := range res.Consulta.Opciones {
		if o.Codigo == "1" && strings.Contains(o.Etiqueta, "Confirmar") {
			visto = true
		}
	}
	if !visto {
		t.Fatalf("la petición no ofrece la opción de confirmar: %v", res.Consulta.Opciones)
	}
}

// --- 🔴 Cuándo NO pregunta, que es la mitad importante -----------------------

// TestLosNivelesDeTextoLibreNoConsultanNUNCA es el mismo requisito de privacidad
// que fija la cascada (TestNivelesDeTextoLibreNuncaPasanPorLaCascada), y aquí pesa
// MÁS: la cascada comparaba el texto en memoria, una consulta lo MANDA FUERA, a un
// modelo. buyer_data recoge nombre, RUT y dirección —que se escriben CIFRADOS en
// intake_buyer_data—; mandarlos a interpretar sería deshacer el ADR-0017 por la
// puerta de atrás.
func TestLosNivelesDeTextoLibreNoConsultanNUNCA(t *testing.T) {
	m := New()
	linea := []cartLine{{SKU: "AGUA", Label: "Agua", Qty: 2, UnitPrice: 1}}
	for _, nivel := range []string{LevelItemNote, LevelOrderNote, LevelBuyerData} {
		t.Run(nivel, func(t *testing.T) {
			vars := seededVars()
			vars[VarBuyerFields] = []any{map[string]any{"key": "nombre", "label": "Nombre", "required": true}}
			storeState(vars, cartState{Level: nivel, Started: true, CatCode: "1", SKU: "AGUA", Lines: linea})
			// Textos elegidos a mala idea: un dato personal y algo que casaría una
			// opción de OTRO nivel.
			for _, in := range []string{"Juan Pérez, Av. Siempreviva 742", "cancelar pedido"} {
				res := m.Step(model.Node{}, model.Conversation{Vars: vars}, in)
				if res.Consulta != nil {
					t.Fatalf("%s elevó una consulta con %q: %+v", nivel, in, *res.Consulta)
				}
			}
		})
	}
}

// TestNumeroYCodigoNoConsultan: quien teclea un número navega EXACTAMENTE como
// siempre y no paga ni una consulta. Incluye el número que NO existe (paginación,
// opción inventada): sigue siendo un índice de pantalla, no una frase.
func TestNumeroYCodigoNoConsultan(t *testing.T) {
	m := New()
	for _, in := range []string{"1", "77", "0", "9"} {
		res := m.Step(model.Node{}, model.Conversation{Vars: seededVars()}, in)
		if res.Consulta != nil {
			t.Fatalf("la entrada numérica %q no puede elevar consulta: %+v", in, *res.Consulta)
		}
	}
}

// TestLaCascadaQueRESUELVENoConsulta: el orden es código → cascada → consulta.
// Preguntar es lo último, porque es lo único que cuesta tiempo del turno.
func TestLaCascadaQueRESUELVENoConsulta(t *testing.T) {
	m, e, vars := conCascada(t)
	res := m.Step(model.Node{}, model.Conversation{Vars: vars}, "postres")
	if res.Consulta != nil {
		t.Fatalf("la cascada resolvió: no había nada que preguntar (%+v)", *res.Consulta)
	}
	e.soloVio(t, "exact", LevelCategories)
}

// TestProsaLargaNoConsulta: el techo de tokens también acota la consulta, y aquí
// no es por coste de comparar sino por el turno de una persona esperando. Ver la
// nota de consultable.
func TestProsaLargaNoConsulta(t *testing.T) {
	m := New()
	larga := strings.TrimSpace(strings.Repeat("palabra ", maxTokensEntrada+1))
	res := m.Step(model.Node{}, model.Conversation{Vars: seededVars()}, larga)
	if res.Consulta != nil {
		t.Fatalf("una parrafada no puede colgar el turno esperando a un modelo: %+v", *res.Consulta)
	}
}

// --- Qué acepta de vuelta ---------------------------------------------------

// TestVeredictoInadmisibleSeDescarta es la aduana: el resolutor es un modelo y
// puede inventarse un código o devolver la frase del cliente tal cual. El módulo
// solo acepta lo que él mismo ofreció, y lo demás deja la entrada INTACTA — que es
// la pantalla de siempre. Es, además, la última barrera de privacidad: por esta
// puerta el texto del cliente no entra en el estado del carrito.
func TestVeredictoInadmisibleSeDescarta(t *testing.T) {
	m := New()
	vars := seededVars()
	inadmisibles := []string{"quiero algo rico", "CATEGORIA-QUE-NO-EXISTE", ""}
	for _, codigo := range inadmisibles {
		res := turno(m, model.Conversation{Vars: vars}, "quiero algo rico", func(modules.Consulta) modules.Veredicto {
			return modules.Veredicto{Codigo: codigo}
		})
		st := loadState(res.Vars)
		if st.Level != LevelCategories {
			t.Fatalf("el código inadmisible %q movió el carrito a %+v", codigo, st)
		}
		mustContain(t, res.Outputs, "Opción no válida")
	}
}

// TestDegradacionProduceLaPantallaDeSIEMPRE: sin resolutor —el estado real de
// producción mientras el resolutor del LLM no esté cableado— el turno acaba con el
// mismo reprompt que el día antes de esta tarea. La consulta no puede dejar a
// nadie mudo.
func TestDegradacionProduceLaPantallaDeSIEMPRE(t *testing.T) {
	m := New()
	res := turno(m, model.Conversation{Vars: seededVars()}, "quiero algo rico", nil)
	mustContain(t, res.Outputs, "Opción no válida", "Elige una categoría")
	if res.Consulta != nil {
		t.Fatal("la segunda pasada NO puede volver a pedir consulta")
	}
	if _, hay := res.Vars[modules.VarConsultaVeredicto]; hay {
		t.Fatal("el veredicto no puede sobrevivir al turno: el mensaje siguiente lo leería como «ya preguntaste»")
	}
	// Y el efecto de arranque se declara UNA vez, en la pasada que de verdad
	// corrió: si el módulo mutara antes de pedir, aquí habría dos.
	var arranques int
	for _, ef := range res.Effects {
		if ef.Name == EffectCartStarted {
			arranques++
		}
	}
	if arranques != 1 {
		t.Fatalf("cart_started se declaró %d veces en un turno, quiero 1", arranques)
	}
}

// TestConVeredictoSembradoElModuloNoVuelveAPEDIR es la disciplina del módulo que
// hace innecesario el corte del engine (que existe igual, porque este contrato lo
// pueden romper otros módulos): con la clave en Vars, pida lo que pida el nivel, el
// carrito NO eleva una segunda consulta. Se prueba con el peor caso: un veredicto
// que NO resolvió, que es justo cuando apetece volver a preguntar.
func TestConVeredictoSembradoElModuloNoVuelveAPEDIR(t *testing.T) {
	m := New()
	vars := modules.ConVeredicto(seededVars(), modules.Veredicto{Motivo: modules.MotivoNoConcluyente})
	res := m.Step(model.Node{}, model.Conversation{Vars: vars}, "quiero algo rico")
	if res.Consulta != nil {
		t.Fatalf("con el veredicto ya sembrado no se puede volver a pedir: %+v", *res.Consulta)
	}
	mustContain(t, res.Outputs, "Opción no válida")
}

// TestLaCascadaCuentaUNAVezPorMensaje: la segunda pasada NO re-ejecuta la cascada
// (se corta en el veredicto), así que la telemetría de T3.5-1 sigue midiendo
// mensajes y no pasadas. Si esto se rompiera, todos los contadores del
// pre-resolutor se duplicarían en silencio justo para las entradas más
// interesantes.
func TestLaCascadaCuentaUNAVezPorMensaje(t *testing.T) {
	m, e, vars := conCascada(t)
	turno(m, model.Conversation{Vars: vars}, "quiero algo rico", nil)
	e.soloVio(t, escalonNinguno, LevelCategories)
}
