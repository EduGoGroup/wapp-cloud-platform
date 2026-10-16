//go:build laboratorio

// e05-menu es el escalón E05 del laboratorio (Plan 058 · Bloque B): el motor de flujos REAL
// —runtime.HandleIncoming, el engine y los módulos menu y message— contestando a un cliente por
// WhatsApp, con los almacenes en memoria y un Sender doble en lugar del Edge. Enseña qué es el
// estado POR CONTACTO (flow_state), cómo un nodo espera la entrada y cómo repregunta.
//
//	GOTOOLCHAIN=go1.26.5 GOWORK=off go run -tags laboratorio ./laboratorio/e05-menu
//	… -llm-intake      (el plan del tenant trae llm_intake: aparece la bienvenida única)
//
// Es el recorrido de TestP3_IncomingToReply (test/procesos/p3_entrante_test.go), pasos 2 y 3, sin
// servidor, sin Postgres y sin Edge. Sale con rc=0 si todo el recorrido se pudo narrar.
package main

import (
	"flag"
	"os"
	"strconv"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"

	"github.com/EduGoGroup/wapp-cloud-platform/laboratorio/bench"
	"github.com/EduGoGroup/wapp-cloud-platform/laboratorio/common"
)

// Los textos del flujo: los mismos del proceso P3 (p3MenuPrompt, p3SalesText, p3SupportText).
const (
	flowID      = "menu-e05"
	keyword     = "hola"
	menuPrompt  = "Hola, ¿con quién quieres hablar?\n1) Ventas\n2) Soporte"
	salesText   = "Te paso con Ventas."
	supportText = "Cuéntame tu problema."
	// otherPhone es un segundo cliente: otra conversación.
	otherPhone = "573001110059"
)

func main() {
	llmIntake := flag.Bool("llm-intake", false, "el plan del tenant trae llm_intake (bienvenida única)")
	verbose := flag.Bool("v", false, "saca el log del cloud por stderr (desde Debug)")
	flag.Parse()
	if err := run(*llmIntake, *verbose); err != nil {
		os.Exit(1)
	}
}

// menuFlow es el flujo del escalón: un menú de dos opciones que llevan a dos mensajes finales.
func menuFlow() model.Flow {
	return model.Flow{
		FlowID: flowID, Initial: "root",
		Nodes: map[string]model.Node{
			"root":    {Type: model.NodeTypeMenu, Prompt: menuPrompt, Options: map[string]string{"1": "ventas", "2": "soporte"}},
			"ventas":  {Type: model.NodeTypeMessage, Text: salesText},
			"soporte": {Type: model.NodeTypeMessage, Text: supportText},
		},
	}
}

func run(llmIntake, verbose bool) error {
	n := common.NewNarrator(nil)
	n.Title("E05 · Menú y mensaje",
		"El motor de flujos real (runtime + engine + menu/message) contestando por WhatsApp.",
		"Real: runtime, engine, módulos, resolver de disparos. Doble: el Edge (Sender), los almacenes en memoria.")

	n.Step("Se monta el motor")
	var features []string
	if llmIntake {
		features = append(features, entitlements.FeatureLLMIntake)
	}
	opts := bench.Options{Features: features}
	if verbose {
		opts.Log = common.NewLog(true)
	}
	b := bench.New(opts)
	n.Note("runtime.New con los mismos cables que internal/arranque/fase7_flujos.go; los almacenes son gemelos en memoria.")
	n.Note("El Edge es runtimehelpertest.Sender: guarda cada SendText en vez de mandarlo a WhatsApp.")
	if llmIntake {
		n.Note("El tenant tiene llm_intake: el primer mensaje de cada conversación recibe la bienvenida única.")
	} else {
		n.Note("El tenant NO tiene features: sin llm_intake no hay bienvenida, ni hilo, ni ventana (prueba -llm-intake).")
	}

	n.Step("Se publica el flujo y su disparo por palabra clave")
	version, err := b.PublishFlow(menuFlow())
	if err != nil {
		n.Fail(err, "publicar el flujo")
		return err
	}
	if _, err := b.AddRule(trigger.Rule{Kind: trigger.KindKeyword, Keyword: keyword, MatchType: trigger.MatchExact, FlowID: flowID}); err != nil {
		n.Fail(err, "crear el disparo")
		return err
	}
	n.OK("flujo %s v%d: root (menu) → ventas | soporte (message, sin next: fin de flujo).", flowID, version)
	n.OK("disparo keyword %q, coincidencia exacta → %s.", keyword, flowID)

	n.Step("La palabra clave arranca el flujo")
	n.Note("Sin conversación viva, el runtime pregunta al resolver de disparos; «hola» casa la regla y arranca el flujo en su nodo inicial.")
	b.Turn(n, bench.Phone, "E05-1", keyword)
	b.ShowState(n, bench.Phone)
	n.Note("El menú ESPERA entrada (WaitsForInput): el estado se queda en root hasta que el cliente conteste.")
	n.Note("last_wa_message_id sale vacío: el turno que ARRANCA no lo anota; lo anota el primer avance. La red contra reenvíos del arranque es el dedupe de ingesta (paso 8).")

	n.Step("Una opción válida: el nodo destino y el fin del flujo")
	b.Turn(n, bench.Phone, "E05-2", "1")
	b.ShowState(n, bench.Phone)
	n.Note("«ventas» es un nodo message sin next: se dice su texto y el flujo termina. Lo que queda en flow_state es el centinela de fin, no root.")

	n.Step("Otra vez la palabra clave: una conversación nueva")
	b.Turn(n, bench.Phone, "E05-3", keyword)
	b.ShowState(n, bench.Phone)
	n.Note("El estado terminal se suelta y el disparo vuelve a casar: menú desde el principio.")

	n.Step("Opciones inválidas: la repregunta y su contador")
	for i, text := range []string{"9", "ventas"} {
		b.Turn(n, bench.Phone, "E05-"+strconv.Itoa(4+i), text)
		b.ShowState(n, bench.Phone)
	}
	n.Note("El menú solo acepta las CLAVES de sus opciones («1», «2»): «ventas» es texto, no clave. Cada fallo suma en vars.menu_reprompt.")
	b.Turn(n, bench.Phone, "E05-6", "x")
	b.ShowState(n, bench.Phone)
	n.Note("Al tercer fallo seguido (modules.MaxReprompts = 3) cambia el texto: «No logré entender…» y el contador vuelve a cero.")
	b.Turn(n, bench.Phone, "E05-7", "2")
	b.ShowState(n, bench.Phone)

	n.Step("El estado es POR CONTACTO")
	n.Note("Otro número escribe «1» sin conversación viva: «1» no casa ningún disparo, así que el runtime lo ignora.")
	b.Turn(n, otherPhone, "E05-8", "1")
	b.ShowState(n, otherPhone)
	n.Note("Y si escribe «hola», recibe SU menú. La clave de flow_state es (tenant, sesión, contact_id opaco).")
	b.Turn(n, otherPhone, "E05-9", keyword)
	b.ShowState(n, otherPhone)
	id, _ := b.ContactID(otherPhone)
	n.Note("contact_id del segundo cliente: %s… (un id opaco: el teléfono no se guarda en claro).", id[:8])
	b.ShowState(n, bench.Phone)

	n.Step("El mismo wa_message_id dos veces")
	n.Note("El Edge reenvía lo que no le acusaron (outbox durable). El dedupe de ingesta corta el repetido antes del motor.")
	b.Turn(n, otherPhone, "E05-9", keyword)
	b.ShowState(n, otherPhone)

	n.Step("Lo que queda")
	n.Show("textos que el Edge habría mandado", len(b.Sender.Texts()))
	n.Show("flow_events", len(b.Repo.FlowEvents()))
	n.Show("conversation_events", len(b.Events.Events(common.LabTenant)))
	n.Note("Un menú plano arrancado por keyword no escribe ni flow_events ni eventos: no hay nada durable que guardar.")
	n.Note("Eso es justo lo que cambia en E06, con el disparo event_start.")
	return nil
}
