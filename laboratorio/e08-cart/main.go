//go:build laboratorio

// e08-cart es el escalón E08 del laboratorio (Plan 058 · Bloque B): una conversación ENTERA de
// carrito con el motor REAL —el recorrido de TestP3_CartConversation (test/procesos/
// p3_entrante_cart_test.go)— y el único punto de la conversación donde entra un modelo: la cantidad
// que el carrito no entiende («abc»), que sube por el resolutor de consultas del engine
// (turnoacotado.Resolver) al llmvia.Selector.Turno REAL y de ahí al frame del laboratorio.
//
//	GOTOOLCHAIN=go1.26.5 GOWORK=off go run -tags laboratorio ./laboratorio/e08-cart -frame script
//	GOTOOLCHAIN=go1.26.5 GOWORK=off go run -tags laboratorio ./laboratorio/e08-cart -frame ollama
//	… -qty "un par"      (otra respuesta libre a «¿Cuántos?»)
//	… -script-turn 3     (con -frame script, el guion contesta usable:true value:3)
//	… -no-resolver       (sin resolutor cableado: la Trampa T-6 del arranque)
//	… -limiter prod      (el tope anti-bucle de producción: 3 respuestas y una cada 2 s)
//
// Sale con rc=0 si el cliente recibió la confirmación del pedido; rc=1 si no.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"os"
	"strconv"
	"strings"

	"golang.org/x/time/rate"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/turnoacotado"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/ratelimit"

	"github.com/EduGoGroup/wapp-cloud-platform/laboratorio/bench"
	"github.com/EduGoGroup/wapp-cloud-platform/laboratorio/common"
)

// El caso del arnés: el flujo, la palabra que abre su evento y la ref del catálogo.
const (
	flowID     = "cart-e08"
	keyword    = "carrito"
	contentRef = "carta"
	confirmed  = "✅ ¡Pedido confirmado! Total $5.00."
	// confirmedPrefix es el principio de la pantalla de cierre, sea cual sea el total.
	confirmedPrefix = "✅ ¡Pedido confirmado!"
)

// cartCatalog es el catálogo v1 de la transcripción del carrito (cartCatalog del arnés): dos
// categorías y tres artículos.
func cartCatalog() map[string]any {
	item := func(code, sku, label string, price float64, description string) map[string]any {
		return map[string]any{"code": code, "sku": sku, "label": label, "price": price, "description": description}
	}
	return map[string]any{"categories": []any{
		map[string]any{"code": "1", "label": "Bebidas", "items": []any{
			item("1", "CAFE", "Café", 2.5, "Espresso doble"),
			item("2", "TE", "Té", 2.0, "Verde o negro"),
		}},
		map[string]any{"code": "2", "label": "Postres", "items": []any{
			item("1", "FLAN", "Flan", 3.0, "Casero"),
		}},
	}}
}

// cartFlow es el flujo de UN nodo cart que lee el catálogo de tenant_content.
func cartFlow() model.Flow {
	return model.Flow{
		FlowID: flowID, Initial: "root",
		Nodes: map[string]model.Node{
			"root": {Type: cart.NodeTypeCart, Content: &model.ContentRef{Source: "json", Ref: contentRef}},
		},
	}
}

type config struct {
	flags      *common.Flags
	qty        string
	scriptTurn int
	noResolver bool
	limiter    string
}

func main() {
	cfg := config{flags: common.RegisterFlags(nil)}
	flag.StringVar(&cfg.qty, "qty", "abc", "lo que el cliente contesta a «¿Cuántos?» en vez de un número")
	flag.IntVar(&cfg.scriptTurn, "script-turn", 0, "con -frame script: el valor que el guion contesta al turno (0 ⇒ no_entendido)")
	flag.BoolVar(&cfg.noResolver, "no-resolver", false, "no cablea el resolutor de consultas en el engine")
	flag.StringVar(&cfg.limiter, "limiter", "none", "tope de auto-respuestas: none (doble sin tope) o prod (3 y una cada 2 s)")
	flag.Parse()
	if err := run(cfg); err != nil {
		os.Exit(1)
	}
}

func run(cfg config) error {
	n := common.NewNarrator(nil)
	n.Title("E08 · Carrito",
		"Una conversación entera de carrito, y el único hueco con modelo: la cantidad que el carrito no entiende.",
		"Frame: "+cfg.flags.Describe())

	b, spy, err := mount(n, cfg)
	if err != nil {
		return err
	}
	if err := browse(n, b, spy, cfg); err != nil {
		return err
	}
	if err := confirm(n, b); err != nil {
		return err
	}
	afterClose(n, b)
	return nil
}

// mount arma el frame, el selector, el resolutor y el banco, y siembra catálogo, flujo y disparo.
func mount(n *common.Narrator, cfg config) (*bench.Bench, *common.SpyFrame, error) {
	n.Step("Se monta el motor con el resolutor de consultas")
	spy, err := common.FrameFromFlags(cfg.flags)
	if err != nil {
		n.Fail(err, "banderas")
		return nil, nil, err
	}
	switch f := spy.Inner().(type) {
	case *common.ScriptFrame:
		if cfg.scriptTurn > 0 {
			f.Respond(common.StageTurn, common.TurnReplyValue(cfg.scriptTurn))
			n.Note("ScriptFrame: el turno contestará usable:true, value:%d (-script-turn).", cfg.scriptTurn)
		} else {
			n.Note("ScriptFrame: el turno contestará %s (un modelo bien portado ante «abc»).", common.TurnReplyNotUnderstood)
		}
	case *common.OllamaFrame:
		think, err := f.ModelThinks(contextBackground())
		if err != nil {
			n.Fail(err, "Ollama no contesta a /api/show")
			return nil, nil, err
		}
		n.Note("OllamaFrame contra %s; %s declara thinking: %v (si sí, viaja think:false).", cfg.flags.OllamaURL, f.Model(), think)
	}
	opts := bench.Options{
		Features:   []string{entitlements.FeatureCartBasic, entitlements.FeatureLLMIntake},
		Aggregator: true,
	}
	log := common.NewLog(cfg.flags.Verbose)
	if cfg.flags.Verbose {
		opts.Log = log
	}
	if !cfg.noResolver {
		sel, err := common.NewSelector(spy, log)
		if err != nil {
			n.Fail(err, "llmvia.NewSelector")
			return nil, nil, err
		}
		res, err := turnoacotado.New(sel)
		if err != nil {
			n.Fail(err, "turnoacotado.New")
			return nil, nil, err
		}
		opts.Resolver = res
		n.Note("engine.WithQueryResolver(turnoacotado.New(selector)): el MISMO cable que fase5/fase7 del arranque.")
	} else {
		n.Note("SIN resolutor (-no-resolver): la Trampa T-6. El engine contestará «sin_resolutor» y el carrito repreguntará.")
	}
	switch cfg.limiter {
	case "prod":
		opts.Limiter = ratelimit.NewLimiter(rate.Limit(0.5), 3)
		n.Note("Limitador de PRODUCCIÓN: platform/ratelimit con 3 de ráfaga y 0,5/s. Va con el reloj de pared, no con el manual.")
	case "none":
	default:
		err := errors.New("-limiter vale none o prod")
		n.Fail(err, "banderas")
		return nil, nil, err
	}
	b := bench.New(opts)
	n.Note("Plan del tenant: cart_basic y llm_intake (el plan por defecto del arnés). Agregador de ventanas cableado, sin barrido.")

	n.Step("Se siembran el catálogo, el flujo y el disparo event_start")
	blob, err := json.Marshal(cartCatalog())
	if err != nil {
		n.Fail(err, "serializar el catálogo")
		return nil, nil, err
	}
	b.SetContent(contentRef, blob)
	if _, err := b.PublishFlow(cartFlow()); err != nil {
		n.Fail(err, "publicar el flujo")
		return nil, nil, err
	}
	if _, err := b.AddRule(trigger.Rule{Kind: trigger.KindEventStart, Keyword: keyword, MatchType: trigger.MatchExact,
		EventKind: trigger.EventKindCart, FlowID: flowID}); err != nil {
		n.Fail(err, "crear el disparo")
		return nil, nil, err
	}
	n.OK("tenant_content %q: Bebidas (Café $2.50, Té $2.00) y Postres (Flan $3.00).", contentRef)
	n.OK("flujo %s: UN nodo cart que lee el catálogo; disparo event_start %q, event_kind cart.", flowID, keyword)
	return b, spy, nil
}

// browse va de la palabra al resumen, con los dos adversarios del arnés por el camino.
func browse(n *common.Narrator, b *bench.Bench, spy *common.SpyFrame, cfg config) error {
	n.Step("«carrito» abre el evento")
	b.Turn(n, bench.Phone, "E08-1", keyword)
	b.ShowState(n, bench.Phone)
	n.Note("La bienvenida sale por llm_intake, una vez; las categorías, del nodo cart. El evento nace en este turno (event_start).")

	n.Step("«8» no es una categoría")
	b.Turn(n, bench.Phone, "E08-2", "8")
	_, calls := spy.Last()
	n.Note("Inferencias hasta ahora: %d. Un NÚMERO nunca sube al modelo (cart/consulta.go, queryable): es un índice de pantalla.", calls)

	n.Step("«1», «1», «2»: Bebidas, Café, Agregar al pedido")
	for i, text := range []string{"1", "1", "2"} {
		b.Turn(n, bench.Phone, "E08-"+strconv.Itoa(3+i), text)
	}
	showCart(n, b)

	n.Step("«" + cfg.qty + "» como cantidad: el turno acotado")
	n.Note("El carrito no lo entiende: no es un número, ni un código del nivel, y la cascada determinista no aplica a la cantidad.")
	n.Note("Así que ELEVA una consulta (modules.Query clase cantidad) y el engine se la pasa al resolutor: turnoacotado → Selector.Turno → frame.")
	b.Turn(n, bench.Phone, "E08-6", cfg.qty)
	showQuery(n, b, spy)
	st := showCart(n, b)
	if level(st) == "quantity" {
		n.Step("«2»: la cantidad, ya en dígitos")
		b.Turn(n, bench.Phone, "E08-7", "2")
	} else {
		n.Note("El turno RESOLVIÓ la cantidad: el carrito la aplicó como si el cliente hubiera escrito el número. Se salta el «2».")
	}
	n.Step("«2»: Finalizar pedido, y el resumen")
	b.Turn(n, bench.Phone, "E08-8", "2")
	showCart(n, b)
	showIntakes(n, b)
	return nil
}

// level es el nivel del carrito en las vars.
func level(st model.Conversation) string {
	if c, ok := st.Vars["cart"].(map[string]any); ok {
		if l, ok := c["level"].(string); ok {
			return l
		}
	}
	return ""
}

// confirm confirma el pedido y narra lo que queda en las tablas.
func confirm(n *common.Narrator, b *bench.Bench) error {
	n.Step("«1»: Confirmar y finalizar")
	replies := b.Turn(n, bench.Phone, "E08-9", "1")
	st, _ := b.ShowState(n, bench.Phone)
	closed := level(st) == "closed"
	seen := len(replies) == 1 && strings.HasPrefix(replies[0], confirmedPrefix)
	switch {
	case seen && replies[0] == confirmed:
		n.OK("la pantalla de cierre es, byte a byte, la del arnés: %q.", confirmed)
	case seen:
		n.OK("pedido confirmado, con otro total que el del arnés (%q): la cantidad la decidió el turno.", replies[0])
	case closed:
		n.Fail(nil, "el pedido se CERRÓ y el cliente NO recibió la confirmación")
		n.Note("El runtime guarda el estado, escribe el hilo y alimenta la ventana ANTES de preguntar al limitador si puede")
		n.Note("contestar (runtime/incoming_advance.go: Save → persistTurnMessages → observeForAggregation → sendReply).")
		n.Note("Con el tope de producción, quien escribe más rápido que una respuesta cada 2 s avanza el carrito A CIEGAS.")
	default:
		n.Fail(nil, "el pedido no se cerró")
	}
	showClose(n, b)
	if !seen {
		return errors.New("el pedido no se confirmó a la vista del cliente")
	}
	return nil
}

// afterClose manda un mensaje más con el pedido ya confirmado.
func afterClose(n *common.Narrator, b *bench.Bench) {
	n.Step("«1» otra vez, con el pedido ya cerrado")
	b.Turn(n, bench.Phone, "E08-10", "1")
	b.ShowState(n, bench.Phone)
	n.Note("El flujo terminó y «1» no casa ningún disparo: el motor suelta el estado terminal y no contesta nada.")
	jobs := b.Jobs.Jobs()
	refs := 0
	for _, j := range jobs {
		refs += len(j.SourceRefs)
	}
	n.Note("La ventana del evento sigue con %d referencias: el mensaje posterior al cierre no entra.", refs)
}

// limiterOf es solo para que el compilador vea que el limitador de producción cumple el puerto.
var _ runtime.ReplyLimiter = (*ratelimit.Limiter)(nil)
