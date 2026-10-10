//go:build pendiente

package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations/crmpush"
)

// webhookEffectName es el efecto que entregan los sinks de estos tests: el literal que el
// arranque inyecta desde el módulo del carrito.
const webhookEffectName = "cart_closed"

// webhookCtxKey marca el contexto de la llamada para comprobar que llega tal cual al gate
// y al almacén.
type webhookCtxKey struct{}

func webhookCtx() context.Context {
	return context.WithValue(context.Background(), webhookCtxKey{}, "marked")
}

// webhookGateFake deja pasar o bloquea por tenant; sin entrada trata como cerrado. Apunta
// cada consulta para poder contarlas.
type webhookGateFake struct {
	mu     sync.Mutex
	open   map[string]bool
	err    error
	asked  []string
	marked []bool
}

func openWebhookGate(tenants ...string) *webhookGateFake {
	g := &webhookGateFake{open: map[string]bool{}}
	for _, tenant := range tenants {
		g.open[tenant] = true
	}
	return g
}

func (g *webhookGateFake) Enabled(ctx context.Context, tenantID string) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.asked = append(g.asked, tenantID)
	g.marked = append(g.marked, ctx.Value(webhookCtxKey{}) == "marked")
	if g.err != nil {
		return false, g.err
	}
	return g.open[tenantID], nil
}

func (g *webhookGateFake) calls() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.asked...)
}

// ctxMarks dice, por consulta, si el gate recibió el ctx marcado.
func (g *webhookGateFake) ctxMarks() []bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]bool(nil), g.marked...)
}

// webhookQueueCall es un INSERT tal como llegó al doble.
type webhookQueueCall struct {
	marked   bool
	tenantID string
	kind     string
	payload  json.RawMessage
}

// webhookQueueFake graba cada INSERT sin tocar Postgres. Los ids salen de 41 en adelante,
// para que un outbox_id inventado no acierte por casualidad.
type webhookQueueFake struct {
	mu    sync.Mutex
	err   error
	calls []webhookQueueCall
}

const webhookFirstOutboxID = int64(41)

func (q *webhookQueueFake) EnqueueWebhook(ctx context.Context, tenantID, kind string, payload json.RawMessage) (int64, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.err != nil {
		return 0, q.err
	}
	q.calls = append(q.calls, webhookQueueCall{
		marked: ctx.Value(webhookCtxKey{}) == "marked", tenantID: tenantID, kind: kind, payload: payload,
	})
	return webhookFirstOutboxID + int64(len(q.calls)) - 1, nil
}

func (q *webhookQueueFake) recorded() []webhookQueueCall {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]webhookQueueCall(nil), q.calls...)
}

// Aserciones de compilación: el sink es un EventSink con fase, y los dos puertos son ALIAS
// de los de crmpush (dos tipos función solo son idénticos si sus parámetros lo son).
var (
	_ EventSink           = (*WebhookSink)(nil)
	_ PhasedSink          = (*WebhookSink)(nil)
	_ WebhookQueuer       = (*webhookQueueFake)(nil)
	_ WebhookGate         = (*webhookGateFake)(nil)
	_ func(WebhookQueuer) = func(crmpush.Queuer) {}
	_ func(WebhookGate)   = func(crmpush.Gate) {}
)

// webhookContext es la conversación que cierra el carrito. Sesión, flujo y evento llevan
// literales raros para poder buscar que NO viajan.
func webhookContext() EffectContext {
	return EffectContext{
		TenantID: "t-1", ContactID: "c-opaque", SessionID: "session-zzq",
		FlowID: "flow-zzq", FlowVersion: 3, EventID: "event-zzq", Durable: true,
	}
}

// webhookCartClosed es el efecto de cierre con DOS líneas, una personalizada y otra no, tal
// como llega al sink: el proyector ya anotó intake_id, revision_no y lifecycle_status. El
// revision_no es 2 y no 1 para que un literal 1 reintroducido no pase por casualidad, y el
// estado es la clave legada `closed`.
func webhookCartClosed() modules.Effect {
	return modules.Effect{
		Kind: "persist",
		Name: webhookEffectName,
		Payload: map[string]any{
			"intake_id":        "intake-abc-123",
			"revision_no":      2,
			"lifecycle_status": "closed",
			"items": []map[string]any{
				{"sku": "A1", "label": "Café", "customization": "sin azúcar", "qty": 2, "unit_price": 9.9},
				{"sku": "B2", "label": "Té", "qty": 1, "unit_price": 5.0},
			},
			"total": 24.8,
		},
	}
}

// deliver corre el sink por el camino de entrega (gate abierto) y devuelve el ÚNICO
// documento encolado, decodificado como mapa para leer cada campo por su nombre de cable.
func deliver(t *testing.T, eff modules.Effect) (json.RawMessage, map[string]any) {
	t.Helper()
	queue := &webhookQueueFake{}
	sink := NewWebhookSink(newSinkLogRecorder(), webhookEffectName, queue, openWebhookGate("t-1"))

	if err := sink.Handle(webhookCtx(), webhookContext(), eff); err != nil {
		t.Fatalf("Handle devolvió %v; el sink no aborta nunca", err)
	}
	calls := queue.recorded()
	if len(calls) != 1 {
		t.Fatalf("se encoló %d veces, quería 1", len(calls))
	}
	var doc map[string]any
	if err := json.Unmarshal(calls[0].payload, &doc); err != nil {
		t.Fatalf("el documento encolado no es JSON válido: %v", err)
	}
	return calls[0].payload, doc
}

// TestNewWebhookSink_NeverNilAndIdle: el constructor devuelve siempre un sink, también sin
// dependencias, y construir no consulta el gate ni encola.
func TestNewWebhookSink_NeverNilAndIdle(t *testing.T) {
	if NewWebhookSink(nil, "", nil, nil) == nil {
		t.Error("NewWebhookSink sin dependencias devolvió nil")
	}
	gate, queue := openWebhookGate("t-1"), &webhookQueueFake{}
	if NewWebhookSink(newSinkLogRecorder(), webhookEffectName, queue, gate) == nil {
		t.Fatal("NewWebhookSink devolvió nil")
	}
	if n := len(gate.calls()) + len(queue.recorded()); n != 0 {
		t.Errorf("construir el sink tocó sus dependencias %d veces, quería 0", n)
	}
}

// TestWebhookSink_Phase_IsNotify: el sink corre después de toda la proyección (RT-18), lo
// declare quien lo declare: uno completo, uno sin dependencias y un receptor nil.
func TestWebhookSink_Phase_IsNotify(t *testing.T) {
	var nilSink *WebhookSink
	cases := []struct {
		name string
		sink *WebhookSink
	}{
		{"wired sink", NewWebhookSink(newSinkLogRecorder(), webhookEffectName, &webhookQueueFake{}, openWebhookGate())},
		{"sink without dependencies", NewWebhookSink(nil, "", nil, nil)},
		{"nil receiver", nilSink},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.sink.Phase(); got != PhaseNotify {
				t.Errorf("Phase() = %d, quería PhaseNotify (%d)", got, PhaseNotify)
			}
		})
	}
}

// TestWebhookSink_Handle_InertWithoutLogger: receptor nil, valor cero y sink con logger
// nil devuelven nil sin consultar el gate ni encolar.
func TestWebhookSink_Handle_InertWithoutLogger(t *testing.T) {
	gate, queue := openWebhookGate("t-1"), &webhookQueueFake{}
	var nilSink *WebhookSink
	cases := []struct {
		name string
		sink *WebhookSink
	}{
		{"nil receiver", nilSink},
		{"zero value", &WebhookSink{}},
		{"built with nil logger", NewWebhookSink(nil, webhookEffectName, queue, gate)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.sink.Handle(webhookCtx(), webhookContext(), webhookCartClosed()); err != nil {
				t.Errorf("Handle devolvió %v, quería nil", err)
			}
		})
	}
	if n := len(gate.calls()) + len(queue.recorded()); n != 0 {
		t.Errorf("un sink inerte tocó sus dependencias %d veces, quería 0", n)
	}
}

// TestWebhookSink_Handle_OtherEffectsAreNotDelivered: solo se entrega el efecto cuyo
// nombre es EXACTAMENTE el inyectado; el resto no llega al gate ni al almacén, ni deja log.
func TestWebhookSink_Handle_OtherEffectsAreNotDelivered(t *testing.T) {
	for _, name := range []string{"category_selected", "item_added", "cart_closed ", "CART_CLOSED", "cart", ""} {
		t.Run("name="+name, func(t *testing.T) {
			gate, queue, log := openWebhookGate("t-1"), &webhookQueueFake{}, newSinkLogRecorder()
			sink := NewWebhookSink(log, webhookEffectName, queue, gate)
			eff := webhookCartClosed()
			eff.Name = name

			if err := sink.Handle(webhookCtx(), webhookContext(), eff); err != nil {
				t.Fatalf("Handle devolvió %v, quería nil", err)
			}
			if n := len(gate.calls()); n != 0 {
				t.Errorf("se consultó el gate %d veces para un efecto que no se entrega", n)
			}
			if n := len(queue.recorded()); n != 0 {
				t.Errorf("se encoló %d veces un efecto que no se entrega", n)
			}
			if lines := log.all(); len(lines) != 0 {
				t.Errorf("un efecto que no se entrega dejó log: %+v", lines)
			}
		})
	}
}

// TestWebhookSink_Handle_DeliversTheInjectedEffect: el nombre se INYECTA; un sink cableado
// con otro nombre entrega ese y deja pasar de largo cart_closed.
func TestWebhookSink_Handle_DeliversTheInjectedEffect(t *testing.T) {
	queue := &webhookQueueFake{}
	sink := NewWebhookSink(newSinkLogRecorder(), "order_done", queue, openWebhookGate("t-1"))

	if err := sink.Handle(webhookCtx(), webhookContext(), webhookCartClosed()); err != nil {
		t.Fatalf("Handle(cart_closed) devolvió %v", err)
	}
	if n := len(queue.recorded()); n != 0 {
		t.Fatalf("se encoló cart_closed %d veces en un sink que entrega order_done", n)
	}

	eff := webhookCartClosed()
	eff.Name = "order_done"
	if err := sink.Handle(webhookCtx(), webhookContext(), eff); err != nil {
		t.Fatalf("Handle(order_done) devolvió %v", err)
	}
	if n := len(queue.recorded()); n != 1 {
		t.Errorf("el efecto inyectado se encoló %d veces, quería 1", n)
	}
}

// TestWebhookSink_Handle_MissingDependencyIsNoOp: sin almacén o sin gate, el sink no hace
// nada: ni consulta, ni encola, ni loguea un encolado que no ocurrió.
func TestWebhookSink_Handle_MissingDependencyIsNoOp(t *testing.T) {
	cases := []struct {
		name      string
		withQueue bool
		withGate  bool
	}{
		{"without queuer", false, true},
		{"without gate", true, false},
		{"without both", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gate, queue, log := openWebhookGate("t-1"), &webhookQueueFake{}, newSinkLogRecorder()
			var (
				queuer WebhookQueuer
				gater  WebhookGate
			)
			if c.withQueue {
				queuer = queue
			}
			if c.withGate {
				gater = gate
			}

			err := NewWebhookSink(log, webhookEffectName, queuer, gater).Handle(webhookCtx(), webhookContext(), webhookCartClosed())
			if err != nil {
				t.Fatalf("Handle devolvió %v, quería nil", err)
			}
			if n := len(gate.calls()) + len(queue.recorded()); n != 0 {
				t.Errorf("un sink a medias tocó sus dependencias %d veces, quería 0", n)
			}
			if lines := log.all(); len(lines) != 0 {
				t.Errorf("un sink a medias dejó log: %+v", lines)
			}
		})
	}
}

// TestWebhookSink_Handle_ClosedGateDoesNotEnqueue: tenant sin puente CRM activo. Se
// consulta el gate una vez con su tenant, no se encola, y no es una avería: nada en Error
// ni en Warn.
func TestWebhookSink_Handle_ClosedGateDoesNotEnqueue(t *testing.T) {
	gate, queue, log := openWebhookGate("t-other"), &webhookQueueFake{}, newSinkLogRecorder()
	sink := NewWebhookSink(log, webhookEffectName, queue, gate)

	if err := sink.Handle(webhookCtx(), webhookContext(), webhookCartClosed()); err != nil {
		t.Fatalf("Handle devolvió %v, quería nil", err)
	}

	if calls := gate.calls(); len(calls) != 1 || calls[0] != "t-1" {
		t.Errorf("consultas al gate = %v, quería una sola con el tenant t-1", calls)
	}
	if n := len(queue.recorded()); n != 0 {
		t.Errorf("gate cerrado encoló %d veces", n)
	}
	if lines := append(log.at("error"), log.at("warn")...); len(lines) != 0 {
		t.Errorf("un gate cerrado no es una avería y dejó: %+v", lines)
	}
}

// requireFailureLine exige la línea de fallo del sink: una sola en Error, con el mensaje
// literal, el tenant, el nombre del efecto y el error de origen en la cadena.
func requireFailureLine(t *testing.T, log *sinkLogRecorder, cause error) {
	t.Helper()
	lines := log.at("error")
	if len(lines) != 1 {
		t.Fatalf("líneas en Error = %d, quería 1: %+v", len(lines), lines)
	}
	line := lines[0]
	if want := "webhook: no se pudo encolar intake.push"; line.msg != want {
		t.Errorf("mensaje = %q, quería %q", line.msg, want)
	}
	if line.fields["tenant"] != "t-1" || line.fields["name"] != webhookEffectName {
		t.Errorf("claves tenant/name = %v/%v, quería t-1/%s", line.fields["tenant"], line.fields["name"], webhookEffectName)
	}
	logged, ok := line.fields["error"].(error)
	if !ok || !errors.Is(logged, cause) {
		t.Errorf("clave error = %#v, quería un error que envuelva %v", line.fields["error"], cause)
	}
	if len(line.fields) != 3 {
		t.Errorf("la línea de fallo lleva %d claves, quería error, tenant y name: %v", len(line.fields), line.fields)
	}
}

// TestWebhookSink_Handle_GateErrorFailsClosed: un gate en error cuenta como «no», no
// aborta el flujo y deja la línea de fallo.
func TestWebhookSink_Handle_GateErrorFailsClosed(t *testing.T) {
	errGate := errors.New("resolver down")
	gate, queue, log := openWebhookGate("t-1"), &webhookQueueFake{}, newSinkLogRecorder()
	gate.err = errGate

	if err := NewWebhookSink(log, webhookEffectName, queue, gate).Handle(webhookCtx(), webhookContext(), webhookCartClosed()); err != nil {
		t.Fatalf("Handle devolvió %v; un error del gate no aborta el flujo", err)
	}

	if n := len(queue.recorded()); n != 0 {
		t.Errorf("un gate en error encoló %d veces (fail-closed)", n)
	}
	requireFailureLine(t, log, errGate)
}

// TestWebhookSink_Handle_QueueErrorDoesNotAbort: si el almacén falla, Handle sigue
// devolviendo nil y deja la línea de fallo.
func TestWebhookSink_Handle_QueueErrorDoesNotAbort(t *testing.T) {
	errQueue := errors.New("connection lost")
	queue, log := &webhookQueueFake{err: errQueue}, newSinkLogRecorder()

	if err := NewWebhookSink(log, webhookEffectName, queue, openWebhookGate("t-1")).Handle(webhookCtx(), webhookContext(), webhookCartClosed()); err != nil {
		t.Fatalf("Handle devolvió %v; un fallo de encolado no aborta el flujo", err)
	}

	requireFailureLine(t, log, errQueue)
	if lines := log.at("debug"); len(lines) != 0 {
		t.Errorf("se logueó un encolado que no ocurrió: %+v", lines)
	}
}

// TestWebhookSink_Handle_OpenGateEnqueuesOnce: el camino feliz. Una consulta al gate, UNA
// fila con el tenant y el kind del contrato, y el mismo ctx hasta las dos dependencias.
func TestWebhookSink_Handle_OpenGateEnqueuesOnce(t *testing.T) {
	gate, queue := openWebhookGate("t-1"), &webhookQueueFake{}

	if err := NewWebhookSink(newSinkLogRecorder(), webhookEffectName, queue, gate).Handle(webhookCtx(), webhookContext(), webhookCartClosed()); err != nil {
		t.Fatalf("Handle devolvió %v", err)
	}

	if calls := gate.calls(); len(calls) != 1 || calls[0] != "t-1" {
		t.Errorf("consultas al gate = %v, quería una sola con el tenant t-1", calls)
	}
	if marked := gate.ctxMarks(); len(marked) != 1 || !marked[0] {
		t.Error("el gate no recibió el ctx de la llamada")
	}
	calls := queue.recorded()
	if len(calls) != 1 {
		t.Fatalf("gate abierto encoló %d veces, quería exactamente 1", len(calls))
	}
	if calls[0].tenantID != "t-1" || calls[0].kind != "intake.push" {
		t.Errorf("fila encolada = tenant %q, kind %q; quería t-1 e intake.push", calls[0].tenantID, calls[0].kind)
	}
	if !calls[0].marked {
		t.Error("el almacén no recibió el ctx de la llamada")
	}
}

// TestWebhookSink_Handle_LogsTheEnqueueInDebug: encolar deja UNA línea Debug con el
// mensaje literal y tres claves —el tenant, el id que dio el almacén y el intake_id—, y
// nada en Error.
func TestWebhookSink_Handle_LogsTheEnqueueInDebug(t *testing.T) {
	log := newSinkLogRecorder()

	if err := NewWebhookSink(log, webhookEffectName, &webhookQueueFake{}, openWebhookGate("t-1")).Handle(webhookCtx(), webhookContext(), webhookCartClosed()); err != nil {
		t.Fatalf("Handle devolvió %v", err)
	}

	if lines := log.at("error"); len(lines) != 0 {
		t.Errorf("el camino feliz dejó líneas en Error: %+v", lines)
	}
	lines := log.at("debug")
	if len(lines) != 1 {
		t.Fatalf("líneas en Debug = %d, quería 1: %+v", len(lines), lines)
	}
	line := lines[0]
	if want := "webhook: intake.push encolado"; line.msg != want {
		t.Errorf("mensaje = %q, quería %q", line.msg, want)
	}
	want := map[string]any{"tenant": "t-1", "outbox_id": webhookFirstOutboxID, "intake_id": "intake-abc-123"}
	if len(line.fields) != len(want) {
		t.Errorf("la línea lleva %d claves, quería %d: %v", len(line.fields), len(want), line.fields)
	}
	for key, value := range want {
		if got, ok := line.fields[key]; !ok || got != value {
			t.Errorf("clave %q = %#v (presente=%v), quería %#v", key, got, ok, value)
		}
	}
}

// TestWebhookSink_Handle_EnqueuedDocumentIsTheContract: el documento entero, byte a byte:
// las diez claves del contrato en su orden, el estado ya normalizado, las dos líneas con
// sus cinco campos y el dinero intacto. El timestamp es el del reloj de crmpush (el sink no
// lo inyecta): se exige RFC3339 en UTC y se toma tal cual para comparar el resto.
func TestWebhookSink_Handle_EnqueuedDocumentIsTheContract(t *testing.T) {
	body, doc := deliver(t, webhookCartClosed())

	stamp, ok := doc["timestamp"].(string)
	if !ok {
		t.Fatalf("timestamp = %#v, quería una cadena", doc["timestamp"])
	}
	at, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		t.Fatalf("timestamp %q no es RFC3339: %v", stamp, err)
	}
	if _, offset := at.Zone(); offset != 0 {
		t.Errorf("timestamp %q no está en UTC", stamp)
	}

	want := `{"contract_version":"1","verb":"intake.push","tenant":"t-1","contact":"c-opaque",` +
		`"intake_id":"intake-abc-123","lifecycle_status":"confirmed","revision_no":2,` +
		`"items":[{"sku":"A1","label":"Café","customization":"sin azúcar","qty":2,"unit_price":9.9},` +
		`{"sku":"B2","label":"Té","customization":"","qty":1,"unit_price":5}],` +
		`"total":24.8,"timestamp":"` + stamp + `"}`
	if string(body) != want {
		t.Errorf("el documento encolado no es el del contrato\n got: %s\nwant: %s", body, want)
	}
}
