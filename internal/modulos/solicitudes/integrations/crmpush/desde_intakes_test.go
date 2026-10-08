//go:build pendiente

package crmpush

// desde_intakes_test.go — la SEGUNDA puerta arma el MISMO documento que la primera.
//
// 🔴 LO QUE ESTE FICHERO EXISTE PARA IMPEDIR. Si las revisiones del dueño armaran su
// propio cuerpo en vez de pasar por Build, el defecto de R-12 —un campo clave
// clavado— volvería repartido en dos sitios. Por eso el test central no comprueba
// «encoló»: comprueba que lo encolado lleva el número y el estado que le dio el
// llamante. Los dobles son los de push_test.go.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// Aserciones de compilación de lo que desde_intakes.go promete: el constructor, la
// firma sin error y el puerto del dominio satisfecho.
var (
	_ func(*Pusher, logger.Logger) *RevisionPusher                        = NewRevisionPusher
	_ func(*RevisionPusher, context.Context, string, intakes.Detail, int) = (*RevisionPusher).PushRevision
	_ intakes.CRMPusher                                                   = (*RevisionPusher)(nil)
)

// sampleDetail es una solicitud del dominio TAL COMO la devuelve una corrección:
// cabecera + líneas, con el estado que se le pida.
func sampleDetail(status string) intakes.Detail {
	return intakes.Detail{
		Intake: intakes.Intake{
			ID:        sampleIntake,
			ContactID: "contact-opaco-xyz",
			SessionID: "sess-negocio",
			Status:    status,
			Total:     24.8,
		},
		Items: []intakes.Item{
			{SKU: "A1", Label: "Café", Customization: "sin azúcar", Qty: 2, UnitPrice: 9.9},
			{SKU: "B2", Label: "Té", Qty: 1, UnitPrice: 5.0},
		},
	}
}

// revisionFixture es el adaptador sobre un Pusher de dobles, con su PROPIO log.
type revisionFixture struct {
	adapter *RevisionPusher
	pusher  pusherFixture
	log     *recordingLogger
}

func newRevisionFixture(open bool, gateErr, queueErr error) revisionFixture {
	f := revisionFixture{pusher: newPusherFixture(open, gateErr, queueErr), log: newRecordingLogger()}
	f.adapter = NewRevisionPusher(f.pusher.pusher, f.log)
	return f
}

// enqueuedBody devuelve el único cuerpo encolado, por nombre de cable.
func enqueuedBody(t *testing.T, q *fakeQueue) (queueCall, map[string]any) {
	t.Helper()
	calls := q.recorded()
	if len(calls) != 1 {
		t.Fatalf("se encoló %d veces, quiero 1", len(calls))
	}
	var body map[string]any
	if err := json.Unmarshal(calls[0].payload, &body); err != nil {
		t.Fatalf("el cuerpo encolado no es JSON válido: %v", err)
	}
	return calls[0], body
}

// TestRevisionPusher_EnqueuesTheNumberAndStatusItWasGiven (R-12) es el test central:
// el cuerpo que acaba en la cola lleva la revisión 5 y `pending_approval`. Los dos
// valores están elegidos para que un literal reintroducido CHOQUE: con un `1` el
// revision_no saldría 1; con `"confirmed"` el estado saldría confirmed.
func TestRevisionPusher_EnqueuesTheNumberAndStatusItWasGiven(t *testing.T) {
	f := newRevisionFixture(true, nil, nil)

	f.adapter.PushRevision(context.Background(), sampleTenant, sampleDetail(statusPendingApproval), 5)

	call, body := enqueuedBody(t, f.pusher.queue)
	if call.kind != "intake.push" || call.tenantID != sampleTenant {
		t.Fatalf("kind=%q tenant=%q; quiero intake.push y %q", call.kind, call.tenantID, sampleTenant)
	}
	if body["revision_no"] != float64(5) {
		t.Fatalf("revision_no = %#v, quiero 5. El puente hace UPSERT por (intake_id, revision_no): "+
			"con el número equivocado la corrección se pierde como duplicado", body["revision_no"])
	}
	if body["lifecycle_status"] != statusPendingApproval {
		t.Fatalf("lifecycle_status = %#v, quiero %q. Una corrección deja la solicitud POR APROBAR; "+
			"decirle `confirmed` al CRM le hace creer que el pedido está cerrado",
			body["lifecycle_status"], statusPendingApproval)
	}
	if body["intake_id"] != sampleIntake || body["tenant"] != sampleTenant || body["contact"] != "contact-opaco-xyz" {
		t.Fatalf("intake_id/tenant/contact mal traducidos: %v", body)
	}
	if _, present := body["event_history_id"]; present {
		t.Fatalf("esta puerta no rellena event_history_id y apareció: %v", body)
	}

	debug := f.log.at("debug")
	if len(debug) != 1 || debug[0].msg != "crmpush: revisión encolada para el puente CRM" {
		t.Fatalf("Debug del adaptador = %+v; quiero UNA línea con el mensaje literal", debug)
	}
	got := debug[0].fields
	if got["tenant"] != sampleTenant || got["intake_id"] != sampleIntake || got["revision_no"] != 5 ||
		got["outbox_id"] != int64(41) {
		t.Fatalf("claves del Debug = %v; quiero tenant, intake_id, revision_no=5 y outbox_id=41", got)
	}
	if n := len(f.log.at("error")); n != 0 {
		t.Fatalf("un encolado correcto no deja Error y hubo %d", n)
	}
}

// TestRevisionPusher_NormalizesTheLegacyStatus: si la solicitud está guardada con la
// clave legada del carrito, el contrato emite la canónica. El adaptador la pasa
// CRUDA; la regla vive en Build y esta puerta no puede saltársela.
func TestRevisionPusher_NormalizesTheLegacyStatus(t *testing.T) {
	f := newRevisionFixture(true, nil, nil)

	f.adapter.PushRevision(context.Background(), sampleTenant, sampleDetail(statusClosedLegacy), 2)

	_, body := enqueuedBody(t, f.pusher.queue)
	if body["lifecycle_status"] != statusConfirmed {
		t.Fatalf("lifecycle_status = %#v, quiero %q: el contrato JAMÁS emite `closed`",
			body["lifecycle_status"], statusConfirmed)
	}
}

// TestRevisionPusher_LinesCrossWithTheirCustomization: la personalización de LÍNEA es
// dato de producción y viaja (D-041.17); el dinero no se mueve por traducir (INV-13).
func TestRevisionPusher_LinesCrossWithTheirCustomization(t *testing.T) {
	f := newRevisionFixture(true, nil, nil)

	f.adapter.PushRevision(context.Background(), sampleTenant, sampleDetail(statusPendingApproval), 3)

	call, _ := enqueuedBody(t, f.pusher.queue)
	var body Payload
	if err := json.Unmarshal(call.payload, &body); err != nil {
		t.Fatalf("el cuerpo encolado no es JSON válido: %v", err)
	}
	want := []Item{
		{SKU: "A1", Label: "Café", Customization: "sin azúcar", Qty: 2, UnitPrice: 9.9},
		{SKU: "B2", Label: "Té", Customization: "", Qty: 1, UnitPrice: 5.0},
	}
	if len(body.Items) != len(want) {
		t.Fatalf("items = %d, quiero %d", len(body.Items), len(want))
	}
	for i := range want {
		if body.Items[i] != want[i] {
			t.Fatalf("items[%d] = %+v, quiero %+v: la traducción cambió una línea", i, body.Items[i], want[i])
		}
	}
	if body.Total != 24.8 {
		t.Fatalf("total = %v, quiero 24.8; la traducción movió el dinero", body.Total)
	}
}

// TestRevisionPusher_NoLinesEnqueuesAnEmptyList: una solicitud sin líneas sigue
// saliendo con `items: []`, no `null`.
func TestRevisionPusher_NoLinesEnqueuesAnEmptyList(t *testing.T) {
	f := newRevisionFixture(true, nil, nil)
	d := sampleDetail(statusPendingApproval)
	d.Items = nil

	f.adapter.PushRevision(context.Background(), sampleTenant, d, 3)

	_, body := enqueuedBody(t, f.pusher.queue)
	if items, ok := body["items"].([]any); !ok || len(items) != 0 {
		t.Fatalf("items = %#v; sin líneas quiero una lista vacía", body["items"])
	}
}

// TestRevisionPusher_ClosedGateDoesNotEnqueue (R-13): el mismo gate que el cierre del
// carrito y el mismo desenlace — nada encolado, y nada que decir en el log propio.
func TestRevisionPusher_ClosedGateDoesNotEnqueue(t *testing.T) {
	f := newRevisionFixture(false, nil, nil)

	f.adapter.PushRevision(context.Background(), sampleTenant, sampleDetail(statusPendingApproval), 5)

	if n := len(f.pusher.queue.recorded()); n != 0 {
		t.Fatalf("gate cerrado no debe encolar, encoló %d", n)
	}
	if len(f.pusher.gate.calls) != 1 {
		t.Fatalf("el gate se consultó %d veces, quiero 1 (pasa por el mismo Pusher)", len(f.pusher.gate.calls))
	}
	if n := len(f.log.at("debug")) + len(f.log.at("error")); n != 0 {
		t.Fatalf("con el gate cerrado el adaptador no loguea nada propio y dejó %d líneas", n)
	}
}

// TestRevisionPusher_FailureIsLoggedAndDiesHere: la firma no admite error; un fallo
// de la cola o del gate queda en Error con su intake_id y no se reintenta.
func TestRevisionPusher_FailureIsLoggedAndDiesHere(t *testing.T) {
	const want = "crmpush: la revisión del dueño no llegó a la cola del puente; " +
		"la revisión SÍ está escrita y no se reintenta el encolado"
	down := errors.New("avería")
	for name, f := range map[string]revisionFixture{
		"queue fails": newRevisionFixture(true, nil, down),
		"gate fails":  newRevisionFixture(true, down, nil),
	} {
		t.Run(name, func(t *testing.T) {
			f.adapter.PushRevision(context.Background(), sampleTenant, sampleDetail(statusPendingApproval), 5)

			logged := f.log.at("error")
			if len(logged) != 1 || logged[0].msg != want {
				t.Fatalf("Error del adaptador = %+v; quiero UNA línea con el mensaje literal", logged)
			}
			got := logged[0].fields
			if got["tenant"] != sampleTenant || got["intake_id"] != sampleIntake || got["revision_no"] != 5 {
				t.Fatalf("claves del Error = %v; quiero tenant, intake_id y revision_no=5", got)
			}
			if err, ok := got["error"].(error); !ok || !errors.Is(err, down) {
				t.Fatalf("clave error = %#v; quiero el error de Push", got["error"])
			}
			if n := len(f.pusher.queue.recorded()); n != 0 {
				t.Fatalf("un empuje fallido no deja nada encolado ni se reintenta, hay %d", n)
			}
			if len(f.pusher.gate.calls) != 1 {
				t.Fatalf("el gate se consultó %d veces: el encolado NO se reintenta", len(f.pusher.gate.calls))
			}
		})
	}
}

// panickingQueue imita el peor fallo posible del almacén: no un error, un pánico.
type panickingQueue struct{}

func (panickingQueue) EnqueueWebhook(context.Context, string, string, json.RawMessage) (int64, error) {
	panic("conexión en un estado imposible")
}

// panickingGate es lo mismo en la otra dependencia.
type panickingGate struct{}

func (panickingGate) Enabled(context.Context, string) (bool, error) {
	panic("resolver en un estado imposible")
}

// TestRevisionPusher_DoesNotTakeDownTheCaller: ni un adaptador a medias ni una
// dependencia que panica pueden llevarse por delante la respuesta de una corrección
// que YA está escrita — si lo hicieran, el dueño reintentaría y crearía una revisión
// de más. Si PushRevision deja escapar el pánico, este test muere con él.
func TestRevisionPusher_DoesNotTakeDownTheCaller(t *testing.T) {
	openGate := func() *fakeGate { return &fakeGate{open: map[string]bool{sampleTenant: true}} }

	t.Run("half-built adapter", func(t *testing.T) {
		q := &fakeQueue{}
		for name, a := range map[string]*RevisionPusher{
			"nil receiver": nil,
			"no pusher":    NewRevisionPusher(nil, newRecordingLogger()),
			"no log":       NewRevisionPusher(NewPusher(newRecordingLogger(), q, openGate()), nil),
		} {
			t.Run(name, func(t *testing.T) {
				a.PushRevision(context.Background(), sampleTenant, sampleDetail(statusPendingApproval), 5)
				if n := len(q.recorded()); n != 0 {
					t.Fatalf("un adaptador a medias no empuja y encoló %d", n)
				}
			})
		}
	})

	for _, c := range []struct {
		name  string
		queue Queuer
		gate  Gate
		want  string
	}{
		{"the store panics", panickingQueue{}, openGate(), "conexión en un estado imposible"},
		{"the gate panics", &fakeQueue{}, panickingGate{}, "resolver en un estado imposible"},
	} {
		t.Run(c.name, func(t *testing.T) {
			log := newRecordingLogger()
			a := NewRevisionPusher(NewPusher(newRecordingLogger(), c.queue, c.gate, WithClock(fixedClock)), log)

			a.PushRevision(context.Background(), sampleTenant, sampleDetail(statusPendingApproval), 5)

			logged := log.at("error")
			if len(logged) != 1 ||
				logged[0].msg != "crmpush: pánico empujando la revisión al puente; la revisión YA está escrita" {
				t.Fatalf("Error del adaptador = %+v; quiero UNA línea con el mensaje literal del pánico", logged)
			}
			got := logged[0].fields
			if got["intake_id"] != sampleIntake || got["revision_no"] != 5 || got["panic"] != c.want {
				t.Fatalf("claves del Error = %v; quiero intake_id, revision_no=5 y panic=%q", got, c.want)
			}
		})
	}
}
