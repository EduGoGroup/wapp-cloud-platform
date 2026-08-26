package llmvia

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/llm/api"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/degradation"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/tenantllm"
)

// ============================================================================
// EL AVISO AL DUEÑO CUANDO LA VÍA FALLA (T1.6-6, D-044.32, REQ-38)
//
// # Por qué es un DECORADOR y no código dentro de cada adaptador
//
// Porque el aviso tiene que salir igual por las dos vías, y los dos adaptadores no
// son nuestros por igual: el de la vía API vive en wapp-shared/llm/api y no se toca
// desde aquí. Un decorador que envuelve al llm.LLMProvider que sea deja UN solo
// mecanismo para las dos —que es C2 aplicado a la degradación— y de paso mantiene
// los adaptadores limpios: el local no sabe que existe una tabla de avisos.
//
// El decorador tampoco pregunta por la vía: la recibe ATADA al construirse, desde el
// único switch que la mira (Selector.For). Por eso no rompe C2.
//
// # 🔴 LA REGLA QUE DEFINE ESTE FICHERO: LO QUE NO MAPEA, NO AVISA
//
// El vocabulario de motivos es CERRADO (ocho) y NO se ensancha para acomodar un
// fallo nuevo. Un error que no case con ninguno de los ocho se propaga al llamante y
// se queda en el log SIN escribir aviso. La alternativa —un motivo «otro»— parece
// inofensiva y es justo lo que mata el canal: el dueño abriría el aviso, leería «ha
// fallado algo» y a la segunda vez dejaría de abrirlos.
//
// Los motivos SANOS de alto volumen (umbral no alcanzado, atajo determinista,
// fastlane, «sin texto») no llegan siquiera hasta aquí: no son errores del proveedor,
// son el sistema funcionando, y el que los produce ni llama al adaptador. Que
// degradation.Notifier los rechace además con ErrMotivoDesconocido es la segunda red,
// no la primera.
// ============================================================================

// motivoDe traduce el error de una llamada al adaptador en un motivo de
// notificación. El segundo valor dice si hay algo que notificar.
//
// El orden de las ramas ES el contrato, y va de lo más específico a lo más general:
//
//  1. **La calidad NO avisa, y va primero.** llm.ErrLLMQuality significa que el
//     modelo RESPONDIÓ y su salida no era interpretable: el proveedor funciona, el
//     cable funciona, y el caller tiene un reintento a temperatura 0.3 previsto para
//     esto (REQ-02/REQ-03). Avisar al dueño de que «su LLM se degradó» cuando lo que
//     pasó es que el modelo escribió mal un JSON lo mandaría a reiniciar Ollama por
//     nada. Va la PRIMERA porque los providers envuelven este centinela dentro de
//     errores más gordos y una rama más ancha se lo tragaría.
//  2. **El motivo que trae el transporte**, por duck-typing (`Motivo() string`). Es
//     el camino de la vía local: *gatewaygrpc.InferError lo implementa y su
//     vocabulario coincide LITERALMENTE con degradation.Reason (hay un test en el
//     gateway que lo custodia). El .Valid() de aquí no es ceremonia: es lo que impide
//     que un motivo nuevo del transporte entre en la tabla sin pasar por el enum.
//  3. **Los centinelas de la vía API**, que no tienen motivo dentro y hay que
//     traducir.
//  4. **Todo lo demás: nada.** Ver la regla de arriba.
func motivoDe(err error) (degradation.Reason, bool) {
	if err == nil || errors.Is(err, llm.ErrLLMQuality) {
		return "", false
	}

	var conMotivo interface{ Motivo() string }
	if errors.As(err, &conMotivo) {
		if r := degradation.Reason(conMotivo.Motivo()); r.Valid() {
			return r, true
		}
		// Motivo que el transporte nombra y el enum no conoce: NO se inventa una fila.
		// Que llegue aquí significa que alguien amplió el vocabulario del transporte
		// sin ampliar el de la notificación, y el test de simetría del gateway debería
		// haberlo cazado antes.
		return "", false
	}

	switch {
	case errors.Is(err, tenantllm.ErrNotConfigured):
		// El tenant está en vía API y su credencial no existe: ni fila, ni sobre.
		return degradation.ReasonCredencial, true
	case errors.Is(err, api.ErrUnsupportedProvider):
		// Config imposible (un `provider` fuera del CHECK de la 0073). NO es una
		// degradación: nada se ha caído, la fila está mal escrita. Va ANTES de
		// ErrInvalidConfig porque lo envuelve, y sin este orden se contaría como
		// credencial — mandando al dueño a rotar una clave que está perfecta.
		return "", false
	case errors.Is(err, api.ErrInvalidConfig):
		// En la práctica solo puede ser la credencial: el CHECK
		// `tenant_llm_via_api_completa_check` garantiza que provider y model están
		// cuando via='api', así que lo único que api.New puede echar en falta es la
		// clave.
		return degradation.ReasonCredencial, true
	case errors.Is(err, api.ErrUpstream):
		return degradation.ReasonAPIError, true
	default:
		return "", false
	}
}

// notifying envuelve un provider para que cada fallo suyo escriba el aviso de
// degradación del par (motivo, vía). Sin notificador cableado devuelve el provider
// TAL CUAL, sin envoltura: una envoltura que no hace nada solo añade un marco en los
// stack traces.
func (s *Selector) notifying(p llm.LLMProvider, tenantID, via string) llm.LLMProvider {
	if s.notifier == nil {
		return p
	}
	return &avisador{inner: p, sel: s, tenantID: tenantID, via: via}
}

// avisador es el decorador. Los cinco métodos son la misma línea: llamar, y si falló,
// avisar antes de propagar. NO altera el error ni lo envuelve — quien lo reciba tiene
// que poder seguir usando errors.Is y el duck-typing del motivo.
type avisador struct {
	inner    llm.LLMProvider
	sel      *Selector
	tenantID string
	via      string
}

func (a *avisador) ClassifyRequest(ctx context.Context, in llm.ClassifyRequestInput, opts llm.Options) (json.RawMessage, error) {
	out, err := a.inner.ClassifyRequest(ctx, in, opts)
	a.sel.avisar(ctx, a.tenantID, a.via, OrigenPipeline, err)
	return out, err
}

func (a *avisador) ExtractMainIdeas(ctx context.Context, in llm.ExtractMainIdeasInput, opts llm.Options) (json.RawMessage, error) {
	out, err := a.inner.ExtractMainIdeas(ctx, in, opts)
	a.sel.avisar(ctx, a.tenantID, a.via, OrigenPipeline, err)
	return out, err
}

func (a *avisador) ExtractItemSpecs(ctx context.Context, in llm.ExtractItemSpecsInput, opts llm.Options) (json.RawMessage, error) {
	out, err := a.inner.ExtractItemSpecs(ctx, in, opts)
	a.sel.avisar(ctx, a.tenantID, a.via, OrigenPipeline, err)
	return out, err
}

func (a *avisador) NormalizeQuantities(ctx context.Context, in llm.NormalizeQuantitiesInput, opts llm.Options) (json.RawMessage, error) {
	out, err := a.inner.NormalizeQuantities(ctx, in, opts)
	a.sel.avisar(ctx, a.tenantID, a.via, OrigenPipeline, err)
	return out, err
}

func (a *avisador) GenerateQuoteText(ctx context.Context, in llm.GenerateQuoteTextInput, opts llm.Options) (json.RawMessage, error) {
	out, err := a.inner.GenerateQuoteText(ctx, in, opts)
	a.sel.avisar(ctx, a.tenantID, a.via, OrigenPipeline, err)
	return out, err
}

// avisar escribe el aviso si el error tiene motivo. No devuelve nada, y esa firma es
// la decisión: el aviso NO PUEDE tumbar nada. El fallo de la inferencia ya ocurrió y
// ya se va a propagar; que además no se pueda anotar es un segundo problema, no un
// motivo para cambiar lo que el llamante recibe. Es el mismo criterio que
// intakes.Notifier.NotifyStatus, y por la misma razón.
//
// 🔴 EL CONTEXTO SE DESACOPLA (context.WithoutCancel) Y NO ES COSMÉTICO. Uno de los
// fallos que más falta hace anotar —el llamante se rindió, la ventana se cerró, el
// proceso se apaga— llega aquí con el ctx YA CANCELADO. Sin desacoplarlo, el aviso
// fallaría exactamente en los casos en los que hay algo que contar, y el canal se
// quedaría mudo justo cuando importa. El presupuesto propio (avisoTimeout) es lo que
// impide que ese desacople se convierta en una espera sin techo.
func (s *Selector) avisar(ctx context.Context, tenantID, via, origen string, err error) {
	if err == nil {
		return
	}
	reason, ok := motivoDe(err)
	if !ok {
		return
	}
	// ════════════════════════════════════════════════════════════════════════
	// LA MÉTRICA VA ANTES QUE LA TABLA, Y VA AUNQUE NO HAYA TABLA (T3.5-2)
	// ════════════════════════════════════════════════════════════════════════
	//
	// Este punto es EL sitio: es el único del repo que tiene a la vez el motivo ya
	// traducido al vocabulario cerrado, la vía y —desde esta tarea— qué entrada del
	// selector se estaba sirviendo. Contarlo en cada llamante sería el mismo
	// razonamiento repetido N veces, y el N+1 se olvidaría.
	//
	// 🔴 NO ESTÁ DEBAJO DEL `if s.notifier == nil`, y esa colocación es la decisión:
	// el notificador es la tabla de avisos AL DUEÑO —una fila deduplicada por
	// ventana, pensada para que una persona la lea— y la métrica es el conteo para
	// NOSOTROS. Colgar el contador del notificador ataría el dato de campo que
	// desbloquea D-044.41 a que haya base de datos cableada, y encima lo dejaría
	// subcontado por el dedupe: diez timeouts de la misma ventana escriben UN aviso
	// y son DIEZ caídas a Nivel A. Son dos preguntas distintas y se responden por
	// separado.
	s.contarDegradacion(origen, via, reason)
	if s.notifier == nil {
		return
	}
	at := s.ahoraFn()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), avisoTimeout)
	defer cancel()

	creado, recErr := s.notifier.Record(ctx, tenantID, reason, via, at)
	if recErr != nil {
		if s.log != nil {
			// El motivo y la vía SÍ van al log (son vocabulario cerrado, cero PII); el
			// error original NO se repite aquí —ya lo va a ver el llamante— para no
			// duplicar en el log lo que puede llevar reflejado texto del proveedor.
			s.log.Error("degradación: no se pudo escribir el aviso al dueño",
				"tenant_id", tenantID, "reason", reason.String(), "via", via, "error", recErr)
		}
		return
	}
	if creado && s.log != nil {
		// Solo cuando NACE. Un aviso que se colapsa sobre otro de la misma ventana es
		// el dedupe funcionando, y loguearlo cada vez reintroduciría por el log el
		// ruido que la tabla evita.
		s.log.Warn("degradación: la vía LLM del tenant falló y se avisó al dueño",
			"tenant_id", tenantID, "reason", reason.String(), "via", via)
	}
}

// ============================================================================
// EL CONTEO DE LAS CAÍDAS A NIVEL A (T3.5-2, D-044.41)
// ============================================================================

// Origen* es el vocabulario CERRADO de «qué entrada del selector se estaba
// sirviendo cuando la vía falló». Son tres y son de este paquete porque es este
// paquete el que tiene las tres puertas; quien las cuenta (Prometheus) no las
// conoce y no debe inventárselas.
//
// 🔴 NO ES `class` DEL FRAME, y confundirlos daría un número equivocado. `class`
// (gatewaygrpc.ClaseInteractivo/ClaseLote) es un rótulo del CABLE que describe la
// naturaleza de UNA petición, y por él P1 —que es interactiva— viaja marcada
// `interactivo` aunque entre por la misma puerta que P2–P5. Esto de aquí describe
// la PUERTA, no la petición, y es lo que hace legible la serie: `turno` es, por
// construcción, un turno de WhatsApp con alguien esperando delante.
const (
	// OrigenSeleccion: falló CONSTRUIR el adaptador (credencial que no se puede
	// descifrar, vía fuera del vocabulario). No se llegó a tocar el cable, así que
	// una serie que suba aquí NO habla del equipo del cliente sino de su
	// configuración.
	OrigenSeleccion = "seleccion"
	// OrigenPipeline: una de las cinco etapas P1–P5 del presupuesto, servidas por el
	// llm.LLMProvider que devuelve For.
	OrigenPipeline = "pipeline"
	// OrigenTurno: el TURNO ACOTADO del Nivel B (Selector.Turno). Es la serie que el
	// plan viene a producir: alguien escribió algo que el carrito no entendió, se le
	// preguntó al modelo y la vía falló ⇒ el turno cae al camino determinista de
	// siempre (Nivel A) con el reprompt de toda la vida.
	OrigenTurno = "turno"
)

// ObservadorDegradacion cuenta UNA caída a Nivel A. Es un CALLBACK y no una
// métrica, por la misma razón que el resto de los hooks de este repo: el selector
// no importa prometheus y no debería. Lo satisface (*metrics.Metrics).LLMDegradacion.
//
// 🔴 LOS TRES ARGUMENTOS SON DE CARDINALIDAD ACOTADA POR CONSTRUCCIÓN: origen sale
// de las constantes de arriba, via del CHECK de tenant_llm y reason del enum
// cerrado de degradation. Ni el tenant, ni el Edge, ni la sesión, ni una línea de
// texto del cliente salen por aquí: esto acaba en una etiqueta de Prometheus.
type ObservadorDegradacion func(origen, via, reason string)

// WithDegradacionObservada inyecta el contador de caídas a Nivel A. Opcional: sin
// él todo funciona igual y el dato simplemente no se publica — que es exactamente
// lo que pasaba antes de esta tarea, y por lo que D-044.41 lleva desde la Ola 2 sin
// poder decidirse.
func WithDegradacionObservada(fn ObservadorDegradacion) SelectorOption {
	return func(s *Selector) {
		if fn != nil {
			s.degradaciones = fn
		}
	}
}

// contarDegradacion avisa al contador si lo hay. Un observador que entre en pánico
// se lleva la llamada por delante: mismo trato que los demás hooks del repo.
func (s *Selector) contarDegradacion(origen, via string, reason degradation.Reason) {
	if s.degradaciones == nil {
		return
	}
	s.degradaciones(origen, via, reason.String())
}
