// Porta internal/catalogimport/prompt.go @ 3c74b80

package catalogimport

import "github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"

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

// ImportPrompt devuelve el prompt-plantilla listo para copiar: el texto que el
// dueño del negocio pega en SU LLM (Gemini, Claude, el que use) junto con la
// plantilla y su lista de productos, para que le devuelva un documento de import.
//
// Es la pieza que hace que cargar un catálogo cueste cero: wApp no pone
// credenciales de LLM ni paga tokens; pone las palabras exactas que hacen que un
// LLM cualquiera produzca algo que este validador acepta.
//
// Promete: el texto es el del design §6 palabra por palabra (testdata/
// import_prompt.txt, byte a byte), en una sola línea y sin salto final; dicta el
// format entre comillas y la versión tal como los exige el validador —salen de
// ImportFormat e ImportVersion, no están escritos a mano—; nombra sku, price,
// variants y components; y es el mismo en cada llamada.
//
// 🟡 Deuda visible (D-20, decidida por Jhoan el 2026-08-06): este texto NO se ha
// probado contra un LLM real, y con cero gasto (D-11) sigue así. Se porta literal:
// no se «mejora» (reglas.md T-9).
func ImportPrompt() string {
	panic(pendiente.Implementar("catalogimport.ImportPrompt"))
}
