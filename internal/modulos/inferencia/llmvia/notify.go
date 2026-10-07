// Porta internal/llmvia/notify.go @ ebf4eb7

package llmvia

import "github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"

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
	panic(pendiente.Implementar("llmvia.WithDegradacionObservada"))
}
