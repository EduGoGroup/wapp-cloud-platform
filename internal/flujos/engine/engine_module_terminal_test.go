package engine_test

// engine_module_terminal_test.go — el FIN DE FLUJO DECLARADO POR EL MÓDULO (hallazgo
// #24, Plan 043 · Ola 6): un módulo de UN SOLO NODO que espera input siempre (p. ej.
// "cart") apunta Result.Next al centinela model.NodeTerminal y engine.Step lo reconoce
// SIN buscarlo en def.Nodes.
//
// Por qué estos tests existen y no bastaban los e2e: la rama nueva vive en el NÚCLEO
// COMPARTIDO que ejecutan menu, survey, media y cart, y hasta esta revisión lo único
// que la fijaba eran dos e2e de internal/publicapi que se OMITEN sin WAPP_TEST_DB_DSN
// —medido: quitando el `return` temprano de esa rama, `go test ./internal/flujos/...`
// seguía entero en verde—. Un cambio del motor tiene que caerse en el paquete del
// motor, sin base de datos.

import (
	"context"
	"slices"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
)

// tipoUnSoloNodo es el tipo de nodo del módulo de prueba: uno solo, interactivo,
// como el "cart" real.
const tipoUnSoloNodo = "sonda_un_nodo"

// móduloUnSoloNodo imita la FORMA del carrito sin su dominio: navega quedándose en su
// nodo (Next==nil) y, cuando termina, DECLARA el fin apuntando Next al centinela con
// una VARIABLE PROPIA —no una referencia a una global compartida—, que es justo como
// lo hace cart.Step. Si el engine comparase punteros en vez de valores, este módulo
// no terminaría nunca.
type móduloUnSoloNodo struct{}

func (móduloUnSoloNodo) Type() string                 { return tipoUnSoloNodo }
func (móduloUnSoloNodo) WaitsForInput() bool          { return true }
func (móduloUnSoloNodo) ProducesDurableContent() bool { return false }

func (móduloUnSoloNodo) Render(model.Node, model.Content) []string {
	return []string{"pantalla de ARRANQUE (Render)"}
}

func (móduloUnSoloNodo) Step(_ model.Node, conv model.Conversation, input string) modules.Result {
	vars := map[string]any{"visto": input}
	if input == "fin" {
		term := model.NodeTerminal // variable propia: el centinela se compara por VALOR
		return modules.Result{
			Next:    &term,
			Vars:    vars,
			Outputs: []string{"pantalla FINAL del módulo"},
			Effects: []modules.Effect{{Kind: "event", Name: "sonda_cerrada"}},
		}
	}
	return modules.Result{Vars: vars, Outputs: []string{"sigo aquí"}}
}

func flujoUnSoloNodo() model.Flow {
	return model.Flow{
		FlowID:  "sonda-un-nodo",
		Version: 1,
		Initial: "único",
		Nodes:   map[string]model.Node{"único": {Type: tipoUnSoloNodo}},
	}
}

func engineUnSoloNodo() *engine.Engine {
	reg := modules.NewRegistry()
	reg.Register(móduloUnSoloNodo{})
	return engine.New(reg)
}

// TestStepModuloDeclaraFinDeFlujo fija el contrato ENTERO de la rama nueva en un solo
// sitio: termina, NO exige que el centinela exista en def.Nodes, NO vuelve a llamar
// Render, conserva el Outputs que el propio Step produjo, propaga los Effects y deja
// las Vars del módulo.
func TestStepModuloDeclaraFinDeFlujo(t *testing.T) {
	e := engineUnSoloNodo()
	def := flujoUnSoloNodo()
	st, _, err := e.Enter(context.Background(), def, model.Conversation{})
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}

	st2, outs, effects, err := e.Step(context.Background(), def, st, engine.Input{Text: "fin"})
	if err != nil {
		t.Fatalf("Step: %v (el centinela NO debe buscarse en def.Nodes)", err)
	}
	if !st2.Finished() {
		t.Fatalf("el módulo declaró el fin; CurrentNode = %q, quiero el centinela", st2.CurrentNode)
	}
	// La pantalla es la del propio Step, NO la de Render: si el engine cayera al
	// camino de renderFrom, aquí saldría "pantalla de ARRANQUE (Render)" (o un error).
	if got := texts(outs); !slices.Equal(got, []string{"pantalla FINAL del módulo"}) {
		t.Fatalf("outs = %q, quiero la pantalla que el propio Step produjo (sin re-Render)", got)
	}
	if len(effects) != 1 || effects[0].Name != "sonda_cerrada" {
		t.Fatalf("los efectos del turno que termina el flujo deben propagarse; effects = %v", effects)
	}
	if st2.Vars["visto"] != "fin" {
		t.Fatalf("las Vars del módulo deben conservarse al terminar; vars = %v", st2.Vars)
	}

	// Y el turno siguiente es la salida neutra de siempre (conversación terminada).
	st3, outs3, effects3, err := e.Step(context.Background(), def, st2, engine.Input{Text: "hola"})
	if err != nil || len(outs3) != 0 || len(effects3) != 0 || !st3.Finished() {
		t.Fatalf("sobre una conversación terminada el Step debe ser neutro; err=%v outs=%v effects=%v nodo=%q",
			err, texts(outs3), effects3, st3.CurrentNode)
	}
}

// TestStepModuloSinFinSigueEnSuNodo es el CONTROL de la rama nueva: mientras el módulo
// no declara el fin (Next==nil), nada cambia — mismo nodo, su pantalla, sin centinela.
// Sin este control, una condición invertida en el engine pasaría desapercibida en este
// mismo paquete.
func TestStepModuloSinFinSigueEnSuNodo(t *testing.T) {
	e := engineUnSoloNodo()
	def := flujoUnSoloNodo()
	st, _, err := e.Enter(context.Background(), def, model.Conversation{})
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	st2, outs, _, err := e.Step(context.Background(), def, st, engine.Input{Text: "sigo"})
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	if st2.Finished() || st2.CurrentNode != "único" {
		t.Fatalf("sin declarar el fin el módulo permanece en su nodo; CurrentNode = %q", st2.CurrentNode)
	}
	if got := texts(outs); !slices.Equal(got, []string{"sigo aquí"}) {
		t.Fatalf("outs = %q, quiero la pantalla del propio Step", got)
	}
}
