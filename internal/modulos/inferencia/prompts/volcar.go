// Porta internal/prompts/volcar.go @ ebf4eb7

package prompts

import (
	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// NombreDeFichero es el nombre que Volcar le da a cada etapa. El prefijo es
// contrato (es lo que Cargar lee para saber la etapa); el resto es descripción y
// se puede cambiar.
//
// Se declara como tabla y no se compone al vuelo para que el nombre que un
// operador ve en su directorio sea SIEMPRE el mismo y se pueda buscar en la
// documentación tal cual está escrito. Tiene una entrada por etapa de
// llm.EtapasAjustables, y son estos cuatro literales.
var NombreDeFichero = map[llm.Etapa]string{
	llm.EtapaP2: "p2-extraer-ideas" + Extension,
	llm.EtapaP3: "p3-especificar-item" + Extension,
	llm.EtapaP4: "p4-normalizar-cantidades" + Extension,
	llm.EtapaP5: "p5-redactar-cotizacion" + Extension,
}

// QueHaceLaEtapa describe en una línea para qué sirve cada prompt. Va en el
// preámbulo del fichero volcado: quien lo abre para tocarlo tiene que saber qué
// está tocando sin ir a buscar el código. Una frase por etapa de
// llm.EtapasAjustables, literal.
var QueHaceLaEtapa = map[llm.Etapa]string{
	llm.EtapaP2: "saca las ideas principales del mensaje: una entrada por cosa distinta que pide el cliente.",
	llm.EtapaP3: "especifica UN ítem —producto, variante, añadidos, personalizaciones—. Se llama una vez por ítem.",
	llm.EtapaP4: "normaliza cantidades, paquetes y rangos, y resuelve la fecha de entrega contra la del mensaje.",
	llm.EtapaP5: "redacta el mensaje de cotización con la voz del negocio, copiando importes del borrador.",
}

// Volcar escribe en dir los CUATRO ficheros con el texto que corre HOY, y devuelve
// las rutas escritas: `dir` unido a NombreDeFichero, en el orden de
// llm.EtapasAjustables. Cada fichero lleva Serializar de la plantilla compilada
// (llm.PlantillaPorDefecto) y permisos 0o600; `dir` se crea si no existe, con sus
// padres, con 0o750.
//
// 🔴 ES LA ÚNICA FORMA CORRECTA DE EMPEZAR A EDITAR UN PROMPT. Escribir los
// ficheros a mano copiando de la documentación, de un ejemplo o de un fichero de
// otro entorno produce plantillas que ya nacen viejas y que cambian el prompt sin
// que nadie lo haya querido — y el cambio no se ve en ningún diff, porque el
// fichero es nuevo. Volcar sale del código compilado, así que el diff contra lo
// que edites es exactamente lo que cambiaste.
//
// No sobrescribe: un fichero que ya existe se respeta —su contenido queda
// intacto— y se informa en el error, «<ruta> ya existe y NO se sobrescribe;
// bórralo tú si de verdad quieres perder lo que tiene». El operador que quiera
// regenerarlo lo borra a conciencia; perderle una tarde de ajustes a alguien por
// un comando de más no es un intercambio aceptable. No promete atomicidad: los
// ficheros de las etapas anteriores a la que choca pueden quedar escritos.
//
// Un `dir` vacío —también el que solo trae espacios— es un error: «volcar
// necesita un directorio». Todo error envuelve ErrPromptsDir y llega sin rutas.
func Volcar(dir string) ([]string, error) {
	panic(pendiente.Implementar("prompts.Volcar"))
}

// Serializar convierte una plantilla en el contenido de su fichero. Es la inversa
// exacta de Parsear —hay un test que lo exige sobre las cuatro etapas—, y esa
// simetría es lo que hace que volcar, editar y cargar sea un ciclo cerrado en vez
// de dos formatos que se parecen.
//
// La versión del artefacto se escribe como el hueco HuecoVersion y no como el
// número: la primera aparición de `"version": <llm.ArtifactVersion>` en el esquema
// sale como `"version": {{version}}`. Así un fichero editado hoy sigue valiendo el
// día que ese número cambie, en vez de quedarse clavado en un valor que ya no es.
//
// El contenido es, byte a byte y en este orden: el preámbulo, MarcaInstruccion y
// un `\n`, la instrucción tal cual, MarcaEsquema y un `\n`, y el esquema (con el
// hueco). Sin recortes ni saltos añadidos: lo que se escribe es lo que Parsear
// devuelve. El preámbulo —<ETAPA> es la etapa en mayúsculas y <frase> su
// QueHaceLaEtapa— es este texto, seguido de una línea en blanco:
//
//	Prompt de la etapa <ETAPA> — <frase>
//
//	Esto de aquí arriba, ANTES del primer marcador, es documentación y NO se le manda
//	al modelo. Escribe aquí lo que haga falta para el que venga detrás.
//
//	Cómo se usa este fichero:
//	  - Edítalo y REINICIA el cloud. No hay recarga en caliente, a propósito.
//	  - Si te equivocas, el cloud NO ARRANCA y te dice qué fichero y por qué.
//	  - {{version}} se sustituye por la versión de artefacto que el código sabe leer.
//	  - El texto entre marcadores se preserva EXACTO, líneas en blanco incluidas.
//
//	🔴 EN EL ESQUEMA NO PUEDE HABER UN VALOR QUE EL VALIDADOR RECHACE. El modelo COPIA
//	el ejemplo: un 0 escrito ahí es un 0 en su respuesta. Los `...` sí pueden quedarse
//	—son huecos reconocibles y se detectan si el modelo los ecoa—, pero un número tiene
//	que ser válido tal cual está impreso. Esto ya costó una etapa entera: P4 fue 0 de 14
//	en su primer día en campo porque su esquema imprimía `"package_size": 0`.
func Serializar(e llm.Etapa, p llm.Plantilla) string {
	panic(pendiente.Implementar("prompts.Serializar"))
}
