// Porta internal/publicapi/intakes.go @ ed60c24 (1073 líneas: intakeDTO e intakeItemDTO, líneas
// 59-115; intakeDetailResponse e intakeRevisionDTO, líneas 127-223; toIntakeDetailResponse,
// líneas 329-367; toIntakeDTO, líneas 938-963).
//
// intakes_dto.go — LAS PROYECCIONES AL WIRE DE UNA SOLICITUD: la cabecera, la línea, la revisión
// y el detalle. Es un trozo de intakes.go partido por tema (05 E-13). Van aparte de los handlers
// porque las comparten todos: la cabecera la responden G1, G2 y G3, y el detalle G2, G4, G5 y G6.
//
// No exporta nada y nació con el verde (05 E-4, P6): la forma de los cuerpos está en el contrato
// de MountIntakes y la prueba intakes_dto_test.go a través de las rutas.

package apipublica

import (
	"encoding/json"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// intakeDTO es la proyección al wire de una cabecera de solicitud.
//
// contact_id viaja OPACO TAL CUAL está en BD (INV-04 / ADR-0010): es un
// identificador sin número ni JID, y esta capa no lo descifra ni lo enriquece con
// nombre o teléfono. tenant_id NO viaja: siempre es el del token.
// `customer_note` es la indicación del cliente para todo el pedido (D-041.19).
// Viaja SIEMPRE, también vacía, por la misma razón que `customization` en la
// línea: quien consume tiene que poder pintar la cabecera sin preguntarse si la
// clave falta porque el cliente no indicó nada o porque este servidor todavía no
// la publica. Va en el DTO de la cabecera y no solo en el del detalle, así que la
// lista la trae igual: es un campo de la solicitud, y una bandeja que muestra
// «dejarlo en portería» junto al pedido es exactamente para lo que existe.
//
// `overdue` es la MARCA DERIVADA del plazo del presupuesto (Plan 044 · T4.5,
// REQ-25): «este presupuesto lleva más de intakes.QuoteDeadline esperando la
// decisión del dueño». Se calcula al leer, en esta misma proyección, y no tiene
// columna en la base ni estado propio.
//
// 🔴 `overdue: true` NO CAMBIA `status`, y quien pinte esto no puede tratarlo como
// si lo cambiara: la solicitud sigue en `pending_approval`, con sus mismos
// `allowed_transitions`, y la salida sigue siendo humana —aprobar, rechazar o
// descartar—. Nada muere por tiempo (D-041.16). Por eso el campo NO se llama
// `expired`: `expired` es un ESTADO legado y terminal del que ya nadie entra ni
// sale, y llamar igual a las dos cosas invitaría a confundir «lleva mucho
// esperando» con «está muerto».
//
// Viaja SIEMPRE, también en `false`, por la misma razón que `customer_note` y
// `customization`: quien consume tiene que poder pintar la fila sin preguntarse si
// la clave falta porque el presupuesto está en plazo o porque este servidor todavía
// no la publica.
type intakeDTO struct {
	ID           string  `json:"id"`
	ContactID    string  `json:"contact_id"`
	SessionID    string  `json:"session_id"`
	Status       string  `json:"status"`
	Total        float64 `json:"total"`
	CustomerNote string  `json:"customer_note"`
	Overdue      bool    `json:"overdue"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
}

// intakeItemDTO es una línea de la solicitud. sku/label son códigos del catálogo
// del tenant (dato de negocio), NUNCA PII.
//
// `customization` es la personalización no facturable de la línea (D-041.17): el
// «sin cebolla». Viaja SIEMPRE, también vacía (sin `omitempty`), y eso es
// deliberado: quien consume el detalle —la consola del dueño, el puente del CRM—
// tiene que poder pintar la línea sin preguntarse si la clave falta porque no hay
// personalización o porque este servidor todavía no la publica.
type intakeItemDTO struct {
	SKU           string  `json:"sku"`
	Label         string  `json:"label"`
	Customization string  `json:"customization"`
	Qty           int     `json:"qty"`
	UnitPrice     float64 `json:"unit_price"`
}

// intakeDetailResponse es el contrato de GET /api/v1/intakes/{id}: la cabecera
// (campos promovidos), sus líneas y los destinos a los que la solicitud puede ir
// desde donde está.
//
// `allowed_transitions` sale de la MISMA fuente que el cuerpo del 422
// (intakes.AllowedTransitions) y en el mismo orden determinista. Sin él, una
// consola que quiera pintar el selector de cambio de estado tendría dos salidas y
// las dos son malas: provocar un 422 para averiguar qué puede hacer, o duplicar el
// mapa de estados en el cliente y desincronizarlo en cuanto la Ola 4 lo amplíe.
//
// Un estado TERMINAL devuelve `[]`, nunca `null`: "no hay acciones" y "no sé" son
// respuestas distintas y la UI pinta cosas distintas con cada una.
//
// `revisions` es la NEGOCIACIÓN auditada (ADR-0031 §3, tabla intake_revisions de la
// migración 0045). Va aparte de `items` porque son cosas distintas: `items` es lo
// VENDIDO y `revisions` el rastro de cómo se llegó a ello. Un `[]` aquí ya no es
// una respuesta fingida como lo habría sido antes de que la tabla existiera:
// significa literalmente "esta solicitud no tiene revisiones registradas".
// `buyer_data_present` (D-041.13, T4.5) es TODO lo que esta API publica del
// checklist del comprador: un booleano. Los valores —RUT, dirección de entrega—
// están cifrados en public.intake_buyer_data y su descifrado está CUSTODIADO: llega
// con su consumidor real (el puente del Plan 042 o una pantalla que lo justifique),
// no por si acaso.
//
// Sirve para lo que una consola necesita de verdad sin exponer a nadie: saber si el
// pedido trae los datos que el dueño pidió antes de ponerse a prepararlo. Quien
// añada aquí el contenido está deshaciendo la tarea entera, no ampliando un DTO.
type intakeDetailResponse struct {
	intakeDTO
	Items              []intakeItemDTO     `json:"items"`
	Revisions          []intakeRevisionDTO `json:"revisions"`
	AllowedTransitions []string            `json:"allowed_transitions"`
	BuyerDataPresent   bool                `json:"buyer_data_present"`
}

// intakeRevisionDTO es una revisión al wire.
//
// `payload` viaja como JSON CRUDO (json.RawMessage), no como un objeto tipado: su
// forma depende de `kind` y del `version` que lleva dentro, y tiparlo aquí
// obligaría a esta capa a conocer todas las formas presentes y futuras — incluida
// la del Plan 044, que aún no existe. El `version` dentro del blob es lo que
// permite a un cliente saber si entiende lo que lee.
//
// `created_by` es un ROL, nunca una persona, y ni el payload ni el texto renderizado
// llevan los datos del COMPRADOR (RUT, dirección: ésos viven cifrados en
// intake_buyer_data y de ahí no salen por esta ruta).
//
// 🔧 LO QUE SÍ PUEDE VIAJAR AQUÍ DESDE T3.5: el TEXTO LITERAL del cliente. Una
// revisión `interpreted` del pipeline LLM trae dentro del payload el `source_text`
// que el cliente escribió y las `evidence` que sostienen cada línea. En reposo van
// CIFRADOS con KEK y fuera de la columna `payload` (migración 0079); el store los
// descifra al leer, así que llegan a este DTO en claro — y ESO ES EL REQUISITO, no
// una fuga: sin el original al lado de la interpretación, el dueño no puede validar
// que el LLM entendió bien ni pedir un re-análisis (D-044.13 / D-14, ADR-0034
// §Decisión 2). Lo que protege esa entrega es la MISMA puerta que todo lo demás de
// esta ruta: identidad válida y tenant propio (INV-8). Vencido el TTL de retención
// del tenant, el literal ya no está —lo podó la lectura anterior— y el payload llega
// con su interpretación estructurada sola, que es el comportamiento correcto.
type intakeRevisionDTO struct {
	RevisionNo   int             `json:"revision_no"`
	Kind         string          `json:"kind"`
	Payload      json.RawMessage `json:"payload"`
	RenderedText string          `json:"rendered_text,omitempty"`
	CreatedBy    string          `json:"created_by,omitempty"`
	CreatedAt    string          `json:"created_at"`
	// LiteralPrunedAt es el instante RFC3339 UTC en que la RETENCIÓN destruyó el
	// texto literal del cliente de esta revisión. La clave está AUSENTE mientras no
	// se haya podado (D-044.52 §3).
	//
	// 🔑 QUÉ PREGUNTA CONTESTA. Sin él, un `source_text` que no viene tiene DOS
	// causas que el consumidor no puede separar y que significan cosas opuestas
	// para el dueño: «esta revisión nunca tuvo texto» —todas las del carrito
	// numérico, y las del pipeline cuyo literal venía vacío— y «lo tuvo, se retuvo
	// el plazo pactado y se destruyó». Con las dos calladas, la consola solo puede
	// pintar un hueco y dejar que el dueño decida por su cuenta si el sistema le
	// perdió el dato. Los tres casos salen de DOS claves:
	//
	//	payload.source_text presente ................. hay literal
	//	ausente + literal_pruned_at ausente .......... nunca lo hubo
	//	ausente + literal_pruned_at presente ......... se podó, y cuándo
	//
	// 🔑 POR QUÉ UN INSTANTE Y NO UN BOOLEANO. Porque es lo que el dominio AFIRMA:
	// intakes.Revision.LiteralPrunedAt ES el `literal_pruned_at` de la fila —el
	// momento REAL de la destrucción, que la poda sella UNA vez y no vuelve a mover
	// (pruneLiteralQuery)—. Un `literal_purged: true` sería una derivación suya que
	// tira la mitad del dato sin comprar nada: el booleano se obtiene igual mirando
	// si la clave está, así que quien solo quiera los tres casos no paga por el
	// instante, y quien tenga que contestar «¿cuándo se destruyó el texto de mi
	// cliente?» —una pregunta de retención, no de pantalla— con el booleano no
	// puede.
	//
	// Va FUERA del payload y hermano de `created_at`, no dentro: el payload es la
	// interpretación de lo que el cliente pidió, y esto es un hecho SOBRE la
	// revisión. Estar fuera es además lo que lo deja al margen del gate de
	// `llm_intake`, que filtra claves DEL payload (ver intakes_llm_gate.go).
	LiteralPrunedAt string `json:"literal_pruned_at,omitempty"`
}

// intakeToDTO (toIntakeDTO en la cara vieja) proyecta una cabecera al wire. El estado ya viene
// NORMALIZADO del dominio (el `closed` legado del módulo cart sale como `confirmed`).
//
// `at` es el instante contra el que se decide la marca `overdue`, y entra como
// ARGUMENTO en vez de leerse aquí con time.Now() por la misma razón por la que
// Summary recibe su reloj (BuildSummary): la regla es una comparación de tiempos, y
// una proyección que consulta el reloj por dentro solo se puede probar esperando.
// Los llamantes pasan el reloj de IntakesDeps — la marca es «ahora mismo», no un dato
// guardado.
//
// La REGLA no vive aquí: la aplica intakes.Overdue, que es la MISMA función que usa
// el pre-filtro del recordatorio del plazo. Dos copias de «cuándo está vencido»
// serían una bandeja que marca lo que el recordatorio no avisa.
func intakeToDTO(in intakes.Intake, at time.Time) intakeDTO {
	return intakeDTO{
		ID:           in.ID,
		ContactID:    in.ContactID,
		SessionID:    in.SessionID,
		Status:       in.Status,
		Total:        in.Total,
		CustomerNote: in.CustomerNote,
		Overdue:      intakes.Overdue(in, at),
		CreatedAt:    in.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:    in.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// intakeToDetailResponse (toIntakeDetailResponse en la cara vieja) proyecta la solicitud completa
// al wire. Es UN punto y no dos porque la edición manual (T4.10) responde EXACTAMENTE el mismo
// cuerpo que el detalle: la consola repinta con lo que le devuelve el PUT, sin un segundo GET, y
// dos proyecciones separadas empezarían a divergir en el primer campo nuevo.
func intakeToDetailResponse(detail intakes.Detail, at time.Time) intakeDetailResponse {
	items := make([]intakeItemDTO, 0, len(detail.Items))
	for _, it := range detail.Items {
		items = append(items, intakeItemDTO{
			SKU: it.SKU, Label: it.Label, Customization: it.Customization,
			Qty: it.Qty, UnitPrice: it.UnitPrice,
		})
	}
	revisions := make([]intakeRevisionDTO, 0, len(detail.Revisions))
	for _, rev := range detail.Revisions {
		revisions = append(revisions, intakeRevisionDTO{
			RevisionNo:   rev.RevisionNo,
			Kind:         rev.Kind,
			Payload:      rev.Payload,
			RenderedText: rev.RenderedText,
			CreatedBy:    rev.CreatedBy,
			CreatedAt:    rev.CreatedAt.UTC().Format(time.RFC3339),
			// formatInstant y no Format a secas: el cero de time.Time tiene que
			// salir como cadena VACÍA para que `omitempty` borre la clave. Un
			// Format del cero publicaría "0001-01-01T00:00:00Z" en toda revisión
			// sin podar —que es una fecha que no significa nada— y volvería a
			// hacer indistinguible el caso que este campo viene a separar.
			LiteralPrunedAt: formatInstant(rev.LiteralPrunedAt),
		})
	}
	return intakeDetailResponse{
		intakeDTO: intakeToDTO(detail.Intake, at),
		Items:     items,
		Revisions: revisions,
		// detail.Status ya viene normalizado del dominio: una solicitud
		// guardada como `closed` ofrece los destinos de `confirmed`.
		AllowedTransitions: intakes.AllowedTransitions(detail.Status),
		BuyerDataPresent:   detail.BuyerDataPresent,
	}
}
