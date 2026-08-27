package intakes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// approve.go — LA ACCIÓN APROBAR DEL DUEÑO (Plan 044 · Ola 4 · T4.3, D-044.49).
//
// Es el acto en el que un presupuesto deja de ser un borrador: el dueño escribe la
// cotización, la aprueba, y en ese mismo acto el cliente la recibe por WhatsApp, la
// solicitud pasa a `confirmed` y el puente CRM se entera. Casi todas las piezas ya
// existían sueltas —el envío por la sesión del intake (notifier.go), la plantilla de
// seña del tenant, la transición `pending_approval → confirmed` (status.go), la
// clase `approved` (revisions.go) y el encolado con el revision_no real
// (service.go)—. Lo que aporta esta tarea es la PUERTA, las precondiciones y, sobre
// todo, el ORDEN.
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 EL ORDEN DE LAS OPERACIONES, QUE ES LA DECISIÓN DE ESTE FICHERO
// ════════════════════════════════════════════════════════════════════════════
//
// `RenderedText` está documentado como «el texto EXACTO que se le mandó al cliente»
// (revisions.go). Con dos escrituras y un envío, algún fallo parcial tiene que ser
// posible, y elegir el orden es elegir CUÁL:
//
//	 1. componer el texto entero (cotización del dueño + plantilla de seña);
//	 2. la TRANSICIÓN, con compare-and-swap (SetStatus);
//	 3. la REVISIÓN `approved`, que se lleva ese texto;
//	 4. el ENVÍO al cliente;
//	 5. el empuje al CRM con el revision_no REAL.
//
// POR QUÉ EL TEXTO SE COMPONE ANTES DE ESCRIBIR NADA (1 antes que 2). Componer lee
// la config del tenant, que es I/O y puede fallar. Haciéndolo después, un fallo de
// lectura dejaría una solicitud CONFIRMADA cuya cotización no se llegó a armar.
// Componer antes hace que ese fallo no cueste nada: no se escribió todavía. Y es
// seguro respecto del total, que es lo único del texto que depende del estado: la
// entrada en `confirmed` no toca `total` (Store.UpdateStatus solo lo recalcula al
// entrar en `pending_approval`, por la línea de envío).
//
// POR QUÉ LA TRANSICIÓN VA PRIMERO (2 antes que 3). El compare-and-swap es lo que
// hace que de dos aprobaciones simultáneas gane UNA: la perdedora se lleva
// ErrConflict —o un TransitionError si la otra ya confirmó— y sale sin escribir ni
// mandar nada. Con la revisión primero, las dos habrían dejado su `approved` y el
// cliente habría recibido dos cotizaciones. Es el mismo criterio con el que este
// paquete cuelga el aviso del cliente de la ESCRITURA GANADORA y no de la petición
// (Service.notify).
//
// POR QUÉ EL ENVÍO VA DESPUÉS DE LA REVISIÓN (4 después de 3). Es la pregunta
// difícil, porque los dos órdenes mienten en algún fallo:
//
//   - enviar y luego escribir: si la escritura falla, se le mandó al cliente una
//     cotización que NO CONSTA. El dueño ve un 500, reintenta, y el reintento manda
//     un SEGUNDO mensaje. La revisión existe para una sola cosa —ser la defensa el
//     día que el cliente diga «a mí me dijeron $X» (D-041.25 §d)— y este orden
//     produce justo el caso que la destruye: dijimos algo y no hay rastro.
//   - escribir y luego enviar: si el envío falla, la revisión afirma un mensaje que
//     no llegó. Es un falso POSITIVO de esa defensa —inofensivo: el cliente no puede
//     reclamar lo que nunca leyó— y queda en el log con su command_id.
//
// Se elige escribir primero. La asimetría decide: el fallo del segundo orden es
// recuperable mirando el log; el del primero destruye la única prueba escrita.
//
// LOS FALLOS PARCIALES QUE QUEDAN, dichos sin adornos:
//
//   - falla (2) ⇒ no pasó NADA. 404/422/409/500 y el borrador sigue donde estaba.
//   - falla (3) ⇒ la solicitud queda CONFIRMADA sin su revisión y SIN mandar nada
//     al cliente (no se envía lo que no se registró). El dueño recibe un 500 que lo
//     dice con esas palabras, y su reintento chocará con un 422 porque la solicitud
//     ya está en el destino: tiene que escribirle al cliente a mano. Es el precio de
//     no tener las dos escrituras en una transacción, y se paga a conciencia — la
//     transición pasa por Service.SetStatus, que es la puerta con la que el resto del
//     sistema mueve estados, y meterla en una unidad de trabajo propia habría
//     duplicado la máquina de estados en un store nuevo.
//   - falla (4) ⇒ todo está escrito y el cliente no se enteró. El log lo dice con el
//     command_id (notifier.go); la respuesta al dueño sigue siendo 200, porque
//     notificar no puede tumbar una transición aplicada.
//   - falla (5) ⇒ el CRM no recibe esa revisión y no se reintenta (ver CRMPusher).
//
// ════════════════════════════════════════════════════════════════════════════
// LO QUE ESTA PUERTA NO HACE
// ════════════════════════════════════════════════════════════════════════════
//
// NO MATERIALIZA las líneas del borrador en `intake_items`. El pipeline del 044
// escribe sus líneas en la REVISIÓN y no en la tabla de líneas (stages/draft.go), y
// hacerlo aquí destruiría dato: el payload de una revisión `corrected` no lleva
// `customization` (revisionLinesOf), así que volcarlo sobre `intake_items` borraría
// el «sin cebolla» que quien prepara el pedido tiene que leer. Quien materializa es
// el `PUT …/items` del dueño, que es donde el precio se pone. Lo que hace esta
// puerta es NEGARSE a vender un presupuesto que no está materializado (ErrEmptyQuote)
// en vez de mandar una cotización de cero líneas.
//
// NO COBRA. El intake queda en `confirmed`, no en `deposit_requested`: la plantilla
// de seña se ADJUNTA al texto (D-044.49 §1), que es otra cosa que pedir la seña.

// ApprovableStatus es el ÚNICO estado desde el que se aprueba: el presupuesto por
// aprobar (D-044.49). Es más estrecho que la máquina de estados a propósito —
// CanTransition también admite `open → confirmed`, que es como cierra el carrito
// numérico—, y esa diferencia no es cosmética: una solicitud `open` es un carrito
// VIVO al que el cliente le está añadiendo líneas y que todavía no tiene su línea de
// envío (la pone la entrada en `pending_approval`). Aprobarla sería cerrarle el
// pedido al cliente por debajo y cotizarle un envío que nadie calculó.
//
// Es el gemelo exacto de EditableStatus (edit.go): la misma respuesta a «¿sobre qué
// estado opera el dueño?», dada por la otra puerta de la misma pantalla.
const ApprovableStatus = StatusPendingApproval

// separadorDeSeña es lo que va entre la cotización del dueño y la plantilla de seña
// del tenant: un renglón en blanco. Son dos mensajes en uno —lo que cuesta y cómo se
// paga— y en WhatsApp lo que separa dos ideas es una línea vacía, no un guion.
const separadorDeSeña = "\n\n"

// Las dos claves del contrato §7.4 que ESTA puerta lee de cada línea del borrador.
// Están aquí como literales por lo mismo que las del gate de publicapi: el contrato
// es del CABLE y no del struct de Go. Lo que impide que se desincronicen de
// stages.Linea es TestAprobar_LasClavesSonLasDelContrato, que las compara por
// reflexión contra las etiquetas JSON reales.
//
// `unit_price` es `*float64` en el productor y NO lleva `omitempty`: design §7.4
// escribe `"unit_price": null` para la línea que el dueño tiene que precificar, y un
// 0 significa otra cosa (un artículo de regalo, edit.go). Toda la precondición de
// esta tarea vive en esa diferencia.
const (
	ClaveLineaUnitPrice = "unit_price"
	ClaveLineaLabel     = "label"
)

// ErrEmptyQuoteText es la aprobación sin texto. El cuerpo `{"rendered_text": "…"}`
// es OBLIGATORIO (D-044.49): el dueño es el autor de lo que sale, y una aprobación
// muda dejaría al cliente con un pedido confirmado del que nunca le contaron el
// precio. No se sustituye por un texto genérico de la plataforma — el genérico es
// justo lo que esta puerta apaga.
var ErrEmptyQuoteText = errors.New("la aprobación no trae el texto de la cotización que se le manda al cliente")

// ErrEmptyQuote es la aprobación de una solicitud SIN LÍNEAS DE CLIENTE.
//
// No es una precondición decorativa: el borrador del pipeline LLM vive en la
// revisión y NO en `intake_items` (stages/draft.go), así que una solicitud recién
// interpretada tiene cero líneas hasta que el dueño las guarda con el `PUT …/items`.
// Sin esta guarda, aprobar ese borrador confirmaría un pedido de total 0 y le
// empujaría al CRM un documento vacío, en silencio y sin un solo error.
var ErrEmptyQuote = errors.New("la solicitud no tiene ni una línea de cliente que cotizar")

// ErrNoQuoteSender es el servicio sin canal para responderle al cliente. Es un ERROR
// y no un silencio —al revés que WithNotifier o WithCRMPusher, que son efectos
// best-effort— porque aquí el mensaje ES el acto: el botón se llama «Aprobar y
// responder», y aprobar sin poder responder confirmaría el pedido dejando al cliente
// sin enterarse. Falla ANTES de escribir nada.
var ErrNoQuoteSender = errors.New("intakes: el servicio no tiene canal para responderle al cliente (falta WithQuoteSender)")

// ErrNoRevisionWriter es el servicio cuyo store no sabe escribir revisiones. Mismo
// criterio: una aprobación sin rastro no es una aprobación (D-041.26), así que se
// corta antes de tocar el estado.
var ErrNoRevisionWriter = errors.New("intakes: el store cableado no sabe escribir revisiones (RevisionWriter)")

// PendingPriceLine es UNA línea del borrador que sigue sin precio, con lo justo para
// que el dueño la encuentre en su pantalla: la POSICIÓN en el borrador y la etiqueta.
//
// La posición y no el sku, y eso importa: una línea `unmatched` —la que el catálogo
// no reconoció, que es justo la que suele no tener precio— NO TIENE SKU (match.go).
// La posición es lo único que identifica una línea dentro de su revisión, y el orden
// de `lines` es contrato (§7.5).
type PendingPriceLine struct {
	Index int    `json:"index"`
	Label string `json:"label"`
}

// PendingPriceError es el rechazo de una aprobación cuyo borrador todavía tiene
// líneas sin precio (precondición de T4.3: «cero líneas sin precio, 400 si quedan»).
//
// Lleva TODAS las líneas y no la primera, por el mismo motivo que InvalidItemsError
// acumula sus defectos: quien tiene tres renglones sin precificar tiene que verlos
// los tres, no descubrirlos de uno en uno a base de aprobaciones rechazadas.
type PendingPriceError struct {
	Lines []PendingPriceLine
}

// Error implementa error.
func (e *PendingPriceError) Error() string {
	nombres := make([]string, 0, len(e.Lines))
	for _, l := range e.Lines {
		nombres = append(nombres, strconv.Itoa(l.Index)+":"+l.Label)
	}
	return fmt.Sprintf("el borrador tiene %d líneas sin precio (%s)", len(e.Lines), strings.Join(nombres, ", "))
}

// NotApprovableError es el rechazo de una aprobación sobre una solicitud que no está
// por aprobar. Lleva el estado actual porque quien lo recibe necesita saber qué pasó:
// alguien la movió, o nunca llegó a ser un presupuesto.
//
// Es un tipo DISTINTO de NotEditableError y de TransitionError aunque los tres hablen
// de estados, por lo mismo que aquellos dos son distintos entre sí (edit.go): si
// compartieran tipo, el llamante no podría distinguir «no puedes ir ahí» de «no
// puedes editar aquí» de «no puedes aprobar aquí».
type NotApprovableError struct {
	Status string
}

// Error implementa error.
func (e *NotApprovableError) Error() string {
	return fmt.Sprintf("una solicitud en %q no se puede aprobar", e.Status)
}

// LastRevision devuelve la revisión de número MÁS ALTO, que es el borrador vigente.
// El bool distingue «no hay revisiones» de «la primera».
//
// Se busca el máximo en vez de tomar el último elemento aunque los dos stores
// devuelvan la lista ordenada por revision_no: el orden es contrato de la CONSULTA
// (postgres.go: `ORDER BY r.revision_no`), y colgar de él una decisión que dice qué
// se puede vender lo convertiría en contrato del dominio sin que nadie lo hubiera
// declarado.
func LastRevision(revisions []Revision) (Revision, bool) {
	var out Revision
	found := false
	for _, rev := range revisions {
		if !found || rev.RevisionNo > out.RevisionNo {
			out, found = rev, true
		}
	}
	return out, found
}

// PendingPriceLines son las líneas SIN PRECIO del borrador vigente de la solicitud.
// Lista vacía ⇒ el presupuesto está entero y se puede aprobar.
//
// Mira SOLO la última revisión y no el histórico, y es lo correcto: las revisiones
// anteriores son el rastro de cómo se llegó aquí, no lo que se vende. Una línea que
// nació sin precio en la rev 1 y que el dueño precificó en la rev 2 está resuelta.
func PendingPriceLines(revisions []Revision) []PendingPriceLine {
	last, ok := LastRevision(revisions)
	if !ok {
		return nil
	}
	return LinesWithoutPrice(last.Payload)
}

// LinesWithoutPrice recorre el payload de UNA revisión y devuelve las líneas cuyo
// `unit_price` no es un número. Es PURA: sin BD, sin reloj y sin mutar la entrada.
//
// Lo que NO ENCUENTRA es tan importante como lo que encuentra, y no es un hueco:
//
//   - Un payload que no es un objeto JSON, o que no trae la clave `lines`, devuelve
//     nada. Es el caso de las revisiones `cart` y `corrected`, cuyo contrato es
//     {"version","total","items"} con `unit_price` float64 NO nullable
//     (linesRevisionPayload): por construcción no pueden tener una línea sin precio,
//     así que no hay nada que buscarles. La consecuencia es la que se quiere: en
//     cuanto el dueño guarda las líneas con el `PUT …/items`, la revisión vigente
//     pasa a ser `corrected` y la precondición queda satisfecha porque de verdad lo
//     está.
//   - Una línea que no es un objeto se salta. El payload lo escribe un productor
//     nuestro; si su forma cambió, lo que hay que arreglar no es esta función.
//
// Y lo que sí encuentra incluye la clave AUSENTE, no solo el `null` explícito: un
// `int`/`float` de Go no distingue «no vino la clave» de «vino 0», y aquí esa
// diferencia es la que separa «el dueño tiene que ponerle precio» de «es un regalo».
func LinesWithoutPrice(payload json.RawMessage) []PendingPriceLine {
	raiz, ok := comoObjeto(payload)
	if !ok {
		return nil
	}
	lineas, hay := comoLista(raiz[ClavePayloadLines])
	if !hay {
		return nil
	}

	var out []PendingPriceLine
	for i, cruda := range lineas {
		linea, esObjeto := comoObjeto(cruda)
		if !esObjeto {
			continue
		}
		if tienePrecio(linea[ClaveLineaUnitPrice]) {
			continue
		}
		out = append(out, PendingPriceLine{Index: i, Label: etiquetaDeLínea(linea)})
	}
	return out
}

// tienePrecio responde si el valor crudo de `unit_price` es un número. `null`, la
// clave ausente y cualquier cosa que no sea un número son «sin precio»: ante la duda,
// la línea está pendiente. El error de esa elección es un 400 que el dueño arregla
// poniendo el precio; el de la contraria sería cotizarle al cliente una línea a 0.
func tienePrecio(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var precio *float64
	if err := json.Unmarshal(raw, &precio); err != nil {
		return false
	}
	return precio != nil
}

// etiquetaDeLínea saca el `label` de una línea cruda. Vacío si no lo trae o no es una
// cadena: el índice ya identifica la línea, y una etiqueta inventada sería peor que
// ninguna.
func etiquetaDeLínea(linea map[string]json.RawMessage) string {
	crudo, hay := linea[ClaveLineaLabel]
	if !hay {
		return ""
	}
	var label string
	if err := json.Unmarshal(crudo, &label); err != nil {
		return ""
	}
	return label
}

// tieneLíneasDeCliente responde si queda algo que cotizar. Las líneas de LA
// PLATAFORMA (prefijo reservado: hoy el envío, D-041.11) NO cuentan: un presupuesto
// que solo lleva la línea de envío no es un pedido, es un envío de nada.
func tieneLíneasDeCliente(items []Item) bool {
	for _, it := range items {
		if !strings.HasPrefix(it.SKU, ReservedSKUPrefix) {
			return true
		}
	}
	return false
}

// approvedRevision arma la revisión que deja una aprobación, con el texto EXACTO que
// se le manda al cliente. Vive aquí y no en cada store por lo mismo que
// correctedRevision y revalidatedRevision: dos implementaciones que retrataran cosas
// distintas harían que los tests de handler dijeran algo falso sobre producción.
//
// La foto son las líneas PERSISTIDAS —todas, la de envío incluida—, así que `total`
// cuadra con la suma de `items`. Es la misma forma que la revisión de la corrección
// manual, y a propósito: quien lea la negociación entera compara revisión con
// revisión sin cambiar de parser a media lista.
//
// `created_by` es `owner` y no `system`: aquí sí decide una persona, y es el hecho
// central de INV-1. Sigue siendo un ROL y jamás una persona (CERO PII).
func approvedRevision(intakeID string, total float64, items []Item, renderedText string) (Revision, error) {
	payload, err := ApprovedRevisionPayload(total, revisionLinesOf(items))
	if err != nil {
		return Revision{}, err
	}
	return Revision{
		IntakeID:     intakeID,
		Kind:         RevisionKindApproved,
		Payload:      payload,
		RenderedText: renderedText,
		CreatedBy:    RevisionByOwner,
	}, nil
}

// Approve APRUEBA el presupuesto: le manda al cliente la cotización que escribió el
// dueño —con la plantilla de seña del tenant adjunta—, deja la solicitud en
// `confirmed` con su revisión `approved`, y empuja esa revisión al puente CRM.
//
// El orden y los fallos parciales están arriba, en la cabecera del fichero. Aquí solo
// hay que saber que las validaciones van TODAS antes de la primera escritura, y que
// el recurso se resuelve ANTES que el cuerpo (igual que en SetStatus y ReplaceItems):
// una solicitud ajena responde ErrNotFound y no revela por el código de error que
// existe (INV-8).
//
// 🔴 INV-1 — NINGÚN CAMINO AUTOMÁTICO LLEGA AQUÍ. Este método lo llama UN solo sitio,
// el handler de POST /api/v1/intakes/{id}/approve, y nada más: ni el motor de flujos,
// ni el pipeline del 044, ni un barrido. No hay reloj que apruebe y no hay LLM que
// responda al cliente. Lo que sostiene esa frase no es este comentario sino el
// candado sobre el AST de inv1_aprobar_ast_test.go, que barre los directorios de los
// flujos automáticos y CORTA si el barrido se queda sin mirar nada.
//
// Errores: ErrEmptyQuoteText (sin texto), ErrNotFound (no es del tenant),
// *NotApprovableError (no está por aprobar), *PendingPriceError (quedan líneas sin
// precio), ErrEmptyQuote (no hay nada que cotizar), *TransitionError / ErrConflict
// (alguien la movió entre la lectura y la escritura).
func (s *Service) Approve(ctx context.Context, tenantID, intakeID, renderedText string) (Detail, error) {
	if s.quotes == nil {
		return Detail{}, ErrNoQuoteSender
	}
	if s.revisions == nil {
		return Detail{}, ErrNoRevisionWriter
	}
	// TrimSpace solo para DECIDIR si hay texto: lo que se compone, se guarda y se
	// manda es el original byte a byte. Recortarlo sería reescribir lo que el dueño
	// escribió, y la revisión dejaría de ser lo que salió por el cable.
	if strings.TrimSpace(renderedText) == "" {
		return Detail{}, ErrEmptyQuoteText
	}

	current, err := s.store.Get(ctx, tenantID, intakeID)
	if err != nil {
		return Detail{}, err
	}
	if from := NormalizeStatus(current.Status); from != ApprovableStatus {
		return Detail{}, &NotApprovableError{Status: from}
	}
	if pendientes := PendingPriceLines(current.Revisions); len(pendientes) > 0 {
		return Detail{}, &PendingPriceError{Lines: pendientes}
	}
	if !tieneLíneasDeCliente(current.Items) {
		return Detail{}, ErrEmptyQuote
	}

	// (1) el texto ENTERO, antes de escribir nada. Ver la cabecera.
	texto := s.quotes.QuoteText(ctx, tenantID, current.Intake, renderedText)

	// (2) la transición. NoticeByCaller: el aviso genérico del estado destino
	// —«✅ Tu pedido quedó confirmado. Total $X»— NO sale por este camino (D-044.49
	// §1). Ya lo dice la cotización del dueño, con su detalle línea a línea, y el
	// genérico solo repetiría el número peor contado.
	updated, err := s.SetStatus(ctx, tenantID, intakeID, StatusConfirmed, NoticeByCaller)
	if err != nil {
		return Detail{}, err
	}

	// (3) el rastro, con el texto que se va a mandar.
	rev, err := approvedRevision(intakeID, updated.Total, current.Items, texto)
	if err != nil {
		return Detail{}, err
	}
	rev, err = s.revisions.InsertRevision(ctx, rev)
	if err != nil {
		return Detail{}, fmt.Errorf("intakes: la solicitud quedó CONFIRMADA pero su revisión approved no se escribió, "+
			"así que la cotización NO se envió y hay que mandarla a mano (intake_id=%s): %w", intakeID, err)
	}

	// (4) el mensaje al cliente. No devuelve error a propósito (notifier.go, regla 1):
	// una aprobación ya escrita no se deshace porque el teléfono esté apagado.
	s.quotes.SendQuote(ctx, tenantID, updated, texto)

	// (5) el puente CRM, con el revision_no REAL de la revisión que se acaba de
	// numerar. El detalle se compone con lo que ya está en la mano y NO con una
	// relectura: releer podría fallar después de haber mandado el mensaje, y
	// devolvería un 500 por una aprobación que ocurrió entera.
	detail := Detail{
		Intake:           updated,
		Items:            current.Items,
		Revisions:        conLaRevisión(current.Revisions, rev),
		BuyerDataPresent: current.BuyerDataPresent,
	}
	s.PushRevisionToCRM(ctx, tenantID, detail, rev.RevisionNo)
	return detail, nil
}

// conLaRevisión devuelve el histórico con la revisión nueva al final, sobre un slice
// NUEVO. La copia no es ceremonia: `append` sobre el slice que devolvió el store
// podría escribir en su array subyacente, y ese array es el que el store le entregó
// al llamante.
func conLaRevisión(historico []Revision, nueva Revision) []Revision {
	out := make([]Revision, 0, len(historico)+1)
	out = append(out, historico...)
	return append(out, nueva)
}
