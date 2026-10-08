// Porta internal/intakes/revisions.go @ 64c181a

package intakes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Clases de revisión (columna `kind` de public.intake_revisions, migración 0045).
// El conjunto está CERRADO por un CHECK en la tabla: añadir una clase exige una
// migración, que es exactamente la fricción que se quiere — una revisión de clase
// desconocida no es un dato nuevo, es un bug que llegó a la BD.
//
// Los siete valores son claves de wire en inglés (R-09) y se conservan byte a byte.
const (
	// RevisionKindCart ("cart") — la solicitud tal como la cerró el carrito
	// numérico. Es la revisión 1 de todo pedido conversacional (Plan 016/041).
	RevisionKindCart = "cart"
	// RevisionKindInterpreted ("interpreted") — lo que el pipeline LLM entendió del
	// texto del cliente (Plan 044).
	RevisionKindInterpreted = "interpreted"
	// RevisionKindCorrected ("corrected") — la corrección del dueño sobre una
	// revisión previa.
	RevisionKindCorrected = "corrected"
	// RevisionKindApproved ("approved") — la versión que el dueño aprobó y que se
	// le presupuestó al cliente.
	RevisionKindApproved = "approved"
	// RevisionKindCRM ("crm") — lo que devolvió el puente (ADR-0032).
	RevisionKindCRM = "crm"
	// RevisionKindDiscarded ("discarded") — rastro del descarte MANUAL de un pedido
	// huérfano (D-041.18). Es lo que distingue en el embudo un `abandoned` por
	// descarte del dueño de uno por cancelación del evento, sin un segundo valor de
	// status.
	RevisionKindDiscarded = "discarded"
	// RevisionKindRevalidated ("revalidated") — rastro del gancho de revalidación
	// del rescate (D-041.25): qué precios cambiaron, qué líneas se retiraron y el
	// texto exacto con el que se le avisó al cliente.
	RevisionKindRevalidated = "revalidated"
)

// Autores de una revisión (columna `created_by`): "system", "owner" y "crm". Es un
// ROL, no una persona: aquí NUNCA va un identificador de usuario, un número ni un
// nombre (CERO PII).
const (
	RevisionBySystem = "system"
	RevisionByOwner  = "owner"
	RevisionByCRM    = "crm"
)

// RevisionPayloadVersion es la versión del esquema del payload que escribe ESTE
// código: 1. Viaja DENTRO del blob (`{"version":1,…}`) y no en una columna aparte
// para que el payload siga siendo autodescriptivo cuando alguien lo copie a un
// export, a un webhook o a un ticket: un lector que no entienda la versión debe
// poder saberlo mirando solo lo que tiene delante.
const RevisionPayloadVersion = 1

// ErrEmptyRevisionPayload lo devuelven los stores cuando se intenta escribir una
// revisión sin payload; su texto es "la revisión no lleva payload". No se sustituye
// por un `{}` silencioso: una revisión vacía es un relato que afirma "aquí no pasó
// nada" sobre un acto que sí ocurrió. El store que lo devuelve NO escribe nada.
var ErrEmptyRevisionPayload = errors.New("la revisión no lleva payload")

// Revision es una versión auditada de la solicitud: qué se negoció y quién lo
// produjo (ADR-0031 §3). NO sustituye a las líneas (Items), que siguen siendo la
// verdad de lo VENDIDO; esto es el borrador y el rastro de cómo se llegó a él.
//
// # QUÉ HAY EN CLARO Y QUÉ NO (ENMENDADO — Plan 044 · T3.5, D-044.13 / ADR-0034)
//
// Esta cabecera decía «CERO PII: Payload y RenderedText son dato de negocio en
// claro». Eso era cierto mientras la única revisión del sistema era la del carrito
// numérico (líneas y totales), y dejó de serlo cuando el pipeline LLM empezó a
// guardar aquí lo que el CLIENTE escribió. El criterio vigente es GRADUADO (R-10):
//
//   - `payload` sigue EN CLARO y es dato de negocio cuantificable (nivel 1): skus,
//     cantidades, precios, fechas, variantes y las personalizaciones («sin sal»).
//   - El LITERAL del cliente dentro de ese payload —`source_text` y las `evidence`
//     de cada línea— es nivel 2 y NO VIVE EN LA COLUMNA `payload`: viaja cifrado en
//     el trío `literal_enc`/`literal_dek`/`literal_kek_id` (migración 0079). El
//     store lo saca al escribir y lo devuelve a su sitio al leer, así que el Payload
//     de este struct trae el texto SOLO cuando salió de una lectura y la retención
//     no ha vencido. Ver literal.go.
//   - Lo que identifique al COMPRADOR (RUT, dirección) sigue sin pasar por aquí:
//     vive en intake_buyer_data, y eso no ha cambiado.
type Revision struct {
	// IntakeID es la solicitud a la que pertenece la revisión.
	IntakeID string
	// RevisionNo es el correlativo POR SOLICITUD (1..N). Lo asigna el store al
	// escribir: el llamante NO lo elige, porque solo el store puede saber cuál es
	// el siguiente sin carrera.
	RevisionNo int
	// Kind es una de las clases RevisionKind*.
	Kind string
	// Payload es el estado completo de la revisión, versionado ({"version":1,…}).
	Payload json.RawMessage
	// RenderedText es el texto EXACTO que se le mandó al cliente en esta revisión,
	// si lo hubo. Vacío cuando la revisión no produjo mensaje.
	RenderedText string
	// CreatedBy es uno de los RevisionBy* (rol, nunca una persona).
	CreatedBy string
	// CreatedAt lo fija la BD al escribir.
	CreatedAt time.Time
	// LiteralPrunedAt es el instante en que la PODA PEREZOSA destruyó el literal de
	// esta revisión por haber vencido su retención (T3.5). Zero = no se ha podado,
	// que NO significa que tuviera literal: la mayoría de las revisiones no lo tiene
	// nunca. Existe para que ese caso no sea mudo — «aquí no hubo texto» y «el texto
	// se retuvo el plazo pactado y se destruyó» son dos hechos distintos.
	//
	// Solo lo puebla la LECTURA. InsertRevision lo devuelve siempre en cero: una
	// revisión recién escrita no puede estar podada.
	//
	// Lo que los stores deben cumplir con este campo (reglas de los tests viejos de
	// retención y del sello de la poda; R-07):
	//
	//   - La lectura que PODA publica ya el sello que acaba de poner: no puede decir
	//     «nunca tuvo texto» y que la siguiente lectura diga otra cosa.
	//   - Una revisión ya podada conserva su instante REAL: releerla no lo mueve ni
	//     vuelve a anunciar la poda. Si se moviera, dejaría de ser «cuándo se
	//     destruyó el texto» y pasaría a ser «la última vez que alguien miró».
	//   - Se sella UNA revisión, no la solicitud: las hermanas no se enteran.
	//   - Una revisión que nunca tuvo literal (las del carrito) no se sella ni se
	//     anuncia, por vieja que sea.
	//   - La poda se lleva el literal y deja INTACTA la interpretación estructurada.
	LiteralPrunedAt time.Time
}

// RevisionWriter es el puerto de ESCRITURA de revisiones. Lo satisfacen *Postgres
// (producción) y *MemoryStore (tests).
//
// Está separado del Store de lectura a propósito: quien escribe revisiones es el
// productor del acto (el proyector del carrito hoy; el pipeline LLM, el descarte y
// la revalidación después), y ninguno de ellos necesita —ni debe tener— la bandeja
// entera de solicitudes del tenant.
type RevisionWriter interface {
	// InsertRevision persiste la revisión asignándole el siguiente revision_no de
	// esa solicitud (1 para la primera; dos solicitudes distintas no comparten
	// numeración) y devuelve la revisión ya numerada y fechada (CreatedAt no es
	// cero). rev.RevisionNo se IGNORA en la entrada.
	//
	// Un rev.Payload vacío se rechaza con ErrEmptyRevisionPayload y no se escribe
	// nada.
	//
	// El Payload que DEVUELVE es el que fue a parar al almacén: ya sin el literal
	// del cliente (literal.go), que se guarda aparte.
	//
	// No es idempotente: dos llamadas con el mismo contenido crean dos revisiones,
	// porque dos actos iguales sobre el mismo pedido son dos hechos distintos y la
	// auditoría debe verlos. Quien necesite "una sola vez" lo resuelve aguas
	// arriba, no aquí.
	InsertRevision(ctx context.Context, rev Revision) (Revision, error)
}

// RevisionLine es una línea tal como queda CONGELADA dentro del payload de una
// revisión. No es intakes.Item: una revisión es una FOTO, y por eso guarda el
// precio con el que se negoció aunque el catálogo cambie después. Las etiquetas
// JSON (`sku`, `label`, `qty`, `unit_price`, en ese orden y SIN omitempty: una
// línea a cero serializa sus cuatro claves) son parte del contrato del payload
// versionado — cambiarlas exige subir RevisionPayloadVersion.
type RevisionLine struct {
	SKU       string  `json:"sku"`
	Label     string  `json:"label"`
	Qty       int     `json:"qty"`
	UnitPrice float64 `json:"unit_price"`
}

// Claves de la SEÑAL FEW-SHOT dentro del payload de una revisión `corrected`
// (Plan 044 · T4.4, D-044.11). Se declaran como constantes porque son contrato del
// CABLE —lo que la Ola 5 tendrá que buscar en el payload— y coinciden con las
// etiquetas JSON reales de CorrectionSignal, igual que LineKeyUnitPrice y
// PayloadKeyLines con las suyas.
//
// Eran `ClaveAsCorrection`, `ClaveCorrectsRevisionNo` y `ClaveCorrectsKind` en el
// paquete viejo; sus valores no cambian.
const (
	KeyAsCorrection       = "as_correction"
	KeyCorrectsRevisionNo = "corrects_revision_no"
	KeyCorrectsKind       = "corrects_kind"
)

// CorrectionSignal es lo que una corrección declarada del dueño deja escrito en su
// revisión: que ESTA edición fue una corrección del 044 (y no la edición manual
// rutinaria del 041) y CUÁL era el borrador que corrigió.
//
// 🔴 QUÉ ES Y QUÉ NO ES, PORQUE HOY NO TIENE CONSUMIDOR. D-044.11 quiere alimentar
// el generador de la voz de la dueña con «las últimas N cotizaciones aprobadas +
// ejemplos semilla de `tenant_content` ref `quote_style_examples`», y dice que «cada
// corrección del dueño es señal barata». Esa ref tiene CERO apariciones en el código
// de producción y su consumidor es de la OLA 5. Aquí la señal solo se PRODUCE y se
// GUARDA: nadie la lee todavía y marcar la casilla de T4.4 no significa que el
// few-shot funcione.
//
// POR QUÉ EL PAR Y NO SOLO LA MARCA. Un few-shot aprende de «esto propuso la máquina,
// esto dejó la persona»: con la marca sola habría que adivinar qué revisión se estaba
// corrigiendo, y el número que la identifica solo se conoce con el candado tomado,
// dentro de la escritura. Por eso lo resuelve el store y no el llamante.
//
// Los tres campos llevan `omitempty` y viven en la RAÍZ del payload: sin señal, el
// JSON de la revisión sale BYTE A BYTE como el que escribe el 041 hoy. Esa es la
// forma en que «cero regresión» se puede demostrar en vez de prometerse. Una señal
// vacía serializa `{}`.
type CorrectionSignal struct {
	// AsCorrection es la marca: el dueño declaró esta edición como corrección
	// (`"as_correction": true` en el PUT). Nunca se escribe `false` — su ausencia
	// ES el «no».
	AsCorrection bool `json:"as_correction,omitempty"`
	// CorrectsRevisionNo es el número de la revisión que había ANTES de ésta: el
	// borrador que el dueño corrigió. Cero —y por tanto ausente— cuando la
	// solicitud no tenía ninguna revisión, que es un caso raro pero posible.
	CorrectsRevisionNo int `json:"corrects_revision_no,omitempty"`
	// CorrectsKind es la clase de ese borrador (`interpreted` cuando lo escribió el
	// pipeline del 044, `cart` cuando venía del carrito numérico, `corrected` si es
	// la segunda pasada del dueño). Es lo que le permite a la Ola 5 quedarse solo
	// con las correcciones que enseñan algo sobre la interpretación del LLM.
	CorrectsKind string `json:"corrects_kind,omitempty"`
}

// linesRevisionPayload es la forma canónica del payload de las revisiones que
// retratan un conjunto de líneas con su total: RevisionKindCart (lo que armó el
// carrito), RevisionKindCorrected (cómo quedó tras la corrección del dueño) y
// RevisionKindApproved (lo que el dueño aprobó y cotizó, Plan 044 · T4.3). Es UNA
// forma y no tres porque es la misma pregunta —qué líneas y por cuánto— contestada
// por tres puertas; `kind` es lo que dice cuál fue.
//
// La señal va EMBEBIDA (json inline) y no en un objeto anidado: `as_correction` es
// el nombre que el contrato del PUT ya usa (D-044.48 §1), y repetirlo dentro de un
// objeto llamado «correction» lo diría dos veces. Los tres campos son opcionales, así
// que una señal VACÍA —la de las otras dos clases de revisión, y la del PUT del 041—
// serializa exactamente lo que se serializaba antes de esta tarea: nada.
//
// ⚠️ NO SUBE RevisionPayloadVersion, y es deliberado: son campos ADITIVOS y
// opcionales. Ningún lector de la v1 se rompe (no hay un solo DisallowUnknownFields
// sobre este payload en el repo), mientras que subir la versión la habría subido para
// las TRES clases —`cart` y `approved` incluidas, que no han cambiado en nada— y
// habría movido todos los golden files por un campo que la mayoría de las revisiones
// no lleva.
type linesRevisionPayload struct {
	Version int            `json:"version"`
	Total   float64        `json:"total"`
	Items   []RevisionLine `json:"items"`
	*CorrectionSignal
}

// CartRevisionPayload arma el payload de la revisión de CIERRE DE CARRITO
// (`{"version":1,"total":…,"items":[…]}`, design §3): las claves salen en ESE orden
// —`version` (siempre RevisionPayloadVersion), `total`, `items`— y cada línea con
// las etiquetas de RevisionLine.
//
// Una lista vacía o nil se serializa `[]` y no `null`: quien lea el payload debe
// poder distinguir "se cerró sin líneas" de "aquí no se registró la lista", y `null`
// no dice cuál de las dos es.
//
// Los números y los textos salen como los escribe encoding/json: `<`, `>`, `&` y
// U+2028 de un sku o de una etiqueta van escapados (`\u003c`…), y un precio enorme
// en notación exponencial (`1e+21`).
//
// Un total o un precio no representable en JSON (NaN, ±Inf) devuelve payload nil y
// el error "intakes: serializar payload de la revisión del carrito: " envolviendo
// (%w) el de encoding/json.
func CartRevisionPayload(total float64, lines []RevisionLine) (json.RawMessage, error) {
	return linesPayload("del carrito", total, lines, CorrectionSignal{})
}

// CorrectedRevisionPayload arma el payload de la revisión de CORRECCIÓN MANUAL
// del dueño (T4.10 / REQ-36): la misma forma versionada que la del carrito, para
// que quien lea la negociación entera compare revisión con revisión sin cambiar de
// parser a media lista.
//
// `signal` es la señal few-shot del 044 (T4.4) y viene VACÍA en el camino del 041:
// así el payload sale byte a byte como el de CartRevisionPayload con los mismos
// argumentos. Con señal, sus campos NO vacíos se añaden en la RAÍZ, detrás de
// `items` y en el orden `as_correction`, `corrects_revision_no`, `corrects_kind`;
// cada uno se omite por separado (la marca sin el par, o el par sin la marca, son
// payloads válidos). No sube RevisionPayloadVersion: son campos aditivos y
// opcionales.
//
// El error de serialización es "intakes: serializar payload de la revisión de la
// corrección manual: " envolviendo (%w) el de encoding/json.
func CorrectedRevisionPayload(total float64, lines []RevisionLine, signal CorrectionSignal) (json.RawMessage, error) {
	return linesPayload("de la corrección manual", total, lines, signal)
}

// ApprovedRevisionPayload arma el payload de la revisión de APROBACIÓN del dueño
// (Plan 044 · T4.3): la MISMA forma versionada que la del carrito y la de la
// corrección —byte a byte la de CartRevisionPayload con los mismos argumentos—,
// porque es la misma pregunta —qué líneas y por cuánto— contestada por una tercera
// puerta. Lo que distingue esta revisión de aquellas dos no es la forma del payload
// sino su `kind` y su `rendered_text`, que aquí NUNCA está vacío: la aprobación es,
// por definición, lo que se le dijo al cliente.
//
// El error de serialización es "intakes: serializar payload de la revisión de la
// aprobación: " envolviendo (%w) el de encoding/json.
func ApprovedRevisionPayload(total float64, lines []RevisionLine) (json.RawMessage, error) {
	return linesPayload("de la aprobación", total, lines, CorrectionSignal{})
}

// linesPayload serializa la forma compartida. `what` solo entra en el mensaje de
// error: sin él, un fallo de serialización no diría qué revisión se perdió.
//
// La señal se embebe SIEMPRE y no bajo un `if`: con los tres campos vacíos, los tres
// `omitempty` la borran entera del JSON. Una rama aquí sería un segundo sitio donde
// se decide si hay señal, y quien decide eso es la edición (edit.go) — una sola vez,
// que es lo que hace que una mutación en esa guarda se vea.
func linesPayload(what string, total float64, lines []RevisionLine, signal CorrectionSignal) (json.RawMessage, error) {
	if lines == nil {
		lines = []RevisionLine{}
	}
	raw, err := json.Marshal(linesRevisionPayload{
		Version:          RevisionPayloadVersion,
		Total:            total,
		Items:            lines,
		CorrectionSignal: &signal,
	})
	if err != nil {
		return nil, fmt.Errorf("intakes: serializar payload de la revisión %s: %w", what, err)
	}
	return raw, nil
}
