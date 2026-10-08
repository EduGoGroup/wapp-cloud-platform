// Porta internal/intakes/metricas.go @ 64c181a (las opciones del Service)

// service_metrics.go — QUIÉN PUBLICA las métricas de la bandeja: las dos opciones
// del `Service` que el fichero viejo `metricas.go` llevaba junto a los nombres de
// los eventos y al puerto. Los nombres, el puerto y la forma de cada payload viven
// en `metricas.go`; aquí está el cableado y lo que se promete de él.
//
// BEST-EFFORT, Y ESO ES EL CONTRATO ENTERO. Un fallo del emisor se AVISA y la
// acción del dueño sigue: aprobar un presupuesto NO puede fallar porque una fila de
// telemetría no se escribiera.

package intakes

import (
	"context"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"
)

// WithMetrics cablea la telemetría de la bandeja. R-04: sin esta opción —o con un
// publicador nil— el servicio funciona igual y NO publica nada, que no es un error;
// ni siquiera calcula el KPI que no se iba a publicar (no recorre las revisiones y
// no deja ni una línea en el log).
//
// Con publicador, cada acción del dueño APLICADA publica exactamente UNA medición
// por MetricsPublisher.PublishMetric, con el tenant, el contacto OPACO de la
// solicitud (jamás otro dato) y el payload de `metricas.go`, sin una clave de más:
//
//   - Approve → EventApproved, `{"rev": <n>, "elapsed_from_draft_ms": <ms>}`. `rev`
//     es el número de la revisión `approved` que se acaba de escribir. El tiempo
//     corre desde el `created_at` de la revisión `interpreted` de número MÁS BAJO
//     —la primera, no la de un re-análisis, y buscada por número y no por posición;
//     las de `created_at` en cero no cuentan— hasta el reloj de WithMetricsClock, en
//     milisegundos enteros. Sin ninguna (la solicitud nació del carrito) vale 0 y
//     se registra en Debug, no en Warn: es el curso normal. Un resultado NEGATIVO no
//     se publica: se recorta a 0 y SÍ se avisa (Warn).
//   - RequestInfo → EventInfoRequested, `{"questions": 1}`.
//   - la corrección de líneas → EventLineCorrected (ver `metricas.go`).
//
// Una acción rechazada, o que falla antes de completarse, no publica nada.
//
// Un error del publicador NO se propaga: la acción devuelve su resultado normal y
// se registra un Warn con el `name` del evento y el `intake_id`, que es lo único con
// lo que se puede reconstruir a mano lo que faltó en un panel. Warn y no Error: lo
// que se pierde es UNA fila de telemetría.
//
// LLEVA EL LOG COMO SEGUNDO ARGUMENTO, y no es adorno: un colaborador best-effort
// sin dónde avisar es un fallo silencioso. Va en la MISMA opción —y no en una
// segunda que haya que recordar— para que no exista el estado «emisor cableado, log
// olvidado». Un `log` nil deja el logger por defecto que pone NewService; el log se
// sustituye aunque el publicador sea nil.
func WithMetrics(publisher MetricsPublisher, log logger.Logger) Option {
	return func(s *Service) {
		s.metrics = publisher
		if log != nil {
			s.log = log
		}
	}
}

// WithMetricsClock sustituye el reloj con el que se mide `elapsed_from_draft_ms`
// (time.Now por defecto). Existe para los tests: ese campo es la resta de dos
// instantes y con el reloj de verdad no se puede afirmar un número —solo un rango—,
// que es justo la clase de aserción que deja pasar un cero.
//
// Pasar nil no hace nada: el servicio no se queda sin reloj. NO es el reloj de
// Summary (ese es el de WithClock) y no lo mueve.
func WithMetricsClock(now func() time.Time) Option {
	return func(s *Service) {
		if now != nil {
			s.metricsNow = now
		}
	}
}

// publishMetric publica UNA medición. BEST-EFFORT: sin publicador cableado no hace
// nada, y un fallo suyo se avisa y no se propaga (ver la cabecera). Era
// `publicarMetrica` en el viejo.
//
// El `name` y el `payload` los pone cada llamante porque son su contrato; lo que este
// método garantiza es lo COMÚN —que el contacto que viaja es el OPACO de la solicitud
// y jamás otro dato— para que ninguna de las tres puertas pueda firmarla distinto.
func (s *Service) publishMetric(ctx context.Context, tenantID string, in Intake, name string, payload map[string]any) {
	if s.metrics == nil {
		return
	}
	if err := s.metrics.PublishMetric(ctx, tenantID, in.ContactID, name, payload); err != nil {
		// Warn y no Error: lo que se pierde es UNA fila de telemetría, y la acción del
		// dueño ocurrió entera. El intake_id va en el log porque es lo único con lo que
		// se puede reconstruir a mano lo que faltó en un panel.
		s.log.Warn("intakes: no se pudo publicar la métrica de la bandeja; la acción del dueño SÍ se aplicó",
			"name", name, "intake_id", in.ID, "error", err.Error())
	}
}

// publishCorrectionMetric publica `intake_line_corrected` con la forma de design
// §10: `{"lines_corrected": 2, "lines_total": 4}`. Era `métricaDeCorrección` en el
// viejo.
func (s *Service) publishCorrectionMetric(ctx context.Context, tenantID string, in Intake, before, after []Item) {
	corrected, total := correctionCount(before, after)
	s.publishMetric(ctx, tenantID, in, EventLineCorrected, map[string]any{
		"lines_corrected": corrected,
		"lines_total":     total,
	})
}

// publishApprovalMetric publica `intake_approved` con la forma de design §10:
// `{"rev": 3, "elapsed_from_draft_ms": 1900000}`. Era `métricaDeAprobación` en el
// viejo.
//
// 🔴 LA GUARDA SE REPITE AQUÍ, Y NO ES REDUNDANTE: Go evalúa los ARGUMENTOS antes de
// entrar en la función, así que el mapa —y con él el recorrido de las revisiones de
// `elapsedFromDraft`— se construía ENTERO aunque `publishMetric` fuera a salir por
// su propia guarda. Eso hacía dos cosas mal: un servicio sin telemetría cableada
// calculaba igual el KPI (contradiciendo lo que promete WithMetrics) y encima podía
// dejar rastro en el log de algo que nadie iba a publicar.
//
// Sus dos hermanas no la llevan porque sus payloads son literales y contadores ya
// calculados: ahí «evaluar el argumento» no cuesta nada. Esta es la única que lee.
func (s *Service) publishApprovalMetric(ctx context.Context, tenantID string, in Intake, revisionNo int, revisions []Revision) {
	if s.metrics == nil {
		return
	}
	s.publishMetric(ctx, tenantID, in, EventApproved, map[string]any{
		"rev":                   revisionNo,
		"elapsed_from_draft_ms": s.elapsedFromDraft(in, revisions).Milliseconds(),
	})
}

// publishInfoRequestMetric publica `intake_info_requested` con la forma de design
// §10: `{"questions": 1}`. Era `métricaDeInformación` en el viejo.
//
// EL 1 ES LITERAL Y ESO ES CORRECTO: `RequestInfo` manda UNA pregunta y solo una
// —«jamás sale sola» es su criterio, y una lista de preguntas no cabe en su firma—,
// así que el campo cuenta las preguntas de ESTE acto y hoy siempre vale uno. Se
// publica igualmente porque el KPI se calcula con `SUM(questions)` sobre las filas del
// periodo: contar filas y sumar el campo dan lo mismo hoy y seguirían dándolo el día
// que la puerta admita varias.
func (s *Service) publishInfoRequestMetric(ctx context.Context, tenantID string, in Intake) {
	s.publishMetric(ctx, tenantID, in, EventInfoRequested, map[string]any{
		"questions": 1,
	})
}

// elapsedFromDraft mide cuánto tardó el dueño en aprobar DESDE QUE TUVO EL BORRADOR
// DELANTE, que es el KPI que design §10 cuelga de este evento. Era `desdeElBorrador`
// en el viejo.
//
// LOS DOS EXTREMOS, DICHOS ENTEROS:
//
//	INICIO → el `created_at` de la PRIMERA revisión `interpreted`, que es el instante
//	         en que el pipeline dejó el borrador escrito. Lo pone la BD.
//	FIN    → `s.metricsNow()`, el reloj de Go del proceso que atiende el POST.
//
// Son DOS RELOJES y se dice en vez de esconderse: se restan igual porque la magnitud
// que se mide es de minutos u horas (el dueño mirando su bandeja) y la deriva entre
// dos relojes con NTP es ruido frente a eso. Lo que NO se hace con estos dos
// instantes es DECIDIR nada.
//
// 🔴 SIN REVISIÓN `interpreted` DEVUELVE 0, Y NO ES UNA ANOMALÍA. Una solicitud que NO
// nació del pipeline —el cierre de un carrito la crea directamente— no tiene borrador
// que cronometrar, y el único valor honesto es cero: inventar el `created_at` de la
// cabecera mediría «desde que existe el pedido», que es otra cosa con el mismo nombre.
// El runbook filtra por `> 0` justamente por esto.
//
// 🔧 ESE CASO SE REGISTRA EN DEBUG Y NO EN WARN, y la corrección importa: es el curso
// NORMAL de toda solicitud que viene del carrito, así que un Warn le pondría una línea
// de alarma a cada aprobación de un pedido perfectamente sano — ruido que enseña a
// ignorar el log. Warn se queda para lo que sí es raro: el negativo de abajo.
//
// Un resultado NEGATIVO tampoco se publica: se recorta a 0 y SÍ se avisa. Un tiempo
// negativo en un panel no se lee como «relojes desajustados», se lee como un bug.
func (s *Service) elapsedFromDraft(in Intake, revisions []Revision) time.Duration {
	start, found := draftInstant(revisions)
	if !found {
		s.log.Debug("intakes: la solicitud aprobada no nació del pipeline (no tiene revisión "+
			"interpretada), así que elapsed_from_draft_ms se publica como 0",
			"name", EventApproved, "intake_id", in.ID)
		return 0
	}
	d := s.metricsNow().Sub(start)
	if d < 0 {
		s.log.Warn("intakes: elapsed_from_draft_ms salió NEGATIVO; el reloj de la base y el del proceso están desalineados",
			"name", EventApproved, "intake_id", in.ID, "desfase_ms", d.Milliseconds())
		return 0
	}
	return d
}
