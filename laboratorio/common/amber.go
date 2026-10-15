//go:build laboratorio

package common

import (
	"github.com/EduGoGroup/wapp-shared/intents"
	"github.com/EduGoGroup/wapp-shared/llm"
)

// El caso Ámbar: una pastelería que recibe por WhatsApp un pedido de dos tortas y un paquete de
// tequeños en tres mensajes. Es el MISMO caso del arnés de test/procesos (draftCatalog, draftBurst,
// p4AheadCatalog y los JSON de guion_test.go), copiado aquí porque un _test.go no se importa. Si el
// arnés cambia el caso, este fichero NO se entera: es un dato del laboratorio, no un contrato.

// LabTenant es el tenant con el que trabajan los escalones. Es un UUID fijo para que los
// almacenes que lo exigen lo acepten; no existe en ninguna base.
const LabTenant = "0a1b0000-0000-4000-8000-000000000058"

// Las ideas del caso: lo que P2 saca del hilo y la clave con la que P3 elige qué contestar.
const (
	AmberIdeaChocolate = "torta de chocolate"
	AmberIdeaVanilla   = "torta de vainilla"
	AmberIdeaTequenos  = "tequeños congelados"
)

// Las evidencias: subcadenas LITERALES de AmberBurst. P2 descarta la idea cuya evidencia no está en
// el texto, así que no son decorado.
const (
	AmberEvidenceDelivery  = "para el miércoles de la semana que viene"
	AmberEvidenceChocolate = "una torta sería con decoración infantil, de bizcocho húmedo de chocolate"
	AmberEvidenceVanilla   = "otra de bizcocho de vainilla que tenga lluvia de colores"
	AmberEvidenceTequenos  = "un paquete de tequeños congelados de 30"
)

// AmberP4ProposedDate es la fecha que el «modelo» propone en P4: la del caso original. La aritmética que
// manda es la de Go contra el message_ts (D-044.9), así que el borrador NO la lleva.
const AmberP4ProposedDate = "2026-07-22"

// Las respuestas del modelo bien portado del caso, etapa por etapa (valor por valor las del arnés).
const (
	AmberReplyP2 = `{"version":1,"wants":[` +
		`{"idea":"` + AmberIdeaChocolate + `","evidence":"` + AmberEvidenceChocolate + `"},` +
		`{"idea":"` + AmberIdeaVanilla + `","evidence":"` + AmberEvidenceVanilla + `"},` +
		`{"idea":"` + AmberIdeaTequenos + `","evidence":"` + AmberEvidenceTequenos + `"}],` +
		`"delivery_hint":{"text":"el miércoles de la semana que viene","evidence":"` + AmberEvidenceDelivery + `"}}`

	AmberReplyP3Chocolate = `{"version":1,"items":[{"product":"` + AmberIdeaChocolate + `","variant":"10 o 12 porciones",` +
		`"addon_candidates":["decoración infantil"],"notes":"bizcocho húmedo de chocolate con crema de chocolate",` +
		`"evidence":"` + AmberEvidenceChocolate + `"}]}`
	AmberReplyP3Vanilla = `{"version":1,"items":[{"product":"` + AmberIdeaVanilla + `","variant":"25 o 30 porciones",` +
		`"customizations":["lluvia de colores, dulce de leche y merengue"],"evidence":"` + AmberEvidenceVanilla + `"}]}`
	AmberReplyP3Tequenos = `{"version":1,"items":[{"product":"` + AmberIdeaTequenos + `","evidence":"` + AmberEvidenceTequenos + `"}]}`

	// AmberReplyP4 recoge las dos reglas de P4: «un paquete de 30» es qty 1 con package_size 30
	// (nunca qty 30), y package_size nunca vale 0.
	AmberReplyP4 = `{"version":1,"delivery_date":"` + AmberP4ProposedDate + `","items":[` +
		`{"product":"` + AmberIdeaChocolate + `","qty":1,"evidence":"` + AmberEvidenceChocolate + `","range":{"min":10,"max":12,"unit":"porciones"}},` +
		`{"product":"` + AmberIdeaVanilla + `","qty":1,"evidence":"` + AmberEvidenceVanilla + `","range":{"min":25,"max":30,"unit":"porciones"}},` +
		`{"product":"` + AmberIdeaTequenos + `","qty":1,"evidence":"` + AmberEvidenceTequenos + `","unit_kind":"package","package_size":30}]}`

	// AmberQuoteText es lo que la P5 del guion redacta.
	AmberQuoteText = "¡Hola! Te paso el presupuesto: torta de chocolate de 10 porciones, torta de vainilla de " +
		"25 porciones y un paquete de 30 tequeños congelados. Quedo atenta para confirmarte la entrega."
)

// AmberBurst es la ráfaga canónica: el caso en tres mensajes. El primero lleva AmberKeyword, la
// palabra que abre el evento del flujo.
func AmberBurst() []string {
	return []string{
		"Hola, buenas! Te quería pedir un presupuesto para el miércoles de la semana que viene",
		"Serían 2 tortas. Una torta sería con decoración infantil, de bizcocho húmedo de chocolate con crema de chocolate, de 10 o 12 porciones",
		"Y la otra de bizcocho de vainilla que tenga lluvia de colores, con dulce de leche y merengue, de 25 o 30 porciones. " +
			"También quería un paquete de tequeños congelados de 30",
	}
}

// AmberKeyword es la palabra que abre el evento del flujo del caso (draftKeyword).
const AmberKeyword = "presupuesto"

// AmberCatalogRef es la ref de tenant_content de la que el pipeline lee el catálogo.
const AmberCatalogRef = "catalogo"

// AmberCatalogItems es cuántos artículos trae AmberCatalog.
const AmberCatalogItems = 6

// AmberCatalog es el catálogo del caso en el contrato de POST /api/v1/catalog/import: la torta de
// chocolate se vende por presentaciones, la de vainilla y los tequeños a precio fijo, más tres
// artículos de SKU adversario (separador repetido, espacio U+00A0, dígitos no ASCII).
func AmberCatalog() map[string]any {
	item := func(code, sku, label string, price float64) map[string]any {
		return map[string]any{"code": code, "sku": sku, "label": label, "price": price}
	}
	choc := item("1", "TORTA-CHOC", "Torta de chocolate", 2100)
	choc["variants"] = []any{
		map[string]any{"code": "10", "label": "10 porciones", "price": 2100},
		map[string]any{"code": "12", "label": "12 porciones", "price": 2400},
		map[string]any{"code": "25", "label": "25 porciones", "price": 3900},
	}
	return map[string]any{
		"format": "wapp.catalog_import", "version": 1,
		"catalog": map[string]any{"categories": []any{
			map[string]any{"code": "1", "label": "Tortas", "items": []any{
				choc, item("2", "TORTA-VAIN", "Torta de vainilla", 3900),
			}},
			map[string]any{"code": "2", "label": "Congelados", "items": []any{
				item("1", "TEQ-30", "Tequeños congelados", 490),
			}},
			map[string]any{"code": "3", "label": "Panes", "items": []any{
				item("1", "PAN@@1", "Pan de masa madre", 120),
				item("2", "PAN 2", "Pan integral", 130),
				item("3", "PAN-١٢٣", "Pan ١٢٣", 140),
			}},
		}},
	}
}

// Las intenciones del caso (p4AheadCatalog): la que adelanta el cierre de la ventana y otra que no.
const (
	// IntentIntakeRequest es runtime.IntentIntakeRequest (internal/modulos/conversacion/runtime/aggregator.go).
	IntentIntakeRequest = "intake_request"
	IntentOpeningHours  = "consulta_horario"
)

// AmberIntents es el catálogo de intenciones del caso, ya en la forma POBRE que el prompt de P1
// necesita (la que produce flatten en intakeahead): sin params (D-044.20), un ejemplo cada una.
func AmberIntents() []llm.IntentSpec {
	return []llm.IntentSpec{
		{
			Name: IntentIntakeRequest, Description: "El cliente pide productos o un presupuesto",
			Examples: []llm.IntentExample{{Message: "quiero dos tortas para el sábado"}},
		},
		{
			Name: IntentOpeningHours, Description: "El cliente pregunta por el horario de atención",
			Examples: []llm.IntentExample{{Message: "¿a qué hora cierran?"}},
		},
	}
}

// AmberP1Input arma la entrada de P1 como la arma el adelanto de ventana: el texto, AmberIntents y la
// etiqueta reservada de lo desconocido.
func AmberP1Input(text string) llm.ClassifyRequestInput {
	return llm.ClassifyRequestInput{
		Text:         text,
		Catalog:      AmberIntents(),
		UnknownLabel: intents.ReservedUnknown,
	}
}
