package publicapi_test

// intakes_approve_test.go — POST /api/v1/intakes/{id}/approve (Plan 044 · T4.3).
//
// La puerta del dueño: los códigos del contrato, el gate comercial y —lo que de
// verdad importa— que un 200 signifique «se aprobó Y se le contó al cliente».

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

// intakePorAprobar es el presupuesto de la escena: en `pending_approval`, con su
// línea precificada.
const intakePorAprobar = "88888888-8888-8888-8888-888888888888"

// cotizaciónDelDueño es el texto que viaja en el cuerpo.
const cotizaciónDelDueño = "Hola! Tu pedido: 1 torta $18.000. ¿Te sirve para el miércoles?"

// canalFalso satisface intakes.QuoteSender sin tocar WhatsApp: compone pegando un
// sufijo reconocible y retiene lo entregado. No imita al Notifier real —eso ya lo
// prueban los tests de su paquete—; aquí solo hace falta poder afirmar que el
// handler SÍ hace salir un mensaje.
type canalFalso struct {
	mu       sync.Mutex
	sufijo   string
	enviados []string
}

func (c *canalFalso) QuoteText(_ context.Context, _ string, _ intakes.Intake, ownerText string) string {
	return ownerText + c.sufijo
}

func (c *canalFalso) SendQuote(_ context.Context, _ string, _ intakes.Intake, text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.enviados = append(c.enviados, text)
}

func (c *canalFalso) mensajes() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.enviados...)
}

// bandejaPorAprobar siembra la solicitud lista para aprobar.
func bandejaPorAprobar() *intakes.MemoryStore {
	st := intakes.NewMemoryStore()
	st.Add(tenantA, intakes.Intake{
		ID: intakePorAprobar, ContactID: contactoOpaco, SessionID: "sess-a",
		Status: intakes.StatusPendingApproval, Total: 18000,
		CreatedAt: día(6), UpdatedAt: día(6),
	}, intakes.Item{SKU: "torta-v1", Label: "Torta 10-12 porciones", Qty: 1, UnitPrice: 18000})
	return st
}

// depsAprobar arma unas Deps con `cart_basic` y SIN `llm_intake`: es el tenant
// `Basic`, que existe de verdad en UAT, y es la condición que D-044.49 §3 exige que
// funcione entera.
func depsAprobar(st *intakes.MemoryStore, canal intakes.QuoteSender) publicapi.Deps {
	fake := entitlements.NewFake()
	fake.Enable(tenantA, entitlements.FeatureCartBasic)
	return publicapi.Deps{
		Intakes:      intakes.NewService(st, intakes.WithQuoteSender(canal)),
		Entitlements: fake,
	}
}

func aprobar(t *testing.T, api *testAPI, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	return call(api, keyAIntakes, http.MethodPost, "/api/v1/intakes/"+id+"/approve", body)
}

// TestApprove_200_ApruebaResponde: el camino feliz completo por HTTP, con un tenant
// `Basic` (cart_basic, sin llm_intake). Ni un 403 por el camino — que es el criterio
// entero de D-044.49 §3.
func TestApprove_200_ApruebaYResponde(t *testing.T) {
	canal := &canalFalso{sufijo: "\n\nAbona la seña a la cuenta 123-4."}
	st := bandejaPorAprobar()
	api := newAPI(depsAprobar(st, canal), intakesKeys())

	rec := aprobar(t, api, intakePorAprobar, `{"rendered_text":`+strconv.Quote(cotizaciónDelDueño)+`}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	detalle := decodeDetalle(t, rec.Body.Bytes())
	if detalle.Status != intakes.StatusConfirmed {
		t.Fatalf("status=%q, quiero confirmed", detalle.Status)
	}
	if len(detalle.Revisions) != 1 {
		t.Fatalf("revisiones=%d, quiero 1 (la aprobación)", len(detalle.Revisions))
	}
	rev := detalle.Revisions[0]
	if rev.Kind != intakes.RevisionKindApproved || rev.CreatedBy != intakes.RevisionByOwner {
		t.Fatalf("revisión kind=%q created_by=%q; quiero approved/owner", rev.Kind, rev.CreatedBy)
	}

	// Lo que el 200 tiene que significar: el cliente recibió el mensaje del dueño, y
	// lo que quedó escrito es exactamente eso.
	msgs := canal.mensajes()
	if len(msgs) != 1 {
		t.Fatalf("mensajes al cliente = %d, quiero 1", len(msgs))
	}
	if !strings.HasPrefix(msgs[0], cotizaciónDelDueño) {
		t.Fatalf("el texto del dueño no salió íntegro: %q", msgs[0])
	}
	if rev.RenderedText != msgs[0] {
		t.Fatalf("rendered_text=%q pero se envió %q", rev.RenderedText, msgs[0])
	}
}

// TestApprove_400_SinRenderedText: el cuerpo es obligatorio (D-044.49). Un `{}` por
// un fallo de la UI no puede confirmar el pedido en silencio.
func TestApprove_400_SinRenderedText(t *testing.T) {
	canal := &canalFalso{}
	st := bandejaPorAprobar()
	api := newAPI(depsAprobar(st, canal), intakesKeys())

	for _, body := range []string{`{}`, `{"rendered_text":""}`, `{"rendered_text":"   "}`} {
		rec := aprobar(t, api, intakePorAprobar, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body=%s → code=%d, quiero 400; resp=%s", body, rec.Code, rec.Body.String())
		}
	}
	if len(canal.mensajes()) != 0 {
		t.Fatal("un 400 no puede haberle mandado nada al cliente")
	}
}

// TestApprove_400_LíneasSinPrecio: la precondición de la tarea, con su detalle. El
// cuerpo dice CUÁLES faltan, con su posición — la línea `unmatched` no tiene sku, así
// que el índice es lo único que la identifica.
func TestApprove_400_LíneasSinPrecio(t *testing.T) {
	canal := &canalFalso{}
	st := bandejaPorAprobar()
	if _, err := st.InsertRevision(context.Background(), intakes.Revision{
		IntakeID: intakePorAprobar, Kind: intakes.RevisionKindInterpreted,
		CreatedBy: intakes.RevisionBySystem,
		Payload: json.RawMessage(`{"version":1,"lines":[
			{"kind":"matched","sku":"torta-v1","label":"Torta 10-12 porciones","qty":1,"unit_price":18000},
			{"kind":"unmatched","label":"Torta vainilla 25-30 porciones","qty":1,"unit_price":null}]}`),
	}); err != nil {
		t.Fatalf("sembrar la revisión interpretada: %v", err)
	}
	api := newAPI(depsAprobar(st, canal), intakesKeys())

	rec := aprobar(t, api, intakePorAprobar, `{"rendered_text":"lo que sea"}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d, quiero 400; body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error string `json:"error"`
		Lines []struct {
			Index int    `json:"index"`
			Label string `json:"label"`
		} `json:"lines"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal del 400: %v; body=%s", err, rec.Body.String())
	}
	if body.Error != "lines_without_price" {
		t.Fatalf("error=%q, quiero lines_without_price", body.Error)
	}
	if len(body.Lines) != 1 || body.Lines[0].Index != 1 || body.Lines[0].Label != "Torta vainilla 25-30 porciones" {
		t.Fatalf("el detalle del 400 no dice cuál falta: %+v", body.Lines)
	}
	if len(canal.mensajes()) != 0 {
		t.Fatal("un 400 no puede haberle mandado nada al cliente")
	}
}

// TestApprove_422_NoEstáPorAprobar: aprobar es la acción sobre el presupuesto. El
// cuerpo dice dónde está y desde dónde sí se aprueba, para que la consola no tenga
// que adivinar.
func TestApprove_422_NoEstáPorAprobar(t *testing.T) {
	canal := &canalFalso{}
	st := bandejaPorAprobar()
	api := newAPI(depsAprobar(st, canal), intakesKeys())

	// Se aprueba una vez (queda `confirmed`) y se reintenta: es el caso REAL, dos
	// pestañas abiertas o un doble clic.
	if rec := aprobar(t, api, intakePorAprobar, `{"rendered_text":"la primera"}`); rec.Code != http.StatusOK {
		t.Fatalf("la primera aprobación tiene que ir bien; code=%d body=%s", rec.Code, rec.Body.String())
	}
	rec := aprobar(t, api, intakePorAprobar, `{"rendered_text":"la segunda"}`)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("code=%d, quiero 422; body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error        string   `json:"error"`
		Status       string   `json:"status"`
		ApprovableIn []string `json:"approvable_in"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal del 422: %v; body=%s", err, rec.Body.String())
	}
	if body.Error != "not_approvable" || body.Status != intakes.StatusConfirmed {
		t.Fatalf("cuerpo del 422 = %+v; quiero not_approvable sobre confirmed", body)
	}
	if len(body.ApprovableIn) != 1 || body.ApprovableIn[0] != intakes.StatusPendingApproval {
		t.Fatalf("approvable_in=%v, quiero [pending_approval]", body.ApprovableIn)
	}
	// Y el cliente recibió UN mensaje, no dos: el segundo intento no llegó a hablar.
	if got := len(canal.mensajes()); got != 1 {
		t.Fatalf("mensajes al cliente = %d tras dos POST, quiero 1", got)
	}
}

// TestApprove_404_SolicitudAjena: 404 opaco, nunca 403 — un 403 confirmaría que el
// id existe en otro tenant (INV-8).
func TestApprove_404_SolicitudAjena(t *testing.T) {
	api := newAPI(depsAprobar(bandejaPorAprobar(), &canalFalso{}), intakesKeys())
	rec := aprobar(t, api, "99999999-9999-9999-9999-999999999999", `{"rendered_text":"hola"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d, quiero 404; body=%s", rec.Code, rec.Body.String())
	}
}

// TestApprove_403_SinFeature: el gate comercial es `cart_basic` (D-044.49 §3), el
// mismo que abre la bandeja. Sin plan, la puerta no se abre.
func TestApprove_403_SinFeature(t *testing.T) {
	d := publicapi.Deps{
		Intakes:      intakes.NewService(bandejaPorAprobar(), intakes.WithQuoteSender(&canalFalso{})),
		Entitlements: entitlements.NewFake(), // ninguna feature encendida
	}
	rec := aprobar(t, newAPI(d, intakesKeys()), intakePorAprobar, `{"rendered_text":"hola"}`)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("code=%d, quiero 403; body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal del 403: %v; body=%s", err, rec.Body.String())
	}
	if body["feature"] != entitlements.FeatureCartBasic {
		t.Fatalf("el 403 pide %q; el gate de approve es cart_basic y NO llm_intake (D-044.49 §3)", body["feature"])
	}
}

// TestApprove_403_SinScope: con la feature pero sin `intakes.write`, la escritura no
// pasa. Son dos guardias y ninguno sustituye al otro.
func TestApprove_403_SinScope(t *testing.T) {
	api := newAPI(depsAprobar(bandejaPorAprobar(), &canalFalso{}), intakesKeys())
	rec := call(api, keyARead, http.MethodPost, "/api/v1/intakes/"+intakePorAprobar+"/approve",
		`{"rendered_text":"hola"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code=%d, quiero 403 sin el scope; body=%s", rec.Code, rec.Body.String())
	}
}
