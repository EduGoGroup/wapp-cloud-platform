package reanalisis_test

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/reanalisis"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/tenantllm"
)

// dobles_test.go — el atrezo de los tests de T4.6.
//
// 🔴 LOS SEIS DOBLES CUENTAN SUS ESCRITURAS, y no es decoración: la mitad de los
// criterios de esta tarea son sobre lo que NO se escribe (un 422 no puede dejar un
// job huérfano, un texto repetido no puede dejar dos filas, ningún camino le habla al
// cliente). Sin contadores, esos criterios se «probarían» comprobando el error que
// devuelve la función, que es exactamente lo que ya sabíamos.

const (
	tenantDePrueba   = "t-1"
	intakeDePrueba   = "3f2a9c4e-6b1d-4f8a-9c2e-0d5b7a1f3e64"
	eventoDePrueba   = "9c1f7b3a-2e64-4d85-bf10-7a3c5e9d2b48"
	sesionDePrueba   = "sess-1"
	contactoDePrueba = "c-opaco-1"
	jobDePrueba      = "b71c4e2a-95d3-4f60-8a1e-2c7b9d5f3a08"
)

// errInfra es el fallo de infraestructura con el que se prueba el FAIL-CLOSED.
var errInfra = errors.New("la base se cayó")

// ---------------------------------------------------------------------------
// LA BITÁCORA: EL ORDEN EN QUE SE PREGUNTÓ
// ---------------------------------------------------------------------------

// Los ESCALONES observables del orden del §8.1. Cada uno es un puerto distinto, así
// que la bitácora no los infiere: los ANOTA el doble cuando de verdad le preguntan.
//
// ⚠️ LA FORMA (`validarForma`) NO ESTÁ, y no falta: no toca ningún puerto —es
// vocabulario y saneo, en memoria— así que no puede dejar huella aquí. Su posición la
// custodian TestReanalizar_LaFormaGanaAlGate y
// TestReanalizar_SinLLMIntake_ElGateGanaAlaViaQueNoCoincide, que son de conducta.
const (
	pasoGateNivel   = "gate:llm_intake"
	pasoVia         = "via:tenant_llm"
	pasoGateVia     = "gate:api_llm"
	pasoSolicitud   = "solicitud"
	pasoJobVivo     = "job-vivo"
	pasoFuente      = "fuente:hilo"
	pasoDedupe      = "dedupe:pegadas"
	pasoEscribeHilo = "escribe:hilo"
	pasoAbreJob     = "abre:job"
	pasoCompone     = "compone:sobre"
)

// bitacora registra EN ORDEN a qué puerto se le preguntó. Es lo que convierte «el
// orden es contrato» de un comentario en una aserción.
//
// 🔴 EXISTE POR UNA MUTACIÓN QUE NO SE CAZABA. Mover `objetivoDe` y
// `origenDelMaterial` DELANTE de `autorizar` —o sea, invertir el orden que el propio
// fichero documenta como contrato— dejaba los 71 tests en verde. Los candados que
// había protegían la parte FINA (el 400 partido) y ninguno la GRUESA: que el gate gana
// a la solicitud y a la fuente. Con el orden invertido, un tenant sin `llm_intake`
// recibe un `422 source_unavailable` o un `404` en vez de un `403`, o sea que se le
// confirma si el evento tiene material o si la solicitud es suya sin tener derecho a
// preguntar — exactamente lo que el §8.1 evita dejando la fuente para el final.
//
// Se comparte entre los seis dobles con mutex: no porque el servicio use goroutines
// —no las usa— sino porque un doble compartido sin candado es una carrera esperando a
// que alguien la escriba.
type bitacora struct {
	mu    sync.Mutex
	pasos []string
}

func (b *bitacora) anota(paso string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pasos = append(b.pasos, paso)
}

func (b *bitacora) leer() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.pasos...)
}

// ---------------------------------------------------------------------------
// LOS DOBLES
// ---------------------------------------------------------------------------

type solicitudesFalsas struct {
	objetivo intakes.ReanalysisTarget
	err      error
	llamadas int
	libro    *bitacora
}

func (s *solicitudesFalsas) ReanalysisTargetOf(_ context.Context, _, _ string) (intakes.ReanalysisTarget, error) {
	s.libro.anota(pasoSolicitud)
	s.llamadas++
	if s.err != nil {
		return intakes.ReanalysisTarget{}, s.err
	}
	return s.objetivo, nil
}

// hiloFalso imita al *events.Store: el hilo del evento, las transcripciones ya
// pegadas y el escritor de una nueva.
type hiloFalso struct {
	entradas []events.ThreadEntry
	pegadas  []string
	errLeer  error
	errPegar error

	// escritas son los cuerpos que se mandaron a AppendPastedMessage, EN ORDEN. Es lo
	// que permite afirmar «sigue habiendo UNA» sin mirar el error de vuelta.
	escritas []string
	// limite guarda el `limit` con el que se pidió el hilo: el criterio dice que la
	// comprobación de fuente tiene que mirar las MISMAS entradas que el compositor
	// compondrá, y eso es el número.
	limite int
	libro  *bitacora
}

func (h *hiloFalso) ListThread(_ context.Context, _ string, limit int) ([]events.ThreadEntry, error) {
	h.libro.anota(pasoFuente)
	h.limite = limit
	if h.errLeer != nil {
		return nil, h.errLeer
	}
	return h.entradas, nil
}

func (h *hiloFalso) ListPastedByOwner(_ context.Context, _ string) ([]string, error) {
	h.libro.anota(pasoDedupe)
	if h.errLeer != nil {
		return nil, h.errLeer
	}
	return h.pegadas, nil
}

func (h *hiloFalso) AppendPastedMessage(_ context.Context, _ string, body string) (int, error) {
	h.libro.anota(pasoEscribeHilo)
	if h.errPegar != nil {
		return 0, h.errPegar
	}
	h.escritas = append(h.escritas, body)
	h.pegadas = append(h.pegadas, body)
	return len(h.pegadas), nil
}

// jobsFalsos imita al *intake.Postgres por el puerto estrecho de esta puerta.
type jobsFalsos struct {
	vivo     string
	errVivo  error
	errAbrir error

	// abiertos son las solicitudes de apertura que llegaron. Cero es la afirmación
	// que sostiene «un 422 no deja jobs huérfanos».
	abiertos []intake.SolicitudReanalisis
	libro    *bitacora
}

func (j *jobsFalsos) JobNoTerminalDeEvento(_ context.Context, _, _ string) (string, bool, error) {
	j.libro.anota(pasoJobVivo)
	if j.errVivo != nil {
		return "", false, j.errVivo
	}
	return j.vivo, j.vivo != "", nil
}

func (j *jobsFalsos) AbrirReanalisis(_ context.Context, s intake.SolicitudReanalisis) (string, error) {
	j.libro.anota(pasoAbreJob)
	if j.errAbrir != nil {
		return "", j.errAbrir
	}
	j.abiertos = append(j.abiertos, s)
	return jobDePrueba, nil
}

type compositorFalso struct {
	err    error
	claves []intake.WindowKey
	libro  *bitacora
}

func (c *compositorFalso) ComposeAtFlush(_ context.Context, k intake.WindowKey) error {
	c.libro.anota(pasoCompone)
	c.claves = append(c.claves, k)
	return c.err
}

// featuresFalsas responde por clave. Registra TODAS las preguntas para poder afirmar
// el invariante de D-044.28: por `api_llm` no se pregunta en la vía local.
type featuresFalsas struct {
	tiene     map[string]bool
	err       error
	preguntas []string
	libro     *bitacora
}

func (f *featuresFalsas) Has(_ context.Context, _, feature string) (bool, error) {
	// El paso se nombra por la CLAVE y no por el orden en que llegó: así la bitácora
	// distingue el gate del nivel del gate de la vía, que es justo la distinción que
	// D-044.28 exige no perder.
	if feature == entitlements.FeatureAPILLM {
		f.libro.anota(pasoGateVia)
	} else {
		f.libro.anota(pasoGateNivel)
	}
	f.preguntas = append(f.preguntas, feature)
	if f.err != nil {
		return false, f.err
	}
	return f.tiene[feature], nil
}

func (f *featuresFalsas) preguntoPor(clave string) bool {
	for _, p := range f.preguntas {
		if p == clave {
			return true
		}
	}
	return false
}

type configFalsa struct {
	cfg   tenantllm.Config
	hay   bool
	err   error
	libro *bitacora
}

func (c *configFalsa) Get(_ context.Context, _ string) (tenantllm.Config, bool, error) {
	c.libro.anota(pasoVia)
	if c.err != nil {
		return tenantllm.Config{}, false, c.err
	}
	return c.cfg, c.hay, nil
}

// ---------------------------------------------------------------------------
// EL BANCO
// ---------------------------------------------------------------------------

// banco junta los seis dobles, el servicio ya construido, el log capturado y la
// bitácora del orden.
type banco struct {
	svc         *reanalisis.Servicio
	solicitudes *solicitudesFalsas
	hilo        *hiloFalso
	jobs        *jobsFalsos
	compositor  *compositorFalso
	features    *featuresFalsas
	config      *configFalsa
	log         *bytes.Buffer
	libro       *bitacora
}

// pasos son los escalones que se recorrieron, EN ORDEN.
func (b *banco) pasos() []string { return b.libro.leer() }

// bancoDe monta el escenario FELIZ por defecto: tenant con `llm_intake`, sin fila en
// `tenant_llm` (vía efectiva `local`, D-044.48 §4 — el estado real de los tres
// tenants de UAT), una solicitud con su evento y un hilo con una frase del cliente.
// Cada test tuerce lo que necesita.
func bancoDe(t *testing.T, ajustes ...func(*banco)) *banco {
	t.Helper()
	libro := &bitacora{}
	b := &banco{
		solicitudes: &solicitudesFalsas{libro: libro, objetivo: intakes.ReanalysisTarget{
			SessionID:      sesionDePrueba,
			ContactID:      contactoDePrueba,
			EventID:        eventoDePrueba,
			Status:         intakes.StatusPendingApproval,
			LastRevisionNo: 1,
		}},
		hilo: &hiloFalso{libro: libro, entradas: []events.ThreadEntry{
			{Seq: 1, Role: events.RoleClient, Kind: events.KindMessage, Text: textoDelCliente},
		}},
		jobs:       &jobsFalsos{libro: libro},
		compositor: &compositorFalso{libro: libro},
		features:   &featuresFalsas{libro: libro, tiene: map[string]bool{entitlements.FeatureLLMIntake: true}},
		config:     &configFalsa{libro: libro},
		log:        &bytes.Buffer{},
		libro:      libro,
	}
	for _, a := range ajustes {
		a(b)
	}
	svc, err := reanalisis.NewServicio(logger.New(logger.WithWriter(b.log)),
		b.solicitudes, b.hilo, b.jobs, b.compositor, b.features, b.config)
	require.NoError(t, err)
	b.svc = svc
	return b
}

// pide ejecuta el caso de uso con el cuerpo dado.
func (b *banco) pide(t *testing.T, req reanalisis.Solicitud) (reanalisis.Resultado, error) {
	t.Helper()
	if req.TenantID == "" {
		req.TenantID = tenantDePrueba
	}
	if req.IntakeID == "" {
		req.IntakeID = intakeDePrueba
	}
	return b.svc.Reanalizar(context.Background(), req)
}

// exigeCeroEscrituras es la afirmación que comparten TODOS los desenlaces de error:
// una petición rechazada no deja fila en ninguna de las tres tablas que esta puerta
// toca. Sin esto, cada 422 dejaría un `intake_job` que ningún worker puede completar.
func (b *banco) exigeCeroEscrituras(t *testing.T) {
	t.Helper()
	require.Empty(t, b.jobs.abiertos, "un desenlace de error abrió un job huérfano")
	require.Empty(t, b.hilo.escritas, "un desenlace de error escribió en el hilo del evento")
	require.Empty(t, b.compositor.claves, "un desenlace de error compuso un sobre")
}

// configAPI deja al tenant con fila `via=api` COMPLETA: credencial y consentimiento,
// que es lo único que la 0073 permite guardar con esa vía
// (tenant_llm_via_api_completa_check).
func configAPI(b *banco) {
	b.config.hay = true
	b.config.cfg = tenantllm.Config{
		TenantID:    tenantDePrueba,
		Via:         tenantllm.ViaAPI,
		Provider:    tenantllm.ProviderAnthropic,
		Model:       "claude-x",
		HasAPIKey:   true,
		ConsentedAt: time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC),
	}
}

// configLocal deja al tenant con fila `via=local`: SIN credencial y sin
// consentimiento, que es lo único que la 0073 permite con esa vía
// (tenant_llm_local_sin_credencial_check).
func configLocal(b *banco) {
	b.config.hay = true
	b.config.cfg = tenantllm.Config{TenantID: tenantDePrueba, Via: tenantllm.ViaLocal}
}

// timeCero es el instante nulo: `consented_at` a NULL en la fila.
var timeCero time.Time

// logDeDescarte es un logger que no se mira. Los tests que SÍ miran el log usan el
// buffer del banco.
func logDeDescarte() logger.Logger { return logger.New(logger.WithWriter(&bytes.Buffer{})) }

// identificadoresDe devuelve TODOS los identificadores que aparecen en el CÓDIGO de
// un fichero de producción de este paquete, separados por espacios.
//
// 🔴 SE PARSEA EL AST Y NO SE GREPEA EL FUENTE, y la razón la descubrió el primer
// intento: los `require.NotContains` sobre el texto crudo salieron ROJOS porque los
// COMENTARIOS de este paquete nombran a propósito lo que el código no debe tener
// («no hay Notifier», «no se lee el SourceText del job»). Un grep no distingue la
// prohibición de su infracción. `parser.ParseFile` sin ParseComments descarta los
// comentarios de raíz, así que lo que queda es lo que el compilador ve.
//
// Falla ruidosamente si el fichero se renombra, que es lo correcto: un test que se
// salta porque no encuentra su sujeto no prueba nada.
func identificadoresDe(t *testing.T, nombre string) string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, nombre, nil, 0)
	require.NoError(t, err, "no se pudo parsear %s: si el fichero cambió de nombre, este test hay que actualizarlo", nombre)

	var nombres []string
	ast.Inspect(f, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			nombres = append(nombres, id.Name)
		}
		return true
	})
	// Las cadenas literales también entran: un import por ruta (`".../gateway"`) o un
	// nombre de método invocado por reflexión no serían identificadores y se colarían.
	ast.Inspect(f, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			nombres = append(nombres, lit.Value)
		}
		return true
	})
	return strings.Join(nombres, " ")
}
