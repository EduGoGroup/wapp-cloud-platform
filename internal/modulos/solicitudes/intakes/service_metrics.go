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
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
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
	panic(pendiente.Implementar("intakes.WithMetrics"))
}

// WithMetricsClock sustituye el reloj con el que se mide `elapsed_from_draft_ms`
// (time.Now por defecto). Existe para los tests: ese campo es la resta de dos
// instantes y con el reloj de verdad no se puede afirmar un número —solo un rango—,
// que es justo la clase de aserción que deja pasar un cero.
//
// Pasar nil no hace nada: el servicio no se queda sin reloj. NO es el reloj de
// Summary (ese es el de WithClock) y no lo mueve.
func WithMetricsClock(now func() time.Time) Option {
	panic(pendiente.Implementar("intakes.WithMetricsClock"))
}
