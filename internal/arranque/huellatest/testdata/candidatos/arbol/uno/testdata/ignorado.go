package ignorado

import "net/http"

// Montar no cuenta: cuelga de un testdata.
func Montar(mux *http.ServeMux) { mux.Handle("/ignorada", http.NotFoundHandler()) }
