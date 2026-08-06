package intakes_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// discard_test.go cubre el DESCARTE MANUAL por lotes (Plan 041 · T4.8, D-041.18):
// las cuatro razones del `skipped`, la idempotencia, el lote mixto y la puerta
// exclusiva por la que un `expired` legado sale de su estado.

const (
	discardTenant = "11111111-2222-3333-4444-555555555555"
	discardOtro   = "99999999-8888-7777-6666-555555555555"
)

// bandejaDescarte siembra una bandeja con una solicitud de cada caso que el lote
// tiene que saber contestar, más una de OTRO tenant.
func bandejaDescarte() *intakes.MemoryStore {
	st := intakes.NewMemoryStore()
	add := func(tenant, id, status, session, contacto string) {
		st.Add(tenant, intakes.Intake{
			ID: id, ContactID: contacto, SessionID: session, Status: status,
			Total: 7000, CreatedAt: time.Now(), UpdatedAt: time.Now(),
		})
	}
	add(discardTenant, "id-open", intakes.StatusOpen, "sess-a", "contacto-1")
	add(discardTenant, "id-expired", intakes.StatusExpired, "sess-a", "contacto-2")
	add(discardTenant, "id-abandoned", intakes.StatusAbandoned, "sess-a", "contacto-3")
	add(discardTenant, "id-confirmed", intakes.StatusConfirmed, "sess-a", "contacto-4")
	add(discardTenant, "id-vivo", intakes.StatusOpen, "sess-b", "contacto-5")
	add(discardOtro, "id-ajeno", intakes.StatusOpen, "sess-x", "contacto-6")
	// La conversación viva cuelga de (tenant, sesión, contacto), no del id de la
	// solicitud: es exactamente la ligadura que se deriva en producción.
	st.SetLiveCart(discardTenant, "sess-b", "contacto-5")
	return st
}

// razónDe busca la razón con la que el lote rechazó un id.
func razónDe(res intakes.DiscardResult, id string) string {
	for _, s := range res.Skipped {
		if s.IntakeID == id {
			return s.Reason
		}
	}
	return ""
}

// TestDiscardableStatuses_SeDerivanDeCanDiscard: la lista que viaja al WHERE del
// UPDATE no puede ser una segunda fuente de verdad. Si alguien amplía o recorta
// `discardable` sin tocar nada más, esto lo sigue.
func TestDiscardableStatuses_SeDerivanDeCanDiscard(t *testing.T) {
	got := intakes.DiscardableStatuses()

	if !slices.IsSorted(got) {
		t.Fatalf("DiscardableStatuses()=%v; tiene que ser determinista (ordenada)", got)
	}
	for _, status := range got {
		if !intakes.CanDiscard(status) {
			t.Fatalf("%q está en la lista pero CanDiscard dice que no es descartable", status)
		}
	}
	// La contracara: nada descartable puede faltar de la lista, o el UPDATE dejaría
	// filas inmortales en la bandeja.
	for _, status := range []string{
		intakes.StatusOpen, intakes.StatusExpired, intakes.StatusConfirmed,
		intakes.StatusPendingApproval, intakes.StatusAbandoned, intakes.StatusCancelled,
		intakes.StatusSettled, intakes.StatusRejected, intakes.StatusNeedsInfo,
		intakes.StatusDepositRequested, intakes.StatusDepositPaid,
	} {
		if intakes.CanDiscard(status) != slices.Contains(got, status) {
			t.Fatalf("%q: CanDiscard=%v pero en la lista=%v",
				status, intakes.CanDiscard(status), slices.Contains(got, status))
		}
	}
}

// TestDiscard_LoteVacío: un lote sin ids NO es "no hay nada que hacer". Casi
// siempre es una UI que perdió su selección, y un 200 con listas vacías le diría al
// dueño que se descartó lo que había marcado.
func TestDiscard_LoteVacío(t *testing.T) {
	svc := intakes.NewService(bandejaDescarte())

	if _, err := svc.Discard(context.Background(), discardTenant, nil); !errors.Is(err, intakes.ErrEmptyDiscardBatch) {
		t.Fatalf("err=%v, quiero ErrEmptyDiscardBatch", err)
	}
	if _, err := svc.Discard(context.Background(), discardTenant, []string{}); !errors.Is(err, intakes.ErrEmptyDiscardBatch) {
		t.Fatalf("lista vacía: err=%v, quiero ErrEmptyDiscardBatch", err)
	}
}

// TestDiscard_ElTopeDelLote: MaxDiscardBatch entra, uno más no. El límite se mide
// sobre lo que LLEGA (antes de colapsar repetidos): es una cota del cuerpo.
func TestDiscard_ElTopeDelLote(t *testing.T) {
	svc := intakes.NewService(bandejaDescarte())
	ctx := context.Background()

	justo := make([]string, intakes.MaxDiscardBatch)
	for i := range justo {
		justo[i] = "inexistente-" + strconv.Itoa(i)
	}
	res, err := svc.Discard(ctx, discardTenant, justo)
	if err != nil {
		t.Fatalf("con %d ids: err=%v, quiero que pase", intakes.MaxDiscardBatch, err)
	}
	if len(res.Skipped) != intakes.MaxDiscardBatch {
		t.Fatalf("skipped=%d, quiero %d", len(res.Skipped), intakes.MaxDiscardBatch)
	}

	var tooLarge *intakes.TooLargeBatchError
	_, err = svc.Discard(ctx, discardTenant, append(justo, "uno-de-más"))
	if !errors.As(err, &tooLarge) {
		t.Fatalf("con %d ids: err=%v, quiero *TooLargeBatchError", intakes.MaxDiscardBatch+1, err)
	}
	if tooLarge.Count != intakes.MaxDiscardBatch+1 || tooLarge.Max != intakes.MaxDiscardBatch {
		t.Fatalf("error=%+v; tiene que decir cuántos llegaron y cuál es el tope", tooLarge)
	}

	// El tope NO se esquiva repitiendo el mismo id: se cuenta antes de colapsar.
	repetidos := make([]string, intakes.MaxDiscardBatch+1)
	for i := range repetidos {
		repetidos[i] = "id-open"
	}
	if _, err := svc.Discard(ctx, discardTenant, repetidos); !errors.As(err, &tooLarge) {
		t.Fatalf("lote de repetidos: err=%v, quiero *TooLargeBatchError", err)
	}
}

// TestDiscard_LoteMixto_LasCuatroRazones es el corazón de la tarea: un lote con
// TODOS los casos a la vez sale con lo bueno descartado y cada rechazo con su razón
// exacta. Un rechazo NO revierte los demás.
func TestDiscard_LoteMixto_LasCuatroRazones(t *testing.T) {
	store := bandejaDescarte()
	svc := intakes.NewService(store)

	res, err := svc.Discard(context.Background(), discardTenant, []string{
		"id-open",      // descartable
		"id-expired",   // descartable (el legado del reloj derogado)
		"id-abandoned", // ya descartada
		"id-confirmed", // no se descarta: se cancela
		"id-vivo",      // conversación viva detrás
		"id-ajeno",     // de otro tenant
		"id-que-no-existe",
	})
	if err != nil {
		t.Fatalf("Discard: %v", err)
	}

	if !slices.Equal(res.Discarded, []string{"id-open", "id-expired"}) {
		t.Fatalf("discarded=%v, quiero [id-open id-expired]", res.Discarded)
	}
	for id, quiero := range map[string]string{
		"id-abandoned":     intakes.DiscardSkipAlreadyDiscarded,
		"id-confirmed":     intakes.DiscardSkipNotOpen,
		"id-vivo":          intakes.DiscardSkipLiveEvent,
		"id-ajeno":         intakes.DiscardSkipNotFound,
		"id-que-no-existe": intakes.DiscardSkipNotFound,
	} {
		if got := razónDe(res, id); got != quiero {
			t.Fatalf("razón de %s = %q, quiero %q", id, got, quiero)
		}
	}
	if len(res.Skipped) != 5 {
		t.Fatalf("skipped=%d, quiero 5", len(res.Skipped))
	}

	// Lo rechazado sigue EXACTAMENTE donde estaba: un lote mixto no arrastra a nadie.
	for id, quiero := range map[string]string{
		"id-open":      intakes.StatusAbandoned,
		"id-expired":   intakes.StatusAbandoned,
		"id-abandoned": intakes.StatusAbandoned,
		"id-confirmed": intakes.StatusConfirmed,
		"id-vivo":      intakes.StatusOpen,
	} {
		det, gerr := store.Get(context.Background(), discardTenant, id)
		if gerr != nil {
			t.Fatalf("Get(%s): %v", id, gerr)
		}
		if det.Status != quiero {
			t.Fatalf("%s quedó en %q, quiero %q", id, det.Status, quiero)
		}
	}
	// Y la del otro tenant no se tocó: el 404 opaco no es solo del código de error.
	otro, gerr := store.Get(context.Background(), discardOtro, "id-ajeno")
	if gerr != nil {
		t.Fatalf("Get del otro tenant: %v", gerr)
	}
	if otro.Status != intakes.StatusOpen {
		t.Fatalf("la solicitud ajena quedó en %q; nadie la tocó", otro.Status)
	}
}

// storeQueLoDiceTodo es un Store que reporta los DOS hechos a la vez: el estado en
// el que quedó la solicitud Y que hay conversación viva. Los stores de hoy no lo
// hacen —salen en cuanto el estado no es descartable, sin llegar a mirar la
// conversación—, pero el que traiga el Plan 043 puede resolver las dos cosas de una
// consulta, y el ORDEN con el que el dominio elige la razón tiene que sobrevivir a
// eso.
type storeQueLoDiceTodo struct {
	*intakes.MemoryStore
}

// Discard delega en el store real y añade el hecho que el real no llega a mirar.
func (s storeQueLoDiceTodo) Discard(ctx context.Context, tenantID, intakeID string, discardable []string) (intakes.DiscardOutcome, error) {
	out, err := s.MemoryStore.Discard(ctx, tenantID, intakeID, discardable)
	if err != nil {
		return out, err
	}
	out.LiveCart = true
	return out, nil
}

// TestDiscard_LaConversaciónVivaNoTapaAlEstado fija la PRIORIDAD de las razones: el
// estado manda sobre la conversación.
//
//   - A quien repite un lote se le dice `already_discarded`, no `live_event`:
//     decirle que hay alguien hablando le haría creer que si espera podrá
//     descartarla, cuando ya está hecho.
//   - Una `confirmed` se rechaza por `not_open`: no se descarta porque está
//     confirmada, no porque haya conversación.
func TestDiscard_LaConversaciónVivaNoTapaAlEstado(t *testing.T) {
	store := bandejaDescarte()
	svc := intakes.NewService(storeQueLoDiceTodo{MemoryStore: store})

	res, err := svc.Discard(context.Background(), discardTenant,
		[]string{"id-abandoned", "id-confirmed"})
	if err != nil {
		t.Fatalf("Discard: %v", err)
	}
	for id, quiero := range map[string]string{
		"id-abandoned": intakes.DiscardSkipAlreadyDiscarded,
		"id-confirmed": intakes.DiscardSkipNotOpen,
	} {
		if got := razónDe(res, id); got != quiero {
			t.Fatalf("razón de %s = %q, quiero %q: el estado manda sobre la conversación",
				id, got, quiero)
		}
	}
}

// TestDiscard_Idempotente: repetir el MISMO lote deja el mismo estado final y NO
// escribe una segunda revisión. Es lo que hace seguro reintentar tras un fallo a
// medio lote.
func TestDiscard_Idempotente(t *testing.T) {
	store := bandejaDescarte()
	svc := intakes.NewService(store)
	ctx := context.Background()
	lote := []string{"id-open", "id-expired"}

	if _, err := svc.Discard(ctx, discardTenant, lote); err != nil {
		t.Fatalf("primer descarte: %v", err)
	}
	revs := len(store.Revisions("id-open"))

	res, err := svc.Discard(ctx, discardTenant, lote)
	if err != nil {
		t.Fatalf("segundo descarte: %v", err)
	}
	if len(res.Discarded) != 0 {
		t.Fatalf("discarded=%v en la repetición; no quedaba nada por descartar", res.Discarded)
	}
	for _, id := range lote {
		if got := razónDe(res, id); got != intakes.DiscardSkipAlreadyDiscarded {
			t.Fatalf("razón de %s = %q, quiero %q", id, got, intakes.DiscardSkipAlreadyDiscarded)
		}
	}
	if got := len(store.Revisions("id-open")); got != revs {
		t.Fatalf("revisiones=%d tras repetir, quiero %d: descartar dos veces no se audita dos veces", got, revs)
	}
	det, err := store.Get(ctx, discardTenant, "id-open")
	if err != nil || det.Status != intakes.StatusAbandoned {
		t.Fatalf("estado final=%q (err=%v), quiero abandoned", det.Status, err)
	}
}

// TestDiscard_IdsRepetidos: el mismo id dos veces en UN lote se contesta UNA vez.
// Sin colapsar, el segundo saldría `already_discarded` por obra del primero y la
// respuesta tendría el mismo id en las dos listas — un cuerpo que se contradice.
func TestDiscard_IdsRepetidos(t *testing.T) {
	svc := intakes.NewService(bandejaDescarte())

	res, err := svc.Discard(context.Background(), discardTenant,
		[]string{"id-open", "id-open", "id-confirmed", "id-open"})
	if err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if !slices.Equal(res.Discarded, []string{"id-open"}) {
		t.Fatalf("discarded=%v, quiero [id-open] una sola vez", res.Discarded)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].IntakeID != "id-confirmed" {
		t.Fatalf("skipped=%+v, quiero solo id-confirmed", res.Skipped)
	}
}

// TestDiscard_RevisiónDelDescarte: cada descarte efectivo deja su rastro
// `discarded` firmado por el DUEÑO —no por el sistema: esto es lo contrario de una
// muerte por reloj— y su payload dice DE DÓNDE venía, que es lo único que la
// columna `status` deja de contar al pasar todo a `abandoned`.
func TestDiscard_RevisiónDelDescarte(t *testing.T) {
	store := bandejaDescarte()
	svc := intakes.NewService(store)

	if _, err := svc.Discard(context.Background(), discardTenant, []string{"id-expired"}); err != nil {
		t.Fatalf("Discard: %v", err)
	}

	revs := store.Revisions("id-expired")
	if len(revs) != 1 {
		t.Fatalf("revisiones=%d, quiero 1", len(revs))
	}
	rev := revs[0]
	if rev.Kind != intakes.RevisionKindDiscarded {
		t.Fatalf("kind=%q, quiero %q", rev.Kind, intakes.RevisionKindDiscarded)
	}
	if rev.CreatedBy != intakes.RevisionByOwner {
		t.Fatalf("created_by=%q, quiero %q (es una persona decidiendo, no el sistema)",
			rev.CreatedBy, intakes.RevisionByOwner)
	}
	if rev.RevisionNo != 1 {
		t.Fatalf("revision_no=%d, quiero 1", rev.RevisionNo)
	}

	var payload struct {
		Version    int     `json:"version"`
		FromStatus string  `json:"from_status"`
		Total      float64 `json:"total"`
	}
	if err := json.Unmarshal(rev.Payload, &payload); err != nil {
		t.Fatalf("payload ilegible (%v): %s", err, rev.Payload)
	}
	if payload.Version != intakes.RevisionPayloadVersion {
		t.Fatalf("version=%d, quiero %d", payload.Version, intakes.RevisionPayloadVersion)
	}
	if payload.FromStatus != intakes.StatusExpired {
		t.Fatalf("from_status=%q, quiero %q: es lo único que dice si el dueño limpió"+
			" una huérfana o una fila del reloj derogado", payload.FromStatus, intakes.StatusExpired)
	}
	if payload.Total != 7000 {
		t.Fatalf("total=%v, quiero 7000", payload.Total)
	}
}

// TestDiscard_ExpiredSaleSoloPorEstaPuerta es la decisión de Jhoan del 2026-08-06
// dicha en un test: `expired → abandoned` OCURRE por el descarte manual y NO existe
// como transición del ciclo de vida. Si alguien "unifica" las dos cosas metiendo
// expired en `transitions`, la segunda mitad de este test se cae.
func TestDiscard_ExpiredSaleSoloPorEstaPuerta(t *testing.T) {
	store := bandejaDescarte()
	svc := intakes.NewService(store)
	ctx := context.Background()

	res, err := svc.Discard(ctx, discardTenant, []string{"id-expired"})
	if err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if !slices.Equal(res.Discarded, []string{"id-expired"}) {
		t.Fatalf("discarded=%v; el legado vencido SÍ se descarta a mano", res.Discarded)
	}

	// La misma pareja de estados por la puerta del ciclo de vida: rechazada.
	if intakes.CanTransition(intakes.StatusExpired, intakes.StatusAbandoned) {
		t.Fatal("CanTransition(expired, abandoned) devolvió true: eso abriría la transición" +
			" en POST /intakes/{id}/status y en el <select> de la consola")
	}
	if slices.Contains(intakes.AllowedTransitions(intakes.StatusExpired), intakes.StatusAbandoned) {
		t.Fatal("abandoned aparece en AllowedTransitions(expired): no se le ofrece al operador")
	}
}

// TestDiscard_NoTocaLasLíneas: descartar cambia el ESTADO, no el pedido. Las líneas
// siguen ahí para el CSV, el summary y quien reclame (criterio (d) del plan).
func TestDiscard_NoTocaLasLíneas(t *testing.T) {
	store := intakes.NewMemoryStore()
	store.Add(discardTenant, intakes.Intake{
		ID: "id-con-líneas", ContactID: "contacto-1", SessionID: "sess-a",
		Status: intakes.StatusOpen, Total: 21000,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	},
		intakes.Item{SKU: "torta-v1", Label: "Torta", Customization: "sin sal", Qty: 1, UnitPrice: 18000},
		intakes.Item{SKU: "_shipping", Label: "Envío", Qty: 1, UnitPrice: 3000})

	if _, err := intakes.NewService(store).Discard(
		context.Background(), discardTenant, []string{"id-con-líneas"}); err != nil {
		t.Fatalf("Discard: %v", err)
	}

	det, err := store.Get(context.Background(), discardTenant, "id-con-líneas")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if det.Status != intakes.StatusAbandoned {
		t.Fatalf("status=%q, quiero abandoned", det.Status)
	}
	if len(det.Items) != 2 || det.Items[0].Customization != "sin sal" || det.Total != 21000 {
		t.Fatalf("el descarte tocó el pedido: items=%+v total=%v", det.Items, det.Total)
	}
}
