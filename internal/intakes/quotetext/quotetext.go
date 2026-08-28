package quotetext

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// EjemplosPorDefecto es el N de D-044.11: cuántas cotizaciones aprobadas del tenant se
// le enseñan al modelo como few-shot. Es una constante NOMBRADA y no un literal suelto
// porque es una decisión de producto («cinco basta para que se le pegue el tono») y no
// un detalle de implementación.
const EjemplosPorDefecto = 5

// ════════════════════════════════════════════════════════════════════════════
// 🔴 LAS DOS COTAS DEL FEW-SHOT — DECISIÓN DE T5.1, PORQUE EL PLAN NO DECLARA NINGUNA
// ════════════════════════════════════════════════════════════════════════════
//
// QUE QUEDE ESCRITO: `llm.GenerateQuoteTextInput.Examples` NO TIENE TOPE DE TAMAÑO EN
// NINGUNA PARTE. Ni en el puerto («Puede venir vacío» es todo lo que su contrato dice),
// ni en el design, ni en `tasks.md`, ni en `requirements.md` — la palabra `Examples` no
// aparece en ninguno de los tres. Y el material del few-shot no está acotado por ningún
// otro sitio tampoco:
//
//   - la SEMILLA sí hereda una cota, la de `tenant_content` (1 MiB por blob,
//     design.md §2.2), que es TRES ÓRDENES DE MAGNITUD más de lo que cabe en un prompt;
//   - el HISTORIAL no hereda ninguna: `intake_revisions.rendered_text` es un `TEXT`
//     pelado (migración 0045) y `POST /api/v1/intakes/{id}/approve` —a diferencia de
//     `reanalyze`, del callback del CRM y de la importación de catálogo— decodifica su
//     cuerpo SIN `http.MaxBytesReader`. O sea que el texto que alimenta el few-shot no
//     tiene tope ni en la entrada HTTP, ni en la columna, ni en el prompt.
//
// POR QUÉ ESO IMPORTA AQUÍ Y NO ES UNA PRECAUCIÓN ABSTRACTA. P5 sale por la vía local:
// viaja al Edge por CloudLink y se ejecuta contra el Ollama del cliente, en CPU. Dos
// números medidos de este mismo plan lo enmarcan:
//
//   - el worker le da a CADA llamada `pipeline.PlazoPorLlamadaSuelo` = 48 s, de los que
//     el Edge se queda 41 s (MargenVeredicto = 7 s);
//   - el PREFILL EN FRÍO son ~50 s medidos en UAT (config.go, bootstrap.go). Es decir:
//     **más que el plazo entero**. Un prompt cuyo prefijo no esté caliente no cabe.
//
// Y el bloque de ejemplos ES PREFIJO: `BuildGenerateQuoteTextPromptCon` lo compone
// ANTES del borrador (instrucción + reglas + esquema + EJEMPLOS + borrador), y ese
// orden es contrato del ADR-0046 precisamente para que se cachee. Así que el tamaño de
// este bloque es exactamente lo que se paga en cada prefill frío — y se paga a menudo,
// porque el few-shot cambia cada vez que el dueño aprueba una cotización nueva.
//
// Sin cota, cinco cotizaciones largas concatenadas matan a P5 por timeout, y el
// síntoma sería un fallo de INFRAESTRUCTURA que no señala a ningún sitio. Esta ola ya
// vio morir a P4 así.
// ════════════════════════════════════════════════════════════════════════════

// MaxRunasEjemplo descarta ENTERO un ejemplo demasiado largo para el prompt.
//
// 1200 runas es ~8 veces una cotización real del caso base (las dos de Herminia rondan
// las 150), así que no muerde nunca en el caso normal: lo que corta es el blob que un
// tenant pegue por error en `quote_style_examples` —que admite 1 MiB— o un
// `rendered_text` en el que alguien escribió un contrato.
const MaxRunasEjemplo = 1200

// MaxRunasFewShot es el presupuesto AGREGADO del bloque de ejemplos.
//
// # DE DÓNDE SALE EL NÚMERO, QUE ES LA MITAD DE LA DECISIÓN
//
// 3000 runas son unos 3,3 KB en UTF-8 con acentos. Sumado a lo que el prompt de P5
// lleva siempre —instrucción, reglas de JSON, esquema y el borrador, del orden de 1,5 KB
// para un pedido de diez líneas— da un prompt de ~4,8 KB. El único dato de campo que
// hay sobre el tamaño de un prompt en este plan es el que MATÓ a una etapa: 7.786 bytes
// con un plazo de 30 s (ver el bloque de local.go sobre el reloj único). 4,8 KB queda
// claramente por debajo, y con 48 s en vez de 30.
//
// Por el otro lado: cinco cotizaciones típicas son ~750 runas, así que el presupuesto
// es CUATRO VECES el caso normal y en la práctica solo muerde cuando los ejemplos son
// desmesurados — que es justo lo que se quiere acotar.
//
// 🔴 NO ES UN NÚMERO HEREDADO NI MEDIDO: lo elegí en T5.1 contra los dos números de
// arriba porque el plan no declaraba ninguna cota. Quien lo mueva tiene que mover el
// razonamiento, no solo el literal — y lo barato de medir es el prefill de P5 con el
// few-shot lleno, que hoy nadie ha medido.
const MaxRunasFewShot = 3000

// RefEstiloSemilla es la `ref` de `public.tenant_content` donde el tenant deja sus
// cotizaciones de muestra (D-044.11).
//
// 🔴 ESTE PAQUETE ES SU PRIMER CONSUMIDOR. Antes de T5.1 la ref no aparecía en una
// sola línea de código de producción —solo en dos comentarios—, así que NO hay nada
// cableado que la escriba ni ningún tenant que la tenga puesta: la semilla estará
// vacía en campo hasta que alguien haga el `PUT /api/v1/tenant-content/quote_style_examples`.
// Que el generador funcione sin ella no es un fallback, es el caso normal de hoy.
const RefEstiloSemilla = "quote_style_examples"

// Origen de la sugerencia. Vocabulario cerrado: viaja por la API y por el log.
const (
	// OrigenLLM — el modelo redactó el texto Y sus importes cuadran con las líneas.
	OrigenLLM = "llm"
	// OrigenDeterminista — el texto lo compuso Render. Siempre viene con Motivo.
	OrigenDeterminista = "deterministic"
)

// Motivos por los que se cayó al determinista que NO vienen del verificador de
// precios. Los del verificador son los Motivo* de precios.go y viajan por el mismo
// campo: para quien lee el log son la misma pregunta —«¿por qué no lo escribió el
// modelo?»— y partirlos en dos campos obligaría a mirar los dos siempre.
const (
	// MotivoSinEjemplos — el tenant no tiene ni historial aprobado ni semilla, así
	// que no hay voz que imitar. 🔴 EN ESTE CASO NO SE LLAMA AL MODELO: no es que la
	// llamada se descarte, es que no ocurre. Ver Sugerir.
	MotivoSinEjemplos = "sin_ejemplos"
	// MotivoProveedorNoDisponible — no se pudo obtener el provider de la vía del
	// tenant (credencial caída, vía desconocida, entitlement).
	MotivoProveedorNoDisponible = "proveedor_no_disponible"
	// MotivoLLMFallo — el proveedor respondió con error (transporte, timeout o
	// calidad). No se reintenta aquí: el reintento por calidad es del pipeline, y
	// esto es una sugerencia interactiva que el dueño puede volver a pedir.
	MotivoLLMFallo = "llm_fallo"
	// MotivoSalidaIlegible — respondió, pero lo que devolvió no es el artefacto P5.
	MotivoSalidaIlegible = "salida_no_es_artefacto"
)

// ErrSinCablear es «se intentó construir el servicio sin una pieza obligatoria».
var ErrSinCablear = errors.New("quotetext: faltan piezas obligatorias (log, solicitudes, historial o selector)")

// ErrSinLineas es «esta solicitud no tiene nada que cotizar»: ni una línea de cliente.
// Es el hermano de intakes.ErrEmptyQuote y se declara aquí porque el que sale de
// Approve arrastra su propia semántica de transición, que este camino no tiene.
var ErrSinLineas = errors.New("quotetext: la solicitud no tiene líneas que cotizar")

// ════════════════════════════════════════════════════════════════════════════
// 🔴 DEUDA DECLARADA: NO HAY FORMA DE MEDIR CUÁNTAS VECES SE CAE AL DETERMINISTA
// ════════════════════════════════════════════════════════════════════════════
//
// Esto NO es un olvido, es un hueco conocido que se deja abierto con nombre y apellido.
//
// LO QUE HAY: cuando el texto del modelo se descarta, el motivo sale por DOS sitios y
// los dos son de consumo individual — una línea de `log.Warn` con `motivo` y `detalle`,
// y el campo `fallback_reason` de la respuesta HTTP, que ve la consola de UN dueño en
// UNA pantalla.
//
// LO QUE NO HAY: ninguna serie agregable. No se emite `flow_event` ni métrica Prometheus
// alguna, así que NADIE puede responder «¿qué porcentaje de sugerencias las escribe de
// verdad el modelo?» ni «¿cuál de los nueve motivos manda?». Y esas dos preguntas son
// las que deciden si la voz de la dueña funciona o si se está sirviendo el texto sobrio
// todo el rato: exactamente el modo de fallo MUDO que ya se pagó una vez en esta tarea
// —el umbral que apagaba el generador con una galleta barata en el carrito— y que solo
// apareció porque alguien fue a buscarlo a mano.
//
// POR QUÉ NO SE CONSTRUYE AQUÍ: la telemetría de esta ola es T5.2, y su lista de
// eventos está CERRADA en design §10 (`intake_draft_created`, `intake_line_corrected`,
// `intake_approved`, `intake_info_requested`, `intake_reanalyzed`). Ninguno de los cinco
// cubre esto, y añadir un sexto es una decisión de esa tarea, no de ésta. Meterlo por mi
// cuenta dejaría un evento fuera del contrato que T5.2 tiene que emitir con payloads
// exactos.
//
// QUÉ HARÍA FALTA, para que quien lo recoja no tenga que redescubrirlo: un contador con
// las etiquetas `origen` (llm|deterministic) y `motivo` —el vocabulario ya es cerrado y
// ya viaja por este struct, así que el productor es una línea en Sugerir—, o un
// `flow_event` equivalente si se prefiere la vía del outbox. Dueño natural: T5.2.
// ════════════════════════════════════════════════════════════════════════════

// Sugerencia es lo que devuelve el generador: un texto y la verdad sobre quién lo
// escribió.
//
// 🔴 EL ORIGEN NO ES TELEMETRÍA DECORATIVA. Es lo que le permite a la consola —y a
// quien lea un log— saber si lo que tiene delante lo redactó el modelo o es el
// respaldo sobrio, y por qué. Sin él, «la voz de la dueña no funciona» y «la voz de la
// dueña funciona pero este tenant no tiene historial» serían indistinguibles desde
// fuera, que es exactamente el agujero que hizo falta una tarea entera para cerrar en
// la Ola 1.7.
type Sugerencia struct {
	// Texto es la cotización sugerida, lista para que el dueño la edite y la apruebe.
	Texto string
	// Origen es OrigenLLM u OrigenDeterminista.
	Origen string
	// Motivo dice POR QUÉ no fue el modelo. Vacío cuando Origen es OrigenLLM.
	Motivo string
}

// ---------------------------------------------------------------------------
// LOS PUERTOS — todos declarados del lado del consumidor y todos de LECTURA
// ---------------------------------------------------------------------------

// LectorSolicitudes es de dónde sale la solicitud con sus líneas. Lo satisface
// *intakes.Service y *intakes.MemoryStore (los dos tienen ya esta firma).
type LectorSolicitudes interface {
	Get(ctx context.Context, tenantID, intakeID string) (intakes.Detail, error)
}

// LectorHistorial devuelve los textos de las últimas cotizaciones APROBADAS del
// tenant, de la más reciente a la más antigua. Lo satisface *intakes.Postgres
// (producción) y *intakes.MemoryStore (tests).
//
// Es POR TENANT y no por solicitud, y ésa es la diferencia con todo lo demás que este
// dominio lee: la voz de la dueña se aprende de lo que escribió en OTROS pedidos.
type LectorHistorial interface {
	ApprovedRenderedTexts(ctx context.Context, tenantID string, limit int) ([]string, error)
}

// LectorSemilla lee el blob de `public.tenant_content`. Lo satisface
// *store.PostgresRepository (misma firma, structural typing).
//
// Es OPCIONAL: sin él, el few-shot se arma solo con el historial.
type LectorSemilla interface {
	GetTenantContent(ctx context.Context, tenantID, ref string) ([]byte, error)
}

// ProviderSelector traduce un tenant en el llm.LLMProvider de SU vía. Lo satisface
// *llmvia.Selector.
//
// Este paquete NO sabe qué vía le tocó al tenant y no puede preguntarlo (requisito C2
// del ADR-0044): pide un provider y llama a GenerateQuoteText venga de donde venga.
type ProviderSelector interface {
	For(ctx context.Context, tenantID, originSessionID string) (llm.LLMProvider, error)
}

// ---------------------------------------------------------------------------
// EL SERVICIO
// ---------------------------------------------------------------------------

// Servicio es el generador. Mira sus campos: no hay ningún puerto que escriba y no hay
// ninguno que envíe. Ver la cabecera del paquete.
type Servicio struct {
	log       logger.Logger
	solic     LectorSolicitudes
	historial LectorHistorial
	semilla   LectorSemilla
	sel       ProviderSelector
	n         int
	plazo     time.Duration
}

// Opción configura el servicio al construirlo.
type Opción func(*Servicio)

// ConSemilla enchufa el lector de `tenant_content`. Sin él no hay ejemplos semilla.
func ConSemilla(s LectorSemilla) Opción {
	return func(sv *Servicio) {
		if s != nil {
			sv.semilla = s
		}
	}
}

// ConEjemplos fija el N del few-shot. Un valor <= 0 se ignora y deja
// EjemplosPorDefecto, por lo mismo que ConPlazoPorLlamada en stages: el llamante
// natural es una config con default, y un cero ahí significa «no configurado».
func ConEjemplos(n int) Opción {
	return func(sv *Servicio) {
		if n > 0 {
			sv.n = n
		}
	}
}

// ConPlazo acota cuánto puede durar LA llamada al modelo. Un valor <= 0 hereda el ctx
// del llamante tal cual.
func ConPlazo(d time.Duration) Opción {
	return func(sv *Servicio) {
		if d > 0 {
			sv.plazo = d
		}
	}
}

// NewServicio construye el generador. Devuelve ErrSinCablear si le falta una pieza
// obligatoria.
func NewServicio(log logger.Logger, solic LectorSolicitudes, historial LectorHistorial,
	sel ProviderSelector, opts ...Opción) (*Servicio, error) {
	if log == nil || solic == nil || historial == nil || sel == nil {
		return nil, ErrSinCablear
	}
	sv := &Servicio{log: log, solic: solic, historial: historial, sel: sel, n: EjemplosPorDefecto}
	for _, opt := range opts {
		if opt != nil {
			opt(sv)
		}
	}
	return sv, nil
}

// Sugerir devuelve el texto sugerido para la cotización de una solicitud.
//
// # EL ORDEN, QUE ES EL CONTRATO
//
//  1. la solicitud (404 opaco si no es del tenant, INV-8);
//  2. las precondiciones de contenido —hay líneas de cliente, ninguna sin precio—,
//     que son las MISMAS que las de `Approve` y por eso se reusan en vez de copiarse:
//     una sugerencia para un presupuesto que no se puede aprobar es trabajo tirado;
//  3. el borrador y su render determinista, que se calculan SIEMPRE y ANTES de tocar
//     el modelo. A partir de aquí ya hay una respuesta buena pase lo que pase;
//  4. el few-shot. **Sin ejemplos, se devuelve el determinista y NO SE LLAMA AL
//     MODELO** — ni siquiera se le pide el provider al selector;
//  5. la llamada, el parseo y el verificador de precios. Cualquier tropiezo cae al
//     determinista con su motivo.
//
// 🔴 NO SE COMPRUEBA EL ESTADO DE LA SOLICITUD, Y ES UNA DECISIÓN. `Approve` exige
// `pending_approval` porque transiciona; esto no transiciona nada, no escribe nada y
// no manda nada. Añadir aquí un 422 sería inventar una puerta que nadie pidió y que le
// impediría al dueño mirar cómo habría quedado el texto de un pedido que ya cerró.
func (s *Servicio) Sugerir(ctx context.Context, tenantID, intakeID string) (Sugerencia, error) {
	d, err := s.solic.Get(ctx, tenantID, intakeID)
	if err != nil {
		return Sugerencia{}, err
	}
	if !TieneLineasDeCliente(d.Items) {
		return Sugerencia{}, ErrSinLineas
	}
	if pendientes := intakes.PendingPriceLines(d.Revisions); len(pendientes) > 0 {
		return Sugerencia{}, &intakes.PendingPriceError{Lines: pendientes}
	}

	b := BorradorDe(d.Items)
	if !mismoImporte(b.Total, d.Total) {
		// No corrige nada: manda la suma de las líneas, que es lo que el cliente puede
		// comprobar a mano. Pero se dice, porque el store promete que coinciden
		// (EnsureShippingLine cuadra la cabecera) y que dejen de hacerlo es un dato.
		s.log.Warn("quotetext: el total de la cabecera no es la suma de las líneas; manda la suma",
			"tenant_id", tenantID, "intake_id", intakeID,
			"total_cabecera", d.Total, "suma_lineas", b.Total)
	}
	determinista := Render(b)

	if len(SecuenciaEsperada(b)) == 0 {
		// Todas las líneas están por confirmar: no hay ni un importe que el modelo
		// pudiera copiar ni que se le pudiera verificar. Llamarlo sería gastar una
		// inferencia para tirar su respuesta.
		return Sugerencia{Texto: determinista, Origen: OrigenDeterminista, Motivo: MotivoSinImportes}, nil
	}

	ejemplos := s.ejemplos(ctx, tenantID)
	if len(ejemplos) == 0 {
		return Sugerencia{Texto: determinista, Origen: OrigenDeterminista, Motivo: MotivoSinEjemplos}, nil
	}

	texto, motivo := s.redactar(ctx, tenantID, d, b, ejemplos)
	if motivo != "" {
		return Sugerencia{Texto: determinista, Origen: OrigenDeterminista, Motivo: motivo}, nil
	}
	return Sugerencia{Texto: texto, Origen: OrigenLLM}, nil
}

// redactar hace LA llamada al modelo y verifica lo que conteste. Devuelve el texto y
// un motivo VACÍO cuando el texto se puede mandar; en cualquier otro caso devuelve el
// motivo por el que no, y el llamante usa el determinista.
//
// 🔴 NO DEVUELVE ERROR, Y NO ES PEREZA: aquí no hay ningún fallo que deba tumbar la
// petición del dueño. El proveedor caído, el timeout, la salida ilegible y el precio
// inventado tienen todos la misma respuesta correcta —el texto sobrio— y convertir
// alguno en un 500 le quitaría al dueño una sugerencia que sí se podía dar.
func (s *Servicio) redactar(ctx context.Context, tenantID string, d intakes.Detail,
	b Borrador, ejemplos []string) (string, string) {
	quote, err := b.JSON()
	if err != nil {
		s.log.Warn("quotetext: no se pudo serializar el borrador para el prompt",
			"intake_id", d.ID, "error", err)
		return "", MotivoSalidaIlegible
	}
	prov, err := s.sel.For(ctx, tenantID, d.SessionID)
	if err != nil {
		s.log.Warn("quotetext: no hay proveedor LLM para este tenant; sale el texto determinista",
			"intake_id", d.ID, "error", err)
		return "", MotivoProveedorNoDisponible
	}

	raw, err := s.pedir(ctx, prov, quote, ejemplos)
	if err != nil {
		s.log.Warn("quotetext: el proveedor no redactó la cotización; sale el texto determinista",
			"intake_id", d.ID, "calidad", errors.Is(err, llm.ErrLLMQuality), "error", err)
		return "", MotivoLLMFallo
	}
	art, err := llm.ParseQuoteText(raw)
	if err != nil {
		// 🔴 El error NO cita `raw`: es texto redactado, no metadato.
		s.log.Warn("quotetext: la salida del modelo no es un artefacto P5 legible; sale el texto determinista",
			"intake_id", d.ID, "error", err)
		return "", MotivoSalidaIlegible
	}

	if v := Verificar(b, art.Text); !v.OK {
		s.log.Warn("quotetext: el texto del modelo NO cuadra con las líneas (INV-2); sale el texto determinista",
			"intake_id", d.ID, "motivo", v.Motivo, "detalle", v.Detalle)
		return "", v.Motivo
	}
	return art.Text, ""
}

// pedir es LA llamada, acotada por su propio plazo. Extraída por lo mismo que
// `pedirCantidades` en P4: el `defer cancel()` tiene que cerrar donde acaba la llamada.
func (s *Servicio) pedir(ctx context.Context, prov llm.LLMProvider,
	quote json.RawMessage, ejemplos []string) (json.RawMessage, error) {
	llamada, cancel := s.acotar(ctx)
	defer cancel()
	return prov.GenerateQuoteText(llamada,
		llm.GenerateQuoteTextInput{Quote: quote, Examples: ejemplos},
		llm.Options{Temperature: llm.TemperatureGreedy})
}

// acotar envuelve el ctx con el plazo por llamada. Devuelve SIEMPRE una cancelación
// llamable, para que el `defer cancel()` no necesite un `if` alrededor.
func (s *Servicio) acotar(ctx context.Context) (context.Context, context.CancelFunc) {
	if s.plazo <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, s.plazo)
}

// ---------------------------------------------------------------------------
// EL FEW-SHOT (D-044.11)
// ---------------------------------------------------------------------------

// ejemplos arma el few-shot: las últimas N cotizaciones aprobadas del tenant MÁS los
// ejemplos semilla, saneados y acotados.
//
// # EL REPARTO DEL CUPO, QUE ES UNA DECISIÓN DE T5.1
//
// D-044.11 dice «las últimas N aprobadas + semilla opcional» y no dice cuántas de cada
// una cuando hay de las dos. Con el cupo entero para el historial, un tenant que se
// haya molestado en escribir su semilla dejaría de verla en cuanto tuviera cinco
// aprobadas; con el cupo entero para la semilla, la voz real del tenant se pierde. El
// reparto es: **la semilla tiene reservada la mitad del cupo, y el historial se queda
// con el resto** — con la semilla vacía, el historial usa el cupo entero.
//
// El orden final es historial primero (más reciente antes) y semilla después, porque
// lo que el tenant escribió de verdad la semana pasada retrata mejor su voz que una
// muestra que puso el día que se dio de alta. Ese orden es, además, el de PRIORIDAD
// DECRECIENTE, y por eso el presupuesto de runas se gasta recorriéndolo de principio a
// fin (ver acotarPorPresupuesto): el reparto de arriba decide cuántas RANURAS le tocan
// a cada fuente, y la cota agregada decide cuántas de ésas caben de verdad en el prompt.
//
// Ningún fallo de lectura tumba nada: un few-shot más pobre solo significa un texto
// más sobrio, y en el peor caso el determinista.
func (s *Servicio) ejemplos(ctx context.Context, tenantID string) []string {
	semilla := s.deLaSemilla(ctx, tenantID)

	hist, err := s.historial.ApprovedRenderedTexts(ctx, tenantID, s.n)
	if err != nil {
		s.log.Warn("quotetext: no se pudo leer el historial aprobado del tenant; el few-shot va sin él",
			"tenant_id", tenantID, "error", err)
		hist = nil
	}
	hist = sanear(hist)

	if cupo := s.n - s.n/2; len(semilla) > 0 && len(hist) > cupo {
		hist = hist[:cupo]
	}
	out := hist
	for _, ex := range semilla {
		if len(out) >= s.n {
			break
		}
		if !contieneTexto(out, ex) {
			out = append(out, ex)
		}
	}
	return s.acotarPorPresupuesto(tenantID, out)
}

// acotarPorPresupuesto aplica MaxRunasFewShot sobre la lista YA ordenada por
// prioridad, y devuelve el prefijo que cabe.
//
// # LA REGLA DEL RECORTE, QUE ES LO QUE HAY QUE PODER PREDECIR
//
// Se recorre de principio a fin sumando runas. En cuanto un ejemplo NO CABE, se para:
// se descartan él y TODOS LOS SIGUIENTES. Dos decisiones dentro de esa frase:
//
//  1. **Se descartan ejemplos ENTEROS, jamás se trunca uno.** Un ejemplo cortado a
//     media frase no es un ejemplo peor: es un ejemplo de otra cosa. Le está enseñando
//     al modelo que las cotizaciones de este negocio acaban de golpe, y P5 redacta
//     EXACTAMENTE lo que el cliente va a leer por WhatsApp. Perder un ejemplo cuesta
//     algo de estilo; truncarlo enseña un defecto.
//  2. **Se para en el primero que no cabe, en vez de saltárselo y seguir probando.**
//     Saltar haría que la lista final dependiera de las longitudes de los ejemplos
//     POSTERIORES: dos tenants con el mismo historial y un ejemplo largo en medio se
//     llevarían few-shots distintos, y explicar por qué exigiría releer los cinco
//     textos. Parando, el resultado es siempre «los K primeros», y K se explica solo.
//
// Como la lista viene en orden de prioridad decreciente —historial de más reciente a
// más antiguo, y la semilla detrás—, recortar por la cola descarta primero lo más
// antiguo del historial, que es lo que menos dice sobre cómo escribe el negocio HOY.
//
// El recorte se avisa por log: un few-shot que encoge sin decirlo sería exactamente la
// clase de merma silenciosa que esta cota existe para hacer visible.
func (s *Servicio) acotarPorPresupuesto(tenantID string, ex []string) []string {
	total := 0
	for i, e := range ex {
		n := utf8.RuneCountInString(e)
		if total+n > MaxRunasFewShot {
			s.log.Warn("quotetext: el few-shot no cabe en su presupuesto; se recorta por la cola",
				"tenant_id", tenantID, "ejemplos_pedidos", len(ex), "ejemplos_usados", i,
				"runas_usadas", total, "presupuesto_runas", MaxRunasFewShot)
			return ex[:i]
		}
		total += n
	}
	return ex
}

// deLaSemilla lee y sanea los ejemplos de `tenant_content`. Devuelve nada —nunca un
// error— cuando no hay lector, cuando la ref no existe (el caso NORMAL hoy) o cuando
// el blob no tiene una forma que este paquete sepa leer.
func (s *Servicio) deLaSemilla(ctx context.Context, tenantID string) []string {
	if s.semilla == nil {
		return nil
	}
	blob, err := s.semilla.GetTenantContent(ctx, tenantID, RefEstiloSemilla)
	if err != nil {
		// La ausencia de la ref NO es un problema y es lo que va a pasar en todos los
		// tenants hasta que alguien la escriba: por eso es Debug y no Warn.
		s.log.Debug("quotetext: sin ejemplos semilla para este tenant",
			"tenant_id", tenantID, "ref", RefEstiloSemilla, "error", err)
		return nil
	}
	ex, err := ParseSemilla(blob)
	if err != nil {
		s.log.Warn("quotetext: el blob de ejemplos semilla no tiene una forma legible; se ignora",
			"tenant_id", tenantID, "ref", RefEstiloSemilla, "error", err)
		return nil
	}
	return sanear(ex)
}

// ParseSemilla lee el blob de `tenant_content` ref `quote_style_examples`.
//
// 🔴 ESTE FORMATO LO FIJA T5.1 PORQUE NO EXISTÍA. No hay ni un tenant con esta ref
// escrita, así que no hay compatibilidad que romper. Se admiten las DOS formas obvias
// para que nadie tenga que adivinar cuál es:
//
//	["texto 1", "texto 2"]                 — el array pelado
//	{"examples": ["texto 1", "texto 2"]}   — envuelto, por si algún día lleva más claves
//
// Cualquier otra cosa devuelve error y el llamante la ignora con un aviso: un blob mal
// formado no puede dejar sin cotización a nadie.
func ParseSemilla(blob []byte) ([]string, error) {
	var lista []string
	if err := json.Unmarshal(blob, &lista); err == nil {
		return lista, nil
	}
	var envuelto struct {
		Examples []string `json:"examples"`
	}
	if err := json.Unmarshal(blob, &envuelto); err != nil {
		return nil, errors.New("quotetext: la semilla no es un array de textos ni un objeto con `examples`")
	}
	if envuelto.Examples == nil {
		return nil, errors.New("quotetext: el objeto de la semilla no trae la clave `examples`")
	}
	return envuelto.Examples, nil
}

// sanear deja los ejemplos utilizables: sin vacíos, sin repetidos, sin los que se
// pasan de MaxRunasEjemplo y sin los que no son UTF-8. Conserva el orden de entrada.
//
// Es la PRIMERA de las dos cotas y actúa por ejemplo: el que se pasa se descarta
// ENTERO, nunca se trunca (el porqué está en acotarPorPresupuesto, y vale igual aquí).
// La segunda —el presupuesto agregado— se aplica después, sobre la lista ya ordenada.
func sanear(ex []string) []string {
	out := make([]string, 0, len(ex))
	for _, e := range ex {
		e = strings.TrimSpace(e)
		if e == "" || utf8.RuneCountInString(e) > MaxRunasEjemplo || !utf8.ValidString(e) {
			continue
		}
		if !contieneTexto(out, e) {
			out = append(out, e)
		}
	}
	return out
}

// contieneTexto es la pertenencia por igualdad exacta. El dedupe es literal a
// propósito: dos cotizaciones que se parecen mucho son DOS ejemplos legítimos de la
// misma voz, y decidir cuánto parecido es demasiado sería inventar un umbral.
func contieneTexto(vals []string, v string) bool {
	for _, x := range vals {
		if x == v {
			return true
		}
	}
	return false
}
