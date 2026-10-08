package crmpush

// push_test.go — la FORMA del `intake.push`: Build, los tipos del cable y el schema
// publicado. La regla de Push (gate → INSERT) está en push_pusher_test.go y los dobles
// en push_doubles_test.go, partidos de aquí por tamaño (E-13).
//
// Todo lo de aquí corre siempre y sin Postgres: Build es pura y Push habla con dobles.
// Es un test INTERNO porque comparte dobles con desde_intakes_test.go y con el
// candado, y porque ningún paquete de ayuda importa a éste (no hay ciclo que evitar).

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/EduGoGroup/wapp-shared/logger"
)

// Aserciones de compilación de lo que push.go promete: constantes de cadena, los dos
// puertos con firmas de la biblioteca estándar, los tipos del cable y las firmas.
var (
	_ string = Kind
	_ string = Verb
	_ string = ContractVersion

	_ Queuer = (*fakeQueue)(nil)
	_ Gate   = (*fakeGate)(nil)

	_ = Item{SKU: "", Label: "", Customization: "", Qty: 0, UnitPrice: 0}
	_ = Input{TenantID: "", ContactID: "", IntakeID: "", LifecycleStatus: "", RevisionNo: 0,
		Items: []Item(nil), Total: 0, EventHistoryID: ""}
	_ = Payload{ContractVersion: "", Verb: "", Tenant: "", Contact: "", IntakeID: "",
		LifecycleStatus: "", RevisionNo: 0, Items: []Item(nil), Total: 0, Timestamp: "", EventHistoryID: ""}
	_ = Result{Enqueued: false, OutboxID: int64(0), Payload: Payload{}}

	_ func(Input, time.Time) Payload                        = Build
	_ func(func() time.Time) Option                         = WithClock
	_ func(logger.Logger, Queuer, Gate, ...Option) *Pusher  = NewPusher
	_ func(*Pusher, context.Context, Input) (Result, error) = (*Pusher).Push
	_ Option                                                = func(*Pusher) {}
)

const (
	sampleTenant = "tenant-abc"
	sampleIntake = "11111111-1111-1111-1111-111111111111"
)

// Los estados se escriben a mano —y no con las constantes de intakes— para comparar
// contra el VALOR DE CABLE que el schema declara: un test que compara la constante
// consigo misma pasa aunque alguien le cambie el valor a las dos a la vez.
const (
	statusPendingApproval = "pending_approval"
	statusClosedLegacy    = "closed"
	statusConfirmed       = "confirmed"
)

// contractDir apunta al contrato PUBLICADO, no a una copia: si alguien edita el
// schema o el ejemplo, este test lo ve sin que nadie sincronice nada.
const contractDir = "../../../../../docs/contracts/wapp-crm-v1"

// workerFilledFields son los tres que este documento NO puede congelar
// (D-042.9/D-042.11): buyer_data y variables{} por coste —descifrado y consulta,
// prohibidos en línea con el mensaje por INV-02— y customer_note por EXPOSICIÓN (PII
// en claro en webhook_outbox, una tabla que sobrevive a la entrega y que no se poda).
var workerFilledFields = []string{"buyer_data", "variables", "customer_note"}

// --- muestras ------------------------------------------------------------------

// sampleInput es un empuje completo y VÁLIDO: los dos campos que estuvieron clavados
// traen valores que delatarían un literal —la revisión 4 (no 1) y un estado que NO
// es `confirmed`—.
func sampleInput() Input {
	return Input{
		TenantID:        sampleTenant,
		ContactID:       "contact-opaco-xyz",
		IntakeID:        sampleIntake,
		LifecycleStatus: statusPendingApproval,
		RevisionNo:      4,
		Items: []Item{
			{SKU: "A1", Label: "Café", Customization: "sin azúcar", Qty: 2, UnitPrice: 9.9},
			{SKU: "B2", Label: "Té", Qty: 1, UnitPrice: 5.0},
		},
		Total: 24.8,
	}
}

// wireDoc serializa el documento y lo devuelve crudo y por NOMBRE DE CABLE: si
// alguien renombra una etiqueta json, un decode tipado lo taparía.
func wireDoc(t *testing.T, p Payload) ([]byte, map[string]any) {
	t.Helper()
	body, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("serializar el documento: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("el documento no es JSON válido: %v", err)
	}
	return body, doc
}

// --- Build --------------------------------------------------------------------

// TestConstants_AreTheWireValues: el kind, el verbo y la versión son texto del
// contrato y se afirman literales.
func TestConstants_AreTheWireValues(t *testing.T) {
	if Kind != "intake.push" || Verb != "intake.push" || ContractVersion != "1" {
		t.Fatalf("Kind=%q Verb=%q ContractVersion=%q; quiero intake.push, intake.push y 1",
			Kind, Verb, ContractVersion)
	}
}

// TestBuild_ContractDocument congela la FORMA JSON entera: campos, orden, etiquetas,
// las constantes del contrato y los que NO viajan.
func TestBuild_ContractDocument(t *testing.T) {
	body, _ := wireDoc(t, Build(sampleInput(), fixedClock()))
	want := `{"contract_version":"1","verb":"intake.push","tenant":"tenant-abc","contact":"contact-opaco-xyz",` +
		`"intake_id":"11111111-1111-1111-1111-111111111111","lifecycle_status":"pending_approval","revision_no":4,` +
		`"items":[{"sku":"A1","label":"Café","customization":"sin azúcar","qty":2,"unit_price":9.9},` +
		`{"sku":"B2","label":"Té","customization":"","qty":1,"unit_price":5}],` +
		`"total":24.8,"timestamp":"2026-08-27T10:00:00Z"}`
	if string(body) != want {
		t.Fatalf("el documento no coincide con el contrato\n got: %s\nwant: %s", body, want)
	}
}

// TestBuild_IsPure: mismo Input y mismo instante, mismo documento.
func TestBuild_IsPure(t *testing.T) {
	a, b := Build(sampleInput(), fixedClock()), Build(sampleInput(), fixedClock())
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("dos llamadas iguales dieron documentos distintos:\n%+v\n%+v", a, b)
	}
}

// TestBuild_LifecycleStatusIsTheCallers (R-12): el estado que sale es el que ENTRA,
// no un literal. `closed` es la clave LEGADA del carrito y el contrato prohíbe
// emitirla; `pending_approval` es el caso que el literal `confirmed` arruinaba; y el
// vacío sale vacío — no se inventa un estado.
func TestBuild_LifecycleStatusIsTheCallers(t *testing.T) {
	for _, c := range []struct {
		name  string
		given string
		want  string
	}{
		{"legacy cart close key becomes confirmed", statusClosedLegacy, statusConfirmed},
		{"re-push of a correction stays pending_approval", statusPendingApproval, statusPendingApproval},
		{"canonical confirmed stays confirmed", statusConfirmed, statusConfirmed},
		{"another canonical status is not rewritten", "needs_info", "needs_info"},
		{"absent stays empty", "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := sampleInput()
			in.LifecycleStatus = c.given
			if got := Build(in, fixedClock()).LifecycleStatus; got != c.want {
				t.Fatalf("lifecycle_status = %q, quiero %q", got, c.want)
			}
		})
	}
}

// TestBuild_RevisionNoIsTheCallers (R-12): el número viaja entero, y su AUSENCIA sale
// como 0 —el único valor que el schema rechaza— en vez de como un 1 inventado que el
// puente aplicaría sin sospechar.
func TestBuild_RevisionNoIsTheCallers(t *testing.T) {
	for _, n := range []int{7, 1, 0} {
		in := sampleInput()
		in.RevisionNo = n
		if got := Build(in, fixedClock()).RevisionNo; got != n {
			t.Fatalf("revision_no = %d, quiero %d (el del llamante, sin tocar)", got, n)
		}
	}
}

// TestBuild_CopiesTheCallersFields: lo que no es constante del contrato sale del
// Input tal cual.
func TestBuild_CopiesTheCallersFields(t *testing.T) {
	in := sampleInput()
	p := Build(in, fixedClock())
	if p.Tenant != in.TenantID || p.Contact != in.ContactID || p.IntakeID != in.IntakeID || p.Total != in.Total {
		t.Fatalf("tenant/contact/intake_id/total no son los del Input: %+v", p)
	}
	if !reflect.DeepEqual(p.Items, in.Items) {
		t.Fatalf("las líneas no cruzan iguales y en su orden:\n got: %+v\nwant: %+v", p.Items, in.Items)
	}
}

// TestBuild_NoLinesEmitsAnEmptyList: `items` es requerido por el schema y un nil
// serializa como `null`, que el puente rechaza por un motivo distinto del real.
func TestBuild_NoLinesEmitsAnEmptyList(t *testing.T) {
	for name, items := range map[string][]Item{"nil": nil, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			in := sampleInput()
			in.Items = items
			p := Build(in, fixedClock())
			if p.Items == nil {
				t.Fatal("Items es nil; sin líneas debe ser una lista vacía")
			}
			if body, _ := wireDoc(t, p); !strings.Contains(string(body), `"items":[]`) {
				t.Fatalf("items sin líneas debe salir como [], no como null: %s", body)
			}
		})
	}
}

// TestBuild_DoesNotShareTheCallersSlice: el documento se persiste; que el llamante
// siga tocando su slice después no puede cambiar lo que ya se encoló.
func TestBuild_DoesNotShareTheCallersSlice(t *testing.T) {
	in := sampleInput()
	p := Build(in, fixedClock())
	in.Items[0].Label = "PISADO"
	if p.Items[0].Label != "Café" {
		t.Fatalf("el documento comparte la slice del llamante: items[0].label = %q", p.Items[0].Label)
	}
}

// TestBuild_EventHistoryIDIsOptional: es el único campo OPCIONAL del contrato
// (MD-042.1) — vacío ⇒ la clave NO aparece (no aparece como ""); con valor, viaja.
func TestBuild_EventHistoryIDIsOptional(t *testing.T) {
	_, doc := wireDoc(t, Build(sampleInput(), fixedClock()))
	if _, present := doc["event_history_id"]; present {
		t.Fatalf("event_history_id no debe aparecer mientras esté vacío: %v", doc)
	}
	in := sampleInput()
	in.EventHistoryID = "evt-2026-08-27-1000"
	_, doc = wireDoc(t, Build(in, fixedClock()))
	if doc["event_history_id"] != "evt-2026-08-27-1000" {
		t.Fatalf("event_history_id = %#v, quiero el del Input", doc["event_history_id"])
	}
}

// TestBuild_DoesNotFreezeWhatTheWorkerFills vigila la mitad CONGELADA del reparto por
// NOMBRE DE CABLE. Es estructural —Input no tiene esos campos— y existe para que
// añadirlos «porque ya los tengo a mano» se ponga rojo aquí y no en producción.
func TestBuild_DoesNotFreezeWhatTheWorkerFills(t *testing.T) {
	in := sampleInput()
	in.EventHistoryID = "evt-1"
	body, doc := wireDoc(t, Build(in, fixedClock()))
	for _, k := range workerFilledFields {
		if _, present := doc[k]; present {
			t.Fatalf("%q entró en el documento que se PERSISTE en webhook_outbox. Los tres los "+
				"completa el worker justo antes del POST: congelarlos aquí vuelve a violar INV-02 "+
				"(y, con customer_note, deja PII en claro en una fila que nadie poda):\n%s", k, body)
		}
	}
}

// TestBuild_TimestampIsNowInRFC3339: a segundos y con el desplazamiento que traiga
// `now`; Build no convierte a UTC.
func TestBuild_TimestampIsNowInRFC3339(t *testing.T) {
	caracas := time.FixedZone("-04", -4*60*60)
	for _, c := range []struct {
		name string
		now  time.Time
		want string
	}{
		{"utc", fixedClock(), "2026-08-27T10:00:00Z"},
		{"sub-second is dropped", time.Date(2026, 8, 27, 10, 0, 0, 987654321, time.UTC), "2026-08-27T10:00:00Z"},
		{"offset is kept, not converted", time.Date(2026, 8, 27, 6, 0, 0, 0, caracas), "2026-08-27T06:00:00-04:00"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := Build(sampleInput(), c.now).Timestamp; got != c.want {
				t.Fatalf("timestamp = %q, quiero %q", got, c.want)
			}
		})
	}
}

// --- el schema publicado --------------------------------------------------------

func compileSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	sch, err := jsonschema.NewCompiler().Compile(filepath.Join(contractDir, "intake.push.schema.json"))
	if err != nil {
		t.Fatalf("compilar intake.push.schema.json: %v", err)
	}
	return sch
}

// loadExample lee el ejemplo publicado; cada llamada devuelve un mapa nuevo, así que
// la mutación de un subtest no contamina a otro.
func loadExample(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(contractDir, "examples", "intake.push.json")) // #nosec G304 -- ruta fija de test
	if err != nil {
		t.Fatalf("leer el ejemplo publicado: %v", err)
	}
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("parsear el ejemplo publicado: %v", err)
	}
	return v
}

// deliveredDoc es el documento de Build MÁS lo que el worker le añade antes del POST,
// con los valores mínimos que el contrato admite (sin checklist, sin variables, sin
// indicación).
func deliveredDoc(t *testing.T, in Input) map[string]any {
	t.Helper()
	_, doc := wireDoc(t, Build(in, fixedClock()))
	doc["buyer_data"] = map[string]any{}
	doc["variables"] = map[string]any{}
	doc["customer_note"] = ""
	return doc
}

// TestBuild_PayloadValidatesAgainstThePublishedSchema: lo que Build congela, más los
// tres campos del worker, ES un `intake.push` válido — también cuando la fila venía
// con la clave legada `closed` (que el schema rechaza y Build normaliza), sin líneas
// o con el campo opcional.
func TestBuild_PayloadValidatesAgainstThePublishedSchema(t *testing.T) {
	sch := compileSchema(t)
	for name, mutate := range map[string]func(*Input){
		"sample":                 func(*Input) {},
		"legacy closed status":   func(in *Input) { in.LifecycleStatus = statusClosedLegacy },
		"no lines":               func(in *Input) { in.Items = nil; in.Total = 0 },
		"with event_history_id":  func(in *Input) { in.EventHistoryID = "evt-1" },
		"first revision of cart": func(in *Input) { in.RevisionNo = 1; in.LifecycleStatus = statusConfirmed },
	} {
		t.Run(name, func(t *testing.T) {
			in := sampleInput()
			mutate(&in)
			if err := sch.Validate(deliveredDoc(t, in)); err != nil {
				t.Fatalf("el documento de Build, completado por el worker, NO valida contra el schema: %v", err)
			}
		})
	}
}

// TestBuild_PayloadAloneIsNotADeliverableDocument: los tres del worker son REQUERIDOS
// por el schema. Lo que se encola no es lo que se entrega, y falta de cualquiera de
// los tres lo delata el schema.
func TestBuild_PayloadAloneIsNotADeliverableDocument(t *testing.T) {
	sch := compileSchema(t)
	for _, k := range workerFilledFields {
		t.Run("without "+k, func(t *testing.T) {
			doc := deliveredDoc(t, sampleInput())
			delete(doc, k)
			if err := sch.Validate(doc); err == nil {
				t.Fatalf("sin %q el documento debía ser rechazado por el schema y no lo fue", k)
			}
		})
	}
}

// TestBuild_AbsentKeyFieldsAreVisiblyRejectedBySchema (R-12): no inventar el número ni
// el estado solo sirve si su ausencia se NOTA. El 0 y el vacío son justo los valores
// que el schema rechaza.
func TestBuild_AbsentKeyFieldsAreVisiblyRejectedBySchema(t *testing.T) {
	sch := compileSchema(t)
	for name, mutate := range map[string]func(*Input){
		"absent revision_no":      func(in *Input) { in.RevisionNo = 0 },
		"absent lifecycle_status": func(in *Input) { in.LifecycleStatus = "" },
	} {
		t.Run(name, func(t *testing.T) {
			in := sampleInput()
			mutate(&in)
			if err := sch.Validate(deliveredDoc(t, in)); err == nil {
				t.Fatal("el schema debía rechazar el documento y lo aceptó: la ausencia pasaría por un estado legítimo")
			}
		})
	}
}

// TestPayload_IsTheFrozenHalfOfThePublishedExample: el ejemplo publicado valida, y
// quitándole los tres campos del worker cabe EXACTAMENTE en Payload — ni una clave
// del contrato sin campo, ni un campo sin clave.
func TestPayload_IsTheFrozenHalfOfThePublishedExample(t *testing.T) {
	example := loadExample(t)
	if err := compileSchema(t).Validate(example); err != nil {
		t.Fatalf("el ejemplo publicado NO valida contra su schema: %v", err)
	}
	for _, k := range workerFilledFields {
		if _, present := example[k]; !present {
			t.Fatalf("el ejemplo publicado ya no trae %q: el contrato cambió", k)
		}
		delete(example, k)
	}
	frozen, err := json.Marshal(example)
	if err != nil {
		t.Fatalf("re-serializar el ejemplo: %v", err)
	}
	dec := json.NewDecoder(strings.NewReader(string(frozen)))
	dec.DisallowUnknownFields()
	var p Payload
	if err := dec.Decode(&p); err != nil {
		t.Fatalf("la mitad congelada del ejemplo no cabe en Payload: %v", err)
	}
	_, back := wireDoc(t, p)
	if !reflect.DeepEqual(back, example) {
		t.Fatalf("Payload no reproduce la mitad congelada del ejemplo:\n got: %v\nwant: %v", back, example)
	}
}

// TestIntakePushSchema_NegativeCases: cada mutación del ejemplo publicado debe romper
// la validación. Si alguna pasa, el schema dejó de proteger lo que este paquete da
// por hecho (que `closed` y el `revision_no` 0 se rechazan, que no hay moneda ni
// teléfono).
func TestIntakePushSchema_NegativeCases(t *testing.T) {
	sch := compileSchema(t)
	for name, mutate := range map[string]func(t *testing.T, v map[string]any){
		"total as string":                    func(_ *testing.T, v map[string]any) { v["total"] = "24" },
		"lifecycle_status outside the enum":  func(_ *testing.T, v map[string]any) { v["lifecycle_status"] = statusClosedLegacy },
		"empty lifecycle_status":             func(_ *testing.T, v map[string]any) { v["lifecycle_status"] = "" },
		"undeclared currency (INV-09)":       func(_ *testing.T, v map[string]any) { v["currency"] = "CLP" },
		"reserved contact_phone not emitted": func(_ *testing.T, v map[string]any) { v["contact_phone"] = "+56912345678" },
		"revision_no below the minimum":      func(_ *testing.T, v map[string]any) { v["revision_no"] = float64(0) },
		"items as null":                      func(_ *testing.T, v map[string]any) { v["items"] = nil },
		"customer_note missing (required)":   func(_ *testing.T, v map[string]any) { delete(v, "customer_note") },
		"variables missing (required)":       func(_ *testing.T, v map[string]any) { delete(v, "variables") },
		"buyer_data missing (required)":      func(_ *testing.T, v map[string]any) { delete(v, "buyer_data") },
		"items[].qty below the minimum": func(t *testing.T, v map[string]any) {
			items, ok := v["items"].([]any)
			if !ok || len(items) == 0 {
				t.Fatal("el ejemplo intake.push no trae items[] o no es un array")
			}
			first, ok := items[0].(map[string]any)
			if !ok {
				t.Fatal("items[0] del ejemplo intake.push no es un objeto")
			}
			first["qty"] = float64(0)
		},
	} {
		t.Run(name, func(t *testing.T) {
			v := loadExample(t)
			mutate(t, v)
			if err := sch.Validate(v); err == nil {
				t.Fatalf("la mutación %q debía romper la validación y no lo hizo", name)
			}
		})
	}
}
