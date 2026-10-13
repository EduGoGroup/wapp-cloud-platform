//go:build integracion

package procesos

import (
	"testing"
)

// P4 · La recuperación del reinicio (hallazgo 51 de F8, punto (b)). El estado de una ventana de
// captación vive en intake_jobs, no en la memoria del proceso: si el servidor muere con una ventana
// abierta y esa ventana vence mientras no hay proceso, el que arranca después la cierra AL ARRANCAR,
// antes de su primer barrido periódico, y lo dice en una línea de log propia. Sin eso la ventana no
// se perdería —el barrido de los 5 s la acabaría cerrando—, y por eso lo que este proceso afirma es
// QUIÉN la cierra: la línea del arranque, no solo que la fila cambie de estado.

const (
	// p4BootPn es el contacto cuya ráfaga se queda en una ventana abierta cuando el servidor muere.
	p4BootPn = "573004460001"

	// p4MsgBootRecovery es la línea INFO del arranque que cierra las ventanas vencidas sin proceso;
	// lleva en `jobs` cuántas cerró y no sale si no cerró ninguna. p4MsgWindowClosed es la DEBUG de
	// cada ventana cerrada, la cierre el arranque o un barrido, con su `job_id`.
	p4MsgBootRecovery = "agregador: ventanas vencidas cerradas al arrancar"
	p4MsgWindowClosed = "agregador: ventana cerrada"

	// p4BootJobID lee el id del job que lleva un wa_message_id en sus referencias, y p4BootJobStatus
	// el estado de un job por su id.
	p4BootJobID     = `SELECT id::text FROM public.intake_jobs WHERE tenant_id = $1 AND source_refs ? $2`
	p4BootJobStatus = `SELECT status FROM public.intake_jobs WHERE id = $1::uuid`
)

// p4LogLinesIn devuelve, de un log JSON entero, las líneas con el mensaje msg.
func p4LogLinesIn(log, msg string) []map[string]any {
	var out []map[string]any
	for _, l := range lineasJSON(log) {
		if l["msg"] == msg {
			out = append(out, l)
		}
	}
	return out
}

// TestP4_BootRecoveryClosesExpiredWindow deja una ventana `aggregating` abierta, para el servidor,
// vence la ventana con el servidor YA parado y lo vuelve a arrancar sobre la misma base:
//
//   - mientras el primer proceso vive, los plazos de la ventana son largos y su barrido no la cierra:
//     la fila sigue `aggregating` cuando el proceso muere, y su log no trae ningún cierre;
//   - la ventana vence por SQL sin proceso (los plazos de la empresa a cero), y la fila no se mueve:
//     no hay nadie que la cierre;
//   - el proceso nuevo la cierra al arrancar: su log trae la línea del arranque con `jobs` = 1 y,
//     ANTES que ella, el cierre de ESE job; la fila deja de estar `aggregating`;
//   - y el job no se queda ahí: con el Edge reconectado, el pipeline lo lleva hasta el borrador.
//
// El orden parar → vencer → arrancar es lo que quita la carrera: si la ventana venciera con el primer
// proceso vivo, la cerraría su barrido de los 5 s y el arranque no tendría nada que recuperar.
// Necesita Docker.
func TestP4_BootRecoveryClosesExpiredWindow(t *testing.T) {
	t.Parallel()
	sc := draftScenario(t, "p4_boot_recovery", "p4-arranque")
	ids := sendDraftBurst(t, sc, p4BootPn, draftBurst())
	jobID := p9Scalar(t, sc.DB, p4BootJobID, sc.Tenant, ids[0])
	if jobID == "" {
		t.Fatalf("la ráfaga no dejó ningún job con la referencia %s", ids[0])
	}

	previous := p4StopExpireRestart(t, sc, jobID)
	p4DeadProcessClosedNothing(t, previous)
	p4BootClosedTheWindow(t, sc, jobID)

	// El stream del Edge murió con el proceso: reconecta, y su primer latido lo declara READY.
	//
	// ✎ Medido: el worker del proceso nuevo reclama el job en cuanto el arranque lo cierra, ANTES de que
	// ningún Edge haya podido reconectar, así que el primer intento de P2 cae por `edge_offline` (un
	// aviso de degradación al dueño y el job en backoff) y es el flanco a READY de la reconexión el
	// que lo reanuda: el borrador sale en el intento 2. Pasa igual en los dos binarios. No se afirma
	// aquí —es una carrera entre el worker y la reconexión, no una regla—; lo que se exige es el final.
	sc.Edge.conectar(t)
	sc.Edge.esperarLeases(t, 2, edgeTopeFila)
	if !sc.Edge.puedeOperar() {
		t.Fatalf("tras reiniciar, el Edge reconectado no puede operar")
	}
	if gotJob, intakeID := waitDraft(t, sc, p4BootPn); gotJob != jobID || intakeID == "" {
		t.Errorf("el borrador salió del job %q (solicitud %q), quería el de la ventana recuperada, %q", gotJob, intakeID, jobID)
	}

	if n := len(p9LogLines(sc.S, p4MsgBootRecovery, nil)); n != 1 {
		t.Errorf("el log del proceso nuevo trae %d líneas %q, quería 1: solo recupera el arranque", n, p4MsgBootRecovery)
	}
	if problems := sc.Script.Problems(); len(problems) != 0 {
		t.Errorf("el guion no supo atender: %v", problems)
	}
	if errs := sc.Edge.Errores(); len(errs) != 0 {
		t.Errorf("errores del núcleo del Edge: %v", errs)
	}
	requireNoJobWithoutLiteral(t, sc)
	edgeSinErrores(t, sc.S, nil)
}

// p4StopExpireRestart para el servidor con la ventana abierta, la vence sin proceso y lo reinicia.
// Devuelve el log entero del proceso parado. Afirma el estado de la fila en los dos momentos en que
// nadie puede moverla: `aggregating` con el proceso ya muerto, y otra vez tras vencerla.
//
// serverRestart no deja hacer nada entre parar y arrancar, y no hace falta que lo deje: Parar es
// idempotente, así que se para aquí y serverRestart, al parar otra vez, recoge el mismo código de
// salida y relanza.
func p4StopExpireRestart(t *testing.T, sc *draftScene, jobID string) (previousLog string) {
	t.Helper()
	if got := p9Scalar(t, sc.DB, p4BootJobStatus, jobID); got != "aggregating" {
		t.Fatalf("antes de parar el servidor el job está %q, quería aggregating", got)
	}
	if code := sc.S.Parar(t); code != 0 {
		t.Fatalf("el servidor no paró limpio con la ventana abierta: código %d", code)
	}
	if got := p9Scalar(t, sc.DB, p4BootJobStatus, jobID); got != "aggregating" {
		t.Fatalf("con el servidor ya parado el job está %q, quería aggregating: la cerró el proceso que murió", got)
	}
	flushDraftWindow(t, sc)
	if got := p9Scalar(t, sc.DB, p4BootJobStatus, jobID); got != "aggregating" {
		t.Fatalf("vencida la ventana sin proceso, el job está %q, quería aggregating: nadie podía cerrarla", got)
	}
	return serverRestart(t, sc.S)
}

// p4DeadProcessClosedNothing mira el log del proceso que murió: no cerró ninguna ventana (ni en un
// barrido ni en su propio arranque, que no tenía nada que recuperar) y no dejó líneas ERROR ni pánicos.
func p4DeadProcessClosedNothing(t *testing.T, previous string) {
	t.Helper()
	for _, msg := range []string{p4MsgWindowClosed, p4MsgBootRecovery} {
		if lines := p4LogLinesIn(previous, msg); len(lines) != 0 {
			t.Errorf("el proceso que murió dejó %d líneas %q; quería 0, el cierre es del arranque siguiente: %v", len(lines), msg, lines)
		}
	}
	p1WireNoPanics(t, previous)
	for _, l := range lineasJSON(previous) {
		if l["level"] == "ERROR" {
			t.Errorf("línea ERROR inesperada en el log del proceso parado: %v", l)
		}
	}
}

// p4BootClosedTheWindow espera la línea del arranque en el log del proceso NUEVO y afirma que es de
// esta ventana: `jobs` vale 1, el cierre de jobID está en el log y va ANTES que ella (la línea del
// arranque se escribe al terminar su pasada), y la fila ya no está `aggregating`.
func p4BootClosedTheWindow(t *testing.T, sc *draftScene, jobID string) {
	t.Helper()
	edgeEsperar(t, edgeTopeFila, "la línea de log «"+p4MsgBootRecovery+"» del proceso nuevo", func() bool {
		return len(p9LogLines(sc.S, p4MsgBootRecovery, nil)) > 0
	})
	closedAt, bootAt := -1, -1
	for i, l := range sc.S.LineasLog() {
		switch l["msg"] {
		case p4MsgWindowClosed:
			if l["job_id"] == jobID && closedAt < 0 {
				closedAt = i
			}
		case p4MsgBootRecovery:
			if bootAt < 0 {
				bootAt = i
				if n, ok := l["jobs"].(float64); !ok || n != 1 || l["level"] != "INFO" {
					t.Errorf("la línea del arranque es %v; quería nivel INFO y jobs = 1", l)
				}
			}
		}
	}
	if closedAt < 0 || closedAt > bootAt {
		t.Errorf("el cierre del job %s está en la línea %d del log nuevo y la del arranque en la %d; quería el cierre, y antes", jobID, closedAt, bootAt)
	}
	if got := p9Scalar(t, sc.DB, p4BootJobStatus, jobID); got == "aggregating" || got == "" {
		t.Errorf("tras la línea del arranque el job está %q, quería que ya no estuviera aggregating", got)
	}
}
