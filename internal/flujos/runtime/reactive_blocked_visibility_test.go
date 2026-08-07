package runtime_test

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/trigger"
)

// Visibilidad de los cortes del motor reactivo: los tres (passive, self-loop,
// rate-limit) son silenciosos por diseño y en el e2e del 2026-08-06 eso llevó a
// diagnosticar mal un escenario. Aquí se fija el contrato: se CUENTAN siempre, y el
// corte por passive además se ANUNCIA a INFO una vez por sesión (no en cada
// entrante: a ~2.000/hora inundaría el log).

// contadorCortes recoge los motivos que el runtime reporta por el hook. El runtime
// procesa entrantes concurrentes, así que el doble se sincroniza.
type contadorCortes struct {
	mu        sync.Mutex
	porMotivo map[string]int
}

func nuevoContadorCortes() *contadorCortes {
	return &contadorCortes{porMotivo: map[string]int{}}
}

func (c *contadorCortes) registrar(reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.porMotivo[reason]++
}

func (c *contadorCortes) total(reason string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.porMotivo[reason]
}

// logCapturado es un logger con el nivel POR DEFECTO (info) escribiendo a un
// buffer: el Debug NO llega al buffer, que es justo lo que se quiere medir —lo que
// vería un operador sin tocar la configuración—.
func logCapturado(buf *bytes.Buffer) logger.Logger {
	return logger.New(logger.WithWriter(buf))
}

// newPassiveRuntimeObservado arma un runtime cuya sesión resuelve passive, con el
// log capturado y el contador de cortes inyectado.
func newPassiveRuntimeObservado(t *testing.T, buf *bytes.Buffer, cnt *contadorCortes) *runtime.Runtime {
	t.Helper()
	repo := store.NewMemoryRepository()
	if _, err := repo.InsertDefinition(context.Background(), testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	ts := trigger.NewMemoryStore()
	if _, err := ts.Insert(context.Background(), keywordRule()); err != nil {
		t.Fatalf("insert regla: %v", err)
	}
	return runtime.New(repo, newEngine(), &fakeSender{},
		fakeResolver{tenantID: testTenant, role: "passive"},
		contact.NewMemoryResolver(repo), logCapturado(buf),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		runtime.WithReactiveBlockedHook(cnt.registrar))
}

// El corte por passive se anuncia a INFO la PRIMERA vez de cada sesión y calla las
// siguientes (a Debug), pero se CUENTA en todas. Sin esto, o el operador no ve nada
// con el nivel por defecto, o ve una línea por entrante.
func TestReactiveBlocked_PassiveSeAnunciaUnaVezPorSesion(t *testing.T) {
	var buf bytes.Buffer
	cnt := nuevoContadorCortes()
	rt := newPassiveRuntimeObservado(t, &buf, cnt)
	ctx := context.Background()

	for i, waID := range []string{"wamid.v1", "wamid.v2", "wamid.v3"} {
		if err := rt.HandleIncoming(ctx, testSession, incoming(testContact, "pedido", waID)); err != nil {
			t.Fatalf("HandleIncoming #%d: %v", i+1, err)
		}
	}

	// El contador NO se calla: los tres entrantes se cortaron por passive.
	if got := cnt.total("passive"); got != 3 {
		t.Fatalf("el contador debe registrar los 3 cortes por passive, registró %d", got)
	}
	// El log dice el motivo UNA vez, con el session_id (lo que el contador no puede
	// etiquetar sin disparar la cardinalidad).
	anuncios := strings.Count(buf.String(), "motor reactivo omitido")
	if anuncios != 1 {
		t.Fatalf("el corte por passive debe anunciarse UNA vez por sesión con el nivel por defecto, apareció %d veces:\n%s",
			anuncios, buf.String())
	}
	if !strings.Contains(buf.String(), testSession) {
		t.Fatalf("el anuncio debe llevar el session_id para saber qué sesión marcar como bot, log:\n%s", buf.String())
	}
}

// Una sesión passive NUEVA vuelve a anunciarse: el "una vez" es por sesión, no por
// proceso — si no, la segunda sesión mal configurada sería invisible.
func TestReactiveBlocked_PassiveAnunciaCadaSesionNueva(t *testing.T) {
	var buf bytes.Buffer
	cnt := nuevoContadorCortes()
	rt := newPassiveRuntimeObservado(t, &buf, cnt)
	ctx := context.Background()

	if err := rt.HandleIncoming(ctx, "sess-uno", incoming(testContact, "pedido", "wamid.s1")); err != nil {
		t.Fatalf("HandleIncoming sesión 1: %v", err)
	}
	if err := rt.HandleIncoming(ctx, "sess-dos", incoming(testContact, "pedido", "wamid.s2")); err != nil {
		t.Fatalf("HandleIncoming sesión 2: %v", err)
	}

	if anuncios := strings.Count(buf.String(), "motor reactivo omitido"); anuncios != 2 {
		t.Fatalf("cada sesión passive distinta debe anunciarse una vez (esperado 2), apareció %d veces:\n%s",
			anuncios, buf.String())
	}
	for _, s := range []string{"sess-uno", "sess-dos"} {
		if !strings.Contains(buf.String(), s) {
			t.Fatalf("falta el session_id %q en el log:\n%s", s, buf.String())
		}
	}
}

// El corte anti-self-loop comparte el contador, con su propio motivo: los tres
// cortes responden la MISMA pregunta del operador ("¿por qué no contesta?") y se
// leen juntos en wapp_flow_reactive_blocked_total.
func TestReactiveBlocked_SelfLoopCuentaConSuMotivo(t *testing.T) {
	cnt := nuevoContadorCortes()
	repo := store.NewMemoryRepository()
	ctx := context.Background()
	if _, err := repo.InsertDefinition(ctx, testTenant, sampleFlow()); err != nil {
		t.Fatalf("sembrar definición: %v", err)
	}
	ts := trigger.NewMemoryStore()
	if _, err := ts.Insert(ctx, keywordRule()); err != nil {
		t.Fatalf("insert regla: %v", err)
	}
	sender := &fakeSender{}
	rt := runtime.New(repo, newEngine(), sender, fakeResolver{tenantID: testTenant},
		contact.NewMemoryResolver(repo), discardLogger(),
		runtime.WithTriggerResolver(trigger.NewConfigResolver(ts)),
		runtime.WithSelfNumbers(fakeSelfNumbers{byTenant: map[string][]string{testTenant: {selfPnA}}}),
		runtime.WithReactiveBlockedHook(cnt.registrar))

	if err := rt.HandleIncoming(ctx, testSession, incomingPn(selfPnA, "pedido", "wamid.sl1")); err != nil {
		t.Fatalf("HandleIncoming self-loop: %v", err)
	}

	if sender.count() != 0 {
		t.Fatalf("no-regresión: un entrante desde un número propio no debe auto-responder, envió %d", sender.count())
	}
	if got := cnt.total("self_loop"); got != 1 {
		t.Fatalf("el corte anti-self-loop debe contarse con motivo self_loop, registró %d", got)
	}
	if got := cnt.total("passive"); got != 0 {
		t.Fatalf("el motivo no debe confundirse con passive, registró %d", got)
	}
}

// Sin hook inyectado los cortes se comportan igual (nil-safe): observar es opcional,
// decidir no. Cubre el arranque de cualquier consumidor que no cablee métricas.
func TestReactiveBlocked_SinHookNoRompe(t *testing.T) {
	rt, _, sender, _ := newRoleTriggerRuntime(t, "passive", keywordRule())
	if err := rt.HandleIncoming(context.Background(), testSession, incoming(testContact, "pedido", "wamid.nh")); err != nil {
		t.Fatalf("HandleIncoming sin hook: %v", err)
	}
	if sender.count() != 0 {
		t.Fatalf("una sesión passive no debería auto-responder, envió %d", sender.count())
	}
}
