package turnoacotado_test

// turnoacotado_test.go — EL RESOLUTOR, PROBADO SIN RED (Plan 044 · Ola 3.5 · T3.5-2).
//
// 🔴 NINGÚN TEST DE ESTE PAQUETE LLAMA A OLLAMA, y no es una limitación: es lo que
// hace que la suite se pueda correr en cualquier sitio y que un rojo signifique algo.
// Lo que se ejerce aquí es TODO lo que este paquete decide —qué prompt arma, qué
// acepta de vuelta y qué rechaza— con un doble del turnero en medio. Lo único que no
// se puede probar así es si el modelo acierta, y eso no se prueba con un test: se
// mide en campo, y el sitio donde se mira es el desenlace de la consulta.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/llmvia"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/turnoacotado"
)

// turneroFake es el doble del selector de vía: guarda lo que se le pidió y devuelve
// lo que se le diga.
type turneroFake struct {
	out   string
	err   error
	visto llmvia.TurnoRequest
	veces int
}

func (f *turneroFake) Turno(_ context.Context, _, _ string, t llmvia.TurnoRequest) (string, error) {
	f.veces++
	f.visto = t
	return f.out, f.err
}

func resolutor(t *testing.T, f *turneroFake) *turnoacotado.Resolver {
	t.Helper()
	r, err := turnoacotado.New(f)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

// menuDeCuatro es una consulta de ELECCIÓN con cuatro opciones cuyos códigos NO son
// números: es el caso real del carrito (categorías, artículos, «Volver»), y es lo que
// obliga a que el modelo conteste una POSICIÓN y la traducción se haga en Go.
func menuDeCuatro(texto string) modules.Consulta {
	return modules.Consulta{
		Clase: modules.ClaseOpcion, Nivel: "articles", Texto: texto,
		Opciones: []modules.OpcionConsulta{
			{Codigo: "burger-clasica", Etiqueta: "Hamburguesa clásica"},
			{Codigo: "burger-doble", Etiqueta: "Hamburguesa doble"},
			{Codigo: "papas", Etiqueta: "Papas fritas"},
			{Codigo: "volver", Etiqueta: "Volver"},
		},
	}
}

func cantidad(texto string) modules.Consulta {
	return modules.Consulta{Clase: modules.ClaseCantidad, Nivel: "quantity", Texto: texto}
}

// ------------------------------------------------------- lo que el modelo devuelve

// TestVeredicto_LaTablaEntera recorre de una vez lo que este resolutor acepta y lo
// que rechaza. Cada fila es un modo de fallo distinto y ninguna es teórica: las tres
// primeras son el camino feliz medido, y las demás son cosas que el modelo hizo de
// verdad durante la medición del 2026-08-26.
func TestVeredicto_LaTablaEntera(t *testing.T) {
	t.Parallel()
	casos := []struct {
		nombre   string
		consulta modules.Consulta
		crudo    string
		codigo   string // "" ⇒ NO resuelto
		porQue   string
	}{
		{"opción por posición", menuDeCuatro("quiero la doble"), `{"usable":true,"value":2,"reason":"ok"}`,
			"burger-doble", "el modelo elige la POSICIÓN 2 y Go la traduce al código del catálogo"},
		{"opción «Volver» es una opción más", menuDeCuatro("mejor volvé atrás"), `{"usable":true,"value":4,"reason":"ok"}`,
			"volver", "los rótulos fijos del menú no son un caso aparte"},
		{"cantidad en palabras", cantidad("mejor dos"), `{"usable":true,"value":2,"reason":"ok"}`,
			"2", "ES el caso que justifica la tarea: la cascada ortográfica no sabe hacer esto"},

		{"🔴 opción FUERA DE RANGO", menuDeCuatro("quiero 5"), `{"usable":true,"value":5,"reason":"ok"}`,
			"", "FALLO CONOCIDO Y MEDIDO del modelo: con 4 opciones contesta 5 tan tranquilo. " +
				"NO se arregla con el prompt (se intentó): el rango lo valida Go"},
		{"opción 0", menuDeCuatro("ninguna"), `{"usable":true,"value":0,"reason":"ok"}`,
			"", "no hay opción cero; sin este corte se leería como la última del slice al revés"},
		{"opción negativa", menuDeCuatro("?"), `{"usable":true,"value":-1,"reason":"ok"}`,
			"", "un índice negativo entraría en pánico al indexar"},
		{"cantidad cero", cantidad("ninguna"), `{"usable":true,"value":0,"reason":"ok"}`,
			"", "stepQuantity exige >= 1; gastar un turno en un 0 es gastarlo para nada"},
		{"cantidad absurda", cantidad("un millón"), `{"usable":true,"value":1000000,"reason":"ok"}`,
			"", "cuatro dígitos es más de lo que nadie pide por WhatsApp"},

		{"el modelo dice que no", menuDeCuatro("¿hacen envíos?"), `{"usable":false,"value":null,"reason":"otra_pregunta"}`,
			"", "«no supe» es una respuesta legítima y la más importante de las tres"},
		{"usable sin value", menuDeCuatro("la doble"), `{"usable":true}`,
			"", "clave AUSENTE: el modelo se contradice. ⚠️ Este caso NO distingue el `*int` " +
				"del `int` —el rango rechaza el 0 igual, medido por mutación—; está por el " +
				"desenlace, no por el decodificador"},
		{"value null con usable true", menuDeCuatro("la doble"), `{"usable":true,"value":null,"reason":"ok"}`,
			"", "el modelo se contradice; no se adivina cuál de las dos claves mentía"},
		{"prosa en vez de JSON", menuDeCuatro("la doble"), `Creo que quiere la hamburguesa doble.`,
			"", "no se «interpreta con cariño» ni se busca un número dentro de la frase"},
		{"JSON truncado", menuDeCuatro("la doble"), `{"usable":true,"value":2`,
			"", "salida cortada por el techo de tokens: es calidad, no una avería de la vía"},
		{"valla de Markdown", menuDeCuatro("la doble"), "```json\n{\"usable\":true,\"value\":3,\"reason\":\"ok\"}\n```",
			"papas", "el ExtractJSON compartido ya sabe de vallas; no se reimplementa aquí"},
		{"🔴 el reason miente y da igual", menuDeCuatro("quiero papas"), `{"usable":true,"value":3,"reason":"no_entendido"}`,
			"papas", "MEDIDO: el reason sale mal a menudo mientras usable/value son correctos. " +
				"Es telemetría, no la decisión: colgar lógica de él sería decidir con el campo peor calibrado"},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			t.Parallel()
			r := resolutor(t, &turneroFake{out: c.crudo})
			v, err := r.ResolverConsulta(context.Background(), "tenant-1", "s-1", c.consulta)
			if err != nil {
				t.Fatalf("una respuesta mala del modelo NO es un error de la vía: %v", err)
			}
			if v.Codigo != c.codigo {
				t.Fatalf("Codigo = %q, quiero %q — %s", v.Codigo, c.codigo, c.porQue)
			}
			if c.codigo == "" && v.Motivo != modules.MotivoNoConcluyente {
				t.Fatalf("Motivo = %q, quiero %q: el módulo tiene que poder distinguir «no supe» "+
					"de «se rompió»", v.Motivo, modules.MotivoNoConcluyente)
			}
		})
	}
}

// TestVeredicto_NoDevuelveNuncaTextoDelCliente es la última barrera de privacidad
// vista desde este lado: aunque el modelo devuelva la frase del cliente en `value`
// (no puede, el esquema fuerza integer, pero el esquema lo aplica OTRA máquina), lo
// único que sale de aquí es un código del catálogo o unos dígitos.
//
// Importa porque el Veredicto se siembra en Vars y Vars se serializa ENTERO al JSONB
// de flow_state, en claro y para siempre. Es la misma fuga que cerró StripIntentSignal.
func TestVeredicto_NoDevuelveNuncaTextoDelCliente(t *testing.T) {
	t.Parallel()
	// gosec ve «RUT 12.345…» y grita G101; es texto de una clienta, no una credencial.
	const fraseDeLaClienta = "quiero la doble para Juan Pérez, RUT 12.345.678-9" //nolint:gosec
	r := resolutor(t, &turneroFake{out: `{"usable":true,"value":"` + fraseDeLaClienta + `","reason":"ok"}`})

	v, err := r.ResolverConsulta(context.Background(), "tenant-1", "s-1", menuDeCuatro(fraseDeLaClienta))
	if err != nil {
		t.Fatalf("ResolverConsulta: %v", err)
	}
	if v.Codigo != "" {
		t.Fatalf("Codigo = %q: un value que no es un entero del rango NO se aplica", v.Codigo)
	}
	if strings.Contains(v.Codigo, "Juan") || strings.Contains(string(v.Motivo), "Juan") {
		t.Fatal("el texto del cliente salió en el veredicto y acabaría EN CLARO en flow_state")
	}
}

// ------------------------------------------------------------------- los prompts

// TestPrompt_CadaClaseLLevaElSuyo custodia el hallazgo de la medición: un esquema
// único da 9/12 y separado por tipo de pregunta da 11/12. Mezclarlos confunde al
// modelo porque `value` significa cosas OPUESTAS en cada clase (en una ES la
// cantidad, en la otra es el número de opción).
//
// 🔬 MUTACIÓN: hacer que prompt() devuelva el mismo texto para las dos clases ⇒ rojo.
func TestPrompt_CadaClaseLLevaElSuyo(t *testing.T) {
	t.Parallel()
	fOp, fCant := &turneroFake{out: "{}"}, &turneroFake{out: "{}"}
	if _, err := resolutor(t, fOp).ResolverConsulta(context.Background(), "t", "s", menuDeCuatro("la doble")); err != nil {
		t.Fatalf("opción: %v", err)
	}
	if _, err := resolutor(t, fCant).ResolverConsulta(context.Background(), "t", "s", cantidad("mejor dos")); err != nil {
		t.Fatalf("cantidad: %v", err)
	}
	if fOp.visto.Prompt == fCant.visto.Prompt {
		t.Fatal("las dos clases mandan el MISMO prompt: eso es el diseño que midió 9/12")
	}
	if fOp.visto.Formato == fCant.visto.Formato {
		t.Fatal("las dos clases mandan el MISMO esquema; la descripción de `value` es distinta en cada una")
	}
	for _, f := range []string{fOp.visto.Formato, fCant.visto.Formato} {
		if f == "" || f[0] != '{' {
			t.Fatalf("formato = %q: tiene que ser un JSON Schema serializado, no la cadena \"json\" "+
				"(el Edge los distingue por el primer byte)", f)
		}
		for _, motivo := range []string{"ok", "cambio_de_intencion", "fuera_de_rango", "no_entendido", "otra_pregunta"} {
			if !strings.Contains(f, `"`+motivo+`"`) {
				t.Errorf("el esquema no cierra el enum de `reason` sobre %q", motivo)
			}
		}
	}
}

// TestPrompt_LaListaVaNUMERADAYSINLosCodigos: al modelo se le enseña lo que el
// CLIENTE tiene delante (una lista numerada de rótulos), nunca los códigos del
// catálogo. Dos razones y las dos duelen si se rompe: (1) un código como
// `burger-doble` es vocabulario nuestro que el modelo no entiende y le da una forma
// más de inventarse una cadena; (2) la traducción posición → código es lo que hace
// que el carrito no tenga que fiarse de lo que el modelo escriba.
//
// Y el few-shot TIENE que estar: en un modelo de 1-2B los ejemplos pesan más que las
// instrucciones (ADR-0020 §6), y sin ellos «mejor dos» → 2 no sale.
func TestPrompt_LaListaVaNUMERADAYSINLosCodigos(t *testing.T) {
	t.Parallel()
	f := &turneroFake{out: "{}"}
	if _, err := resolutor(t, f).ResolverConsulta(context.Background(), "t", "s",
		menuDeCuatro("la doble")); err != nil {
		t.Fatalf("ResolverConsulta: %v", err)
	}
	p := f.visto.Prompt
	for _, quiero := range []string{"1. Hamburguesa clásica", "2. Hamburguesa doble", "4. Volver"} {
		if !strings.Contains(p, quiero) {
			t.Errorf("el prompt no lleva %q: el modelo elige por POSICIÓN de la lista que ve", quiero)
		}
	}
	for _, prohibido := range []string{"burger-doble", "burger-clasica", "volver\"", "volver\n"} {
		if strings.Contains(p, prohibido) {
			t.Errorf("el prompt lleva el CÓDIGO %q: al modelo se le enseña lo que el cliente ve, "+
				"no el vocabulario interno del catálogo", prohibido)
		}
	}
	if !strings.Contains(p, "la doble") {
		t.Error("el prompt no lleva la respuesta del cliente, que es lo único que hay que interpretar")
	}
	// El few-shot, por su ejemplo más característico: sin él el mapeo por nombre falla.
	if !strings.Contains(p, "quiero el Helado") {
		t.Error("falta el few-shot medido: en un modelo de 1-2B pesa MÁS que las instrucciones (ADR-0020 §6)")
	}
}

// TestPrompt_ElPrefijoEsESTABLEEntreDosTurnos: todo lo que no depende del mensaje
// —instrucciones y ejemplos— va DELANTE y byte a byte igual, y lo único variable va
// al final. No es estética: el Ollama del Edge cachea el PREFIJO del prompt, y un
// prefijo que cambia en cada turno hace que cada turno pague prefill en frío — los
// 18 s medidos que el plazo de 12 corta. Sería una avería silenciosa: todo
// «funciona», solo que siempre por el camino lento.
//
// 🔬 MUTACIÓN: mover el caso del cliente al principio del prompt ⇒ rojo.
func TestPrompt_ElPrefijoEsESTABLEEntreDosTurnos(t *testing.T) {
	t.Parallel()
	f1, f2 := &turneroFake{out: "{}"}, &turneroFake{out: "{}"}
	if _, err := resolutor(t, f1).ResolverConsulta(context.Background(), "t", "s", cantidad("mejor dos")); err != nil {
		t.Fatalf("primer turno: %v", err)
	}
	if _, err := resolutor(t, f2).ResolverConsulta(context.Background(), "t", "s", cantidad("ponme una docena")); err != nil {
		t.Fatalf("segundo turno: %v", err)
	}
	comun := prefijoComun(f1.visto.Prompt, f2.visto.Prompt)
	if comun < len(f1.visto.Prompt)-120 {
		t.Fatalf("el prefijo común entre dos turnos son %d bytes de %d: lo que cambia tiene que ser "+
			"SOLO la cola con el mensaje del cliente, o cada turno paga prefill en frío",
			comun, len(f1.visto.Prompt))
	}
}

func prefijoComun(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

// ------------------------------------------------------------ errores y ausencias

// TestResolverConsulta_LosDosErroresQueSIloSon: se reserva el error para lo que
// dispara el aviso al dueño y la métrica de caída a Nivel A. Confundir esto con un
// veredicto no concluyente mandaría a una persona a revisar un equipo que está bien.
func TestResolverConsulta_LosDosErroresQueSIloSon(t *testing.T) {
	t.Parallel()
	t.Run("la vía falló", func(t *testing.T) {
		t.Parallel()
		fallo := errors.New("ollama caído")
		_, err := resolutor(t, &turneroFake{err: fallo}).ResolverConsulta(
			context.Background(), "t", "s", menuDeCuatro("la doble"))
		if !errors.Is(err, fallo) {
			t.Fatalf("err = %v: el fallo de la vía se propaga SIN envolver, para que ni errors.Is "+
				"ni el duck-typing del motivo se pierdan por el camino", err)
		}
	})
	t.Run("clase que nadie enseñó a preguntar", func(t *testing.T) {
		t.Parallel()
		f := &turneroFake{out: "{}"}
		_, err := resolutor(t, f).ResolverConsulta(context.Background(), "t", "s",
			modules.Consulta{Clase: "inventada", Nivel: "x", Texto: "hola"})
		if !errors.Is(err, turnoacotado.ErrClaseDesconocida) {
			t.Fatalf("err = %v, quiero ErrClaseDesconocida: una clase nueva que nadie enseñó a "+
				"preguntar tiene que doler en el primer turno, no degradar en silencio", err)
		}
		if f.veces != 0 {
			t.Error("se gastó una inferencia en una consulta que no se sabe formular")
		}
	})
}

// TestResolverConsulta_LaViaAPINoEsUnaAVERIA: para un tenant en vía API este escalón
// NO EXISTE, y eso no es un fallo de nadie. Se devuelve el MOTIVO que lo dice en vez
// de un error, para que no escriba un aviso de degradación al dueño ni cuente como
// caída a Nivel A. Su carrito sigue funcionando con el reprompt de siempre.
func TestResolverConsulta_LaViaAPINoEsUnaAVERIA(t *testing.T) {
	t.Parallel()
	r := resolutor(t, &turneroFake{err: llmvia.ErrViaSinTurnoAcotado})
	v, err := r.ResolverConsulta(context.Background(), "t", "s", menuDeCuatro("la doble"))
	if err != nil {
		t.Fatalf("err = %v: una vía que no sirve este escalón no es una avería", err)
	}
	if v.Resuelto() || v.Motivo != modules.MotivoSinResolutor {
		t.Fatalf("veredicto = %+v, quiero no-resuelto con motivo %q", v, modules.MotivoSinResolutor)
	}
}

// TestResolverConsulta_SinOpcionesNoSeGastaUnaInferencia: preguntar «elige una de
// estas» sin lista es pagar una plaza del Ollama del cliente por una respuesta que no
// se podría usar. El carrito ya no pregunta en ese caso; esto es la red de abajo.
func TestResolverConsulta_SinOpcionesNoSeGastaUnaInferencia(t *testing.T) {
	t.Parallel()
	f := &turneroFake{out: `{"usable":true,"value":1,"reason":"ok"}`}
	v, err := resolutor(t, f).ResolverConsulta(context.Background(), "t", "s",
		modules.Consulta{Clase: modules.ClaseOpcion, Nivel: "articles", Texto: "la doble"})
	if err != nil || v.Resuelto() {
		t.Fatalf("veredicto = %+v, err = %v: sin catálogo no hay nada que elegir", v, err)
	}
	if f.veces != 0 {
		t.Fatal("se llamó al modelo para elegir entre cero opciones")
	}
}

// TestNew_SinTurneroFallaAlARRANCAR: un resolutor sin con quién preguntar es un bug
// del cableado, y tiene que verse al arrancar y no en el primer turno de un cliente.
func TestNew_SinTurneroFallaAlARRANCAR(t *testing.T) {
	t.Parallel()
	if _, err := turnoacotado.New(nil); !errors.Is(err, turnoacotado.ErrSinTurnero) {
		t.Fatalf("err = %v, quiero ErrSinTurnero", err)
	}
}
