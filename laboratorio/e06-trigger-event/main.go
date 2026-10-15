//go:build laboratorio

// e06-trigger-event es el escalón E06 del laboratorio (Plan 058 · Bloque B): el MISMO flujo de E05
// arrancado por dos disparos distintos, en dos empresas en memoria idénticas salvo en eso. Con
// `keyword` la conversación queda en el limbo (sin evento padre) y con `event_start` nace un
// conversation_event: su fila en flow_events, su hilo y su ventana de captación. Y el caso 14 de
// documentations/operacion/troubleshooting.md tal como lo trata hoy el código: un flujo con
// contenido durable arrancado por keyword.
//
//	GOTOOLCHAIN=go1.26.5 GOWORK=off go run -tags laboratorio ./laboratorio/e06-trigger-event
//	… -no-llm-intake   (sin la feature: ni hilo ni ventana, tampoco con evento)
package main

import (
	"flag"
	"os"
	"strings"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"

	"github.com/EduGoGroup/wapp-cloud-platform/laboratorio/bench"
	"github.com/EduGoGroup/wapp-cloud-platform/laboratorio/common"
)

const (
	flowID     = "menu-e06"
	keyword    = "hola"
	menuPrompt = "Hola, ¿con quién quieres hablar?\n1) Ventas\n2) Soporte"
	// surveyFlowID es el flujo con contenido DURABLE del tercer acto: una pregunta de encuesta.
	surveyFlowID  = "encuesta-e06"
	surveyKeyword = "encuesta"
)

func main() {
	noIntake := flag.Bool("no-llm-intake", false, "el plan del tenant NO trae llm_intake")
	verbose := flag.Bool("v", false, "saca el log del cloud por stderr (desde Debug)")
	flag.Parse()
	if err := run(!*noIntake, *verbose); err != nil {
		os.Exit(1)
	}
}

func menuFlow() model.Flow {
	return model.Flow{
		FlowID: flowID, Initial: "root",
		Nodes: map[string]model.Node{
			"root":    {Type: model.NodeTypeMenu, Prompt: menuPrompt, Options: map[string]string{"1": "ventas", "2": "soporte"}},
			"ventas":  {Type: model.NodeTypeMessage, Text: "Te paso con Ventas."},
			"soporte": {Type: model.NodeTypeMessage, Text: "Cuéntame tu problema."},
		},
	}
}

func surveyFlow() model.Flow {
	end := "gracias"
	return model.Flow{
		FlowID: surveyFlowID, Initial: "q1",
		Nodes: map[string]model.Node{
			"q1": {Type: model.NodeTypeSurveyQuestion, QuestionID: "atencion",
				Prompt: "¿Cómo calificas la atención?\n1) Buena\n2) Mala", Options: map[string]string{"1": end, "2": end}},
			"gracias": {Type: model.NodeTypeMessage, Text: "¡Gracias!"},
		},
	}
}

func run(llmIntake, verbose bool) error {
	n := common.NewNarrator(nil)
	n.Title("E06 · Disparo y evento",
		"El mismo flujo con disparo keyword y con event_start: sin evento no hay ventana de captación.",
		"Real: runtime, engine, resolver de disparos, plano de eventos, agregador. Doble: Sender, almacenes en memoria.")

	newWorld := func() *bench.Bench {
		opts := bench.Options{Aggregator: true}
		if llmIntake {
			opts.Features = []string{entitlements.FeatureLLMIntake}
		}
		if verbose {
			opts.Log = common.NewLog(true)
		}
		return bench.New(opts)
	}
	if llmIntake {
		n.Note("Las dos empresas tienen llm_intake (el plan por defecto): bienvenida, hilo del evento y ventana de captación.")
	} else {
		n.Note("Sin llm_intake (-no-llm-intake): ni bienvenida, ni hilo, ni ventana, con evento o sin él.")
	}
	n.Note("El agregador de ventanas está cableado (runtime.WithAggregator) pero su barrido NO corre: las ventanas se abren y no cierran. Cerrarlas es E09.")

	n.Step("Empresa A · disparo keyword")
	a := newWorld()
	if _, err := a.PublishFlow(menuFlow()); err != nil {
		n.Fail(err, "publicar el flujo")
		return err
	}
	if _, err := a.AddRule(trigger.Rule{Kind: trigger.KindKeyword, Keyword: keyword, MatchType: trigger.MatchExact, FlowID: flowID}); err != nil {
		n.Fail(err, "crear el disparo")
		return err
	}
	n.OK("regla keyword %q → %s.", keyword, flowID)
	a.Turn(n, bench.Phone, "E06-A1", keyword)
	a.ShowState(n, bench.Phone)
	a.Turn(n, bench.Phone, "E06-A2", "1")
	report(n, a)
	n.Note("Con keyword el flujo corre igual, pero en el LIMBO: no nace ningún conversation_event. Sin evento no hay hilo,")
	n.Note("y la ventana de captación no puede abrirse: su clave (tenant, sesión, contacto, event_id) exige el evento.")

	n.Step("Empresa B · disparo event_start")
	b := newWorld()
	if _, err := b.PublishFlow(menuFlow()); err != nil {
		n.Fail(err, "publicar el flujo")
		return err
	}
	if _, err := b.AddRule(trigger.Rule{Kind: trigger.KindEventStart, Keyword: keyword, MatchType: trigger.MatchExact,
		EventKind: trigger.EventKindSurvey, FlowID: flowID}); err != nil {
		n.Fail(err, "crear el disparo")
		return err
	}
	n.OK("regla event_start %q, event_kind %q → %s.", keyword, trigger.EventKindSurvey, flowID)
	n.Note("event_kind es la ETIQUETA del evento (cart, survey o media; «menu» está reservado al despachador y presenta su lista en vez del flujo).")
	n.Note("El motor no comprueba que la etiqueta case con los nodos del flujo: aquí un menú lleva la etiqueta survey.")
	b.Turn(n, bench.Phone, "E06-B1", keyword)
	b.ShowState(n, bench.Phone)
	b.Turn(n, bench.Phone, "E06-B2", "1")
	b.ShowState(n, bench.Phone)
	report(n, b)
	n.Note("Con event_start el evento NACE en el turno que lo abre (event_started en flow_events), la conversación lo apunta como")
	n.Note("activo y dueño, el hilo guarda cada mensaje SELLADO (cifrado) y la ventana acumula las referencias de los entrantes.")

	n.Step("Empresa C · el caso 14: un flujo con contenido durable arrancado por keyword")
	c := newWorld()
	if _, err := c.PublishFlow(surveyFlow()); err != nil {
		n.Fail(err, "publicar el flujo")
		return err
	}
	if _, err := c.AddRule(trigger.Rule{Kind: trigger.KindKeyword, Keyword: surveyKeyword, MatchType: trigger.MatchExact, FlowID: surveyFlowID}); err != nil {
		n.Fail(err, "crear el disparo")
		return err
	}
	n.OK("flujo %s (una survey_question: contenido DURABLE, escribe survey_results) y regla keyword %q.", surveyFlowID, surveyKeyword)
	n.Note("Ojo: la API pública NO dejaría crear esta regla (422, admin/triggers.go: blocksStartWithoutEventStart) porque el tenant")
	n.Note("no tiene ninguna event_start viva. El laboratorio escribe en el almacén directamente, por debajo de la API.")
	c.Turn(n, bench.Phone, "E06-C1", surveyKeyword)
	c.ShowState(n, bench.Phone)
	n.Note("El runtime se NIEGA a arrancarlo sin evento (ErrDurableFlowNeedsEvent) y degrada a la oferta del despachador; sin")
	n.Note("ninguna event_start no hay nada que ofrecer, y el contacto se queda sin respuesta. Es el mismo silencio que la API evita.")

	n.Step("Empresa C · con una red event_start")
	if _, err := c.AddRule(trigger.Rule{Kind: trigger.KindEventStart, Keyword: "opinar", MatchType: trigger.MatchExact,
		EventKind: trigger.EventKindSurvey, FlowID: surveyFlowID}); err != nil {
		n.Fail(err, "crear el disparo event_start")
		return err
	}
	n.OK("regla event_start %q, event_kind %q → %s.", "opinar", trigger.EventKindSurvey, surveyFlowID)
	c.Turn(n, bench.Phone, "E06-C2", surveyKeyword)
	c.ShowState(n, bench.Phone)
	n.Note("Sigue el silencio. La oferta del despachador solo enseña los tipos que el PLAN del tenant trae (fail-closed,")
	n.Note("events/dispatcher.go): survey exige la feature %q, y esta empresa solo tiene llm_intake.", entitlements.FeatureSurvey)

	n.Step("Empresa C · con la feature survey, la keyword degrada a la oferta")
	c.Features.Enable(common.LabTenant, entitlements.FeatureSurvey)
	n.OK("el plan del tenant gana la feature %q.", entitlements.FeatureSurvey)
	c.Turn(n, bench.Phone, "E06-C3", surveyKeyword)
	c.ShowState(n, bench.Phone)
	n.Note("Ahora la keyword ofrece los tipos que el tenant habilita; elegir uno hace nacer el evento por la puerta del despachador.")
	c.Turn(n, bench.Phone, "E06-C4", "1")
	c.ShowState(n, bench.Phone)
	report(n, c)
	n.Note("Fíjate: el evento nació por la puerta del DESPACHADOR y su hilo está vacío y sin ventana. Es DELIBERADO (Plan 044,")
	n.Note("runtime/events_menu.go): ese «1» es la respuesta a una lista que pintamos nosotros, no lo que el cliente pide, y anclaría")
	n.Note("la fecha del presupuesto en él. El precio: la primera pregunta tampoco queda en el hilo. Se contesta:")
	c.Turn(n, bench.Phone, "E06-C5", "1")
	c.ShowState(n, bench.Phone)
	report(n, c)

	n.Step("Lo que hay que llevarse")
	n.Note("troubleshooting.md §14 (2026-08-12) contaba que un carrito arrancado por keyword cerraba el pedido en el chat y lo perdía")
	n.Note("contra el NOT NULL de intakes.event_id. Hoy el código lo corta antes, en dos sitios: la API (422) y el runtime (no arranca).")
	n.Note("La regla de fondo no cambió: lo que se quiere CAPTAR (pedido, encuesta, ventana) necesita un evento, y el evento lo pare event_start.")
	return nil
}

// report narra lo que quedó en las tablas que separan el limbo del evento.
func report(n *common.Narrator, b *bench.Bench) {
	n.Show("flow_events", strings.Join(orNone(b.FlowEventNames()), ", "))
	evs := b.Events.Events(common.LabTenant)
	if len(evs) == 0 {
		n.Show("conversation_events", "(ninguno)")
	}
	for _, ev := range evs {
		n.Show("conversation_events", ev.Kind+" · "+string(ev.Status)+" · flujo "+ev.FlowID)
		var kinds []string
		for _, e := range b.Events.Entries(ev.ID) {
			k := string(e.Role) + ":" + string(e.Kind)
			if e.Sealed {
				k += " (sellada)"
			}
			kinds = append(kinds, k)
		}
		n.Show("   su hilo", strings.Join(orNone(kinds), ", "))
	}
	jobs := b.Jobs.Jobs()
	if len(jobs) == 0 {
		n.Show("intake_jobs", "(ninguna ventana)")
	}
	for _, j := range jobs {
		n.Show("intake_jobs", j.Status+" · source_refs "+strings.Join(j.SourceRefs, ","))
	}
}

func orNone(s []string) []string {
	if len(s) == 0 {
		return []string{"(ninguna)"}
	}
	return s
}
