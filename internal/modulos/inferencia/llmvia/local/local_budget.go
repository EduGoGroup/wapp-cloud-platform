// Porta internal/llmvia/local/local.go @ ebf4eb7 (el presupuesto: la tabla de etapas con sus techos, el reloj único y el interruptor del techo; partido de local.go por E-13)

package local

import (
	"context"
	"errors"
	"fmt"
	"time"

	edgegrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/grpc"
)

// ════════════════════════════════════════════════════════════════════════════
// 🔴 EL PRESUPUESTO DE SALIDA LO FIJA EL CLOUD, Y LO FIJA POR TAREA (T1.7-3)
// ════════════════════════════════════════════════════════════════════════════
//
// LO QUE PASABA HASTA ESTA TAREA. El `num_predict` lo ponía el Edge y valía 256 para
// TODO. Las salidas medidas en campo de P2 y P3 son de 265–293 tokens, o sea que por
// la vía local esas dos etapas salían TRUNCADAS: el JSON no cerraba, ExtractJSON o el
// parser lo rechazaban con llm.ErrLLMQuality, el caller reintentaba a 0,3 y el
// reintento volvía a truncar en el mismo sitio. Dos inferencias de ~25 s para producir
// dos JSON rotos. Es el bloqueante de la Ola 2 por vía local.
//
// QUÉ ES ESTE NÚMERO, Y QUÉ NO ES. Es un TECHO, no una reserva: el modelo para en su
// token de fin, así que un techo alto no cuesta nada cuando no se usa. Lo único que
// paga un techo alto es el caso degenerado —el modelo que se repite— y ahí, a 6–12
// tok/s en el fierro real, cada 100 tokens de techo son 8–17 s de la PLAZA ÚNICA.
// De ahí el criterio con el que están elegidos los cinco números:
//
//	TECHO ≈ 2× la salida legítima más grande que esa etapa puede producir.
//
// Ni menos (truncar una salida legítima cuesta la inferencia entera más su reintento),
// ni mucho más (el techo es lo único que acota al modelo degenerado; el `timeout_ms`
// también corta, pero después de haber ocupado la plaza todo ese rato).
//
// ⚠️ ACOTA, NO CURA, y conviene no confundirlo: una P3 de 293 tokens a 6–12 tok/s
// sigue siendo 25–50 s de generación. Este campo impide que una inferencia ocupe la
// plaza MÁS de lo previsto; no promete que la ocupe menos.
//
// 🔴 Y ES DE SALIDA, NO DE ENTRADA. Los tamaños que circulan por los documentos de esta
// ola —«P4 pasó de 1.967 a 2.331 B al reordenarlo para I6»— son del PROMPT, o sea
// entrada, y no entran en esta cuenta: un prompt más largo se prefilla (una vez, si el
// prefijo es estable) pero no hace más larga la respuesta. Mezclar los dos números es el
// error fácil aquí, y llevaría a subir techos por un motivo que no los toca.
//
// 🔴 Y NO SE DERIVA DE `llm.Options`: el puerto compartido solo lleva Temperature, y
// ampliarlo sería pedirle al CALLER que sepa cuántos tokens ocupa el esquema de una
// P4 — que es justo lo que el ADR-0045 pone del lado del Cloud y, dentro del Cloud,
// del sitio que conoce la etapa. Ese sitio es este.

// stage (en el paquete viejo, etapa) es lo que este adaptador sabe de cada una de las
// cinco tareas y que NO viaja en el prompt: cuánto puede ocupar su respuesta y de qué
// color pintar su serie.
//
// Es una tabla y no cinco constantes sueltas para que los dos campos de una etapa se
// lean juntos: son la misma decisión vista dos veces.
type stage struct {
	// maxOutputTokens es el techo de la salida (campo 7 del frame).
	maxOutputTokens int32
	// class es el rótulo de telemetría (campo 8). SOLO rótulo: ver InferRequest.Class.
	class string
}

// Las cinco etapas del pipeline, con la aritmética de cada techo.
//
// El `class` va por etapa —y no por llamante— porque hoy cada etapa tiene UN llamante
// y su naturaleza es la de ese llamante: P1 la pide el adelanto de ventana, que existe
// para que el turno de WhatsApp no espere; P2–P5 son el pipeline del presupuesto, que
// corre de fondo sobre el hilo ya cerrado. ⚠️ El día que alguien reclasifique en masa
// —una P1 de lote— este campo deja de ser propiedad de la etapa y tiene que pasar a
// ser un parámetro del llamante. Hoy no hay tal llamante, y fingir que lo hay sería
// una opción sin usar que nadie mantendría.
var (
	// stageP1 — clasificar el mensaje en UNA intención.
	//
	// TECHO 192. La salida es un objeto de cinco claves cortas: version, intent,
	// confidence, params y evidence. Medido en campo: 16–17 tokens en el caso
	// frecuente y 52 en el mayor observado (tarea 618 del 24-08). El caso grande
	// legítimo —tres o cuatro params más una `evidence` que es una frase entera del
	// cliente— ronda los 60. 🔴 POR ESO NO SON 64, que es el número que el plan traía:
	// 64 está a un 23 % del máximo YA OBSERVADO, o sea que no es holgura, es una
	// moneda al aire — y el precio de perderla es truncar la clasificación que el
	// adelanto de ventana existe para conseguir. 192 son ~3,7× lo medido, y como techo
	// del caso degenerado siguen siendo 16–32 s, por debajo del presupuesto de 45 s de
	// quien la pide.
	stageP1 = stage{maxOutputTokens: 192, class: edgegrpc.ClassInteractive}
	// stageP2 — las ideas principales del hilo.
	//
	// TECHO 512. Medido: 265–267 tokens para un hilo de dos ideas, con la `evidence`
	// literal que el prompt exige. La salida ESCALA CON EL HILO —una entrada por cosa
	// distinta que el cliente pide, ~40–50 tokens cada una—, así que 512 cubre unas
	// diez ideas, que es más de lo que un hilo de WhatsApp trae.
	stageP2 = stage{maxOutputTokens: 512, class: edgegrpc.ClassBatch}
	// stageP3 — especificar UN ítem.
	//
	// TECHO 512. Medido: 170 y 293 tokens en dos ítems distintos. Aquí la salida NO
	// escala con el pedido (se llama una vez por ítem) sino con lo barroco de un solo
	// ítem: producto, variante, addons candidatos, personalizaciones, notas y
	// evidencia. 512 son ~1,75× el mayor medido.
	stageP3 = stage{maxOutputTokens: 512, class: edgegrpc.ClassBatch}
	// stageP4 — normalizar cantidades.
	//
	// TECHO 1024, y es el número que más se aparta del enunciado del plan («P4/P5
	// según su esquema»), así que va con su cuenta. 🔴 P4 ES LA ÚNICA ETAPA CUYA
	// SALIDA CRECE CON EL TAMAÑO DEL PEDIDO Y ADEMÁS REPITE EL ESQUEMA ENTERO DE CADA
	// ÍTEM: devuelve la lista COMPLETA, y cada ítem lleva product, qty, range,
	// unit_kind, package_size, addon_candidates, customizations, notes y evidence —un
	// superconjunto de lo que P3 produce por ítem—. Un ítem bien poblado son ~70–90
	// tokens; la cabecera (version + las dos fechas) otros ~30. Diez ítems ≈ 830
	// tokens. Con 512 —el número que P2 y P3 se ganaron— un pedido de seis ítems se
	// truncaría, y truncar P4 tira el presupuesto entero. 1024 cubre unos doce.
	stageP4 = stage{maxOutputTokens: 1024, class: edgegrpc.ClassBatch}
	// stageP5 — redactar la cotización.
	//
	// TECHO 768. La salida es {"version":N,"text":"..."} y el texto también escala con
	// el pedido: una línea por ítem con su importe (~25–35 tokens) más saludo y cierre
	// (~40). Quince ítems ≈ 570 tokens. Es la ÚLTIMA etapa y su salida es literalmente
	// lo que el cliente lee por WhatsApp, así que es el peor sitio del pipeline donde
	// ahorrar tokens: una cotización cortada a media línea es peor que ninguna.
	stageP5 = stage{maxOutputTokens: 768, class: edgegrpc.ClassBatch}
)

// WithMaxOutputTokens enciende o apaga el presupuesto de SALIDA que el Cloud fija por
// tarea (campo 7 del frame). Por defecto está ENCENDIDO — New lo materializa —, así
// que un Provider construido sin esta opción manda el techo de su etapa.
//
// 🔴 APAGA EL ENVÍO, NO BAJA EL NÚMERO: con `false` el frame lleva MaxOutputTokens 0
// (campo ausente), en las cinco etapas Y en el calentamiento. Lo que hay que poder
// reproducir es la conducta ANTERIOR a T1.7-3, y esa era «el Cloud no dice nada y el
// Edge aplica su 256», no «el Cloud pide 256».
//
// Existe para el control A/B de campo en la MISMA tanda, no para ajustar nada: quien
// quiera otro techo lo cambia en la tabla de etapas, que es donde está su aritmética.
func WithMaxOutputTokens(on bool) Option {
	return func(p *Provider) { p.capsOutput = on }
}

// outputCap (en el paquete viejo, techo) resuelve el presupuesto de salida EFECTIVO de
// una etapa. Con el interruptor apagado devuelve 0, que el gateway traduce a «campo
// ausente» y el Edge a su default.
func (p *Provider) outputCap(st stage) int32 {
	if !p.capsOutput {
		return 0
	}
	return st.maxOutputTokens
}

// ════════════════════════════════════════════════════════════════════════════
// 🔴 UN SOLO RELOJ: EL PLAZO SE HEREDA, NO SE INVENTA
// ════════════════════════════════════════════════════════════════════════════
//
// La historia de campo (2026-08-23) y la regla —EL ADAPTADOR NUNCA ES MÁS RESTRICTIVO
// QUE SU LLAMANTE— están en el comentario del paquete, en local.go. Es la misma
// doctrina que el MargenSocket del Edge («el que vence primero es SIEMPRE el plazo de
// dentro, y el veredicto lo emite quien lo sabe»), aplicada un salto más arriba en la
// misma cadena.

// MargenVeredicto es lo que este adaptador RESERVA del plazo del llamante para que el
// veredicto lo emita el Edge y no un corte del cliente. Vale 7 s.
//
// LA ARITMÉTICA, que es lo único que justifica el número:
//
//	ctx del llamante                    D
//	timeout_ms que se le da al Edge     D − MargenVeredicto   (el Edge corta aquí)
//	timer del gateway (awaitInference)  D − MargenVeredicto + edgegrpc.DefaultInferGrace
//
// 🔴 Promete MargenVeredicto > edgegrpc.DefaultInferGrace (R4.6.d), y un test lo
// custodia en vez de confiarlo a este párrafo. Con esa desigualdad el timer del
// gateway vence ANTES que el ctx, así que el desenlace es determinista: o llega el
// INFERENCE_ERROR_TIMEOUT nombrado del Edge, o el gateway emite `timeout` CON motivo.
// Lo que NUNCA pasa es que gane un `ctx.Done()`, que es edgegrpc.ErrInferenceAbandoned:
// SIN motivo, SIN aviso al dueño, y mintiendo sobre la causa. Si los dos márgenes
// fueran iguales, el veredicto lo decidiría el `select` de Go, que elige al azar entre
// casos listos: el aviso al dueño saldría o no según la moneda.
//
// SIETE SEGUNDOS = DefaultInferGrace (5 s) + 2 s de colchón, que son exactamente el
// MargenSocket del Edge y están aquí por lo mismo: cubrir un viaje de vuelta sin
// depender de que nadie lo esté midiendo.
const MargenVeredicto = 7 * time.Second

// DefaultTimeout es la RED DE SEGURIDAD: el `Timeout` del frame cuando el llamante no
// trae deadline. Vale 30 s. Es el caso RARO, no el normal.
//
// El camino normal es el pipeline, que SIEMPRE llama con deadline; en ese camino esta
// constante no se lee nunca. Un ctx sin deadline llegando aquí es un test o un
// llamante nuevo que se olvidó de acotar su propia espera, y para ese quedarse sin
// techo sería peor que un techo arbitrario. Quien necesite otro lo fija con
// WithTimeout — y si lo que quiere es que las inferencias del pipeline duren más, el
// número que tiene que mover NO es este, sino el presupuesto del llamante.
const DefaultTimeout = 30 * time.Second

// ErrSinPresupuesto indica que al llamante ya no le queda plazo útil: lo que resta de
// su deadline no supera MargenVeredicto, así que ninguna respuesta podría llegar a
// tiempo de servirle. El texto es observable y no cambia.
//
// Quien lo devuelve lo ENVUELVE (errors.Is lo encuentra) con el detalle, en este
// formato literal: "<ErrSinPresupuesto>: quedan <resto redondeado al milisegundo>, y
// el margen del veredicto es <MargenVeredicto>".
//
// 🔴 NO SE LLAMA AL EDGE, y esa es la decisión. Mandar el frame igual gastaría un
// command_id, un viaje por el stream y —lo caro— una plaza del Ollama del cliente
// para producir algo que nadie va a estar esperando cuando llegue.
//
// Es un error PELADO, sin Motivo(), y eso también es deliberado: el escritor de avisos
// de llmvia solo notifica lo que trae motivo, y aquí no hay ninguna degradación de la
// vía que contarle al dueño. Su equipo está perfectamente; lo que se acabó fue el
// presupuesto de quien preguntó. Es la misma familia que
// edgegrpc.ErrInferenceAbandoned.
var ErrSinPresupuesto = errors.New("llmvia/local: al llamante no le queda plazo para una inferencia")

// WithTimeout fija la RED DE SEGURIDAD (ver DefaultTimeout): el `Timeout` del frame
// cuando el ctx del llamante NO trae deadline. Un valor <= 0 se ignora.
//
// ⚠️ NO es un techo sobre el plazo heredado, y cambiarlo para que lo fuera sería
// reintroducir el defecto que este paquete arregló: un techo local puede quedarse por
// debajo de lo que el llamante estaba dispuesto a esperar. Con deadline en el ctx,
// esta opción no se lee.
func WithTimeout(d time.Duration) Option {
	return func(p *Provider) {
		if d > 0 {
			p.timeout = d
		}
	}
}

// frameTimeout (en el paquete viejo, plazo) resuelve el `timeout_ms` que viaja en el
// frame HEREDÁNDOLO del deadline del llamante. Es el corazón de este paquete desde el
// arreglo del reloj único: ver el bloque «UN SOLO RELOJ» de arriba.
//
// Tres desenlaces, y ninguno de ellos inventa un plazo por su cuenta:
//
//  1. **El ctx trae deadline** ⇒ lo que queda menos MargenVeredicto. Ese es el caso
//     de TODO el pipeline.
//  2. **Le queda menos que el margen** ⇒ ErrSinPresupuesto, sin tocar el cable.
//  3. **El ctx NO trae deadline** ⇒ DefaultTimeout, la red de seguridad.
//
// 🔴 LO QUE NO HACE, dicho porque es la tentación evidente: NO acota el resultado con
// p.timeout. Un `min(restante, p.timeout)` parecería prudente y sería exactamente el
// defecto de campo otra vez —el adaptador cortando por debajo de lo que su llamante
// estaba dispuesto a esperar—, solo que escrito con más letras.
func (p *Provider) frameTimeout(ctx context.Context) (time.Duration, error) {
	dl, ok := ctx.Deadline()
	if !ok {
		return p.timeout, nil
	}
	remaining := time.Until(dl) - MargenVeredicto
	if remaining <= 0 {
		return 0, fmt.Errorf("%w: quedan %v, y el margen del veredicto es %v",
			ErrSinPresupuesto, time.Until(dl).Round(time.Millisecond), MargenVeredicto)
	}
	return remaining, nil
}
