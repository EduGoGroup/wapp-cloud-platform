package arranque

// conversacion_cableado_test.go — EL CANDADO DE CABLEADO DE conmutar(conversacion) (F8 · T8.33,
// T8.34; reglas.md de F8, trampas T-1…T-6; D-F8-16; 05 §4.2, hallazgo 39 de F1).
//
// Test nuevo del arranque nuevo (no es copia del viejo). Es un CANDADO DE CABLEADO, como sus
// vecinos *_cableado_test.go: no prueba conducta del motor (eso es de internal/modulos/
// conversacion y de los procesos de F9), sino que el arranque une las piezas como dice. Lo hace
// por dos vías:
//
//   - sobre el arranque REAL (las fases 2–8 del contenedor de la huella, sin red ni BD) y por
//     reflexión: qué instancia guarda cada consumidor en su campo privado;
//   - sobre el AST del paquete (conversacion_cableado_ast_test.go) lo que la reflexión no puede
//     ver: un valor de método (`c.gw.OnIncoming = c.flowRuntime.OnIncoming`), una clausura de
//     handler, un `go …Run(ctx)` de la fase 9, las rutas de import.
//
// 🔴 Por qué no basta «el arranque no hace panic»: casi todo lo que aquí se afirma falla EN
// SILENCIO. Un segundo runtime parte el candado por conversación (T-1); un segundo resolver de
// derechos son dos cachés (T-2); un segundo KeyProvider apaga el anti-self-loop sin un error
// (T-3); y en el runtime nuevo WithEventSink(nil) y WithResumePolicy(tipo, nil) se IGNORAN
// (D-F8-16), así que un sink mal construido deja al motor con el LogSink de relleno.

import (
	"go/ast"
	"reflect"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/survey"
	flowruntime "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
)

// Rutas de import de lo que el arranque solo puede construir UNA vez.
const (
	conversationModulePath        = "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion"
	conversationRuntimePath       = conversationModulePath + "/runtime"
	conversationEnginePath        = conversationModulePath + "/engine"
	conversationBoundedTurnPath   = conversationModulePath + "/turnoacotado"
	conversationStorePath         = conversationModulePath + "/store"
	conversationEventsPath        = conversationModulePath + "/events"
	conversationTriggerPath       = conversationModulePath + "/trigger"
	conversationAdminPath         = conversationModulePath + "/admin"
	conversationCryptoPath        = "github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
	conversationEntitlementsPath  = "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	conversationFillerSinkTypeStr = "*runtime.LogSink"
)

// TestCableado_TheBootBuildsEachConversationPieceOnce (T-1, T-2, T-3): en toda la producción de
// internal/arranque hay exactamente UNA construcción del runtime, del agregador, del compositor
// del sobre, del engine, del resolutor del turno acotado, de los almacenes de conversación, del
// despachador, del KeyProvider y del resolver de derechos. Dos de cualquiera compilan, arrancan y
// no dan un solo error: parten estado (candado por conversación, cachés, índice ciego).
func TestCableado_TheBootBuildsEachConversationPieceOnce(t *testing.T) {
	once := []struct{ path, fn, why string }{
		{conversationRuntimePath, "New", "dos runtimes parten el candado por conversación, el limitador, las rachas y el semáforo (T-1)"},
		{conversationRuntimePath, "NewIntakeAggregator", "dos agregadores barren la misma tabla de ventanas"},
		{conversationRuntimePath, "NewSourceTextComposer", "el agregador y el re-análisis componen el sobre con el MISMO compositor"},
		{conversationEnginePath, "New", "el runtime y el comprobador de flujo durable leen del MISMO engine"},
		{conversationBoundedTurnPath, "New", "un solo resolutor de consultas, sobre el único selector de vía"},
		{conversationStorePath, "NewPostgresRepository", "un solo almacén de flujos"},
		{conversationEventsPath, "NewStore", "un segundo almacén del evento sería un segundo cipher y un segundo reloj"},
		{conversationEventsPath, "NewDispatcher", "WithDispatcher y WithOpeningBuilder reciben el MISMO despachador"},
		{conversationTriggerPath, "NewPostgresStore", "un solo almacén de reglas de disparo"},
		{conversationCryptoPath, "NewKeyProvider", "dos KeyProviders: ningún índice ciego casa y el anti-self-loop deja de bloquear (T-3)"},
		{conversationEntitlementsPath, "NewPostgres", "dos resolvers de derechos son dos cachés TTL y dos verdades de lo contratado (T-2)"},
	}
	for _, k := range once {
		if n := callsTo(t, k.path, k.fn); n != 1 {
			t.Errorf("%s.%s aparece %d veces en la producción de internal/arranque; se espera 1: %s",
				k.path[len("github.com/EduGoGroup/wapp-cloud-platform/internal/"):], k.fn, n, k.why)
		}
	}

	// Un solo runtime.New no basta si la función que lo envuelve se llama dos veces.
	_, files := astDelArranque(t)
	builds := 0
	inspecciona(files, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && textoDe(call.Fun) == "construirRuntimeDeFlujos" {
			builds++
		}
		return true
	})
	if builds != 1 {
		t.Errorf("construirRuntimeDeFlujos se llama %d veces en la producción de internal/arranque; se espera 1: "+
			"cada llamada es un runtime más (T-1)", builds)
	}
}

// conversationRuntimeOfTheBoot devuelve el contenedor del arranque real con el runtime ya
// construido, o corta el test: sin runtime no hay nada que afirmar.
func conversationRuntimeOfTheBoot(t *testing.T) *contenedor {
	t.Helper()
	c := contenedorDeHuella(t, "minimo")
	if c.flowRuntime == nil {
		t.Fatal("la fase 7 no construyó el runtime de flujos")
	}
	return c
}

// TestIdentidad_OneEntitlementsResolverForConversation (T-2): el resolver de derechos del
// runtime, del agregador, del despachador de eventos y de las tres áreas gateadas de la cara
// nueva (I14–I17, I18, I19) es c.entResolver, el único del proceso.
func TestIdentidad_OneEntitlementsResolverForConversation(t *testing.T) {
	c := conversationRuntimeOfTheBoot(t)
	if c.entResolver == nil {
		t.Fatal("la fase 3 no construyó el resolver de derechos")
	}
	face := conversationDepsOfTheNewFace(c)
	consumers := []struct {
		name string
		got  reflect.Value
	}{
		{"el runtime (WithEntitlements)", field(t, c.flowRuntime, "entitlements")},
		{"el agregador de ventanas", field(t, c.intakeAggregator, "ents")},
		{"el despachador de eventos", field(t, c.dispatcher, "feats")},
		{"el import de catálogo de la cara nueva (I14–I17)", reflect.ValueOf(face.catalogImport.Entitlements)},
		{"la bandeja de eventos de la cara nueva (I18)", reflect.ValueOf(face.events.Entitlements)},
		{"la cancelación de eventos de la cara nueva (I19)", reflect.ValueOf(face.eventCancel.Entitlements)},
	}
	for _, k := range consumers {
		if !sameInstance(k.got, c.entResolver) {
			t.Errorf("el resolver de derechos de %s no es la MISMA instancia que c.entResolver: "+
				"habría dos cachés y dos verdades de lo contratado (T-2)", k.name)
		}
	}
}

// TestIdentidad_TheSelfLoopGuardSharesTheKeyProviderOfTheFleet (T-3): el KeyProvider con el que
// el runtime calcula el índice ciego del anti-self-loop es c.flowDeps.kp, el MISMO con el que el
// repositorio de flota lo escribe. Con dos, ningún índice casa y el guard deja de bloquear sin
// dar un error.
func TestIdentidad_TheSelfLoopGuardSharesTheKeyProviderOfTheFleet(t *testing.T) {
	c := conversationRuntimeOfTheBoot(t)
	if c.flowDeps.kp == nil {
		t.Fatal("la fase 3 no construyó el KeyProvider")
	}
	self := field(t, c.flowRuntime, "selfNumbers")
	if self.IsNil() {
		t.Fatal("el runtime no tiene guarda anti-self-loop (WithSelfNumbers no se cableó)")
	}
	if got := self.Elem().Type(); got != reflect.TypeFor[*flowruntime.PostgresSelfNumbers]() {
		t.Fatalf("la guarda anti-self-loop es un %s; se espera *runtime.PostgresSelfNumbers", got)
	}
	if !sameInstance(inner(t, self, "kp"), c.flowDeps.kp) {
		t.Error("el KeyProvider de la guarda anti-self-loop no es c.flowDeps.kp: ningún índice ciego casaría (T-3)")
	}
	if !sameInstance(field(t, c.fleetRepo, "kp"), c.flowDeps.kp) {
		t.Error("el KeyProvider del repositorio de flota no es c.flowDeps.kp: quien escribe el índice ciego y " +
			"quien lo lee (el anti-self-loop) usarían llaves distintas (T-3)")
	}
}

// TestIdentidad_TheRuntimeIsBuiltOnTheSharedPieces (T-1, T-13): el runtime habla con los Edge por
// c.gw, sin adaptador, y sus colaboradores son las instancias del contenedor: el engine, los
// almacenes, el agregador, el despachador (por sus dos puertos) y los contactos del núcleo.
func TestIdentidad_TheRuntimeIsBuiltOnTheSharedPieces(t *testing.T) {
	c := conversationRuntimeOfTheBoot(t)
	pieces := []struct {
		name string
		got  reflect.Value
		want any
	}{
		{"sender (el gateway, T-13)", field(t, c.flowRuntime, "sender"), c.gw},
		{"engine", field(t, c.flowRuntime, "engine"), c.flowEngine},
		{"store (el almacén de flujos)", field(t, c.flowRuntime, "store"), c.flowStore},
		{"events (el almacén del evento)", field(t, c.flowRuntime, "events"), c.eventStore},
		{"intakes (IntakeAbandoner: el Service de solicitudes)", field(t, c.flowRuntime, "intakes"), c.intakeService},
		{"aggregator", field(t, c.flowRuntime, "aggregator"), c.intakeAggregator},
		{"dispatcher", field(t, c.flowRuntime, "dispatcher"), c.dispatcher},
		{"opening (el MISMO despachador)", field(t, c.flowRuntime, "opening"), c.dispatcher},
		{"welcomes (el almacén de flujos)", field(t, c.flowRuntime, "welcomes"), c.flowStore},
		{"contacts (el resolver del núcleo)", field(t, c.flowRuntime, "contacts"), c.flowDeps.contacts},
	}
	for _, k := range pieces {
		if reflect.ValueOf(k.want).IsNil() {
			t.Errorf("el arranque no construyó lo que el runtime espera en %s", k.name)
			continue
		}
		if !sameInstance(k.got, k.want) {
			t.Errorf("el campo %s del runtime no es la MISMA instancia que la del contenedor", k.name)
		}
	}
}

// TestIdentidad_TheNewFaceStartsAndCancelsOnTheOneRuntime (T-1, R8.5.b): el Starter de I4 y el
// Canceller de I19 que recibe la cara nueva son c.flowRuntime, el mismo puntero que recibe el
// gateway. Y la bandeja de eventos (I18) lee del MISMO almacén que el motor. Que J19 y
// gw.OnIncoming reciben también ese runtime lo afirma el AST
// (TestCableado_TheGatewayHooksAreWired, TestCableado_AdminStartRunsOnTheOneRuntime).
func TestIdentidad_TheNewFaceStartsAndCancelsOnTheOneRuntime(t *testing.T) {
	c := conversationRuntimeOfTheBoot(t)
	face := conversationDepsOfTheNewFace(c)
	if !sameInstance(reflect.ValueOf(face.flows.Starter), c.flowRuntime) {
		t.Error("el Starter de I4 (POST /api/v1/flows/{id}/start) no es c.flowRuntime: habría dos runtimes (T-1)")
	}
	if !sameInstance(reflect.ValueOf(face.eventCancel.Canceller), c.flowRuntime) {
		t.Error("el Canceller de I19 (POST /api/v1/conversation-events/{id}/cancel) no es c.flowRuntime: habría dos runtimes (T-1)")
	}
	if !sameInstance(reflect.ValueOf(face.events.Events), c.eventStore) {
		t.Error("la bandeja de eventos de I18 no lee de c.eventStore: sería un segundo reloj opinando sobre qué está vencido")
	}
	if !sameInstance(reflect.ValueOf(face.flows.Flows), c.flowStore) || !sameInstance(reflect.ValueOf(face.flows.Triggers), c.triggerStore) {
		t.Error("I1–I3 e I11–I13 no reciben c.flowStore y c.triggerStore, los almacenes de los que lee el motor")
	}
	if !sameInstance(reflect.ValueOf(face.flows.TriggersDurableFlow), c.durableFlowChecker) {
		t.Error("las reglas de disparo de la cara nueva no reciben c.durableFlowChecker")
	}
}

// TestCableado_TheGatewayHooksAreSet (T-5): tras el arranque real los cuatro hooks del gateway
// están puestos y el gauge de rachas tiene fuente. Con cualquiera a nil el proceso arranca entero
// y lo suyo no ocurre: sin OnIncoming ninguna conversación avanza. QUIÉN está enchufado en cada
// uno lo afirma el AST (TestCableado_TheGatewayHooksAreWired): un valor de método no se puede
// comparar por receptor.
func TestCableado_TheGatewayHooksAreSet(t *testing.T) {
	c := conversationRuntimeOfTheBoot(t)
	if c.gw.OnIncoming == nil {
		t.Error("c.gw.OnIncoming es nil: el motor está armado y ningún mensaje llega a él (T-5)")
	}
	if c.gw.OnHeartbeat == nil {
		t.Error("c.gw.OnHeartbeat es nil")
	}
	if c.gw.OnWarmup == nil {
		t.Error("c.gw.OnWarmup es nil: nada precalienta la caché de prefijo")
	}
	if c.gw.OnEdgeReady == nil {
		t.Error("c.gw.OnEdgeReady es nil: los jobs de un Edge recuperado esperarían a su backoff")
	}
	if field(t, c.mtx, "streakMaxSource").IsNil() {
		t.Error("el gauge wapp_flow_autoreply_streak_max no tiene fuente: SetFlowAutoreplyStreakMaxSource no se llamó " +
			"y las rachas vencidas no se barren nunca (T-7)")
	}
}

// conversationSinksOfTheRuntime lee la lista de sinks del runtime construido y devuelve el
// PersistSink y el WebhookSink. Falla si la lista no es EXACTAMENTE esos dos; en particular, si
// lleva el LogSink de relleno que runtime.New pone cuando no le llega ninguno.
func conversationSinksOfTheRuntime(t *testing.T, c *contenedor) (persist, webhook reflect.Value) {
	t.Helper()
	sinks := field(t, c.flowRuntime, "sinks")
	for i := range sinks.Len() {
		sink := sinks.Index(i)
		if sink.IsNil() {
			t.Fatalf("el sink %d del runtime es nil", i)
		}
		switch got := sink.Elem().Type(); {
		case got == reflect.TypeFor[*flowruntime.PersistSink]():
			persist = sink
		case got == reflect.TypeFor[*flowruntime.WebhookSink]():
			webhook = sink
		case got.String() == conversationFillerSinkTypeStr:
			t.Errorf("el sink %d del runtime es el %s de RELLENO: runtime.New lo pone cuando no recibe "+
				"ningún sink (un WithEventSink(nil) se ignora), y con él los efectos solo se loguean", i, got)
		default:
			t.Errorf("el sink %d del runtime es un %s que el arranque no cablea", i, got)
		}
	}
	if sinks.Len() != 2 || !persist.IsValid() || !webhook.IsValid() {
		t.Fatalf("el runtime tiene %d sinks (PersistSink: %t, WebhookSink: %t); el arranque cablea exactamente esos dos",
			sinks.Len(), persist.IsValid(), webhook.IsValid())
	}
	return persist, webhook
}

// TestCableado_TheRuntimeHasItsRealSinks (D-F8-16): la lista de sinks del runtime construido es
// la que el arranque cablea —el PersistSink y el WebhookSink— y no el LogSink de relleno
// (WithEventSink(nil) se ignora en silencio). Y el WebhookSink lleva sus dos dependencias, las
// del contenedor: sin ellas el puente CRM no encolaría nada.
func TestCableado_TheRuntimeHasItsRealSinks(t *testing.T) {
	c := conversationRuntimeOfTheBoot(t)
	_, webhook := conversationSinksOfTheRuntime(t, c)
	if !inner(t, webhook, "hasDependencies").Bool() {
		t.Error("el WebhookSink se construyó sin cola o sin gate: el puente CRM no encolaría nada")
	}
	pusher := inner(t, webhook, "pusher")
	if c.integrationsStore == nil || !sameInstance(inner(t, pusher, "queuer"), c.integrationsStore) {
		t.Error("la cola del WebhookSink no es c.integrationsStore, la que drena el worker del puente CRM")
	}
	if c.webhookGate == nil || !sameInstance(inner(t, pusher, "gate"), c.webhookGate) {
		t.Error("el gate del WebhookSink no es c.webhookGate, el mismo que decide en el callback del CRM (G17)")
	}
	if got := inner(t, webhook, "deliverEffect").String(); got != cart.EffectCartClosed {
		t.Errorf("el WebhookSink entrega el efecto %q; se espera cart.EffectCartClosed (%q)", got, cart.EffectCartClosed)
	}
}

// TestCableado_ThePersistSinkHasItsThreadAndProjectors (D-F8-16): el PersistSink del runtime
// escribe en c.flowStore, su hilo de decisiones es c.eventStore (WithDecisionThread) y lleva
// los dos proyectores, el del carrito y el de la encuesta, sobre los almacenes del contenedor.
func TestCableado_ThePersistSinkHasItsThreadAndProjectors(t *testing.T) {
	c := conversationRuntimeOfTheBoot(t)
	persist, _ := conversationSinksOfTheRuntime(t, c)
	if !sameInstance(inner(t, persist, "repo"), c.flowStore) {
		t.Error("el PersistSink no escribe en c.flowStore")
	}
	if c.eventStore == nil || !sameInstance(inner(t, persist, "decisions"), c.eventStore) {
		t.Error("el hilo de decisiones del PersistSink no es c.eventStore (WithDecisionThread): las filas " +
			"`decision` del evento no se escribirían, o las escribiría un segundo almacén")
	}
	projectors := inner(t, persist, "projectors")
	var cartProjector, surveyProjector reflect.Value
	for i := range projectors.Len() {
		p := projectors.Index(i)
		if p.IsNil() {
			t.Fatalf("el proyector %d del PersistSink es nil", i)
		}
		switch p.Elem().Type() {
		case reflect.TypeFor[*cart.Projector]():
			cartProjector = p
		case reflect.TypeFor[*survey.Projector]():
			surveyProjector = p
		default:
			t.Errorf("el proyector %d del PersistSink es un %s que el arranque no cablea", i, p.Elem().Type())
		}
	}
	if projectors.Len() != 2 || !cartProjector.IsValid() || !surveyProjector.IsValid() {
		t.Fatalf("el PersistSink tiene %d proyectores (*cart.Projector: %t, *survey.Projector: %t); se esperan esos dos",
			projectors.Len(), cartProjector.IsValid(), surveyProjector.IsValid())
	}
	// 🔴 revisions y shipping son c.intakeStore: otra cosa sería una segunda instancia del almacén
	// de solicitudes (la de D-F6-1, que murió en F8).
	ports := []struct {
		name string
		got  reflect.Value
		want any
	}{
		{"revisions del proyector del carrito", inner(t, cartProjector, "revisions"), c.intakeStore},
		{"shipping del proyector del carrito", inner(t, cartProjector, "shipping"), c.intakeStore},
		{"buyer del proyector del carrito", inner(t, cartProjector, "buyer"), c.buyerDataStore},
		{"store del proyector del carrito", inner(t, cartProjector, "store"), c.flowStore},
		{"store del proyector de la encuesta", inner(t, surveyProjector, "store"), c.flowStore},
	}
	for _, k := range ports {
		if !sameInstance(k.got, k.want) {
			t.Errorf("el puerto %s no es la MISMA instancia que el almacén del contenedor (%T)", k.name, k.want)
		}
	}
}

// TestCableado_TheRuntimeHasTheCartResumePolicy (D-F8-16): el mapa de políticas de reanudación
// del runtime construido tiene una entrada NO nil para el nodo del carrito, y es la
// *cart.ResumePolicy sobre c.flowStore. WithResumePolicy(tipo, nil) se ignora en silencio: sin
// la política, un carrito a medias se reanudaría con la regla por defecto.
func TestCableado_TheRuntimeHasTheCartResumePolicy(t *testing.T) {
	c := conversationRuntimeOfTheBoot(t)
	policies := field(t, c.flowRuntime, "resumePolicies")
	if policies.Len() != 1 {
		t.Errorf("el runtime tiene %d políticas de reanudación; el arranque cablea una, la del carrito", policies.Len())
	}
	policy := policies.MapIndex(reflect.ValueOf(cart.NodeTypeCart))
	if !policy.IsValid() || policy.IsNil() {
		t.Fatalf("el runtime no tiene política de reanudación para el nodo %q: WithResumePolicy no se cableó, "+
			"o recibió un nil (que se ignora en silencio)", cart.NodeTypeCart)
	}
	if got := policy.Elem().Type(); got != reflect.TypeFor[*cart.ResumePolicy]() {
		t.Fatalf("la política de reanudación del nodo %q es un %s; se espera *cart.ResumePolicy", cart.NodeTypeCart, got)
	}
	if !sameInstance(inner(t, policy, "store"), c.flowStore) {
		t.Error("la política de reanudación del carrito no lee de c.flowStore")
	}
}

// TestCableado_TheEngineHasItsQueryResolver (T-6): el engine construido tiene resolutor de
// consultas y es c.consultaResolver, el del turno acotado sobre el único selector de vía; y
// tiene observador. Sin resolutor el engine devuelve «sin_resolutor» y el carrito repromptea
// como siempre: ya pasó dos veces.
func TestCableado_TheEngineHasItsQueryResolver(t *testing.T) {
	c := conversationRuntimeOfTheBoot(t)
	if c.consultaResolver == nil {
		t.Fatal("la fase 5 no construyó el resolutor del turno acotado")
	}
	if !sameInstance(field(t, c.flowEngine, "queryResolver"), c.consultaResolver) {
		t.Error("el resolutor de consultas del engine no es c.consultaResolver: el tercer escalón del carrito " +
			"quedaría apagado (WithQueryResolver sin cablear, T-6)")
	}
	if field(t, c.flowEngine, "queryObserver").IsNil() {
		t.Error("el engine no tiene observador de consultas (WithQueryObserver): una degradación sería " +
			"indistinguible de un turno normal")
	}
	if !sameInstance(field(t, c.consultaResolver, "turner"), c.llmSelector) {
		t.Error("el resolutor del turno acotado no pregunta al c.llmSelector, el único selector de vía")
	}
	if field(t, c.flowEngine, "content").IsNil() {
		t.Error("el engine no tiene fuente de contenido (WithContentSource)")
	}
}

// TestCableado_TheAggregatorHasComposerAndAhead (AG-8): el agregador construido compone el sobre
// con c.intakeComposer —no con el noop de relleno, que cerraría las ventanas con el sobre a
// NULL— y pide el adelanto de la clasificación a c.intakeAhead; y escribe la ventana por
// c.intakeJobStore, LA cola. WithSourceComposer(nil) y WithAheadRequester(nil) se ignoran.
func TestCableado_TheAggregatorHasComposerAndAhead(t *testing.T) {
	c := conversationRuntimeOfTheBoot(t)
	if c.intakeAggregator == nil {
		t.Fatal("la fase 7 no construyó el agregador de ventanas")
	}
	if c.intakeComposer == nil || !sameInstance(field(t, c.intakeAggregator, "compose"), c.intakeComposer) {
		t.Errorf("el compositor del agregador es un %s y no c.intakeComposer: las ventanas cerrarían con el "+
			"sobre a NULL (WithSourceComposer sin cablear, o con nil)", conversationDynamicType(field(t, c.intakeAggregator, "compose")))
	}
	if c.intakeAhead == nil || !sameInstance(field(t, c.intakeAggregator, "ahead"), c.intakeAhead) {
		t.Errorf("quien pide el adelanto en el agregador es un %s y no c.intakeAhead: toda ventana cerraría por "+
			"su reloj (WithAheadRequester sin cablear, o con nil)", conversationDynamicType(field(t, c.intakeAggregator, "ahead")))
	}
	if !sameInstance(field(t, c.intakeAggregator, "jobs"), c.intakeJobStore) {
		t.Error("el agregador no escribe la ventana por c.intakeJobStore, la cola que leen el worker y las etapas")
	}
	if !sameInstance(field(t, c.intakeAggregator, "settings"), c.flowStore) {
		t.Error("el agregador no lee la ventana de agregación de c.flowStore")
	}
}

// conversationDynamicType rinde el tipo dinámico de un campo de interfaz, o «nil».
func conversationDynamicType(v reflect.Value) string {
	if v.Kind() == reflect.Interface {
		if v.IsNil() {
			return "nil"
		}
		v = v.Elem()
	}
	return v.Type().String()
}
