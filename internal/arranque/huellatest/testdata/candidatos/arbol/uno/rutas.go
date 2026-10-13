// Package uno es un árbol de prueba de huellatest.Candidatos.
package uno

import "net/http"

// Montar registra rutas con literales, con una variable y con una concatenación.
func Montar(mux *http.ServeMux, h http.Handler, variable string) {
	mux.Handle("GET /admin/tenants", h)
	mux.HandleFunc("/healthz", h.ServeHTTP)
	mux.Handle(`POST /api/v1/x/{id}`, h)
	mux.Handle(variable, h)
	mux.Handle("/con"+variable, h)
	http.Handle("/global", h)
}
