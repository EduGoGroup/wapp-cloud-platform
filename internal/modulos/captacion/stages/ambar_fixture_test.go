package stages_test

// ════════════════════════════════════════════════════════════════════════════
// EL FIXTURE DEL CASO AMBAR
//
// 🔴 CALIDAD C: ESTE TEXTO LO REDACTÓ CLAUDE. NO ES EL TEXTO REAL DE AMBAR.
//
// El texto literal de aquella solicitud no existe transcrito en ninguna parte: las
// fuentes solo la DESCRIBEN. Este fixture es un DETECTOR DE REGRESIÓN —si el anclaje
// deja de funcionar, los tests se ponen rojos— y NO ACREDITA ACIERTO: ninguna medida de
// calidad del modelo puede salir de aquí.
//
// Lleva los rótulos `### MENSAJES …###` y el prefijo `cliente:` porque ESA es la forma
// real de lo que las etapas reciben: el literal lo compone el flush de la ventana, con
// sus delimitadores y una línea por mensaje. Así el anclaje tiene que aguantar lo que
// aguanta en producción: que una evidencia legítima cruce un salto de línea.
//
// Es LA MISMA cadena que `casebank.AmbarCaseText`; lo vigila ambar_seed_test.go.
// ════════════════════════════════════════════════════════════════════════════

// ambarText es la solicitud larga de las 9:55 (caso Fusión), ya compuesta.
const ambarText = "### MENSAJES DE LA CONVERSACIÓN (literal, en orden) ###\n" +
	"cliente: Hola, buenas! Te quería pedir un presupuesto para el miércoles de la semana que viene\n" +
	"cliente: Serían 2 tortas. Una torta sería con decoración infantil, de bizcocho húmedo de chocolate " +
	"con crema de chocolate, de 10 o 12 porciones\n" +
	"cliente: Y la otra de bizcocho de vainilla que tenga lluvia de colores, con dulce de leche y " +
	"merengue, de 25 o 30 porciones\n" +
	"cliente: También quería un paquete de tequeños congelados de 30\n" +
	"cliente: Me pasas precio porfa?\n" +
	"### FIN DE LOS MENSAJES ###"

// Las cuatro evidencias que el modelo DEBERÍA copiar del fixture (design §7.1).
//
// ⚠️ chocolateCakeEvidence empieza en minúscula donde el fixture dice «Una torta»: está
// a propósito. Es el caso que justifica que la comparación normalice mayúsculas, y el
// que se rompería si alguien «simplificara» el anclaje a un strings.Contains crudo.
const (
	chocolateCakeEvidence = "una torta sería con decoración infantil, de bizcocho húmedo de chocolate"
	vanillaCakeEvidence   = "otra de bizcocho de vainilla que tenga lluvia de colores"
	tequenosEvidence      = "un paquete de tequeños congelados de 30"
	deliveryEvidence      = "para el miércoles de la semana que viene"
)

// inventedEvidence NO aparece en el fixture: es del mismo dominio y suena creíble, que
// es justo la clase de alucinación que el anclaje existe para cazar.
const inventedEvidence = "y también dos bandejas de pasapalos surtidos para veinte personas"
