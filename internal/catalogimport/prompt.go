package catalogimport

import (
	"fmt"
	"strconv"
)

// El prompt-plantilla vive AQUÍ, en el mismo paquete que ImportFormat e
// ImportVersion, y no en un runbook ni en una plantilla del BFF. La razón es que el
// design lo pide versionado JUNTO AL CONTRATO —«si ImportVersion sube, el prompt se
// revisa en el mismo commit» (design §6)— y un documento en otro repo no puede
// cumplir eso: nadie que suba la versión del contrato abre la carpeta de docs de
// otro proyecto. Aquí, en cambio, el olvido no es posible: PromptContractVersion
// está tres líneas más abajo que el texto y un test lo ata a ImportVersion, así que
// subir el contrato sin revisar el prompt deja la suite en rojo.
//
// El BFF no lo copia: lo PIDE por HTTP (GET /api/v1/catalog/import/prompt) y lo
// muestra como texto copiable. Una copia pegada en una plantilla HTML sería una
// segunda fuente que envejece sola.

// PromptContractVersion es la versión del contrato con la que se revisó por última
// vez el texto del prompt. DEBE ser igual a ImportVersion; un test lo exige.
//
// No es redundancia: es el disparador. Cuando el contrato cambie, la suite se pone
// en rojo aquí y obliga a leer el prompt y decidir —en el MISMO commit— si sigue
// diciendo la verdad. Actualizar este número sin releer el texto es saltarse a
// propósito el único mecanismo que impide que el prompt se quede describiendo un
// contrato que ya no existe.
const PromptContractVersion = 1

// promptTemplate es el prompt-plantilla del design §6, palabra por palabra. Lo
// único que se le quitó son los acentos graves con los que el design marca los
// nombres de campo: son decoración de Markdown, y el destino de este texto es la
// caja de chat de un LLM, no un documento.
//
// El format y la versión NO están escritos a mano: se interpolan desde las
// constantes del contrato. Un prompt que le dicte al LLM una versión distinta de la
// que el validador acepta produce documentos que se rechazan enteros, y el dueño
// del negocio culparía a su LLM.
//
// 🟡 DEUDA VISIBLE, DECIDIDA POR JHOAN (2026-08-06): este texto NO se ha probado
// contra un LLM externo real. El criterio de T3.2 pide una corrida «lista de
// productos + plantilla → LLM externo → JSON que valida» y esa corrida queda
// PENDIENTE: lo que hay aquí es el texto del design, no un texto verificado. El
// design ya lo anticipaba («el texto final se ajusta en la ola contra pruebas
// reales con Gemini/Claude»), así que trátalo como una hipótesis con formato de
// instrucción hasta que alguien pegue la corrida en el journal.
const promptTemplate = `Te doy mi lista de productos con precios y una plantilla JSON. ` +
	`Genera SOLO un JSON válido que siga EXACTAMENTE el formato de la plantilla ` +
	`(format: %s, version: %d). Reglas: cada producto va en una categoría (crea ` +
	`categorías razonables si no las doy); sku corto y único por producto, sin ` +
	`espacios; price numérico sin símbolo de moneda; si un producto tiene ` +
	`tamaños/presentaciones con precios distintos, usa variants; si es un combo de ` +
	`varios productos, usa components con los sku de sus partes; no inventes ` +
	`productos ni precios que no estén en mi lista; no agregues comentarios ni texto ` +
	`fuera del JSON. Mi lista: …`

// ImportPrompt devuelve el prompt-plantilla listo para copiar: el texto que el
// dueño del negocio pega en SU LLM (Gemini, Claude, el que use) junto con la
// plantilla y su lista de productos, para que le devuelva un documento de import.
//
// Es la pieza que hace que cargar un catálogo cueste cero: wApp no pone
// credenciales de LLM ni paga tokens; pone las palabras exactas que hacen que un
// LLM cualquiera produzca algo que este validador acepta.
func ImportPrompt() string {
	return fmt.Sprintf(promptTemplate, strconv.Quote(ImportFormat), ImportVersion)
}
