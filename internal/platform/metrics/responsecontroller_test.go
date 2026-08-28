package metrics_test

// responsecontroller_test.go — InstrumentHTTP NO puede cortar la cadena del
// http.ResponseController (Plan 047 · Ola 2).
//
// El envoltorio que InstrumentHTTP le pone a CADA petición de los dos listeners para
// capturar el status es también el que se interpone entre el handler y la conexión.
// Un envoltorio que no implementa `Unwrap() http.ResponseWriter` deja al
// ResponseController sin camino hasta ella, y TODOS sus métodos —SetWriteDeadline,
// SetReadDeadline, Flush— empiezan a devolver http.ErrNotSupported sin que nada más
// cambie: el handler cree que puso el plazo y no puso ninguno.
//
// Eso es exactamente lo que le pasaba a `POST /api/v1/intakes/{id}/quote-suggestion`
// mientras se escribía este arreglo, y por eso el test vive aquí y no solo en la ruta
// que lo necesita: quien quite el Unwrap rompe una ruta de OTRO paquete.

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/metrics"
)

// envoltorioSordo es un ResponseWriter envuelto SIN Unwrap: el defecto, en dos líneas.
// Existe para que el aserto positivo de abajo no sea una tautología — demuestra que
// este test SÍ distingue una cadena entera de una cortada, y que el verde de
// InstrumentHTTP dice algo.
type envoltorioSordo struct{ http.ResponseWriter }

func TestInstrumentHTTP_NoRompeElResponseController(t *testing.T) {
	m := metrics.New()

	var (
		errDirecto       error
		errInstrumentado error
		errSordo         error
	)
	plazo := func(w http.ResponseWriter) error {
		return http.NewResponseController(w).SetWriteDeadline(time.Now().Add(time.Minute))
	}

	mux := http.NewServeMux()
	mux.Handle("GET /directo", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		errDirecto = plazo(w)
	}))
	mux.Handle("GET /instrumentado", m.InstrumentHTTP("public",
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			errInstrumentado = plazo(w)
		})))
	mux.Handle("GET /sordo", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		errSordo = plazo(&envoltorioSordo{w})
	}))

	srv := httptest.NewServer(mux)
	defer srv.Close()
	for _, ruta := range []string{"/directo", "/instrumentado", "/sordo"} {
		resp, err := http.Get(srv.URL + ruta)
		if err != nil {
			t.Fatalf("GET %s: %v", ruta, err)
		}
		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			t.Fatalf("leyendo la respuesta de %s: %v", ruta, err)
		}
		_ = resp.Body.Close()
	}

	// El control NEGATIVO primero: si esto no falla, el mecanismo que el test cree
	// estar midiendo no existe y los otros dos asertos no valen nada.
	if !errors.Is(errSordo, http.ErrNotSupported) {
		t.Fatalf("un envoltorio SIN Unwrap devolvió %v, esperaba ErrNotSupported: "+
			"este test ya no distingue una cadena entera de una cortada", errSordo)
	}
	if errDirecto != nil {
		t.Fatalf("sirviendo SIN envoltorio, SetWriteDeadline devolvió %v: "+
			"el servidor de este test no soporta plazos y el caso instrumentado no probaría nada", errDirecto)
	}
	if errInstrumentado != nil {
		t.Fatalf("detrás de InstrumentHTTP, SetWriteDeadline devolvió %v.\n"+
			"El envoltorio de métricas cortó la cadena del ResponseController: le falta "+
			"`Unwrap() http.ResponseWriter`. Con esto roto, el plazo de escritura por ruta de "+
			"POST /api/v1/intakes/{id}/quote-suggestion no se pone y esa respuesta vuelve a no "+
			"caber por el cable (internal/publicapi/plazoescritura.go).", errInstrumentado)
	}
}
