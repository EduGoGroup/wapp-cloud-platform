package grpc

// Las dos reglas que deciden si el aviso se da por entregado (MD-046.3): SIN ACK NO HAY MARCA
// y UN RECHAZO DEL EDGE NO MARCA. En los dos casos el reintento es el siguiente latido, sin
// temporizadores ni backoff propio. Trozo de greeting_test.go, partido por tema (E-13).

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"
)

// logCommandID es un command_id con forma de UUIDv4 dentro de una línea de log.
var logCommandID = regexp.MustCompile(`command_id=[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\b`)

// El primer latido tras emparejar cae en la ventana del lease: el Edge ACUSA el comando y dice
// que no lo envió (Ack ok=false, sin error de Go). «Acusado» no es «entregado»: no se marca,
// el latido siguiente reintenta, y una vez entregado deja de reintentar.
func TestGreetingRejectedByTheEdgeIsNotMarkedAndRetriesOnTheNextHeartbeat(t *testing.T) {
	t.Parallel()
	rig := newGreetingRig(t, ownNumber, []edgeReply{rejects}) // el segundo envío ya pasa

	rig.beat()

	sent := rig.edge.sent()
	if len(sent) != 1 {
		t.Fatalf("primer latido: %d envíos, se esperaba 1", len(sent))
	}
	if rig.fleet.isGreeted() || rig.fleet.count("MarkGreeted") != 0 {
		t.Fatalf("tras un Ack ok=false: marcada=%v con %d llamadas a MarkGreeted; se esperaba sin marca y 0",
			rig.fleet.isGreeted(), rig.fleet.count("MarkGreeted"))
	}
	requireLog(t, rig.log, "WARN",
		"saludo: el Edge rechazó el aviso de sesión pasiva; NO se marca y se reintentará en el siguiente latido",
		"session_id=s-1", "edge_id=edge-1", "literal=AVISO_SESION_PASIVA_V1",
		"command_id="+sent[0].GetCommandId(), `edge_error="lease no vigente"`)

	rig.beat()

	if n := len(rig.edge.sent()); n != 2 {
		t.Fatalf("segundo latido: %d envíos acumulados, se esperaban 2 (el reintento)", n)
	}
	if !rig.fleet.isGreeted() || rig.fleet.count("MarkGreeted") != 1 {
		t.Fatalf("tras el Ack bueno: marcada=%v con %d llamadas a MarkGreeted; se esperaba la marca y 1",
			rig.fleet.isGreeted(), rig.fleet.count("MarkGreeted"))
	}

	rig.beat()

	if n := len(rig.edge.sent()); n != 2 {
		t.Errorf("tercer latido: %d envíos acumulados, se esperaban 2 (ya estaba saludada)", n)
	}
}

// Sin Ack no hay marca, salga SendText por donde salga: la sesión sin stream (el comando no
// viajó), el Edge que no acusa dentro del plazo, el stream que cae con el envío en vuelo y el
// presupuesto del job ya gastado. Cada uno deja UN Warn —el del envío fallido, con el
// command_id del intento, y no el del rechazo— y el siguiente latido vuelve a preguntar.
func TestGreetingWithoutAckIsNotMarked(t *testing.T) {
	t.Parallel()
	spent, cancel := context.WithCancel(context.Background())
	cancel()
	cases := []struct {
		name      string
		sessionID string
		script    []edgeReply
		ctx       context.Context
		opts      []Option
		wantSent  int // -1: no se afirma (el empuje compite con el ctx ya terminado)
	}{
		{name: "session without stream", sessionID: "s-orphan", ctx: context.Background(), wantSent: 0},
		{name: "edge never acks", sessionID: "s-1", script: []edgeReply{staysSilent}, ctx: context.Background(),
			opts: []Option{WithAckTimeout(time.Nanosecond)}, wantSent: 1},
		{name: "stream drops in flight", sessionID: "s-1", script: []edgeReply{dropsStream}, ctx: context.Background(), wantSent: 1},
		{name: "job budget already spent", sessionID: "s-1", script: []edgeReply{staysSilent, staysSilent}, ctx: spent, wantSent: -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rig := newGreetingRig(t, ownNumber, tc.script, tc.opts...)
			cc := phone("tenant-1", "edge-1", tc.sessionID)

			rig.srv.greetIfNeeded(tc.ctx, cc)

			if n := len(rig.edge.sent()); tc.wantSent >= 0 && n != tc.wantSent {
				t.Errorf("el Edge recibió %d SendText, se esperaban %d", n, tc.wantSent)
			}
			if rig.fleet.isGreeted() || rig.fleet.count("MarkGreeted") != 0 {
				t.Fatalf("sin Ack: marcada=%v con %d llamadas a MarkGreeted; se esperaba sin marca y 0",
					rig.fleet.isGreeted(), rig.fleet.count("MarkGreeted"))
			}
			line := requireLog(t, rig.log, "WARN",
				"saludo: el envío del aviso de sesión pasiva falló; se reintentará en el siguiente latido",
				"session_id="+tc.sessionID, "edge_id=edge-1", "literal=AVISO_SESION_PASIVA_V1", "error=")
			if !logCommandID.MatchString(line) {
				t.Errorf("la línea no lleva el command_id (UUIDv4) del intento: %q", line)
			}
			if rig.log.contains("el Edge rechazó") {
				t.Errorf("un envío sin Ack se registró como rechazo del Edge: %q", rig.log.String())
			}

			rig.srv.greetIfNeeded(tc.ctx, cc)

			if got := rig.fleet.count("PendingGreeting"); got != 2 {
				t.Errorf("PendingGreeting se llamó %d veces, se esperaban 2: el siguiente latido reintenta", got)
			}
		})
	}
}

// La espera del Ack corre con el PRESUPUESTO DEL JOB, no con un reloj propio: quien corta es el
// carril. Con el presupuesto ya gastado y un Edge que no acusa, el envío vuelve en el acto con
// el error del contexto del job —no con el del plazo del acuse, que aquí vence más tarde—.
func TestGreetingWaitsForTheAckOnTheBudgetOfTheJob(t *testing.T) {
	t.Parallel()
	spent, cancel := context.WithCancel(context.Background())
	cancel()
	rig := newGreetingRig(t, ownNumber, []edgeReply{staysSilent}, WithAckTimeout(100*time.Millisecond))

	rig.srv.greetIfNeeded(spent, rig.cc)

	if rig.fleet.isGreeted() || rig.fleet.count("MarkGreeted") != 0 {
		t.Fatalf("sin Ack: marcada=%v con %d llamadas a MarkGreeted; se esperaba sin marca y 0",
			rig.fleet.isGreeted(), rig.fleet.count("MarkGreeted"))
	}
	line := requireLog(t, rig.log, "WARN",
		"saludo: el envío del aviso de sesión pasiva falló; se reintentará en el siguiente latido",
		"session_id=s-1", "context canceled")
	if strings.Contains(line, "deadline exceeded") {
		t.Errorf("el envío esperó el plazo del acuse en vez de rendirse con el presupuesto del job: %q", line)
	}
}
