//go:build integracion

package procesos

import (
	"context"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// Los tests propios del log y del subproceso del servidor de prueba: las líneas del log, el
// registro concurrente y las dos ramas de la parada (limpia y con SIGKILL).
// Sale de servidor_test.go (D-F9-11: solo se movieron declaraciones).

// TestArnes_LineasLog prueba el análisis del log sin Docker ni servidor: solo las líneas que son
// un objeto JSON se devuelven, en orden y parseadas; el resto (texto, vacías, truncadas, arreglos,
// null) se ignora; una línea escrita en dos trozos cuenta como una; y el servidor sin lanzar da
// vacío.
func TestArnes_LineasLog(t *testing.T) {
	t.Parallel()
	texto := strings.Join([]string{
		`{"time":"2026-10-01T10:00:00Z","level":"INFO","msg":"arranque: fase completada","fase":"1/9","nombre":"infraestructura","ms":42}`,
		`2026/10/01 10:00:01 aviso en texto plano, como los de stderr`,
		``,
		`{"level":"WARN","msg":"con {llaves} dentro","anidado":{"a":[1,2]}}`,
		`[1,2,3]`,
		`{"truncada":`,
		`null`,
		`   {"level":"DEBUG","msg":"con sangría"}   `,
		`{"level":"ERROR","msg":"sin salto final"}`,
	}, "\n")

	registro := &registroLog{}
	mitad := len(texto) / 2
	for _, trozo := range []string{texto[:mitad], texto[mitad:]} { // el corte puede caer a mitad de línea
		if _, err := registro.Write([]byte(trozo)); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	s := &servidor{proceso: &procesoHijo{registro: registro}}

	if s.Log() != texto {
		t.Errorf("Log() no devuelve lo escrito")
	}
	lineas := s.LineasLog()
	msgs := make([]string, len(lineas))
	for i, l := range lineas {
		msgs[i] = fmt.Sprint(l["msg"])
	}
	quiere := []string{"arranque: fase completada", "con {llaves} dentro", "con sangría", "sin salto final"}
	if !slices.Equal(msgs, quiere) {
		t.Fatalf("mensajes = %q, quería %q", msgs, quiere)
	}
	if ms, ok := lineas[0]["ms"].(float64); !ok || ms != 42 {
		t.Errorf("ms = %#v, quería 42 (float64)", lineas[0]["ms"])
	}
	if anidado, ok := lineas[1]["anidado"].(map[string]any); !ok || anidado == nil {
		t.Errorf("el objeto anidado no se parseó: %v", lineas[1])
	}

	if vacias := (&servidor{}).LineasLog(); vacias == nil || len(vacias) != 0 {
		t.Errorf("un servidor sin lanzar debe dar un slice vacío no nil, dio %#v", vacias)
	}
	if (&servidor{}).Log() != "" {
		t.Errorf("un servidor sin lanzar debe dar un log vacío")
	}
}

// TestArnes_UltimasLineas prueba el recorte del volcado: las últimas n líneas, el texto entero si
// es más corto, y los bordes (n = 0, texto vacío, salto final).
func TestArnes_UltimasLineas(t *testing.T) {
	t.Parallel()
	casos := []struct {
		nombre string
		texto  string
		n      int
		quiere string
	}{
		{"las_ultimas_dos", "a\nb\nc\nd\n", 2, "c\nd"},
		{"sin_salto_final", "a\nb\nc", 2, "b\nc"},
		{"mas_corto_que_n", "a\nb\n", 200, "a\nb"},
		{"n_cero", "a\nb\n", 0, ""},
		{"texto_vacio", "", 5, ""},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			t.Parallel()
			if got := ultimasLineas(c.texto, c.n); got != c.quiere {
				t.Errorf("ultimasLineas(%q, %d) = %q, quería %q", c.texto, c.n, got, c.quiere)
			}
		})
	}
}

// TestArnes_RegistroConcurrente prueba que el búfer del log resiste escritores concurrentes sin
// mezclar líneas: 8 goroutines escriben 200 líneas JSON cada una y todas salen enteras. Bajo
// -race caza cualquier acceso sin candado.
func TestArnes_RegistroConcurrente(t *testing.T) {
	t.Parallel()
	const escritores, porEscritor = 8, 200
	registro := &registroLog{}
	var espera sync.WaitGroup
	for g := range escritores {
		espera.Go(func() {
			for n := range porEscritor {
				linea := fmt.Sprintf("{\"g\":%d,\"n\":%d}\n", g, n)
				if _, err := registro.Write([]byte(linea)); err != nil {
					t.Errorf("Write: %v", err)
					return
				}
				_ = registro.String() // lectura concurrente con las escrituras (String no devuelve error)
			}
		})
	}
	espera.Wait()
	if got := len(lineasJSON(registro.String())); got != escritores*porEscritor {
		t.Errorf("líneas JSON = %d, quería %d", got, escritores*porEscritor)
	}
}

// esperarEnLog espera, sondeando cada 20 ms con tope de 10 s, a que el log del proceso contenga
// texto. Recibe el test, el proceso y el texto; falla (t.Fatalf) si no aparece a tiempo.
func esperarEnLog(t *testing.T, h *procesoHijo, texto string) {
	t.Helper()
	ctx, cancelar := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancelar()
	for !strings.Contains(h.registro.String(), texto) {
		select {
		case <-ctx.Done():
			t.Fatalf("el log no llegó a contener %q:\n%s", texto, h.registro.String())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// stopOnCleanup registra en t.Cleanup la parada del proceso (SIGTERM, su plazo, SIGKILL). Es para
// los tests que lanzan un proceso con iniciarProceso sin pasar por arrancar: se llama justo después
// de lanzarlo, para que un t.Fatalf anterior a la parada del propio test no deje el proceso suelto.
// Como parar es idempotente, si el test ya lo paró el Cleanup no vuelve a señalar ni a esperar.
func stopOnCleanup(t *testing.T, h *procesoHijo) {
	t.Helper()
	t.Cleanup(func() { h.parar(t) })
}

// TestArnes_PararMataSiNoCede prueba la rama SIGKILL de la parada con un proceso que ignora
// SIGTERM: tras el plazo se mata, el código es -1, es idempotente y varias goroutines que paran
// a la vez reciben el mismo código. No necesita Docker ni el binario del servidor, solo `sh`. Si
// falla antes de parar, el Cleanup mata el proceso: no queda un `sleep 60` suelto.
func TestArnes_PararMataSiNoCede(t *testing.T) {
	t.Parallel()
	const tope = 300 * time.Millisecond
	// exec conserva la disposición «ignorar SIGTERM», así que el que recibe las señales es sleep.
	cmd := exec.Command("sh", "-c", `trap '' TERM; echo listo; exec sleep 60`)
	h, err := iniciarProceso(cmd, tope)
	if err != nil {
		t.Fatalf("iniciarProceso: %v", err)
	}
	stopOnCleanup(t, h)
	esperarEnLog(t, h, "listo") // sin esto SIGTERM podría llegar antes del trap y matarlo sin SIGKILL

	codigos := make([]int, 3)
	inicio := time.Now()
	var espera sync.WaitGroup
	for i := range codigos {
		espera.Go(func() { codigos[i] = h.parar(t) })
	}
	espera.Wait()
	if tardo := time.Since(inicio); tardo < tope || tardo > 10*time.Second {
		t.Errorf("parar tardó %s; quería entre %s (el plazo) y 10 s", tardo, tope)
	}
	for i, c := range codigos {
		if c != -1 {
			t.Errorf("parar #%d devolvió %d, quería -1 (muerto por SIGKILL)", i, c)
		}
	}
	segunda := time.Now()
	if c := h.parar(t); c != -1 || time.Since(segunda) > 100*time.Millisecond {
		t.Errorf("la llamada repetida devolvió %d en %s, quería -1 al instante", c, time.Since(segunda))
	}
}

// TestArnes_PararLimpio prueba la parada que sí cede: un proceso que sale con 0 al recibir
// SIGTERM da código 0, la segunda llamada devuelve lo mismo, y parar un proceso que ya había
// salido por su cuenta devuelve su código de salida. Solo necesita `sh`. Los dos procesos quedan
// con su parada en el Cleanup, por si el test falla antes de pararlos.
func TestArnes_PararLimpio(t *testing.T) {
	t.Parallel()
	h, err := iniciarProceso(exec.Command("sh", "-c", `trap 'exit 0' TERM; echo listo; while :; do sleep 1; done`), 10*time.Second)
	if err != nil {
		t.Fatalf("iniciarProceso: %v", err)
	}
	stopOnCleanup(t, h)
	esperarEnLog(t, h, "listo")
	if c := h.parar(t); c != 0 {
		t.Errorf("parar devolvió %d, quería 0", c)
	}
	if c := h.parar(t); c != 0 {
		t.Errorf("la segunda llamada devolvió %d, quería 0", c)
	}

	salido, err := iniciarProceso(exec.Command("sh", "-c", `echo adios; exit 7`), 10*time.Second)
	if err != nil {
		t.Fatalf("iniciarProceso: %v", err)
	}
	stopOnCleanup(t, salido)
	<-salido.salio
	if c := salido.parar(t); c != 7 {
		t.Errorf("parar de un proceso que ya salió con 7 devolvió %d", c)
	}
	if !strings.Contains(salido.registro.String(), "adios") {
		t.Errorf("el log no recogió la salida del proceso: %q", salido.registro.String())
	}
}
