// Package prompts carga de DISCO el texto ajustable de los prompts del pipeline
// y lo entrega ya validado, para que afinar un prompt no cueste una release del
// módulo compartido.
//
// ════════════════════════════════════════════════════════════════════════════
// 🧭 SI VIENES A CAMBIAR EL TEXTO DE UN PROMPT — LEE ESTO Y NO BUSQUES MÁS
// ════════════════════════════════════════════════════════════════════════════
//
//  1. ¿Dónde está el texto?  En el directorio que apunta WAPP_PROMPTS_DIR.
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
// `p6-...tmpl` o `P4-...tmpl`, reinicias, y NADA CAMBIA sin que nadie te diga por
// qué. Si quieres guardar notas al lado, ponlas en un fichero sin extensión
// `.tmpl` — esos sí se ignoran.
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
// recortaran los bordes, encender WAPP_PROMPTS_DIR cambiaría los prompts sin que
// nadie hubiera editado nada — y hay una asimetría real que lo demuestra: la
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
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/EduGoGroup/wapp-shared/llm"
)

// El formato de un fichero, en constantes porque lo comparten el lector y el
// escritor: si Volcar y Cargar se desincronizaran, un vuelco no se podría releer.
const (
	// MarcaInstruccion abre la sección de la instrucción. Lo que quede por encima
	// es el preámbulo del fichero y no viaja al modelo.
	MarcaInstruccion = "--- INSTRUCCION ---"
	// MarcaEsquema abre la sección del esquema y cierra la de la instrucción.
	MarcaEsquema = "--- ESQUEMA ---"
	// Extension es la que hace que un fichero se considere una plantilla. Un
	// fichero con otra extensión se ignora en silencio, y ese es su uso: dejar
	// notas al lado sin que el cargador las mire.
	Extension = ".tmpl"
	// HuecoVersion se sustituye por llm.ArtifactVersion al cargar.
	HuecoVersion = "{{version}}"
)

// ErrPromptsDir es el centinela de cualquier fallo cargando el directorio. Se
// inspecciona con errors.Is. Todos los fallos de este paquete son de ARRANQUE: no
// hay ninguno que se degrade a seguir con el texto compilado, porque un operador
// que editó un fichero y no ve el efecto es peor que un arranque que no ocurre.
var ErrPromptsDir = errors.New("prompts: directorio de plantillas inválido")

// Cargadas es el resultado de una carga: las plantillas por etapa y de dónde
// salió cada una, para que el arranque pueda DECIRLO en el log. Que el log diga
// «p4 ← /etc/wapp/prompts/p4-normalizar-cantidades.tmpl» es la diferencia entre
// diagnosticar en un minuto y en una tarde.
type Cargadas struct {
	// Plantillas tiene una entrada por etapa ajustable, siempre las cuatro: las
	// que no tenían fichero llevan la compilada.
	Plantillas map[llm.Etapa]llm.Plantilla
	// Origen dice, por etapa, la ruta del fichero o "compilada".
	Origen map[llm.Etapa]string
}

// OrigenCompilado es lo que Cargadas.Origen dice de una etapa sin fichero.
const OrigenCompilado = "compilada"

// Cargar lee el directorio y devuelve las CUATRO plantillas, cada una validada.
//
// Un `dir` vacío no es un error: devuelve las compiladas y lo dice en Origen. Es
// el caso normal en producción, donde no hay directorio y no debe haberlo.
//
// Falla —y no arranca— si: el directorio no se puede leer, un `.tmpl` no empieza
// por un prefijo de etapa conocido, dos ficheros reclaman la misma etapa, un
// fichero no trae sus dos marcadores, o una plantilla no pasa llm.ValidarPlantilla.
func Cargar(dir string) (Cargadas, error) {
	out := Cargadas{
		Plantillas: make(map[llm.Etapa]llm.Plantilla, len(llm.EtapasAjustables)),
		Origen:     make(map[llm.Etapa]string, len(llm.EtapasAjustables)),
	}
	for _, e := range llm.EtapasAjustables {
		p, ok := llm.PlantillaPorDefecto(e)
		if !ok {
			return Cargadas{}, fmt.Errorf("%w: la etapa %q está en EtapasAjustables y no tiene "+
				"plantilla compilada; es un bug del módulo llm, no de la configuración", ErrPromptsDir, e)
		}
		out.Plantillas[e] = p
		out.Origen[e] = OrigenCompilado
	}
	if strings.TrimSpace(dir) == "" {
		return out, nil
	}

	entradas, err := os.ReadDir(dir)
	if err != nil {
		return Cargadas{}, fmt.Errorf("%w: no se puede leer %s: %w", ErrPromptsDir, dir, err)
	}

	// Se recorre ORDENADO para que un directorio con dos ficheros en conflicto
	// falle siempre nombrando los mismos, y no según el orden del sistema de
	// ficheros: un error que cambia de texto entre dos ejecuciones idénticas no se
	// puede buscar en un historial.
	nombres := make([]string, 0, len(entradas))
	for _, en := range entradas {
		if !en.IsDir() && strings.EqualFold(filepath.Ext(en.Name()), Extension) {
			nombres = append(nombres, en.Name())
		}
	}
	sort.Strings(nombres)

	vistas := make(map[llm.Etapa]string, len(nombres))
	for _, nombre := range nombres {
		etapa, err := etapaDeNombre(nombre)
		if err != nil {
			return Cargadas{}, err
		}
		if antes, dup := vistas[etapa]; dup {
			return Cargadas{}, fmt.Errorf("%w: %s y %s reclaman la etapa %q; deja uno solo "+
				"(el que sobra puede quedarse si le quitas la extensión %s)",
				ErrPromptsDir, antes, nombre, etapa, Extension)
		}
		ruta := filepath.Join(dir, nombre)
		crudo, err := os.ReadFile(ruta) //nolint:gosec // ruta de configuración del operador, no de un usuario
		if err != nil {
			return Cargadas{}, fmt.Errorf("%w: no se puede leer %s: %w", ErrPromptsDir, ruta, err)
		}
		plantilla, err := Parsear(string(crudo))
		if err != nil {
			return Cargadas{}, fmt.Errorf("%w: %s: %w", ErrPromptsDir, ruta, err)
		}
		if err := llm.ValidarPlantilla(etapa, plantilla); err != nil {
			return Cargadas{}, fmt.Errorf("%w: %s no se puede servir: %w", ErrPromptsDir, ruta, err)
		}
		vistas[etapa] = nombre
		out.Plantillas[etapa] = plantilla
		out.Origen[etapa] = ruta
	}
	return out, nil
}

// etapaDeNombre saca la etapa del PREFIJO del nombre de fichero. Es la única
// parte del nombre que este paquete interpreta.
func etapaDeNombre(nombre string) (llm.Etapa, error) {
	base := strings.ToLower(strings.TrimSuffix(nombre, filepath.Ext(nombre)))
	for _, e := range llm.EtapasAjustables {
		if strings.HasPrefix(base, string(e)+"-") {
			return e, nil
		}
	}
	return "", fmt.Errorf("%w: %s no empieza por ninguna etapa conocida (%s) seguida de un guion; "+
		"un fichero así se quedaría sin aplicar SIN avisar, que es justo lo que este error evita",
		ErrPromptsDir, nombre, etapasComoTexto())
}

func etapasComoTexto() string {
	ss := make([]string, 0, len(llm.EtapasAjustables))
	for _, e := range llm.EtapasAjustables {
		ss = append(ss, string(e))
	}
	return strings.Join(ss, ", ")
}

// Parsear convierte el contenido de un fichero en una plantilla. Es público
// porque es la mitad del contrato del formato y merece testearse sin tocar disco.
//
// 🔴 EL TEXTO DE CADA SECCIÓN SALE VERBATIM: todo lo que hay entre el salto que
// cierra la línea del marcador y el marcador siguiente (o el final del fichero),
// tal cual, líneas en blanco incluidas. Lo único que se toca es el hueco
// {{version}}.
//
// No se recortan los bordes, y esa decisión tiene una prueba concreta detrás: la
// instrucción de P5 empieza pegada al margen —es la primera línea de su prompt,
// que no lleva cabecera común— y las de P2, P3 y P4 abren con línea en blanco. Un
// TrimSpace «inofensivo» iguala las cuatro y cambia el prompt de P5 al encender el
// directorio, sin que nadie haya editado nada. Lo cazó el test de ida y vuelta.
func Parsear(contenido string) (llm.Plantilla, error) {
	// El orden se comprueba ANTES de trocear. Si no, un fichero con las secciones
	// invertidas falla por «falta el marcador ESQUEMA» —porque ya se lo comió la
	// sección anterior—, y ese mensaje manda a buscar un marcador que SÍ está.
	iInstr := strings.Index(contenido, MarcaInstruccion)
	iEsq := strings.Index(contenido, MarcaEsquema)
	if iInstr >= 0 && iEsq >= 0 && iEsq < iInstr {
		return llm.Plantilla{}, fmt.Errorf("%q va antes que %q: el orden de las secciones es el del prompt",
			MarcaEsquema, MarcaInstruccion)
	}

	instruccion, resto, err := seccion(contenido, MarcaInstruccion)
	if err != nil {
		return llm.Plantilla{}, err
	}
	esquema, _, err := seccion(resto, MarcaEsquema)
	if err != nil {
		return llm.Plantilla{}, err
	}
	if strings.TrimSpace(instruccion) == "" {
		return llm.Plantilla{}, fmt.Errorf("la sección %q está vacía", MarcaInstruccion)
	}
	if strings.TrimSpace(esquema) == "" {
		return llm.Plantilla{}, fmt.Errorf("la sección %q está vacía", MarcaEsquema)
	}

	version := fmt.Sprintf("%d", llm.ArtifactVersion)
	return llm.Plantilla{
		Instruccion: strings.ReplaceAll(instruccion, HuecoVersion, version),
		Esquema:     strings.ReplaceAll(esquema, HuecoVersion, version),
	}, nil
}

// seccion devuelve el texto que sigue a un marcador hasta el siguiente marcador o
// el final, y el resto del fichero a partir de ese marcador.
//
// Consume EXACTAMENTE el salto de línea que cierra la línea del marcador y ni uno
// más: ese salto es del formato, y los que vengan detrás son del prompt.
func seccion(contenido, marca string) (texto, resto string, err error) {
	i := strings.Index(contenido, marca)
	if i < 0 {
		return "", "", fmt.Errorf("falta el marcador %q", marca)
	}
	desde := i + len(marca)
	rest := contenido[desde:]
	rest = strings.TrimPrefix(rest, "\r")
	if !strings.HasPrefix(rest, "\n") {
		return "", "", fmt.Errorf("el marcador %q tiene que ir SOLO en su línea", marca)
	}
	rest = rest[1:]

	fin := len(rest)
	for _, otra := range []string{MarcaInstruccion, MarcaEsquema} {
		if otra == marca {
			continue
		}
		if j := strings.Index(rest, otra); j >= 0 && j < fin {
			fin = j
		}
	}
	return rest[:fin], rest[fin:], nil
}
