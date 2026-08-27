package casebank

import "encoding/json"

// semilla.go — EL PRIMER CASO DEL BANCO: la solicitud de Ambar (caso Fusión).
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 CALIDAD C: ESTE TEXTO LO REDACTÓ CLAUDE. NO ES EL TEXTO REAL DE AMBAR.
// ════════════════════════════════════════════════════════════════════════════
//
// El texto literal de aquella solicitud NO EXISTE TRANSCRITO EN NINGUNA PARTE del
// repositorio de documentación —verificado con `grep` sobre `docs/` entera,
// incluido el documento fuente
// (`brainstorm/2026-08-03-…/08-caso-fusion-anatomia-del-flujo-real.md:12-26`)—:
// las fuentes solo DESCRIBEN la solicitud en tercera persona, y el propio plan lo
// admite (`tasks.md:4073`, «Fixture del caso Ambar en CALIDAD C (redactado; el
// texto real no existe transcrito)»).
//
// Consecuencia, dicha aquí y REPETIDA DENTRO DE LA FILA QUE SE SIEMBRA (clave
// `_procedencia` de `expected`), para que nadie lea dentro de seis meses «el
// banco tiene el caso real» y sea falso:
//
//	este caso es un DETECTOR DE REGRESIÓN y NO ACREDITA ACIERTO. Ninguna medida
//	de calidad del modelo puede salir de aquí. Un banco de evaluación cuya
//	primera fila es material redactado mide contra un texto que nadie escribió.
//
// # POR QUÉ EL AVISO VA EN `expected` Y NO DENTRO DE `source_text`
//
// Porque `source_text` es EXACTAMENTE lo que un modelo vería, y meterle una línea
// de cabecera que en producción no existe cambiaría el estímulo del eval: se
// estaría midiendo al pipeline sobre un texto que ningún cliente manda. El aviso
// tiene que viajar con la fila, sí, pero por el carril de los metadatos.
//
// # ES LA MISMA CADENA QUE EL FIXTURE DE `stages`
//
// `internal/intake/stages/ambar_fixture_test.go` tiene esta misma constante
// (`textoAmbar`) desde T2.2, y no se puede importar porque vive en un `_test.go`.
// La copia es deliberada y NO queda a la buena fe: `stages/ambar_semilla_test.go`
// exige que las dos cadenas sean idénticas, así que el día que una cambie sin la
// otra, ese test se pone rojo.
//
// # CÓMO SE SUSTITUYE POR EL TEXTO REAL EL DÍA QUE APAREZCA
//
// Se cambia esta constante (y la gemela de `stages`, o el candado avisa), se
// vuelve a sembrar contra un tenant limpio y se BORRA a mano la fila vieja: la
// siembra es idempotente por el texto, así que un texto nuevo entra como caso
// nuevo y el redactado se queda al lado si nadie lo retira.

// TextoCasoAmbar es la solicitud larga de las 9:55 (caso Fusión, doc 08), ya
// compuesta por `runtime.ComposeSourceText` — con sus delimitadores y una línea
// por mensaje, que es la forma REAL de lo que P2 recibe.
//
// 🔴 NO CONTIENE NI UN NOMBRE, NI UN TELÉFONO, NI UN JID: está redactado así a
// propósito, y por eso `Anonimizar(TextoCasoAmbar)` lo devuelve INTACTO. Ese es
// el test que importa de la semilla, y no es una tautología: un anonimizador de
// teléfonos demasiado goloso se comería «10 o 12 porciones», «25 o 30» y «un
// paquete de tequeños congelados de 30», que son justo las cantidades sobre las
// que este caso evalúa a P4.
const TextoCasoAmbar = "### MENSAJES DE LA CONVERSACIÓN (literal, en orden) ###\n" +
	"cliente: Hola, buenas! Te quería pedir un presupuesto para el miércoles de la semana que viene\n" +
	"cliente: Serían 2 tortas. Una torta sería con decoración infantil, de bizcocho húmedo de chocolate " +
	"con crema de chocolate, de 10 o 12 porciones\n" +
	"cliente: Y la otra de bizcocho de vainilla que tenga lluvia de colores, con dulce de leche y " +
	"merengue, de 25 o 30 porciones\n" +
	"cliente: También quería un paquete de tequeños congelados de 30\n" +
	"cliente: Me pasas precio porfa?\n" +
	"### FIN DE LOS MENSAJES ###"

// NombresDelCaso son los nombres propios que rodean a este caso en la
// documentación —la clienta, la dueña del negocio y el negocio— y que hay que
// retirar si alguna vez se pega el texto REAL.
//
// ⚠️ HOY NINGUNO APARECE EN `TextoCasoAmbar`, y eso NO hace inútil la lista: es
// la que se le pasa al anonimizador al sembrar, así que el día que la constante
// se sustituya por la transcripción de verdad —donde sí es probable que Ambar se
// presente por su nombre— la retirada ya está puesta y no depende de que alguien
// se acuerde.
func NombresDelCaso() []string {
	return []string{"Ambar", "Herminia", "Fusión", "Fusion"}
}

// procedencia es el aviso de calidad que VIAJA DENTRO DE LA FILA. Va en
// `expected` bajo una clave con guion bajo delante para que se distinga de las
// claves de la interpretación curada.
const procedencia = `{
  "fuente": "REDACTADO por Claude a partir de la descripción en tercera persona de docs/brainstorm/2026-08-03-.../08-caso-fusion-anatomia-del-flujo-real.md:12-26",
  "calidad": "C",
  "es_texto_real_del_cliente": false,
  "aviso": "Este caso es un DETECTOR DE REGRESION y NO ACREDITA ACIERTO: ninguna medida de calidad del modelo puede salir de el. El texto literal de la solicitud de Ambar no existe transcrito en ninguna parte (Plan 044, tasks.md:4073).",
  "plan": "044 · Ola 5 · T5.3"
}`

// EsperadoCasoAmbar es la interpretación CURADA A MANO contra la que se compara
// lo que produzca el pipeline. Es la de `design.md` §7.1-§7.3, la misma que los
// tests de P2/P3/P4 usan como salida de un modelo bien portado.
//
// 🔴 NO LLEVA FECHA ABSOLUTA, y esa ausencia es una decisión. P4 calcula la fecha
// contra `message_ts` (D-044.9), y esta tabla NO guarda `message_ts`: escribir
// aquí «2026-07-22» convertiría la interpretación correcta en algo que solo es
// cierto si el caso se replica un 13 de julio de 2026. Lo que se cura es la PISTA
// TEXTUAL —«el miércoles de la semana que viene»—, que es lo que el modelo tiene
// que extraer; resolverla a una fecha es trabajo de Go y ya tiene sus tests.
func EsperadoCasoAmbar() json.RawMessage {
	return json.RawMessage(`{
  "_procedencia": ` + procedencia + `,
  "version": 1,
  "delivery_hint": {
    "text": "el miércoles de la semana que viene",
    "evidence": "para el miércoles de la semana que viene"
  },
  "items": [
    {
      "product": "torta",
      "qty": 1,
      "range": {"min": 10, "max": 12, "unit": "porciones"},
      "addon_candidates": ["decoración infantil"],
      "notes": "bizcocho húmedo de chocolate con crema de chocolate",
      "evidence": "una torta sería con decoración infantil, de bizcocho húmedo de chocolate"
    },
    {
      "product": "torta",
      "qty": 1,
      "range": {"min": 25, "max": 30, "unit": "porciones"},
      "notes": "bizcocho de vainilla con lluvia de colores, dulce de leche y merengue",
      "evidence": "otra de bizcocho de vainilla que tenga lluvia de colores"
    },
    {
      "product": "tequeños congelados",
      "qty": 1,
      "unit_kind": "package",
      "package_size": 30,
      "evidence": "un paquete de tequeños congelados de 30"
    }
  ]
}`)
}

// CasoAmbar arma el caso completo listo para sembrar contra un tenant.
//
// `Consented` va en `true` PORQUE EL CONTENIDO ES REDACTADO: no hay ningún
// cliente real cuyo consentimiento haga falta pedir, y eso —no una excepción al
// guard— es lo que autoriza esta fila. 🔴 El día que la constante se sustituya
// por el texto REAL de una persona, esta línea deja de ser defendible sola y hay
// que tener el consentimiento del tenant antes de tocarla.
func CasoAmbar(tenantID string) Caso {
	return Caso{
		TenantID:   tenantID,
		Consented:  true,
		SourceText: TextoCasoAmbar,
		Expected:   EsperadoCasoAmbar(),
	}
}
