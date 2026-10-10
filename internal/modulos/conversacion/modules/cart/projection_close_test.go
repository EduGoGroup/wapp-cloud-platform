package cart_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// projection_close_test.go — cart_closed: el cierre de la solicitud, su revisión, la
// línea de envío y lo que se anota en el efecto para el WebhookSink. Son escrituras
// ENCADENADAS y sin transacción común: cada test de fallo fija qué queda escrito.

// closedEffect es el efecto de cierre tal como lo emite la sub-máquina (camino en
// proceso), con el total de sus líneas.
func closedEffect(lines ...map[string]any) modules.Effect {
	total := 0.0
	for _, l := range lines {
		total += float64(l["qty"].(int)) * l["unit_price"].(float64) //nolint:errcheck // aserción de tipo sobre un valor que el propio test construyó: si no casa, el test falla (o entra en pánico) igual
	}
	return modules.Effect{Kind: kindPersist, Name: cart.EffectCartClosed,
		Payload: map[string]any{"items": lines, "total": total}}
}

// Al cerrar SIN solicitud abierta nace una "closed" coherente, con sus líneas, su
// total, el evento de la meta y la nota vacía.
func TestProjector_CartClosed_CreatesACoherentClosedIntake(t *testing.T) {
	r := newRig()
	r.project(t, meta(), closedEffect(line("EMPA", 2, 2500)))
	got := r.onlyIntake(t)
	if got.Status != "closed" || got.Total != 5000 || got.TenantID != tenantID || got.ContactID != contactID ||
		got.SessionID != sessionID || got.EventID != eventID || got.CustomerNote != "" {
		t.Errorf("solicitud = %+v, quiero una closed de 5000 con la identidad y el evento de la meta", got)
	}
	want := []cartLine{{SKU: "EMPA", Label: "Etiqueta de EMPA", Qty: 2, UnitPrice: 2500}}
	if lines := r.lines(t, got.ID); !reflect.DeepEqual(lines, want) {
		t.Errorf("líneas = %+v, quiero %+v", lines, want)
	}
	if !reflect.DeepEqual(r.repo.calls, []string{"CloseIntake"}) {
		t.Errorf("llamadas al almacén = %v, quiero solo el cierre atómico", r.repo.calls)
	}
}

// Con solicitud abierta se cierra ESA —no nace otra—, sus líneas pasan a ser las del
// cierre (fuente de verdad, sin duplicar las ya proyectadas) y la revisión cuelga de ella.
func TestProjector_CartClosed_ClosesTheOpenIntake(t *testing.T) {
	r := newRig()
	r.project(t, meta(), snapshotEffect(cart.EffectItemAdded, line("EMPA", 1, 2500)))
	open := r.onlyIntake(t)

	r.project(t, meta(), closedEffect(line("EMPA", 1, 2500), line("JUGO", 2, 1000)))
	closed := r.onlyIntake(t)
	if closed.ID != open.ID || closed.Status != "closed" || closed.Total != 4500 {
		t.Errorf("solicitud = %+v, quiero la abierta (%s) cerrada con total 4500", closed, open.ID)
	}
	if lines := r.lines(t, closed.ID); len(lines) != 2 {
		t.Errorf("líneas = %+v, quiero las DOS del cierre, sin duplicar la ya proyectada", lines)
	}
	if revs := r.revisions.Revisions(open.ID); len(revs) != 1 {
		t.Errorf("revisiones de la solicitud abierta = %d, quiero 1", len(revs))
	}
}

// La indicación del pedido llega a la CABECERA en el mismo cierre y no mueve el dinero;
// sin la clave, o con una de otro tipo, es la cadena vacía. La de línea llega a su línea.
func TestProjector_CartClosed_Notes(t *testing.T) {
	cases := []struct {
		name string
		note any
		set  bool
		want string
	}{
		{name: "with note", note: "dejarlo en portería", set: true, want: "dejarlo en portería"},
		{name: "without the key", want: ""},
		{name: "note of another type", note: 7, set: true, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig()
			eff := closedEffect(line("EMPA", 2, 2500))
			eff.Payload["items"].([]map[string]any)[0]["customization"] = "sin ají" //nolint:errcheck // aserción de tipo sobre un valor que el propio test construyó: si no casa, el test falla (o entra en pánico) igual
			if tc.set {
				eff.Payload["customer_note"] = tc.note
			}
			r.project(t, meta(), eff)
			got := r.onlyIntake(t)
			if got.CustomerNote != tc.want || got.Total != 5000 {
				t.Errorf("solicitud = %+v, quiero la nota %q y el total 5000", got, tc.want)
			}
			if lines := r.lines(t, got.ID); len(lines) != 1 || lines[0].Customization != "sin ají" {
				t.Errorf("líneas = %+v, quiero la indicación de la línea", lines)
			}
		})
	}
}

// El cierre también lee el payload tras el round-trip JSON (un replay del outbox).
func TestProjector_CartClosed_ReadsAJSONRoundTrippedPayload(t *testing.T) {
	r := newRig()
	var payload map[string]any
	raw := `{"items":[{"sku":"EMPA","label":"Empanada","qty":2,"unit_price":2500,"customization":"sin ají"}],"total":5000,"customer_note":"timbre 4B"}`
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("payload de prueba: %v", err)
	}
	r.project(t, meta(), modules.Effect{Name: cart.EffectCartClosed, Payload: payload})
	got := r.onlyIntake(t)
	if got.Total != 5000 || got.CustomerNote != "timbre 4B" {
		t.Errorf("solicitud = %+v", got)
	}
	want := []cartLine{{SKU: "EMPA", Label: "Empanada", Qty: 2, UnitPrice: 2500, Customization: "sin ají"}}
	if lines := r.lines(t, got.ID); !reflect.DeepEqual(lines, want) {
		t.Errorf("líneas = %+v, quiero %+v", lines, want)
	}
}

// La revisión del cierre: de tipo cart, del sistema, sin texto renderizado, y con la
// foto de lo que se cerró (sin la indicación de la línea).
func TestProjector_CartClosed_WritesTheCartRevision(t *testing.T) {
	r := newRig()
	eff := closedEffect(line("EMPA", 2, 2500))
	eff.Payload["items"].([]map[string]any)[0]["customization"] = "sin ají" //nolint:errcheck // aserción de tipo sobre un valor que el propio test construyó: si no casa, el test falla (o entra en pánico) igual
	r.project(t, meta(), eff)

	revs := r.revisions.Revisions(r.onlyIntake(t).ID)
	if len(revs) != 1 {
		t.Fatalf("revisiones = %d, quiero 1", len(revs))
	}
	rev := revs[0]
	if rev.RevisionNo != 1 || rev.Kind != intakes.RevisionKindCart || rev.CreatedBy != intakes.RevisionBySystem || rev.RenderedText != "" {
		t.Errorf("revisión = %+v, quiero la 1, de tipo cart, del sistema y sin texto", rev)
	}
	want, err := intakes.CartRevisionPayload(5000, []intakes.RevisionLine{{SKU: "EMPA", Label: "Etiqueta de EMPA", Qty: 2, UnitPrice: 2500}})
	if err != nil {
		t.Fatalf("payload esperado: %v", err)
	}
	if string(rev.Payload) != string(want) {
		t.Errorf("payload de la revisión = %s\nquiero %s", rev.Payload, want)
	}
}

// Lo que el cierre ANOTA en el efecto —el MISMO mapa que leerá el WebhookSink—: el id
// de la solicitud, el número REAL de la revisión y el estado legado, sin normalizar.
func TestProjector_CartClosed_AnnotatesTheEffect(t *testing.T) {
	r := newRig()
	eff := closedEffect(line("EMPA", 2, 2500))
	payload := eff.Payload
	r.project(t, meta(), eff)
	id := r.onlyIntake(t).ID
	if payload["intake_id"] != id {
		t.Errorf("intake_id = %#v, quiero %q", payload["intake_id"], id)
	}
	if got, ok := payload["revision_no"].(int); !ok || got != 1 {
		t.Errorf("revision_no = %#v, quiero el int 1", payload["revision_no"])
	}
	if payload["lifecycle_status"] != "closed" {
		t.Errorf("lifecycle_status = %#v, quiero la clave legada \"closed\"", payload["lifecycle_status"])
	}
	if payload["total"] != 5000.0 || len(payload["items"].([]map[string]any)) != 1 { //nolint:errcheck // aserción de tipo sobre un valor que el propio test construyó: si no casa, el test falla (o entra en pánico) igual
		t.Errorf("el payload original cambió: %v", payload)
	}
}

// T4.10: el número NO es siempre 1. Si el pipeline ya colgó su revisión de la misma
// solicitud, el cierre es la 2 y eso es lo que se anota.
func TestProjector_CartClosed_AnnotatesTheSecondRevisionWhenThePipelineWroteFirst(t *testing.T) {
	r := newRig()
	r.project(t, meta(), snapshotEffect(cart.EffectItemAdded, line("EMPA", 2, 2500)))
	open := r.onlyIntake(t).ID
	if _, err := r.revisions.InsertRevision(context.Background(), intakes.Revision{
		IntakeID: open, Kind: intakes.RevisionKindInterpreted,
		Payload: json.RawMessage(`{"version":1,"items":[]}`), CreatedBy: intakes.RevisionBySystem,
	}); err != nil {
		t.Fatalf("sembrar la revisión del pipeline: %v", err)
	}

	eff := closedEffect(line("EMPA", 2, 2500))
	r.project(t, meta(), eff)
	revs := r.revisions.Revisions(open)
	if len(revs) != 2 || revs[1].Kind != intakes.RevisionKindCart || revs[1].RevisionNo != 2 {
		t.Fatalf("revisiones = %+v, quiero la del pipeline y la del cierre con el número 2", revs)
	}
	if eff.Payload["revision_no"] != 2 {
		t.Errorf("revision_no anotado = %#v, quiero 2", eff.Payload["revision_no"])
	}
}

// La línea de envío se pide DESPUÉS de la revisión, sobre la solicitud que cerró y con
// la política del carrito: solo si el tenant tiene zonas.
func TestProjector_CartClosed_AsksForTheShippingLine(t *testing.T) {
	r := newRig()
	r.project(t, meta(), closedEffect(line("EMPA", 2, 2500)))
	id := r.onlyIntake(t).ID
	if r.shipping.calls != 1 || r.shipping.tenantID != tenantID || r.shipping.intakeID != id || r.shipping.policy != intakes.ShippingOnlyIfZones {
		t.Errorf("envío = %+v, quiero UNA petición sobre (%s, %s) con ShippingOnlyIfZones", r.shipping, tenantID, id)
	}
}

// Los fallos, uno a uno: qué queda escrito y qué NO se anota.
func TestProjector_CartClosed_Failures(t *testing.T) {
	boom := errors.New("pg caído")

	t.Run("close fails: nothing else is written", func(t *testing.T) {
		r := newRig()
		r.repo.fail["CloseIntake"] = boom
		eff := closedEffect(line("EMPA", 2, 2500))
		if err := r.p.Project(context.Background(), meta(), eff); !errors.Is(err, boom) {
			t.Fatalf("Project = %v, quiero el error del cierre", err)
		}
		if len(r.repo.Intakes()) != 0 || r.shipping.calls != 0 {
			t.Errorf("tras el fallo del cierre hay %d solicitudes y %d peticiones de envío", len(r.repo.Intakes()), r.shipping.calls)
		}
		assertNotAnnotated(t, eff)
	})

	t.Run("revision fails: the close survives", func(t *testing.T) {
		repo := newSpyStore()
		broken := &brokenRevisions{}
		shipping := &shippingSpy{}
		p := cart.NewProjector(repo, broken, shipping, &buyerSpy{})
		eff := closedEffect(line("EMPA", 2, 2500))
		err := p.Project(context.Background(), meta(), eff)
		if !errors.Is(err, errBrokenRevision) {
			t.Fatalf("Project = %v, quiero el error de la revisión", err)
		}
		closed := repo.Intakes()
		if len(closed) != 1 || closed[0].Status != "closed" || len(repo.IntakeItems(closed[0].ID)) != 1 {
			t.Fatalf("el cierre debía sobrevivir con sus líneas: %+v", closed)
		}
		if want := "cart: revisión del cierre de la solicitud " + closed[0].ID + ": revisión caída"; err.Error() != want {
			t.Errorf("error = %q\nquiero  %q", err, want)
		}
		if shipping.calls != 0 {
			t.Error("se pidió el envío sin revisión: el envío va DESPUÉS")
		}
		assertNotAnnotated(t, eff)
	})

	t.Run("shipping fails: close and revision survive", func(t *testing.T) {
		r := newRig()
		r.shipping.err = boom
		eff := closedEffect(line("EMPA", 2, 2500))
		err := r.p.Project(context.Background(), meta(), eff)
		if !errors.Is(err, boom) {
			t.Fatalf("Project = %v, quiero el error del envío", err)
		}
		closed := r.onlyIntake(t)
		if closed.Status != "closed" || len(r.revisions.Revisions(closed.ID)) != 1 {
			t.Errorf("el cierre y su revisión debían sobrevivir: %+v", closed)
		}
		if want := "cart: línea de envío de la solicitud " + closed.ID + ": pg caído"; err.Error() != want {
			t.Errorf("error = %q\nquiero  %q", err, want)
		}
		assertNotAnnotated(t, eff)
	})
}

// assertNotAnnotated exige que el efecto NO lleve ninguna de las tres anotaciones.
func assertNotAnnotated(t *testing.T, eff modules.Effect) {
	t.Helper()
	for _, key := range []string{"intake_id", "revision_no", "lifecycle_status"} {
		if got, present := eff.Payload[key]; present {
			t.Errorf("se anotó %s = %#v pese al fallo", key, got)
		}
	}
}

// H24: el SEGUNDO pedido tras confirmar tiene su PROPIO evento y su PROPIA solicitud
// abierta; la del primero queda cerrada, con sus líneas y su revisión, y no se toca.
func TestProjector_SecondOrderAfterConfirmingHasItsOwnIntake(t *testing.T) {
	r := newRig()
	first := meta()
	r.project(t, first, snapshotEffect(cart.EffectItemAdded, line("EMPA", 2, 2500)))
	r.project(t, first, closedEffect(line("EMPA", 2, 2500)))
	closed := r.onlyIntake(t)

	second := meta()
	second.EventID = "9b7d4e21-8a55-4c0f-9d2b-3a443f2a1c4e"
	r.project(t, second, snapshotEffect(cart.EffectItemAdded, line("JUGO", 1, 1000)))

	all := map[string]store.Intake{}
	for _, in := range r.repo.Intakes() {
		all[in.EventID] = in
	}
	if len(all) != 2 {
		t.Fatalf("solicitudes por evento = %+v, quiero dos: una por pedido", all)
	}
	if got := all[first.EventID]; got.ID != closed.ID || got.Status != "closed" || got.Total != 5000 {
		t.Errorf("primer pedido = %+v, quiero el cerrado de 5000, intacto", got)
	}
	if got := all[second.EventID]; got.ID == closed.ID || got.Status != "open" {
		t.Errorf("segundo pedido = %+v, quiero una solicitud NUEVA y abierta", got)
	}
	if lines := r.lines(t, closed.ID); len(lines) != 1 || lines[0].SKU != "EMPA" {
		t.Errorf("líneas del primer pedido = %+v, quiero las suyas", lines)
	}
	if lines := r.lines(t, all[second.EventID].ID); len(lines) != 1 || lines[0].SKU != "JUGO" {
		t.Errorf("líneas del segundo pedido = %+v, quiero las suyas", lines)
	}
}
