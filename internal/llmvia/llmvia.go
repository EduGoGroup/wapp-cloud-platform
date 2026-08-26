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
	// degradaciones cuenta las caídas a Nivel A (T3.5-2, D-044.41). nil ⇒ no se
	// cuenta nada y el sistema se comporta igual: ver WithDegradacionObservada.
	degradaciones ObservadorDegradacion
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
//
// 🔴 ACUMULA, NO ASIGNA — Y ES A PROPÓSITO. `bootstrap.go` (nuevoStackLLMDeCaptacion)
// llama a esta función DOS VECES en la misma construcción del Selector: una con
// local.ConPlantillas(...) y otra, por separado, con local.WithMaxOutputTokens(...).
// Es una opción variádica que se invoca más de una vez sobre el mismo NewSelector, y
// eso es exactamente el caso que rompe una asignación (`s.localOpts = opts`): la
// segunda llamada pisaba en silencio a la primera, así que solo WithMaxOutputTokens
// sobrevivía y ConPlantillas nunca llegaba al local.Provider. Consecuencia real: la
// palanca WAPP_LLM_PROMPTS_DIR (docs/funcionalidades/36-...) estaba MUERTA — nadie la
// tenía encendida en UAT, así que el defecto no se notó en campo. Custodiado por
// TestWithLocalOptions_AcumulaEntreLlamadas (caja negra, prompt real) y
// TestWithLocalOptions_localOptsAcumulaLasDosLlamadas (caja blanca, longitud del
// slice): los dos se ponen en rojo si alguien vuelve a escribir `=` en vez de
// `append`.
func WithLocalOptions(opts ...local.Option) SelectorOption {
	return func(s *Selector) { s.localOpts = append(s.localOpts, opts...) }
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
		s.avisar(ctx, tenantID, via, OrigenSeleccion, err)
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

// ============================================================================
// EL TURNO ACOTADO: UNA PREGUNTA SUELTA, DENTRO DE UN TURNO DE WHATSAPP
// (Plan 044 · Ola 3.5 · T3.5-2, ADR-0044 §5 · Nivel B)
// ============================================================================
//
// 🔴 ESTO VIVE EN ESTE FICHERO POR LA MISMA RAZÓN QUE PlazaDe, Y NO ES ORGANIZACIÓN:
// TestC2_LaViaSoloSePreguntaEnLaSeleccion recorre el AST de todo `internal/` y exige
// que la lista de ficheros que preguntan por la vía sea EXACTAMENTE su lista de
// permitidos. Sacar este método a un `turno.go` propio lo pone rojo, y la salida
// —ampliar la lista— convertiría una regla de UN sitio en una de tres.
//
// # POR QUÉ UN MÉTODO NUEVO EN EL PUERTO Y NO UNA LLAMADA POR local.Provider
//
// Es literalmente la doctrina que ya está escrita arriba, en el bloque de For: «si
// necesitas saber la vía fuera de la selección, lo que necesitas es OTRO MÉTODO EN
// EL PUERTO». PlazaDe fue el segundo; este es el tercero. Y además el camino de
// siempre no servía, por dos motivos independientes:
//
//  1. Los cinco métodos de llm.LLMProvider son las cinco etapas del pipeline
//     (P1–P5) y ninguna es esto. Meter el turno acotado en ClassifyRequest sería
//     estrenar un sexto significado para un método que ya tiene uno.
//  2. local.Provider.plazo DESCUENTA MargenVeredicto (7 s) del deadline del
//     llamante SIEMPRE. Es correcto para el pipeline —que llama con 40–45 s— y es
//     ruinoso aquí: el turno acotado dura 12 s por diseño, así que ese descuento se
//     llevaría más de la mitad del presupuesto o, con un ctx justo, devolvería
//     ErrSinPresupuesto sin tocar el cable. El margen se aplica al REVÉS en este
//     método: no se resta del plazo del Edge, se SUMA a lo que esperamos nosotros.
//
// # QUE PASE POR EL AVISADOR, PORQUE UN FALLO DE AQUÍ ES UN FALLO DE LA VÍA
//
// Armar el frame por nuestra cuenta se salta el decorador que envuelve a For
// (notify.go), y con él el aviso al dueño del ADR-0044 §5. Sería una asimetría
// injustificable: si el Ollama del cliente está caído, su dueño tiene que enterarse
// igual lo pida el presupuesto o lo pida el carrito. Por eso este método llama al
// MISMO s.avisar —mismo motivoDe, mismo dedupe, mismo log— y no duplica ni una
// línea de ese mecanismo; lo único que añade es decir por qué puerta entró.

// ErrViaSinTurnoAcotado indica que el tenant no está en una vía capaz de servir un
// turno acotado. NO es una avería: es la respuesta correcta para un tenant en vía
// API, y por eso es un error NOMBRADO y no un `nil` mudo — hermano de
// ErrViaSinCalentamiento y por el mismo motivo.
//
// # POR QUÉ LA VÍA API NO TIENE TURNO ACOTADO (todavía)
//
// Porque el adaptador de la vía API es wapp-shared/llm/api y expone EXACTAMENTE los
// cinco métodos del pipeline: no hay por dónde meterle un prompt suelto sin
// ampliar el puerto compartido y publicar una release de shared. Y no hace falta
// para esta ola: el turno acotado nace para que el carrito entienda «mejor dos», y
// el tenant en vía API no se queda sin carrito — se queda sin ESE escalón, o sea en
// el Nivel A de siempre (el reprompt), que es la degradación que este plan diseñó.
// Cuando alguien quiera cerrarlo, el sitio es el puerto de shared, no un `if` aquí.
var ErrViaSinTurnoAcotado = errors.New("llmvia: la vía del tenant no sabe servir un turno acotado")

// PlazoTurno es el presupuesto de UN turno acotado, el que viaja como `timeout_ms`
// en el frame. 🔴 EL NÚMERO ESTÁ RAZONADO Y NO SE TOCA SIN REHACER LA CUENTA:
//
//		MEDIDO (2026-08-26, qwen3:1.7b, 18–20 tokens de salida, prefijo CALIENTE):
//		  VPS (CPU, ~6 tok/s):   mediana 4.588 ms, máximo 7.932 ms
//		  Local (GPU):           mediana   502 ms, máximo   760 ms
//		FRÍO (prefijo no cacheado): VPS 17.980 ms, local ~1.800 ms
//
//	 1. EL TECHO NO PUEDE ENVENENAR EL BREAKER DEL TENANT, que es COMPARTIDO con el
//	    pipeline de intakes. El Edge marca «lenta» toda respuesta que pase de 0,8 ×
//	    timeout_ms (ADR-0042). Con 12 s el umbral queda en 9.600 ms, por encima del
//	    peor caso caliente medido (7.932 ms) ⇒ las respuestas SANAS no cuentan como
//	    lentas. Con un timeout_ms de 5 s el umbral sería 4.000 ms y marcaría lentas
//	    CASI TODAS las respuestas buenas del VPS, abriendo el circuito del tenant por
//	    haber trabajado bien — y quien pagaría ese circuito abierto sería el
//	    pipeline, que no ha hecho nada.
//	 2. Y A LA VEZ TIENE QUE CORTAR EL CASO FRÍO (17.980 ms). Un turno que paga
//	    prefill frío NO CABE en un turno de WhatsApp, así que se corta y se degrada a
//	    Nivel A con aviso, que es el mecanismo del ADR-0044 §5 tal cual.
//
// 🔴 Y POR ESO NO SE CONSTRUYE NINGÚN DETECTOR DE «PREFIJO FRÍO»: el timeout YA lo
// implementa. Un detector sería una segunda verdad sobre lo mismo, con su propio
// estado y su propia forma de desincronizarse. Si te ves escribiéndolo, párate.
const PlazoTurno = 12 * time.Second

// TechoTurno es el presupuesto de SALIDA del turno acotado (campo 7 del frame).
// La salida real medida son 18–20 tokens —es un objeto de tres claves cortas y el
// JSON Schema forzado no deja producir más— así que 128 es ~6,5× lo observado: de
// sobra para el caso legítimo y suficientemente bajo para que un modelo degenerado
// no se coma el plazo entero generando basura. Es el mismo criterio de la tabla de
// etapas de llmvia/local, aplicado a una salida mucho más pequeña.
const TechoTurno int32 = 128

// TurnoRequest es lo que hay que saber para servir un turno acotado. El PROMPT y el
// ESQUEMA vienen armados de fuera y este paquete no los mira.
//
// 🔴 ESO ES C2, NO PEREZA: «este paquete no tiene un solo prompt propio». Quien sabe
// qué preguntar es quien conoce el dominio de la pregunta (el resolutor del carrito,
// internal/turnoacotado); aquí solo se elige la vía y se empuja el frame. Si un día
// el prompt del turno acabara escrito en este fichero, habría dos sitios donde vive
// el conocimiento del dominio y el segundo sería invisible.
type TurnoRequest struct {
	// Prompt es el texto YA COMPUESTO (instrucciones + few-shot + la pregunta).
	// Viaja verbatim: el Edge lo entrega al modelo en UN solo turno de usuario.
	Prompt string
	// Formato es el JSON Schema SERIALIZADO que fuerza la forma de la respuesta.
	// Viaja opaco como string —el campo del proto es un string y el Edge lo
	// distingue de la cadena "json" mirando si empieza por '{'— así que aquí no se
	// parsea ni se valida: un esquema roto tiene que llegar arriba como el 400 del
	// proveedor que es, no convertirse en otra cosa por el camino.
	Formato string
}

// Turno sirve UN turno acotado del Nivel B y devuelve el texto CRUDO del modelo.
//
// No lo parsea, no lo valida y no comprueba que sea JSON: el contrato es idéntico
// al de Frame.Infer y por el mismo motivo. Quien pregunta es quien sabe qué forma
// espera, y la última palabra sobre lo que el modelo dijo la tiene Go, no el modelo.
//
// # LOS DOS RELOJES, Y POR QUÉ EL MARGEN SE SUMA EN VEZ DE RESTARSE
//
//	al Edge se le pide          PlazoTurno                        (12 s)
//	el Gateway espera           PlazoTurno + DefaultInferGrace    (17 s)
//	nosotros esperamos hasta    PlazoTurno + MargenVeredicto      (19 s)
//
// Con MargenVeredicto (7 s) > DefaultInferGrace (5 s), el timer del Gateway vence
// ANTES que nuestro ctx, así que el desenlace es DETERMINISTA: o llega el timeout
// nombrado del Edge —el caso normal, a los ~12 s— o el Gateway emite `timeout` CON
// motivo. Lo que nunca gana es nuestro `ctx.Done()`, que sería
// ErrInferenceAbandonada: sin motivo, SIN AVISO AL DUEÑO y mintiendo sobre la causa.
// Es la misma aritmética que documenta local.MargenVeredicto, aplicada del derecho:
// allí se resta del presupuesto del llamante porque el llamante trae 40 s; aquí el
// presupuesto lo fija esta constante y el margen es tiempo EXTRA de espera nuestra.
//
// ⚠️ El ctx del llamante SIGUE MANDANDO cuando es más corto: context.WithTimeout se
// queda con el que venza antes. Un turno de WhatsApp trae ~30 s
// (Flow.IncomingTimeout), o sea holgura de sobra para los 19; si algún llamante
// futuro trae menos, este método no se lo inventa.
func (s *Selector) Turno(ctx context.Context, tenantID, originSessionID string, t TurnoRequest) (string, error) {
	cfg, found, err := s.cfg.Get(ctx, tenantID)
	if err != nil {
		return "", fmt.Errorf("llmvia: leyendo la configuración LLM del tenant: %w", err)
	}
	// ==================================================================
	// 🔴 EL MISMO SWITCH POR VÍA DE For y PlazaDe, no uno nuevo: mismas dos ramas,
	// mismo default de REQ-33 (sin fila ⇒ local) y mismo error para el valor fuera
	// del vocabulario. Tres hermanos que se amplían JUNTOS o uno empieza a mentir.
	// ==================================================================
	via := tenantllm.ViaLocal
	if found {
		via = cfg.Via
	}
	switch via {
	case tenantllm.ViaLocal:
	case tenantllm.ViaAPI:
		return "", ErrViaSinTurnoAcotado
	default:
		return "", fmt.Errorf("%w: %q (tenant %s)", ErrViaDesconocida, via, tenantID)
	}
	if s.frame == nil {
		// Mismo error que devolvería local.New: un selector sin cable es un fallo de
		// ARRANQUE, y decirlo con el vocabulario de siempre evita estrenar un tercer
		// nombre para el mismo problema.
		return "", local.ErrSinTransporte
	}

	ctx, cancel := context.WithTimeout(ctx, PlazoTurno+local.MargenVeredicto)
	defer cancel()
	raw, err := s.frame.Infer(ctx, tenantID, gatewaygrpc.InferRequest{
		Prompt: t.Prompt,
		Format: t.Formato,
		// TEMPERATURA 0, y no es configurable a propósito: esto no redacta nada, elige
		// entre opciones que ya existen. El reintento a 0,3 por calidad que el pipeline
		// tiene previsto (REQ-02/REQ-03) aquí NO aplica — un segundo viaje de 4–8 s
		// dentro del mismo turno de WhatsApp cuesta más de lo que rescata.
		Temperature: 0,
		Timeout:     PlazoTurno,
		// La sesión de la CONVERSACIÓN que preguntó: es trazabilidad y, si está viva,
		// es además el stream por el que sale, así que contesta el mismo Edge que
		// recibió el mensaje — el que tiene el prefijo de este prompt caliente.
		OriginSessionID: originSessionID,
		MaxOutputTokens: TechoTurno,
		// SOLO RÓTULO (ver InferRequest.Class). Que sea `interactivo` no le pide nada
		// al Edge ni mueve ningún umbral: es para que en el parte de inferencia se
		// pueda separar lo que alguien estaba esperando de lo que corría de fondo.
		Class: gatewaygrpc.ClaseInteractivo,
	})
	// EL AVISADOR, REUSADO. No se duplica motivoDe ni el dedupe ni el log: se llama
	// al mismo sitio que llama el decorador de For, diciendo por qué puerta se entró.
	// Un err nil no avisa ni cuenta (primera línea de avisar).
	s.avisar(ctx, tenantID, via, OrigenTurno, err)
	if err != nil {
		return "", err
	}
	return raw, nil
}
