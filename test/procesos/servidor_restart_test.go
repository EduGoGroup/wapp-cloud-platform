//go:build integracion

package procesos

import (
	"context"
	"net/http"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

// El reinicio del servidor de prueba: parar el subproceso y volver a lanzar EL MISMO binario con
// EL MISMO entorno —la misma base clonada, la misma PKI, las mismas claves (lease, JWT, KEK), el
// mismo S3 falso, la misma identidad y las mismas cuatro direcciones—. Es lo que hace un despliegue
// o una caída: el proceso muere y otro nace sobre el mismo Postgres. Sirve para afirmar lo que tiene
// que sobrevivir a la memoria del proceso (la revocación de un lease, el corte de una empresa).

const (
	// serverRestartBindAttempts es cuántas veces se intenta relanzar sobre las mismas direcciones si
	// el proceso nuevo muere con «address already in use». Los puertos de un servidor no se vuelven a
	// entregar dentro de la corrida (puertosEntregados), así que quien los ocupe es otro proceso de la
	// máquina, y de paso: se reintenta con espera en vez de cambiar de puertos, porque los Edges ya
	// enrolados guardan la dirección de CloudLink.
	serverRestartBindAttempts = 5
	// serverRestartBindWait es lo que se espera entre dos de esos intentos.
	serverRestartBindWait = 500 * time.Millisecond
)

// serverRestart para el servidor (SIGTERM, su plazo, SIGKILL) y lo vuelve a lanzar con el mismo
// binario, directorio y entorno que el proceso que acaba de morir, y espera a que esté listo
// (/healthz en 200 y los otros tres listeners aceptando). Devuelve el log ENTERO del proceso parado:
// tras el reinicio, s.Log y s.LineasLog son solo del proceso nuevo, y quien quiera afirmar algo de
// lo anterior (que no hubo ERROR, que paró limpio) lo mira en lo devuelto.
//
// Lo que NO se conserva es lo que vivía en la memoria del proceso: los streams de los Edges (cada
// Edge tiene que volver a conectar), las solicitudes en vuelo y el limitador de peticiones. El
// Cleanup que registró arrancar para este servidor para al proceso NUEVO.
//
// Falla (t.Fatalf) si el servidor no se había lanzado, si no paró limpio (código ≠ 0: un reinicio
// tras una muerte sucia no dice nada de un despliegue), o si el proceso nuevo no queda listo.
func serverRestart(t *testing.T, s *servidor) (previousLog string) {
	t.Helper()
	old := s.proceso
	if old == nil {
		t.Fatal("serverRestart: el servidor no llegó a lanzarse")
	}
	if code := s.Parar(t); code != 0 {
		s.volcado = true
		t.Fatalf("serverRestart: el servidor no paró limpio antes de reiniciar: código %d\núltimas %d líneas del log:\n%s",
			code, lineasDeLog, ultimasLineas(s.Log(), lineasDeLog))
	}
	previousLog = old.registro.String()

	start := time.Now()
	for attempt := 1; ; attempt++ {
		err := s.serverRelaunch(t.Context(), old.cmd)
		if err == nil {
			t.Logf("servidor reiniciado (%s) y listo en %s sobre la base %s: admin=%s publica=%s enrolar=%s conectar=%s", binarioElegido(),
				time.Since(start).Round(time.Millisecond), s.Base.Nombre, s.AdminAddr, s.PublicaAddr, s.EnrolarAddr, s.ConectarAddr)
			return previousLog
		}
		if attempt < serverRestartBindAttempts && s.murioPorPuertoOcupado(err) {
			t.Logf("serverRestart: las direcciones del servidor siguen ocupadas (intento %d de %d); se reintenta en %s",
				attempt, serverRestartBindAttempts, serverRestartBindWait)
			select {
			case <-t.Context().Done():
				t.Fatalf("serverRestart: el test terminó esperando a que se liberen las direcciones: %v", t.Context().Err())
			case <-time.After(serverRestartBindWait):
			}
			continue
		}
		code := s.Parar(t)
		s.volcado = true
		t.Fatalf("serverRestart: %v (código de salida %d)\núltimas %d líneas del log del proceso nuevo:\n%s",
			err, code, lineasDeLog, ultimasLineas(s.Log(), lineasDeLog))
	}
}

// serverRelaunch hace UN intento de relanzar: un comando nuevo con la ruta, el directorio y el
// entorno del anterior (un exec.Cmd no se puede reutilizar), que pasa a ser el proceso vigente del
// servidor, y la espera a que esté listo. Devuelve el error de lanzar, el de salir antes de estar
// listo (errSalioAntes) o el de no estarlo a tiempo.
func (s *servidor) serverRelaunch(ctx context.Context, previous *exec.Cmd) error {
	cmd := exec.Command(previous.Path) //nolint:gosec // el mismo binario que compiló este arnés
	cmd.Dir = previous.Dir
	cmd.Env = slices.Clone(previous.Env)
	proceso, err := iniciarProceso(cmd, topeParadaServidor)
	if err != nil {
		return err
	}
	s.proceso = proceso
	return s.esperarListo(ctx)
}

// TestArnes_ServerRestart prueba serverRestart contra el servidor real: tras reiniciar, el servidor
// escucha en las MISMAS cuatro direcciones, sobre la MISMA base (el código emitido antes sigue ahí)
// y con las MISMAS claves (el Context Token emitido por el proceso anterior sigue valiendo, y un
// Edge que se enrola después acepta el lease que firma el proceso nuevo); el log devuelto es el del
// proceso parado, con su parada limpia, y el log vigente es solo del proceso nuevo; y se puede
// reiniciar más de una vez. Necesita Docker.
func TestArnes_ServerRestart(t *testing.T) {
	t.Parallel()
	esc := edgeEscenarioNuevo(t, "arnes_restart", "arnes-restart", false)
	s := esc.S
	before := [4]string{s.AdminAddr, s.PublicaAddr, s.EnrolarAddr, s.ConectarAddr}
	firstProcess := s.proceso
	codeBefore := edgeEmitirCodigo(t, s, esc.TokenStaff, esc.Tenant)

	previous := serverRestart(t, s)

	if after := [4]string{s.AdminAddr, s.PublicaAddr, s.EnrolarAddr, s.ConectarAddr}; after != before {
		t.Errorf("tras reiniciar las direcciones son %v, quería las mismas %v", after, before)
	}
	serverRestartCheckProcess(t, s, firstProcess, previous)
	serverRestartCheckSameState(t, esc, codeBefore)

	// Un segundo reinicio también sale: reiniciar no gasta nada que no se reponga.
	serverRestart(t, s)
	if codigo, _, err := consultarHealthz(t.Context(), s.AdminAddr); err != nil || codigo != http.StatusOK {
		t.Errorf("/healthz tras el segundo reinicio: código %d, error %v", codigo, err)
	}
	if code := s.Parar(t); code != 0 {
		t.Errorf("Parar del proceso reiniciado devolvió %d, quería 0", code)
	}
}

// serverRestartCheckProcess comprueba el relevo de procesos: el primero murió y el vigente es otro,
// vivo; el log devuelto es el del primero, con su parada limpia, y el vigente no la trae; y el S3
// falso, que es el MISMO doble, vio el HeadBucket de los dos arranques.
func serverRestartCheckProcess(t *testing.T, s *servidor, first *procesoHijo, previous string) {
	t.Helper()
	if s.proceso == first || !first.haSalido() || s.proceso.haSalido() {
		t.Fatalf("tras reiniciar: mismo proceso=%v, el primero salió=%v, el nuevo salió=%v; quería otro proceso, vivo, y el primero muerto",
			s.proceso == first, first.haSalido(), s.proceso.haSalido())
	}
	if !strings.Contains(previous, textoParadaLimpia) || strings.Contains(s.Log(), textoParadaLimpia) {
		t.Errorf("la parada limpia (%q) tiene que estar en el log devuelto y no en el vigente: devuelto=%v vigente=%v",
			textoParadaLimpia, strings.Contains(previous, textoParadaLimpia), strings.Contains(s.Log(), textoParadaLimpia))
	}
	if previous != first.registro.String() || len(s.LineasLog()) == 0 {
		t.Errorf("el log devuelto no es el del proceso parado, o el proceso nuevo no ha escrito nada (%d líneas JSON)", len(s.LineasLog()))
	}
	if n := len(s.S3.Peticiones()); n < 2 {
		t.Errorf("el S3 falso vio %d peticiones, quería al menos 2: el HeadBucket de cada arranque", n)
	}
}

// serverRestartCheckSameState comprueba que el proceso nuevo corre sobre lo mismo: la misma base (el
// código emitido antes sigue ahí, sin usar), las mismas claves JWT (el token que firmó el proceso
// anterior entra) y la misma PKI y clave de lease (un Edge se enrola con el código de antes, conecta
// con mTLS y su Validator acepta el lease que firma el proceso nuevo).
func serverRestartCheckSameState(t *testing.T, esc edgeEscenario, codeBefore string) {
	t.Helper()
	s := esc.S
	if n := consultaEntero(t, esc.DB, `SELECT count(*) FROM public.enrollment_codes WHERE tenant_id = $1::uuid AND code = $2 AND used_at IS NULL`, esc.Tenant, codeBefore); n != 1 {
		t.Errorf("tras reiniciar, el código emitido antes aparece %d veces sin usar, quería 1", n)
	}
	if r := s.Admin(esc.TokenStaff).Get(t, rutaTenants, nil); r.Codigo != http.StatusOK || !strings.Contains(string(r.Cuerpo), esc.Tenant) {
		t.Errorf("GET %s con el token de antes del reinicio: HTTP %d, quería 200 con la empresa\ncuerpo: %s", rutaTenants, r.Codigo, recortar(r.Cuerpo))
	}
	e := enrolar(t, s, codeBefore)
	e.conectar(t)
	if !e.puedeOperar() || len(e.Errores()) != 0 {
		t.Errorf("el Edge enrolado tras reiniciar: puedeOperar=%v errores=%v; quería verdadero y ninguno", e.puedeOperar(), e.Errores())
	}
	e.desconectar(t)
}
