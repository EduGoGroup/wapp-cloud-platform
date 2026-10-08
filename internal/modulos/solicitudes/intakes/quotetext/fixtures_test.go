package quotetext_test

// fixtures_test.go — el pedido del caso Fusión y una respuesta buena del modelo, que
// comparten los tests del paquete.

import (
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes/quotetext"
)

// fusionItems son las líneas del caso Fusión, ya con los precios que puso la dueña:
// dos tortas y el envío (línea de sistema).
var fusionItems = []intakes.Item{
	{SKU: "TORTA-CHOC", Label: "Torta chocolate húmedo + crema choc. — 15 porciones", Qty: 1, UnitPrice: 2100},
	{SKU: "TORTA-VAI", Label: "Torta vainilla, ddl, merengue — 25-30 porciones", Qty: 1, UnitPrice: 2950},
	{SKU: intakes.ShippingSKU, Label: "Envío", Qty: 1, UnitPrice: 490},
}

// fusionTotal es la suma de las tres. Se escribe a mano y no se calcula para que el
// test no repita la aritmética que está probando.
const fusionTotal = 5540.0

// fusionDraft es el borrador del caso base.
func fusionDraft() quotetext.Draft { return quotetext.DraftOf(fusionItems) }

// modelText es una respuesta BUENA para el caso Fusión: dice los tres precios y el
// total, con marca de dinero, y no trae ningún número inventado.
const modelText = "Hola! Te paso el presupuesto:\n" +
	"Pastel para 15 personas, chocolate húmedo, relleno chocolate y oreo — $2100. Incluye impresiones no comestibles\n" +
	"El otro para 25-30 personas, vainilla, ddl y merengue — $2950\n" +
	"Envío — $490\n" +
	"Total $5540"
