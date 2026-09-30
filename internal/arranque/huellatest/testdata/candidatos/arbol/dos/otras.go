// Package dos repite un literal de uno y añade uno propio.
package dos

import "net/http"

// Montar repite "/healthz" (sale una vez) y añade un comodín de resto.
func Montar(mux *http.ServeMux, f http.HandlerFunc) {
	mux.HandleFunc("/healthz", f)
	mux.HandleFunc("GET /api/v1/f/{resto...}", f)
}
