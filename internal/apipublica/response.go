// Porta internal/publicapi/publicapi.go @ 9a77307 (writeJSON y writeJSONErr, líneas 1146-1170),
// internal/publicapi/messages.go @ 9a77307 (writeError, línea 322), internal/publicapi/limits.go
// @ 9a77307 (errorBody, línea 71) e internal/publicapi/audit.go @ 9a77307 (parseIntQuery, línea 70).
//
// response.go — LAS UTILIDADES DE RESPUESTA QUE COMPARTEN TODAS LAS ÁREAS DE LA CARA. En la spec
// FX era `respuesta.go` (05 E-11). Nace en verde y sin rojo: no exporta nada, y un auxiliar no
// exportado no puede existir en un rojo (05 E-4, P6; hallazgo 33).
//
// Viven aquí y no en el fichero de área donde estaban en la cara vieja por la trampa T-15
// (reglas.md §2): writeError estaba en messages.go (F3) y parseIntQuery en audit.go, pero los
// usan áreas que se mudan antes o en otro orden; en un fichero común nacen con su PRIMER
// consumidor y ninguna mudanza tardía las deja huérfanas.

package apipublica

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// writeJSON serializa v como JSON con el código dado (mismo patrón que
// httpapi/flujos-admin). Ante fallo de codificación responde 500. El fallo de
// ESCRITURA se descarta: quien necesite enterarse usa writeJSONErr.
func writeJSON(w http.ResponseWriter, code int, v any) {
	//nolint:errcheck // el descarte es el contrato de writeJSON: quien necesite
	// enterarse del fallo de escritura llama a writeJSONErr, que lo devuelve.
	_ = writeJSONErr(w, code, v)
}

// writeJSONErr es writeJSON pero DEVUELVE el fallo de escritura en vez de
// tragárselo. Existe porque ese error silencioso fue lo que dejó el incidente del
// 2026-08-06 sin una sola línea de log: el handler creía haber respondido 200 y el
// cliente veía la conexión cerrada sin cuerpo. Un Write que falla es justo el
// evento que hay que registrar, no el que hay que ignorar.
//
// Un v que no se puede codificar no llega a escribir la cabecera pedida: responde el 500 en
// texto plano «codificando respuesta» de http.Error y devuelve el error de codificación.
func writeJSONErr(w http.ResponseWriter, code int, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "codificando respuesta", http.StatusInternalServerError)
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, werr := w.Write(body)
	return werr
}

// writeError responde un error como JSON tipado {error} (formato del listener
// público, coherente con el middleware de auth de T3).
func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, errorBody(msg))
}

// errorBody es el cuerpo de error CANÓNICO de la API pública: {"error": prosa}.
// Existe para que un handler que devuelve su cuerpo al llamante (en vez de
// escribirlo) use exactamente la misma forma que writeError.
func errorBody(msg string) map[string]string {
	return map[string]string{"error": msg}
}

// parseIntQuery lee un entero no negativo de la query; def si falta o es inválido.
//
// «Inválido» es lo que strconv.Atoi rechaza (decimales, letras, dígitos no ASCII, desbordes) y
// cualquier negativo. Un 0 explícito es 0, no def: quien pide cero elementos los pide.
func parseIntQuery(r *http.Request, key string, def int) int {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 {
		return def
	}
	return v
}
