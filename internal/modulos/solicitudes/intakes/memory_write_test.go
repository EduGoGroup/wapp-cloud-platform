package intakes_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// Lo que estos tests fijan es lo que la suite de contrato NO puede: lo que es solo del doble (la
// fila legada sin evento, las fechas EXACTAS del reloj inyectado, los datos del comprador en
// claro) y lo que Postgres resuelve de otro modo (la solicitud inexistente en InsertRevision).

// Aserciones de compilación de lo que promete memory_write.go.
var (
	_ func(*intakes.MemoryStore, context.Context, intakes.Revision) (intakes.Revision, error)                                         = (*intakes.MemoryStore).InsertRevision
	_ func(*intakes.MemoryStore, context.Context, string, string, string, []string) (intakes.Intake, error)                           = (*intakes.MemoryStore).UpdateStatus
	_ func(*intakes.MemoryStore, context.Context, string, string, intakes.ShippingPolicy) error                                       = (*intakes.MemoryStore).EnsureShippingLine
	_ func(*intakes.MemoryStore, context.Context, string, string, []intakes.Item, []string, intakes.EditMode) (intakes.Detail, error) = (*intakes.MemoryStore).ReplaceItems
	_ func(*intakes.MemoryStore, context.Context, string, string, intakes.Revalidation, string, []string) (intakes.Detail, error)     = (*intakes.MemoryStore).ApplyRevalidation
	_ func(*intakes.MemoryStore, context.Context, string, string, []string) (intakes.DiscardOutcome, error)                           = (*intakes.MemoryStore).Discard
	_ func(*intakes.MemoryStore, context.Context, string, string) error                                                               = (*intakes.MemoryStore).AbandonByEvent
	_ func(*intakes.MemoryStore, context.Context, string, string, string) error                                                       = (*intakes.MemoryStore).PutBuyerField
)

// TestMemoryStore_InsertRevision_DatesAndDoesNotRequireTheIntake: sin fecha, la revisión lleva el
// instante del reloj; con fecha, se respeta (así se siembra una revisión antigua); y no hace falta
// que la solicitud exista — la divergencia consciente con la FK de la tabla real.
func TestMemoryStore_InsertRevision_DatesAndDoesNotRequireTheIntake(t *testing.T) {
	store, clock := newMemoryStore()
	undated, err := store.InsertRevision(ctx, intakes.Revision{IntakeID: "suelta", Kind: intakes.RevisionKindCart, Payload: []byte(`{"version":1}`)})
	if err != nil || undated.RevisionNo != 1 || !undated.CreatedAt.Equal(clock.Now()) {
		t.Errorf("sin fecha = (%+v, %v), quería la nº 1 fechada con el reloj", undated, err)
	}
	old := time.Date(2020, 2, 3, 4, 5, 6, 0, time.UTC)
	dated, err := store.InsertRevision(ctx, intakes.Revision{
		IntakeID: "suelta", RevisionNo: 40, Kind: intakes.RevisionKindCart, Payload: []byte(`{"version":1}`),
		CreatedAt: old, LiteralPrunedAt: old,
	})
	if err != nil || dated.RevisionNo != 2 || !dated.CreatedAt.Equal(old) {
		t.Errorf("con fecha = (%+v, %v), quería la nº 2 con su fecha, %v", dated, err, old)
	}
	if !dated.LiteralPrunedAt.IsZero() {
		t.Errorf("una revisión recién escrita sale con sello de poda: %v", dated.LiteralPrunedAt)
	}
	if got := store.Revisions("suelta"); len(got) != 2 || !got[1].CreatedAt.Equal(old) || !got[1].LiteralPrunedAt.IsZero() {
		t.Errorf("revisiones guardadas = %+v", got)
	}
}

// TestMemoryStore_InsertRevision_UnsplittablePayloadWritesNothing: si el literal no se puede sacar
// (un `source_text` que no es una cadena), se devuelve ese error y NO se guarda la revisión: lo
// contrario dejaría en claro algo que se llama `source_text`.
func TestMemoryStore_InsertRevision_UnsplittablePayloadWritesNothing(t *testing.T) {
	store, _ := newMemoryStore()
	got, err := store.InsertRevision(ctx, intakes.Revision{
		IntakeID: "i-1", Kind: intakes.RevisionKindInterpreted, Payload: []byte(`{"version":1,"source_text":{"a":1}}`),
	})
	if err == nil || !strings.HasPrefix(err.Error(), "intakes: source_text del payload no es una cadena: ") {
		t.Errorf("err = %v, quería el de SplitLiteral", err)
	}
	if got.RevisionNo != 0 || len(store.PersistedRevisions("i-1")) != 0 {
		t.Errorf("se guardó una revisión con el payload sin partir: %+v", store.PersistedRevisions("i-1"))
	}
	if _, err := store.InsertRevision(ctx, intakes.Revision{IntakeID: "i-1", Kind: intakes.RevisionKindCart}); !errors.Is(err, intakes.ErrEmptyRevisionPayload) {
		t.Errorf("payload vacío: err = %v, quería ErrEmptyRevisionPayload", err)
	}
}

// TestMemoryStore_Writes_RefreshUpdatedAtWithTheClock: toda escritura de la cabecera deja en
// UpdatedAt EXACTAMENTE el instante del reloj —como el `updated_at = now()` del store real—, y la
// revisión que escribe lleva ese mismo instante. La garantía de la línea de envío que no cambia
// nada no lo mueve.
func TestMemoryStore_Writes_RefreshUpdatedAtWithTheClock(t *testing.T) {
	open := []string{intakes.StatusOpen}
	writes := map[string]func(store *intakes.MemoryStore) error{
		"UpdateStatus": func(s *intakes.MemoryStore) error {
			_, err := s.UpdateStatus(ctx, tenant1, "i-1", intakes.StatusCancelled, open)
			return err
		},
		"EnsureShippingLine": func(s *intakes.MemoryStore) error {
			return s.EnsureShippingLine(ctx, tenant1, "i-1", intakes.ShippingAlways)
		},
		"ReplaceItems": func(s *intakes.MemoryStore) error {
			_, err := s.ReplaceItems(ctx, tenant1, "i-1", twoLines()[:1], open, intakes.EditPlain)
			return err
		},
		"ApplyRevalidation": func(s *intakes.MemoryStore) error {
			rv := intakes.Revalidation{Changes: []intakes.LineChange{{SKU: "queso", Label: "Queso", Qty: 1, From: 3, Removed: true}}, TotalBefore: 7, TotalAfter: 4}
			_, err := s.ApplyRevalidation(ctx, tenant1, "i-1", rv, "tu pedido cambió", open)
			return err
		},
		"Discard": func(s *intakes.MemoryStore) error {
			_, err := s.Discard(ctx, tenant1, "i-1", open)
			return err
		},
		"AbandonByEvent": func(s *intakes.MemoryStore) error { return s.AbandonByEvent(ctx, tenant1, "ev-1") },
	}
	for name, write := range writes {
		store, clock := newMemoryStore()
		store.Add(tenant1, seeded("i-1", intakes.StatusOpen, 1), twoLines()...)
		store.SetEvent("ev-1", "cancelled")
		store.BindEvent("i-1", "ev-1")
		clock.Advance(time.Hour)
		if err := write(store); err != nil {
			t.Errorf("%s: error inesperado %v", name, err)
			continue
		}
		got := mustGet(t, store, tenant1, "i-1")
		if !got.UpdatedAt.Equal(clock.Now()) {
			t.Errorf("%s: UpdatedAt = %v, quería el instante del reloj, %v", name, got.UpdatedAt, clock.Now())
		}
		for _, rev := range got.Revisions {
			if !rev.CreatedAt.Equal(clock.Now()) {
				t.Errorf("%s: la revisión %d está fechada en %v, quería %v", name, rev.RevisionNo, rev.CreatedAt, clock.Now())
			}
		}
	}

	store, clock := newMemoryStore()
	store.Add(tenant1, seeded("i-1", intakes.StatusOpen, 1), twoLines()...)
	if err := store.EnsureShippingLine(ctx, tenant1, "i-1", intakes.ShippingAlways); err != nil {
		t.Fatalf("EnsureShippingLine: error inesperado %v", err)
	}
	first := clock.Now()
	clock.Advance(time.Hour)
	if err := store.EnsureShippingLine(ctx, tenant1, "i-1", intakes.ShippingAlways); err != nil {
		t.Fatalf("EnsureShippingLine repetida: error inesperado %v", err)
	}
	if got := mustGet(t, store, tenant1, "i-1"); !got.UpdatedAt.Equal(first) {
		t.Errorf("repetir EnsureShippingLine movió UpdatedAt de %v a %v", first, got.UpdatedAt)
	}
}

// TestMemoryStore_ReplaceItems_KeepsTheLiteralOutOfWhatIsStored: la revisión `corrected` pasa por
// el mismo camino de escritura que las demás; lo guardado nunca lleva literal y la «última
// revisión» que señala una corrección se mira en crudo, sin podar la que corrige.
func TestMemoryStore_ReplaceItems_KeepsTheLiteralOutOfWhatIsStored(t *testing.T) {
	store, clock, log := retentionStore(t)
	clock.Advance(intakes.DefaultLiteralTTL + time.Hour) // la interpretada (nº 2) ya venció
	got, err := store.ReplaceItems(ctx, tenant1, "i-1", twoLines(), []string{intakes.StatusPendingApproval}, intakes.EditAsCorrection)
	if err != nil {
		t.Fatalf("ReplaceItems: error inesperado %v", err)
	}
	if len(got.Revisions) != 3 || got.Revisions[2].Kind != intakes.RevisionKindCorrected {
		t.Fatalf("revisiones devueltas = %+v, quería tres con la corregida al final", got.Revisions)
	}
	var signal intakes.CorrectionSignal
	persisted := store.PersistedRevisions("i-1")
	if err := json.Unmarshal(persisted[2].Payload, &signal); err != nil {
		t.Fatalf("leyendo la señal: %v", err)
	}
	if want := (intakes.CorrectionSignal{AsCorrection: true, CorrectsRevisionNo: 2, CorrectsKind: intakes.RevisionKindInterpreted}); signal != want {
		t.Errorf("señal = %+v, quería %+v", signal, want)
	}
	// El detalle devuelto SÍ es una lectura: poda la vencida, una vez.
	if got.Revisions[1].LiteralPrunedAt.IsZero() || len(log.snapshot()) != 1 {
		t.Errorf("el detalle devuelto no podó la revisión vencida: sello %v, %d anuncios", got.Revisions[1].LiteralPrunedAt, len(log.snapshot()))
	}
}

// TestMemoryStore_Discard_LegacyRowWithoutEvent: la fila legada sin ligadura no tiene evento vivo
// que mirar y es descartable; AbandonByEvent con el evento vacío no la alcanza.
func TestMemoryStore_Discard_LegacyRowWithoutEvent(t *testing.T) {
	store, _ := newMemoryStore()
	store.Add(tenant1, seeded("legacy", intakes.StatusOpen, 1), twoLines()...)
	store.SetEvent("", "open") // ni siquiera un evento con id vacío la liga

	if err := store.AbandonByEvent(ctx, tenant1, ""); err != nil {
		t.Fatalf("AbandonByEvent con el evento vacío: error inesperado %v", err)
	}
	if stored := store.StoredStatus(tenant1, "legacy"); stored != intakes.StatusOpen {
		t.Fatalf("AbandonByEvent con el evento vacío abandonó la fila legada: %q", stored)
	}
	out, err := store.Discard(ctx, tenant1, "legacy", []string{intakes.StatusOpen})
	if want := (intakes.DiscardOutcome{Discarded: true, Status: intakes.StatusOpen}); err != nil || out != want {
		t.Errorf("Discard de la fila legada = (%+v, %v), quería %+v", out, err, want)
	}
	if stored := store.StoredStatus(tenant1, "legacy"); stored != intakes.StatusAbandoned {
		t.Errorf("tras el descarte está %q, quería abandoned", stored)
	}
}

// TestMemoryStore_Discard_DoesNotCloseALiveContainer: con el evento `open` el descarte se frena y
// el evento sigue `open` — se cancela por su propia puerta, no por ésta.
func TestMemoryStore_Discard_DoesNotCloseALiveContainer(t *testing.T) {
	store, _ := newMemoryStore()
	store.Add(tenant1, seeded("i-1", intakes.StatusOpen, 1))
	store.SetEvent("ev-1", "open")
	store.BindEvent("i-1", "ev-1")
	out, err := store.Discard(ctx, tenant1, "i-1", []string{intakes.StatusOpen})
	if want := (intakes.DiscardOutcome{Status: intakes.StatusOpen, LiveEvent: true}); err != nil || out != want {
		t.Errorf("Discard con el evento vivo = (%+v, %v), quería %+v", out, err, want)
	}
	if status := store.EventStatus("ev-1"); status != "open" {
		t.Errorf("el evento quedó %q, quería open", status)
	}
	if revs := store.PersistedRevisions("i-1"); len(revs) != 0 {
		t.Errorf("el descarte frenado escribió revisiones: %+v", revs)
	}
}

// TestMemoryStore_PutBuyerField_MergesAndFlagsThePresence: el campo se FUSIONA en el checklist de
// la solicitud; repetir la clave pisa su valor; un valor vacío se guarda; la clave vacía se rechaza
// sin guardar; y desde el primer campo Get dice BuyerDataPresent —ListDetails, nunca—.
func TestMemoryStore_PutBuyerField_MergesAndFlagsThePresence(t *testing.T) {
	store, _ := newMemoryStore()
	store.Add(tenant1, seeded("i-1", intakes.StatusOpen, 1))
	store.Add(tenant1, seeded("i-2", intakes.StatusOpen, 2))
	if mustGet(t, store, tenant1, "i-1").BuyerDataPresent {
		t.Fatal("una solicitud sin datos del comprador dice BuyerDataPresent")
	}
	if err := store.PutBuyerField(ctx, "i-1", "", "valor"); !errors.Is(err, intakes.ErrBuyerFieldEmpty) {
		t.Errorf("clave vacía: err = %v, quería ErrBuyerFieldEmpty", err)
	}
	if mustGet(t, store, tenant1, "i-1").BuyerDataPresent || len(store.BuyerDataOf("i-1")) != 0 {
		t.Error("el campo rechazado dejó datos guardados")
	}
	for _, f := range [][2]string{{"rut", "11.111.111-1"}, {"direccion", "Av. Siempre Viva 742"}, {"rut", "22.222.222-2"}, {"nota", ""}} {
		if err := store.PutBuyerField(ctx, "i-1", f[0], f[1]); err != nil {
			t.Fatalf("PutBuyerField(%s): error inesperado %v", f[0], err)
		}
	}
	want := intakes.BuyerData{"rut": "22.222.222-2", "direccion": "Av. Siempre Viva 742", "nota": ""}
	requireBuyerData(t, store.BuyerDataOf("i-1"), want)
	if !mustGet(t, store, tenant1, "i-1").BuyerDataPresent {
		t.Error("con datos guardados, Get no dice BuyerDataPresent")
	}
	if mustGet(t, store, tenant1, "i-2").BuyerDataPresent || len(store.BuyerDataOf("i-2")) != 0 {
		t.Error("los datos de una solicitud aparecen en otra")
	}
	details, err := store.ListDetails(ctx, tenant1, intakes.Filter{}, 10)
	if err != nil {
		t.Fatalf("ListDetails: error inesperado %v", err)
	}
	for _, d := range details {
		if d.BuyerDataPresent {
			t.Errorf("ListDetails publica BuyerDataPresent de %s: el export no dice nada del comprador", d.ID)
		}
	}
}

// requireBuyerData exige que el checklist guardado tenga exactamente las claves de `want`, cada
// una con su valor.
func requireBuyerData(t *testing.T, got, want intakes.BuyerData) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("checklist = %v, quería %v", got, want)
	}
	for k, v := range want {
		if gv, ok := got[k]; !ok || gv != v {
			t.Errorf("checklist[%q] = %q (presente=%v), quería %q", k, gv, ok, v)
		}
	}
}
