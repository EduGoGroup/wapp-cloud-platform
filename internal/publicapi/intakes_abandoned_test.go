package publicapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// T4.6 · PUBLICAR `abandoned`. La máquina de estados ya sabía abandonar antes de
// esta tarea (constante, transición `open → abandoned` y alta en `known`); lo que
// estos tests fijan es lo que hace que el estado SIRVA para algo: que la bandeja lo
// filtre, que salga en los DOS formatos del export y que cuente en el desglose del
// summary. Sin esos cuatro caminos, el Plan 043 podría abandonar solicitudes que
// después nadie sería capaz de encontrar.
//
// Cada camino lleva su test porque son consultas y serializadores DISTINTOS:
// llevarlo en el CSV no dice nada del XLSX —otro escritor, celda a celda— ni del
// summary, que se construye campo a campo en su propio DTO.

// intakeA7 es la solicitud que estos tests abandonan. Vive FUERA de seedIntakes,
// igual que la intakeA6 vencida, porque la bandeja común es el fixture de una
// docena de tests y sus conteos son goldens: añadirle una sexta solicitud los
// rompería todos para probar algo que solo necesita esta tarea.
const intakeA7 = "77777777-7777-7777-7777-777777777777"

// seedAbandonable siembra, sobre la bandeja de siempre, una solicitud ABIERTA con
// dos líneas —una personalizada— y una revisión. Las tres cosas importan: el
// criterio de T4.6 no es solo que el estado se filtre, sino que abandonar NO borre
// lo que se había negociado.
func seedAbandonable(t *testing.T) *intakes.MemoryStore {
	t.Helper()
	st := seedIntakes()
	st.Add(tenantA, intakes.Intake{
		ID: intakeA7, ContactID: contactoOpaco, SessionID: "sess-a",
		Status: intakes.StatusOpen, Total: 21000,
		CreatedAt: día(7), UpdatedAt: día(7), CustomerNote: "para el sábado",
	},
		intakes.Item{SKU: "torta-v1", Label: "Torta 10-12 porciones",
			Customization: "sin sal", Qty: 1, UnitPrice: 18000},
		intakes.Item{SKU: "vela-num", Label: "Vela número 3", Qty: 1, UnitPrice: 3000})

	if _, err := st.InsertRevision(context.Background(), intakes.Revision{
		IntakeID: intakeA7, Kind: intakes.RevisionKindCart,
		Payload:   json.RawMessage(`{"version":1,"items":[{"sku":"torta-v1","qty":1}]}`),
		CreatedBy: intakes.RevisionBySystem,
	}); err != nil {
		t.Fatalf("sembrando la revisión de %s: %v", intakeA7, err)
	}
	return st
}

// abandonar lleva la A7 de `open` a `abandoned` por la MISMA puerta que invocará el
// Plan 043 al cancelar el evento: el endpoint de transición. Fabricar la fila ya
// abandonada en el store probaría el filtro pero no que se pueda LLEGAR ahí, que es
// la mitad del criterio.
func abandonar(t *testing.T, api *testAPI) {
	t.Helper()
	rec := call(api, keyAIntakes, http.MethodPost,
		"/api/v1/intakes/"+intakeA7+"/status", `{"status":"abandoned"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("abandonar %s → code=%d, quiero 200; body=%s", intakeA7, rec.Code, rec.Body.String())
	}
	var dto intakeDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("unmarshal de la transición: %v; body=%s", err, rec.Body.String())
	}
	if dto.Status != intakes.StatusAbandoned {
		t.Fatalf("tras la transición status=%q, quiero abandoned", dto.Status)
	}
}

// TestIntakes_Abandoned_FiltroDelListado es el PRIMER camino: la bandeja filtrada
// por `?status=abandoned` la encuentra, y el mismo movimiento la saca del filtro
// `open`. Las dos mitades van juntas a propósito: un filtro que devolviera la fila
// en los dos cubos a la vez pasaría la primera comprobación y mentiría igual.
func TestIntakes_Abandoned_FiltroDelListado(t *testing.T) {
	api := newAPI(exportDeps(seedAbandonable(t)), intakesKeys())

	// Antes de abandonarla está entre las abiertas (A7 es más reciente que A2).
	abiertas := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes?status=open", "")
	if got := idsDe(decodeList(t, abiertas.Body.Bytes())); !slices.Equal(got, []string{intakeA7, intakeA2}) {
		t.Fatalf("abiertas antes=%v, quiero [%s %s]", got, intakeA7, intakeA2)
	}

	abandonar(t, api)

	rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes?status=abandoned", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	lista := decodeList(t, rec.Body.Bytes())
	if got := idsDe(lista); !slices.Equal(got, []string{intakeA7}) {
		t.Fatalf("status=abandoned devuelve %v, quiero [%s]", got, intakeA7)
	}
	if lista.Total != 1 {
		t.Fatalf("total=%d, quiero 1: el paginador cuenta las coincidencias del filtro", lista.Total)
	}
	if got := lista.Intakes[0].Status; got != intakes.StatusAbandoned {
		t.Fatalf("status en el wire=%q, quiero abandoned", got)
	}

	// Y ya no está entre las abiertas: la bandeja del dueño deja de ofrecerla como
	// pendiente, que es justamente para lo que el Plan 043 abandona.
	abiertas = call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes?status=open", "")
	if got := idsDe(decodeList(t, abiertas.Body.Bytes())); !slices.Equal(got, []string{intakeA2}) {
		t.Fatalf("abiertas después=%v, quiero solo [%s]", got, intakeA2)
	}
}

// TestIntakes_Abandoned_ConservaLíneasYRevisiones: abandonar cambia el ESTADO y
// nada más. Las dos líneas y la revisión siguen ahí, y la solicitud no ofrece
// ningún destino —es terminal— para que la consola no pinte un selector con
// acciones imposibles.
func TestIntakes_Abandoned_ConservaLíneasYRevisiones(t *testing.T) {
	api := newAPI(exportDeps(seedAbandonable(t)), intakesKeys())
	abandonar(t, api)

	rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes/"+intakeA7, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	var detalle intakeDetailDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &detalle); err != nil {
		t.Fatalf("unmarshal del detalle: %v; body=%s", err, rec.Body.String())
	}

	if detalle.Status != intakes.StatusAbandoned {
		t.Fatalf("status=%q, quiero abandoned", detalle.Status)
	}
	if len(detalle.Items) != 2 {
		t.Fatalf("items=%+v; abandonar no borra las líneas", detalle.Items)
	}
	if detalle.Items[0].Customization != "sin sal" {
		t.Fatalf("la personalización se perdió al abandonar: %+v", detalle.Items[0])
	}
	if detalle.CustomerNote != "para el sábado" {
		t.Fatalf("customer_note=%q; la indicación del cliente sobrevive", detalle.CustomerNote)
	}
	if len(detalle.Revisions) != 1 || detalle.Revisions[0].Kind != intakes.RevisionKindCart {
		t.Fatalf("revisions=%+v; la negociación auditada sigue ahí", detalle.Revisions)
	}
	// Terminal: `[]`, nunca `null` — "no hay acciones" y "no sé" pintan distinto.
	if detalle.AllowedTransitions == nil || len(detalle.AllowedTransitions) != 0 {
		t.Fatalf("allowed_transitions=%v; abandoned es terminal y devuelve lista vacía",
			detalle.AllowedTransitions)
	}
}

// TestIntakesExport_CSV_Abandoned es el SEGUNDO camino: el archivo que abre quien
// no usa la consola. Se compara byte a byte contra el golden —con la cabecera
// canónica y el BOM delante— porque contar filas dejaría pasar un estado sin
// normalizar o una columna corrida, que es lo que arruina la hoja al abrirla.
func TestIntakesExport_CSV_Abandoned(t *testing.T) {
	api := newAPI(exportDeps(seedAbandonable(t)), intakesKeys())
	abandonar(t, api)

	rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes/export?status=abandoned", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}

	const cabecera = "77777777-7777-7777-7777-777777777777,2026-08-07T12:00:00Z,abandoned,sess-a," +
		"9f1c0a7e-0000-4000-8000-000000000abc,21000,para el sábado"
	quiero := csvGolden(
		csvHeader,
		cabecera+",torta-v1,Torta 10-12 porciones,sin sal,1,18000,18000",
		cabecera+",vela-num,Vela número 3,,1,3000,3000",
	)
	if got := rec.Body.String(); got != quiero {
		t.Fatalf("CSV distinto del golden.\n got=%q\nquiero=%q", got, quiero)
	}
	// El BOM sigue delante: sin él, Excel en español destroza «sábado» y «número».
	if !strings.HasPrefix(rec.Body.String(), "\xef\xbb\xbf") {
		t.Fatal("el CSV de la solicitud abandonada perdió el BOM")
	}
}

// TestIntakesExport_XLSX_Abandoned es el TERCER camino. El libro se REABRE con
// excelize —lo mismo que hace Excel— porque el XLSX lo escribe otro serializador:
// que el CSV lleve el estado no prueba nada de la celda.
func TestIntakesExport_XLSX_Abandoned(t *testing.T) {
	api := newAPI(exportDeps(seedAbandonable(t)), intakesKeys())
	abandonar(t, api)

	rec := call(api, keyAIntakes, http.MethodGet,
		"/api/v1/intakes/export?format=xlsx&status=abandoned", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}

	filas := leerXLSX(t, rec.Body.Bytes())
	if len(filas) != 3 {
		t.Fatalf("filas=%d, quiero 3 (cabecera + dos líneas); filas=%v", len(filas), filas)
	}
	if got := strings.Join(filas[0], ","); got != csvHeader {
		t.Fatalf("cabecera del XLSX=%q", got)
	}

	// El estado se busca por ÍNDICE de la cabecera y no por posición fija: así el
	// test sigue diciendo la verdad cuando otra tarea añada una columna.
	iEstado := slices.Index(filas[0], "status")
	if iEstado < 0 {
		t.Fatalf("la cabecera del XLSX no trae `status`: %v", filas[0])
	}
	for i, fila := range filas[1:] {
		if fila[iEstado] != intakes.StatusAbandoned {
			t.Fatalf("fila %d: status=%q, quiero abandoned", i+1, fila[iEstado])
		}
		if fila[0] != intakeA7 {
			t.Fatalf("fila %d: intake_id=%q, quiero %s", i+1, fila[0], intakeA7)
		}
	}
	// Las dos líneas viajaron enteras, con sus tildes y su personalización.
	iCustom, iTotal := slices.Index(filas[0], "customization"), slices.Index(filas[0], "line_total")
	if filas[1][iCustom] != "sin sal" || filas[2][iCustom] != "" {
		t.Fatalf("customization=%q/%q; quiero «sin sal» y vacía", filas[1][iCustom], filas[2][iCustom])
	}
	if filas[2][slices.Index(filas[0], "label")] != "Vela número 3" {
		t.Fatalf("la etiqueta acentuada llegó como %q", filas[2][slices.Index(filas[0], "label")])
	}
	if filas[1][iTotal] != "18000" || filas[2][iTotal] != "3000" {
		t.Fatalf("line_total=%q/%q; abandonar no toca el dinero", filas[1][iTotal], filas[2][iTotal])
	}
}

// TestIntakeSummary_Abandoned es el CUARTO camino: el desglose por estado que el
// dueño le pega a un LLM. Se afirma el mapa ENTERO y no solo la clave nueva: un
// `abandoned` que apareciera SIN desaparecer de `open` contaría la misma solicitud
// dos veces y el resumen sumaría más pedidos de los que hubo.
func TestIntakeSummary_Abandoned(t *testing.T) {
	api := newAPI(exportDeps(seedAbandonable(t)), intakesKeys())
	abandonar(t, api)

	rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes/summary.json", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	sum := decodeSummary(t, rec.Body.Bytes())

	quiero := map[string]int{"confirmed": 3, "open": 1, "cancelled": 1, "abandoned": 1}
	if len(sum.Totals.ByStatus) != len(quiero) {
		t.Fatalf("by_status=%v, quiero %v", sum.Totals.ByStatus, quiero)
	}
	for estado, n := range quiero {
		if sum.Totals.ByStatus[estado] != n {
			t.Fatalf("by_status[%s]=%d, quiero %d (by_status=%v)",
				estado, sum.Totals.ByStatus[estado], n, sum.Totals.ByStatus)
		}
	}
	if sum.Totals.Intakes != 6 {
		t.Fatalf("totals.intakes=%d, quiero 6: abandonar no saca la solicitud del resumen",
			sum.Totals.Intakes)
	}

	// Y sigue en el detalle crudo, con sus líneas: el LLM del dueño tiene que poder
	// contar qué se abandonó y con qué dentro, no solo cuántas.
	for _, in := range sum.Intakes {
		if in.ID != intakeA7 {
			continue
		}
		if in.Status != intakes.StatusAbandoned {
			t.Fatalf("status en el detalle crudo=%q, quiero abandoned", in.Status)
		}
		if len(in.Items) != 2 || in.Items[0].Customization != "sin sal" {
			t.Fatalf("items de la abandonada=%+v; quiero las dos líneas con su personalización", in.Items)
		}
		return
	}
	t.Fatalf("el summary no trae la solicitud abandonada %s", intakeA7)
}
