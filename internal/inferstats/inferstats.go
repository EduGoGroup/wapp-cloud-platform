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
package inferstats

import "sync"

// Parte es el bloque de inferencia de UN latido, ya traducido desde el contrato.
//
// Todos los contadores son ACUMULADOS del proceso del Edge. Los punteros distinguen
// «no medible» del cero, y esa distinción es el punto: el contrato transporta la
// ausencia con presencia nativa (sub-mensaje no puesto), y colapsarla a 0 se leería
// como «instantáneo» o «ninguna», que son afirmaciones distintas de «no lo sé».
type Parte struct {
	// PorRegimen cuenta las inferencias por régimen de prefill (`frio`, `templado`,
	// `caliente`). Mapa a propósito y no tres campos: el emisor puede recalibrar los
	// umbrales o añadir una cuarta clave sin tocar el contrato ni este código, y quien
	// lo lee debe tolerar claves que no conoce.
	PorRegimen map[string]int64
	// PorClase cuenta las inferencias por `class` (`interactivo`, `lote`). 🔴 SOLO
	// RÓTULO: aquí tampoco decide nada, igual que en el frame.
	PorClase map[string]int64
	// OmitidasPorMotivo es el desglose de intents omitidos del cajero (Plan 051 ·
	// T4.3), que hasta hoy también subía y moría.
	OmitidasPorMotivo map[string]int64
	// MuestrasPrefill y MuestrasGeneracion son el `samples` de cada InferenceLatency:
	// cuántas observaciones sostienen el cuantil de esa fase. nil = el Edge no lo
	// reporta (Edge viejo, o fase sin medir todavía).
	//
	// 🔴 SE PUBLICA EL `n` Y NO EL CUANTIL, y el porqué está en el registrador de
	// metrics: un p50 no se agrega entre Edges.
	MuestrasPrefill    *int64
	MuestrasGeneracion *int64
}

// Clave identifica al PROCESO que produjo los números: un Edge de un tenant. Ver la
// cabecera para por qué no es la sesión.
type Clave struct {
	TenantID string
	EdgeID   string
}

// Store guarda el último parte de cada Edge. Es seguro para uso concurrente: escribe
// el carril de trabajo de cada stream y lee el scrape de /metrics, que corren en
// goroutines distintas (y `Gather` colecta EN PARALELO).
//
// El cero-valor NO es utilizable: usa New.
type Store struct {
	mu      sync.RWMutex
	ultimos map[Clave]Parte
}

// New construye el almacén vacío.
func New() *Store { return &Store{ultimos: make(map[Clave]Parte)} }

// Observa registra el parte de un Edge, SUSTITUYENDO al anterior.
//
// Sustituir y no acumular es la decisión, y va contra la intuición de «es un
// contador, súmalo»: lo que llega ya es el acumulado del Edge. Un `+=` aquí contaría
// cada inferencia una vez por cada latido que la mencione, que con un latido cada
// pocos segundos es un factor de cien.
//
// Nil-safe: sobre un *Store nil no hace nada, para que un arranque sin observabilidad
// no obligue a poner guardas en el camino del latido.
func (s *Store) Observa(k Clave, p Parte) {
	if s == nil || k.EdgeID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ultimos[k] = Parte{
		PorRegimen:         copiar(p.PorRegimen),
		PorClase:           copiar(p.PorClase),
		OmitidasPorMotivo:  copiar(p.OmitidasPorMotivo),
		MuestrasPrefill:    copiarInt(p.MuestrasPrefill),
		MuestrasGeneracion: copiarInt(p.MuestrasGeneracion),
	}
}

// Agregado es la suma de la flota, listo para publicar.
type Agregado struct {
	// PorRegimen, PorClase y OmitidasPorMotivo suman los acumulados de todos los
	// Edges vivos, clave a clave.
	PorRegimen        map[string]int64
	PorClase          map[string]int64
	OmitidasPorMotivo map[string]int64
	// MuestrasPrefill y MuestrasGeneracion suman el `n` de los Edges QUE LO REPORTAN.
	// nil cuando no lo reporta ninguno — que es «no medible», no «cero muestras».
	MuestrasPrefill    *int64
	MuestrasGeneracion *int64
	// Edges es cuántos Edges sostienen el agregado. Se publica porque una suma de la
	// flota sin saber sobre cuántos se hizo es la misma trampa que un cuantil sin su
	// `n`: dice poco y parece decir mucho.
	Edges int
}

// Agrega suma los partes de todos los Edges conocidos.
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
func (s *Store) Agrega() Agregado {
	out := Agregado{
		PorRegimen:        map[string]int64{},
		PorClase:          map[string]int64{},
		OmitidasPorMotivo: map[string]int64{},
	}
	if s == nil {
		return out
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out.Edges = len(s.ultimos)
	for _, p := range s.ultimos {
		sumar(out.PorRegimen, p.PorRegimen)
		sumar(out.PorClase, p.PorClase)
		sumar(out.OmitidasPorMotivo, p.OmitidasPorMotivo)
		out.MuestrasPrefill = sumarInt(out.MuestrasPrefill, p.MuestrasPrefill)
		out.MuestrasGeneracion = sumarInt(out.MuestrasGeneracion, p.MuestrasGeneracion)
	}
	return out
}

func copiar(m map[string]int64) map[string]int64 {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]int64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func copiarInt(p *int64) *int64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func sumar(dst, src map[string]int64) {
	for k, v := range src {
		dst[k] += v
	}
}

// sumarInt suma conservando el «no medible»: nil + nil sigue siendo nil, y nil + n es
// n. Lo que NO puede pasar es que un Edge que no mide arrastre la suma a 0 y la
// convierta en «cero muestras», que es una afirmación distinta.
func sumarInt(acc, v *int64) *int64 {
	if v == nil {
		return acc
	}
	total := *v
	if acc != nil {
		total += *acc
	}
	return &total
}
