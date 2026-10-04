// Package inferstats guarda EN MEMORIA el último parte de inferencia que cada Edge
// reporta en su Heartbeat, y lo agrega para que el `/metrics` del Cloud lo publique
// (Plan 044 · Ola 1.7 · T1.7-9).
//
// # Qué resuelve
//
// La telemetría de inferencia del Edge SUBÍA Y MORÍA: el parte del cajero llega en
// `SessionHealth`, el Gateway lo durabiliza en `fleet` y la API REST lo sirve por
// sesión — pero nadie lo convertía en serie Prometheus, así que no se podía responder
// «¿qué proporción de las inferencias de la última hora pagó arranque en frío?» sin
// abrir un log. Este paquete es la mitad que faltaba, del lado de los datos.
//
// # 🔴 LA CLAVE ES EL EDGE, NO LA SESIÓN, Y ES LO PRIMERO QUE HAY QUE ENTENDER
//
// Los contadores del parte los lleva EL PROCESO del Edge —un cajero, un Ollama—, no
// una conversación de WhatsApp. Pero viajan en el latido de CADA sesión, así que un
// Edge con tres teléfonos manda TRES latidos con LOS MISMOS totales. Guardarlos por
// sesión y sumar multiplicaría por tres las inferencias del mundo, con una serie
// perfectamente creíble y falsa. Por eso el mapa se indexa por (tenant, edge) y el
// último parte de un Edge SUSTITUYE al anterior en vez de sumarse.
//
// Es la misma lección que el calentamiento de T1.7-4 («uno por EDGE, no por sesión»),
// y no es casualidad: las dos salen de que el Edge multiplexa N sesiones sobre un
// proceso (ADR-0008).
//
// # 🔴 AQUÍ NO SE RESTA NADA: LOS CONTADORES SON ACUMULADOS AJENOS
//
// Lo que llega es el TOTAL DE LA VIDA del proceso del Edge, no el delta desde el
// último latido. Este paquete NO calcula deltas y quien publica NO hace `Add`: se
// expone el acumulado tal cual y es Prometheus quien deriva la tasa. Acumular los
// totales que llegan en cada latido contaría cada inferencia una vez por latido.
//
// Corolario tranquilizador: que el carril COALESCE latidos (un latido intermedio se
// descarta, D-050.4) da exactamente igual aquí. El siguiente trae el mismo total o
// uno mayor, y no se pierde ninguna cuenta.
//
// # Lo que este paquete NO hace, a propósito
//
//   - **No caduca ni olvida Edges.** Un Edge que se desconecta deja su último parte
//     puesto. Es deliberado: si se borrara, la suma de la flota BAJARÍA al
//     desconectarse alguien, Prometheus lo leería como un reinicio de contador y
//     `rate()` perdería el tramo. Un Edge que se va deja su serie plana, que es la
//     lectura honesta —dejó de haber inferencias— y no un escalón hacia abajo.
//   - **No conoce Prometheus.** Devuelve números; quien los publica es
//     internal/platform/metrics, que es el único sitio del repo que importa el
//     cliente (mismo desacoplo que el colector de flow_events).
//   - **No conoce el contrato CloudLink.** Recibe un struct plano; la traducción desde
//     el proto la hace el Gateway, que es quien ya lo importa.
//
// # ⚠️ ALCANCE: ESTO NO DURABILIZA NADA, Y NO ES UN OLVIDO
//
// Los cuatro campos que la Ola 1.7 estrenó en `SessionHealth` (`inference_prefill`,
// `inference_generation`, `inference_by_regime`, `inference_by_class`) salen por
// `/metrics` y SOLO por ahí: NO se añadieron a `fleet.HealthSnapshot`, no se escriben
// en la base y no aparecen en `GET /api/v1/sessions`.
//
// Fue una decisión de alcance de T1.7-9, no un descuido: persistirlos pedía migración
// y bump de `SchemaVersion`, que es otra tanda con su propia coordinación de esquema.
// Se deja dicho porque el síntoma de no saberlo es concreto y desorienta — alguien
// busca el régimen de prefill en la API REST o en `fleet_sessions`, no lo encuentra, y
// concluye que el Edge no lo manda. Sí lo manda; lo que no hay es dónde guardarlo.
//
// Consecuencia práctica que conviene tener presente: al reiniciar el Cloud el almacén
// nace VACÍO, y las series no vuelven hasta el primer latido de cada Edge (segundos).
// No se pierde ninguna cuenta —lo que llega son acumulados del Edge, no deltas— pero un
// scrape hecho en esa ventana muestra la flota más pequeña de lo que es.
//
// # Nombres (E-11)
//
// Al portarlo, los exportados pasan a inglés: Parte → Report, Clave → Key, Agregado →
// Aggregate, Observa → Observe, Agrega → Aggregated; y los campos de Report, PorRegimen →
// ByRegime, PorClase → ByClass, OmitidasPorMotivo → SkippedByReason, MuestrasPrefill →
// PrefillSamples, MuestrasGeneracion → GenerationSamples. Los campos de Aggregate NO cambian:
// son los de inferencia.Agregado, que vive en platform.
//
// Porta internal/inferstats/inferstats.go @ 8896f13.
package inferstats

import (
	"sync"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/metrics/inferencia"
)

// Report (en el paquete viejo, Parte) es el bloque de inferencia de UN latido, ya traducido
// desde el contrato.
//
// Todos los contadores son ACUMULADOS del proceso del Edge. Los punteros distinguen
// «no medible» del cero, y esa distinción es el punto: el contrato transporta la
// ausencia con presencia nativa (sub-mensaje no puesto), y colapsarla a 0 se leería
// como «instantáneo» o «ninguna», que son afirmaciones distintas de «no lo sé».
type Report struct {
	// ByRegime (PorRegimen) cuenta las inferencias por régimen de prefill (`frio`,
	// `templado`, `caliente`). Mapa a propósito y no tres campos: el emisor puede
	// recalibrar los umbrales o añadir una cuarta clave sin tocar el contrato ni este
	// código, y quien lo lee debe tolerar claves que no conoce.
	ByRegime map[string]int64
	// ByClass (PorClase) cuenta las inferencias por `class` (`interactivo`, `lote`).
	// 🔴 SOLO RÓTULO: aquí tampoco decide nada, igual que en el frame.
	ByClass map[string]int64
	// SkippedByReason (OmitidasPorMotivo) es el desglose de intents omitidos del cajero
	// (Plan 051 · T4.3), que hasta hoy también subía y moría.
	SkippedByReason map[string]int64
	// PrefillSamples y GenerationSamples (MuestrasPrefill, MuestrasGeneracion) son el
	// `samples` de cada InferenceLatency: cuántas observaciones sostienen el cuantil de
	// esa fase. nil = el Edge no lo reporta (Edge viejo, o fase sin medir todavía).
	//
	// 🔴 SE PUBLICA EL `n` Y NO EL CUANTIL, y el porqué está en el registrador de
	// metrics: un p50 no se agrega entre Edges.
	PrefillSamples    *int64
	GenerationSamples *int64
}

// Key (en el paquete viejo, Clave) identifica al PROCESO que produjo los números: un
// Edge de un tenant. Ver la cabecera para por qué no es la sesión.
type Key struct {
	TenantID string
	EdgeID   string
}

// Store guarda el último parte de cada Edge. Es seguro para uso concurrente: escribe
// el carril de trabajo de cada stream y lee el scrape de /metrics, que corren en
// goroutines distintas (y `Gather` colecta EN PARALELO).
//
// El cero-valor NO es utilizable: usa New.
type Store struct {
	mu   sync.RWMutex
	last map[Key]Report
}

// New construye el almacén vacío: su Aggregated() tiene Edges == 0, los tres mapas vacíos
// (no nil) y las dos muestras nil.
func New() *Store { return &Store{last: make(map[Key]Report)} }

// Observe (en el paquete viejo, Observa) registra el parte de un Edge, SUSTITUYENDO al
// anterior de esa misma Key: lo que el parte nuevo no trae (una clave de mapa, una
// muestra) deja de estar.
//
// Sustituir y no acumular es la decisión, y va contra la intuición de «es un
// contador, súmalo»: lo que llega ya es el acumulado del Edge. Un `+=` aquí contaría
// cada inferencia una vez por cada latido que la mencione, que con un latido cada
// pocos segundos es un factor de cien.
//
// El parte se COPIA (los tres mapas y los dos punteros): el almacén no comparte respaldo
// con el llamante, y una mutación posterior de lo que este pasó no cambia lo guardado.
//
// Un parte con EdgeID vacío se IGNORA (no hay proceso al que atribuirlo); el TenantID
// vacío no se filtra.
//
// Nil-safe: sobre un *Store nil no hace nada, para que un arranque sin observabilidad
// no obligue a poner guardas en el camino del latido.
func (s *Store) Observe(k Key, r Report) {
	if s == nil || k.EdgeID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last[k] = Report{
		ByRegime:          cloneCounts(r.ByRegime),
		ByClass:           cloneCounts(r.ByClass),
		SkippedByReason:   cloneCounts(r.SkippedByReason),
		PrefillSamples:    cloneInt(r.PrefillSamples),
		GenerationSamples: cloneInt(r.GenerationSamples),
	}
}

// Aggregate (en el paquete viejo, Agregado) es la suma de la flota, lista para publicar.
//
// 🔴 Es un ALIAS de tipo de inferencia.Agregado, no un tipo propio: platform/metrics
// recibe (*Store).Aggregated como `func() inferencia.Agregado`, y con un tipo definido
// aquí dejaría de encajar. Por eso sus campos conservan el nombre que tienen en platform.
type Aggregate = inferencia.Agregado

// Aggregated (en el paquete viejo, Agrega) suma los partes de todos los Edges conocidos:
// los tres mapas se suman clave a clave, Edges es el número de Edges con parte, y cada
// muestra se suma conservando el «no medible»: nil + nil sigue siendo nil, y nil + n es
// n. Lo que NO puede pasar es que un Edge que no mide arrastre la suma a 0 y la
// convierta en «cero muestras», que es una afirmación distinta.
//
// Los tres mapas del resultado NUNCA son nil y son propios del llamante (igual que los
// punteros de las muestras): mutarlos no toca el almacén. Sobre un *Store nil devuelve
// ese mismo agregado vacío y utilizable.
//
// El almacén NO olvida: un Edge que deja de reportar conserva su último parte y sigue
// en la suma (ver la cabecera del paquete).
//
// ⚠️ SUMAR CONTADORES MONÓTONOS DE VARIOS PROCESOS TIENE UN LÍMITE CONOCIDO, y se
// escribe aquí porque no se ve leyendo el código: si el daemon de UN Edge reinicia,
// sus contadores vuelven a cero y la SUMA baja. Prometheus lo lee como un reinicio de
// contador —correcto— pero al hacerlo pierde el incremento que los DEMÁS Edges
// tuvieran en ese mismo intervalo de scrape.
//
// Se acepta a sabiendas y con su condición: con la flota de hoy (unidades) el efecto
// es despreciable y la alternativa —una serie por Edge— exigiría etiquetar por
// `edge_id`, que es 1:1 con una instalación de un tenant y rompe la regla dura del
// paquete de métricas (nada de etiquetas por tenant: cardinalidad y aislamiento). Si
// la flota crece hasta que los reinicios sean frecuentes, ESTA es la decisión que hay
// que revisar, y `Edges` es el número que lo dirá.
func (s *Store) Aggregated() Aggregate {
	out := Aggregate{
		PorRegimen:        map[string]int64{},
		PorClase:          map[string]int64{},
		OmitidasPorMotivo: map[string]int64{},
	}
	if s == nil {
		return out
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out.Edges = len(s.last)
	for _, r := range s.last {
		addCounts(out.PorRegimen, r.ByRegime)
		addCounts(out.PorClase, r.ByClass)
		addCounts(out.OmitidasPorMotivo, r.SkippedByReason)
		out.MuestrasPrefill = addSamples(out.MuestrasPrefill, r.PrefillSamples)
		out.MuestrasGeneracion = addSamples(out.MuestrasGeneracion, r.GenerationSamples)
	}
	return out
}

// cloneCounts (en el viejo, copiar) copia el mapa; uno vacío se guarda como nil.
func cloneCounts(m map[string]int64) map[string]int64 {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]int64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// cloneInt (en el viejo, copiarInt) copia el valor apuntado; nil sigue siendo nil.
func cloneInt(p *int64) *int64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// addCounts (en el viejo, sumar) suma src sobre dst, clave a clave.
func addCounts(dst, src map[string]int64) {
	for k, v := range src {
		dst[k] += v
	}
}

// addSamples (en el viejo, sumarInt) suma conservando el «no medible»: nil + nil sigue
// siendo nil, y nil + n es n. Lo que NO puede pasar es que un Edge que no mide arrastre
// la suma a 0 y la convierta en «cero muestras», que es una afirmación distinta.
func addSamples(acc, v *int64) *int64 {
	if v == nil {
		return acc
	}
	total := *v
	if acc != nil {
		total += *acc
	}
	return &total
}
