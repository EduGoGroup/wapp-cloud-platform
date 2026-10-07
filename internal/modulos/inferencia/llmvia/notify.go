// Porta internal/llmvia/notify.go @ ebf4eb7

package llmvia

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/llm/api"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/degradation"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm"
)

// ============================================================================
// EL AVISO AL DUEÑO CUANDO LA VÍA FALLA (T1.6-6, D-044.32, REQ-38, ADR-0044 §5)
//
// Este fichero es el contrato de lo que pasa cuando una entrada del Selector falla.
// Sus exportados son pocos (el vocabulario de orígenes y el contador); la conducta
// la ejercen For y Turno, y se promete aquí porque es de aquí.
//
// # Por qué es un DECORADOR y no código dentro de cada adaptador
//
// Porque el aviso tiene que salir igual por las dos vías, y los dos adaptadores no
// son nuestros por igual: el de la vía API vive en wapp-shared/llm/api y no se toca
// desde aquí. Un decorador que envuelve al llm.LLMProvider que sea deja UN solo
// mecanismo para las dos —que es C2 aplicado a la degradación— y de paso mantiene
// los adaptadores limpios: el local no sabe que existe una tabla de avisos.
//
// El decorador tampoco pregunta por la vía: la recibe ATADA al construirse, desde la
// selección. 🔴 Por eso este fichero NO compara por vía, y el candado C2 lo vigila.
//
// El decorador envuelve los CINCO métodos de llm.LLMProvider con la misma línea:
// llamar y, si falló, avisar (origen OrigenPipeline) antes de propagar. NO altera el
// error ni lo envuelve —quien lo reciba tiene que poder seguir usando errors.Is y el
// duck-typing del motivo— y no toca la salida.
//
// # 🔴 LA REGLA QUE DEFINE ESTE FICHERO: LO QUE NO MAPEA, NO AVISA
//
// El vocabulario de motivos es CERRADO (los ocho de degradation.Reason) y NO se
// ensancha para acomodar un fallo nuevo. Un error que no case con ninguno se propaga
// al llamante SIN escribir aviso y SIN contarse. La alternativa —un motivo «otro»—
// parece inofensiva y es justo lo que mata el canal: el dueño abriría el aviso,
// leería «ha fallado algo» y a la segunda vez dejaría de abrirlos.
//
// # El mapeo error → motivo: EL ORDEN DE LAS RAMAS ES EL CONTRATO (R4.5.d, T-6)
//
// Va de lo más específico a lo más general:
//
//  1. **La calidad NO avisa, y va primero.** nil, o llm.ErrLLMQuality (también
//     envuelto): el modelo RESPONDIÓ y su salida no era interpretable. El proveedor
//     funciona, el cable funciona, y el llamante tiene un reintento a temperatura
//     0,3 previsto para esto. Avisar al dueño lo mandaría a reiniciar Ollama por un
//     JSON mal cerrado. Va la PRIMERA porque los providers envuelven este centinela
//     dentro de errores más gordos y una rama más ancha se lo tragaría.
//  2. **El motivo que trae el transporte**, por duck-typing: un error (también
//     envuelto) con método `Motivo() string`. Es el camino de la vía local
//     (*edgegrpc.InferError). Si ese motivo es un degradation.Reason válido ⇒ avisa
//     con él. Si NO lo es (`se_rompio_algo`, el sano `fastlane`, la cadena vacía) ⇒
//     no avisa, Y NO SIGUE MIRANDO las ramas de abajo: no se inventa una fila.
//  3. **Los centinelas de la vía API**, que no traen motivo dentro:
//     tenantllm.ErrNotConfigured ⇒ `credencial`; api.ErrUnsupportedProvider ⇒ NO
//     avisa (la fila está mal escrita, nada se ha caído), y va ANTES de
//     api.ErrInvalidConfig porque este lo envuelve: sin ese orden se contaría como
//     credencial, mandando al dueño a rotar una clave que está perfecta;
//     api.ErrInvalidConfig ⇒ `credencial`; api.ErrUpstream ⇒ `api_error`.
//  4. **Todo lo demás: nada.**
//
// # Qué pasa cuando hay motivo (R4.5.e)
//
//  1. 🔴 PRIMERO SE CUENTA, Y SE CUENTA AUNQUE NO HAYA NOTIFICADOR (T-8): el
//     observador de WithDegradacionObservada recibe (origen, vía, motivo). El
//     notificador es la tabla de avisos AL DUEÑO —una fila deduplicada por ventana—
//     y el contador es el conteo para NOSOTROS: colgar uno del otro lo ataría a que
//     haya base de datos y lo dejaría subcontado por el dedupe (diez timeouts de la
//     misma ventana escriben UN aviso y son DIEZ caídas a Nivel A).
//  2. Si hay notificador (WithNotifier), UNA llamada a Notifier.Record con el tenant,
//     el motivo, la vía y el instante del reloj del Selector (WithClock).
//  3. 🔴 EL CONTEXTO SE DESACOPLA Y NO ES COSMÉTICO (T-7): Record recibe un ctx que
//     NO hereda la cancelación del llamante y que vence, como mucho, a los 3 s. Uno
//     de los fallos que más falta hace anotar —el llamante se rindió, la ventana se
//     cerró, el proceso se apaga— llega con el ctx YA CANCELADO; sin desacoplarlo, el
//     canal se quedaría mudo justo cuando importa. Los 3 s son lo que impide que ese
//     desacople se convierta en una espera sin techo.
//  4. EL AVISO NO PUEDE TUMBAR NADA: un fallo de Record solo va al log, con el
//     mensaje literal "degradación: no se pudo escribir el aviso al dueño" y las
//     claves tenant_id, reason, via y error (el del Record; el error ORIGINAL no se
//     repite ahí, puede llevar reflejado texto del proveedor). Lo que recibe el
//     llamante no cambia.
//  5. Si el aviso NACIÓ (creado), un Warn con el mensaje literal
//     "degradación: la vía LLM del tenant falló y se avisó al dueño" y las claves
//     tenant_id, reason y via. Uno que se colapsa sobre el de su ventana NO se
//     loguea: es el dedupe funcionando.
//
// ⚠️ Lo que se sigue de 1 y de For: SIN notificador el provider de For va sin
// envoltura, así que un fallo del PIPELINE no pasa por aquí y no se cuenta. Sin
// notificador se cuentan las otras dos puertas: la selección y el turno.
// ============================================================================

// Origen* es el vocabulario CERRADO de «qué entrada del selector se estaba
// sirviendo cuando la vía falló». Son tres y son de este paquete porque es este
// paquete el que tiene las tres puertas; quien las cuenta (Prometheus, etiqueta
// `origen` de wapp_llm_degradacion_total) no las conoce y no debe inventárselas.
// Los tres literales son observables y no cambian.
//
// 🔴 NO ES `class` DEL FRAME, y confundirlos daría un número equivocado. `class`
// (edgegrpc.ClassInteractive/ClassBatch) es un rótulo del CABLE que describe la
// naturaleza de UNA petición, y por él P1 —que es interactiva— viaja marcada
// `interactivo` aunque entre por la misma puerta que P2–P5. Esto de aquí describe
// la PUERTA, no la petición, y es lo que hace legible la serie: `turno` es, por
// construcción, un turno de WhatsApp con alguien esperando delante.
const (
	// OrigenSeleccion: falló CONSTRUIR el adaptador en For (credencial que no se
	// puede descifrar, configuración que el constructor rechaza). No se llegó a tocar
	// el cable, así que una serie que suba aquí NO habla del equipo del cliente sino
	// de su configuración.
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
// Recibe, por cada fallo CON motivo: el origen (una de las tres constantes Origen*),
// la vía ("local" | "api") y el motivo (el literal del degradation.Reason).
//
// 🔴 LOS TRES ARGUMENTOS SON DE CARDINALIDAD ACOTADA POR CONSTRUCCIÓN: origen sale
// de las constantes de arriba, via del CHECK de tenant_llm y reason del enum
// cerrado de degradation. Ni el tenant, ni el Edge, ni la sesión, ni una línea de
// texto del cliente salen por aquí: esto acaba en una etiqueta de Prometheus.
//
// Un observador que entre en pánico se lleva la llamada por delante: mismo trato
// que los demás hooks del repo.
type ObservadorDegradacion func(origen, via, reason string)

// WithDegradacionObservada inyecta el contador de caídas a Nivel A. Opcional: sin
// él todo funciona igual y el dato simplemente no se publica — que es exactamente
// lo que pasaba antes de T3.5-2, y por lo que D-044.41 llevaba desde la Ola 2 sin
// poder decidirse.
//
// fn nil SE IGNORA: no apaga un observador que ya estuviera puesto por una opción
// anterior.
func WithDegradacionObservada(fn ObservadorDegradacion) SelectorOption {
	return func(s *Selector) {
		if fn != nil {
			s.observer = fn
		}
	}
}

// noticeTimeout (en el paquete viejo, avisoTimeout) acota la escritura del aviso de
// degradación. Es un techo pequeño a propósito: el aviso corre en el camino de FALLO de
// una inferencia que ya se perdió, y hacer esperar más al llamante por una notificación
// sería pagar dos veces el mismo incidente.
const noticeTimeout = 3 * time.Second

// reasonOf (en el paquete viejo, motivoDe) traduce el error de una llamada al adaptador
// en un motivo de notificación. El segundo valor dice si hay algo que notificar.
//
// 🔴 El orden de las ramas ES el contrato (T-6), y es el de la cabecera de este fichero:
// de lo más específico a lo más general.
func reasonOf(err error) (degradation.Reason, bool) {
	// 1. La calidad NO avisa, y va PRIMERO: los providers envuelven este centinela dentro
	// de errores más gordos y una rama más ancha se lo tragaría.
	if err == nil || errors.Is(err, llm.ErrLLMQuality) {
		return "", false
	}

	// 2. El motivo que trae el transporte. El vocabulario de *edgegrpc.InferError coincide
	// LITERALMENTE con degradation.Reason (hay un test en el transporte que lo custodia).
	// El .Valid() de aquí no es ceremonia: es lo que impide que un motivo nuevo del
	// transporte entre en la tabla sin pasar por el enum.
	var withReason interface{ Motivo() string }
	if errors.As(err, &withReason) {
		if r := degradation.Reason(withReason.Motivo()); r.Valid() {
			return r, true
		}
		// Motivo que el transporte nombra y el enum no conoce: NO se inventa una fila.
		// Que llegue aquí significa que alguien amplió el vocabulario del transporte
		// sin ampliar el de la notificación, y el test de simetría del transporte debería
		// haberlo cazado antes.
		return "", false
	}

	// 3. Los centinelas de la vía API, que no tienen motivo dentro y hay que traducir.
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
		// 4. Todo lo demás: nada. Lo que no mapea, no avisa.
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
	return &notifyingProvider{inner: p, sel: s, tenantID: tenantID, via: via}
}

// notifyingProvider (en el paquete viejo, avisador) es el decorador. Los cinco métodos
// son la misma línea: llamar, y si falló, avisar antes de propagar. NO altera el error
// ni lo envuelve — quien lo reciba tiene que poder seguir usando errors.Is y el
// duck-typing del motivo.
type notifyingProvider struct {
	inner    llm.LLMProvider
	sel      *Selector
	tenantID string
	via      string
}

func (a *notifyingProvider) ClassifyRequest(ctx context.Context, in llm.ClassifyRequestInput, opts llm.Options) (json.RawMessage, error) {
	out, err := a.inner.ClassifyRequest(ctx, in, opts)
	a.sel.notify(ctx, a.tenantID, a.via, OrigenPipeline, err)
	return out, err
}

func (a *notifyingProvider) ExtractMainIdeas(ctx context.Context, in llm.ExtractMainIdeasInput, opts llm.Options) (json.RawMessage, error) {
	out, err := a.inner.ExtractMainIdeas(ctx, in, opts)
	a.sel.notify(ctx, a.tenantID, a.via, OrigenPipeline, err)
	return out, err
}

func (a *notifyingProvider) ExtractItemSpecs(ctx context.Context, in llm.ExtractItemSpecsInput, opts llm.Options) (json.RawMessage, error) {
	out, err := a.inner.ExtractItemSpecs(ctx, in, opts)
	a.sel.notify(ctx, a.tenantID, a.via, OrigenPipeline, err)
	return out, err
}

func (a *notifyingProvider) NormalizeQuantities(ctx context.Context, in llm.NormalizeQuantitiesInput, opts llm.Options) (json.RawMessage, error) {
	out, err := a.inner.NormalizeQuantities(ctx, in, opts)
	a.sel.notify(ctx, a.tenantID, a.via, OrigenPipeline, err)
	return out, err
}

func (a *notifyingProvider) GenerateQuoteText(ctx context.Context, in llm.GenerateQuoteTextInput, opts llm.Options) (json.RawMessage, error) {
	out, err := a.inner.GenerateQuoteText(ctx, in, opts)
	a.sel.notify(ctx, a.tenantID, a.via, OrigenPipeline, err)
	return out, err
}

// notify (en el paquete viejo, avisar) escribe el aviso si el error tiene motivo. No
// devuelve nada, y esa firma es la decisión: el aviso NO PUEDE tumbar nada. El fallo de
// la inferencia ya ocurrió y ya se va a propagar; que además no se pueda anotar es un
// segundo problema, no un motivo para cambiar lo que el llamante recibe.
//
// 🔴 EL CONTEXTO SE DESACOPLA (context.WithoutCancel) Y NO ES COSMÉTICO (T-7). Uno de
// los fallos que más falta hace anotar —el llamante se rindió, la ventana se cerró, el
// proceso se apaga— llega aquí con el ctx YA CANCELADO. Sin desacoplarlo, el aviso
// fallaría exactamente en los casos en los que hay algo que contar, y el canal se
// quedaría mudo justo cuando importa. El presupuesto propio (noticeTimeout) es lo que
// impide que ese desacople se convierta en una espera sin techo.
func (s *Selector) notify(ctx context.Context, tenantID, via, origin string, err error) {
	if err == nil {
		return
	}
	reason, ok := reasonOf(err)
	if !ok {
		return
	}
	// ════════════════════════════════════════════════════════════════════════
	// LA MÉTRICA VA ANTES QUE LA TABLA, Y VA AUNQUE NO HAYA TABLA (T3.5-2, T-8)
	// ════════════════════════════════════════════════════════════════════════
	//
	// Este punto es EL sitio: es el único del repo que tiene a la vez el motivo ya
	// traducido al vocabulario cerrado, la vía y qué entrada del selector se estaba
	// sirviendo. Contarlo en cada llamante sería el mismo razonamiento repetido N
	// veces, y el N+1 se olvidaría.
	//
	// 🔴 NO ESTÁ DEBAJO DEL `if s.notifier == nil`, y esa colocación es la decisión:
	// el notificador es la tabla de avisos AL DUEÑO —una fila deduplicada por
	// ventana, pensada para que una persona la lea— y la métrica es el conteo para
	// NOSOTROS. Colgar el contador del notificador ataría el dato de campo que
	// desbloquea D-044.41 a que haya base de datos cableada, y encima lo dejaría
	// subcontado por el dedupe: diez timeouts de la misma ventana escriben UN aviso
	// y son DIEZ caídas a Nivel A. Son dos preguntas distintas y se responden por
	// separado.
	s.countFall(origin, via, reason)
	if s.notifier == nil {
		return
	}
	at := s.clockNow()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), noticeTimeout)
	defer cancel()

	created, recErr := s.notifier.Record(ctx, tenantID, reason, via, at)
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
	if created && s.log != nil {
		// Solo cuando NACE. Un aviso que se colapsa sobre otro de la misma ventana es
		// el dedupe funcionando, y loguearlo cada vez reintroduciría por el log el
		// ruido que la tabla evita.
		s.log.Warn("degradación: la vía LLM del tenant falló y se avisó al dueño",
			"tenant_id", tenantID, "reason", reason.String(), "via", via)
	}
}

// countFall (en el paquete viejo, contarDegradacion) avisa al contador si lo hay. Un
// observador que entre en pánico se lleva la llamada por delante: mismo trato que los
// demás hooks del repo.
func (s *Selector) countFall(origin, via string, reason degradation.Reason) {
	if s.observer == nil {
		return
	}
	s.observer(origin, via, reason.String())
}
