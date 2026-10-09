// Porta internal/intake/stages/draft.go @ 4cd9cfb

package stages

import (
	"context"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/anclaje"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// draft_revision.go — LA REVISIÓN de la etapa `draft` (trozo de draft.go, E-13): el
// contrato §7.4 que se guarda en `intake_revisions.payload` y el puerto por el que se
// escribe. Quien lo ejecuta es Draft.Run (paso 3).
//
// # QUÉ SE ESCRIBE
//
// UNA revisión por pasada, por RevisionWriter y por ningún otro puerto (R-06), con:
//
//   - `IntakeID`: la solicitud que resolvió la cabecera;
//   - `Kind`: `intakes.RevisionKindInterpreted`, SIEMPRE —un re-análisis sigue siendo una
//     interpretación de la máquina—;
//   - `Payload`: el RevisionPayload en JSON. Si no se puede serializar:
//     `draft: serializar el payload de la revisión del job %s: %w`, sin citar el payload;
//   - `CreatedBy`: un ROL, jamás una persona, y sale del JOB: `intakes.RevisionByOwner`
//     si `job.Reanalysis.IsFromOwner()`, `intakes.RevisionBySystem` si no;
//   - `RenderedText` vacío: la revisión interpretada no mandó ningún texto al cliente.
//
// El `revision_no` lo asigna el STORE: la etapa no escribe «1» en ninguna parte, y el
// número que publica (artefacto, empuje, `to_rev`) es el que el store devolvió.
//
// # CÓMO SE ARMA EL PAYLOAD
//
//   - `version`: `intakes.RevisionPayloadVersion`.
//   - `source_text`: `in.SourceText`, entero. `message_ts`: `job.MessageTS`, el del
//     PRIMER mensaje de la ventana, no la hora de creación. `delivery_date`:
//     `in.DeliveryDate`, tal cual (vacía = el borrador sale sin fecha).
//   - `lines`: las de `Match.Lines`, TAL CUAL y en su orden —la etapa no decide cuántas
//     hay ni les cambia nada—, cada una con los adjuntos de `Media.ByLine[su posición]`.
//     Sin líneas se serializa `[]`.
//   - `media_refs`: los de `Media.Request` y, detrás, los de cualquier índice de
//     `Media.ByLine` que no corresponda a ninguna línea (negativo o fuera de rango): NO
//     se pierden, suben a la cabecera —la cláusula de cierre del anclaje, «sin certeza, a
//     la cabecera»— con el `Warn` `draft: adjuntos anclados a una línea que no existe;
//     suben a la cabecera`. Ni se pierde ni se duplica un adjunto.
//   - `warnings`: los de `Match.Warnings`, tal cual (DEUDA-044.16).
//   - `analysis`: lo que trae `in.Analysis`, COMPLETADO —nunca pisado—. Si el job es un
//     re-análisis de la dueña, lo que el llamante dejó vacío sale del job: `provider` de
//     `Reanalysis.Via`, `source` de `Reanalysis.Source` y `reanalyzed_from` de
//     `Reanalysis.From` cuando es > 0 (0 = «no había revisión previa» ⇒ `null`). Después,
//     `source` vacío ⇒ SourceEventThread. Si `provider` sigue vacío el borrador sale
//     igual, con el `Warn` `draft: la revisión sale SIN vía de análisis; no se podrá
//     comparar local contra api (D-044.15)`. `model` no lo completa nadie.
//   - `suggested_questions`: se DERIVAN de las líneas, en su orden; no las trae nadie.
//
// # 🔴 LAS PREGUNTAS SON PARA EL CLIENTE, NO TAREAS PARA EL DUEÑO
//
// Solo hay dos huecos que únicamente el cliente puede rellenar, y sus textos son
// literales de contrato (design §7.4):
//
//   - la ZONA DE ENVÍO: una línea KindShipping SIN precio ⇒
//     `¿Zona de entrega para calcular el envío?`. Con precio, la zona estaba resuelta y
//     no se pregunta;
//   - la VARIANTE: una línea (que no sea el envío) con `variant_options` ⇒ con rango,
//     `¿Confirmas el tamaño de «<label>»: <min> o <max><unidad>?`, donde la unidad es la
//     del rango sin blancos en los bordes y con un espacio delante, o nada si viene vacía
//     —no se inventa: queda «: 10 o 12?»—; sin rango,
//     `¿Cuál de las presentaciones de «<label>» necesitas?`.
//
// NO generan pregunta las líneas `unmatched` (su precio lo pone el DUEÑO) ni los avisos
// del match (son cosas que el dueño MIRA en el original).

// Los valores de `analysis.source` (design §7.4, D-044.15): de dónde salió el material
// que se interpretó. Son vocabulario del payload y de `intake_reanalyzed`: no se tocan.
const (
	// SourceEventThread (antes `OrigenHiloDelEvento`) es el literal del cliente
	// reconstruido del hilo del evento. Es SIEMPRE el de la revisión 1.
	SourceEventThread = "event_thread"
	// SourcePastedText (antes `OrigenTextoPegado`) es material extra que pegó el DUEÑO
	// (Plan 045, D-045.5).
	SourcePastedText = "pasted_text"
	// SourceBoth (antes `OrigenAmbos`) es el hilo del evento MÁS el texto pegado.
	SourceBoth = "both"
)

// Analysis (antes `Analisis`) es el rastro de QUIÉN interpretó (design §7.4, D-044.15).
// Va EN CLARO: no es texto del cliente, es metadato de proceso.
//
// 🔴 EL CAMPO SE LLAMA `provider` Y LLEVA UNA VÍA (`local`|`api`, el eje del ADR-0044):
// está así en el contrato escrito y no se renombra por cuenta propia.
type Analysis struct {
	// Provider es la VÍA por la que corrió el pipeline: `local` o `api`.
	Provider string `json:"provider"`
	// Model es el modelo concreto, tal como lo declare el adaptador.
	Model string `json:"model,omitempty"`
	// Source es uno de los Source…: de dónde salió el material interpretado.
	Source string `json:"source"`
	// ReanalyzedFrom es el `revision_no` del que salió este re-análisis. Es puntero y NO
	// lleva `omitempty` a propósito: §7.4 escribe `"reanalyzed_from": null` en la
	// revisión 1, y «null» dice «esta es la primera lectura».
	ReanalyzedFrom *int `json:"reanalyzed_from"`
}

// RevisionLine (antes `LineaRevision`) es una línea del presupuesto tal como se congela
// en el payload: la línea del match MÁS los adjuntos que se le anclaron.
//
// 🔴 EMBEBE Line EN VEZ DE COPIAR SUS CAMPOS: la forma de la línea (§7.4) está definida
// en UN sitio —match.go—, y sus claves JSON salen aplanadas junto a `media_refs`.
type RevisionLine struct {
	Line
	// MediaRefs son los adjuntos ANCLADOS a esta línea. Los que no se pudieron anclar
	// con certeza están en la cabecera del payload.
	MediaRefs []anclaje.MediaRef `json:"media_refs,omitempty"`
}

// RevisionPayload (antes `PayloadRevision`) es el CONTRATO §7.4: lo que se guarda en
// `intake_revisions.payload` con `kind='interpreted'`. Las etiquetas JSON son el contrato
// mismo —cambiarlas exige subir `intakes.RevisionPayloadVersion`—, y dos de ellas
// (`source_text` y la `evidence` de cada línea) son además las claves que el store de
// solicitudes busca para sacar el literal y cifrarlo: renombrarlas aquí sin tocar
// `intakes` dejaría el texto del cliente EN CLARO.
type RevisionPayload struct {
	// Version es `intakes.RevisionPayloadVersion`.
	Version int `json:"version"`
	// SourceText es el texto ORIGINAL del cliente. 🔴 ES NIVEL 2 (ADR-0034): no llega a
	// la columna `payload`; el store lo saca y lo guarda cifrado, y lo devuelve a su
	// sitio al leer.
	SourceText string `json:"source_text,omitempty"`
	// MessageTS es el instante del PRIMER mensaje de la ventana (D-044.9).
	MessageTS time.Time `json:"message_ts"`
	// Analysis es el rastro de quién interpretó (D-044.15).
	Analysis Analysis `json:"analysis"`
	// DeliveryDate es la fecha de entrega ABSOLUTA que calculó P4. Vacía cuando la
	// expresión no se reconoció: el dueño la pregunta, que es mejor que fabricar un día.
	DeliveryDate string `json:"delivery_date,omitempty"`
	// MediaRefs son los adjuntos de la CABECERA: los audios y todo lo que el anclaje no
	// pudo colgar de una línea con certeza.
	MediaRefs []anclaje.MediaRef `json:"media_refs,omitempty"`
	// Lines son las líneas del presupuesto EN ORDEN (el orden es contrato, §7.5).
	Lines []RevisionLine `json:"lines"`
	// SuggestedQuestions son las preguntas PREPARADAS para el cliente. Nunca salen
	// solas: el dueño las edita y las manda (INV-1). Lista vacía se serializa `[]` y no
	// `null`: «no hay nada que preguntar» es una respuesta.
	SuggestedQuestions []string `json:"suggested_questions"`
	// Warnings son los ítems que el match degradó (DEUDA-044.16): posición y motivo. No
	// está en el ejemplo de §7.4 y se añade a propósito: la bandeja lee ESTE payload, y
	// sin los avisos aquí el motivo se quedaría en `intake_jobs.artifacts.match`.
	Warnings []Warning `json:"warnings,omitempty"`
}

// RevisionWriter (antes `EscritorRevision`) es lo ÚNICO que esta etapa necesita del
// dominio de solicitudes: dejar la revisión. Lo satisfacen `*intakes.Postgres` y
// `*intakes.MemoryStore`.
//
// 🔴 R-06: ES LA ÚNICA PUERTA POR LA QUE SALE UNA REVISIÓN. El store de solicitudes SACA
// el literal del payload y lo sella con el cipher antes de que toque la BD; es una
// barrera única en el store para que ningún escritor de revisiones pueda persistir
// literal en claro por olvido. Por el almacén de flujos (IntakeStore, EventWriter) el
// texto del cliente quedaría en claro, y por eso esos dos puertos no tienen por dónde.
type RevisionWriter interface {
	InsertRevision(ctx context.Context, rev intakes.Revision) (intakes.Revision, error)
}
