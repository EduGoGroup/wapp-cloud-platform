package integrations

import (
	"context"
	"encoding/json"
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

func (s *fakeStore) ClaimWebhookBatch(_ context.Context, limit int) ([]WebhookOutbox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []WebhookOutbox
	for _, r := range s.rows {
		if len(out) >= limit {
			break
		}
		if r.Status == StatusPending && !r.NextAttemptAt.After(time.Now()) {
			r.Status = StatusDelivering
			out = append(out, *r)
		}
	}
	return out, nil
}

func (s *fakeStore) MarkWebhookDelivered(_ context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[id].Status = StatusDelivered
	return nil
}

func (s *fakeStore) MarkWebhookFailed(_ context.Context, id int64, nextAttemptAt time.Time, lastErr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.rows[id]
	r.Status = StatusPending
	r.Attempts++
	r.NextAttemptAt = nextAttemptAt
	r.LastError = lastErr
	return nil
}

func (s *fakeStore) MarkWebhookDead(_ context.Context, id int64, lastErr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.rows[id]
	r.Status = StatusDead
	r.Attempts++
	r.LastError = lastErr
	return nil
}

func (s *fakeStore) RecoverOrphanDeliveries(_ context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.orphansCall++
	n := 0
	for _, r := range s.rows {
		if r.Status == StatusDelivering {
			r.Status = StatusPending
			n++
		}
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
