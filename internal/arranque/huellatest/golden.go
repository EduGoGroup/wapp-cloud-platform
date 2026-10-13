package huellatest

// golden.go — la dorada de la huella, en TEXTO (F10 · T10.2, diseno.md §5 del relevo).
//
// La huella del arranque viejo se congela en un fichero de texto con UNA ENTRADA POR LÍNEA,
// ordenadas: así el diff de una regeneración se lee línea a línea en la revisión, y la dorada
// sobrevive al borrado del arranque que la escribió. Sustituye al JSON de F0
// (testdata/huella.json), que solo guardaba la parte de ejecución: aquí va también la
// estática (goroutines y hooks), que hasta F10 se comparaba leyendo la fuente del viejo.
//
// Formato de cada línea (cuatro campos separados por TAB):
//
//	<perfil> TAB <clase> TAB <listener> TAB <valor>
//
// con clase ∈ {ruta, rpc, metrica, goroutine, hook, entorno}; el listener es ":8100",
// ":8103"… en `ruta` y `rpc`, y "-" en las demás. Las líneas vacías y las que empiezan por
// "#" se ignoran al leer.

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// goldenNoListener ocupa el campo listener en las clases que no lo tienen.
	goldenNoListener = "-"
	// goldenFields es el número de campos de una línea.
	goldenFields = 4

	goldenRoute     = "ruta"
	goldenRPC       = "rpc"
	goldenMetric    = "metrica"
	goldenGoroutine = "goroutine"
	goldenHook      = "hook"
	goldenEnv       = "entorno"

	// goldenHeader encabeza toda dorada. Dice quién la escribe y qué significa regenerarla.
	goldenHeader = "# Huella del arranque VIEJO de wapp-cloud-platform, congelada (F10 · T10.2).\n" +
		"# La escribe SOLO TestHuellaVieja con -args -actualizar; regenerarla es cambiar un\n" +
		"# contrato hacia fuera: commit propio y decisión escrita.\n" +
		"# Una entrada por línea, ordenadas: perfil <TAB> clase <TAB> listener (o -) <TAB> valor\n"
)

// WriteGolden escribe hs en path (lo crea o lo trunca) en el formato de texto de este
// fichero: la cabecera y después una línea por entrada, TODAS ordenadas (sort.Strings sobre
// la línea entera), terminado en salto de línea. Una entrada repetida se escribe tantas
// veces como aparece (las listas son multiconjuntos). Determinista: el orden de hs y el de
// sus listas no cambia los bytes.
//
// Es un error, y no se escribe nada: un perfil vacío, repetido o que empieza por "#"; un
// listener vacío; y un perfil, listener o valor con TAB o salto de línea (no se podría
// volver a leer). ReadGolden(path) devuelve después las mismas huellas, con los perfiles y
// las listas en el orden del fichero.
func WriteGolden(path string, hs []Huella) error {
	var lines []string
	seen := make(map[string]bool, len(hs))
	for _, h := range hs {
		if h.Perfil == "" || strings.HasPrefix(h.Perfil, "#") || seen[h.Perfil] {
			return fmt.Errorf("huellatest: perfil %q vacío, repetido o con «#» delante", h.Perfil)
		}
		seen[h.Perfil] = true
		var err error
		if lines, err = goldenLines(lines, h); err != nil {
			return err
		}
	}
	sort.Strings(lines)
	var b strings.Builder
	b.WriteString(goldenHeader)
	for _, l := range lines {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return fmt.Errorf("huellatest: escribir %s: %w", path, err)
	}
	return nil
}

// ReadGolden lee la dorada de texto path y devuelve una Huella por perfil, en el orden en
// que cada perfil aparece por primera vez, con las listas en el orden del fichero. Las
// líneas vacías y las que empiezan por "#" se ignoran; una lista sin entradas queda nil.
//
// Un fichero que no existe es un error que envuelve fs.ErrNotExist. Una línea que no tiene
// cuatro campos, con un campo vacío, de una clase desconocida, o con un listener que no
// corresponde a su clase ("-" en ruta o rpc; otro que "-" en las demás) es un error que
// nombra path y el número de línea (desde 1).
func ReadGolden(path string) ([]Huella, error) {
	b, err := fs.ReadFile(os.DirFS(filepath.Dir(path)), filepath.Base(path))
	if err != nil {
		return nil, fmt.Errorf("huellatest: leer %s: %w", path, err)
	}
	var hs []Huella
	index := map[string]int{}
	for i, line := range strings.Split(string(b), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != goldenFields || f[0] == "" || f[2] == "" || f[3] == "" {
			return nil, fmt.Errorf("huellatest: %s:%d: se esperaban %d campos no vacíos separados por TAB", path, i+1, goldenFields)
		}
		pos, ok := index[f[0]]
		if !ok {
			pos = len(hs)
			index[f[0]] = pos
			hs = append(hs, Huella{Perfil: f[0]})
		}
		if err := goldenAdd(&hs[pos], f[1], f[2], f[3]); err != nil {
			return nil, fmt.Errorf("huellatest: %s:%d: %w", path, i+1, err)
		}
	}
	return hs, nil
}

// goldenLines añade a lines las líneas de h.
func goldenLines(lines []string, h Huella) ([]string, error) {
	add := func(class, listener string, values []string) error {
		for _, v := range values {
			for _, field := range []string{h.Perfil, listener, v} {
				if field == "" || strings.ContainsAny(field, "\t\n") {
					return fmt.Errorf("huellatest: perfil %q, %s: campo %q vacío o con TAB o salto de línea", h.Perfil, class, field)
				}
			}
			lines = append(lines, strings.Join([]string{h.Perfil, class, listener, v}, "\t"))
		}
		return nil
	}
	for class, byListener := range map[string]map[string][]string{goldenRoute: h.Rutas, goldenRPC: h.RPC} {
		for listener, values := range byListener {
			if err := add(class, listener, values); err != nil {
				return nil, err
			}
		}
	}
	for class, values := range map[string][]string{
		goldenMetric: h.Metricas, goldenGoroutine: h.Goroutines, goldenHook: h.Hooks, goldenEnv: h.Entorno,
	} {
		if err := add(class, goldenNoListener, values); err != nil {
			return nil, err
		}
	}
	return lines, nil
}

// goldenAdd apunta en h una entrada leída de la dorada.
func goldenAdd(h *Huella, class, listener, value string) error {
	withListener := class == goldenRoute || class == goldenRPC
	if withListener == (listener == goldenNoListener) {
		return fmt.Errorf("el listener %q no corresponde a la clase %q", listener, class)
	}
	switch class {
	case goldenRoute:
		if h.Rutas == nil {
			h.Rutas = map[string][]string{}
		}
		h.Rutas[listener] = append(h.Rutas[listener], value)
	case goldenRPC:
		if h.RPC == nil {
			h.RPC = map[string][]string{}
		}
		h.RPC[listener] = append(h.RPC[listener], value)
	case goldenMetric:
		h.Metricas = append(h.Metricas, value)
	case goldenGoroutine:
		h.Goroutines = append(h.Goroutines, value)
	case goldenHook:
		h.Hooks = append(h.Hooks, value)
	case goldenEnv:
		h.Entorno = append(h.Entorno, value)
	default:
		return fmt.Errorf("clase desconocida %q", class)
	}
	return nil
}
