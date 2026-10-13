//go:build integracion

package procesos

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
)

// El log del servidor de prueba: el búfer concurrente donde escribe el subproceso, sus líneas JSON
// y las últimas líneas que se vuelcan cuando algo falla.
// Sale de servidor_test.go (D-F9-11: solo se movieron declaraciones).

// registroLog es el búfer donde cae la salida del subproceso (stdout y stderr al MISMO
// búfer). Es seguro entre goroutines: el proceso escribe mientras el test lee.
type registroLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write añade p al búfer bajo candado. Cumple io.Writer; nunca falla.
func (r *registroLog) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.Write(p)
}

// String devuelve una copia de todo lo escrito hasta ahora.
func (r *registroLog) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.String()
}

// lineasJSON recibe un texto de log y devuelve, en orden, cada línea que es un objeto JSON
// parseada a map[string]any. Ignora las líneas vacías, las de texto plano, el JSON truncado y el
// JSON que no es un objeto (un arreglo, null, un número). Devuelve un slice vacío, no nil, si no
// hay ninguna. No falla.
func lineasJSON(texto string) []map[string]any {
	lineas := []map[string]any{}
	for linea := range strings.SplitSeq(texto, "\n") {
		linea = strings.TrimSpace(linea)
		if !strings.HasPrefix(linea, "{") {
			continue
		}
		var objeto map[string]any
		if err := json.Unmarshal([]byte(linea), &objeto); err != nil || objeto == nil {
			continue
		}
		lineas = append(lineas, objeto)
	}
	return lineas
}

// ultimasLineas recibe un texto y devuelve sus últimas n líneas (sin el salto final del texto), o
// el texto entero si tiene menos. n ≤ 0 devuelve "". No falla.
func ultimasLineas(texto string, n int) string {
	if n <= 0 {
		return ""
	}
	lineas := strings.Split(strings.TrimSuffix(texto, "\n"), "\n")
	if len(lineas) > n {
		lineas = lineas[len(lineas)-n:]
	}
	return strings.Join(lineas, "\n")
}
