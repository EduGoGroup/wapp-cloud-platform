package publicapi_test

// intakes_vencimiento_test.go — LA MARCA «VENCIDO» EN EL WIRE
// (Plan 044 · Ola 4 · T4.5, REQ-25, D-044.50 §1).
//
// La REGLA del plazo se prueba entera en el dominio (internal/intakes/
// vencimiento_test.go) y no se repite aquí. Lo que se prueba en este fichero es lo
// otro, que el dominio no puede afirmar: que la marca CRUZA el contrato y que
// cruzarla no mueve el estado.
//
// 🔴 Por qué hace falta un test aparte para el LISTADO. Los golden congelan el
// DETALLE, no la bandeja, y la bandeja es justo la pantalla que la marca existe para
// pintar (T4.2 / T4.7). Sin esto, `overdue` podría salir en el detalle y faltar en la
// lista sin que nada se pusiera rojo.
//
// Ninguno de estos tests necesita base de datos: ni uno se salta.

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

const (
	intakeVencido = "aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa"
	intakeEnPlazo = "bbbbbbbb-2222-4222-8222-bbbbbbbbbbbb"
)

// bandejaConPlazos siembra DOS presupuestos en `pending_approval` que solo se
// diferencian en cuánto llevan esperando: uno se pasó del plazo y el otro no.
//
// Las fechas se calculan contra time.Now() y no contra una constante, porque la
// marca se evalúa con el reloj REAL del servidor: es lo que hace que este test mida
// el camino de producción y no una versión suya con el reloj sustituido. La
// distancia —tres plazos por un lado, un minuto por el otro— es tan grande que
// ninguna lentitud de CI puede hacerla ambigua.
func bandejaConPlazos() *intakes.MemoryStore {
	st := intakes.NewMemoryStore()
	añadir := func(id string, esperandoDesde time.Time) {
		st.Add(tenantA, intakes.Intake{
			ID: id, ContactID: contactoOpaco, SessionID: "sess-a",
			Status: intakes.StatusPendingApproval, Total: 21000,
			CreatedAt: esperandoDesde, UpdatedAt: esperandoDesde,
		})
	}
	añadir(intakeVencido, time.Now().Add(-3*intakes.QuoteDeadline))
	añadir(intakeEnPlazo, time.Now().Add(-time.Minute))
	return st
}

// depsBandeja arma unas Deps con `cart_basic` para el tenant A y NINGÚN recordatorio
// cableado: la marca es derivada y no depende de que el aviso al dueño esté enchufado.
func depsBandeja(st *intakes.MemoryStore) publicapi.Deps {
	fake := entitlements.NewFake()
	fake.Enable(tenantA, entitlements.FeatureCartBasic)
	return publicapi.Deps{Intakes: intakes.NewService(st), Entitlements: fake}
}

// cabecerasPorID decodifica la respuesta del listado y la indexa por id, quedándose
// con las dos claves que este test mira.
func cabecerasPorID(t *testing.T, body []byte) map[string]struct {
	Status  string `json:"status"`
	Overdue bool   `json:"overdue"`
} {
	t.Helper()
	var resp struct {
		Intakes []struct {
			ID      string `json:"id"`
			Status  string `json:"status"`
			Overdue bool   `json:"overdue"`
		} `json:"intakes"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decodificando el listado: %v\nbody=%s", err, body)
	}
	out := map[string]struct {
		Status  string `json:"status"`
		Overdue bool   `json:"overdue"`
	}{}
	for _, in := range resp.Intakes {
		out[in.ID] = struct {
			Status  string `json:"status"`
			Overdue bool   `json:"overdue"`
		}{Status: in.Status, Overdue: in.Overdue}
	}
	return out
}

// TestBandeja_ElVencidoSaleMarcadoYSigueEnPendingApproval es el criterio (a) visto
// desde el wire. Las DOS solicitudes viajan en la MISMA respuesta a propósito: así el
// test distingue «marca lo que toca» de «marca todo», que es lo que un caso suelto no
// puede.
func TestBandeja_ElVencidoSaleMarcadoYSigueEnPendingApproval(t *testing.T) {
	api := newAPI(depsBandeja(bandejaConPlazos()), intakesKeys())
	rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}

	filas := cabecerasPorID(t, rec.Body.Bytes())
	if len(filas) != 2 {
		t.Fatalf("solicitudes = %d, quiero 2", len(filas))
	}

	vencido, enPlazo := filas[intakeVencido], filas[intakeEnPlazo]
	if !vencido.Overdue {
		t.Error("el presupuesto que lleva tres plazos esperando sale con overdue=false")
	}
	if enPlazo.Overdue {
		t.Error("el presupuesto de hace un minuto sale con overdue=true: la marca no distingue nada")
	}
	// Y la otra mitad del criterio: la marca NO es un estado. Los dos siguen donde
	// estaban, y el vencido sobre todo.
	for id, fila := range filas {
		if fila.Status != intakes.StatusPendingApproval {
			t.Errorf("status de %s = %q, quiero %q: `overdue` es una MARCA DERIVADA y no puede "+
				"transicionar nada (D-041.16, nada muere por tiempo)",
				id, fila.Status, intakes.StatusPendingApproval)
		}
	}
}

// TestBandeja_LaMarcaViajaSiempreAunqueSeaFalsa: la clave está en el cuerpo también
// cuando vale `false`. Sin esto, quien consume tendría que distinguir «no está
// vencido» de «este servidor todavía no publica la marca», que son dos cosas
// distintas y se pintan distinto.
func TestBandeja_LaMarcaViajaSiempreAunqueSeaFalsa(t *testing.T) {
	api := newAPI(depsBandeja(bandejaConPlazos()), intakesKeys())
	rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200", rec.Code)
	}

	var resp struct {
		Intakes []map[string]json.RawMessage `json:"intakes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decodificando: %v", err)
	}
	for i, fila := range resp.Intakes {
		if _, hay := fila["overdue"]; !hay {
			t.Fatalf("la fila %d del listado no trae la clave `overdue`: %v", i, fila)
		}
	}
}

// TestBandeja_ElDetalleYLaListaDicenLoMismo: la misma solicitud, dos rutas, una sola
// respuesta. Es lo que impide que la marca se calcule en un sitio y se olvide en el
// otro — el modo de fallo de tener dos proyecciones, que aquí se evita con un
// toIntakeDTO compartido y que este test es el que lo comprueba.
func TestBandeja_ElDetalleYLaListaDicenLoMismo(t *testing.T) {
	deps := depsBandeja(bandejaConPlazos())
	api := newAPI(deps, intakesKeys())

	lista := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes", "")
	if lista.Code != http.StatusOK {
		t.Fatalf("listado: code=%d, quiero 200", lista.Code)
	}
	enLista := cabecerasPorID(t, lista.Body.Bytes())

	for _, id := range []string{intakeVencido, intakeEnPlazo} {
		detalle := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes/"+id, "")
		if detalle.Code != http.StatusOK {
			t.Fatalf("detalle de %s: code=%d, quiero 200; body=%s", id, detalle.Code, detalle.Body.String())
		}
		var d struct {
			Overdue bool `json:"overdue"`
		}
		if err := json.Unmarshal(detalle.Body.Bytes(), &d); err != nil {
			t.Fatalf("decodificando el detalle de %s: %v", id, err)
		}
		if d.Overdue != enLista[id].Overdue {
			t.Errorf("%s: overdue en el detalle = %v y en la lista = %v. La marca sale de UNA "+
				"función y de UNA proyección: si difieren, alguien copió la regla",
				id, d.Overdue, enLista[id].Overdue)
		}
	}
}
