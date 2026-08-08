package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/integrations/sigv1"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/tenantvars"
)

func discardLogger() logger.Logger {
	return logger.New(logger.WithWriter(discardWriter{}))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// fakeStore es una implementación en memoria de Store para testear el worker
// sin Postgres real (el claim con SKIP LOCKED se cubre aparte, contra Postgres
// de verdad, en postgres_integration_test.go).
type fakeStore struct {
	mu           sync.Mutex
	rows         map[int64]*WebhookOutbox
	nextID       int64
	integrations map[string]TenantIntegration
	secrets      map[string]string
	orphansCall  int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		rows:         map[int64]*WebhookOutbox{},
		integrations: map[string]TenantIntegration{},
		secrets:      map[string]string{},
	}
}

func (s *fakeStore) EnqueueWebhook(_ context.Context, tenantID, kind string, payload json.RawMessage) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	s.rows[s.nextID] = &WebhookOutbox{
		ID: s.nextID, TenantID: tenantID, Kind: kind, Payload: payload,
		Status: StatusPending, NextAttemptAt: time.Now(), CreatedAt: time.Now(),
	}
	return s.nextID, nil
}

// ClaimWebhookBatch reproduce la semántica del store real tras la Ola 3.1: marca
// delivering Y SELLA el claim con la marca de tiempo que después hace de valla.
func (s *fakeStore) ClaimWebhookBatch(_ context.Context, limit int) ([]WebhookOutbox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ahora := time.Now()
	var out []WebhookOutbox
	for _, r := range s.rows {
		if len(out) >= limit {
			break
		}
		if r.Status == StatusPending && !r.NextAttemptAt.After(ahora) {
			r.Status = StatusDelivering
			r.ClaimedAt = ahora
			out = append(out, *r)
		}
	}
	return out, nil
}

// markClosed reproduce la valla optimista del store real: solo cierra la fila si
// sigue en delivering CON el claim que presenta el llamante. Sin esto, el fake
// sería más permisivo que Postgres y los tests del worker no dirían nada
// verdadero sobre el caso multi-réplica.
func (s *fakeStore) markClosed(claim WebhookOutbox, apply func(*WebhookOutbox)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[claim.ID]
	if !ok || r.Status != StatusDelivering || !r.ClaimedAt.Equal(claim.ClaimedAt) {
		return fmt.Errorf("fakeStore: entrega %d: %w", claim.ID, ErrClaimLost)
	}
	apply(r)
	r.ClaimedAt = time.Time{} // el claim se cierra
	return nil
}

func (s *fakeStore) MarkWebhookDelivered(_ context.Context, claim WebhookOutbox) error {
	return s.markClosed(claim, func(r *WebhookOutbox) { r.Status = StatusDelivered })
}

func (s *fakeStore) MarkWebhookFailed(_ context.Context, claim WebhookOutbox, nextAttemptAt time.Time, lastErr string) error {
	return s.markClosed(claim, func(r *WebhookOutbox) {
		r.Status = StatusPending
		r.Attempts++
		r.NextAttemptAt = nextAttemptAt
		r.LastError = lastErr
	})
}

func (s *fakeStore) MarkWebhookDead(_ context.Context, claim WebhookOutbox, lastErr string) error {
	return s.markClosed(claim, func(r *WebhookOutbox) {
		r.Status = StatusDead
		r.Attempts++
		r.LastError = lastErr
	})
}

// RecoverOrphanDeliveries rescata SOLO los claims vencidos (o sin sellar, que es
// como quedaron las filas del código anterior a la migración 0049).
func (s *fakeStore) RecoverOrphanDeliveries(_ context.Context, lease time.Duration) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.orphansCall++
	corte := time.Now().Add(-lease)
	n := 0
	for _, r := range s.rows {
		if r.Status != StatusDelivering {
			continue
		}
		if !r.ClaimedAt.IsZero() && r.ClaimedAt.After(corte) {
			continue // claim VIGENTE: alguien la está entregando ahora mismo
		}
		r.Status = StatusPending
		r.Attempts++
		r.LastError = "claim vencido"
		r.ClaimedAt = time.Time{}
		n++
	}
	return n, nil
}

func (s *fakeStore) GetTenantIntegration(_ context.Context, tenantID string) (TenantIntegration, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ti, ok := s.integrations[tenantID]
	return ti, ok, nil
}

func (s *fakeStore) GetTenantSecret(_ context.Context, tenantID string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sec, ok := s.secrets[tenantID]
	return sec, ok, nil
}

func (s *fakeStore) UpsertTenantIntegration(_ context.Context, ti TenantIntegration, secret string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.integrations[ti.TenantID] = ti
	if secret != "" {
		s.secrets[ti.TenantID] = secret
	}
	return nil
}

func (s *fakeStore) DeleteTenantIntegration(_ context.Context, tenantID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.integrations, tenantID)
	delete(s.secrets, tenantID)
	return nil
}

func (s *fakeStore) statusOf(id int64) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rows[id].Status
}

func (s *fakeStore) attemptsOf(id int64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rows[id].Attempts
}

// fakeBuyerData satisface BuyerDataReader con un mapa fijo por intake_id.
type fakeBuyerData struct {
	data map[string]intakes.BuyerData
}

func (f fakeBuyerData) GetBuyerData(_ context.Context, intakeID string) (intakes.BuyerData, bool, error) {
	bd, ok := f.data[intakeID]
	return bd, ok, nil
}

// fakeTenantVars satisface TenantVariablesReader con una lista fija por tenant.
type fakeTenantVars struct {
	vars map[string][]tenantvars.Variable
}

func (f fakeTenantVars) List(_ context.Context, tenantID string) ([]tenantvars.Variable, error) {
	return f.vars[tenantID], nil
}

func enabledIntegration(tenantID, endpointURL string) TenantIntegration {
	return TenantIntegration{
		TenantID: tenantID, CatalogAdapter: "local", EventsAdapter: "webhook",
		EndpointURL: endpointURL, HasSecret: true, Enabled: true,
	}
}

// mustEnqueue encola la plantilla de prueba y falla el test si el fakeStore
// (que nunca falla) devolviera un error — solo existe para no repetir el
// manejo de err en cada test (errcheck).
func mustEnqueue(t *testing.T, store *fakeStore, tenant, intakeID string) int64 {
	t.Helper()
	id, err := store.EnqueueWebhook(context.Background(), tenant, "intake.push", templatePayload(t, intakeID, tenant))
	if err != nil {
		t.Fatalf("encolar: %v", err)
	}
	return id
}

func templatePayload(t *testing.T, intakeID, tenant string) json.RawMessage {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"contract_version": "1", "verb": "intake.push", "tenant": tenant,
		"contact": "c-opaco", "intake_id": intakeID, "lifecycle_status": "confirmed",
		"revision_no": 1, "customer_note": "", "items": []any{}, "total": 10.0,
		"timestamp": "2026-08-07T10:00:00Z",
	})
	if err != nil {
		t.Fatalf("armar plantilla: %v", err)
	}
	return body
}

// TestWorker_Deliver_2xx_FirmaVerificable es el caso (a) de T3.3: entrega verde
// y la firma HMAC recibida por el servidor es recomputable con el mismo
// secreto — confirma que el worker firma tal como manda D-042.5.
func TestWorker_Deliver_2xx_FirmaVerificable(t *testing.T) {
	var gotSig, gotTS, gotDelivery string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get("X-Wapp-Signature")
		gotTS = r.Header.Get("X-Wapp-Timestamp")
		gotDelivery = r.Header.Get("X-Wapp-Delivery")
		buf, rerr := io.ReadAll(r.Body)
		if rerr != nil {
			t.Errorf("leer body de la request: %v", rerr)
		}
		gotBody = buf
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store := newFakeStore()
	store.integrations["t-1"] = enabledIntegration("t-1", srv.URL)
	store.secrets["t-1"] = "s3cr3t"
	id, err := store.EnqueueWebhook(context.Background(), "t-1", "intake.push", templatePayload(t, "i-1", "t-1"))
	if err != nil {
		t.Fatalf("encolar: %v", err)
	}

	w := NewWorker(store, fakeBuyerData{}, fakeTenantVars{}, discardLogger(), WorkerConfig{MaxAttempts: 5}, nil)
	w.pollOnce(context.Background())

	if status := store.statusOf(id); status != StatusDelivered {
		t.Fatalf("status=%q, quiero delivered", status)
	}
	if gotDelivery != strconv.FormatInt(id, 10) {
		t.Fatalf("X-Wapp-Delivery=%q, quiero %d", gotDelivery, id)
	}
	ts, err := strconv.ParseInt(gotTS, 10, 64)
	if err != nil {
		t.Fatalf("X-Wapp-Timestamp no es un entero: %q", gotTS)
	}
	wantSig := sigv1.SignatureHeader(sigv1.Sign("s3cr3t", ts, gotBody))
	if gotSig != wantSig {
		t.Fatalf("firma recibida=%q, recomputada=%q — no coinciden", gotSig, wantSig)
	}
}

// TestWorker_Deliver_500DosVeces_LuegoOK: caso (b) de T3.3 — dos fallos y
// entrega, con attempts=2 al final y next_attempt_at creciendo entre intentos
// (verificado indirectamente: el segundo backoff programado es mayor que
// cero y el resultado final es delivered).
func TestWorker_Deliver_500DosVeces_LuegoOK(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store := newFakeStore()
	store.integrations["t-1"] = enabledIntegration("t-1", srv.URL)
	store.secrets["t-1"] = "s3cr3t"
	id := mustEnqueue(t, store, "t-1", "i-1")

	w := NewWorker(store, fakeBuyerData{}, fakeTenantVars{}, discardLogger(), WorkerConfig{MaxAttempts: 5}, nil)
	// Tres pollOnce: el fakeStore vuelve la fila a pending con next_attempt_at
	// en backoffDuration(...) en el futuro, así que forzamos next_attempt_at al
	// pasado entre corridas (equivalente a "ya pasó el tiempo de espera").
	w.pollOnce(context.Background())
	forceRetriable(store, id)
	w.pollOnce(context.Background())
	forceRetriable(store, id)
	w.pollOnce(context.Background())

	if status := store.statusOf(id); status != StatusDelivered {
		t.Fatalf("status final=%q, quiero delivered tras el 3er intento", status)
	}
	if attempts := store.attemptsOf(id); attempts != 2 {
		t.Fatalf("attempts=%d, quiero 2 (los dos fallos; delivered no incrementa attempts)", attempts)
	}
	if calls != 3 {
		t.Fatalf("el servidor recibió %d POST, quiero 3", calls)
	}
}

// forceRetriable adelanta next_attempt_at al pasado para que la fila vuelva a
// ser reclamable en el siguiente pollOnce, sin depender de temporizadores
// reales en el test.
func forceRetriable(s *fakeStore, id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.rows[id]; ok {
		r.NextAttemptAt = time.Now().Add(-time.Second)
	}
}

// TestWorker_SiempreFalla_TopeBajado_Dead: caso (c) de T3.3 — con MaxAttempts
// bajo, agota los reintentos y termina dead, visible con su último error
// (T3.4).
func TestWorker_SiempreFalla_TopeBajado_Dead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	store := newFakeStore()
	store.integrations["t-1"] = enabledIntegration("t-1", srv.URL)
	store.secrets["t-1"] = "s3cr3t"
	id := mustEnqueue(t, store, "t-1", "i-1")

	w := NewWorker(store, fakeBuyerData{}, fakeTenantVars{}, discardLogger(), WorkerConfig{MaxAttempts: 2}, nil)
	w.pollOnce(context.Background())
	forceRetriable(store, id)
	w.pollOnce(context.Background())

	if status := store.statusOf(id); status != StatusDead {
		t.Fatalf("status=%q, quiero dead tras agotar MaxAttempts=2", status)
	}
	s := store
	s.mu.Lock()
	lastErr := s.rows[id].LastError
	s.mu.Unlock()
	if lastErr == "" {
		t.Fatal("una fila dead debe dejar constancia del último error (T3.4, visibilidad)")
	}
}

// TestWorker_MatarEntreClaimYPost_SeRecuperaAlRearrancar: caso (d) de T3.3 —
// simula un crash del proceso entre el claim y el POST (la fila queda
// 'delivering' con next_attempt_at ya vencido) y verifica que Run() la
// recupera a pending al arrancar, vía RecoverOrphanDeliveries.
func TestWorker_MatarEntreClaimYPost_SeRecuperaAlRearrancar(t *testing.T) {
	store := newFakeStore()
	id := mustEnqueue(t, store, "t-1", "i-1")
	// Simula el crash: el claim anterior la dejó 'delivering' con
	// next_attempt_at ya vencido (el DEFAULT now() del encolado nunca se tocó).
	store.mu.Lock()
	store.rows[id].Status = StatusDelivering
	store.mu.Unlock()

	w := NewWorker(store, fakeBuyerData{}, fakeTenantVars{}, discardLogger(), WorkerConfig{PollInterval: time.Hour}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	// Run() llama RecoverOrphanDeliveries SÍNCRONAMENTE antes del primer poll;
	// darle un instante para completar esa primera pasada basta (PollInterval
	// es 1h, así que el segundo poll no interferirá dentro del test).
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	if store.orphansCall == 0 {
		t.Fatal("RecoverOrphanDeliveries debe llamarse al arrancar Run()")
	}
	if status := store.statusOf(id); status == StatusDelivering {
		t.Fatalf("la fila huérfana debía recuperarse a pending, sigue en %q", status)
	}
}

// TestWorker_CompletaBuyerDataYVariables: el body que llega al puente incluye
// buyer_data descifrado y variables{} — NO la plantilla desnuda que se encoló
// (D-042.9/D-042.11: se completan en el worker, justo antes del POST).
func TestWorker_CompletaBuyerDataYVariables(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if derr := json.NewDecoder(r.Body).Decode(&gotBody); derr != nil {
			t.Errorf("decodificar body recibido: %v", derr)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store := newFakeStore()
	store.integrations["t-1"] = enabledIntegration("t-1", srv.URL)
	store.secrets["t-1"] = "s3cr3t"
	mustEnqueue(t, store, "t-1", "i-1")

	buyer := fakeBuyerData{data: map[string]intakes.BuyerData{"i-1": {"documento": "12.345.678-5"}}}
	tvars := fakeTenantVars{vars: map[string][]tenantvars.Variable{"t-1": {{Key: "moneda", Value: "Bs"}}}}

	w := NewWorker(store, buyer, tvars, discardLogger(), WorkerConfig{MaxAttempts: 5}, nil)
	w.pollOnce(context.Background())

	bd, ok := gotBody["buyer_data"].(map[string]any)
	if !ok || bd["documento"] != "12.345.678-5" {
		t.Fatalf("buyer_data en el body entregado = %v, quiero {documento: 12.345.678-5}", gotBody["buyer_data"])
	}
	vars, ok := gotBody["variables"].(map[string]any)
	if !ok || vars["moneda"] != "Bs" {
		t.Fatalf("variables en el body entregado = %v, quiero {moneda: Bs}", gotBody["variables"])
	}
	// La plantilla encolada (json.RawMessage) NO debe tener mutado buyer_data
	// ni variables: solo el body que sale por HTTP los lleva.
	var stored map[string]any
	store.mu.Lock()
	rawPayload := store.rows[1].Payload
	store.mu.Unlock()
	if uerr := json.Unmarshal(rawPayload, &stored); uerr != nil {
		t.Fatalf("decodificar la plantilla encolada: %v", uerr)
	}
	if _, has := stored["buyer_data"]; has {
		t.Fatal("la plantilla ENCOLADA no debe llevar buyer_data — eso lo añade el worker en memoria, no se re-escribe en la fila")
	}
}

// TestWorker_SinIntegracionHabilitada_Falla: si el tenant ya no tiene
// integración (borrada/deshabilitada tras encolar), la entrega falla — no hay
// destino, y el worker no debe intentar un POST sin URL.
func TestWorker_SinIntegracionHabilitada_Falla(t *testing.T) {
	store := newFakeStore()
	id := mustEnqueue(t, store, "t-sin-integracion", "i-1")

	w := NewWorker(store, fakeBuyerData{}, fakeTenantVars{}, discardLogger(), WorkerConfig{MaxAttempts: 5}, nil)
	w.pollOnce(context.Background())

	if status := store.statusOf(id); status != StatusPending {
		t.Fatalf("status=%q, quiero pending (reintentable, backoff normal)", status)
	}
	if attempts := store.attemptsOf(id); attempts != 1 {
		t.Fatalf("attempts=%d, quiero 1", attempts)
	}
}

// TestWorker_RecordCallback_NilSeguro: un worker construido sin callback de
// métricas (onRecord nil) no panica al entregar.
func TestWorker_RecordCallback_NilSeguro(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store := newFakeStore()
	store.integrations["t-1"] = enabledIntegration("t-1", srv.URL)
	store.secrets["t-1"] = "s3cr3t"
	mustEnqueue(t, store, "t-1", "i-1")

	w := NewWorker(store, fakeBuyerData{}, fakeTenantVars{}, discardLogger(), WorkerConfig{MaxAttempts: 5}, nil)
	w.pollOnce(context.Background()) // no debe panicar
}

// TestWorker_RecordCallback_LlamadoConElStatusCorrecto verifica que el
// callback de métricas (T3.4) se invoca con "delivered" en el camino feliz.
func TestWorker_RecordCallback_LlamadoConElStatusCorrecto(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store := newFakeStore()
	store.integrations["t-1"] = enabledIntegration("t-1", srv.URL)
	store.secrets["t-1"] = "s3cr3t"
	mustEnqueue(t, store, "t-1", "i-1")

	var recorded []string
	w := NewWorker(store, fakeBuyerData{}, fakeTenantVars{}, discardLogger(), WorkerConfig{MaxAttempts: 5},
		func(status string) { recorded = append(recorded, status) })
	w.pollOnce(context.Background())

	if len(recorded) != 1 || recorded[0] != StatusDelivered {
		t.Fatalf("recorded=%v, quiero [delivered]", recorded)
	}
}

// storeConClaimRobado simula que, entre el POST con 2xx y el cierre de la fila,
// el lease de este worker venció y OTRO la reclamó: el cierre llega tarde.
type storeConClaimRobado struct{ *fakeStore }

func (storeConClaimRobado) MarkWebhookDelivered(_ context.Context, _ WebhookOutbox) error {
	return fmt.Errorf("simulado: %w", ErrClaimLost)
}

// TestWorker_RecuperaSoloLosClaimsVencidos es el test de regresión del hallazgo
// #2 del review de las Olas 1-3: la recuperación de huérfanas NO puede tocar una
// entrega que alguien está entregando ahora mismo. Antes del lease, la condición
// (next_attempt_at <= now()) era cierta para TODA fila en vuelo, así que con dos
// réplicas el arranque de una revertía el trabajo de la otra.
func TestWorker_RecuperaSoloLosClaimsVencidos(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	id := mustEnqueue(t, store, "t-1", "i-1")

	batch, err := store.ClaimWebhookBatch(ctx, 10)
	if err != nil || len(batch) != 1 {
		t.Fatalf("ClaimWebhookBatch: len=%d err=%v", len(batch), err)
	}
	if batch[0].ClaimedAt.IsZero() {
		t.Fatal("el claim debe venir SELLADO con claimed_at: es la valla de las tres transiciones")
	}

	// Lease holgado ⇒ el claim está VIGENTE ⇒ no se toca.
	n, err := store.RecoverOrphanDeliveries(ctx, time.Hour)
	if err != nil {
		t.Fatalf("RecoverOrphanDeliveries: %v", err)
	}
	if n != 0 {
		t.Fatalf("se rescataron %d entregas VIVAS; el lease existe justo para protegerlas", n)
	}
	if got := store.statusOf(id); got != StatusDelivering {
		t.Fatalf("status=%q, una entrega en vuelo debe seguir en delivering", got)
	}

	// Lease cero ⇒ todo claim está vencido ⇒ se rescata Y cuenta el intento.
	n, err = store.RecoverOrphanDeliveries(ctx, 0)
	if err != nil {
		t.Fatalf("RecoverOrphanDeliveries: %v", err)
	}
	if n != 1 {
		t.Fatalf("rescatadas=%d, quiero 1 (el claim ya venció)", n)
	}
	if got := store.statusOf(id); got != StatusPending {
		t.Fatalf("status=%q, quiero pending tras el rescate", got)
	}
	if got := store.attemptsOf(id); got != 1 {
		t.Fatalf("attempts=%d, quiero 1: un claim vencido FUE un intento, y sin contarlo "+
			"una entrega que tumba a su worker giraría para siempre sin llegar a dead", got)
	}
}

// TestWorker_ClaimPerdido_NoCuentaComoEntrega: si el cierre llega tarde, el
// worker NO debe apuntarse la entrega (la resolverá quien tenga el claim) ni
// tratarlo como un fallo del puente. Se cuenta aparte, en claim_lost.
func TestWorker_ClaimPerdido_NoCuentaComoEntrega(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	base := newFakeStore()
	base.integrations["t-1"] = enabledIntegration("t-1", srv.URL)
	base.secrets["t-1"] = "s3cr3t"
	mustEnqueue(t, base, "t-1", "i-1")

	var recorded []string
	w := NewWorker(storeConClaimRobado{base}, fakeBuyerData{}, fakeTenantVars{}, discardLogger(),
		WorkerConfig{MaxAttempts: 5}, func(status string) { recorded = append(recorded, status) })
	w.pollOnce(context.Background())

	if len(recorded) != 1 || recorded[0] != statusClaimLost {
		t.Fatalf("recorded=%v, quiero [%s] — un claim perdido no es ni delivered ni failed",
			recorded, statusClaimLost)
	}
}

// TestWorker_ClaimLostSeReconoceEnvuelto protege el contrato de ErrClaimLost: el
// store real lo devuelve ENVUELTO con %w y el worker lo reconoce con errors.Is.
// Si alguien lo devolviera con %v, este test se pone rojo antes de que el worker
// empiece a tratar un claim perdido como un fallo de entrega.
func TestWorker_ClaimLostSeReconoceEnvuelto(t *testing.T) {
	store := newFakeStore()
	mustEnqueue(t, store, "t-1", "i-1")
	if _, err := store.ClaimWebhookBatch(context.Background(), 1); err != nil {
		t.Fatalf("claim: %v", err)
	}
	// Un claim con un sello que no casa: el mismo id, otro claimed_at.
	ajeno := WebhookOutbox{ID: 1, ClaimedAt: time.Now().Add(-time.Hour)}
	err := store.MarkWebhookDelivered(context.Background(), ajeno)
	if !errors.Is(err, ErrClaimLost) {
		t.Fatalf("err=%v, quiero que errors.Is(err, ErrClaimLost) sea true", err)
	}
}

// TestWorkerConfig_ClaimLeaseSiempreSuperaElTimeout es la invariante que impide
// reintroducir el bug por configuración: un lease por debajo del timeout HTTP
// rescataría entregas que siguen legítimamente en vuelo.
func TestWorkerConfig_ClaimLeaseSiempreSuperaElTimeout(t *testing.T) {
	for _, timeout := range []time.Duration{0, time.Second, 10 * time.Second, 2 * time.Minute} {
		cfg := WorkerConfig{Timeout: timeout}.withDefaults()
		if cfg.ClaimLease <= cfg.Timeout {
			t.Fatalf("Timeout=%v ⇒ ClaimLease=%v: por debajo del timeout se rescatarían entregas VIVAS",
				timeout, cfg.ClaimLease)
		}
		if cfg.ClaimLease <= 0 {
			t.Fatalf("ClaimLease=%v: time.NewTicker entra en pánico con <= 0", cfg.ClaimLease)
		}
	}
}

func TestBackoffDuration_CreceYRespetaElTope(t *testing.T) {
	for attempt := 1; attempt <= 6; attempt++ {
		d := backoffDuration(attempt)
		if d <= 0 {
			t.Fatalf("backoffDuration(%d)=%v, debe ser positivo", attempt, d)
		}
		// El jitter es ±20%, así que comparamos contra el piso 0.8x del valor
		// base esperado para confirmar la tendencia creciente sin falsos
		// negativos por el propio jitter.
		floor := time.Duration(float64(30*time.Second*(1<<uint(attempt-1))) * 0.8)
		if floor > time.Hour {
			floor = time.Duration(float64(time.Hour) * 0.8)
		}
		if d < floor {
			t.Fatalf("backoffDuration(%d)=%v, esperaba al menos %v", attempt, d, floor)
		}
	}
	// El tope: un attempt muy alto no debe superar 1h × 1.2 (tope + jitter).
	d := backoffDuration(50)
	if d > time.Hour+time.Hour/5 {
		t.Fatalf("backoffDuration(50)=%v, no debe superar el tope de 1h + jitter", d)
	}
}

// TestBackoffDuration_ElJitterDispersaDeVerdad blinda la corrección del
// 2026-08-08. La versión anterior derivaba el jitter de time.Now().UnixNano()%400
// y en darwin/arm64 —resolución de reloj de 1 µs— colapsaba a DOS valores
// ({0.800, 1.000}), pero seguía pasando TestBackoffDuration_CreceYRespetaElTope
// porque ese test solo comprueba cotas sobre UNA llamada por intento. Un jitter
// de dos valores no des-correlaciona reintentos: es justo la tormenta que el
// jitter existe para evitar.
//
// El umbral es deliberadamente flojo (50 de 400 posibles): con crypto/rand lo
// esperable son ~390 valores distintos en 2000 muestras, así que 50 deja
// margen sobrado para no ser flaky y aun así falla estrepitosamente si alguien
// vuelve a atar el jitter a la resolución del reloj.
func TestBackoffDuration_ElJitterDispersaDeVerdad(t *testing.T) {
	const muestras = 2000
	const minDistintos = 50

	vistos := make(map[time.Duration]struct{}, muestras)
	for range muestras {
		vistos[backoffDuration(1)] = struct{}{}
	}

	if len(vistos) < minDistintos {
		t.Fatalf("backoffDuration(1) devolvió solo %d valores distintos en %d llamadas "+
			"(esperaba al menos %d): el jitter no dispersa, mira si volvió a depender del reloj",
			len(vistos), muestras, minDistintos)
	}
}
