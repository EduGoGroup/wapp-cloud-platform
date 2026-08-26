// Package anclaje decide A QUÉ LÍNEA del presupuesto pertenece cada adjunto que
// mandó el cliente —una foto, un audio, un documento— y, cuando no hay certeza,
// decide NO DECIDIR: la referencia sube a nivel de SOLICITUD (Plan 044 · Ola 3 ·
// T3.3, REQ-29).
//
// # ES DETERMINISTA A PROPÓSITO: AQUÍ NO ENTRA EL LLM
//
// El pipeline ya gasta cuatro llamadas al modelo (P2→P3→P4 y la zona gris del
// matcher). Repartir cuatro fotos entre tres líneas no necesita una quinta: es una
// regla de orden y de palabras, y una regla se puede probar. Un modelo, además,
// SIEMPRE contesta —inventaría el ancla que esta tarea prohíbe inventar—.
//
// # LAS TRES REGLAS, EN ESTE ORDEN Y NO EN OTRO
//
//  1. 🔴 **El audio va SIEMPRE a nivel de solicitud**, con la etiqueta literal
//     EtiquetaAudio. Nunca a una línea, ni aunque su propio mensaje nombre el
//     producto. No es una heurística: es REQ-29 («los audios del cliente van al
//     borrador como adjuntos SIN procesarse»). Por eso se comprueba ANTES que nada,
//     y por eso las otras dos reglas no llegan a verlo.
//  2. **Mención textual**: si el mensaje que TRAE el adjunto nombra a UNA sola línea
//     —por un token distintivo de su etiqueta—, el adjunto es de esa línea. «Así la
//     quiero, de chocolate» junto a la foto es la señal más fuerte que hay.
//  3. **Proximidad**: si no hay mención, se mira hacia atrás desde el mensaje del
//     adjunto buscando el mensaje de texto más cercano que sostenga la evidencia de
//     UNA sola línea. Ese es «hablar de la torta 1 y mandar dos fotos justo después».
//
// Y la cláusula de cierre, que es la que manda: **cualquier otra cosa es solicitud**.
// Ninguna evidencia cerca, evidencia de DOS líneas en el mismo mensaje, el adjunto
// llegó antes de que se hablara de nada, pasó demasiado tiempo — todo eso es
// «no lo sé», y «no lo sé» se pinta en la cabecera del borrador, no colgando de una
// línea que el dueño va a creerse.
//
// # 🔴 EL INVARIANTE CONTABLE: NI SE PIERDE NI SE DUPLICA
//
// Toda referencia de entrada sale EXACTAMENTE UNA VEZ, anclada a una línea o a nivel
// de solicitud. Es estructural y no una promesa del comentario: Repartir recorre las
// refs UNA vez y cada vuelta termina en UN solo `append` —los tres caminos acaban en
// `continue`—. Quien añada un cuarto destino, o un «y además déjala en la cabecera
// por si acaso», rompe TestInvarianteContable_NiSePierdeNiSeDuplica.
//
// # 🔴 LOS DOS RELOJES: AQUÍ NO SE LLAMA A time.Now()
//
// Este paquete no consulta ningún reloj. Los instantes ENTRAN, y tienen que venir
// TODOS del mismo sitio: el reloj del CLIENTE (`ts_unix` del entrante, que es el
// mismo que alimenta `intake_jobs.message_ts` y la base de fechas de P4). Mezclar
// aquí un `now()` del servidor o un `created_at` de Postgres con el `ts_unix` del
// teléfono sería comparar dos relojes: el error saldría a favor o en contra según el
// desfase del día, sería permanente y no daría ni una señal. Con los dos lados del
// mismo origen, la resta significa lo que dice.
//
// Corolario práctico: si el llamante NO tiene instantes (todos en cero), la ventana
// temporal no descarta nada y el reparto se decide solo por ORDEN (`Seq`), que es la
// forma degradada y sana. No hay un modo «adivina la hora».
//
// # QUÉ SE REUSÓ DEL PLAN 017 Y QUÉ NO, Y POR QUÉ
//
// 🔴 **La infraestructura de media del Plan 017 es de SALIDA, no de entrada, y no
// sirve para esto.** `model.MediaRef` (`internal/flujos/model/model.go:169`) describe
// un archivo que NOSOTROS mandamos: lleva `Filename`, `Mime` y `Caption` porque el
// Edge los fija al subirlo a WhatsApp, su `Kind` es el par `document|image` que
// `mapKind` (`internal/gateway/grpc/send.go:454`) traduce al enum del proto —donde
// **audio no existe**, cae en `MEDIA_KIND_UNSPECIFIED`— y no tiene instante, porque
// un archivo que se envía no tiene «cuándo lo mandó el cliente». Ese tipo vive además
// en `internal/flujos/model`, y arrastrarlo hasta `internal/intake` acoplaría el
// pipeline de captación al dominio del Motor de Flujos para heredar tres campos que
// no se usan y perder los dos que sí. Lo que SÍ se hereda del 017 es su CONVENCIÓN:
// `Ref` es una key opaca del almacén con el prefijo `wapp/media/…`
// (`internal/publicapi/media.go:28`), sin URL ni credenciales (ADR-0007/0009).
//
// Lo que se reusa de verdad es `internal/evidence`: la regla de «esta frase aparece
// DE VERDAD en lo que escribió el cliente» ya está escrita, medida y con dueño, y
// aquí se llama —no se reimplementa—.
package anclaje

import (
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/evidence"
)

// Clases de adjunto que este paquete distingue. El vocabulario NO es un CHECK de
// base de datos: es lo que llega del entrante, y por eso se compara en minúsculas y
// con tolerancia.
//
// 🔴 Son TRES los nombres que significan «audio», no uno: WhatsApp manda una nota de
// voz como `ptt` (push-to-talk) y un archivo de música como `audio`. Tratar solo
// `audio` dejaría que la nota de voz —que es justo el caso de Ambar, cuatro notas de
// voz en la conversación real— se colara hasta una línea. Ver esAudio.
const (
	KindImage    = "image"
	KindAudio    = "audio"
	KindPTT      = "ptt"   // nota de voz de WhatsApp: ES audio
	KindVoice    = "voice" // alias que usan algunos puentes: ES audio
	KindVideo    = "video"
	KindDocument = "document"
)

// EtiquetaAudio es el texto LITERAL con el que el audio del cliente aparece en el
// borrador (tasks.md T3.3, REQ-29, design §7.5). Es una constante y no un literal
// suelto porque la pinta la bandeja y la comprueba el test: dos copias se
// desincronizan.
//
// ⚠️ `design.md` §7.4 la muestra recortada («🎙️ audio del cliente») dentro de un
// JSON de ejemplo. La forma buena es esta, la de T3.3 y REQ-29, que es la que el
// dueño lee y la que le dice qué tiene que hacer con ella.
const EtiquetaAudio = "🎙️ audio del cliente — escúchalo"

// MediaRef es UN adjunto del cliente, tal como se persiste en
// `intake_revisions.payload` (design §7.4): `{"kind":…,"ref":…,"label":…}`.
//
// 🔴 Seq y En NO se serializan, y no es un descuido: son las ENTRADAS de la
// heurística, no parte del contrato del borrador. El día que alguien las publique
// estará metiendo el reloj del cliente en un payload que se guarda en claro.
type MediaRef struct {
	// Ref es el identificador OPACO del objeto (la key `wapp/media/…` del Plan 017, o
	// el `wa_message_id` mientras el entrante no traiga una key propia — que es lo que
	// pasa hoy, ver aggregator.go:228). Nunca una URL ni una credencial.
	Ref string `json:"ref"`
	// Kind es la clase del adjunto. Ver las constantes Kind*.
	Kind string `json:"kind"`
	// Label es la etiqueta que ve el dueño. Solo la llevan los audios
	// (EtiquetaAudio); una foto anclada a su línea no necesita rótulo porque su
	// contexto ES la línea.
	Label string `json:"label,omitempty"`
	// Seq es el número del turno que trajo el adjunto, en la misma escala que
	// `events.ThreadEntry.Seq`. Es el ORDEN, y es lo único imprescindible: sin
	// instantes el reparto sigue funcionando, sin orden no.
	Seq int `json:"-"`
	// En es el instante del mensaje del CLIENTE que trajo el adjunto. Cero = «no se
	// sabe», y entonces la ventana temporal no descarta nada.
	En time.Time `json:"-"`
}

// Turno es UN mensaje de la conversación, con su orden y su instante. Es la forma
// mínima de `events.ThreadEntry` (`internal/flujos/events/thread_reader.go:80`) más
// el instante que a ese tipo le falta hoy.
//
// El texto que se pasa aquí tiene que ser el del CLIENTE. Un resumen del sistema o
// una coletilla del negocio no son sitio donde buscar la evidencia de una línea
// (REQ-10b, D-044.24): quien construya los turnos filtra por `Kind == KindMessage` y
// `Role == client` ANTES de llamar, exactamente igual que hace el compositor del
// literal (`runtime.ComposeSourceText`).
type Turno struct {
	Seq   int
	Texto string
	En    time.Time
}

// Linea es una línea del presupuesto que PUEDE recibir adjuntos.
//
// Es a propósito una vista mínima y no el tipo de línea del borrador: este paquete
// no sabe de sku, ni de precio, ni de match, y no debe saberlo. Recibe un índice y
// las dos cuerdas de las que tira la heurística, y devuelve índices.
type Linea struct {
	// Idx es el identificador de la línea para el llamante (su posición en el
	// borrador). Se devuelve tal cual en Reparto.PorLinea; este paquete no lo
	// interpreta.
	Idx int
	// Evidencia es la frase que el cliente escribió y que sostiene la línea (la
	// `evidence` de P3/P4). Es lo que ancla la línea a UN mensaje concreto de la
	// conversación, y por eso es lo que hace posible la proximidad.
	//
	// Vacía ⇒ la línea no participa en la regla de proximidad. Es el caso de la línea
	// de envío, que no sale de ninguna frase del cliente.
	Evidencia string
	// Etiqueta es el nombre del producto tal como se le va a pintar al dueño («Torta
	// chocolate húmedo + crema choc.»). De aquí salen los tokens distintivos de la
	// mención textual.
	Etiqueta string
}

// Reparto es el resultado: qué adjunto quedó en qué sitio.
type Reparto struct {
	// PorLinea va indexado por Linea.Idx. Una línea sin adjuntos NO aparece.
	PorLinea map[int][]MediaRef
	// Solicitud son los adjuntos de la cabecera: los audios (siempre) y todo aquello
	// de lo que no hubo certeza.
	Solicitud []MediaRef
}

// Opciones son los dos topes de la regla de proximidad.
//
// 🔴 LOS DOS NÚMEROS NO ESTÁN MEDIDOS. No hay una muestra de conversaciones reales
// con adjuntos de la que salgan: son el lado CONSERVADOR de una regla cuyo error
// barato es «sube a la cabecera» y cuyo error caro es «cuelga de la línea
// equivocada». Estrecharlos manda más refs a la solicitud (seguro); ensancharlos
// ancla más (arriesgado). El día que haya muestra, se recalibran con ella y no a ojo.
type Opciones struct {
	// MaxMensajesAtras es cuántos mensajes CON TEXTO se miran hacia atrás, contando el
	// del propio adjunto. Los mensajes sin texto —los otros adjuntos de la misma
	// ráfaga— NO gastan presupuesto: si no, mandar tres fotos seguidas haría que la
	// tercera «olvidara» de qué se estaba hablando.
	// 0 ⇒ MaxMensajesAtrasPorDefecto.
	MaxMensajesAtras int
	// Ventana es cuánto tiempo hacia atrás se admite. Una foto que llega dos horas
	// después de hablar de la torta no es «justo después». Solo se aplica cuando los
	// DOS instantes son conocidos.
	// 0 ⇒ VentanaPorDefecto.
	Ventana time.Duration
}

// Los valores por defecto. Ver la advertencia de Opciones sobre de dónde salen.
const (
	MaxMensajesAtrasPorDefecto = 3
	VentanaPorDefecto          = 5 * time.Minute
)

func (o Opciones) conDefectos() Opciones {
	if o.MaxMensajesAtras <= 0 {
		o.MaxMensajesAtras = MaxMensajesAtrasPorDefecto
	}
	if o.Ventana <= 0 {
		o.Ventana = VentanaPorDefecto
	}
	return o
}

// Repartir reparte `refs` entre las líneas y la cabecera. Es PURA: no lee, no
// escribe, no consulta reloj y no registra nada.
//
// El orden de salida es el de ENTRADA, dentro de cada destino. No depende de recorrer
// ningún mapa, así que dos ejecuciones con la misma entrada dan la misma salida byte
// a byte — que es lo que permite afirmar el reparto en un test en vez de contarlo.
func Repartir(turnos []Turno, lineas []Linea, refs []MediaRef, opts Opciones) Reparto {
	opts = opts.conDefectos()

	orden := slices.Clone(turnos)
	slices.SortStableFunc(orden, func(a, b Turno) int { return a.Seq - b.Seq })

	dist := distintivos(lineas)
	out := Reparto{PorLinea: make(map[int][]MediaRef, len(lineas))}

	for _, ref := range refs {
		// REGLA 1, y va primera. Un audio no llega a las otras dos reglas: no es que
		// «no encuentre ancla», es que no se le busca.
		if esAudio(ref.Kind) {
			ref.Label = EtiquetaAudio
			out.Solicitud = append(out.Solicitud, ref)
			continue
		}
		// REGLA 2.
		if idx, ok := anclaPorMencion(ref, orden, dist); ok {
			out.PorLinea[idx] = append(out.PorLinea[idx], ref)
			continue
		}
		// REGLA 3.
		if idx, ok := anclaPorProximidad(ref, orden, lineas, opts); ok {
			out.PorLinea[idx] = append(out.PorLinea[idx], ref)
			continue
		}
		// LA CLÁUSULA DE CIERRE: sin certeza, a la cabecera. Nunca se inventa el ancla.
		out.Solicitud = append(out.Solicitud, ref)
	}
	return out
}

// esAudio reconoce las TRES formas con las que llega una nota de voz o un audio. La
// comparación es en minúsculas porque el `kind` viene del entrante y no de un CHECK.
func esAudio(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case KindAudio, KindPTT, KindVoice:
		return true
	default:
		return false
	}
}

// anclaPorMencion mira el texto del MENSAJE QUE TRAE el adjunto y busca en él un
// token distintivo de UNA sola línea.
//
// Dos líneas nombradas en la misma frase («te mando fotos de las dos tortas») NO
// anclan a ninguna: son la ambigüedad que la regla 2 del enunciado manda mandar a la
// solicitud.
func anclaPorMencion(ref MediaRef, turnos []Turno, dist map[int][]string) (int, bool) {
	i, hay := slices.BinarySearchFunc(turnos, ref.Seq, func(t Turno, seq int) int { return t.Seq - seq })
	if !hay {
		return 0, false
	}
	pie := tokenSet(turnos[i].Texto)
	if len(pie) == 0 {
		return 0, false
	}
	encontrado, n := 0, 0
	for idx, toks := range dist {
		for _, t := range toks {
			if _, ok := pie[t]; ok {
				encontrado, n = idx, n+1
				break
			}
		}
	}
	if n != 1 {
		return 0, false
	}
	return encontrado, true
}

// anclaPorProximidad camina hacia atrás desde el mensaje del adjunto —incluido él
// mismo, que es el más cercano de todos— hasta encontrar un mensaje que sostenga la
// evidencia de UNA sola línea.
//
// Tres formas de terminar SIN ancla, y las tres son deliberadas:
//
//   - un mensaje sostiene evidencia de DOS o más líneas ⇒ ambigüedad, se para ahí. No
//     se sigue mirando hacia atrás: lo más cercano ya fue ambiguo, y lo de más atrás
//     no puede aclararlo.
//   - se agotó el presupuesto de mensajes o la ventana temporal ⇒ demasiado lejos.
//   - se acabó la conversación hacia atrás ⇒ el adjunto llegó antes de que se hablara
//     de nada.
func anclaPorProximidad(ref MediaRef, turnos []Turno, lineas []Linea, opts Opciones) (int, bool) {
	presupuesto := opts.MaxMensajesAtras
	for i := len(turnos) - 1; i >= 0; i-- {
		t := turnos[i]
		if t.Seq > ref.Seq {
			continue
		}
		// LA VENTANA. Solo muerde con los dos instantes conocidos: ver el bloque de los
		// dos relojes en el docstring del paquete.
		if !ref.En.IsZero() && !t.En.IsZero() && ref.En.Sub(t.En) > opts.Ventana {
			return 0, false
		}
		texto := evidence.Normalize(t.Texto)
		if texto == "" {
			// Otro adjunto de la misma ráfaga. No aporta y no cobra.
			continue
		}
		if presupuesto <= 0 {
			return 0, false
		}
		presupuesto--
		cand := lineasSostenidasPor(texto, lineas)
		switch len(cand) {
		case 0:
			continue
		case 1:
			return cand[0], true
		default:
			return 0, false
		}
	}
	return 0, false
}

// lineasSostenidasPor devuelve los índices de las líneas cuya evidencia aparece en
// `textoNorm` (ya normalizado). Usa la MISMA regla que P2/P3 para decidir si una
// frase aparece de verdad en lo que escribió el cliente: internal/evidence.
//
// Una evidencia que cruza DOS mensajes no la sostiene ninguno por separado, y
// entonces esa línea no participa. Es el lado seguro: se pierde un anclaje, no se
// inventa uno.
func lineasSostenidasPor(textoNorm string, lineas []Linea) []int {
	var out []int
	for _, l := range lineas {
		if evidence.Contains(textoNorm, l.Evidencia) {
			out = append(out, l.Idx)
		}
	}
	return out
}

// distintivos saca, por línea, los tokens de su etiqueta que NO comparte con ninguna
// otra línea.
//
// Es lo que hace utilizable la mención textual en el caso de Ambar: las dos tortas
// comparten «torta», así que «torta» no distingue nada y se cae; lo que queda es
// «chocolate» frente a «vainilla», que sí. Sin este filtro, una foto con el pie «la
// torta» nombraría a las dos líneas —y la ambigüedad la mandaría a la solicitud—,
// pero una con «la torta de chocolate» también nombraría a las dos y se perdería un
// anclaje bueno.
func distintivos(lineas []Linea) map[int][]string {
	frecuencia := make(map[string]int)
	crudos := make(map[int][]string, len(lineas))
	for _, l := range lineas {
		vistos := make(map[string]struct{})
		for t := range tokenSet(l.Etiqueta) {
			vistos[t] = struct{}{}
		}
		for t := range vistos {
			frecuencia[t]++
			crudos[l.Idx] = append(crudos[l.Idx], t)
		}
	}
	out := make(map[int][]string, len(crudos))
	for idx, toks := range crudos {
		var propios []string
		for _, t := range toks {
			if frecuencia[t] == 1 {
				propios = append(propios, t)
			}
		}
		if len(propios) > 0 {
			slices.Sort(propios) // determinismo: el mapa no ordena, esto sí
			out[idx] = propios
		}
	}
	return out
}

// palabrasVacias son las que sobreviven al filtro de longitud y no distinguen nada.
// La lista es corta a propósito: el filtro que hace el trabajo pesado es el de
// frecuencia (un token que aparece en dos etiquetas ya se cae solo).
var palabrasVacias = map[string]struct{}{
	"para": {}, "este": {}, "esta": {}, "unos": {}, "unas": {},
	"como": {}, "todo": {}, "toda": {}, "otro": {}, "otra": {},
}

// minRunasToken descarta los tokens cortos («de», «con», «sin», «x», «10»), que
// aparecen en cualquier etiqueta y no identifican un producto.
const minRunasToken = 4

// tokenSet parte un texto en tokens comparables: minúsculas, sin puntuación, sin
// palabras cortas y sin las vacías.
//
// 🔴 Se compara por TOKEN y no por subcadena, y es la misma lección que dejó escrita
// T3.2 con «sin sal» contra un catálogo que tiene «salsa»: `strings.Contains` casaría
// «sal» dentro de «salsa» y colgaría la foto de la línea equivocada.
//
// Nota de consolidación: `wapp-shared/textmatch` (T3.1) trae `Normalize` y
// `SplitTokens` con esta misma forma. NO se importa todavía porque ese módulo aún no
// tiene tag publicado y meterlo en el `go.mod` de este repo rompería la compilación
// sin workspace. El día que se publique, esta función se sustituye por la suya.
func tokenSet(s string) map[string]struct{} {
	campos := strings.FieldsFunc(evidence.Normalize(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := make(map[string]struct{}, len(campos))
	for _, c := range campos {
		if utf8.RuneCountInString(c) < minRunasToken {
			continue
		}
		if _, vacia := palabrasVacias[c]; vacia {
			continue
		}
		out[c] = struct{}{}
	}
	return out
}
