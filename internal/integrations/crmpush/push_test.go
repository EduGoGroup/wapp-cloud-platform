package crmpush

// push_test.go — la regla del `intake.push`, probada donde vive y sin Postgres.
//
// 🔴 POR QUÉ ES UN TEST INTERNO Y NO UNO DE INTEGRACIÓN. Los TestPostgres_* de este
// repo se SALTAN sin DATABASE_URL y el `rc` sigue siendo 0: si lo único que probara
// esta pieza fuera un test de integración, en la práctica no la probaría nadie. Todo
// lo de aquí corre siempre: Build es pura y Push habla con dos dobles.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"
)

const (
	tenantDePrueba = "tenant-abc"
	intakeDePrueba = "11111111-1111-1111-1111-111111111111"
)

func logDescartado() logger.Logger { return logger.New(logger.WithWriter(io.Discard)) }

// relojFijo congela el `timestamp` del contrato para poder comparar el documento
// entero contra un literal.
func relojFijo() func() time.Time {
	return func() time.Time { return time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC) }
}

// gateFalso deja pasar/bloquea por tenant; sin entrada trata como cerrado
// (fail-closed, mismo criterio que el gate real).
type gateFalso struct {
	abiertos map[string]bool
	err      error
}

func (g *gateFalso) Enabled(_ context.Context, tenantID string) (bool, error) {
	if g.err != nil {
		return false, g.err
	}
	return g.abiertos[tenantID], nil
}

// colaFalsa graba cada INSERT para que el test inspeccione tenant/kind/cuerpo sin
// tocar Postgres.
type colaFalsa struct {
	llamadas []llamadaCola
	err      error
}

type llamadaCola struct {
	tenantID string
	kind     string
	payload  json.RawMessage
}

func (q *colaFalsa) EnqueueWebhook(_ context.Context, tenantID, kind string, payload json.RawMessage) (int64, error) {
	if q.err != nil {
		return 0, q.err
	}
	q.llamadas = append(q.llamadas, llamadaCola{tenantID: tenantID, kind: kind, payload: payload})
	return int64(len(q.llamadas)), nil
}

// entradaDePrueba es un empuje completo y VÁLIDO: los dos campos que estuvieron
// clavados traen valores que delatarían un literal —la revisión 4 (no 1) y un
// estado que NO es `confirmed`—.
func entradaDePrueba() Input {
	return Input{
		TenantID:        tenantDePrueba,
		ContactID:       "contact-opaco-xyz",
		IntakeID:        intakeDePrueba,
		LifecycleStatus: estadoPendingApproval,
		RevisionNo:      4,
		Items: []Item{
			{SKU: "A1", Label: "Café", Customization: "sin azúcar", Qty: 2, UnitPrice: 9.9},
			{SKU: "B2", Label: "Té", Qty: 1, UnitPrice: 5.0},
		},
		Total: 24.8,
	}
}

// Se escriben los estados a mano —y no con intakes.Status*— para que este test
// compare contra el VALOR DE CABLE que el schema declara, no contra la constante
// que el código usa: un test que compara la constante consigo misma pasa aunque
// alguien le cambie el valor a las dos a la vez.
const (
	estadoPendingApproval = "pending_approval"
	estadoClosedLegacy    = "closed"
	estadoConfirmed       = "confirmed"
)

// TestBuild_DocumentoDelContrato congela la FORMA JSON entera (§3 del contrato
// wapp-crm-v1): campos, orden, etiquetas y los tres que NO viajan.
func TestBuild_DocumentoDelContrato(t *testing.T) {
	body, err := json.Marshal(Build(entradaDePrueba(), relojFijo()()))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"contract_version":"1","verb":"intake.push","tenant":"tenant-abc","contact":"contact-opaco-xyz",` +
		`"intake_id":"11111111-1111-1111-1111-111111111111","lifecycle_status":"pending_approval","revision_no":4,` +
		`"items":[{"sku":"A1","label":"Café","customization":"sin azúcar","qty":2,"unit_price":9.9},` +
		`{"sku":"B2","label":"Té","customization":"","qty":1,"unit_price":5}],` +
		`"total":24.8,"timestamp":"2026-08-27T10:00:00Z"}`
	if string(body) != want {
		t.Fatalf("el documento no coincide con el contrato\n got: %s\nwant: %s", body, want)
	}
}

// TestBuild_LifecycleStatusEsElDelLlamante es la mitad 2 de T4.10 en una frase: el
// estado que sale es el que ENTRA, no un literal.
//
// Los tres casos son los tres que importan:
//
//   - `closed` ⇒ `confirmed`. Es la clave LEGADA con la que cart cierra la fila
//     (Plan 016) y el contrato PROHÍBE emitirla («El contrato JAMÁS emite closed»).
//     Normalizar aquí es lo que hace que ninguna puerta pueda saltárselo.
//   - `pending_approval` ⇒ `pending_approval`. Es el caso que el literal ARRUINABA:
//     el re-empuje de una corrección devuelve la solicitud a ese estado, y un
//     `"confirmed"` clavado le contaría al CRM justo lo contrario.
//   - ausente ⇒ vacío. NO se inventa un estado: el schema declara un enum y el vacío
//     no está en él, así que el puente lo rechaza de forma VISIBLE en vez de aplicar
//     una mentira que nadie va a notar (mismo criterio que revision_no ⇒ 0).
func TestBuild_LifecycleStatusEsElDelLlamante(t *testing.T) {
	for _, c := range []struct {
		nombre string
		dado   string
		quiero string
	}{
		{"cierre de carrito (clave legada)", estadoClosedLegacy, estadoConfirmed},
		{"re-empuje de una corrección", estadoPendingApproval, estadoPendingApproval},
		{"sin estado", "", ""},
	} {
		t.Run(c.nombre, func(t *testing.T) {
			in := entradaDePrueba()
			in.LifecycleStatus = c.dado
			if got := Build(in, relojFijo()()).LifecycleStatus; got != c.quiero {
				t.Fatalf("lifecycle_status = %q, quiero %q", got, c.quiero)
			}
		})
	}
}

// TestBuild_RevisionNoEsElDelLlamante: el número viaja entero, y su AUSENCIA sale
// como 0 —el único valor que el schema rechaza (`minimum: 1`)— en vez de como un 1
// inventado que el puente aplicaría sin sospechar.
func TestBuild_RevisionNoEsElDelLlamante(t *testing.T) {
	in := entradaDePrueba()
	in.RevisionNo = 7
	if got := Build(in, relojFijo()()).RevisionNo; got != 7 {
		t.Fatalf("revision_no = %d, quiero 7", got)
	}
	in.RevisionNo = 0
	if got := Build(in, relojFijo()()).RevisionNo; got != 0 {
		t.Fatalf("revision_no ausente = %d, quiero 0 (el schema lo rechaza, que es el punto)", got)
	}
}

// TestBuild_SinLíneasEmiteListaVacía: `items` es requerido por el schema y un nil
// serializa como `null`, que el puente rechaza por otro motivo distinto del real.
func TestBuild_SinLíneasEmiteListaVacía(t *testing.T) {
	in := entradaDePrueba()
	in.Items = nil
	body, err := json.Marshal(Build(in, relojFijo()()))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !contiene(body, `"items":[]`) {
		t.Fatalf("items sin líneas debe salir como [], no como null: %s", body)
	}
}

// TestBuild_NoCompartLaSliceDelLlamante: el documento se persiste; que el llamante
// siga tocando su slice después no puede cambiar lo que ya se encoló.
func TestBuild_NoCompartLaSliceDelLlamante(t *testing.T) {
	in := entradaDePrueba()
	p := Build(in, relojFijo()())
	in.Items[0].Label = "PISADO"
	if p.Items[0].Label != "Café" {
		t.Fatalf("el documento comparte la slice del llamante: items[0].label = %q", p.Items[0].Label)
	}
}

// TestBuild_EventHistoryIDOmitido: es el único campo OPCIONAL del contrato
// (MD-042.1) — vacío ⇒ la clave NO aparece, no aparece como "".
func TestBuild_EventHistoryIDOmitido(t *testing.T) {
	body, err := json.Marshal(Build(entradaDePrueba(), relojFijo()()))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if contiene(body, `"event_history_id"`) {
		t.Fatalf("event_history_id no debe aparecer mientras esté vacío: %s", body)
	}
}

// camposQueRellenaElWorker son los tres que este documento NO puede congelar
// (D-042.9/D-042.11): buyer_data y variables{} por coste —descifrado y consulta,
// prohibidos en línea con el mensaje por INV-02— y customer_note por EXPOSICIÓN
// (PII en claro en webhook_outbox, una tabla que sobrevive a la entrega y que no se
// poda, D-046.16/ADR-0043).
var camposQueRellenaElWorker = []string{"buyer_data", "variables", "customer_note"}

// TestBuild_NoCongelaLoQueRellenaElWorker vigila la mitad CONGELADA del reparto por
// NOMBRE DE CABLE. Hoy es estructural —Input no tiene esos campos, así que no hay
// por dónde colarlos— y por eso el test es barato; existe para que añadirlos a Input
// «porque ya los tengo a mano» se ponga rojo aquí y no en producción.
func TestBuild_NoCongelaLoQueRellenaElWorker(t *testing.T) {
	body, err := json.Marshal(Build(entradaDePrueba(), relojFijo()()))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("el documento no es JSON válido: %v", err)
	}
	for _, k := range camposQueRellenaElWorker {
		if _, hay := got[k]; hay {
			t.Fatalf("%q entró en el documento que se PERSISTE en webhook_outbox. Los tres los "+
				"completa el worker justo antes del POST: congelarlos aquí vuelve a violar INV-02 "+
				"(y, con customer_note, deja PII en claro en una fila que nadie poda):\n%s", k, body)
		}
	}
}

// --- Push: la regla completa --------------------------------------------------

// pusherDePrueba arma el encolador con los dos dobles y el reloj fijo.
func pusherDePrueba(abierto bool, errGate, errCola error) (*Pusher, *colaFalsa) {
	q := &colaFalsa{err: errCola}
	g := &gateFalso{abiertos: map[string]bool{tenantDePrueba: abierto}, err: errGate}
	return NewPusher(logDescartado(), q, g, WithClock(relojFijo())), q
}

// TestPush_GateAbiertoEncolaUnaVez: el camino feliz — un INSERT, con el kind y el
// tenant del contrato, y el cuerpo decodificable.
func TestPush_GateAbiertoEncolaUnaVez(t *testing.T) {
	p, q := pusherDePrueba(true, nil, nil)

	res, err := p.Push(context.Background(), entradaDePrueba())
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if !res.Enqueued {
		t.Fatal("Enqueued=false con el gate abierto")
	}
	if res.OutboxID != 1 {
		t.Fatalf("outbox_id = %d, quiero 1", res.OutboxID)
	}
	if len(q.llamadas) != 1 {
		t.Fatalf("se encoló %d veces, quiero 1", len(q.llamadas))
	}
	if q.llamadas[0].kind != "intake.push" {
		t.Fatalf("kind = %q, quiero intake.push", q.llamadas[0].kind)
	}
	if q.llamadas[0].tenantID != tenantDePrueba {
		t.Fatalf("tenant = %q, quiero %q", q.llamadas[0].tenantID, tenantDePrueba)
	}
	var cuerpo map[string]any
	if err := json.Unmarshal(q.llamadas[0].payload, &cuerpo); err != nil {
		t.Fatalf("el cuerpo encolado no es JSON válido: %v", err)
	}
	// Se leen por NOMBRE DE CABLE y no decodificando a Payload: si alguien renombra
	// una etiqueta json, un decode tipado lo taparía y el puente se quedaría sin una
	// clave que el schema declara requerida.
	if cuerpo["lifecycle_status"] != estadoPendingApproval {
		t.Fatalf("lifecycle_status en el cuerpo = %#v, quiero %q", cuerpo["lifecycle_status"], estadoPendingApproval)
	}
	if cuerpo["revision_no"] != float64(4) {
		t.Fatalf("revision_no en el cuerpo = %#v, quiero 4", cuerpo["revision_no"])
	}
}

// TestPush_GateCerradoNoEncolaYNoEsError: un tenant sin puente CRM activo no es una
// avería. Las dos puertas tienen que poder distinguirlo de un fallo, y por eso sale
// por Result.Enqueued y no por error.
func TestPush_GateCerradoNoEncolaYNoEsError(t *testing.T) {
	p, q := pusherDePrueba(false, nil, nil)

	res, err := p.Push(context.Background(), entradaDePrueba())
	if err != nil {
		t.Fatalf("un gate cerrado NO es un error: %v", err)
	}
	if res.Enqueued {
		t.Fatal("Enqueued=true con el gate cerrado")
	}
	if len(q.llamadas) != 0 {
		t.Fatalf("gate cerrado no debe encolar, encoló %d", len(q.llamadas))
	}
}

// TestPush_GateEnErrorNoEncola: fail-closed. El error SÍ sube —el llamante decide
// qué hacer con él— pero no se encola nada: un puente que no se pudo evaluar no
// recibe basura.
func TestPush_GateEnErrorNoEncola(t *testing.T) {
	p, q := pusherDePrueba(true, errors.New("resolver caído"), nil)

	if _, err := p.Push(context.Background(), entradaDePrueba()); err == nil {
		t.Fatal("un gate en error tiene que devolver error: el sink lo loguea y una ruta HTTP puede contestarlo")
	}
	if len(q.llamadas) != 0 {
		t.Fatalf("gate en error no debe encolar (fail-closed), encoló %d", len(q.llamadas))
	}
}

// TestPush_ErrorDeColaSube: el fallo del INSERT no se traga aquí. Quien decide qué
// hacer es la puerta: el EventSink lo loguea y devuelve nil (jamás cuelga el mensaje
// del cliente), y una ruta HTTP puede contestar con un código.
func TestPush_ErrorDeColaSube(t *testing.T) {
	p, _ := pusherDePrueba(true, nil, errors.New("conexión perdida"))

	res, err := p.Push(context.Background(), entradaDePrueba())
	if err == nil {
		t.Fatal("un fallo del store tiene que subir: tragárselo le quita la decisión a la puerta")
	}
	if res.Enqueued {
		t.Fatal("Enqueued=true con el INSERT fallido")
	}
}

// TestPush_SinDependenciasEsNoOp: un Pusher a medias no encola y no panica. Es el
// mismo no-op seguro que tenía el sink construido sin sender/gate.
func TestPush_SinDependenciasEsNoOp(t *testing.T) {
	for _, c := range []struct {
		nombre string
		p      *Pusher
	}{
		{"nil", nil},
		{"sin cola", NewPusher(logDescartado(), nil, &gateFalso{abiertos: map[string]bool{tenantDePrueba: true}})},
		{"sin gate", NewPusher(logDescartado(), &colaFalsa{}, nil)},
		{"sin log", NewPusher(nil, &colaFalsa{}, &gateFalso{abiertos: map[string]bool{tenantDePrueba: true}})},
	} {
		t.Run(c.nombre, func(t *testing.T) {
			res, err := c.p.Push(context.Background(), entradaDePrueba())
			if err != nil {
				t.Fatalf("un Pusher a medias no debe fallar: %v", err)
			}
			if res.Enqueued {
				t.Fatal("un Pusher a medias no puede decir que encoló")
			}
		})
	}
}

// contiene busca una subcadena sin depender de bytes.Contains, para que el
// test siga siendo legible cuando lo que falla es una etiqueta json.
func contiene(b []byte, s string) bool {
	for i := 0; i+len(s) <= len(b); i++ {
		if string(b[i:i+len(s)]) == s {
			return true
		}
	}
	return false
}
