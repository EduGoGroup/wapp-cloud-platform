//go:build integracion

package procesos

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"
)

// El subproceso del servidor de prueba: lanzarlo, saber si salió y con qué código, y pararlo
// (SIGTERM, su plazo, SIGKILL).
// Sale de servidor_test.go (D-F9-11: solo se movieron declaraciones).

// procesoHijo es un subproceso en marcha: el comando, su log, un canal que se cierra cuando
// cmd.Wait() devolvió y el estado de la parada. Separarlo del servidor permite probar la parada
// con un proceso cualquiera, sin Docker ni binario del servidor.
type procesoHijo struct {
	cmd        *exec.Cmd
	registro   *registroLog
	salio      chan struct{} // se cierra cuando cmd.Wait() devolvió
	errEspera  error         // lo que devolvió cmd.Wait(); solo se lee después de <-salio
	topeParada time.Duration // cuánto se espera tras SIGTERM antes de SIGKILL

	mu     sync.Mutex // serializa parar: la segunda llamada espera a la primera y no repite nada
	parado bool
	codigo int
}

// iniciarProceso recibe un comando aún sin lanzar y el plazo entre SIGTERM y SIGKILL, le conecta
// stdout y stderr al MISMO búfer y lo lanza, con una goroutine que hace cmd.Wait() UNA sola vez y
// cierra el canal de salida. Devuelve el proceso en marcha, o el error de cmd.Start. cmd.Env lo
// pone quien llama (para el servidor, desde cero).
func iniciarProceso(cmd *exec.Cmd, topeParada time.Duration) (*procesoHijo, error) {
	h := &procesoHijo{cmd: cmd, registro: &registroLog{}, salio: make(chan struct{}), topeParada: topeParada}
	cmd.Stdout, cmd.Stderr = h.registro, h.registro
	cmd.WaitDelay = esperaTrasMuerte
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("lanzar %s: %w", cmd.Path, err)
	}
	go func() {
		h.errEspera = cmd.Wait()
		close(h.salio)
	}()
	return h, nil
}

// haSalido dice, sin bloquear, si cmd.Wait() ya devolvió (el canal salio está cerrado).
func (h *procesoHijo) haSalido() bool {
	select {
	case <-h.salio:
		return true
	default:
		return false
	}
}

// codigoSalida devuelve el código de salida del proceso: el que devolvió, o -1 si lo terminó una
// señal. Solo es válido una vez cerrado el canal salio; antes devuelve -1.
func (h *procesoHijo) codigoSalida() int {
	if h.cmd.ProcessState == nil {
		return -1
	}
	return h.cmd.ProcessState.ExitCode()
}

// parar envía SIGTERM, espera hasta topeParada y, pasado ese plazo, SIGKILL. Devuelve el código de
// salida. Es idempotente y segura entre goroutines: la primera llamada hace la parada, las demás
// devuelven el mismo código sin tocar el proceso. Un fallo al señalar se anota con t.Logf.
func (h *procesoHijo) parar(t *testing.T) int {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.parado {
		h.codigo = h.detener(t)
		h.parado = true
	}
	return h.codigo
}

// detener hace la parada de verdad (parar la protege con el candado). Si el proceso ya había
// salido por su cuenta, solo devuelve su código.
func (h *procesoHijo) detener(t *testing.T) int {
	t.Helper()
	if h.haSalido() {
		return h.codigoSalida()
	}
	if err := h.cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Logf("no se pudo enviar SIGTERM: %v", err)
	}
	plazo := time.NewTimer(h.topeParada)
	defer plazo.Stop()
	select {
	case <-h.salio:
	case <-plazo.C:
		t.Logf("el proceso no salió en %s tras SIGTERM: SIGKILL", h.topeParada)
		if err := h.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Logf("no se pudo enviar SIGKILL: %v", err)
		}
		<-h.salio
	}
	return h.codigoSalida()
}
