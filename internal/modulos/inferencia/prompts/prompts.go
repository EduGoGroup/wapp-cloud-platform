// Porta internal/prompts/prompts.go @ ebf4eb7

// Package prompts carga de DISCO el texto ajustable de los prompts del pipeline
// y lo entrega ya validado, para que afinar un prompt no cueste una release del
// módulo compartido.
//
// ════════════════════════════════════════════════════════════════════════════
// 🧭 SI VIENES A CAMBIAR EL TEXTO DE UN PROMPT — LEE ESTO Y NO BUSQUES MÁS
// ════════════════════════════════════════════════════════════════════════════
//
//  1. ¿Dónde está el texto?  En el directorio que apunta WAPP_LLM_PROMPTS_DIR.
//     Si no está puesta, corre el texto COMPILADO en
//     `shared/wapp-shared/llm` (plantilla.go).
//  2. ¿Cómo saco los ficheros de partida?  `Volcar(dir)` escribe los CUATRO con
//     el texto real que corre hoy. Nunca los escribas a
//     mano copiando de otro sitio: se quedan viejos.
//  3. ¿Cómo aplico un cambio?  Editas el fichero y REINICIAS el cloud. No hay
//     recarga en caliente a propósito (ver abajo).
//  4. ¿Qué pasa si me equivoco?  El cloud NO ARRANCA y te dice qué fichero y por
//     qué. Nunca sirve un prompt roto.
//
// ── NOMENCLATURA DE LOS FICHEROS ────────────────────────────────────────────
//
//	<etapa>-<lo-que-quieras>.tmpl
//
// El PREFIJO `<etapa>-` es lo único que este paquete mira, y es el contrato: es
// el identificador corto de llm.Etapa («p2», «p3», «p4», «p5») seguido de un
// guion. Todo lo que va detrás es descripción para humanos y se puede cambiar sin
// romper nada — de ahí que la convención sea flexible. Los nombres que escribe
// Volcar, y que conviene mantener:
//
//	p2-extraer-ideas.tmpl              p4-normalizar-cantidades.tmpl
//	p3-especificar-item.tmpl           p5-redactar-cotizacion.tmpl
//
// 🔴 UN FICHERO CON UN PREFIJO QUE NO ES UNA ETAPA ES UN ERROR, no un fichero que
// se ignora. El modo de fallo que eso evita es el peor de todos: editas
// `p6-...tmpl`, reinicias, y NADA CAMBIA sin que nadie te diga por qué. Si
// quieres guardar notas al lado, ponlas en un fichero sin extensión `.tmpl` —
// esos sí se ignoran.
//
// Las MAYÚSCULAS no cuentan, ni en el prefijo ni en la extensión: `P4-x.TMPL`
// es la plantilla de p4 y SÍ se aplica. (El comentario del paquete viejo ponía
// `P4-...tmpl` como ejemplo de fichero que no se aplica; el código nunca hizo
// eso, y este contrato escribe lo que el código hace.)
//
// ── FORMATO DE UN FICHERO ───────────────────────────────────────────────────
//
//	Todo lo que escribas aquí arriba, ANTES del primer marcador, es
//	documentación tuya y NO se le manda al modelo. Úsalo.
//
//	--- INSTRUCCION ---
//	Lo que se le manda hacer al modelo.
//
//	--- ESQUEMA ---
//	Esquema de la respuesta:
//	{"version": {{version}}, ...}
//
// `{{version}}` se sustituye por la versión de artefacto que el código sabe leer.
// Escríbela así en vez de a mano: si el número cambia, los ficheros no se quedan
// atrás.
//
// 🔴 EL TEXTO ENTRE MARCADORES SE PRESERVA EXACTO, líneas en blanco incluidas. No
// se normaliza nada, y es a propósito: es lo único que garantiza que volcar los
// ficheros y cargarlos dé EL MISMO prompt que corre sin directorio. Si se
// recortaran los bordes, encender WAPP_LLM_PROMPTS_DIR cambiaría los prompts sin
// que nadie hubiera editado nada — y hay una asimetría real que lo demuestra: la
// instrucción de P5 empieza pegada al margen (es la primera línea de su prompt,
// que no lleva cabecera común) mientras que las de P2, P3 y P4 abren con una línea
// en blanco. Un recorte «inofensivo» se habría comido esa diferencia.
//
// ── LO QUE ESTE PAQUETE NO HACE, Y POR QUÉ ──────────────────────────────────
//
// NO recarga en caliente. Un prompt que cambia bajo los pies de un pipeline en
// vuelo hace que dos etapas del MISMO trabajo corran con textos distintos, y el
// artefacto resultante no es reproducible ni explicable. El reinicio es la
// frontera que hace que «este job corrió con este prompt» sea una frase cierta.
// Por eso aquí no hay vigilante de ficheros ni goroutine alguna.
//
// NO compone el prompt. La composición —cabecera, reglas de salida, orden de las
// piezas— vive en `llm.Build*PromptCon` y no se toca desde un fichero: el ORDEN es
// lo que mantiene cacheable el prefijo que el proveedor reutiliza (I6, ADR-0046),
// y dárselo a editar a un fichero sería regalar una forma silenciosa de
// multiplicar el coste de cada llamada.
//
// NO cubre P1. El prompt de clasificación lo gobierna el catálogo de intenciones
// del tenant, que YA se edita por API (`PUT /api/v1/intents`). Meterlo aquí le
// daría dos fuentes de verdad al mismo texto.
package prompts

import (
	"errors"

	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// El formato de un fichero, en constantes porque lo comparten el lector y el
// escritor: si Volcar y Cargar se desincronizaran, un vuelco no se podría releer.
// Son literales que el operador lee y escribe a mano: valen EXACTAMENTE esto,
// byte a byte.
const (
	// MarcaInstruccion abre la sección de la instrucción. Lo que quede por encima
	// es el preámbulo del fichero y no viaja al modelo. Vale "--- INSTRUCCION ---".
	MarcaInstruccion = "--- INSTRUCCION ---"
	// MarcaEsquema abre la sección del esquema y cierra la de la instrucción.
	// Vale "--- ESQUEMA ---".
	MarcaEsquema = "--- ESQUEMA ---"
	// Extension es la que hace que un fichero se considere una plantilla. Un
	// fichero con otra extensión se ignora en silencio, y ese es su uso: dejar
	// notas al lado sin que el cargador las mire. Vale ".tmpl".
	Extension = ".tmpl"
	// HuecoVersion se sustituye por llm.ArtifactVersion al cargar. Vale
	// "{{version}}".
	HuecoVersion = "{{version}}"
)

// ErrPromptsDir es el centinela de cualquier fallo cargando o volcando el
// directorio. Se inspecciona con errors.Is. Todos los fallos de este paquete son
// de ARRANQUE: no hay ninguno que se degrade a seguir con el texto compilado,
// porque un operador que editó un fichero y no ve el efecto es peor que un
// arranque que no ocurre.
//
// Su texto es EXACTAMENTE "prompts: directorio de plantillas inválido": lo ve el
// operador tal cual, porque `cmd/prompts` no le añade prefijo propio (este ya
// empieza por «prompts:»).
var ErrPromptsDir = errors.New("prompts: directorio de plantillas inválido")

// Cargadas es el resultado de una carga: las plantillas por etapa y de dónde
// salió cada una, para que el arranque pueda DECIRLO en el log. Que el log diga
// «p4 ← /etc/wapp/prompts/p4-normalizar-cantidades.tmpl» es la diferencia entre
// diagnosticar en un minuto y en una tarde.
type Cargadas struct {
	// Plantillas tiene una entrada por etapa ajustable, siempre las cuatro de
	// llm.EtapasAjustables: las que no tenían fichero llevan la compilada.
	Plantillas map[llm.Etapa]llm.Plantilla
	// Origen dice, por etapa, la ruta del fichero (el directorio unido al nombre
	// del fichero TAL COMO está en disco, con sus mayúsculas) o OrigenCompilado.
	Origen map[llm.Etapa]string
}

// OrigenCompilado es lo que Cargadas.Origen dice de una etapa sin fichero. Vale
// "compilada", literal: sale en el log del arranque.
const OrigenCompilado = "compilada"

// Cargar lee el directorio y devuelve las CUATRO plantillas, cada una validada.
//
// Un `dir` vacío —también el que solo trae espacios— no es un error: devuelve las
// compiladas (llm.PlantillaPorDefecto) y lo dice en Origen con OrigenCompilado. Es
// el caso normal en producción, donde no hay directorio y no debe haberlo.
//
// Con directorio:
//
//   - Solo mira FICHEROS (un subdirectorio se ignora, se llame como se llame) cuya
//     extensión sea Extension SIN distinguir mayúsculas. Todo lo demás —`NOTAS.md`,
//     `p4-viejo.tmpl.bak`— se ignora en silencio.
//   - La etapa sale del PREFIJO `<etapa>-` del nombre sin extensión, pasado a
//     minúsculas; el resto del nombre es libre. `P4-x.TMPL` carga como p4. Fuera de
//     las mayúsculas no se normaliza NADA: un dígito que no sea ASCII («p４-»,
//     «p٤-»), un espacio delante o entre la etapa y el guion (también el de no
//     separación) o un guion que no sea el ASCII no forman un prefijo de etapa.
//   - Los ficheros se recorren ORDENADOS por nombre, para que dos ficheros en
//     conflicto fallen siempre nombrando los mismos y en el mismo orden.
//   - Las etapas sin fichero se quedan con la compilada: ajustar UN prompt no
//     obliga a copiar los otros tres.
//
// Falla —y no arranca— envolviendo ErrPromptsDir, nombrando el fichero y el
// motivo, si:
//
//   - el directorio no se puede leer: «no se puede leer <dir>», envolviendo además
//     el error del sistema (errors.Is contra fs.ErrNotExist funciona);
//   - un `.tmpl` no empieza por una etapa conocida y un guion: «<nombre> no
//     empieza por ninguna etapa conocida (p2, p3, p4, p5) seguida de un guion; un
//     fichero así se quedaría sin aplicar SIN avisar, que es justo lo que este
//     error evita»;
//   - dos ficheros reclaman la misma etapa: «<a> y <b> reclaman la etapa "p4";
//     deja uno solo (el que sobra puede quedarse si le quitas la extensión
//     .tmpl)», con <a> antes que <b> en orden de nombre;
//   - Parsear rechaza el contenido: «<ruta>: » y el texto de Parsear;
//   - la plantilla no pasa llm.ValidarPlantilla: «<ruta> no se puede servir: » y
//     el error del módulo llm, que queda envuelto (errors.Is contra
//     llm.ErrPlantillaInvalida) y dice «lo rechaza su propio validador» cuando el
//     esquema imprime un valor inválido. Es la red del incidente de P4, que fue 0
//     de 14 en campo por un `"package_size": 0` en su esquema.
//
// NUNCA degrada al texto compilado: ante cualquier fallo devuelve el Cargadas
// cero (sin mapas) y el error.
func Cargar(dir string) (Cargadas, error) {
	panic(pendiente.Implementar("prompts.Cargar"))
}

// Parsear convierte el contenido de un fichero en una plantilla. Es público
// porque es la mitad del contrato del formato y merece testearse sin tocar disco.
//
// 🔴 EL TEXTO DE CADA SECCIÓN SALE VERBATIM: todo lo que hay entre el salto que
// cierra la línea del marcador y el marcador siguiente (o el final del fichero),
// tal cual, líneas en blanco incluidas. Lo único que se toca es el hueco
// HuecoVersion, que se sustituye por llm.ArtifactVersion en TODAS sus apariciones
// y en las dos secciones.
//
// No se recortan los bordes, y esa decisión tiene una prueba concreta detrás: la
// instrucción de P5 empieza pegada al margen —es la primera línea de su prompt,
// que no lleva cabecera común— y las de P2, P3 y P4 abren con línea en blanco. Un
// TrimSpace «inofensivo» iguala las cuatro y cambia el prompt de P5 al encender el
// directorio, sin que nadie haya editado nada. Lo cazó el test de ida y vuelta.
//
// Las reglas del formato:
//
//   - Lo anterior a la PRIMERA MarcaInstruccion es preámbulo y no viaja.
//   - Tras cada marcador se consume EXACTAMENTE un salto de línea (`\n` o `\r\n`)
//     y ni uno más: ese salto es del formato, y los que vengan detrás son del
//     prompt. Con finales `\r\n`, los demás `\r` del texto se conservan.
//   - La instrucción llega hasta la primera MarcaEsquema; el esquema, hasta la
//     siguiente MarcaInstruccion o el final. Lo que quede detrás de esa segunda
//     MarcaInstruccion se descarta.
//   - Un marcador REPETIDO dentro de su propia sección no la corta: una segunda
//     MarcaInstruccion antes de MarcaEsquema, o una segunda MarcaEsquema, se
//     quedan como texto de la sección y viajan al modelo.
//
// Errores (texto plano; quien les pone ErrPromptsDir y la ruta es Cargar):
//
//   - MarcaEsquema aparece antes que MarcaInstruccion: «"--- ESQUEMA ---" va antes
//     que "--- INSTRUCCION ---": el orden de las secciones es el del prompt». Se
//     comprueba ANTES de trocear: si no, ese fichero fallaría por «falta el
//     marcador» y mandaría a buscar un marcador que SÍ está.
//   - Falta un marcador: «falta el marcador "<marcador>"».
//   - Tras el marcador no viene un salto de línea —otro marcador pegado, un
//     espacio, o el final del fichero—: «el marcador "<marcador>" tiene que ir
//     SOLO en su línea». Solo se mira lo que le SIGUE.
//   - Una sección sin más que espacios: «la sección "<marcador>" está vacía».
func Parsear(contenido string) (llm.Plantilla, error) {
	panic(pendiente.Implementar("prompts.Parsear"))
}
