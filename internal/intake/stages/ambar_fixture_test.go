package stages_test

// ════════════════════════════════════════════════════════════════════════════
// EL FIXTURE DEL CASO AMBAR (Plan 044 · T2.2)
//
// 🔴 CALIDAD C: ESTE TEXTO LO REDACTÓ CLAUDE. NO ES EL TEXTO REAL DE AMBAR.
//
// El criterio de T2.2 pide «test con el texto real de Ambar (fixture)». Ese texto NO
// EXISTE TRANSCRITO EN NINGUNA PARTE del repositorio de documentación —verificado con
// `grep` sobre `docs/` entera, incluido el documento fuente
// (`brainstorm/2026-08-03-…/08-caso-fusion-anatomia-del-flujo-real.md:12`)—: las fuentes
// solo DESCRIBEN la solicitud en tercera persona. Lo de abajo está redactado a partir de
// esa descripción y de las evidencias de ejemplo de `design.md` §7.1.
//
// Consecuencia, dicha aquí para que nadie lea dentro de dos meses «probado con el caso
// real» y sea falso: este fixture es un DETECTOR DE REGRESIÓN —si el anclaje deja de
// funcionar, estos tests se ponen rojos— y NO ACREDITA ACIERTO. Ninguna medida de
// calidad del modelo puede salir de aquí. Es la misma calidad C del lote de eval de
// T1.8-3, y por el mismo motivo.
//
// # CÓMO SE SUSTITUYE POR EL TEXTO REAL EL DÍA QUE APAREZCA
//
// Se cambia ESTA CONSTANTE y nada más. Las cuatro `evidencia*` de abajo son frases del
// fixture, así que al pegar el texto real hay que ajustarlas para que vuelvan a serlo:
// si alguna deja de ser subcadena, los tests se ponen rojos —que es exactamente lo que
// tiene que pasar— y se corrige la evidencia, nunca el anclaje.
//
// # POR QUÉ LLEVA LOS RÓTULOS `### MENSAJES …###` Y EL PREFIJO `cliente:`
//
// Porque ESA es la forma real de lo que P2 recibe: el literal lo compone
// `runtime.ComposeSourceText` al cerrar la ventana (T1.4) y sale con sus delimitadores
// y una línea por mensaje, `cliente: …` / `negocio: …`. Un fixture de una sola línea
// probaría un texto que en producción no existe, y además se saltaría lo que el
// anclaje tiene que aguantar: que una evidencia legítima cruce un salto de línea.
// ════════════════════════════════════════════════════════════════════════════

// textoAmbar es la solicitud larga de las 9:55 (caso Fusión, doc 08), ya compuesta.
const textoAmbar = "### MENSAJES DE LA CONVERSACIÓN (literal, en orden) ###\n" +
	"cliente: Hola, buenas! Te quería pedir un presupuesto para el miércoles de la semana que viene\n" +
	"cliente: Serían 2 tortas. Una torta sería con decoración infantil, de bizcocho húmedo de chocolate " +
	"con crema de chocolate, de 10 o 12 porciones\n" +
	"cliente: Y la otra de bizcocho de vainilla que tenga lluvia de colores, con dulce de leche y " +
	"merengue, de 25 o 30 porciones\n" +
	"cliente: También quería un paquete de tequeños congelados de 30\n" +
	"cliente: Me pasas precio porfa?\n" +
	"### FIN DE LOS MENSAJES ###"

// Las cuatro evidencias que el modelo DEBERÍA copiar del fixture. Son literalmente las
// de `design.md` §7.1, y el fixture está escrito para contenerlas.
//
// ⚠️ `evidenciaTortaChocolate` empieza en minúscula donde el fixture dice «Una torta»:
// está a propósito. Es el caso que justifica que la comparación normalice mayúsculas y
// el que se rompería si alguien «simplificara» el anclaje a un strings.Contains crudo.
const (
	evidenciaTortaChocolate = "una torta sería con decoración infantil, de bizcocho húmedo de chocolate"
	evidenciaTortaVainilla  = "otra de bizcocho de vainilla que tenga lluvia de colores"
	evidenciaTequenos       = "un paquete de tequeños congelados de 30"
	evidenciaEntrega        = "para el miércoles de la semana que viene"
)

// evidenciaInventada NO aparece en el fixture: es del mismo dominio y suena
// perfectamente creíble, que es justo la clase de alucinación que el anclaje existe
// para cazar (el «pedido 887» del clasificador del Edge). Si algún día se pega el texto
// real de Ambar y esta frase apareciera en él, el test de descarte se pondría rojo y
// habría que cambiarla: eso es la red funcionando, no un defecto.
const evidenciaInventada = "y también dos bandejas de pasapalos surtidos para veinte personas"
