// Package reanalisis es el caso de uso de `POST /api/v1/intakes/{id}/reanalyze`
// (Plan 044 · Ola 4 · T4.6; D-044.15, REQ-24b/c/d/e, contrato completo en design
// §8.1): el dueño mira un presupuesto que la máquina interpretó mal y pide que lo
// vuelva a leer DESDE EL ORIGEN.
//
// # POR QUÉ UN PAQUETE PROPIO Y NO UN MÉTODO MÁS EN `intakes`
//
// Porque esta operación cruza CINCO fronteras que ningún otro caso de uso de la
// bandeja cruza a la vez: los entitlements del tenant, su configuración LLM
// (`tenant_llm`), el hilo cifrado del evento (`conversation_event_messages`), la
// cola del pipeline (`intake_jobs`) y el compositor del literal. Meterlo en
// `intakes.Service` obligaría a ese Service —que hoy sabe de solicitudes y de nada
// más— a depender de las cinco, y a que todo test suyo las montara. Aquí las cinco
// entran por puertos estrechos declarados del lado del consumidor, que es el idioma
// del repo (ThreadReader en el compositor, AlmacenSolicitudes en el draft).
//
// # 🔴 ESTE PAQUETE NO LE HABLA AL CLIENTE, Y NO PUEDE HACERLO (INV-1 / INV-12)
//
// Mira la lista de puertos: no hay ninguno que mande un mensaje. No hay Gateway, no
// hay Notifier, no hay SendText. Un re-análisis es una operación INTERNA del dueño
// sobre su propio pedido —el cliente ni se entera, y no debe enterarse: no le ha
// pasado nada a su pedido— y esa garantía es estructural, no una promesa en un
// comentario. Lo custodia TestServicio_NingunPuertoHablaConElCliente.
//
// # 🔴 Y NO ESPERA AL LLM
//
// El endpoint ABRE el job y devuelve. El pipeline lo reclama después, por su cuenta,
// y puede tardar minutos o morir. Eso no es una limitación: es lo que hace que el
// criterio INV-10 de T4.6 se cumpla solo. Con el proveedor caído de verdad, esta
// función ya devolvió 200 hace rato; la revisión anterior sigue intacta porque nadie
// escribió una nueva, el intake no cambió de estado porque aquí no se transiciona
// nada, y el cliente no recibió nada porque no hay por dónde. Un 500 desnudo no
// puede salir de una caída del proveedor, sencillamente porque el proveedor no se
// toca en este camino.
//
// # EL ORDEN DE LOS CHEQUEOS ES EL CONTRATO, Y ESTÁ RAZONADO EN Reanalizar
//
// design §8.1 lo fija: forma → gate base → gate de vía → credencial → solicitud →
// fuente. Lo único que se mueve respecto de esa lista es dónde cae la mitad del 400
// que necesita leer `tenant_llm`; el porqué está escrito en `resolverVia`.
package reanalisis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/stages"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/tenantllm"
)

// ---------------------------------------------------------------------------
// EL CONTRATO (design §8.1)
// ---------------------------------------------------------------------------

// Solicitud es el cuerpo de la petición, ya con el tenant del token (INV-7: el
// tenant NUNCA sale de la URL ni del cuerpo).
type Solicitud struct {
	TenantID string
	IntakeID string
	// Via AFIRMA por qué vía se quiere correr (`local` | `api`). VACÍA es válida y
	// es el caso normal: se usa la del tenant (`tenant_llm.via`), y sin fila en esa
	// tabla la vía efectiva es `local` (D-044.48 §4).
	//
	// 🔴 EL PROVEEDOR NO ESTÁ AQUÍ Y NO PUEDE ESTARLO (D-044.28 §a): `anthropic` |
	// `gemini` salen SIEMPRE de `tenant_llm`. Aceptarlo en este cuerpo sería dejar
	// que una llamada suelta se salte la configuración del tenant. Un cuerpo que lo
	// mande lo descarta encoding/json sin ruido, igual que el `tenant_id` que
	// tampoco existe: no hay dónde guardarlo.
	Via string
	// Text es material EXTRA del dueño (Plan 045 D-045.5): la transcripción de un
	// audio, el mensaje que le llegó por otro canal. SUMA al literal del evento, no
	// lo sustituye. Vacío es el caso normal —«regenera otra vez, según el origen»—.
	Text string
}

// Resultado es el 200 del contrato §8.1.
type Resultado struct {
	IntakeID string
	// RevisionNo es el número que le TOCARÁ a la revisión que escriba este job: el
	// vigente más uno.
	//
	// ════════════════════════════════════════════════════════════════════════════
	// 🔴 ES UNA PREVISIÓN Y SE PUBLICA IGUAL. LA DECISIÓN, CON SU ALTERNATIVA.
	// ════════════════════════════════════════════════════════════════════════════
	//
	// El §8.1 publica `{"revision_no":3, …}` de forma SÍNCRONA, y por el propio §8.1
	// la revisión solo se escribe cuando el job TERMINA — minutos después, en otro
	// proceso, y puede no terminar nunca. Así que este número no afirma un hecho:
	// anticipa uno. Hay dos formas en que puede no cumplirse: el job muere (no habrá
	// ninguna revisión nueva) o entra otra revisión entremedias —una corrección del
	// dueño por `PUT …/items`— y la del re-análisis acaba siendo la 4.
	//
	// SE PUBLICA, y lo que lo hace honesto es el campo de al lado: `status` sale
	// SIEMPRE como `processing`, que significa literalmente «esto todavía no ha
	// pasado». Un lector que tomara `revision_no` por un hecho consumado estaría
	// ignorando el otro campo del MISMO objeto. Y el número es lo que una UI necesita
	// para decir «se está preparando la revisión 3» sin tener que adivinarlo.
	//
	// LA ALTERNATIVA DESCARTADA era omitirlo. Se descartó porque rompe el contrato
	// escrito sin ganar nada: el consumidor tendría que deducir el número igual, y lo
	// deduciría con la misma aritmética y sin que nadie se lo hubiera dicho.
	//
	// ⚠️ NO ES EL CASO DEL LITERAL `RevisionNo: 1` QUE T4.10 TUVO QUE MATAR, aunque
	// se parezca. Aquel AFIRMABA el número de una revisión ya escrita y lo afirmaba
	// mal —de ahí la regla «un número falso es peor que uno ausente», y el 0 que el
	// schema rechaza—. Éste no afirma nada sobre el pasado: dice qué se está
	// preparando, y va acompañado del estado que dice que no está hecho. Quien
	// necesite el número CIERTO lo lee de la solicitud cuando el job acabe.
	RevisionNo int
	JobID      string
	// Via es la vía EFECTIVA con la que se abrió el job, ya resuelta.
	Via string
	// Status es el estado DE LA PETICIÓN visto por quien llama, no el de la fila.
	// Siempre EstadoEnCurso.
	Status string
}

// EstadoEnCurso es el `status` del 200 (design §8.1: `"status":"processing"`).
//
// ⚠️ NO ES `intake_jobs.status`. El job nace en `pending` —ver
// `intake.AbrirReanalisis` para el porqué— y solo pasa a `processing` cuando un
// worker lo reclama, segundos o minutos después. Lo que este literal dice es «tu
// petición se aceptó y hay trabajo en marcha», que es la pregunta que se hace quien
// llama. Coinciden en el nombre y no en el sujeto; por eso está declarado aquí y no
// se importa de `intake`.
const EstadoEnCurso = "processing"

// Razones del `422 source_unavailable` (design §8.1). Son un vocabulario del
// CONTRATO, no de la base: viajan al cliente dentro del cuerpo del error.
const (
	// RazonPurgada — el literal EXISTIÓ y ya no está: quedan filas `message` del
	// evento pero ninguna conserva cuerpo. Mensaje al dueño (§8.1, literal): «el texto
	// original de esta conversación ya venció por la política de retención; no se
	// puede regenerar desde el origen».
	//
	// ⚠️ HOY NADIE PUEDE PRODUCIR ESTE ESTADO, y se dice para que nadie lo busque en
	// campo: el Plan 046 NO construyó ninguna poda de `conversation_event_messages`
	// (tres tareas descartadas por el ADR-0043), así que la retención de esa tabla
	// es INDEFINIDA. La rama existe porque vaciar el cuerpo dejando la fila es la
	// forma que esa poda tendrá el día que se construya —es exactamente lo que hace
	// la poda perezosa hermana sobre `intake_revisions` (0079)— y porque el contrato
	// la exige. Se prueba con dobles; no es código muerto, es código que se adelanta
	// a un mecanismo ya diseñado.
	RazonPurgada = "purged"
	// RazonNuncaGuardada — no hay NI UNA fila `message` para ese evento: el tenant
	// no tenía `llm_intake` cuando ocurrió (nivel 3 del ADR-0034, no se guardó
	// nunca), o la solicitud es legada y no cuelga de ningún evento. Mensaje al dueño
	// (§8.1, literal): «esta conversación es anterior a tu plan con IA; no hay
	// original guardado».
	RazonNuncaGuardada = "never_stored"
)

// ---------------------------------------------------------------------------
// LOS DESENLACES CON NOMBRE (design §8.1)
// ---------------------------------------------------------------------------
//
// Cada uno es un TIPO y no un sentinela porque los cinco llevan un dato dentro —la
// vía que se pidió, la feature que falta, la razón, el job que estorba— y ese dato
// va en el CUERPO de la respuesta. Un sentinela obligaría al transporte a
// reconstruirlo, y reconstruirlo mal es cómo un 403 acaba nombrando la feature
// equivocada. Se leen con errors.As, igual que *intakes.PendingPriceError.

// ViaInvalidaError es el `400 invalid_via`.
type ViaInvalidaError struct {
	// Via es el valor que mandó el cliente, TAL CUAL, vacío incluido: `{"via":""}`
	// dice «no mandaste vía» y se lee igual de bien en un log que en la UI.
	Via string
	// Configurada es la vía que el tenant tiene elegida, cuando el rechazo es por
	// contradecirla (ver resolverVia). Vacía cuando el rechazo es de vocabulario.
	Configurada string
}

func (e ViaInvalidaError) Error() string {
	if e.Configurada != "" {
		return fmt.Sprintf("reanalisis: la vía %q no es la configurada por el tenant (%q)", e.Via, e.Configurada)
	}
	return fmt.Sprintf("reanalisis: %q no es una vía (local|api)", e.Via)
}

// FeatureAusenteError es el `403 feature_not_enabled`. Lleva la clave porque son
// DOS casos con dos claves distintas —`llm_intake` (el nivel) y `api_llm` (la vía)—
// y la UI ofrece un upgrade distinto con cada una.
type FeatureAusenteError struct{ Feature string }

func (e FeatureAusenteError) Error() string {
	return fmt.Sprintf("reanalisis: el tenant no tiene la capacidad %q", e.Feature)
}

// CredencialAusenteError es el `422 llm_credentials_missing`: la feature SÍ está,
// pero no hay credencial ni consentimiento.
//
// 🔴 NO SE MEZCLA CON FeatureAusenteError, y esa separación es criterio explícito de
// T4.6. «Tu plan no lo incluye» manda a la UI al paywall del add-on; «configura tus
// credenciales» la manda a los ajustes de `tenant-llm`. Fundirlos en un solo cuerpo
// dejaría a un tenant que YA PAGÓ mirando una pantalla de venta.
type CredencialAusenteError struct{ Via string }

func (e CredencialAusenteError) Error() string {
	return fmt.Sprintf("reanalisis: la vía %q exige credencial y consentimiento en tenant_llm", e.Via)
}

// FuenteAusenteError es el `422 source_unavailable`, con su razón.
type FuenteAusenteError struct{ Reason string }

func (e FuenteAusenteError) Error() string {
	return fmt.Sprintf("reanalisis: no hay literal original que re-analizar (%s)", e.Reason)
}

// EnCursoError es el `422 reanalysis_in_progress`: ya hay un job NO TERMINAL para
// ese evento. Lleva el id del job para que quien llame pueda seguirlo en vez de
// reintentar a ciegas.
type EnCursoError struct{ JobID string }

func (e EnCursoError) Error() string {
	return fmt.Sprintf("reanalisis: el evento ya tiene un job vivo (%s)", e.JobID)
}

// ErrSinCablear es el servicio al que le falta una pieza. Las seis son obligatorias
// y no opcionales con cero-valor por la misma razón que las cinco del draft: un
// servicio sin compositor abriría jobs con el sobre vacío que el pipeline mataría
// uno a uno, y nadie lo notaría hasta mirar la tabla.
var ErrSinCablear = errors.New("reanalisis: el servicio necesita log, solicitudes, hilo, jobs, compositor, features y config LLM")

// ---------------------------------------------------------------------------
// LOS PUERTOS
// ---------------------------------------------------------------------------

// Solicitudes es lo ÚNICO que este caso de uso necesita del dominio de solicitudes:
// leer de qué evento cuelga una y por qué revisión iba. NO puede transicionar, NO
// puede editar líneas y NO puede escribir revisiones — y eso es el mecanismo por el
// que «el re-análisis no cambia el estado del intake» (criterio INV-10 de T4.6) se
// sostiene en los tipos y no en una promesa. Lo satisface *intakes.Postgres.
type Solicitudes interface {
	ReanalysisTargetOf(ctx context.Context, tenantID, intakeID string) (intakes.ReanalysisTarget, error)
}

// Hilo es el historial cifrado del evento, DESCIFRADO en el borde (REQ-10c). Lo
// satisface *events.Store, que es quien tiene el FieldCipher.
type Hilo interface {
	ListThread(ctx context.Context, eventID string, limit int) ([]events.ThreadEntry, error)
	ListPastedByOwner(ctx context.Context, eventID string) ([]string, error)
	AppendPastedMessage(ctx context.Context, eventID, body string) (int, error)
}

// Jobs es la cola del pipeline vista por esta puerta: preguntar si hay uno vivo y
// abrir el del re-análisis. Deliberadamente NO es `intake.JobStore` ni
// `intake.PipelineStore`: desde aquí no se puede reclamar, ni avanzar etapa, ni
// terminar un job. Lo satisface *intake.Postgres.
type Jobs interface {
	JobNoTerminalDeEvento(ctx context.Context, tenantID, eventID string) (string, bool, error)
	AbrirReanalisis(ctx context.Context, s intake.SolicitudReanalisis) (string, error)
}

// Compositor lee el hilo, lo compone, lo cifra y lo guarda en el sobre del job. Lo
// satisface *runtime.SourceTextComposer — el MISMO que corre al cerrar una ventana
// del pipeline normal, y por eso este paquete no escribe un segundo compositor: dos
// caminos que producen el `source_text` divergirían en el primer rótulo que cambie.
type Compositor interface {
	ComposeAtFlush(ctx context.Context, key intake.WindowKey) error
}

// Features resuelve los derechos comerciales del tenant. Lo satisface
// entitlements.Resolver — el MISMO resolver cacheado que gatea el resto del carril,
// nunca uno nuevo: dos resolvers serían dos cachés y dos verdades sobre el plan.
type Features interface {
	Has(ctx context.Context, tenantID, feature string) (bool, error)
}

// ConfigLLM lee la configuración LLM del tenant. Es el puerto RECORTADO que NO
// puede devolver la credencial (mismo criterio que publicapi.TenantLLMStore): aquí
// solo hace falta saber SI la hay, no cuál es. Lo satisface *tenantllm.Postgres.
type ConfigLLM interface {
	Get(ctx context.Context, tenantID string) (tenantllm.Config, bool, error)
}

// ---------------------------------------------------------------------------
// EL SERVICIO
// ---------------------------------------------------------------------------

// Servicio ejecuta el re-análisis. Sin estado propio: todo lo que decide sale de
// los puertos y del cuerpo de la petición.
type Servicio struct {
	log         logger.Logger
	solicitudes Solicitudes
	hilo        Hilo
	jobs        Jobs
	compositor  Compositor
	features    Features
	config      ConfigLLM
}

// NewServicio construye el caso de uso. Devuelve ErrSinCablear si le falta una
// pieza: es preferible no montar la ruta a montarla y que responda 500 a medio
// camino (mismo criterio que registerIntakes con sus dependencias).
func NewServicio(log logger.Logger, solicitudes Solicitudes, hilo Hilo, jobs Jobs,
	compositor Compositor, features Features, config ConfigLLM) (*Servicio, error) {
	if log == nil || solicitudes == nil || hilo == nil || jobs == nil ||
		compositor == nil || features == nil || config == nil {
		return nil, ErrSinCablear
	}
	return &Servicio{
		log: log, solicitudes: solicitudes, hilo: hilo, jobs: jobs,
		compositor: compositor, features: features, config: config,
	}, nil
}

// Reanalizar abre el job del re-análisis y devuelve lo que el contrato §8.1 publica.
//
// ════════════════════════════════════════════════════════════════════════════
// EL ORDEN DE OPERACIONES, Y QUÉ ACCIDENTE EVITA CADA ESCALÓN
// ════════════════════════════════════════════════════════════════════════════
//
//  1. FORMA de `via`         → 400. No toca la base: es vocabulario.
//  2. FORMA de `text`        → 400. Sanear ANTES de todo lo demás es lo que
//     permite deduplicar por el hash del texto SANEADO
//     (criterio de T4.6) sin volver a sanear después.
//  3. Gate `llm_intake`      → 403. El nivel.
//  4. Vía EFECTIVA           → 400 si `via` no coincide (ver resolverVia).
//  5. Gate `api_llm`         → 403, solo si la vía efectiva es `api`.
//  6. Credencial             → 422, solo si la vía efectiva es `api`.
//  7. La SOLICITUD           → 404 si no es del tenant (nunca 403: confirmaría
//     que existe).
//  8. Job vivo del evento    → 422. Guarda además la carrera con la ventana viva
//     del cliente, que es `aggregating` y también cuenta.
//  9. La FUENTE              → 422 con su razón.
//  10. — a partir de aquí SE ESCRIBE —
//     el texto pegado (si hay y no está repetido), el job, y el sobre.
//
// 🔴 LA LÍNEA DEL 10 ES LA QUE IMPORTA, Y ES LA RAZÓN DEL ORDEN. Los NUEVE escalones
// anteriores son puras lecturas: una petición que acaba en 400/403/404/422 no ha
// tocado una sola fila. Si la fuente se comprobara DESPUÉS de abrir el job —que es
// lo que sale solo si uno escribe esto en el orden en que se le ocurre—, cada 422
// dejaría un `intake_job` en `pending` que ningún worker puede completar: se
// reclamaría, moriría por falta de sobre, consumiría sus intentos y acabaría
// `failed`. Diez peticiones mal hechas del dueño serían diez jobs muertos en la
// cola, y ninguno diría por qué.
//
// 🔴 EL ÚNICO PUNTO EN QUE ESTE ORDEN SE APARTA DE LA LISTA DEL §8.1, dicho para que
// nadie lo lea como un descuido. El contrato pone TODO el 400 «antes de cualquier
// gate»; aquí el 400 está PARTIDO en dos y solo la primera mitad va delante:
//
//   - la mitad de VOCABULARIO (`via` fuera de `local|api`) es forma pura, no toca la
//     base, y va antes que nada — lo custodia TestReanalizar_LaFormaGanaAlGate;
//   - la mitad de COINCIDENCIA (`via` distinta de la efectiva) NO es forma: hay que
//     leer `tenant_llm` para saberlo. Va después del gate base a propósito, porque el
//     cuerpo de ese 400 publica `configured_via` —o sea, la configuración LLM del
//     tenant— y contestársela a quien no tiene `llm_intake` sería responder antes de
//     gatear. Es el mismo criterio con el que el contrato deja la FUENTE para el
//     final: no se le cuenta nada a quien ni siquiera tiene la capacidad.
//
// Lo custodia TestReanalizar_SinLLMIntake_ElGateGanaAlaViaQueNoCoincide.
//
// 🔴 Y LOS TRES ÚLTIMOS PASOS NO SON TRANSACCIONALES, a propósito y con su coste
// dicho: escribir el texto pegado, abrir el job y componer el sobre son tres
// sentencias en tres tablas distintas. Envolverlas en una transacción exigiría que
// este paquete manejara el `*sql.DB` de los tres stores, que es exactamente el
// acoplamiento que los puertos estrechos evitan. Lo que se paga es esto, y es
// benigno en las dos direcciones: si falla la apertura del job, la fila del texto
// pegado queda escrita y SIRVE —es material del hilo, y el siguiente re-análisis la
// leerá—; si falla la composición, el job se queda sin sobre y el worker lo mata con
// su causa escrita, que es un fallo VISIBLE. Lo que NO puede pasar es lo contrario:
// que se abra un job para un evento sin material.
func (s *Servicio) Reanalizar(ctx context.Context, req Solicitud) (Resultado, error) {
	if s == nil {
		return Resultado{}, ErrSinCablear
	}

	pegado, err := s.validarForma(req)
	if err != nil {
		return Resultado{}, err
	}
	via, err := s.autorizar(ctx, req)
	if err != nil {
		return Resultado{}, err
	}
	objetivo, err := s.objetivoDe(ctx, req)
	if err != nil {
		return Resultado{}, err
	}
	origen, err := s.origenDelMaterial(ctx, objetivo.EventID, pegado)
	if err != nil {
		return Resultado{}, err
	}

	// ── A PARTIR DE AQUÍ SE ESCRIBE ──────────────────────────────────────────
	if pegado != "" {
		if err := s.persistirPegado(ctx, objetivo.EventID, pegado); err != nil {
			return Resultado{}, err
		}
	}

	key := intake.WindowKey{
		TenantID:  req.TenantID,
		SessionID: objetivo.SessionID,
		ContactID: objetivo.ContactID,
		EventID:   objetivo.EventID,
	}
	jobID, err := s.jobs.AbrirReanalisis(ctx, intake.SolicitudReanalisis{
		Key:      key,
		IntakeID: req.IntakeID,
		Contexto: intake.Reanalisis{
			RequestedBy: intake.RequestedByOwner,
			Via:         via,
			Source:      origen,
			From:        objetivo.LastRevisionNo,
		},
	})
	if err != nil {
		return Resultado{}, err
	}

	// EL SOBRE. `ComposeAtFlush` rellena EXACTAMENTE el job que se acaba de abrir:
	// `PutSourceText` elige la fila `pending` más recientemente tocada de esta tupla
	// cuyo sobre esté vacío, y esa es la de arriba (ver intake/reanalisis.go).
	//
	// Un fallo aquí NO tumba la petición y NO se traga: se avisa con todo lo que hace
	// falta para encontrar el job, y el worker lo matará con su causa escrita cuando
	// lo reclame sin literal. Devolver error aquí sería peor: el job YA existe, así
	// que un 500 le diría al dueño que no pasó nada mientras la cola tiene trabajo.
	if cerr := s.compositor.ComposeAtFlush(ctx, key); cerr != nil {
		s.log.Error("reanalisis: el job quedó abierto pero SIN literal; el worker lo matará al reclamarlo",
			"tenant_id", req.TenantID, "intake_id", req.IntakeID, "event_id", objetivo.EventID,
			"job_id", jobID, "error", cerr.Error())
	}

	// EL LOG LLEVA IDENTIFICADORES Y NÚMEROS, NUNCA CONTENIDO (REQ-10c): ni el texto
	// pegado, ni el literal del hilo, ni un trozo de ninguno de los dos. `origen` es
	// vocabulario cerrado y `runas` es un tamaño.
	s.log.Info("reanalisis: job abierto a petición del dueño",
		"tenant_id", req.TenantID, "intake_id", req.IntakeID, "event_id", objetivo.EventID,
		"job_id", jobID, "via", via, "source", origen,
		"reanalyzed_from", objetivo.LastRevisionNo, "status_intake", objetivo.Status,
		"runas_pegadas", len([]rune(pegado)))

	return Resultado{
		IntakeID:   req.IntakeID,
		RevisionNo: objetivo.LastRevisionNo + 1,
		JobID:      jobID,
		Via:        via,
		Status:     EstadoEnCurso,
	}, nil
}

// validarForma es el ESCALÓN 1-2: lo que se puede rechazar sin tocar la base.
//
// Va antes que cualquier gate porque el §8.1 lo dice con todas las letras para el
// `invalid_via` («Validación de forma, antes de cualquier gate») y porque el saneo
// del texto tiene que ocurrir ANTES de todo lo demás: el dedupe de T4.6 compara el
// hash del texto YA SANEADO, y sanearlo dos veces sería tener la regla en dos sitios.
func (s *Servicio) validarForma(req Solicitud) (string, error) {
	if req.Via != "" && !tenantllm.ValidVia(req.Via) {
		return "", ViaInvalidaError{Via: req.Via}
	}
	return sanear(req.Text)
}

// autorizar es el ESCALÓN 3-6: el nivel, la vía y lo que cada vía exige. Devuelve la
// vía EFECTIVA con la que se va a abrir el job.
func (s *Servicio) autorizar(ctx context.Context, req Solicitud) (string, error) {
	if err := s.exigir(ctx, req.TenantID, entitlements.FeatureLLMIntake); err != nil {
		return "", err
	}
	via, cfg, err := s.resolverVia(ctx, req)
	if err != nil {
		return "", err
	}
	if via != tenantllm.ViaAPI {
		return via, nil
	}
	// 🔴 `api_llm` SOLO APARECE DESPUÉS DE ESTE `return`, y es un INVARIANTE del
	// ADR-0044 / D-044.28: esa clave gatea LA VÍA, no la capacidad. Un tenant con
	// `llm_intake` y sin `api_llm` es un tenant VÁLIDO en vía local y su re-análisis
	// tiene que funcionar entero. Preguntar por ella «por si acaso» más arriba es el
	// defecto que vigilan internal/flujos/runtime/via_local_sin_api_llm_test.go,
	// internal/publicapi/tenantllm_gate_via_test.go y, para esta puerta,
	// TestReanalizar_LaViaLocalNoPreguntaPorAPILLM.
	if err := s.exigir(ctx, req.TenantID, entitlements.FeatureAPILLM); err != nil {
		return "", err
	}
	if !credencialCompleta(cfg) {
		return "", CredencialAusenteError{Via: via}
	}
	return via, nil
}

// objetivoDe es el ESCALÓN 7-8: la solicitud existe y es de este tenant, cuelga de un
// evento, y no hay ya un job vivo sobre él.
//
// 🔴 LA GUARDA DEL JOB VIVO ES TAMBIÉN LA DE LA CARRERA. `aggregating` es un estado NO
// terminal, así que un re-análisis pedido en mitad de una ráfaga del cliente encuentra
// aquí la ventana abierta y sale por el 422 — que es la respuesta correcta: el material
// todavía se está escribiendo.
func (s *Servicio) objetivoDe(ctx context.Context, req Solicitud) (intakes.ReanalysisTarget, error) {
	objetivo, err := s.solicitudes.ReanalysisTargetOf(ctx, req.TenantID, req.IntakeID)
	if err != nil {
		return intakes.ReanalysisTarget{}, err
	}
	if objetivo.EventID == "" {
		// Solicitud LEGADA pre-0054: no cuelga de ningún evento, así que no hay hilo
		// del que re-analizar. Es literalmente «no hay original guardado», o sea la
		// razón `never_stored` — y NO un 404: la solicitud existe y el dueño la está
		// mirando en la bandeja.
		return intakes.ReanalysisTarget{}, FuenteAusenteError{Reason: RazonNuncaGuardada}
	}
	jobID, vivo, err := s.jobs.JobNoTerminalDeEvento(ctx, req.TenantID, objetivo.EventID)
	if err != nil {
		return intakes.ReanalysisTarget{}, err
	}
	if vivo {
		return intakes.ReanalysisTarget{}, EnCursoError{JobID: jobID}
	}
	return objetivo, nil
}

// exigir es el gate en-código de una feature, con la MISMA política que el
// middleware HTTP (entitlements.RequireFeature): FAIL-CLOSED en los tres modos de
// no-resolución. Un resolver caído responde «no la tienes», no 500 — el llamante no
// debe poder distinguir «no lo tienes» de «no pude averiguarlo», porque un 5xx
// invita a reintentar hasta colarse.
func (s *Servicio) exigir(ctx context.Context, tenantID, feature string) error {
	has, err := s.features.Has(ctx, tenantID, feature)
	if err != nil || !has {
		return FeatureAusenteError{Feature: feature}
	}
	return nil
}

// resolverVia decide la vía EFECTIVA de este re-análisis y devuelve, de paso, la
// configuración del tenant (que la rama `api` necesita para la credencial).
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 `via` AFIRMA, NO CONMUTA (REQ-33, design §8.1; RATIFICADO POR JHOAN, D-044.51)
// ════════════════════════════════════════════════════════════════════════════
//
// El contrato lo dice con estas palabras: «El campo se conserva en el cuerpo para
// poder AFIRMAR la vía —y que el servidor rechace si no coincide—, NO para
// conmutarla». Era el único punto del §8.1 marcado «⏳ a ratificar en T4.6»; ya no
// lo está.
//
// LA REGLA ES UNA SOLA, y ahí está todo: **`via` tiene que coincidir con la vía
// EFECTIVA, siempre**. De ella salen los tres casos, y ninguno es una excepción:
//
//  1. `via` OMITIDA ⇒ la efectiva, sin más. Y SIN FILA en `tenant_llm` la efectiva
//     es `local` (D-044.48 §4): no es un default cómodo, es la vía que corre y está
//     probada, la que no necesita credencial, y hoy el estado de los TRES tenants de
//     UAT —esa tabla está vacía— y el de todo tenant nuevo.
//  2. `via` PRESENTE y fuera del vocabulario ⇒ 400, ya rechazado antes de llegar
//     aquí (es forma, no configuración).
//  3. `via` PRESENTE y distinta de la efectiva ⇒ 400 `invalid_via`. **También —y
//     sobre todo— cuando el tenant NO tiene fila.**
//
// 🔧 EL PUNTO 3 LLEVABA UN `hayFila &&` Y ERA UN DEFECTO, corregido el 2026-08-27.
// Desactivaba la regla EXACTAMENTE en el caso universal: sin fila se calculaba
// `configurada = local` y después no se usaba para comparar, así que `{"via":"api"}`
// no daba 400 — se convertía en la vía efectiva y caía al 422 de credencial. Un
// código distinto, más abajo, para el único estado que existe hoy en campo. La causa
// de raíz es que «sin fila» se estaba tratando como AUSENCIA de vía cuando D-044.48
// §4 la define como un valor REAL (`local`), y contra un valor real `"api"` es una
// contradicción como cualquier otra.
//
// ⚠️ CONSECUENCIA QUE HAY QUE DECIR EN VOZ ALTA, porque desfasa un criterio escrito
// de T4.6. Ese criterio pide «tenant CON feature y SIN fila en `tenant_llm` ⇒ 422,
// nunca 403»; con la regla ratificada ese caso responde **400**, antes de mirar
// ninguna feature. ⇒ El `422 llm_credentials_missing` queda como DEFENSA EN
// PROFUNDIDAD y no como camino alcanzable por un cliente bien formado: para llegar a
// él la vía efectiva tiene que ser `api`, y una fila `via='api'` la garantiza
// COMPLETA el CHECK `tenant_llm_via_api_completa_check` de la 0073 (proveedor,
// modelo, sobre entero y `consented_at`). Solo lo alcanza una fila que la base no
// puede producir: un restore parcial, un `UPDATE` a mano. Se escribe igual —el
// contrato lo publica y una fila corrupta no puede salir por un 500— y sus tests
// FABRICAN ese estado a sabiendas, diciéndolo.
//
// 🔴 Y EL DEFAULT NO PUEDE SER `api`, que es lo que decía el §8.1 antes de D-044.28:
// mandaría el texto de un cliente a un tercero de pago POR OMISIÓN. El default es la
// vía que no llama a nadie.
func (s *Servicio) resolverVia(ctx context.Context, req Solicitud) (string, tenantllm.Config, error) {
	cfg, hayFila, err := s.config.Get(ctx, req.TenantID)
	if err != nil {
		return "", tenantllm.Config{}, fmt.Errorf("reanalisis: leer la configuración LLM del tenant: %w", err)
	}

	// La vía EFECTIVA. `hayFila` se consume AQUÍ y no vuelve a salir de esta función:
	// a partir de esta línea «no hay fila» ya no es un estado aparte, es `local`. Esa
	// es la lección del defecto de arriba — mientras el booleano siga circulando,
	// alguien lo vuelve a meter en una guarda.
	efectiva := tenantllm.ViaLocal
	if hayFila && cfg.Via != "" {
		efectiva = cfg.Via
	}
	if req.Via != "" && req.Via != efectiva {
		return "", tenantllm.Config{}, ViaInvalidaError{Via: req.Via, Configurada: efectiva}
	}
	return efectiva, cfg, nil
}

// credencialCompleta dice si el tenant puede llamar al proveedor externo. Es PURA y
// vive aparte para poder probar los dos «no» sin montar nada: con fila pero sin
// clave, y con clave pero sin consentimiento.
//
// 🔴 EL CONSENTIMIENTO CUENTA IGUAL QUE LA CLAVE (ADR-0030 D-01/§4). Una fila con
// credencial y sin `consented_at` es un tenant que dejó la clave preparada y NO
// autorizó que el texto de sus clientes salga hacia un tercero. Mandarlo igual sería
// consentir en su nombre.
//
// 🔧 TENÍA UN TERCER «no» —`hayFila`— Y SE RETIRÓ CON EL ARREGLO DE resolverVia. Era
// una guarda sobre un camino MUERTO: solo se llega aquí con la vía efectiva en `api`,
// y la efectiva solo vale `api` si hay fila. Una guarda que no puede fallar sugiere
// que sin ella pasaría lo contrario, y encima repetía la forma (`hayFila && …`) del
// defecto que este fichero acaba de pagar.
func credencialCompleta(cfg tenantllm.Config) bool {
	return cfg.HasAPIKey && !cfg.ConsentedAt.IsZero()
}

// sanear pasa el texto pegado por LA MISMA puerta que el texto libre del cliente
// (`cart.SanitizeNote`, 041 REQ-33e / D-041.19) y traduce su rechazo a un 400.
//
// 🔴 ES LA MISMA FUNCIÓN Y NO UNA COPIA, y su propia cabecera lo exige: «el pipeline
// LLM del Plan 044 escribe esas MISMAS columnas por otra puerta. Ese pipeline debe
// llamar a ESTA función —está exportada por eso—, no copiar la regla». Con dos
// saneos, la columna tendría dos contratos y ninguno sería verdad.
//
// ⚠️ HEREDA EL TOPE DE 280 RUNAS, Y ESO APRIETA AQUÍ. `MaxNoteRunes` se calibró para
// una INDICACIÓN de cocina («sin cebolla»), y lo que entra por `/reanalyze` es una
// TRANSCRIPCIÓN. Se aplica igual —el enunciado de T4.6 lo pide con todas las letras y
// el número es de otra decisión (MD-041.3, que lo declara «no cerrado»)— y se rechaza
// en vez de truncar, por la misma razón que allí: recortar «…y sin maní» pierde el
// final, y el final es donde va el alérgeno. Quien vaya a subir el tope: es una
// decisión de producto y el sitio es cart/note.go, no este fichero.
//
// Un texto que sanea a VACÍO no es un error: equivale a no haber mandado `text`, y
// se trata igual (no se persiste nada y el origen sigue siendo el hilo).
func sanear(text string) (string, error) {
	if text == "" {
		return "", nil
	}
	limpio, err := cart.SanitizeNote(text)
	if err != nil {
		// Se envuelve para que el transporte pueda leer el NoteTooLongError con
		// errors.As y decirle al dueño cuántas runas sobran. El error de cart NO cita
		// el texto, solo su longitud.
		return "", fmt.Errorf("reanalisis: el texto pegado no pasa el saneo: %w", err)
	}
	return limpio, nil
}

// origenDelMaterial decide `analysis.source` Y, de paso, si hay algo que analizar.
// Son la MISMA pregunta contestada una vez: «¿qué material hay?». Partirla en dos
// funciones dejaría dos criterios sobre lo mismo, y el día que divergieran el
// endpoint aceptaría una petición que después no tiene qué componer.
//
// Devuelve FuenteAusenteError con su razón cuando no hay material por ningún lado.
func (s *Servicio) origenDelMaterial(ctx context.Context, eventID, pegado string) (string, error) {
	// El MISMO límite que el compositor, y por eso sale de su constante: la pregunta
	// «¿hay material?» tiene que mirar exactamente las entradas que se van a componer.
	entradas, err := s.hilo.ListThread(ctx, eventID, runtime.DefaultThreadLimit)
	if err != nil {
		return "", fmt.Errorf("reanalisis: leer el hilo del evento %s: %w", eventID, err)
	}
	mensajes, conTexto := contarMensajes(entradas)

	switch {
	case conTexto > 0 && pegado != "":
		return stages.OrigenAmbos, nil
	case conTexto > 0:
		return stages.OrigenHiloDelEvento, nil
	case pegado != "":
		// Sin hilo pero CON transcripción: hay material, y es solo el del dueño. Es
		// el único camino por el que `pasted_text` se escribe, y por eso no es un
		// valor decorativo del contrato §7.4.
		return stages.OrigenTextoPegado, nil
	case mensajes > 0:
		// Quedan filas `message` y ninguna conserva cuerpo ⇒ el literal EXISTIÓ y se
		// vació. Ver RazonPurgada para por qué esto no puede ocurrir todavía en campo.
		return "", FuenteAusenteError{Reason: RazonPurgada}
	default:
		return "", FuenteAusenteError{Reason: RazonNuncaGuardada}
	}
}

// contarMensajes reparte el hilo en los dos números que deciden la razón del 422.
// Es PURA para poder afirmar la frontera entre `purged` y `never_stored` sin base de
// datos — que es justo el par de casos que el criterio de T4.6 exige probar.
//
// 🔴 SOLO CUENTA `entry_kind='message'`. Los `summary` y los `message_out_of_turn`
// son CONTEXTO (REQ-10b, D-044.24) y un `source_text` hecho solo de contexto es
// exactamente el accidente que D-044.24 describe: productos que listamos NOSOTROS y
// ninguna frase del cliente que los contradiga. Es el mismo criterio con el que
// `Composed.Empty()` se mide por `Messages` y no por longitud del texto.
//
// `conTexto` cuenta las que conservan cuerpo. Una fila `message` con `Text` vacío es
// una fila cuyo `body_enc` está a NULL: eso es lo que dejaría la poda del hilo el día
// que exista, y lo que separa `purged` de `never_stored`.
func contarMensajes(entradas []events.ThreadEntry) (mensajes, conTexto int) {
	for _, e := range entradas {
		if e.Kind != events.KindMessage {
			continue
		}
		mensajes++
		if e.Text != "" {
			conTexto++
		}
	}
	return mensajes, conTexto
}

// persistirPegado guarda la transcripción del dueño como UNA fila más del hilo,
// salvo que ya esté (D-044.17, cierre de MD-044.2).
//
// ════════════════════════════════════════════════════════════════════════════
// EL DEDUPE SE HACE EN MEMORIA, Y NO ES UN ATAJO
// ════════════════════════════════════════════════════════════════════════════
//
// El criterio pide deduplicar por `(event_id, origin, hash del texto saneado)`, y ese
// hash NO CABE EN NINGUNA COLUMNA: el CHECK `conversation_event_messages_grade_chk`
// obliga a `payload IS NULL` en toda fila `message`, así que la única sede sería una
// columna nueva. Se descartó por una razón más dura que el coste de migrar: el cuerpo
// va cifrado con DEK fresca y nonce por fila, de modo que dos filas con el MISMO texto
// tienen `body_enc` distintos y no hay forma de compararlas en SQL. Así que se leen
// las filas `owner_pasted` de ese evento —que son unidades, no cientos: las veces que
// el dueño pegó texto en ESE pedido—, se descifran y se compara el hash en memoria.
//
// 🔴 EL HASH ES DEL TEXTO SANEADO EN LOS DOS LADOS. Lo que se guardó ya pasó por
// `cart.SanitizeNote`, y lo entrante también (ver `sanear`), así que dos pegadas que
// solo difieran en espacios repetidos o en un salto de línea SON la misma y no
// duplican. Comparar el crudo dejaría entrar el mismo texto con un espacio de más.
func (s *Servicio) persistirPegado(ctx context.Context, eventID, pegado string) error {
	previas, err := s.hilo.ListPastedByOwner(ctx, eventID)
	if err != nil {
		return fmt.Errorf("reanalisis: leer las transcripciones ya pegadas del evento %s: %w", eventID, err)
	}
	nueva := huella(pegado)
	for _, p := range previas {
		if huella(p) == nueva {
			// Ya está. NO es un error y NO cambia el desenlace de la petición: el
			// re-análisis sigue adelante y volverá a leer esa misma fila como parte del
			// origen, que es literalmente el criterio («el segundo re-análisis, sin
			// `text`, la vuelve a leer»).
			s.log.Debug("reanalisis: la transcripción pegada ya estaba en el hilo; no se duplica",
				"event_id", eventID, "runas", len([]rune(pegado)))
			return nil
		}
	}
	seq, err := s.hilo.AppendPastedMessage(ctx, eventID, pegado)
	if err != nil {
		return fmt.Errorf("reanalisis: guardar la transcripción pegada en el hilo del evento %s: %w", eventID, err)
	}
	s.log.Debug("reanalisis: transcripción del dueño añadida al hilo",
		"event_id", eventID, "seq", seq, "runas", len([]rune(pegado)))
	return nil
}

// huella es el hash del texto saneado con el que se deduplica. SHA-256 en hex.
//
// No se compara el texto tal cual —que con 280 runas sería igual de barato— por dos
// razones: el criterio de T4.6 pide el hash con esas palabras, y una huella no se
// puede leer por accidente en un log si algún día alguien la registra, mientras que
// el texto sí es contenido del cliente (ADR-0034).
func huella(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
