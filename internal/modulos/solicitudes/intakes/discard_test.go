package intakes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"testing"
)

// Los textos, las razones, los estados descartables y los payloads esperados de este
// fichero son LITERALES calculados con el fichero viejo (internal/intakes/discard.go
// @ 64c181a): el candado de fronteras impide importarlo desde aquí, así que la
// equivalencia viejo ↔ nuevo se fija con lo que el viejo devolvió para este corpus.

const discardTenant = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"

// El cero de DiscardOutcome no afirma nada: ni descartó, ni hay evento vivo.
func TestDiscardOutcome_ZeroValueClaimsNothing(t *testing.T) {
	var outcome DiscardOutcome
	if outcome.Discarded || outcome.LiveEvent || outcome.Status != "" {
		t.Fatalf("el cero de DiscardOutcome afirma algo: %+v", outcome)
	}
}

// TestDiscardConstants_WireLiterals: la cota y las cuatro razones, que viajan tal cual a
// `skipped[].reason`.
func TestDiscardConstants_WireLiterals(t *testing.T) {
	t.Parallel()
	if MaxDiscardBatch != 200 {
		t.Errorf("MaxDiscardBatch = %d, quería 200", MaxDiscardBatch)
	}
	cases := []struct{ name, got, want string }{
		{"DiscardSkipNotFound", DiscardSkipNotFound, "not_found"},
		{"DiscardSkipAlreadyDiscarded", DiscardSkipAlreadyDiscarded, "already_discarded"},
		{"DiscardSkipNotOpen", DiscardSkipNotOpen, "not_open"},
		{"DiscardSkipLiveEvent", DiscardSkipLiveEvent, "live_event"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, quería %q", c.name, c.got, c.want)
		}
	}
}

// TestErrEmptyDiscardBatch_Text: el centinela y su texto observable.
func TestErrEmptyDiscardBatch_Text(t *testing.T) {
	t.Parallel()
	if got, want := ErrEmptyDiscardBatch.Error(), "el lote de descarte llegó sin solicitudes"; got != want {
		t.Errorf("ErrEmptyDiscardBatch = %q, quería %q", got, want)
	}
}

// TestTooLargeBatchError_Text: el texto lleva lo que llegó y el máximo. Se recoge como
// puntero.
func TestTooLargeBatchError_Text(t *testing.T) {
	t.Parallel()
	var err error = &TooLargeBatchError{Count: 201, Max: 200}
	if got, want := err.Error(), "el lote trae 201 solicitudes y el máximo es 200"; got != want {
		t.Errorf("Error() = %q, quería %q", got, want)
	}
	var tooLarge *TooLargeBatchError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("errors.As no recoge un *TooLargeBatchError: %v", err)
	}
}

// TestDiscardableStatuses_DerivedSortedAndFresh: hoy son `expired` y `open`, ordenadas; cada
// una es descartable según CanDiscard (no hay segunda fuente de verdad), y cada llamada
// devuelve un slice propio.
func TestDiscardableStatuses_DerivedSortedAndFresh(t *testing.T) {
	t.Parallel()
	got := DiscardableStatuses()
	if want := []string{"expired", "open"}; !slices.Equal(got, want) {
		t.Fatalf("DiscardableStatuses = %v, quería %v", got, want)
	}
	for _, status := range got {
		if !CanDiscard(status) {
			t.Errorf("%q está en la lista y CanDiscard dice que no", status)
		}
	}
	got[0] = "pisado"
	if again := DiscardableStatuses(); again[0] != "expired" {
		t.Errorf("mutar el resultado alcanzó a la siguiente llamada: %v", again)
	}
}

// TestDiscardedRevisionPayload_Shape: tres claves en ese orden, sin líneas; el estado de
// origen se normaliza (la legada `closed` sale `confirmed`) y lo que no es alias va tal cual.
func TestDiscardedRevisionPayload_Shape(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		status string
		total  float64
		want   string
	}{
		{name: "open orphan", status: "open", total: 0, want: `{"version":1,"from_status":"open","total":0}`},
		{name: "legacy expired", status: "expired", total: 12.5, want: `{"version":1,"from_status":"expired","total":12.5}`},
		{name: "legacy closed is normalized", status: "closed", total: 8, want: `{"version":1,"from_status":"confirmed","total":8}`},
		{name: "spaces are not trimmed", status: " Open ", total: 1, want: `{"version":1,"from_status":" Open ","total":1}`},
		{name: "case is not folded", status: "OPEN", total: 1, want: `{"version":1,"from_status":"OPEN","total":1}`},
		{name: "empty status", status: "", total: 0, want: `{"version":1,"from_status":"","total":0}`},
		{name: "unknown status goes as is", status: "desconocido", total: 0.3, want: `{"version":1,"from_status":"desconocido","total":0.3}`},
		{name: "huge total", status: "draft", total: 1e21, want: `{"version":1,"from_status":"draft","total":1e+21}`},
		{name: "negative total", status: "abandoned", total: -3, want: `{"version":1,"from_status":"abandoned","total":-3}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			raw, err := DiscardedRevisionPayload(c.status, c.total)
			if err != nil {
				t.Fatalf("DiscardedRevisionPayload = %v, quería nil", err)
			}
			if string(raw) != c.want {
				t.Errorf("payload = %s, quería %s", raw, c.want)
			}
		})
	}
	if RevisionPayloadVersion != 1 {
		t.Errorf("RevisionPayloadVersion = %d: los literales de arriba dan por hecho la versión 1", RevisionPayloadVersion)
	}
}

// TestDiscardedRevisionPayload_UnserializableTotal: un total que JSON no sabe escribir
// devuelve el error envuelto con su prefijo.
func TestDiscardedRevisionPayload_UnserializableTotal(t *testing.T) {
	t.Parallel()
	_, err := DiscardedRevisionPayload("open", math.NaN())
	if err == nil {
		t.Fatal("con un total NaN no hubo error")
	}
	const want = "intakes: serializar payload de la revisión de descarte: json: unsupported value: NaN"
	if err.Error() != want {
		t.Errorf("error = %q, quería %q", err.Error(), want)
	}
	var unsupported *json.UnsupportedValueError
	if !errors.As(err, &unsupported) {
		t.Errorf("el error no envuelve al de JSON: %v", err)
	}
}

// --- Service.Discard --------------------------------------------------------

// discardStore es un Store que solo sabe descartar: contesta por id lo que se le siembre y
// apunta lo que se le pidió. Embebe el puerto para no escribir los métodos que el descarte
// no puede tocar: si los tocara, el puntero nil lo delataría.
type discardStore struct {
	Store

	outcomes map[string]DiscardOutcome
	errs     map[string]error

	asked       []string
	tenants     []string
	discardable [][]string
}

func (s *discardStore) Discard(_ context.Context, tenantID, intakeID string, discardable []string) (DiscardOutcome, error) {
	s.asked = append(s.asked, intakeID)
	s.tenants = append(s.tenants, tenantID)
	s.discardable = append(s.discardable, discardable)
	if err, ok := s.errs[intakeID]; ok {
		return DiscardOutcome{}, err
	}
	return s.outcomes[intakeID], nil
}

// discardNotifierSpy cuenta los avisos al cliente: el descarte no puede mandar ninguno.
type discardNotifierSpy struct{ calls int }

func (s *discardNotifierSpy) NotifyStatus(context.Context, string, Intake, string) { s.calls++ }

// discardCRMSpy cuenta los empujes al CRM: el descarte no empuja.
type discardCRMSpy struct{ calls int }

func (s *discardCRMSpy) PushRevision(context.Context, string, Detail, int) { s.calls++ }

// discardIDs devuelve n ids distintos.
func discardIDs(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("id-%03d", i)
	}
	return out
}

// TestDiscard_EmptyBatch: un lote sin ids NO es «no hay nada que hacer». El resultado es el
// valor cero y el store ni se toca.
func TestDiscard_EmptyBatch(t *testing.T) {
	t.Parallel()
	for _, ids := range [][]string{nil, {}} {
		st := &discardStore{}
		res, err := NewService(st).Discard(context.Background(), discardTenant, ids)
		if !errors.Is(err, ErrEmptyDiscardBatch) {
			t.Fatalf("Discard(%v) = %v, quería ErrEmptyDiscardBatch", ids, err)
		}
		if res.Discarded != nil || res.Skipped != nil {
			t.Errorf("con error el resultado es %+v, quería el valor cero", res)
		}
		if len(st.asked) != 0 {
			t.Errorf("el store recibió %v, quería nada", st.asked)
		}
	}
}

// TestDiscard_BatchBound: MaxDiscardBatch entra, uno más no. El límite se mide sobre lo que
// LLEGA, antes de colapsar repetidos: 201 veces el mismo id también se rechaza.
func TestDiscard_BatchBound(t *testing.T) {
	t.Parallel()
	for _, ids := range [][]string{discardIDs(MaxDiscardBatch + 1), slices.Repeat([]string{"id-000"}, MaxDiscardBatch+1)} {
		st := &discardStore{}
		res, err := NewService(st).Discard(context.Background(), discardTenant, ids)
		var tooLarge *TooLargeBatchError
		if !errors.As(err, &tooLarge) {
			t.Fatalf("con 201 ids el error es %v, quería un *TooLargeBatchError", err)
		}
		if tooLarge.Count != 201 || tooLarge.Max != 200 {
			t.Errorf("el error lleva (%d, %d), quería (201, 200)", tooLarge.Count, tooLarge.Max)
		}
		if res.Discarded != nil || res.Skipped != nil || len(st.asked) != 0 {
			t.Errorf("con error hubo resultado %+v y %d llamadas al store, quería cero y ninguna", res, len(st.asked))
		}
	}

	st := &discardStore{}
	res, err := NewService(st).Discard(context.Background(), discardTenant, discardIDs(MaxDiscardBatch))
	if err != nil {
		t.Fatalf("con 200 ids Discard = %v, quería nil", err)
	}
	if len(st.asked) != MaxDiscardBatch || len(res.Skipped) != MaxDiscardBatch {
		t.Errorf("con 200 ids se pidieron %d y se contestaron %d, quería 200 y 200", len(st.asked), len(res.Skipped))
	}
}

// TestDiscard_MixedBatchReasonsAndPriority: el lote con todos los casos a la vez. Lo bueno se
// descarta, cada rechazo lleva su razón exacta, un rechazo no revierte a los demás y las dos
// listas respetan el orden de llegada. Las filas con evento vivo fijan la PRIORIDAD: el
// estado manda sobre la conversación.
func TestDiscard_MixedBatchReasonsAndPriority(t *testing.T) {
	t.Parallel()
	st := &discardStore{
		outcomes: map[string]DiscardOutcome{
			"open-orphan":            {Discarded: true, Status: StatusOpen},
			"legacy-expired":         {Discarded: true, Status: StatusExpired},
			"already":                {Status: StatusAbandoned},
			"already-with-live":      {Status: StatusAbandoned, LiveEvent: true},
			"confirmed":              {Status: StatusConfirmed},
			"confirmed-with-live":    {Status: StatusConfirmed, LiveEvent: true},
			"open-with-live":         {Status: StatusOpen, LiveEvent: true},
			"open-lost-the-race":     {Status: StatusOpen},
			"unknown-status-is-shut": {Status: "desconocido", LiveEvent: true},
		},
		errs: map[string]error{"foreign": ErrNotFound},
	}
	batch := []string{
		"confirmed", "open-orphan", "foreign", "already-with-live", "open-with-live",
		"legacy-expired", "confirmed-with-live", "open-lost-the-race", "already", "unknown-status-is-shut",
	}

	res, err := NewService(st).Discard(context.Background(), discardTenant, batch)
	if err != nil {
		t.Fatalf("Discard = %v, quería nil", err)
	}
	if want := []string{"open-orphan", "legacy-expired"}; !slices.Equal(res.Discarded, want) {
		t.Errorf("descartadas = %v, quería %v", res.Discarded, want)
	}
	wantSkipped := []DiscardSkip{
		{IntakeID: "confirmed", Reason: "not_open"},
		{IntakeID: "foreign", Reason: "not_found"},
		{IntakeID: "already-with-live", Reason: "already_discarded"},
		{IntakeID: "open-with-live", Reason: "live_event"},
		{IntakeID: "confirmed-with-live", Reason: "not_open"},
		{IntakeID: "open-lost-the-race", Reason: "not_open"},
		{IntakeID: "already", Reason: "already_discarded"},
		{IntakeID: "unknown-status-is-shut", Reason: "not_open"},
	}
	if !slices.Equal(res.Skipped, wantSkipped) {
		t.Errorf("rechazadas = %+v, quería %+v", res.Skipped, wantSkipped)
	}
}

// TestDiscard_AsksTheStoreWithTheDerivedStatuses: a cada id se le pide el descarte con el
// tenant del llamante y con DiscardableStatuses() como estados descartables.
func TestDiscard_AsksTheStoreWithTheDerivedStatuses(t *testing.T) {
	t.Parallel()
	st := &discardStore{outcomes: map[string]DiscardOutcome{"a": {Discarded: true, Status: StatusOpen}}}
	if _, err := NewService(st).Discard(context.Background(), discardTenant, []string{"a", "b"}); err != nil {
		t.Fatalf("Discard = %v, quería nil", err)
	}
	if !slices.Equal(st.asked, []string{"a", "b"}) {
		t.Fatalf("ids pedidos = %v, quería [a b]", st.asked)
	}
	for i := range st.asked {
		if st.tenants[i] != discardTenant {
			t.Errorf("el id %q se pidió para el tenant %q", st.asked[i], st.tenants[i])
		}
		if !slices.Equal(st.discardable[i], []string{"expired", "open"}) {
			t.Errorf("el id %q se pidió con los estados %v, quería [expired open]", st.asked[i], st.discardable[i])
		}
	}
}

// TestDiscard_ListsAreNeverNil: `[]` y nunca `null`, también cuando una de las dos queda
// vacía.
func TestDiscard_ListsAreNeverNil(t *testing.T) {
	t.Parallel()
	allDiscarded := &discardStore{outcomes: map[string]DiscardOutcome{"a": {Discarded: true, Status: StatusOpen}}}
	var (
		res DiscardResult
		err error
	)
	res, err = NewService(allDiscarded).Discard(context.Background(), discardTenant, []string{"a"})
	if err != nil {
		t.Fatalf("Discard = %v, quería nil", err)
	}
	if res.Skipped == nil || len(res.Skipped) != 0 {
		t.Errorf("sin rechazos, Skipped = %#v, quería una lista vacía no-nil", res.Skipped)
	}

	allSkipped := &discardStore{errs: map[string]error{"a": ErrNotFound}}
	res, err = NewService(allSkipped).Discard(context.Background(), discardTenant, []string{"a"})
	if err != nil {
		t.Fatalf("Discard = %v, quería nil", err)
	}
	if res.Discarded == nil || len(res.Discarded) != 0 {
		t.Errorf("sin descartes, Discarded = %#v, quería una lista vacía no-nil", res.Discarded)
	}
}

// TestDiscard_RepeatedIDsCollapseToTheFirst: el mismo id varias veces en UN lote se pide y se
// contesta UNA vez, en la posición de su primera aparición. Sin colapsar, la respuesta
// tendría el mismo id en las dos listas.
func TestDiscard_RepeatedIDsCollapseToTheFirst(t *testing.T) {
	t.Parallel()
	st := &discardStore{outcomes: map[string]DiscardOutcome{
		"a": {Discarded: true, Status: StatusOpen},
		"b": {Status: StatusConfirmed},
	}}
	res, err := NewService(st).Discard(context.Background(), discardTenant, []string{"b", "a", "b", "a", "a"})
	if err != nil {
		t.Fatalf("Discard = %v, quería nil", err)
	}
	if !slices.Equal(st.asked, []string{"b", "a"}) {
		t.Errorf("ids pedidos al store = %v, quería [b a]", st.asked)
	}
	if !slices.Equal(res.Discarded, []string{"a"}) || !slices.Equal(res.Skipped, []DiscardSkip{{IntakeID: "b", Reason: DiscardSkipNotOpen}}) {
		t.Errorf("resultado = %+v, quería a descartada y b rechazada, una vez cada una", res)
	}
}

// TestDiscard_InfrastructureFailureCutsTheBatch: un fallo que no es ErrNotFound corta el
// lote. El error vuelve intacto, el resultado es el valor cero y los ids que faltaban no se
// piden; lo ya descartado queda descartado (el store ya lo escribió).
func TestDiscard_InfrastructureFailureCutsTheBatch(t *testing.T) {
	t.Parallel()
	infra := errors.New("pool agotado")
	st := &discardStore{
		outcomes: map[string]DiscardOutcome{"a": {Discarded: true, Status: StatusOpen}},
		errs:     map[string]error{"b": infra},
	}
	res, err := NewService(st).Discard(context.Background(), discardTenant, []string{"a", "b", "c"})
	if !errors.Is(err, infra) {
		t.Fatalf("Discard = %v, quería el fallo del store", err)
	}
	if res.Discarded != nil || res.Skipped != nil {
		t.Errorf("con error el resultado es %+v, quería el valor cero", res)
	}
	if !slices.Equal(st.asked, []string{"a", "b"}) {
		t.Errorf("ids pedidos = %v, quería [a b]: tras el fallo no se sigue", st.asked)
	}
}

// TestDiscard_WrappedNotFoundIsStillNotFound: un store que ENVUELVE ErrNotFound (con su
// contexto) sigue siendo «no existe para este tenant»: se contesta `not_found` y el lote
// sigue, no se corta como si fuera infraestructura.
func TestDiscard_WrappedNotFoundIsStillNotFound(t *testing.T) {
	t.Parallel()
	st := &discardStore{
		outcomes: map[string]DiscardOutcome{"b": {Discarded: true, Status: StatusOpen}},
		errs:     map[string]error{"a": fmt.Errorf("leer la solicitud: %w", ErrNotFound)},
	}
	res, err := NewService(st).Discard(context.Background(), discardTenant, []string{"a", "b"})
	if err != nil {
		t.Fatalf("Discard = %v, quería nil: un ErrNotFound envuelto no corta el lote", err)
	}
	if !slices.Equal(res.Skipped, []DiscardSkip{{IntakeID: "a", Reason: DiscardSkipNotFound}}) {
		t.Errorf("Skipped = %+v, quería a con %q", res.Skipped, DiscardSkipNotFound)
	}
	if !slices.Equal(res.Discarded, []string{"b"}) {
		t.Errorf("Discarded = %v, quería [b]: el lote sigue tras el not_found", res.Discarded)
	}
}

// TestDiscard_NeitherNotifiesNorPushes: el descarte es higiene interna del dueño. Con el
// notificador y el puente del CRM cableados, no sale ni un aviso ni un empuje.
func TestDiscard_NeitherNotifiesNorPushes(t *testing.T) {
	t.Parallel()
	st := &discardStore{outcomes: map[string]DiscardOutcome{"a": {Discarded: true, Status: StatusOpen}}}
	notifier, crm := &discardNotifierSpy{}, &discardCRMSpy{}

	res, err := NewService(st, WithNotifier(notifier), WithCRMPusher(crm)).Discard(context.Background(), discardTenant, []string{"a"})
	if err != nil || !slices.Equal(res.Discarded, []string{"a"}) {
		t.Fatalf("Discard = (%+v, %v), quería a descartada", res, err)
	}
	if notifier.calls != 0 || crm.calls != 0 {
		t.Errorf("hubo %d avisos y %d empujes, quería 0 y 0", notifier.calls, crm.calls)
	}
}
