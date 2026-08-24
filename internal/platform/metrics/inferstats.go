package metrics

import (
	"errors"
	"fmt"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/inferstats"
)

// --- Telemetría de inferencia del Edge (Plan 044 · Ola 1.7 · T1.7-9) ---------

// FuenteInferencia entrega el agregado de la flota EN EL MOMENTO DEL SCRAPE. Lo
// satisface (*inferstats.Store).Agrega.
type FuenteInferencia func() inferstats.Agregado

// RegisterInferenceStats publica la telemetría de inferencia del Edge sobre el
// registry propio. Devuelve error solo si el registry lo rechaza por algo distinto de
// «ya estaba», igual que RegisterDBStats.
//
// # POR QUÉ EL MOLDE DE PULL (RegisterDBStats) Y NO EL DE PUSH (flowlifecycle)
//
// El de push existe para DELTAS: el colector de flow_events calcula cuántas filas
// nuevas hubo en esa vuelta y hace `Add`. Aquí lo que llega en cada latido es el
// ACUMULADO DE LA VIDA del proceso del Edge, no un delta. Un `Add` por latido
// contaría cada inferencia una vez por latido —con un latido cada pocos segundos, un
// factor de cien— y la serie subiría sola sin que el Edge hiciera nada. La conducta
// correcta para un acumulado AJENO es publicar su valor actual, que es exactamente lo
// que hace un `CounterFunc`/`ConstMetric` leído en el scrape.
//
// Y es Collector propio en vez de CounterFunc suelto porque las series llevan
// ETIQUETA (`regimen`, `class`, `reason`) y el conjunto de valores lo decide el
// EMISOR: el Edge puede recalibrar sus umbrales o estrenar una cuarta clave sin tocar
// el contrato. Un CounterVec obligaría a enumerar aquí las claves posibles; un
// Collector emite las que hay.
//
// # 🔴 AUSENTE NO ES CERO
//
// Cuando una fase no trae muestras, NO se emite la serie. Emitir un 0 se leería como
// «cero inferencias midieron el prefill», que es una afirmación; la verdad es «este
// Edge no lo reporta». Es la misma regla que el Gateway ya aplica al traducir el
// contrato (nonZeroOrNil).
//
// Nil-safe como el resto del paquete.
func (m *Metrics) RegisterInferenceStats(src FuenteInferencia) error {
	if m == nil || src == nil {
		return nil
	}
	if err := m.reg.Register(&inferenceCollector{src: src}); err != nil {
		var already prometheus.AlreadyRegisteredError
		if errors.As(err, &already) {
			return nil // segunda llamada: idempotente, no tumba el arranque.
		}
		return fmt.Errorf("metrics: registrar la telemetría de inferencia del Edge: %w", err)
	}
	return nil
}

// Descriptores de las series. Se declaran una vez porque un Collector tiene que
// devolver los MISMOS punteros en Describe y en Collect.
//
// CARDINALIDAD, contada: `regimen` tiene tres valores (frio, templado, caliente) más
// los que el emisor añada; `class` tiene dos (interactivo, lote); `reason` son los
// motivos de omisión del cajero (ocho hoy). NO hay etiqueta por tenant, por edge ni
// por sesión — la regla dura del paquete (INV-5: cardinalidad y aislamiento), y aquí
// además tiene un coste que se paga a sabiendas: ver el ⚠️ de inferstats.Agrega.
var (
	descInferenciasPorRegimen = prometheus.NewDesc(
		"wapp_edge_inference_by_regime_total",
		"Inferencias servidas por los Edges, por RÉGIMEN DE PREFILL (frio|templado|caliente), acumuladas desde que arrancó cada Edge. Es la serie que responde «¿qué proporción de las inferencias de la última hora pagó arranque en frío?» sin abrir un log: rate(...{regimen=\"frio\"}) / rate(...). Las tres claves cubren la recta entera a propósito —si `templado` engorda, los umbrales del emisor piden recalibrarse—, y la clave vacía significa que el Edge no clasificó el régimen, no que fuera cero.",
		[]string{"regimen"}, nil)

	descInferenciasPorClase = prometheus.NewDesc(
		"wapp_edge_inference_by_class_total",
		"Inferencias servidas por los Edges, por CLASE declarada (interactivo|lote), acumuladas. 🔴 `class` es SOLO un rótulo y no decide nada en ninguna parte del sistema: sirve para saber de qué color pintar la serie, no para elegir a quién servir ni para mover el umbral del breaker. Un calentamiento cuenta aquí como `lote` y se distingue por su propio campo, no por esta etiqueta.",
		[]string{"class"}, nil)

	descIntentsOmitidos = prometheus.NewDesc(
		"wapp_edge_intent_omitted_total",
		"Intents que el cajero del Edge OMITIÓ, por motivo, acumulados. Subía en el latido desde el Plan 051 · T4.3 y hasta hoy moría en la base: es el desglose que dice si lo que se pierde es capacidad, plazo o salud del proveedor. Los motivos NO se enumeran aquí: llegan del emisor y se publican los que haya.",
		[]string{"reason"}, nil)

	descMuestrasLatencia = prometheus.NewDesc(
		"wapp_edge_inference_samples_total",
		"Observaciones de latencia que sostienen el cuantil de cada fase (prefill|generacion), acumuladas. 🔴 SE PUBLICA EL `n` Y NO EL CUANTIL, y no es un olvido: un p50 NO SE AGREGA entre Edges —sumarlo, promediarlo o tomar su máximo da un número con buena pinta y sin significado— y darle un `edge_id` para poder agregarlo en PromQL rompería la regla de no etiquetar por tenant. El cuantil por sesión sigue estando en GET /api/v1/sessions, que es donde tiene sentido. ⚠️ Y el que llega es MONÓTONO Y SIN RESET (mezcla toda la vida del proceso del Edge), así que tampoco serviría para comparar dos periodos. Ausente = el Edge no lo mide: la serie NO se emite en vez de valer 0.",
		[]string{"fase"}, nil)

	descEdgesReportando = prometheus.NewDesc(
		"wapp_edge_inference_reporting_edges",
		"Cuántos Edges sostienen las sumas de arriba. Se publica por la misma razón por la que un cuantil viaja con su `n`: una suma de flota sin saber sobre cuántos se hizo dice poco y parece decir mucho. Además es el número que avisa de cuándo hay que revisar la agregación: con muchos Edges, el reinicio del daemon de uno hace bajar la suma y Prometheus pierde el tramo de los demás (ver inferstats.Agrega). Nunca decrece por desconexión —un Edge que se va deja su última foto puesta— así que no es «Edges vivos», es «Edges conocidos».",
		nil, nil)
)

// inferenceCollector lee la fuente EN EL SCRAPE y emite las series que existan.
//
// ⚠️ Collect corre concurrentemente con el resto del registry (`Gather` colecta EN
// PARALELO) y sin ctx propio: por eso la fuente es una lectura de MEMORIA y no una
// consulta a la base. Colgar una consulta de aquí acoplaría el /metrics a la salud de
// PostgreSQL — y dejaría sin servir, justo cuando la base va mal, las series del pool
// que existen para diagnosticarlo.
type inferenceCollector struct{ src FuenteInferencia }

func (c *inferenceCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- descInferenciasPorRegimen
	ch <- descInferenciasPorClase
	ch <- descIntentsOmitidos
	ch <- descMuestrasLatencia
	ch <- descEdgesReportando
}

func (c *inferenceCollector) Collect(ch chan<- prometheus.Metric) {
	ag := c.src()
	emitirPorClave(ch, descInferenciasPorRegimen, ag.PorRegimen)
	emitirPorClave(ch, descInferenciasPorClase, ag.PorClase)
	emitirPorClave(ch, descIntentsOmitidos, ag.OmitidasPorMotivo)
	// Los `samples` SON acumulados (contador), y el número de Edges es un nivel que
	// sube y baja (gauge). Van con tipos distintos a propósito: mezclarlos —dividir
	// un contador por un gauge, por ejemplo— da un número absurdo con buena pinta.
	emitirMuestras(ch, "prefill", ag.MuestrasPrefill)
	emitirMuestras(ch, "generacion", ag.MuestrasGeneracion)
	ch <- prometheus.MustNewConstMetric(descEdgesReportando, prometheus.GaugeValue, float64(ag.Edges))
}

// emitirPorClave publica una serie por clave presente. Un mapa vacío no emite nada, y
// eso es lo correcto: «todavía no ha pasado» no es «pasó cero veces».
func emitirPorClave(ch chan<- prometheus.Metric, desc *prometheus.Desc, m map[string]int64) {
	for clave, n := range m {
		ch <- prometheus.MustNewConstMetric(desc, prometheus.CounterValue, float64(n), clave)
	}
}

// emitirMuestras publica el `n` de una fase SOLO si alguien lo mide. Ver el 🔴 de
// descMuestrasLatencia para por qué el nil no se convierte en 0.
func emitirMuestras(ch chan<- prometheus.Metric, fase string, n *int64) {
	if n == nil {
		return
	}
	ch <- prometheus.MustNewConstMetric(descMuestrasLatencia, prometheus.CounterValue, float64(*n), fase)
}
