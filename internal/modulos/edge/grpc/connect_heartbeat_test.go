package grpc

// Lo que el latido persiste y renueva (R-G6, R-G18), parte a parte y por dentro:
// connect_heartbeat.go no tiene exportados, así que nace en verde (T-17) y se prueba llamando
// a lo que llamará el job del latido de connect_route.go. Aquí van el logout, la salud, el
// parte de inferencia y el banco común; el número propio y el tope de dispositivos están en
// connect_heartbeat_selfpn_test.go y la renovación del lease en connect_heartbeat_lease_test.go.
//
// Solo se afirma lo que lleva regla: qué se traduce y cómo, cuándo NO se toca nada, y qué
// queda en el log (sin PII). Que el job las llame, y en qué orden, es de connect_route.go.

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet/fleethelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/inferstats"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
)

// fleetCall es una escritura (o el conteo) que el gateway le pidió a fleet, con sus
// argumentos tal como llegaron.
type fleetCall struct {
	method                              string
	tenantID, edgeID, sessionID, selfPn string
}

// spyFleet es un fleet.Repository sobre Memoria que apunta las cuatro llamadas del latido
// (SetSelfPn, CountLiveBySelfPn, MarkLoggedOut, SaveHealth) con lo que recibieron —Memoria
// vuelve a normalizar el número por su cuenta, así que solo aquí se ve lo que mandó el
// gateway— y puede hacer fallar cualquiera de ellas por su nombre.
type spyFleet struct {
	*fleethelpertest.Memoria
	fail map[string]error

	mu     sync.Mutex
	calls  []fleetCall
	health []fleet.HealthSnapshot
}

func newSpyFleet() *spyFleet {
	return &spyFleet{Memoria: fleethelpertest.NewMemoria(), fail: map[string]error{}}
}

func (f *spyFleet) note(c fleetCall) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
	return f.fail[c.method]
}

func (f *spyFleet) recorded() []fleetCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fleetCall(nil), f.calls...)
}

func (f *spyFleet) SetSelfPn(ctx context.Context, tenantID, edgeID, sessionID, selfPn string) error {
	if err := f.note(fleetCall{"SetSelfPn", tenantID, edgeID, sessionID, selfPn}); err != nil {
		return err
	}
	return f.Memoria.SetSelfPn(ctx, tenantID, edgeID, sessionID, selfPn)
}

func (f *spyFleet) CountLiveBySelfPn(ctx context.Context, tenantID, selfPn string) (int, error) {
	if err := f.note(fleetCall{method: "CountLiveBySelfPn", tenantID: tenantID, selfPn: selfPn}); err != nil {
		return 0, err
	}
	return f.Memoria.CountLiveBySelfPn(ctx, tenantID, selfPn)
}

func (f *spyFleet) MarkLoggedOut(ctx context.Context, tenantID, edgeID, sessionID string) error {
	if err := f.note(fleetCall{method: "MarkLoggedOut", tenantID: tenantID, edgeID: edgeID, sessionID: sessionID}); err != nil {
		return err
	}
	return f.Memoria.MarkLoggedOut(ctx, tenantID, edgeID, sessionID)
}

func (f *spyFleet) SaveHealth(ctx context.Context, tenantID, edgeID, sessionID string, h fleet.HealthSnapshot) error {
	f.mu.Lock()
	f.health = append(f.health, h)
	f.mu.Unlock()
	if err := f.note(fleetCall{method: "SaveHealth", tenantID: tenantID, edgeID: edgeID, sessionID: sessionID}); err != nil {
		return err
	}
	return f.Memoria.SaveHealth(ctx, tenantID, edgeID, sessionID, h)
}

// heartbeatRig es un Server con fleet (el espía), su Registry y un log que captura debug.
type heartbeatRig struct {
	srv   *Server
	reg   *session.Registry
	fleet *spyFleet
	log   *logBuffer
}

func newHeartbeatRig(opts ...Option) *heartbeatRig {
	log, buf := debugLog()
	reg := session.NewRegistry()
	spy := newSpyFleet()
	return &heartbeatRig{srv: New(reg, log, append([]Option{WithFleet(spy)}, opts...)...), reg: reg, fleet: spy, log: buf}
}

// online deja la sesión registrada en fleet, como hace el handshake antes del primer latido.
func (r *heartbeatRig) online(t *testing.T, cc connCtx) {
	t.Helper()
	if err := r.fleet.MarkOnline(t.Context(), cc.tenantID, cc.edgeID, cc.sessionID); err != nil {
		t.Fatalf("MarkOnline(%s): %v", cc.sessionID, err)
	}
}

// row devuelve la fila de flota de la sesión.
func (r *heartbeatRig) row(t *testing.T, cc connCtx) fleet.Session {
	t.Helper()
	s, found, err := r.fleet.Get(t.Context(), cc.tenantID, cc.edgeID, cc.sessionID)
	if err != nil || !found {
		t.Fatalf("Get(%s) = (found=%v, err=%v), se esperaba la fila", cc.sessionID, found, err)
	}
	return s
}

func (r *heartbeatRig) requireLog(t *testing.T, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !r.log.contains(want) {
			t.Errorf("al log le falta %q: %q", want, r.log.String())
		}
	}
}

// degradedChannels son los tres connCtx con los que una escritura en fleet no tiene a quién
// atribuirse: sin identidad mTLS o sin session_id. La cuarta degradación —sin fleet— se
// prueba aparte porque es del Server.
func degradedChannels() map[string]connCtx {
	anonymous := phone("tenant-1", "edge-1", "s-1")
	anonymous.hasIdentity = false
	return map[string]connCtx{
		"without identity":   anonymous,
		"without session id": phone("tenant-1", "edge-1", ""),
	}
}

// fullHeartbeat es un latido con todo: número, salud, parte de inferencia y contador.
func fullHeartbeat() *cloudlinkv1.Heartbeat {
	return &cloudlinkv1.Heartbeat{
		SelfPn:       "573001112233",
		LeaseCounter: 7,
		SessionHealth: &cloudlinkv1.SessionHealth{
			WhatsappSocketState: cloudlinkv1.WhatsappSocketState_WHATSAPP_SOCKET_STATE_CONNECTED,
			InferenceByClass:    map[string]int64{"lote": 1},
		},
	}
}

// Sin identidad mTLS o sin session_id, ninguna de las tres escrituras del latido toca fleet,
// y ninguna deja rastro: es una degradación, no un fallo.
func TestHeartbeatWritesNothingToFleetOnADegradedChannel(t *testing.T) {
	t.Parallel()
	for name, cc := range degradedChannels() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rig := newHeartbeatRig()
			ctx := context.Background()

			rig.srv.persistSelfPn(ctx, cc, fullHeartbeat())
			rig.srv.persistHealth(ctx, cc, fullHeartbeat())
			rig.srv.markLoggedOut(ctx, cc)

			if got := rig.fleet.recorded(); len(got) != 0 {
				t.Errorf("se llamó a fleet: %+v", got)
			}
			if rig.log.String() != "" {
				t.Errorf("la degradación no deja rastro: %q", rig.log.String())
			}
		})
	}
}

// Sin fleet inyectado las tres son no-op (no revientan con el puerto nil).
func TestHeartbeatWithoutFleetIsANoOp(t *testing.T) {
	t.Parallel()
	log, buf := debugLog()
	srv := New(session.NewRegistry(), log)
	cc := phone("tenant-1", "edge-1", "s-1")

	srv.persistSelfPn(context.Background(), cc, fullHeartbeat())
	srv.persistHealth(context.Background(), cc, fullHeartbeat())
	srv.markLoggedOut(context.Background(), cc)

	if buf.String() != "" {
		t.Errorf("sin fleet no hay nada que decir: %q", buf.String())
	}
}

// El logout anunciado por el Edge deja la sesión ZOMBIE (loggedout, distinto de offline) y lo
// dice en Info; no toca nada más.
func TestMarkLoggedOutLeavesTheSessionAsZombie(t *testing.T) {
	t.Parallel()
	rig := newHeartbeatRig()
	cc := phone("tenant-1", "edge-1", "s-1")
	rig.online(t, cc)

	rig.srv.markLoggedOut(context.Background(), cc)

	if got := rig.row(t, cc).State; got != fleet.StateLoggedOut {
		t.Errorf("estado = %q, se esperaba %q", got, fleet.StateLoggedOut)
	}
	want := []fleetCall{{method: "MarkLoggedOut", tenantID: "tenant-1", edgeID: "edge-1", sessionID: "s-1"}}
	if got := rig.fleet.recorded(); !reflect.DeepEqual(got, want) {
		t.Errorf("llamadas a fleet = %+v, se esperaba %+v", got, want)
	}
	rig.requireLog(t, "level=INFO", `msg="heartbeat: la sesión reportó logout de WhatsApp; marcada zombie"`, "session_id=s-1", "edge_id=edge-1")
}

// Si fleet falla al marcar el logout se registra el error con IDs opacos y no se propaga.
func TestMarkLoggedOutLogsAFleetFailure(t *testing.T) {
	t.Parallel()
	rig := newHeartbeatRig()
	rig.fleet.fail["MarkLoggedOut"] = errors.New("base caída")

	rig.srv.markLoggedOut(context.Background(), phone("tenant-1", "edge-1", "s-1"))

	rig.requireLog(t, "level=ERROR", `msg="fleet: marcar loggedout"`, "base caída", "edge_id=edge-1", "session_id=s-1")
}

func int64Ptr(v int64) *int64 { return &v }

// La salud del latido llega a fleet campo a campo. Las tres traducciones que llevan regla:
// el enum del socket pasa a su texto canónico; intent_p50_ms 0 es «no medible» (nil), no
// «0 ms»; y los cuatro contadores del despachador van SIEMPRE con puntero, también en cero
// (proto3 no distingue ahí «no ocurrió» de «no lo mide»). El mapa de omitidos va tal cual.
func TestPersistHealthTranslatesTheSnapshot(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   *cloudlinkv1.SessionHealth
		want fleet.HealthSnapshot
	}{
		{
			name: "degraded edge with every field",
			in: &cloudlinkv1.SessionHealth{
				WhatsappSocketState:   cloudlinkv1.WhatsappSocketState_WHATSAPP_SOCKET_STATE_DEAD,
				DegradedReason:        "dek_load_timeout",
				LastInboundEventAgeS:  1860,
				DekLoadDurationMs:     10000,
				IntentCircuit:         "open",
				OutboxDepth:           3,
				BinaryVersion:         "v0.9.0",
				DaemonUptimeS:         7200,
				WorkerTaskset:         "disjunta",
				IntentP50Ms:           420,
				IntentOmittedByReason: map[string]int64{"sin_capacidad": 4, "circuito": 1},
				StuckHeads:            2,
				StuckHeadPolls:        9,
				FailedSealDispatch:    1,
				FailedSealBudget:      5,
			},
			want: fleet.HealthSnapshot{
				WhatsappState: "dead", DegradedReason: "dek_load_timeout", LastEventAgeS: 1860,
				DekLoadDurationMs: 10000, IntentCircuit: "open", OutboxDepth: 3, BinaryVersion: "v0.9.0", UptimeS: 7200,
				WorkerTaskset: "disjunta", IntentP50Ms: int64Ptr(420),
				IntentOmittedByReason: map[string]int64{"sin_capacidad": 4, "circuito": 1},
				StuckHeads:            int64Ptr(2), StuckHeadPolls: int64Ptr(9),
				FailedSealDispatch: int64Ptr(1), FailedSealBudget: int64Ptr(5),
			},
		},
		{
			name: "empty health block: unknown p50 is nil, the four counters are a measured zero",
			in:   &cloudlinkv1.SessionHealth{},
			want: fleet.HealthSnapshot{
				StuckHeads: int64Ptr(0), StuckHeadPolls: int64Ptr(0),
				FailedSealDispatch: int64Ptr(0), FailedSealBudget: int64Ptr(0),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rig := newHeartbeatRig()
			cc := phone("tenant-1", "edge-1", "s-1")
			rig.online(t, cc)

			rig.srv.persistHealth(context.Background(), cc, &cloudlinkv1.Heartbeat{SessionHealth: tc.in})

			if len(rig.fleet.health) != 1 || !reflect.DeepEqual(rig.fleet.health[0], tc.want) {
				t.Fatalf("snapshot enviado a fleet:\n  got  %+v\n  want %+v", rig.fleet.health, tc.want)
			}
			want := []fleetCall{{method: "SaveHealth", tenantID: "tenant-1", edgeID: "edge-1", sessionID: "s-1"}}
			if got := rig.fleet.recorded(); !reflect.DeepEqual(got, want) {
				t.Errorf("llamadas a fleet = %+v, se esperaba %+v", got, want)
			}
			if rig.log.String() != "" {
				t.Errorf("una salud guardada no deja rastro: %q", rig.log.String())
			}
		})
	}
}

// El enum del socket tiene texto para sus cuatro estados; UNSPECIFIED —y cualquier valor que
// el contrato gane mañana— cae a vacío, para que la API lo omita en vez de inventar un estado.
func TestWhatsappStateStringCoversTheEnum(t *testing.T) {
	t.Parallel()
	cases := map[cloudlinkv1.WhatsappSocketState]string{
		cloudlinkv1.WhatsappSocketState_WHATSAPP_SOCKET_STATE_UNSPECIFIED: "",
		cloudlinkv1.WhatsappSocketState_WHATSAPP_SOCKET_STATE_CONNECTED:   "connected",
		cloudlinkv1.WhatsappSocketState_WHATSAPP_SOCKET_STATE_CONNECTING:  "connecting",
		cloudlinkv1.WhatsappSocketState_WHATSAPP_SOCKET_STATE_DEGRADED:    "degraded",
		cloudlinkv1.WhatsappSocketState_WHATSAPP_SOCKET_STATE_DEAD:        "dead",
		cloudlinkv1.WhatsappSocketState(99):                               "",
	}
	for state, want := range cases {
		if got := whatsappStateString(state); got != want {
			t.Errorf("whatsappStateString(%v) = %q, se esperaba %q", state, got, want)
		}
	}
	// Todo valor que el contrato declare tiene que estar decidido arriba.
	for value := range cloudlinkv1.WhatsappSocketState_name {
		if _, decided := cases[cloudlinkv1.WhatsappSocketState(value)]; !decided {
			t.Errorf("el contrato tiene un estado de socket (%d) cuyo texto no está decidido aquí", value)
		}
	}
}

// R-G6: un Edge viejo, que no adjunta SessionHealth, no toca los campos de salud ya
// guardados: ni siquiera se llama a fleet.
func TestPersistHealthWithoutHealthBlockTouchesNothing(t *testing.T) {
	t.Parallel()
	rig := newHeartbeatRig()
	cc := phone("tenant-1", "edge-1", "s-1")
	rig.online(t, cc)
	rig.srv.persistHealth(context.Background(), cc, &cloudlinkv1.Heartbeat{SessionHealth: &cloudlinkv1.SessionHealth{
		WhatsappSocketState: cloudlinkv1.WhatsappSocketState_WHATSAPP_SOCKET_STATE_DEGRADED, BinaryVersion: "v0.9.0",
	}})

	rig.srv.persistHealth(context.Background(), cc, &cloudlinkv1.Heartbeat{LeaseCounter: 2})

	if n := len(rig.fleet.health); n != 1 {
		t.Fatalf("escrituras de salud = %d, se esperaba solo la primera", n)
	}
	if row := rig.row(t, cc); row.WhatsappState != "degraded" || row.BinaryVersion != "v0.9.0" || row.DegradedSince.IsZero() {
		t.Errorf("el latido sin salud pisó lo guardado: %+v", row)
	}
}

// Si fleet falla al guardar la salud se registra el error con IDs opacos y no se propaga.
func TestPersistHealthLogsAFleetFailure(t *testing.T) {
	t.Parallel()
	rig := newHeartbeatRig()
	rig.fleet.fail["SaveHealth"] = errors.New("base caída")

	rig.srv.persistHealth(context.Background(), phone("tenant-1", "edge-1", "s-1"), fullHeartbeat())

	rig.requireLog(t, "level=ERROR", `msg="fleet: persistir salud"`, "base caída", "edge_id=edge-1", "session_id=s-1")
}

// inferenceRig es un Server SIN fleet y con el almacén del parte de inferencia.
func inferenceRig() (*Server, *inferstats.Store) {
	store := inferstats.New()
	return New(session.NewRegistry(), quietLog(), WithInferenceStats(store)), store
}

func inferenceHeartbeat(sh *cloudlinkv1.SessionHealth) *cloudlinkv1.Heartbeat {
	return &cloudlinkv1.Heartbeat{SessionHealth: sh}
}

// R-G18: el bloque de inferencia del latido llega al almacén que /metrics lee. De cada fase
// se publica el `n` (no el cuantil), y la fase que NO viene se queda en «no medible» (nil):
// ausente no es cero, mientras que una fase presente con cero muestras sí es un cero.
func TestObserveInferenceTranslatesTheInferenceBlock(t *testing.T) {
	t.Parallel()
	srv, store := inferenceRig()

	srv.observeInference(phone("tenant-1", "edge-1", "s-1"), inferenceHeartbeat(&cloudlinkv1.SessionHealth{
		InferenceByRegime:     map[string]int64{"frio": 2, "caliente": 40},
		InferenceByClass:      map[string]int64{"interactivo": 30, "lote": 12},
		IntentOmittedByReason: map[string]int64{"sin_capacidad": 4},
		InferencePrefill:      &cloudlinkv1.InferenceLatency{P50Ms: 120, Samples: 42},
	}))

	got := store.Aggregated()
	want := map[string]map[string]int64{
		"regime":  {"frio": 2, "caliente": 40},
		"class":   {"interactivo": 30, "lote": 12},
		"skipped": {"sin_capacidad": 4},
	}
	if have := map[string]map[string]int64{"regime": got.PorRegimen, "class": got.PorClase, "skipped": got.OmitidasPorMotivo}; !reflect.DeepEqual(have, want) {
		t.Errorf("contadores = %v, se esperaba %v", have, want)
	}
	if got.MuestrasPrefill == nil || *got.MuestrasPrefill != 42 {
		t.Errorf("muestras de prefill = %v, se esperaba 42 (el `n`, no el p50)", got.MuestrasPrefill)
	}
	if got.MuestrasGeneracion != nil {
		t.Errorf("la generación no venía en el latido y salió %d: ausente no es cero", *got.MuestrasGeneracion)
	}

	if n := samplesOf(&cloudlinkv1.InferenceLatency{P50Ms: 9}); n == nil || *n != 0 {
		t.Errorf("una fase presente con cero muestras es un cero medido, no nil: %v", n)
	}
	if n := samplesOf(nil); n != nil {
		t.Errorf("una fase ausente es nil, no %d", *n)
	}
}

// 🔴 T-12: los contadores los lleva el PROCESO del Edge pero viajan en el latido de CADA
// sesión suya. Tres teléfonos de un Edge mandan tres veces los mismos totales: la clave es
// (tenant, Edge), no la sesión, y no se triplican. El mismo edge_id en otro tenant sí es otro.
func TestObserveInferenceIsKeyedByEdgeNotBySession(t *testing.T) {
	t.Parallel()
	srv, store := inferenceRig()
	hb := inferenceHeartbeat(&cloudlinkv1.SessionHealth{InferenceByRegime: map[string]int64{"caliente": 50}})

	for _, sessionID := range []string{"s-1", "s-2", "s-3"} {
		srv.observeInference(phone("tenant-1", "edge-1", sessionID), hb)
	}
	if got := store.Aggregated(); got.PorRegimen["caliente"] != 50 || got.Edges != 1 {
		t.Fatalf("tres teléfonos de UN Edge: inferencias = %d, Edges = %d; se esperaban 50 y 1", got.PorRegimen["caliente"], got.Edges)
	}

	srv.observeInference(phone("tenant-2", "edge-1", "s-1"), hb)
	if got := store.Aggregated(); got.PorRegimen["caliente"] != 100 || got.Edges != 2 {
		t.Errorf("el mismo edge_id en otro tenant es otro Edge: inferencias = %d, Edges = %d", got.PorRegimen["caliente"], got.Edges)
	}
}

// La recogida NO comparte guarda con persistHealth: sin fleet se sigue sirviendo /metrics (el
// banco no lo tiene). Pero un Edge viejo sin salud, o un stream sin identidad mTLS, no
// registran nada; y sin almacén es un no-op.
func TestObserveInferenceGuards(t *testing.T) {
	t.Parallel()
	srv, store := inferenceRig()
	if srv.fleet != nil {
		t.Fatal("este test necesita un Server sin fleet")
	}
	anonymous := phone("tenant-1", "edge-1", "s-2")
	anonymous.hasIdentity = false

	srv.observeInference(phone("tenant-1", "edge-1", "s-1"), &cloudlinkv1.Heartbeat{})
	srv.observeInference(anonymous, fullHeartbeat())
	if edges := store.Aggregated().Edges; edges != 0 {
		t.Fatalf("se registraron %d Edges desde latidos que no traían nada que registrar", edges)
	}

	srv.observeInference(phone("tenant-1", "edge-1", "s-1"), fullHeartbeat())
	if got := store.Aggregated().PorClase["lote"]; got != 1 {
		t.Errorf("sin fleet no se recogió nada (%d): /metrics se quedaría a cero sin un solo error", got)
	}

	New(session.NewRegistry(), quietLog()).observeInference(phone("tenant-1", "edge-1", "s-1"), fullHeartbeat())
}
