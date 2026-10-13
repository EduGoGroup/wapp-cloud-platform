package cart_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
)

// projection_lines_test.go — item_added y note_added: la solicitud "open" que se
// asegura (las dos preguntas y el alta) y sus líneas, que quedan IGUALES a la foto.

// El primer item_added abre la solicitud declarando a su padre, y la deja con las
// líneas de la foto.
func TestProjector_ItemAdded_OpensTheIntakeDeclaringItsEvent(t *testing.T) {
	r := newRig()
	r.project(t, meta(), snapshotEffect(cart.EffectItemAdded, line("PAN", 2, 2.5)))
	got := r.onlyIntake(t)
	if got.ID == "" || got.TenantID != tenantID || got.ContactID != contactID || got.SessionID != sessionID ||
		got.Status != "open" || got.EventID != eventID || got.Total != 0 || !got.ExpiresAt.IsZero() || got.CustomerNote != "" {
		t.Errorf("solicitud = %+v, quiero una open del tenant, contacto, sesión y evento de la meta, sin vencimiento", got)
	}
	want := []cartLine{{SKU: "PAN", Label: "Etiqueta de PAN", Qty: 2, UnitPrice: 2.5}}
	if lines := r.lines(t, got.ID); !reflect.DeepEqual(lines, want) {
		t.Errorf("líneas = %+v, quiero %+v", lines, want)
	}
	if !reflect.DeepEqual(r.repo.calls, []string{"GetOpenIntake", "GetIntakeByEvent", "UpsertIntake", "ReplaceIntakeItems"}) {
		t.Errorf("llamadas = %v: primero la identidad de negocio, después el evento, y solo entonces el alta", r.repo.calls)
	}
}

// La identidad de negocio SIGUE MANDANDO: con una solicitud abierta, el siguiente
// efecto la reusa y la "toca", sin preguntar por evento ni crear otra.
func TestProjector_ItemAdded_ReusesTheOpenIntake(t *testing.T) {
	r := newRig()
	r.project(t, meta(), snapshotEffect(cart.EffectItemAdded, line("PAN", 1, 2)))
	first := r.onlyIntake(t)
	r.repo.calls = nil
	r.project(t, meta(), snapshotEffect(cart.EffectItemAdded, line("PAN", 1, 2), line("QUESO", 1, 3)))
	if second := r.onlyIntake(t); second.ID != first.ID || second.Status != "open" {
		t.Errorf("solicitud = %+v, quiero la misma (%s) y abierta", second, first.ID)
	}
	if !reflect.DeepEqual(r.repo.calls, []string{"GetOpenIntake", "UpsertIntake", "ReplaceIntakeItems"}) {
		t.Errorf("llamadas = %v, quiero reusar la abierta sin preguntar por evento", r.repo.calls)
	}
}

// Las líneas son el ESPEJO de la foto: reemplazo, no acumulación. El mismo efecto
// reentregado deja la tabla igual, y una foto vacía la vacía.
func TestProjector_OpenLines_MirrorTheSnapshot(t *testing.T) {
	r := newRig()
	two := snapshotEffect(cart.EffectItemAdded, line("PAN", 2, 2.5), line("PAN", 1, 2.5))
	itemsOf(t, two.Payload)[1]["customization"] = "bien cocido"
	want := []cartLine{
		{SKU: "PAN", Label: "Etiqueta de PAN", Qty: 2, UnitPrice: 2.5},
		{SKU: "PAN", Label: "Etiqueta de PAN", Qty: 1, UnitPrice: 2.5, Customization: "bien cocido"},
	}
	for range 3 { // el mismo efecto, reentregado
		r.project(t, meta(), two)
		if lines := r.lines(t, r.onlyIntake(t).ID); !reflect.DeepEqual(lines, want) {
			t.Fatalf("líneas = %+v\nquiero   %+v", lines, want)
		}
	}
	// note_added es el OTRO efecto que mueve las líneas: mismo trato.
	r.project(t, meta(), snapshotEffect(cart.EffectNoteAdded, line("QUESO", 4, 3)))
	if lines := r.lines(t, r.onlyIntake(t).ID); !reflect.DeepEqual(lines, []cartLine{{SKU: "QUESO", Label: "Etiqueta de QUESO", Qty: 4, UnitPrice: 3}}) {
		t.Errorf("tras note_added: líneas = %+v, quiero solo las de su foto", lines)
	}
	r.project(t, meta(), snapshotEffect(cart.EffectItemAdded))
	if lines := r.lines(t, r.onlyIntake(t).ID); len(lines) != 0 {
		t.Errorf("foto vacía: líneas = %+v, quiero ninguna", lines)
	}
}

// SIN foto no se toca ninguna línea: es un flow_event histórico reejecutado, que no
// sabe qué había en el carrito. La solicitud se asegura igual.
func TestProjector_OpenLines_WithoutSnapshotLinesAreUntouched(t *testing.T) {
	r := newRig()
	r.project(t, meta(), snapshotEffect(cart.EffectItemAdded, line("PAN", 2, 2.5)))
	id := r.onlyIntake(t).ID
	for _, eff := range []modules.Effect{
		{Name: cart.EffectItemAdded},
		{Name: cart.EffectItemAdded, Payload: map[string]any{"sku": "PAN", "label": "Pan", "qty": 9, "unit_price": 1.0}},
		{Name: cart.EffectNoteAdded, Payload: map[string]any{"scope": "order", "len": 4}},
	} {
		r.repo.calls = nil
		r.project(t, meta(), eff)
		if lines := r.lines(t, id); len(lines) != 1 || lines[0].Qty != 2 {
			t.Fatalf("efecto sin foto: líneas = %+v, quiero las que había", lines)
		}
		if !reflect.DeepEqual(r.repo.calls, []string{"GetOpenIntake", "UpsertIntake"}) {
			t.Errorf("llamadas = %v, quiero asegurar la solicitud sin tocar líneas", r.repo.calls)
		}
	}
	// Y un efecto histórico sin solicitud previa la abre, sin líneas.
	fresh := newRig()
	fresh.project(t, meta(), modules.Effect{Name: cart.EffectItemAdded})
	if got := fresh.onlyIntake(t); got.Status != "open" || len(fresh.lines(t, got.ID)) != 0 {
		t.Errorf("solicitud = %+v, quiero una abierta y sin líneas", got)
	}
}

// La foto se lee en sus dos formas —en proceso y tras el round-trip JSON—, con los
// ítems mal formados omitidos y las claves ausentes a cero.
func TestProjector_OpenLines_SnapshotShapes(t *testing.T) {
	want := []cartLine{
		{SKU: "PAN", Label: "Pan", Qty: 2, UnitPrice: 2.5, Customization: "tostado"},
		{SKU: "QUESO", Label: "Queso", Qty: 1, UnitPrice: 3},
	}
	cases := []struct {
		name  string
		items any
		want  []cartLine
	}{
		{name: "in-process", items: []map[string]any{
			{"sku": "PAN", "label": "Pan", "qty": 2, "unit_price": 2.5, "customization": "tostado"},
			{"sku": "QUESO", "label": "Queso", "qty": 1, "unit_price": 3.0},
		}, want: want},
		{name: "JSON round-trip", items: []any{
			map[string]any{"sku": "PAN", "label": "Pan", "qty": float64(2), "unit_price": 2.5, "customization": "tostado"},
			map[string]any{"sku": "QUESO", "label": "Queso", "qty": float64(1), "unit_price": float64(3)},
		}, want: want},
		{name: "integer price and int64 qty", items: []map[string]any{
			{"sku": "PAN", "label": "Pan", "qty": int64(2), "unit_price": 2},
		}, want: []cartLine{{SKU: "PAN", Label: "Pan", Qty: 2, UnitPrice: 2}}},
		{name: "malformed entries are skipped", items: []any{
			"no soy un mapa", nil, 7,
			map[string]any{"sku": "QUESO", "label": "Queso", "qty": float64(1), "unit_price": 3.0},
		}, want: []cartLine{{SKU: "QUESO", Label: "Queso", Qty: 1, UnitPrice: 3}}},
		{name: "missing or mistyped keys are zero", items: []any{
			map[string]any{"sku": 7, "qty": "2", "unit_price": "2.5", "customization": true},
		}, want: []cartLine{{}}},
		{name: "snapshot of another type is an empty snapshot", items: "roto", want: []cartLine{}},
		{name: "nil snapshot is an empty snapshot", items: nil, want: []cartLine{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig()
			r.project(t, meta(), snapshotEffect(cart.EffectItemAdded, line("VIEJA", 9, 9)))
			r.project(t, meta(), modules.Effect{Name: cart.EffectItemAdded, Payload: map[string]any{"items": tc.items}})
			if lines := r.lines(t, r.onlyIntake(t).ID); !reflect.DeepEqual(lines, tc.want) {
				t.Errorf("líneas = %+v\nquiero   %+v", lines, tc.want)
			}
		})
	}
}

// La solicitud declara a su padre (D-043.21): una abierta LEGADA sin evento se estampa
// con el de la meta; una que ya declara OTRO no se pisa, y queda un Warn.
func TestProjector_ItemAdded_EventStamping(t *testing.T) {
	t.Run("legacy open intake is stamped", func(t *testing.T) {
		r := newRig()
		seed := store.Intake{ID: "legada", TenantID: tenantID, ContactID: contactID, SessionID: sessionID, Status: "open", Total: 7}
		if err := r.repo.MemoryRepository.UpsertIntake(context.Background(), seed); err != nil {
			t.Fatalf("sembrar: %v", err)
		}
		r.project(t, meta(), snapshotEffect(cart.EffectItemAdded, line("PAN", 1, 2)))
		if got := r.onlyIntake(t); got.ID != "legada" || got.EventID != eventID || got.Total != 7 {
			t.Errorf("solicitud = %+v, quiero la legada estampada con el evento de la meta y su total intacto", got)
		}
	})
	t.Run("another event is not overwritten", func(t *testing.T) {
		log := captureLog(t)
		r := newRig()
		seed := store.Intake{ID: "de-otro", TenantID: tenantID, ContactID: contactID, SessionID: sessionID, Status: "open", EventID: "evento-anterior"}
		if err := r.repo.MemoryRepository.UpsertIntake(context.Background(), seed); err != nil {
			t.Fatalf("sembrar: %v", err)
		}
		r.project(t, meta(), snapshotEffect(cart.EffectItemAdded, line("PAN", 1, 2)))
		if got := r.onlyIntake(t); got.ID != "de-otro" || got.EventID != "evento-anterior" {
			t.Errorf("solicitud = %+v, quiero la misma con SU evento", got)
		}
		if lines := r.lines(t, "de-otro"); len(lines) != 1 {
			t.Errorf("líneas = %+v: las líneas sí siguen su curso", lines)
		}
		for _, want := range []string{"level=WARN", "cart: la solicitud abierta ya declara OTRO evento; no se pisa (D-043.21)",
			"intake_id=de-otro", "event_id_solicitud=evento-anterior", "event_id_meta=" + eventID} {
			if !strings.Contains(log.String(), want) {
				t.Errorf("el log no lleva %q:\n%s", want, log)
			}
		}
	})
	t.Run("meta without event touches nothing", func(t *testing.T) {
		log := captureLog(t)
		r := newRig()
		seed := store.Intake{ID: "con-padre", TenantID: tenantID, ContactID: contactID, Status: "open", EventID: "evento-anterior"}
		if err := r.repo.MemoryRepository.UpsertIntake(context.Background(), seed); err != nil {
			t.Fatalf("sembrar: %v", err)
		}
		m := meta()
		m.EventID = ""
		r.project(t, m, snapshotEffect(cart.EffectItemAdded, line("PAN", 1, 2)))
		if got := r.onlyIntake(t); got.EventID != "evento-anterior" {
			t.Errorf("solicitud = %+v, quiero su evento intacto", got)
		}
		if log.Len() != 0 {
			t.Errorf("log inesperado: %s", log)
		}
	})
}

// D-044.46: si el evento YA tiene contenido durable que la primera pregunta no ve (el
// borrador del pipeline, en pending_approval), se REUSA la fila sin tocarle la
// cabecera, y las líneas del carrito cuelgan de ella.
func TestProjector_ItemAdded_ReusesTheContentTheEventAlreadyHas(t *testing.T) {
	log := captureLog(t)
	r := newRig()
	draft := store.Intake{ID: "borrador-del-pipeline", TenantID: tenantID, ContactID: contactID, SessionID: sessionID,
		Status: intakes.StatusPendingApproval, EventID: eventID, Total: 42}
	if err := r.repo.MemoryRepository.UpsertIntake(context.Background(), draft); err != nil {
		t.Fatalf("sembrar el borrador: %v", err)
	}
	r.project(t, meta(), snapshotEffect(cart.EffectItemAdded, line("EMPA", 3, 2500)))

	got := r.onlyIntake(t)
	if got.ID != draft.ID || got.Status != intakes.StatusPendingApproval || got.Total != 42 {
		t.Errorf("solicitud = %+v, quiero el borrador reusado con su estado y su total", got)
	}
	if lines := r.lines(t, draft.ID); !reflect.DeepEqual(lines, []cartLine{{SKU: "EMPA", Label: "Etiqueta de EMPA", Qty: 3, UnitPrice: 2500}}) {
		t.Errorf("líneas = %+v, quiero las del carrito colgadas del borrador", lines)
	}
	if !reflect.DeepEqual(r.repo.calls, []string{"GetOpenIntake", "GetIntakeByEvent", "ReplaceIntakeItems"}) {
		t.Errorf("llamadas = %v: reusar la fila del evento NO lleva UpsertIntake", r.repo.calls)
	}
	for _, want := range []string{"level=INFO", "cart: el evento YA tenía contenido durable; se reusa en vez de crear otro (D-044.46)",
		"intake_id=" + draft.ID, "event_id=" + eventID, "status=" + intakes.StatusPendingApproval} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("el log no lleva %q:\n%s", want, log)
		}
	}
}

// Se porta tal cual: la segunda pregunta no filtra por estado, así que un item_added
// REENTREGADO tras el cierre reusa la solicitud cerrada de su evento y le reescribe
// las líneas con su foto, sin reabrirla.
func TestProjector_ItemAdded_RedeliveredAfterCloseRewritesTheClosedIntakeLines(t *testing.T) {
	r := newRig()
	added := snapshotEffect(cart.EffectItemAdded, line("PAN", 1, 2))
	r.project(t, meta(), added)
	r.project(t, meta(), closedEffect(t, line("PAN", 1, 2), line("QUESO", 1, 3)))
	closed := r.onlyIntake(t)
	r.project(t, meta(), added) // reentrega
	if got := r.onlyIntake(t); got.ID != closed.ID || got.Status != "closed" {
		t.Errorf("solicitud = %+v, quiero la misma y cerrada", got)
	}
	if lines := r.lines(t, closed.ID); len(lines) != 1 || lines[0].SKU != "PAN" {
		t.Errorf("líneas = %+v: la reentrega dejó la foto vieja", lines)
	}
}

// Sin evento en la meta no se inventa nada: la solicitud nace sin padre, no se
// pregunta por evento y queda un Warn.
func TestProjector_ItemAdded_WithoutEventIsBornOrphanAndLogged(t *testing.T) {
	log := captureLog(t)
	r := newRig()
	m := meta()
	m.EventID = ""
	r.project(t, m, snapshotEffect(cart.EffectItemAdded, line("PAN", 1, 2)))
	if got := r.onlyIntake(t); got.EventID != "" || got.Status != "open" {
		t.Errorf("solicitud = %+v, quiero una abierta sin evento", got)
	}
	if !reflect.DeepEqual(r.repo.calls, []string{"GetOpenIntake", "UpsertIntake", "ReplaceIntakeItems"}) {
		t.Errorf("llamadas = %v: sin evento no hay segunda pregunta", r.repo.calls)
	}
	for _, want := range []string{"level=WARN",
		"cart: item_added sin evento en la meta; la solicitud nace sin padre y el CHECK de la 0054 decidirá",
		"tenant_id=" + tenantID, "session_id=" + sessionID} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("el log no lleva %q:\n%s", want, log)
		}
	}
}

// Defensa en profundidad del hallazgo #24: el alta que choca contra el único parcial
// del evento sube tal cual —sigue siendo reconocible como violación de único—, no se
// reintenta, y deja un Error con lo necesario para saber QUÉ pedido se perdió.
func TestProjector_ItemAdded_EventCollisionStaysVisible(t *testing.T) {
	log := captureLog(t)
	r := newRig()
	collision := fmt.Errorf("store: upsert solicitud: %w", &pgconn.PgError{Code: "23505", ConstraintName: "intakes_event_id_uidx"})
	r.repo.fail["UpsertIntake"] = collision

	err := r.p.Project(context.Background(), meta(), snapshotEffect(cart.EffectItemAdded, line("EMPA", 1, 2500)))
	if !errors.Is(err, collision) || !postgres.IsUniqueViolation(err) {
		t.Fatalf("Project = %v, quiero el choque tal cual, reconocible como violación de único", err)
	}
	if !reflect.DeepEqual(r.repo.calls, []string{"GetOpenIntake", "GetIntakeByEvent", "UpsertIntake"}) {
		t.Errorf("llamadas = %v, quiero UN intento de alta y ninguna línea escrita", r.repo.calls)
	}
	for _, want := range []string{"level=ERROR",
		"cart: el evento ya tenía un intake (intakes_event_id_uidx); el pedido de este turno NO se pudo guardar (hallazgo #24)",
		"tenant_id=" + tenantID, "contact_id=" + contactID, "session_id=" + sessionID, "event_id=" + eventID, "intake_id_intentado="} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("el log no lleva %q:\n%s", want, log)
		}
	}

	// Un fallo que NO es de único sube igual, pero sin ese log.
	log.Reset()
	other := newRig()
	boom := errors.New("pg caído")
	other.repo.fail["UpsertIntake"] = boom
	if err := other.p.Project(context.Background(), meta(), snapshotEffect(cart.EffectItemAdded, line("EMPA", 1, 2500))); !errors.Is(err, boom) {
		t.Fatalf("Project = %v, quiero el error del almacén", err)
	}
	if strings.Contains(log.String(), "hallazgo #24") {
		t.Errorf("un fallo cualquiera se logueó como colisión de evento:\n%s", log)
	}
}

// Todo error del almacén sube, y corta ahí.
func TestProjector_OpenLines_StoreErrorsPropagate(t *testing.T) {
	boom := errors.New("pg caído")
	cases := []struct {
		method string
		calls  []string
	}{
		{"GetOpenIntake", []string{"GetOpenIntake"}},
		{"GetIntakeByEvent", []string{"GetOpenIntake", "GetIntakeByEvent"}},
		{"ReplaceIntakeItems", []string{"GetOpenIntake", "GetIntakeByEvent", "UpsertIntake", "ReplaceIntakeItems"}},
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			r := newRig()
			r.repo.fail[tc.method] = boom
			err := r.p.Project(context.Background(), meta(), snapshotEffect(cart.EffectItemAdded, line("PAN", 1, 2)))
			if !errors.Is(err, boom) {
				t.Fatalf("Project = %v, quiero el error del almacén", err)
			}
			if !reflect.DeepEqual(r.repo.calls, tc.calls) {
				t.Errorf("llamadas = %v, quiero %v", r.repo.calls, tc.calls)
			}
		})
	}
}

// El efecto que el módulo DECLARA es el que el proyector materializa: conduciendo el
// carrito de verdad, intake_items es el espejo del carrito en cada paso —también con
// la indicación y el split—, y el cierre no duplica ni una línea.
func TestProjector_MirrorsTheRealCartStepByStep(t *testing.T) {
	r := newRig()
	m := cart.New()
	vars := seededVars()
	play := func(inputs ...string) {
		t.Helper()
		for _, in := range inputs {
			res := turn(m, model.Conversation{Vars: vars}, in, nil)
			for _, eff := range res.Effects {
				if r.p.Handles(eff.Name) {
					r.project(t, meta(), eff)
				}
			}
			vars = res.Vars
		}
	}
	mirror := func(step string) {
		t.Helper()
		want := stateOf(t, vars).Lines
		if got := r.lines(t, r.onlyIntake(t).ID); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: intake_items = %+v\nel carrito dice %+v", step, got, want)
		}
	}

	play("1", "1", "2", "2") // Café ×2
	mirror("tras el primer item_added")
	if got := r.onlyIntake(t); got.Status != "open" {
		t.Fatalf("solicitud = %+v, quiero que siga ABIERTA", got)
	}
	play("3", "2", "sin azúcar") // solo para 1: la línea se parte
	mirror("tras el split")
	play("1", "2", "2", "3") // Té ×3
	mirror("tras el segundo item_added")
	play("2", "3", "Dejarlo en portería", "1") // resumen, nota del pedido, confirmar
	mirror("tras el cierre")

	closed := r.onlyIntake(t)
	if closed.Status != "closed" || closed.Total != 11 || closed.CustomerNote != "Dejarlo en portería" || closed.EventID != eventID {
		t.Errorf("solicitud = %+v, quiero la misma cerrada con total 11, la nota y el evento", closed)
	}
}
