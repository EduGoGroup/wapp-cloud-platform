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
	gatewaygrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/grpc"
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
	// edges es el frame VISTO COMO enrutador de Edges, cuando sabe serlo. Se
	// resuelve UNA vez en NewSelector y no en cada PlazaDe: una aserción de tipo por
	// job de lote no cuesta nada, pero el AVISO de que el transporte no sabe
	// responder tiene que salir al arrancar, no escondido en el camino caliente.
	// nil ⇒ no hay a quién preguntar (ver PlazaDe).
	edges enrutadorDeEdges
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
	// La capacidad opcional del transporte (T2.7): saber qué Edge atendería una
	// inferencia. Se resuelve aquí y se AVISA aquí — una sola línea por proceso— para
	// que un frame que no la tenga no deje el aforo de plaza inerte en silencio, que
	// es la forma cara de fallar.
	if s.frame != nil {
		if e, ok := s.frame.(enrutadorDeEdges); ok {
			s.edges = e
		} else if log != nil {
			log.Warn("llmvia: el transporte de la vía local no sabe decir qué Edge atiende; " +
				"el aforo de plaza del pipeline de lote (T2.7) quedará INERTE para este proceso")
		}
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
	// 🔴 EL SWITCH POR VÍA VIVE EN ESTE PAQUETE Y EN NINGÚN OTRO (C2). Si necesitas
	// preguntar por la vía en otro sitio, lo que necesitas de verdad es otro método
	// en el puerto — y así nació el segundo y único hermano de este switch,
	// `Selector.PlazaDe` (más abajo, T2.7): misma fuente, mismo vocabulario cerrado,
	// mismo default de REQ-33.
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

// ErrViaSinCalentamiento indica que el tenant no está en una vía que tenga caché de
// prefijo que calentar. NO es un fallo: es la respuesta correcta para un tenant en
// vía API, y por eso es un error nombrado y no un `nil` mudo — quien lo reciba tiene
// que poder decir «no había nada que hacer» en vez de «lo hice».
var ErrViaSinCalentamiento = errors.New("llmvia: la vía del tenant no tiene caché de prefijo que calentar")

// Warm emite UN calentamiento de la caché de prefijo del Edge (T1.7-4).
//
// sessionID es la sesión POR LA QUE debe salir —el Edge cuya caché se quiere llenar—,
// no la conversación que preguntó: aquí no hay ninguna. Viaja como TargetSessionID y
// por eso no aparece en el payload del frame.
//
// # 🔴 POR QUÉ ESTO VIVE AQUÍ Y NO EN EL PAQUETE DEL CALENTAMIENTO
//
// Porque preguntar «¿este tenant tiene una caché de prefijo?» ES preguntar por la
// vía, y C2 dice que eso se hace en un solo sitio. La alternativa —que el emisor del
// calentamiento mirase `tenant_llm.via`— habría puesto el segundo `if via` del repo
// justo en el fichero número N+1 contra el que TestC2_LaViaSoloSePreguntaEnLaSeleccion
// existe. La otra alternativa —calentar SIEMPRE, sin mirar— no es gratis: al tenant en
// vía API le gastaría ~50 s del Ollama de SU máquina y 250 MB de caché por un prefijo
// que nadie va a volver a pedir, compitiendo además con el clasificador que el propio
// Edge sí ejecuta.
//
// # Lo que NO hace, y es deliberado
//
//   - NO envuelve el provider con notifying(): un calentamiento que falla no es una
//     degradación de la vía del dueño. Nadie lo pidió y su fallo no le quita nada al
//     cliente; avisarle sería mandarlo a revisar un equipo que está bien. Misma
//     familia que ErrInferenceAbandonada.
//   - NO bloquea al llamante por su cuenta ni se pone reloj: el ctx lo trae quien
//     llama, que es quien sabe cuánto está dispuesto a esperar por algo que nadie
//     está esperando.
func (s *Selector) Warm(ctx context.Context, tenantID, sessionID string, in llm.ClassifyRequestInput) error {
	cfg, found, err := s.cfg.Get(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("llmvia: leyendo la configuración LLM del tenant: %w", err)
	}
	via := tenantllm.ViaLocal
	if found {
		via = cfg.Via
	}
	// ==================================================================
	// 🔴 EL MISMO SWITCH POR VÍA DE Selector.For, no uno nuevo: mismas dos ramas,
	// mismo default de REQ-33 (sin fila ⇒ local) y mismo error para el valor fuera
	// del vocabulario. Si algún día hay una tercera vía, las dos ramas se amplían
	// juntas o una de ellas empieza a mentir.
	// ==================================================================
	switch via {
	case tenantllm.ViaLocal:
	case tenantllm.ViaAPI:
		// El prefijo de un proveedor por API lo cachea —o no— el proveedor, y no hay
		// nada en nuestra mano que empujar.
		return ErrViaSinCalentamiento
	default:
		return fmt.Errorf("%w: %q (tenant %s)", ErrViaDesconocida, via, tenantID)
	}

	prov, err := s.localCalentador(tenantID, sessionID)
	if err != nil {
		return err
	}
	return prov.Warm(ctx, in)
}

// localCalentador arma el adaptador local apuntado a UN Edge concreto.
//
// Copia s.localOpts por el mismo motivo que localProvider —el Selector se comparte
// entre goroutines y un append directo sobre su slice pisaría al vecino— y devuelve
// el tipo CONCRETO a propósito: Warm no está en el puerto llm.LLMProvider y no debe
// estarlo. El puerto es lo que el pipeline usa; el calentamiento no es una etapa del
// pipeline, es mantenimiento de la máquina del cliente.
func (s *Selector) localCalentador(tenantID, sessionID string) (*local.Provider, error) {
	opts := make([]local.Option, 0, len(s.localOpts)+1)
	opts = append(opts, s.localOpts...)
	opts = append(opts, local.WithTargetSession(sessionID))
	return local.New(s.frame, tenantID, opts...)
}

// ============================================================================
// QUÉ PLAZA OCUPA UNA INFERENCIA, Y SI OCUPA ALGUNA
// (Plan 044 · Ola 2 · T2.7, ADR-0046 Mecanismo 1)
// ============================================================================
//
// 🔴 ESTO VIVE EN ESTE FICHERO Y NO EN UN `plaza.go` PROPIO, Y NO ES ORGANIZACIÓN:
// `TestC2_LaViaSoloSePreguntaEnLaSeleccion` recorre el AST de todo `internal/` y exige
// que la lista de ficheros que preguntan por la vía sea EXACTAMENTE su lista de
// permitidos. Sacar esta función a un fichero aparte lo pone rojo, y la salida
// —ampliar la lista— habría convertido una regla de un solo sitio en una regla de dos
// para ahorrarse un desplazamiento. La regla es «un fichero», así que aquí está.
//
// # POR QUÉ ESTA PREGUNTA VIVE AQUÍ Y NO EN EL WORKER DEL PIPELINE
//
// Porque la respuesta DEPENDE DE LA VÍA, y la vía se pregunta en un solo sitio: el
// selector. La regla de la casa (C2 del ADR-0044, y el bloque de For) es que si hace
// falta saber la vía fuera de la selección, lo que hace falta de verdad es OTRO
// MÉTODO EN EL PUERTO. Esto es ese método.
//
// El worker del pipeline recibe un `(edgeID, ok)` y no sabe —ni tiene por qué— por
// qué un tenant no tiene plaza: puede ser que esté en vía API o que no tenga ningún
// Edge conectado ahora mismo. Las dos cosas significan lo mismo para él: no hay
// plaza que tomar, adelante.
//
// # POR QUÉ LA VÍA API NO TIENE PLAZA
//
// Porque el entero del Mecanismo 1 protege UNA MÁQUINA —un Ollama por Edge—, y por
// la vía API no hay máquina del cliente en el camino: la llamada sale a un proveedor
// remoto que atiende en paralelo. Allí el tope que importa es de PRECIO, no de
// capacidad, y es otra decisión que este plan no toma. Serializar dos cadenas de
// lote de un tenant en vía API sería una restricción inventada: cuesta throughput y
// no protege nada.

// enrutadorDeEdges es la CAPACIDAD OPCIONAL del transporte de la vía local: saber
// decir qué Edge atendería una inferencia de este (tenant, sesión). La satisface
// *gatewaygrpc.Server, que es el mismo objeto que ya viaja como local.Frame.
//
// 🔴 ES EL MISMO COLABORADOR, NO UNO NUEVO, y esa es toda la gracia: quien sabe por
// qué Edge sale una inferencia es exactamente quien la manda. Un segundo puerto que
// cablear sería un segundo sitio donde olvidarse, y olvidarlo dejaría el aforo
// INERTE sin un solo error.
type enrutadorDeEdges interface {
	PlazaDe(tenantID, originSessionID string) (string, bool)
}

// El transporte de PRODUCCIÓN la satisface, y se comprueba EN COMPILACIÓN. Sin esta
// línea, el día que alguien renombrara `Server.PlazaDe` el aforo se apagaría entero
// —la aserción de tipo devolvería false, saldría el Warn del arranque y nada más—
// sin que un solo test se pusiera rojo.
var _ enrutadorDeEdges = (*gatewaygrpc.Server)(nil)

// PlazaDe devuelve el Edge cuya plaza ocuparía una inferencia de este tenant
// originada en esa sesión, o `ok = false` si no ocupa ninguna.
//
// `ok = false` NO es un fallo y tiene tres orígenes legítimos, todos con la misma
// consecuencia para el llamante:
//
//   - el tenant está en vía API (no hay máquina del cliente que proteger);
//   - el tenant no tiene ninguna sesión viva en esta réplica (no hay Edge todavía;
//     la inferencia fallará por su cuenta con `edge_offline` y el backoff hará su
//     trabajo);
//   - el transporte no sabe responder a la pregunta (ver NewSelector: se avisa UNA
//     vez al arrancar, no una vez por job).
//
// El error queda para lo que sí lo es: la configuración del tenant no se pudo leer,
// o su vía está fuera del vocabulario cerrado.
//
// ⚠️ CUESTA UNA LECTURA DE `tenant_llm` POR JOB DE LOTE, no por llamada al modelo:
// se resuelve una vez, antes de la cadena, y la cadena dura minutos. Es la misma
// lectura que `For` hace después por cada etapa.
func (s *Selector) PlazaDe(ctx context.Context, tenantID, originSessionID string) (string, bool, error) {
	cfg, found, err := s.cfg.Get(ctx, tenantID)
	if err != nil {
		return "", false, fmt.Errorf("llmvia: leyendo la configuración LLM del tenant: %w", err)
	}
	// Un tenant SIN FILA está en la vía local (REQ-33), igual que en For. Este
	// default no es defensivo: es la respuesta correcta, y hoy la de todos.
	via := tenantllm.ViaLocal
	if found {
		via = cfg.Via
	}
	switch via {
	case tenantllm.ViaLocal:
		if s.edges == nil {
			return "", false, nil
		}
		edgeID, ok := s.edges.PlazaDe(tenantID, originSessionID)
		return edgeID, ok, nil
	case tenantllm.ViaAPI:
		return "", false, nil
	default:
		// El mismo vocabulario cerrado y el mismo error que For: una vía inventada no
		// se degrada a «sin plaza», porque eso escondería una fila corrupta detrás de
		// una conducta que parece normal.
		return "", false, fmt.Errorf("%w: %q (tenant %s)", ErrViaDesconocida, via, tenantID)
	}
}
