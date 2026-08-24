// Package llmvia es EL ÚNICO SITIO DEL REPO QUE PREGUNTA POR LA VÍA (Plan 044 ·
// Ola 1.6 · T1.6-3; D-044.28, D-044.29, REQ-33, REQ-37, ADR-0044 §C2, ADR-0045).
//
// # Qué resuelve
//
// Un tenant tiene UNA vía activa —`local` (su propio Edge) o `api` (un proveedor
// externo con su credencial)— y el pipeline no debe saber cuál le tocó. Este paquete
// traduce `tenant_llm.via` a un llm.LLMProvider ya armado, y a partir de ahí todo el
// mundo llama a los mismos cinco métodos.
//
// # 🔴 C2: «SI HAY UN `if via` FUERA DEL ADAPTADOR, ES DEFECTO» (REQ-37)
//
// La selección es un switch de arranque sobre la vía y NADA MÁS: no hay enrutador
// por tarea ni registro de vías (D-044.21 sigue muerta). El switch de Selector.For
// es el único del repo, y eso NO se confía a la disciplina de nadie:
// TestC2_LaViaSoloSePreguntaEnLaSeleccion recorre el AST de todo `internal/` y falla
// si aparece una comparación por vía fuera de la lista de sitios permitidos —lista
// que es corta, explícita y con el motivo de cada uno escrito al lado—.
//
// # Por qué el provider se construye POR PETICIÓN y no una vez al arrancar
//
// Porque la vía es POR TENANT y cambia sin desplegar: es una fila que el dueño edita
// con un PUT. Un provider cacheado al arranque serviría la vía de ayer, y peor aún,
// la vía de ayer con la credencial de ayer. El coste de construirlo es un SELECT (la
// fila) más, en la vía API, un descifrado del sobre; el de la vía local es cero.
// Cachear eso exigiría invalidar por escritura, que es exactamente el tipo de estado
// que este ecosistema evita cuando no ha medido que haga falta.
//
// # La credencial no pasa por aquí más de lo imprescindible
//
// El store separa Get (sin clave) de APIKey (con clave) a propósito, y esa
// separación se respeta: la clave se pide SOLO en la rama `api`, se le entrega al
// provider y no se guarda, no se loguea y no vuelve a nadie.
package llmvia

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/llm/api"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/degradation"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/llmvia/local"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/tenantllm"
)

// ErrViaDesconocida indica que la fila del tenant declara una vía que este código no
// sabe servir. En la práctica solo puede llegar aquí si alguien escribió la columna
// por fuera del CHECK `tenant_llm_via_check`: se devuelve error y NO se elige una
// vía por defecto, porque adivinar la vía de un tenant es exactamente lo que REQ-33
// prohíbe («mientras un tenant tenga una vía configurada, el sistema jamás deberá
// usar la otra»).
var ErrViaDesconocida = errors.New("llmvia: vía fuera del vocabulario cerrado (local|api)")

// ErrSinConfig indica que el Selector se construyó sin store. Fallo de arranque.
var ErrSinConfig = errors.New("llmvia: el selector necesita el store de tenant_llm")

// avisoTimeout acota la escritura del aviso de degradación. Es un techo pequeño a
// propósito: el aviso corre en el camino de FALLO de una inferencia que ya se
// perdió, y hacer esperar más al llamante por una notificación sería pagar dos veces
// el mismo incidente.
const avisoTimeout = 3 * time.Second

// Store es lo que el selector necesita de la configuración LLM del tenant. Lo
// satisface *tenantllm.Postgres.
//
// Son los DOS métodos, y no uno: Get devuelve la vía sin la credencial (y es lo
// único que la rama local necesita), APIKey la descifra (y solo la rama api la
// llama). Pedir un puerto con un solo método «que lo devuelva todo» reintroduciría
// la clave en el camino de la vía local, donde no pinta nada.
type Store interface {
	Get(ctx context.Context, tenantID string) (tenantllm.Config, bool, error)
	APIKey(ctx context.Context, tenantID string) (string, error)
}

// Notifier escribe el aviso de degradación al dueño. Lo satisface
// *degradation.Notifier.
//
// El instante viaja como argumento —no lo pone el escritor— porque de él depende la
// ventana de dedupe: dos fallos de la misma ventana tienen que caer en el mismo
// bucket o REQ-38 se rompe por el camino largo.
type Notifier interface {
	Record(ctx context.Context, tenantID string, reason degradation.Reason, via string, at time.Time) (bool, error)
}

// Selector traduce la vía configurada de un tenant en un llm.LLMProvider listo para
// usar, ya envuelto con la notificación de degradación.
type Selector struct {
	cfg      Store
	frame    local.Frame
	notifier Notifier
	log      logger.Logger

	localOpts []local.Option
	// ahora es el reloj con el que se sella el instante del fallo. nil ⇒ time.Now.
	ahora func() time.Time
}

// SelectorOption configura el Selector al construirlo.
type SelectorOption func(*Selector)

// WithFrame inyecta el transporte de la vía local (lo satisface *gatewaygrpc.Server).
// Sin él, un tenant en vía local falla al construir su provider — con error, nunca en
// silencio.
func WithFrame(f local.Frame) SelectorOption { return func(s *Selector) { s.frame = f } }

// WithNotifier inyecta el escritor de avisos de degradación. Sin él el sistema
// degrada igual (la conducta de Nivel A no depende de esto) pero NADIE SE ENTERA: el
// aviso se queda en un log. Se admite nil a propósito para que los tests y los
// arranques parciales no arrastren una base de datos.
func WithNotifier(n Notifier) SelectorOption { return func(s *Selector) { s.notifier = n } }

// WithLocalOptions fija las opciones del adaptador local (formato, timeout).
func WithLocalOptions(opts ...local.Option) SelectorOption {
	return func(s *Selector) { s.localOpts = opts }
}

// WithClock inyecta el reloj del instante del fallo. Para tests.
func WithClock(f func() time.Time) SelectorOption { return func(s *Selector) { s.ahora = f } }

// NewSelector construye el selector sobre el store de tenant_llm.
func NewSelector(cfg Store, log logger.Logger, opts ...SelectorOption) (*Selector, error) {
	if cfg == nil {
		return nil, ErrSinConfig
	}
	s := &Selector{cfg: cfg, log: log}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// For devuelve el llm.LLMProvider de la vía configurada del tenant.
//
// originSessionID es la sesión de WhatsApp cuya conversación originó la pregunta,
// cuando el llamante la conoce; vacía es un estado legítimo —la inferencia es de
// alcance EDGE, no de sesión— y el proto la admite vacía sin error.
//
// Se le entrega al adaptador local con local.WithOriginSession y de ahí viaja en el
// frame hasta gatewaygrpc.inferenceSession, que la usa para DOS cosas: es el
// session_id de trazabilidad del payload, y —si esa sesión está viva— es además el
// stream por el que sale, así que la pregunta la atiende el mismo Edge que recibió
// el mensaje del cliente. Si no está viva, el Gateway cae a la primera alfabética de
// las vivas del tenant: esta función RELLENA EL DATO, no cambia esa política de
// caída. La vía API lo ignora, y esa asimetría NO es un `if via` encubierto: es un
// dato que se le pasa al constructor de un adaptador, no una decisión de conducta.
//
// 🔴 QUE ESTE PÁRRAFO SEA VERDAD COSTÓ UNA TAREA (T1.7-8). El parámetro se pedía
// desde T1.6-3 y se TIRABA —WithOriginSession no tenía un solo llamante—, así que el
// session_id del frame viajaba SIEMPRE vacío y el Edge lo elegía el orden
// alfabético. Con un Edge no se nota; con dos del mismo tenant, la inferencia salía
// siempre por el mismo y el otro no calentaba NUNCA su caché de prefijos, que es el
// recurso que esta ola entera intenta aprovechar. Lo custodia un test del selector,
// para que no vuelva a ser una promesa escrita aquí.
//
// 🔴 UN TENANT SIN FILA ESTÁ EN LA VÍA LOCAL, y no es un default defensivo: lo fija
// REQ-33 y lo dice el contrato del store («la ausencia de fila ES una respuesta y
// significa una vía concreta»). Tratarlo como «desconocido» dejaría sin pipeline a
// todo tenant que no haya tocado nunca la pantalla de configuración, que hoy son
// todos.
func (s *Selector) For(ctx context.Context, tenantID, originSessionID string) (llm.LLMProvider, error) {
	cfg, found, err := s.cfg.Get(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("llmvia: leyendo la configuración LLM del tenant: %w", err)
	}
	via := tenantllm.ViaLocal
	if found {
		via = cfg.Via
	}

	var prov llm.LLMProvider
	// ==================================================================
	// 🔴 EL ÚNICO SWITCH POR VÍA DEL REPO (C2). Si necesitas preguntar por la vía en
	// otro sitio, lo que necesitas de verdad es otro método en el puerto.
	// ==================================================================
	switch via {
	case tenantllm.ViaLocal:
		prov, err = s.localProvider(tenantID, originSessionID)
	case tenantllm.ViaAPI:
		prov, err = s.apiProvider(ctx, tenantID, cfg)
	default:
		return nil, fmt.Errorf("%w: %q (tenant %s)", ErrViaDesconocida, via, tenantID)
	}
	if err != nil {
		// El fallo al CONSTRUIR el adaptador es un fallo de la vía tanto como el fallo
		// al consumirlo: para el dueño, «tu credencial ya no vale» y «tu proveedor
		// devolvió 500» son el mismo problema visto en dos momentos. Por eso se avisa
		// aquí también, con el mismo mapeo y el mismo dedupe.
		s.avisar(ctx, tenantID, via, err)
		return nil, err
	}
	return s.notifying(prov, tenantID, via), nil
}

// localProvider arma el adaptador de la vía local con la sesión de origen de ESTA
// petición, que es lo único que cambia entre dos llamadas a For.
//
// ⚠️ LAS OPCIONES SE COPIAN, no se le hace append a s.localOpts. El Selector se
// comparte entre goroutines y For no lo muta: un append directo sobre el slice del
// Selector escribiría en SU array subyacente en cuanto tuviera capacidad de sobra, y
// dos peticiones concurrentes se pisarían la sesión de origen —un cruce de
// trazabilidad entre tenants distintos, silencioso y reproducible una de cada
// muchas—. La copia cuesta una asignación por inferencia, al lado de un viaje al
// Ollama del cliente.
func (s *Selector) localProvider(tenantID, originSessionID string) (llm.LLMProvider, error) {
	opts := make([]local.Option, 0, len(s.localOpts)+1)
	opts = append(opts, s.localOpts...)
	opts = append(opts, local.WithOriginSession(originSessionID))
	prov, err := local.New(s.frame, tenantID, opts...)
	if err != nil {
		// El nil concreto NO se devuelve como interfaz: un (*local.Provider)(nil)
		// metido en un llm.LLMProvider deja de comparar igual a nil y el siguiente
		// que escriba `if prov == nil` se llevará una sorpresa a la primera llamada.
		return nil, err
	}
	return prov, nil
}

// apiProvider arma el provider de la vía API con la credencial descifrada del
// tenant.
//
// La clave se pide AQUÍ y en ningún otro sitio. Si el tenant no la tiene —fila sin
// sobre, o sin fila— el store devuelve tenantllm.ErrNotConfigured, que el mapeo
// traduce a motivo `credencial`: es literalmente el caso que REQ-38 nombra.
func (s *Selector) apiProvider(ctx context.Context, tenantID string, cfg tenantllm.Config) (llm.LLMProvider, error) {
	key, err := s.cfg.APIKey(ctx, tenantID)
	if err != nil {
		// 🔴 EL ERROR NO REPITE LA CLAVE NI SU LONGITUD, por el mismo motivo por el que
		// no lo hace el 400 del PUT: este texto acaba en un log.
		return nil, fmt.Errorf("llmvia: credencial del tenant no disponible: %w", err)
	}
	return api.New(api.Config{
		Provider: cfg.Provider,
		Model:    cfg.Model,
		APIKey:   key,
	})
}

// ahoraFn resuelve el reloj. Mismo criterio que degradation.Notifier: el default se
// aplica en el uso, no en el constructor, para que un Selector armado con literal de
// struct en un test se comporte igual que uno construido con NewSelector.
func (s *Selector) ahoraFn() time.Time {
	if s.ahora == nil {
		return time.Now()
	}
	return s.ahora()
}
