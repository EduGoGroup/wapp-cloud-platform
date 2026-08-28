package publicapi_test

// plazoescritura_test.go — el plazo de escritura EXTENDIDO de
// `POST /api/v1/intakes/{id}/quote-suggestion` (Plan 047 · Ola 2).
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 POR QUÉ ESTE TEST LEVANTA UN SERVIDOR DE VERDAD Y NO USA EL HARNESS
// ════════════════════════════════════════════════════════════════════════════
//
// Porque lo que se prueba NO EXISTE fuera de una conexión: el WriteTimeout de
// http.Server y el plazo que lo sustituye son plazos de la CONEXIÓN, y un
// httptest.ResponseRecorder —el que usa `call()`— no tiene ninguna. Contra el
// recorder, este arreglo y su ausencia dan exactamente el mismo resultado; un test
// escrito ahí no podría fallar nunca, que es la definición de decorado.
//
// Y por eso también la cadena de envoltorios se monta como la monta el bootstrap
// (metrics.InstrumentHTTP por fuera de httpapi.PublicRateLimit por fuera del mux,
// internal/bootstrap/http.go:110-113) en vez de servir el mux pelado: el plazo llega a
// la conexión desenvolviendo `Unwrap()` envoltorio a envoltorio, así que con el mux
// pelado el test saldría verde aunque el envoltorio de métricas —el que hay en
// producción— cortara la cadena. Es UNA COPIA de la composición de producción y como
// tal puede divergir; lo que la sostiene es que sin ella el test no prueba lo que dice.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"
	"golang.org/x/time/rate"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes/quotetext"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/metrics"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

// Las tres cotas del test, y la desigualdad que tienen que cumplir:
//
//	writeTimeoutDelTest  <  lentitudDelTest  <  1,5 s (el dbCtx por defecto)
//
// La primera desigualdad es la que hace que la ruta de control MUERA (si el handler
// respondiera antes del WriteTimeout no habría nada que observar) y la segunda es la
// que garantiza que muere POR EL PLAZO DE ESCRITURA y no por el plazo de base de
// `dbCtx`, que daría un 504 con cuerpo y no una conexión sin respuesta.
//
// Son milisegundos y no los 60 s de producción a propósito: lo que se prueba es que el
// plazo de la conexión SE SUSTITUYE en esa ruta, y eso se observa igual con cualquier
// par de números que cumpla la desigualdad. Probarlo con los 60 s reales costaría un
// minuto de reloj por corrida y no diría nada más.
const (
	writeTimeoutDelTest = 150 * time.Millisecond
	lentitudDelTest     = 600 * time.Millisecond
)

// sugeridorLento es el generador que tarda MÁS que el WriteTimeout del servidor. No
// mira el contexto a propósito: el WriteTimeout de Go no cancela el contexto del
// handler, y un doble que se rindiera al ctx estaría simulando una conducta que el
// servidor real no tiene.
type sugeridorLento struct {
	espera time.Duration
	out    quotetext.Sugerencia
}

func (s sugeridorLento) Sugerir(_ context.Context, _, _ string) (quotetext.Sugerencia, error) {
	time.Sleep(s.espera)
	return s.out, nil
}

// sesionesLentas es la RUTA DE CONTROL: `GET /api/v1/sessions`, misma API, mismo
// middleware, misma cadena — y sin plazo extendido. Devuelve la lista vacía para que
// el único efecto observable sea el tiempo.
type sesionesLentas struct{ espera time.Duration }

func (s sesionesLentas) List(_ context.Context, _ string) ([]fleet.Session, error) {
	time.Sleep(s.espera)
	return nil, nil
}

// bufferConCerrojo es el destino del log. El servidor atiende cada petición en su
// goroutine, así que un bytes.Buffer pelado sería una carrera bajo -race.
type bufferConCerrojo struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *bufferConCerrojo) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *bufferConCerrojo) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// servidorDePlazos levanta la API pública real —Register + la cadena de envoltorios
// del bootstrap— sobre un http.Server con el WriteTimeout CORTO, y devuelve su base
// URL y el log que escribió.
func servidorDePlazos(t *testing.T, d publicapi.Deps, keys map[string]testIdentity) (*testAPI, string, *bufferConCerrojo) {
	t.Helper()

	log := &bufferConCerrojo{}
	api := newAPIConLog(d, keys, sharedlogger.New(sharedlogger.WithWriter(log)))

	mtx := metrics.New()
	// Ráfaga holgada: este test no mide el rate-limit, pero el limitador va en la
	// cadena porque en producción va, y un 429 aquí sería un falso rojo.
	lim := httpapi.NewLimiter(rate.Limit(1000), 1000)
	var handler http.Handler = api.mux
	handler = httpapi.PublicRateLimit(handler, lim, mtx, nil)
	handler = mtx.InstrumentHTTP("public", handler)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("escuchando: %v", err)
	}
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      writeTimeoutDelTest,
	}
	errServe := make(chan error, 1)
	go func() { errServe <- srv.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			t.Errorf("apagando el servidor de prueba: %v", err)
		}
		// Serve devuelve ErrServerClosed tras el Shutdown; CUALQUIER otro error
		// significa que el servidor murió antes y que los asertos del test midieron
		// otra cosa.
		if err := <-errServe; err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("el servidor de prueba no sirvió: %v", err)
		}
	})
	return api, "http://" + ln.Addr().String(), log
}

// pedir ejecuta una petición autenticada contra el servidor y dice si ALGUNO de sus
// intentos viajó por una conexión REUTILIZADA. La reutilización importa: el plazo
// extendido se pone sobre una conexión concreta, y si Go no reusara la conexión, el
// control no probaría que el plazo no se queda pegado a ella.
//
// 🔴 «ALGUNO» y no «el último», y no es un matiz: cuando el servidor cierra una
// conexión reutilizada sin escribir nada, el Transport REINTENTA la petición idempotente
// sobre una conexión nueva, y ese segundo intento dispara GotConn otra vez con
// Reused=false. Quedarse con el último valor —lo primero que escribí— hacía que el test
// se quejara de que no hubo reutilización justo cuando SÍ la había habido.
func pedir(t *testing.T, cli *http.Client, api *testAPI, credencial, metodo, url string) (*http.Response, []byte, bool, error) {
	t.Helper()
	var reusada atomic.Bool // lo escribe la goroutine del Transport, no la del test
	trace := &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			if info.Reused {
				reusada.Store(true)
			}
		},
	}
	req, err := http.NewRequestWithContext(
		httptrace.WithClientTrace(context.Background(), trace), metodo, url, strings.NewReader(""))
	if err != nil {
		t.Fatalf("armando la petición: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+api.token(credencial))
	resp, err := cli.Do(req)
	if err != nil {
		return nil, nil, reusada.Load(), err
	}
	body, errCuerpo := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if errCuerpo != nil {
		// El cuerpo a medias es justo el síntoma del defecto (la conexión se cierra a
		// mitad de la respuesta), así que se devuelve como fallo de la petición y no se
		// traga: un cuerpo truncado leído como bueno sería un verde falso.
		return resp, body, reusada.Load(), errCuerpo
	}
	return resp, body, reusada.Load(), nil
}

// TestQuoteSuggestion_PlazoDeEscritura_SoloEsaRuta es el test del arreglo, y afirma
// las DOS mitades sobre el MISMO servidor y la MISMA conexión:
//
//  1. la sugerencia de cotización, que tarda más que el WriteTimeout del servidor,
//     RESPONDE — porque su ruta sustituye el plazo de escritura;
//  2. la ruta de control, igual de lenta y sobre la conexión que acaba de servir a la
//     primera, NO responde — porque el plazo extendido es de esa petición y no de la
//     conexión ni del servidor.
//
// 🔴 QUÉ MUERE SI SE QUITA QUÉ (comprobado, no supuesto):
//   - sin `SetWriteDeadline` en conPlazoDeRedacción → muere (1);
//   - sin `Unwrap()` en metrics.statusRecorder → muere (1), porque el controlador de
//     respuesta no alcanza la conexión y el plazo no se pone;
//   - moviendo el envoltorio DENTRO de protectRead → muere (1), porque el
//     ResponseWriter de accessLog tampoco desenvuelve;
//   - poniendo el plazo en el servidor (subiendo el WriteTimeout global) → muere (2).
func TestQuoteSuggestion_PlazoDeEscritura_SoloEsaRuta(t *testing.T) {
	sug := sugeridorLento{espera: lentitudDelTest, out: quotetext.Sugerencia{
		Texto: "Hola! Torta $18000. Total $18000", Origen: quotetext.OrigenLLM,
	}}
	ent := entitlements.NewFake()
	ent.Enable(tenantA, entitlements.FeatureCartBasic)
	ent.Enable(tenantA, entitlements.FeatureLLMIntake)
	deps := publicapi.Deps{
		Intakes:          intakes.NewService(bandejaPorAprobar()),
		QuoteSuggestions: sug,
		Entitlements:     ent,
		SessionDeps:      publicapi.SessionDeps{Sessions: sesionesLentas{espera: lentitudDelTest}},
	}

	api, base, log := servidorDePlazos(t, deps, intakesKeys())
	cli := &http.Client{Timeout: 10 * time.Second}

	// (1) La ruta con plazo extendido responde entera.
	resp, body, _, err := pedir(t, cli, api, keyAIntakes, http.MethodPost,
		base+"/api/v1/intakes/"+intakePorAprobar+"/quote-suggestion")
	if err != nil {
		t.Fatalf("la sugerencia no llegó al cliente: %v\n"+
			"Es EL defecto que este arreglo cierra: el handler tarda %v y el WriteTimeout del "+
			"servidor es %v, así que sin el plazo por ruta la respuesta no cabe por el cable.\nlog:\n%s",
			err, lentitudDelTest, writeTimeoutDelTest, log.String())
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sugerencia: status %d, esperaba 200 (body=%s)", resp.StatusCode, body)
	}
	var dto quoteSuggestionDTO
	if err := json.Unmarshal(body, &dto); err != nil {
		t.Fatalf("la respuesta llegó pero no es el cuerpo esperado: %v (body=%q)", err, body)
	}
	if dto.RenderedText != sug.out.Texto {
		t.Fatalf("rendered_text = %q, esperaba %q: llegó una respuesta, pero no la del generador",
			dto.RenderedText, sug.out.Texto)
	}

	// (2) La ruta de control, sobre la MISMA conexión, sigue muriendo.
	resp2, body2, reusada, err := pedir(t, cli, api, keyASessions, http.MethodGet, base+"/api/v1/sessions")
	if err == nil {
		t.Fatalf("GET /api/v1/sessions respondió %d (body=%s) tardando %v con un WriteTimeout de %v: "+
			"el plazo extendido se le pegó a una ruta que no lo pidió. El arreglo tiene que ser POR RUTA.",
			resp2.StatusCode, body2, lentitudDelTest, writeTimeoutDelTest)
	}
	if !reusada {
		// No es un fallo del arreglo, pero sí una pérdida de alcance del aserto: sin
		// reutilización, (2) no dice nada sobre si el plazo se queda pegado a la conexión.
		t.Errorf("la conexión NO se reutilizó: el aserto (2) ya no distingue «plazo por petición» "+
			"de «plazo por conexión». err=%v", err)
	}
}
