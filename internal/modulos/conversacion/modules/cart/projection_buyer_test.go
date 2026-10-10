//go:build pendiente

package cart_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// projection_buyer_test.go — buyer_data_captured (un campo a la fila cifrada) y las
// transiciones de la solicitud abierta (cart_cancelled, cart_expired).

const secretValue = "12.345.678-5"

func buyerEffect(key, value any) modules.Effect {
	return modules.Effect{Kind: modules.KindPrivate, Name: cart.EffectBuyerDataCaptured,
		Payload: map[string]any{"key": key, "value": value}}
}

// El campo se cuelga de la solicitud ABIERTA del contacto —la que abrió el primer
// item_added—, no de una nueva.
func TestProjector_BuyerData_HangsFromTheOpenIntake(t *testing.T) {
	r := newRig()
	r.project(t, meta(), snapshotEffect(cart.EffectItemAdded, line("PAN", 1, 2)))
	open := r.onlyIntake(t)
	r.repo.calls = nil

	r.project(t, meta(), buyerEffect("rut", secretValue))
	r.project(t, meta(), buyerEffect("direccion", "Av. Siempre Viva 742"))
	want := [][3]string{{open.ID, "rut", secretValue}, {open.ID, "direccion", "Av. Siempre Viva 742"}}
	if !reflect.DeepEqual(r.buyer.puts, want) {
		t.Errorf("escrituras = %v\nquiero     %v", r.buyer.puts, want)
	}
	if !reflect.DeepEqual(r.repo.calls, []string{"GetOpenIntake", "GetOpenIntake"}) {
		t.Errorf("llamadas al almacén = %v: guardar un campo solo LEE la solicitud abierta", r.repo.calls)
	}
	if got := r.onlyIntake(t); !reflect.DeepEqual(got, open) {
		t.Errorf("la cabecera cambió: %+v frente a %+v", got, open)
	}
}

// Con el escritor de verdad del dominio de solicitudes (en memoria): los campos se
// FUSIONAN en el checklist de la solicitud.
func TestProjector_BuyerData_WithTheDomainWriter(t *testing.T) {
	repo := newSpyStore()
	domain := intakes.NewMemoryStore()
	p := cart.NewProjector(repo, domain, &shippingSpy{}, domain)
	ctx := context.Background()
	for _, eff := range []modules.Effect{
		snapshotEffect(cart.EffectItemAdded, line("PAN", 1, 2)),
		buyerEffect("rut", secretValue), buyerEffect("direccion", "Calle 1"), buyerEffect("rut", "otro"),
	} {
		if err := p.Project(ctx, meta(), eff); err != nil {
			t.Fatalf("Project(%s): %v", eff.Name, err)
		}
	}
	want := intakes.BuyerData{"rut": "otro", "direccion": "Calle 1"}
	if got := domain.BuyerDataOf(repo.Intakes()[0].ID); !reflect.DeepEqual(got, want) {
		t.Errorf("checklist guardado = %v, quiero %v", got, want)
	}
}

// SIN solicitud abierta es un ERROR, no un silencio: un checklist que el cliente
// rellenó y no se guardó tiene que ser visible. Y el error NO lleva el valor.
func TestProjector_BuyerData_WithoutOpenIntakeIsAnError(t *testing.T) {
	cases := map[string]func(r *rig){
		"no intake at all": func(*rig) {},
		"intake already closed": func(r *rig) {
			r.project(t, meta(), closedEffect(line("PAN", 1, 2.0)))
		},
		"open intake of another contact": func(r *rig) {
			other := meta()
			other.ContactID = "otro-contacto"
			r.project(t, other, snapshotEffect(cart.EffectItemAdded, line("PAN", 1, 2)))
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRig()
			setup(r)
			err := r.p.Project(context.Background(), meta(), buyerEffect("rut", secretValue))
			if err == nil {
				t.Fatal("Project = nil, quiero un error: el dato no tiene dónde guardarse")
			}
			if want := `cart: campo "rut" del comprador sin solicitud abierta que lo reciba`; err.Error() != want {
				t.Errorf("error = %q\nquiero  %q", err, want)
			}
			if len(r.buyer.puts) != 0 {
				t.Errorf("escrituras = %v, quiero ninguna", r.buyer.puts)
			}
		})
	}
}

// Un efecto sin clave o sin valor —o con ellos de otro tipo— no es un fallo: nada que
// guardar, nil, y ni una lectura del almacén.
func TestProjector_BuyerData_IncompletePayloadIsANoOp(t *testing.T) {
	r := newRig()
	for _, eff := range []modules.Effect{
		buyerEffect("", secretValue), buyerEffect("rut", ""), buyerEffect(7, secretValue), buyerEffect("rut", 12345),
		{Name: cart.EffectBuyerDataCaptured}, {Name: cart.EffectBuyerDataCaptured, Payload: map[string]any{"key": "rut"}},
	} {
		r.project(t, meta(), eff)
	}
	if len(r.repo.calls) != 0 || len(r.buyer.puts) != 0 {
		t.Errorf("un efecto incompleto tocó algo: almacén %v, comprador %v", r.repo.calls, r.buyer.puts)
	}
}

// 🔴 Si el escritor falla, el error sube envuelto con la CLAVE del campo y el id de la
// solicitud, y JAMÁS con el valor: ni en el error ni en el log.
func TestProjector_BuyerData_WriterErrorNeverCarriesTheValue(t *testing.T) {
	log := captureLog(t)
	r := newRig()
	r.project(t, meta(), snapshotEffect(cart.EffectItemAdded, line("PAN", 1, 2)))
	id := r.onlyIntake(t).ID
	boom := errors.New("kms caído")
	r.buyer.err = boom

	err := r.p.Project(context.Background(), meta(), buyerEffect("rut", secretValue))
	if !errors.Is(err, boom) {
		t.Fatalf("Project = %v, quiero el error del escritor", err)
	}
	if want := `cart: guardar el campo "rut" del comprador de la solicitud ` + id + `: kms caído`; err.Error() != want {
		t.Errorf("error = %q\nquiero  %q", err, want)
	}
	if strings.Contains(err.Error(), secretValue) || strings.Contains(log.String(), secretValue) {
		t.Errorf("el valor del cliente salió por el error o por el log:\n%v\n%s", err, log)
	}

	r.repo.fail["GetOpenIntake"] = errors.New("pg caído")
	if err := r.p.Project(context.Background(), meta(), buyerEffect("rut", secretValue)); err == nil || strings.Contains(err.Error(), secretValue) {
		t.Errorf("Project = %v, quiero el error del almacén sin el valor", err)
	}
}

// El módulo emite el dato del comprador ANTES que cart_closed, y por eso el proyector
// encuentra abierta la solicitud: conduciendo el carrito de verdad, el último campo y
// el cierre del MISMO turno llegan los dos a su sitio.
func TestProjector_BuyerData_LastFieldAndCloseOfTheSameTurn(t *testing.T) {
	r := newRig()
	m := cart.New()
	vars := seededVars()
	vars[cart.VarBuyerFields] = []store.BuyerField{{Key: "rut", Label: "RUT", Required: true}}
	for _, in := range []string{"1", "1", "2", "1", "2", "1", secretValue} {
		res := turn(m, model.Conversation{Vars: vars}, in, nil)
		for _, eff := range res.Effects {
			if r.p.Handles(eff.Name) {
				r.project(t, meta(), eff)
			}
		}
		vars = res.Vars
	}
	closed := r.onlyIntake(t)
	if closed.Status != "closed" {
		t.Fatalf("solicitud = %+v, quiero el pedido cerrado", closed)
	}
	if want := [][3]string{{closed.ID, "rut", secretValue}}; !reflect.DeepEqual(r.buyer.puts, want) {
		t.Errorf("escrituras = %v, quiero el RUT colgado de la solicitud que se cerró", r.buyer.puts)
	}
}

// cart_cancelled y cart_expired llevan la solicitud abierta a su estado conservando el
// total; sin solicitud abierta son un no-op sin error (H29: el efecto sigue al estado).
func TestProjector_Transitions(t *testing.T) {
	cases := []struct {
		effect string
		status string
	}{
		{cart.EffectCartCancelled, "cancelled"},
		{cart.EffectCartExpired, "expired"}, // sin productor vivo: solo un replay histórico
	}
	for _, tc := range cases {
		t.Run(tc.effect, func(t *testing.T) {
			r := newRig()
			seed := store.Intake{ID: "abierta", TenantID: tenantID, ContactID: contactID, SessionID: sessionID,
				Status: "open", Total: 7.5, EventID: eventID}
			if err := r.repo.MemoryRepository.UpsertIntake(context.Background(), seed); err != nil {
				t.Fatalf("sembrar: %v", err)
			}
			if err := r.repo.MemoryRepository.ReplaceIntakeItems(context.Background(), "abierta", []store.IntakeItem{{SKU: "PAN", Label: "Pan", Qty: 3, UnitPrice: 2.5}}); err != nil {
				t.Fatalf("sembrar líneas: %v", err)
			}
			r.project(t, meta(), modules.Effect{Kind: kindEvent, Name: tc.effect, Payload: map[string]any{}})
			got := r.onlyIntake(t)
			if got.ID != "abierta" || got.Status != tc.status || got.Total != 7.5 || got.EventID != eventID {
				t.Errorf("solicitud = %+v, quiero la misma en %q con su total y su evento", got, tc.status)
			}
			if lines := r.lines(t, "abierta"); len(lines) != 1 {
				t.Errorf("líneas = %+v: la transición no toca las líneas", lines)
			}
			if !reflect.DeepEqual(r.repo.calls, []string{"GetOpenIntake", "MarkIntakeStatus"}) {
				t.Errorf("llamadas = %v", r.repo.calls)
			}
			if r.shipping.calls != 0 || len(r.revisions.Revisions("abierta")) != 0 {
				t.Error("una transición no escribe revisión ni pide envío")
			}

			// Segunda vez: ya no hay abierta ⇒ no-op.
			r.repo.calls = nil
			r.project(t, meta(), modules.Effect{Name: tc.effect})
			if !reflect.DeepEqual(r.repo.calls, []string{"GetOpenIntake"}) || r.onlyIntake(t).Status != tc.status {
				t.Errorf("la segunda vez debía ser un no-op: llamadas %v", r.repo.calls)
			}
		})
	}

	t.Run("store errors propagate", func(t *testing.T) {
		boom := errors.New("pg caído")
		for _, method := range []string{"GetOpenIntake", "MarkIntakeStatus"} {
			r := newRig()
			r.project(t, meta(), snapshotEffect(cart.EffectItemAdded, line("PAN", 1, 2)))
			r.repo.fail[method] = boom
			if err := r.p.Project(context.Background(), meta(), modules.Effect{Name: cart.EffectCartCancelled}); !errors.Is(err, boom) {
				t.Errorf("%s: Project = %v, quiero el error del almacén", method, err)
			}
		}
	})
}

// H29 de punta a punta: cancelar DENTRO del flujo declara cart_cancelled, y ese efecto
// deja la solicitud `cancelled` con sus líneas.
func TestProjector_CancellingInsideTheFlowCancelsTheIntake(t *testing.T) {
	r := newRig()
	m := cart.New()
	vars := seededVars()
	for _, in := range []string{"1", "1", "2", "2", "9"} {
		res := turn(m, model.Conversation{Vars: vars}, in, nil)
		for _, eff := range res.Effects {
			if r.p.Handles(eff.Name) {
				r.project(t, meta(), eff)
			}
		}
		vars = res.Vars
	}
	got := r.onlyIntake(t)
	if got.Status != "cancelled" {
		t.Errorf("solicitud = %+v, quiero cancelled", got)
	}
	if lines := r.lines(t, got.ID); !reflect.DeepEqual(lines, []cartLine{{SKU: "CAFE", Label: "Café", Qty: 2, UnitPrice: 2.5}}) {
		t.Errorf("líneas = %+v, quiero las del carrito cancelado", lines)
	}
}
