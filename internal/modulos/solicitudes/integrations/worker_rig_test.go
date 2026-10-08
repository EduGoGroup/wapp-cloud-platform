package integrations_test

// El banco de pruebas de los worker_*_test.go: un worker de verdad, cableado al doble Memoria por
// un almacén espía, con reloj de prueba, y un puente de mentira (httptest) que apunta lo que recibe.
//
// El worker solo tiene una puerta, Run, y es una goroutine con tickers. Para no depender del reloj
// de pared, los tests ponen el poll a 1 ms y esperan POR EVENTOS (spyStore.waitPolls): el reloj
// que decide qué está vencido es el de prueba, así que los polls de más no hacen nada.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations/integrationshelpertest"
)

const (
	rigTenant = "t-1"
	rigIntake = "i-1"
	// rigSecret es el secreto de firma del tenant de prueba. No es una credencial.
	rigSecret = "s3cr3t-del-banco-de-pruebas" // #nosec G101 -- material de test
)

// workerRig es un worker con todo lo que le rodea a la vista.
type workerRig struct {
	clock *testClock
	mem   *integrationshelpertest.Memoria
	store *spyStore
	buyer *fakeBuyerData
	notes *fakeCustomerNotes
	vars  *fakeTenantVars
	log   *recordingLog
	cfg   integrations.WorkerConfig
	// noCallback construye el worker con onRecord nil.
	noCallback bool
	// bareStore le da al worker el doble Memoria SIN el espía: un almacén que, al revés que
	// Postgres, no mira el contexto y aceptaría un cierre con el proceso ya parándose.
	bareStore bool
	// opts son las opciones del worker; por defecto, el reloj de prueba.
	opts []integrations.WorkerOption

	mu      sync.Mutex
	records []string
}

// newWorkerRig monta el banco: poll a 1 ms, cinco intentos y el reloj de prueba compartido por el
// worker y por el doble.
func newWorkerRig() *workerRig {
	clock := newTestClock()
	mem := integrationshelpertest.NewMemoria()
	mem.SetClock(clock.Now)
	return &workerRig{
		clock: clock,
		mem:   mem,
		store: newSpyStore(mem),
		buyer: &fakeBuyerData{},
		notes: &fakeCustomerNotes{},
		vars:  &fakeTenantVars{},
		log:   newRecordingLog(),
		cfg:   integrations.WorkerConfig{PollInterval: time.Millisecond, MaxAttempts: 5},
		opts:  []integrations.WorkerOption{integrations.WithClock(clock.Now)},
	}
}

// recorded devuelve, en orden, los valores que el worker pasó al callback de métricas.
func (r *workerRig) recorded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.records)
}

// build construye el worker del banco.
func (r *workerRig) build() *integrations.Worker {
	var onRecord func(string)
	if !r.noCallback {
		onRecord = func(status string) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.records = append(r.records, status)
		}
	}
	var store integrations.Store = r.store
	if r.bareStore {
		store = r.mem
	}
	return integrations.NewWorker(store, r.buyer, r.notes, r.vars, r.log, r.cfg, onRecord, r.opts...)
}

// start arranca Run en una goroutine y devuelve la función que lo para: cancela el contexto y
// espera a que Run vuelva (o falla el test si no vuelve).
func (r *workerRig) start(t *testing.T) (stop func()) {
	t.Helper()
	worker := r.build()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.Run(ctx)
	}()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(waitLimit):
				t.Fatalf("Run no volvió en %v tras cancelar el contexto", waitLimit)
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

// runPolls arranca el worker, espera a que termine n polls enteros y lo para.
func (r *workerRig) runPolls(t *testing.T, n int) {
	t.Helper()
	stop := r.start(t)
	r.store.waitPolls(t, n)
	stop()
}

// integrate deja al tenant con su integración encendida hacia esa URL y con el secreto del banco.
func (r *workerRig) integrate(t *testing.T, tenant, url string) {
	t.Helper()
	cfg := integrations.TenantIntegration{
		TenantID: tenant, CatalogAdapter: "local", EventsAdapter: "webhook", EndpointURL: url, Enabled: true,
	}
	if err := r.mem.UpsertTenantIntegration(context.Background(), cfg, rigSecret); err != nil {
		t.Fatalf("sembrar la integración de %s: %v", tenant, err)
	}
}

// enqueue encola esa plantilla para el tenant.
func (r *workerRig) enqueue(t *testing.T, tenant string, payload json.RawMessage) int64 {
	t.Helper()
	id, err := r.mem.EnqueueWebhook(context.Background(), tenant, "intake.push", payload)
	if err != nil {
		t.Fatalf("encolar: %v", err)
	}
	return id
}

// enqueueTemplate encola la plantilla de prueba del tenant y la solicitud del banco.
func (r *workerRig) enqueueTemplate(t *testing.T) int64 {
	t.Helper()
	return r.enqueue(t, rigTenant, templatePayload(t, rigIntake, rigTenant))
}

// burnAttempts consume n intentos de la entrega por el propio puerto (reclamar y fallar, con el
// reintento para ya), para llegar a una fila con intentos previos sin tocarla por dentro.
func (r *workerRig) burnAttempts(t *testing.T, id int64, n int) {
	t.Helper()
	ctx := context.Background()
	for range n {
		batch, err := r.mem.ClaimWebhookBatch(ctx, 1)
		if err != nil || len(batch) != 1 || batch[0].ID != id {
			t.Fatalf("quemar intentos: el reclamo dio (%+v, %v), quería la entrega %d", batch, err, id)
		}
		if err := r.mem.MarkWebhookFailed(ctx, batch[0], r.clock.Now(), "intento quemado"); err != nil {
			t.Fatalf("quemar intentos: %v", err)
		}
		r.clock.Advance(time.Second)
	}
}

// row lee la fila de la entrega.
func (r *workerRig) row(t *testing.T, id int64) integrations.WebhookOutbox {
	t.Helper()
	row, found := r.mem.OutboxRow(id)
	if !found {
		t.Fatalf("la entrega %d no existe", id)
	}
	return row
}

// templatePayload reproduce la plantilla que encola el sink: SIN buyer_data, SIN variables{} y SIN
// customer_note —los tres los añade el worker en memoria justo antes del POST (INV-02)—. El
// `tenant` del cuerpo es un dato del contrato; el dueño de la entrega es el de la FILA.
func templatePayload(t *testing.T, intakeID, tenant string) json.RawMessage {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"contract_version": "1", "verb": "intake.push", "tenant": tenant,
		"contact": "c-opaco", "intake_id": intakeID, "lifecycle_status": "confirmed",
		"revision_no": 1, "items": []any{}, "total": 10.5,
		"timestamp": "2026-08-07T10:00:00Z",
	})
	if err != nil {
		t.Fatalf("armar la plantilla: %v", err)
	}
	return body
}

// bridgeRequest es un POST tal como llegó al puente.
type bridgeRequest struct {
	method string
	header http.Header
	body   []byte
}

// bridge es el puente del cliente de mentira: apunta cada POST y contesta el código que toque.
type bridge struct {
	srv *httptest.Server

	mu       sync.Mutex
	requests []bridgeRequest
	// statuses son los códigos de respuesta, uno por POST; el último vale para los siguientes.
	statuses []int
	// onPost, si no es nil, corre dentro del manejador antes de contestar.
	onPost func(r *http.Request)
	// inFlight y maxInFlight vigilan que el worker no entregue dos a la vez.
	inFlight, maxInFlight int
}

// newBridge levanta el puente; contesta esos códigos en orden (sin ninguno, siempre 200).
func newBridge(t *testing.T, statuses ...int) *bridge {
	t.Helper()
	b := &bridge{statuses: statuses}
	b.srv = httptest.NewServer(http.HandlerFunc(b.serve))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *bridge) serve(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	b.mu.Lock()
	b.inFlight++
	b.maxInFlight = max(b.maxInFlight, b.inFlight)
	b.requests = append(b.requests, bridgeRequest{method: r.Method, header: r.Header.Clone(), body: body})
	status := http.StatusOK
	if n := len(b.statuses); n > 0 {
		status = b.statuses[min(len(b.requests), n)-1]
	}
	hook := b.onPost
	b.mu.Unlock()

	if hook != nil {
		hook(r)
	}
	b.mu.Lock()
	b.inFlight--
	b.mu.Unlock()
	w.WriteHeader(status)
}

// received devuelve los POST recibidos, en orden.
func (b *bridge) received() []bridgeRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.requests)
}

// only devuelve el ÚNICO POST recibido, o falla el test.
func (b *bridge) only(t *testing.T) bridgeRequest {
	t.Helper()
	got := b.received()
	if len(got) != 1 {
		t.Fatalf("el puente recibió %d POST, quería 1", len(got))
	}
	return got[0]
}

// decoded devuelve el cuerpo del POST como objeto JSON.
func (r bridgeRequest) decoded(t *testing.T) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(r.body, &doc); err != nil {
		t.Fatalf("el cuerpo entregado no es un objeto JSON: %v\n%s", err, r.body)
	}
	return doc
}
