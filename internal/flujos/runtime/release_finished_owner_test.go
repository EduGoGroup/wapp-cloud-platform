package runtime_test

// release_finished_owner_test.go — Plan 053 · Ola 2 (T2.3 y T2.4).
//
// Cubre la rama que la Ola 1 dejó HUÉRFANA y que NADIE miraba. El orden importa: la
// guarda de posesión de H2 alimentaba `conocido`, `conocido` alimentaba el efecto
// `event_escaped` y el scoping `ActiveEventKind`, y al retirarse la guarda (T1.6) la
// rama quedó muerta **sin un solo rojo que lo delatara**. T2.4 lo repone leyendo el
// evento ACTIVO, y estos tests son la red que faltaba.
//
// 🔴 POR QUÉ ESTE FICHERO EXISTE, dicho para quien lo lea dentro de un año: la pérdida
// no la encontró un gate — la encontró una revisión adversarial leyendo. Un test que
// no existe no se pone rojo, y esa es exactamente la forma en que esto se perdió.

import (
	"context"
	"sync"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
)

// --- espías --------------------------------------------------------------------

// efectoSpy captura los efectos de CICLO DE VIDA por nombre. Es un EventSink normal:
// no hay puerta trasera, ve exactamente lo que vería un consumidor de producción
// (el colector de métricas de flowlifecycle, por ejemplo).
type efectoSpy struct {
	mu      sync.Mutex
	nombres []string
}

func (e *efectoSpy) Handle(_ context.Context, _ runtime.EffectContext, eff modules.Effect) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.nombres = append(e.nombres, eff.Name)
	return nil
}

func (e *efectoSpy) tiene(nombre string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, n := range e.nombres {
		if n == nombre {
			return true
		}
	}
	return false
}

// resolverSpy envuelve al resolver REAL y anota el ActiveEventKind con el que se le
// consultó. Envuelve en vez de sustituir a propósito: así el test sigue ejercitando
// la resolución de verdad y solo observa el parámetro que T2.4 repone.
type resolverSpy struct {
	trigger.Resolver
	mu     sync.Mutex
	kinds  []string
	textos []string
}

func (r *resolverSpy) Resolve(ctx context.Context, tenantID, sessionID string, sig trigger.Signal) (trigger.Decision, error) {
	r.mu.Lock()
	r.kinds = append(r.kinds, sig.ActiveEventKind)
	r.textos = append(r.textos, sig.Text)
	r.mu.Unlock()
	return r.Resolver.Resolve(ctx, tenantID, sessionID, sig)
}

// kindDe devuelve el ActiveEventKind con el que se resolvió el texto dado.
func (r *resolverSpy) kindDe(texto string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, t := range r.textos {
		if t == texto {
			return r.kinds[i], true
		}
	}
	return "", false
}

// --- el escenario --------------------------------------------------------------

// o2Escenario deja la conversación en el estado exacto que T2.4 tiene que atender:
// un flow_state TERMINAL cuyo DUEÑO (el `cart`) ya se cerró y cuyo ACTIVO (el `menu`)
// sigue VIVO. Es el mismo montaje que flujo_ajeno_terminal_test.go —reusa su env— y
// devuelve el evento del menú, que es de quien hay que hablar.
func o2Escenario(t *testing.T, rt *runtime.Runtime, evs *memEventStore) events.Event {
	t.Helper()
	ctx := context.Background()
	for _, m := range []*cloudlinkv1.IncomingMessage{
		incoming(testContact, "carrito", "o2-1"), // nace el cart y su flujo espera
		incoming(testContact, "menu", "o2-2"),    // nace el menu y HEREDA ese flujo
		incoming(testContact, "1", "o2-3"),       // termina el flujo AJENO ⇒ cierra el DUEÑO
	} {
		if err := rt.HandleIncoming(ctx, testSession, m); err != nil {
			t.Fatalf("precondición %q: %v", m.GetText(), err)
		}
	}
	men := aliveOfKind(t, evs, "menu")
	if got := evs.statuses()[men.ID]; got != events.StatusOpen {
		t.Fatalf("precondición: el `menu` debe seguir VIVO tras cerrarse el dueño; quedó %q", got)
	}
	return men
}

// TestReleaseFinishedState_ReponeElEventEscapedDelMenuVivo (T2.4, criterio 1):
// al soltar la fila terminal, el efecto `event_escaped` VUELVE a emitirse sobre el
// evento ACTIVO que sigue vivo.
//
// Lo cuenta el colector de métricas de flowlifecycle, así que su ausencia no era
// cosmética: era un agujero en la telemetría que ningún test miraba.
func TestReleaseFinishedState_ReponeElEventEscapedDelMenuVivo(t *testing.T) {
	spy := &efectoSpy{}
	rt, _, _, contacts, evs := mudoH2EnvCon(t, runtime.WithEventSink(spy))
	_ = resolveID(t, contacts, testContact)
	men := o2Escenario(t, rt, evs)

	// El turno de suelta: un texto normal sobre la fila terminal.
	if err := rt.HandleIncoming(context.Background(), testSession,
		incoming(testContact, "hola que tal", "o2-4")); err != nil {
		t.Fatalf("turno de suelta: %v", err)
	}

	if !spy.tiene(runtime.EffectEventEscaped) {
		t.Fatalf("T2.4: al soltar la fila terminal debe emitirse %q sobre el evento ACTIVO vivo; "+
			"efectos vistos=%v\n"+
			"(si falta, la rama que la retirada de la guarda de H2 dejó muerta volvió a quedarse sin reponer)",
			runtime.EffectEventEscaped, spy.nombres)
	}
	// Y lo que se suelta es la FILA, no el evento: el `menu` sigue open (D-043.5).
	if got := evs.statuses()[men.ID]; got != events.StatusOpen {
		t.Fatalf("el `menu` debe seguir open tras soltar la fila; quedó %q", got)
	}
}

// TestReleaseFinishedState_ReponeElScopingConElKindDelMenu (T2.4, criterio 2):
// la señal con que se enruta el turno de suelta lleva ActiveEventKind="menu", no "".
func TestReleaseFinishedState_ReponeElScopingConElKindDelMenu(t *testing.T) {
	spy := &resolverSpy{}
	rt, _, _, contacts, evs := mudoH2EnvConResolver(t, spy)
	_ = resolveID(t, contacts, testContact)
	o2Escenario(t, rt, evs)

	if err := rt.HandleIncoming(context.Background(), testSession,
		incoming(testContact, "hola que tal", "o2-4")); err != nil {
		t.Fatalf("turno de suelta: %v", err)
	}

	got, visto := spy.kindDe("hola que tal")
	if !visto {
		t.Fatal("el turno de suelta debe enrutarse por el resolver (handleTrigger) y no se vio")
	}
	if got != "menu" {
		t.Fatalf("T2.4: ActiveEventKind = %q, quiero \"menu\" — el evento ACTIVO sigue vivo y su tipo "+
			"acota la interpretación de la señal (D-043.9)", got)
	}
}

// 🔴 TestReleaseFinishedState_ConKindVacioElScopingNoDESCARTA_ElAflojamiento
// (T2.4, la TERCERA cosa — la que el enunciado original de la tarea no censaba).
//
// Este test NO mide el runtime: mide el RESOLVER, y existe para dejar EJECUTABLE la
// razón por la que pasar "" no es «no acotar» sino **no filtrar**.
//
// La guarda de config_resolver.go es
//
//	if sig.ActiveEventKind != "" && r.EventKind != "" && r.EventKind != sig.ActiveEventKind { continue }
//
// Con ActiveEventKind == "" la condición NUNCA descarta, así que una regla `llm`
// anotada con un event_kind AJENO al evento vivo **casa igual**. Y si esa regla gana,
// la segunda puerta del Plan 054 · F2b (`winner.EventKind != ""`) devuelve
// Action: StartEvent — o sea, **arranca o conmuta un evento**. Por eso la pérdida de
// T2.4 no era solo «se pierde precisión»: era una HABILITACIÓN silenciosa.
func TestReleaseFinishedState_ConKindVacioElScopingNoDescarta_ElAflojamiento(t *testing.T) {
	ctx := context.Background()
	ts := trigger.NewMemoryStore()
	// Una regla llm ANOTADA con event_kind "cart": solo debería casar cuando el evento
	// vivo es un cart.
	if _, err := ts.Insert(ctx, trigger.Rule{
		TenantID: testTenant, Kind: trigger.KindLLM, Keyword: "quiero_pedir",
		MatchType: trigger.MatchExact, EventKind: "cart", FlowID: testFlow, Enabled: true,
	}); err != nil {
		t.Fatalf("sembrar regla llm anotada: %v", err)
	}
	res := trigger.NewConfigResolver(ts)
	sig := func(kind string) trigger.Signal {
		return trigger.Signal{
			Text:            "lo que sea",
			ActiveEventKind: kind,
			Intent:          &trigger.IntentSignal{Name: "quiero_pedir", Confidence: 0.99},
		}
	}

	// (a) con el kind REAL del evento vivo ("menu"), la regla de `cart` se DESCARTA.
	dec, err := res.Resolve(ctx, testTenant, testSession, sig("menu"))
	if err != nil {
		t.Fatalf("Resolve con kind menu: %v", err)
	}
	if dec.Action == trigger.StartEvent {
		t.Fatalf("con ActiveEventKind=\"menu\" una regla llm anotada con event_kind=\"cart\" NO debe casar; "+
			"decisión=%+v", dec)
	}

	// (b) con el kind VACÍO —lo que llegaba mientras la rama estuvo muerta— la MISMA
	// regla casa y abre la segunda puerta del 054 · F2b.
	dec, err = res.Resolve(ctx, testTenant, testSession, sig(""))
	if err != nil {
		t.Fatalf("Resolve con kind vacío: %v", err)
	}
	if dec.Action != trigger.StartEvent || dec.EventKind != "cart" {
		t.Fatalf("PREMISA DEL HALLAZGO ROTA: con ActiveEventKind=\"\" la guarda de scoping no descarta, "+
			"así que la regla anotada DEBE casar y abrir la segunda puerta (StartEvent/cart); decisión=%+v\n"+
			"(si esto cambió, el aflojamiento que T2.4 repara ya no existe y este test sobra — "+
			"pero compruébalo antes de borrarlo)", dec)
	}
}
