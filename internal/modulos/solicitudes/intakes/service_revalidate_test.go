package intakes

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
)

// La escritura de una revalidación (Service.ApplyRevalidation). Los dobles y la siembra están en
// service_test.go.

// svcRevalStore recuerda qué se le pidió y contesta con `detail` y `err`.
type svcRevalStore struct {
	Store
	detail Detail
	err    error
	gets   []string
	writes []svcRevalWrite
}

// svcRevalWrite es UNA escritura de revalidación, tal como la vio el store.
type svcRevalWrite struct {
	tenantID, intakeID string
	rv                 Revalidation
	renderedText       string
	expected           []string
}

func (s *svcRevalStore) Get(_ context.Context, tenantID, intakeID string) (Detail, error) {
	s.gets = append(s.gets, tenantID+"|"+intakeID)
	return s.detail, s.err
}

func (s *svcRevalStore) ApplyRevalidation(_ context.Context, tenantID, intakeID string, rv Revalidation, renderedText string, expected []string) (Detail, error) {
	s.writes = append(s.writes, svcRevalWrite{tenantID: tenantID, intakeID: intakeID, rv: rv, renderedText: renderedText, expected: expected})
	return s.detail, s.err
}

// svcRepriced es una revalidación CON cambios: la torta sube de 18000 a 20000.
func svcRepriced() Revalidation {
	return Revalidation{
		Items: []Item{
			{SKU: "torta-v1", Label: "Torta 10-12 porciones", Customization: "sin maní", Qty: 1, UnitPrice: 20000},
		},
		Changes:     []LineChange{{SKU: "torta-v1", Label: "Torta 10-12 porciones", Qty: 1, From: 18000, To: 20000}},
		TotalBefore: 21500,
		TotalAfter:  23500,
	}
}

// TestApplyRevalidation_WithoutChangesOnlyReads es REQ-35b: sin cambios devuelve la solicitud tal
// como está, con UNA lectura y cero escrituras, sin mirar el texto. El error de la lectura sale
// tal cual.
func TestApplyRevalidation_WithoutChangesOnlyReads(t *testing.T) {
	t.Parallel()
	unchanged := Revalidation{Items: svcItems(), TotalBefore: 21500, TotalAfter: 21500}
	for _, storeErr := range []error{nil, ErrNotFound} {
		st := &svcRevalStore{detail: Detail{Intake: svcIntake(svcIntakeID, StatusConfirmed), Items: svcItems()}, err: storeErr}

		got, err := NewService(st).ApplyRevalidation(context.Background(), svcTenantA, svcIntakeID, unchanged, "")
		if !errors.Is(err, storeErr) || (storeErr == nil && err != nil) {
			t.Errorf("ApplyRevalidation devolvió el error %v, quería %v", err, storeErr)
		}
		if !reflect.DeepEqual(got, st.detail) {
			t.Errorf("ApplyRevalidation devolvió %+v, quería lo que leyó el store", got)
		}
		if !slices.Equal(st.gets, []string{svcTenantA + "|" + svcIntakeID}) || len(st.writes) != 0 {
			t.Errorf("el store recibió %d lecturas y %d escrituras, quería (1, 0)", len(st.gets), len(st.writes))
		}
	}
}

// TestApplyRevalidation_ChangesNeedTheSentText: una revisión que registre el cambio sin lo que se
// le dijo al cliente no sirve de rastro. Se rechaza sin tocar el store.
func TestApplyRevalidation_ChangesNeedTheSentText(t *testing.T) {
	t.Parallel()
	st := &svcRevalStore{}

	_, err := NewService(st).ApplyRevalidation(context.Background(), svcTenantA, svcIntakeID, svcRepriced(), "")
	if !errors.Is(err, ErrEmptyRevalidationText) {
		t.Fatalf("ApplyRevalidation devolvió %v, quería ErrEmptyRevalidationText", err)
	}
	if len(st.gets) != 0 || len(st.writes) != 0 {
		t.Errorf("el store recibió %d lecturas y %d escrituras, quería (0, 0)", len(st.gets), len(st.writes))
	}
}

// TestApplyRevalidation_DelegatesTheWriteExpectingOpen: con cambios, UNA escritura en el store
// con el diff y el texto SIN tocar —solo la cadena vacía se rechaza—, esperando las variantes
// guardadas de `open`. Lo que conteste el store, detalle o error, sale tal cual.
func TestApplyRevalidation_DelegatesTheWriteExpectingOpen(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		text     string
		storeErr error
	}{
		{name: "applied", text: "  Subió la torta \n"},
		{name: "blank text is still a text", text: " "},
		{name: "not open any more", text: "aviso", storeErr: ErrConflict},
		{name: "foreign intake", text: "aviso", storeErr: ErrNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			st := &svcRevalStore{detail: Detail{Intake: svcIntake(svcIntakeID, StatusOpen)}, err: c.storeErr}
			rv := svcRepriced()

			got, err := NewService(st).ApplyRevalidation(context.Background(), svcTenantA, svcIntakeID, rv, c.text)
			if !errors.Is(err, c.storeErr) || (c.storeErr == nil && err != nil) {
				t.Errorf("ApplyRevalidation devolvió el error %v, quería %v", err, c.storeErr)
			}
			if !reflect.DeepEqual(got, st.detail) {
				t.Errorf("ApplyRevalidation devolvió %+v, quería lo que contestó el store", got)
			}
			want := []svcRevalWrite{{tenantID: svcTenantA, intakeID: svcIntakeID, rv: rv, renderedText: c.text, expected: StoredVariants(StatusOpen)}}
			if !reflect.DeepEqual(st.writes, want) || len(st.gets) != 0 {
				t.Errorf("el store recibió %+v (y %d lecturas), quería %+v", st.writes, len(st.gets), want)
			}
			if !slices.Contains(st.writes[0].expected, StatusOpen) || slices.Contains(st.writes[0].expected, StatusConfirmed) {
				t.Errorf("estados esperados = %v, quería solo las variantes de open", st.writes[0].expected)
			}
		})
	}
}

// TestApplyRevalidation_OnTheRealStore recorre la escritura contra el store en memoria: deja UNA
// revisión `revalidated` con el texto que se mandó, NO mueve el estado, y no avisa, ni empuja, ni
// publica. Sobre una solicitud que ya no está `open`, ErrConflict y nada escrito.
func TestApplyRevalidation_OnTheRealStore(t *testing.T) {
	t.Parallel()
	const sentText = "Subió la torta: ahora $20.000"
	t.Run("open intake", func(t *testing.T) {
		t.Parallel()
		sc := newApproveScene(t, StatusOpen)

		detail, err := sc.svc.ApplyRevalidation(context.Background(), svcTenantA, svcIntakeID, svcRepriced(), sentText)
		if err != nil {
			t.Fatalf("ApplyRevalidation devolvió el error %v", err)
		}
		if detail.Status != StatusOpen || svcStatusOf(t, sc.store.MemoryStore) != StatusOpen {
			t.Errorf("estado %q, quería open: la revalidación no transiciona", detail.Status)
		}
		stored := sc.store.Revisions(svcIntakeID)
		last := stored[len(stored)-1]
		if len(stored) != 2 || last.Kind != RevisionKindRevalidated || last.RenderedText != sentText || last.CreatedBy != RevisionBySystem {
			t.Errorf("revisiones = %d, la última (%q, %q, por %q); quería 2 y una revalidated del sistema con el texto enviado",
				len(stored), last.Kind, last.RenderedText, last.CreatedBy)
		}
		if n := len(sc.notifier.calls) + len(sc.crm.calls) + len(sc.metrics.calls) + len(sc.quotes.quotes); n != 0 {
			t.Errorf("la revalidación avisó, empujó, publicó o envió %d veces, quería 0", n)
		}
	})
	t.Run("not open any more", func(t *testing.T) {
		t.Parallel()
		sc := newApproveScene(t, StatusConfirmed)

		_, err := sc.svc.ApplyRevalidation(context.Background(), svcTenantA, svcIntakeID, svcRepriced(), sentText)
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("ApplyRevalidation devolvió %v, quería ErrConflict", err)
		}
		if got := len(sc.store.Revisions(svcIntakeID)); got != 1 {
			t.Errorf("revisiones = %d, quería 1: el rechazo no escribe", got)
		}
	})
}
