package engine_test

// consulta_test.go — EL MECANISMO, probado donde vive (Plan 044 · Ola 3.5 · T3.5-2).
//
// 🔴 POR QUÉ ESTA BATERÍA ESTÁ EN engine/ Y NO EN modules/cart/. De las 78 llamadas
// .Step( que hay en los tests de este repo, ~64 llaman al MÓDULO directamente,
// saltándose el engine: ninguna de ellas ejerce el re-entry, porque el re-entry no
// está en el módulo. Un mecanismo probado solo desde el módulo sería un mecanismo
// sin probar — y este cuelga de él un turno de WhatsApp entero.
//
// Se cubren los cinco caminos: resuelve sin preguntar, resuelve tras la
// re-entrada, el resolutor falla, no hay resolutor, y el módulo que pide DOS veces
// (que no puede hacer bucle).

import (
	"context"
	"errors"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
)

const (
	tipoPreguntón   = "pregunton"
	varDeLaSesión   = "algo_del_estado"
	efectoPrimera   = "efecto_de_la_PRIMERA_pasada"
	efectoSegunda   = "efecto_de_la_segunda_pasada"
	pantallaPrimera = "pantalla de la PRIMERA pasada"
)

// preguntón es un módulo de prueba que eleva una consulta mientras no vea un
// veredicto en sus Vars. Su primera pasada declara ADEMÁS una pantalla y un efecto
// —cosa que un módulo real no debe hacer— justo para poder comprobar que el engine
// los DESCARTA: si algún día se colaran, el efecto se emitiría dos veces.
type preguntón struct {
	pasadas *[]string // el input que vio cada pasada, en orden
	tozudo  bool      // pide consulta SIEMPRE, también en la segunda pasada (el bug)
}

func (preguntón) Type() string                                  { return tipoPreguntón }
func (preguntón) WaitsForInput() bool                           { return true }
func (preguntón) ProducesDurableContent() bool                  { return false }
func (preguntón) Render(_ model.Node, _ model.Content) []string { return []string{"elige"} }

func (p preguntón) Step(_ model.Node, conv model.Conversation, input string) modules.Result {
	*p.pasadas = append(*p.pasadas, input)
	v, hayVeredicto := modules.VeredictoDe(conv.Vars)
	if !hayVeredicto || p.tozudo {
		return modules.Result{
			Vars:    conv.Vars,
			Outputs: []string{pantallaPrimera},
			Effects: []modules.Effect{{Kind: "event", Name: efectoPrimera}},
			Consulta: &modules.Consulta{
				Clase: modules.ClaseOpcion, Nivel: "nivel-de-prueba", Texto: input,
				Opciones: []modules.OpcionConsulta{{Codigo: "1", Etiqueta: "Confirmar y finalizar"}},
			},
		}
	}
	if v.Resuelto() {
		return modules.Result{Vars: conv.Vars, Outputs: []string{"resuelto:" + v.Codigo},
			Effects: []modules.Effect{{Kind: "event", Name: efectoSegunda}}}
	}
	return modules.Result{Vars: conv.Vars, Outputs: []string{"degradado:" + string(v.Motivo)}}
}

// resolutorDoble responde lo que le digan, o falla. Anota además la IDENTIDAD con
// la que se le preguntó: el resolutor real es una inferencia y una inferencia sin
// tenant no existe en este ecosistema (INV-7/INV-8), así que si el engine dejara de
// pasarla el adaptador se quedaría sin poder elegir la vía — y eso, sin este
// registro, sería un fallo que solo se vería en campo.
type resolutorDoble struct {
	veredicto modules.Veredicto
	err       error
	vistas    *[]modules.Consulta
	quien     *[2]string // el (tenantID, sessionID) de la ÚLTIMA llamada
}

func (r resolutorDoble) ResolverConsulta(_ context.Context, tenantID, sessionID string, c modules.Consulta) (modules.Veredicto, error) {
	*r.vistas = append(*r.vistas, c)
	if r.quien != nil {
		*r.quien = [2]string{tenantID, sessionID}
	}
	return r.veredicto, r.err
}

// observados recoge lo que el engine publica por su observador.
type observados [][3]string

func (o *observados) rec(clase, nivel, desenlace string) {
	*o = append(*o, [3]string{clase, nivel, desenlace})
}

func flujoPreguntón() model.Flow {
	return model.Flow{FlowID: "f", Version: 1, Initial: "n1",
		Nodes: map[string]model.Node{"n1": {Type: tipoPreguntón}}}
}

// monta arma el engine con el módulo de prueba y devuelve lo necesario para
// afirmar sobre las dos pasadas.
func monta(t *testing.T, tozudo bool, opts ...engine.Option) (*engine.Engine, *[]string) {
	t.Helper()
	pasadas := &[]string{}
	reg := modules.NewRegistry()
	reg.Register(preguntón{pasadas: pasadas, tozudo: tozudo})
	return engine.New(reg, opts...), pasadas
}

func conversación() model.Conversation {
	return model.Conversation{CurrentNode: "n1", Vars: map[string]any{varDeLaSesión: "intacto"}}
}

// soloElEfectoDeLaSegunda afirma que el Result de la primera pasada se descartó
// ENTERO: si sus efectos sobrevivieran, el módulo declararía cart_started (o
// cualquier otro) DOS veces por un solo mensaje del cliente.
func soloElEfectoDeLaSegunda(t *testing.T, effs []modules.Effect) {
	t.Helper()
	for _, ef := range effs {
		if ef.Name == efectoPrimera {
			t.Fatal("el efecto de la primera pasada NO puede sobrevivir: se declararía dos veces por un mensaje")
		}
	}
	if len(effs) != 1 || effs[0].Name != efectoSegunda {
		t.Fatalf("efectos = %v, quiero solo %q", effs, efectoSegunda)
	}
}

// sinVeredictoEnVars afirma que la clave del veredicto no sobrevive al turno: si
// lo hiciera, se persistiría en el JSONB de flow_state y en el mensaje SIGUIENTE
// el módulo la leería como «ya preguntaste», dejando de pedir para siempre.
func sinVeredictoEnVars(t *testing.T, vars map[string]any) {
	t.Helper()
	if _, hay := vars[modules.VarConsultaVeredicto]; hay {
		t.Fatal("el veredicto quedó vivo en Vars y se persistiría en flow_state")
	}
}

// --- 1 · El módulo no pregunta: nada cambia ---------------------------------

// TestSinConsultaElEngineNoTocaNada: el 99 % de los turnos. Una sola pasada, el
// observador mudo y el resolutor sin usar. Es la regresión cero del mecanismo.
func TestSinConsultaElEngineNoTocaNada(t *testing.T) {
	var vistos observados
	vistas := &[]modules.Consulta{}
	reg := modules.NewRegistry()
	reg.Register(fakeSurvey{}) // módulo de siempre: nunca rellena Result.Consulta
	e := engine.New(reg,
		engine.WithConsultaResolver(resolutorDoble{veredicto: modules.Veredicto{Codigo: "1"}, vistas: vistas}),
		engine.WithConsultaObserver(vistos.rec))

	st := model.Conversation{CurrentNode: "q1"}
	if _, _, _, err := e.Step(context.Background(), flowSurvey(), st, engine.Input{Text: "1"}); err != nil {
		t.Fatalf("Step: %v", err)
	}
	if len(*vistas) != 0 {
		t.Fatalf("se consultó sin que nadie preguntara: %v", *vistas)
	}
	if len(vistos) != 0 {
		t.Fatalf("el observador debe estar mudo cuando no hay consulta: %v", vistos)
	}
}

// --- 2 · Resuelve tras la re-entrada ----------------------------------------

// TestReentradaResuelveYDescartaLaPrimeraPasada es el camino feliz completo, y las
// cuatro cosas que afirma son cuatro defectos distintos:
//
//	pasadas == 2          → se re-entró exactamente una vez.
//	la 2.ª ve el MISMO texto del cliente, no el código: el módulo es quien traduce.
//	Outputs/Effects son SOLO los de la segunda → el Result de la primera se
//	                      descartó ENTERO; si no, el efecto saldría DUPLICADO.
//	Vars sin la clave del veredicto → no sobrevive al turno (si sobreviviera, el
//	                      mensaje siguiente lo leería como «ya preguntaste»).
func TestReentradaResuelveYDescartaLaPrimeraPasada(t *testing.T) {
	var vistos observados
	vistas := &[]modules.Consulta{}
	e, pasadas := monta(t, false,
		engine.WithConsultaResolver(resolutorDoble{veredicto: modules.Veredicto{Codigo: "1"}, vistas: vistas}),
		engine.WithConsultaObserver(vistos.rec))

	st, outs, effs, err := e.Step(context.Background(), flujoPreguntón(), conversación(), engine.Input{Text: "mejor la primera"})
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	if len(*pasadas) != 2 {
		t.Fatalf("pasadas = %v, quiero exactamente 2", *pasadas)
	}
	if (*pasadas)[1] != "mejor la primera" {
		t.Fatalf("la 2.ª pasada debe ver el texto ORIGINAL del cliente, vio %q", (*pasadas)[1])
	}
	if len(outs) != 1 || outs[0].Text != "resuelto:1" {
		t.Fatalf("outputs = %v, quiero solo los de la segunda pasada", outs)
	}
	soloElEfectoDeLaSegunda(t, effs)
	sinVeredictoEnVars(t, st.Vars)
	if st.Vars[varDeLaSesión] != "intacto" {
		t.Fatalf("la re-entrada perdió las Vars originales: %v", st.Vars)
	}
	// La consulta llegó con su contexto, y el observador vio UN desenlace acotado.
	if len(*vistas) != 1 || (*vistas)[0].Texto != "mejor la primera" {
		t.Fatalf("el resolutor recibió %v", *vistas)
	}
	if len(vistos) != 1 || vistos[0] != [3]string{"opcion", "nivel-de-prueba", engine.DesenlaceResuelto} {
		t.Fatalf("observado = %v", vistos)
	}
}

// --- 3 y 4 · Degradación: el fallo y la ausencia ----------------------------

// TestDegradacionesReentranIgualYSeVEN: sin resolutor, con error o con un
// no-concluyente, el módulo recibe un veredicto EXPLÍCITO y se le vuelve a llamar
// igual. Que no haya LLM no puede dejar a una clienta sin respuesta.
//
// Y las tres dejan rastro por el observador: esa es la diferencia con el
// best-effort mudo del content (engine.go), que degrada sin log, sin métrica y sin
// distinguirse de un turno normal. Aquí una degradación es VISIBLE desde fuera.
func TestDegradacionesReentranIgualYSeVEN(t *testing.T) {
	casos := []struct {
		nombre    string
		opts      []engine.Option
		pantalla  string
		desenlace string
	}{
		{"sin resolutor", nil, "degradado:" + string(modules.MotivoSinResolutor), engine.DesenlaceSinResolutor},
		{"el resolutor falla",
			[]engine.Option{engine.WithConsultaResolver(resolutorDoble{err: errors.New("timeout"), vistas: &[]modules.Consulta{}})},
			"degradado:" + string(modules.MotivoFallo), engine.DesenlaceFallo},
		{"el resolutor no sabe",
			[]engine.Option{engine.WithConsultaResolver(resolutorDoble{vistas: &[]modules.Consulta{}})},
			"degradado:" + string(modules.MotivoNoConcluyente), engine.DesenlaceNoConcluyente},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			var vistos observados
			e, pasadas := monta(t, false, append(c.opts, engine.WithConsultaObserver(vistos.rec))...)

			st, outs, _, err := e.Step(context.Background(), flujoPreguntón(), conversación(), engine.Input{Text: "algo raro"})
			if err != nil {
				t.Fatalf("una degradación NO puede devolver error: %v", err)
			}
			if len(*pasadas) != 2 {
				t.Fatalf("pasadas = %v, quiero 2 (se re-entra IGUAL)", *pasadas)
			}
			if len(outs) != 1 || outs[0].Text != c.pantalla {
				t.Fatalf("outputs = %v, quiero %q", outs, c.pantalla)
			}
			sinVeredictoEnVars(t, st.Vars)
			if len(vistos) != 1 || vistos[0][2] != c.desenlace {
				t.Fatalf("observado = %v, quiero el desenlace %q", vistos, c.desenlace)
			}
		})
	}
}

// --- 5 · El módulo que pide DOS veces ---------------------------------------

// TestElModuloQuePideDosVecesNoHaceBUCLE: un módulo con un bug pide otra vez en la
// segunda pasada. El engine NO obedece —EXACTAMENTE UNA re-entrada— y deja rastro.
// Aquí no hay una pantalla mala en juego: hay un turno de WhatsApp colgado y una
// persona mirando el teléfono.
//
// Se afirma también que el Result de la segunda pasada SÍ sale (lo que el módulo
// produjo se entrega; lo único que se ignora es la petición): el engine no conoce
// el dominio y no tiene con qué sustituirla.
func TestElModuloQuePideDosVecesNoHaceBUCLE(t *testing.T) {
	var vistos observados
	e, pasadas := monta(t, true,
		engine.WithConsultaResolver(resolutorDoble{veredicto: modules.Veredicto{Codigo: "1"}, vistas: &[]modules.Consulta{}}),
		engine.WithConsultaObserver(vistos.rec))

	_, outs, _, err := e.Step(context.Background(), flujoPreguntón(), conversación(), engine.Input{Text: "da igual"})
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	if len(*pasadas) != 2 {
		t.Fatalf("pasadas = %d, quiero 2: una tercera llamada ya es el bucle", len(*pasadas))
	}
	if len(outs) != 1 || outs[0].Text != pantallaPrimera {
		t.Fatalf("outputs = %v: lo que el módulo produjo en la 2.ª pasada se entrega igual", outs)
	}
	if len(vistos) != 2 || vistos[1][2] != engine.DesenlaceBucle {
		t.Fatalf("observado = %v, quiero que el último desenlace sea %q", vistos, engine.DesenlaceBucle)
	}
}

// --- 6 · El bucle, mirado por donde CUESTA DINERO ---------------------------

// TestElModuloTozudoNoGastaUnaSegundaInferencia cubre el hueco que el test 5 deja.
//
// El test 5 comprueba que la SEGUNDA pasada no dispara una tercera y que el
// desenlace `bucle` se observa. Lo que NADIE comprobaba es lo que ese bucle
// costaría: que el RESOLUTOR no se llame otra vez. Y no es un detalle de eficiencia
// — es un segundo viaje al Ollama del cliente DENTRO del mismo turno de WhatsApp:
// 12 s más de plazo, una segunda plaza ocupada, y una persona mirando el teléfono
// mientras tanto. Un mecanismo que se defiende del bucle contando las pasadas del
// módulo pero no las llamadas al modelo estaría defendiéndose de la mitad barata.
//
// 🔬 MUTACIÓN: convertir el `if res.Consulta != nil` de engine.Step en un `for`, o
// resolver la consulta de la segunda pasada «ya que la tenemos» ⇒ rojo aquí y
// verde en el test 5.
func TestElModuloTozudoNoGastaUnaSegundaInferencia(t *testing.T) {
	var vistos observados
	vistas := &[]modules.Consulta{}
	e, pasadas := monta(t, true,
		engine.WithConsultaResolver(resolutorDoble{veredicto: modules.Veredicto{Codigo: "1"}, vistas: vistas}),
		engine.WithConsultaObserver(vistos.rec))

	if _, _, _, err := e.Step(context.Background(), flujoPreguntón(), conversación(),
		engine.Input{Text: "da igual"}); err != nil {
		t.Fatalf("Step: %v", err)
	}
	if len(*vistas) != 1 {
		t.Fatalf("el resolutor se llamó %d veces, quiero exactamente 1: la petición de la "+
			"segunda pasada se IGNORA, y obedecerla costaría otra inferencia entera "+
			"(otros 12 s) dentro del mismo turno de WhatsApp", len(*vistas))
	}
	if len(*pasadas) != 2 {
		t.Fatalf("pasadas = %d, quiero 2", len(*pasadas))
	}
	// Y el bucle se VE: es lo único que separa un módulo con un bug de un turno
	// normal, porque el engine lo tapa sin devolver error.
	if len(vistos) != 2 || vistos[1][2] != engine.DesenlaceBucle {
		t.Fatalf("observado = %v, quiero que el último desenlace sea %q", vistos, engine.DesenlaceBucle)
	}
}

// --- 7 · La identidad con la que se pregunta --------------------------------

// TestLaConsultaSePreguntaConElTenantYLaSesionDeLaConversacion: el módulo es puro y
// no conoce ni el tenant ni la sesión, así que los pone el ENGINE desde la
// Conversation que ya tiene en la mano.
//
// Las dos mitades tienen consecuencias distintas si se pierden, y ninguna da error:
//
//   - SIN TENANT no hay vía que resolver (una fila por tenant, REQ-33) y el
//     resolutor real falla en todas las consultas de todos los clientes.
//   - SIN SESIÓN la pregunta sale por el Edge que decida el orden alfabético del
//     Gateway, no por el que atendió a esta persona: otra máquina, otra caché de
//     prefijo, y un turno que paga prefill en frío (18 s medidos) donde debía pagar
//     4–8. Es EXACTAMENTE el defecto que costó la tarea T1.7-8 en el pipeline, y
//     este test existe para que no se repita por el camino del carrito.
//
// 🔬 MUTACIÓN: pasar "" en cualquiera de los dos argumentos de resolverConsulta ⇒ rojo.
func TestLaConsultaSePreguntaConElTenantYLaSesionDeLaConversacion(t *testing.T) {
	quien := &[2]string{}
	e, _ := monta(t, false,
		engine.WithConsultaResolver(resolutorDoble{
			veredicto: modules.Veredicto{Codigo: "1"}, vistas: &[]modules.Consulta{}, quien: quien}))

	conv := conversación()
	conv.TenantID = "tenant-de-la-conversacion"
	conv.SessionID = "sesion-que-recibio-el-mensaje"
	if _, _, _, err := e.Step(context.Background(), flujoPreguntón(), conv,
		engine.Input{Text: "mejor la primera"}); err != nil {
		t.Fatalf("Step: %v", err)
	}
	if *quien != [2]string{"tenant-de-la-conversacion", "sesion-que-recibio-el-mensaje"} {
		t.Fatalf("el resolutor recibió %v: el engine tiene que preguntar con el tenant y la "+
			"sesión de ESTA conversación, que es lo único que decide la vía y el Edge", *quien)
	}
}
