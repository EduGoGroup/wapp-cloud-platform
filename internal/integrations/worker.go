package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/integrations/sigv1"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/tenantvars"
)

// Es el ÚNICO archivo del paquete que importa net/http: el store y el sink que
// encola (runtime.WebhookSink) NUNCA hacen POST — es la garantía estructural de
// INV-02 (design.md D-042.4, handoff §7.2).

// maxDrainBody acota cuánto de la respuesta del puente se drena antes de cerrar
// la conexión (para reutilizarla) — un puente hostil o roto no debe poder
// forzar al worker a leer un body arbitrariamente grande.
const maxDrainBody = 64 * 1024

// BuyerDataReader es lo mínimo que el worker necesita del dominio de solicitudes
// para completar `buyer_data` justo antes del POST (D-042.9: "el builder la
// descifra... dentro del worker, nunca en línea con el mensaje"). Lo satisface
// *intakes.PostgresBuyerData; interfaz mínima (ISP, patrón flowEventStore/
// ProjectionStore de este mismo repo) para poder testear el worker con un fake.
type BuyerDataReader interface {
	GetBuyerData(ctx context.Context, intakeID string) (intakes.BuyerData, bool, error)
}

// TenantVariablesReader es lo mínimo que el worker necesita de tenantvars.Store
// para completar `variables{}` (D-042.11: snapshot al momento de la ENTREGA, no
// del push — decisión 2026-08-07, prevalece INV-02). Interfaz mínima (ISP): el
// worker solo lee, nunca escribe (Replace es del CRUD de tenant-variables, otro
// consumidor).
type TenantVariablesReader interface {
	List(ctx context.Context, tenantID string) ([]tenantvars.Variable, error)
}

// WorkerConfig son los parámetros de D-042.4, todos con default si vienen <= 0
// (nunca un poll a 0 ni un tope de intentos nulo por accidente).
type WorkerConfig struct {
	// PollInterval es la cadencia del loop de reclamo. Default 5s (WAPP_WEBHOOK_POLL_INTERVAL).
	PollInterval time.Duration
	// MaxAttempts es el tope de intentos antes de pasar a dead. Default 10 (WAPP_WEBHOOK_MAX_ATTEMPTS).
	MaxAttempts int
	// Timeout es el timeout HTTP de CADA entrega. Default 10s (WAPP_WEBHOOK_TIMEOUT).
	Timeout time.Duration
	// BatchSize es cuántas filas reclama cada vuelta del poll. Default 20: no hay
	// env para esto en D-042.4 (no lo pide), valor conservador fijo en código.
	BatchSize int
}

func (c WorkerConfig) withDefaults() WorkerConfig {
	if c.PollInterval <= 0 {
		c.PollInterval = 5 * time.Second
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 10
	}
	if c.Timeout <= 0 {
		c.Timeout = 10 * time.Second
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 20
	}
	return c
}

// Worker es el entregador en proceso del puente CRM (D-042.4): loop de poll,
// claim con SKIP LOCKED, POST firmado, backoff exponencial con jitter, y
// recuperación de huérfanos al arrancar. Es la primera goroutine de polling de
// larga vida de este repo (no hay molde previo que copiar — confirmado antes de
// escribir esto): el patrón de apagado es el mismo `select` sobre ctx.Done() que
// ya usa serveAndWait en bootstrap.go, adaptado a un ticker.
type Worker struct {
	store    Store
	buyer    BuyerDataReader
	tenvars  TenantVariablesReader
	http     *http.Client
	log      logger.Logger
	cfg      WorkerConfig
	onRecord func(status string)
}

// NewWorker construye el worker. onRecord es el callback de métricas (T3.4,
// visibilidad de dead) — mismo patrón desacoplado que receipts.NewSink: este
// paquete NUNCA importa internal/platform/metrics, el llamante (bootstrap.go)
// pasa mtx.WebhookDelivery directo. onRecord puede ser nil (tests).
func NewWorker(store Store, buyer BuyerDataReader, tenvars TenantVariablesReader, log logger.Logger, cfg WorkerConfig, onRecord func(status string)) *Worker {
	return &Worker{
		store:    store,
		buyer:    buyer,
		tenvars:  tenvars,
		http:     &http.Client{},
		log:      log,
		cfg:      cfg.withDefaults(),
		onRecord: onRecord,
	}
}

// record llama a onRecord si está inyectado (nil-safe, patrón de todo el
// paquete metrics).
func (w *Worker) record(status string) {
	if w.onRecord != nil {
		w.onRecord(status)
	}
}

// Run bloquea hasta que ctx se cancele (D-042.4). Se arranca con `go worker.Run(ctx)`
// sobre el MISMO ctx derivado de signal.NotifyContext que cierra el resto del
// proceso (bootstrap.go) — un solo Ctrl+C también para el worker, sin un segundo
// mecanismo de shutdown.
func (w *Worker) Run(ctx context.Context) {
	if n, err := w.store.RecoverOrphanDeliveries(ctx); err != nil {
		w.log.Error("webhook worker: recuperar entregas huérfanas al arrancar", "error", err)
	} else if n > 0 {
		w.log.Info("webhook worker: entregas huérfanas recuperadas a pending", "count", n)
	}

	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()

	w.pollOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			w.log.Info("webhook worker: apagando (contexto cancelado)")
			return
		case <-ticker.C:
			w.pollOnce(ctx)
		}
	}
}

// pollOnce reclama un lote y entrega cada fila de forma SECUENCIAL: D-042.4 no
// pide concurrencia dentro de un lote (el paralelismo real, si hiciera falta,
// vendría de correr varias réplicas del proceso — para eso está SKIP LOCKED, no
// para goroutines dentro de un mismo poll).
func (w *Worker) pollOnce(ctx context.Context) {
	batch, err := w.store.ClaimWebhookBatch(ctx, w.cfg.BatchSize)
	if err != nil {
		w.log.Error("webhook worker: reclamar lote", "error", err)
		return
	}
	for _, item := range batch {
		w.deliver(ctx, item)
	}
}

// deliver completa el payload (buyer_data + variables{}, D-042.9/D-042.11), firma
// y hace el POST. Un error en cualquier paso ANTES del POST (parseo, cifrado,
// tenant sin integración) se trata como fallo de entrega igual que un POST
// fallido: mismo camino de backoff/dead, un solo lugar que decide.
func (w *Worker) deliver(ctx context.Context, item WebhookOutbox) {
	body, endpointURL, secret, err := w.completePayload(ctx, item)
	if err != nil {
		w.fail(ctx, item, err.Error())
		return
	}

	reqCtx, cancel := context.WithTimeout(ctx, w.cfg.Timeout)
	defer cancel()

	now := time.Now().Unix()
	sig := sigv1.Sign(secret, now, body)

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpointURL, bytes.NewReader(body))
	if err != nil {
		w.fail(ctx, item, fmt.Sprintf("construir request: %v", err))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Wapp-Signature", sigv1.SignatureHeader(sig))
	req.Header.Set("X-Wapp-Timestamp", fmt.Sprintf("%d", now))
	req.Header.Set("X-Wapp-Delivery", fmt.Sprintf("%d", item.ID))

	resp, err := w.http.Do(req)
	if err != nil {
		w.fail(ctx, item, fmt.Sprintf("POST: %v", err))
		return
	}
	defer func() {
		// Drenar antes de cerrar permite reutilizar la conexión (patrón de
		// internal/iam/infra/identity/client.go).
		if _, derr := io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrainBody)); derr != nil {
			_ = derr
		}
		if cerr := resp.Body.Close(); cerr != nil {
			_ = cerr
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		w.fail(ctx, item, fmt.Sprintf("respuesta %d del puente", resp.StatusCode))
		return
	}

	if err := w.store.MarkWebhookDelivered(ctx, item.ID); err != nil {
		w.log.Error("webhook worker: marcar delivered", "error", err, "outbox_id", item.ID)
		return
	}
	w.record(StatusDelivered)
	w.log.Debug("webhook worker: entrega OK", "outbox_id", item.ID, "tenant", item.TenantID, "kind", item.Kind)
}

// completePayload decodifica la plantilla encolada, añade buyer_data (descifrado)
// y variables{} (snapshot AHORA, no al momento del push), y resuelve el endpoint
// + secreto vigentes del tenant. Si el tenant ya no tiene integración habilitada
// (borrada o deshabilitada después de encolar), devuelve un error explícito: el
// llamante lo trata como fallo terminal (sin destino, reintentar no ayuda).
func (w *Worker) completePayload(ctx context.Context, item WebhookOutbox) (body []byte, endpointURL, secret string, err error) {
	var payload map[string]any
	if err := json.Unmarshal(item.Payload, &payload); err != nil {
		return nil, "", "", fmt.Errorf("plantilla del payload no es JSON válido: %w", err)
	}

	intakeID, ok := payload["intake_id"].(string)
	if !ok {
		intakeID = "" // plantilla sin la clave (no debería pasar, no es motivo de fallo duro)
	}
	buyerData, err := w.resolveBuyerData(ctx, intakeID)
	if err != nil {
		return nil, "", "", err
	}
	payload["buyer_data"] = buyerData

	variables, err := w.resolveVariables(ctx, item.TenantID)
	if err != nil {
		return nil, "", "", err
	}
	payload["variables"] = variables

	body, err = json.Marshal(payload)
	if err != nil {
		return nil, "", "", fmt.Errorf("re-serializar el payload completo: %w", err)
	}

	endpointURL, secret, err = w.resolveDestination(ctx, item.TenantID)
	if err != nil {
		return nil, "", "", err
	}
	return body, endpointURL, secret, nil
}

// resolveBuyerData descifra el checklist del comprador (D-042.9). "" o sin fila
// ⇒ {} (el contrato admite buyer_data vacío cuando el tenant no lo configuró).
func (w *Worker) resolveBuyerData(ctx context.Context, intakeID string) (map[string]string, error) {
	if intakeID == "" {
		return map[string]string{}, nil
	}
	bd, found, err := w.buyer.GetBuyerData(ctx, intakeID)
	if err != nil {
		return nil, fmt.Errorf("leer buyer_data de %s: %w", intakeID, err)
	}
	if !found {
		return map[string]string{}, nil
	}
	return bd, nil
}

// resolveVariables toma el snapshot de tenant_variables AL MOMENTO DE LA
// ENTREGA (D-042.11, decisión 2026-08-07: prevalece INV-02 sobre "al momento
// del push").
func (w *Worker) resolveVariables(ctx context.Context, tenantID string) (map[string]string, error) {
	vars, err := w.tenvars.List(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("leer tenant_variables de %s: %w", tenantID, err)
	}
	variables := make(map[string]string, len(vars))
	for _, v := range vars {
		variables[v.Key] = v.Value
	}
	return variables, nil
}

// resolveDestination lee el endpoint y descifra el secreto vigentes del tenant.
// Si la integración ya no está habilitada (borrada o apagada después de
// encolar), o le falta secreto, devuelve un error explícito: el llamante lo
// trata como fallo terminal — sin destino, reintentar no ayuda.
func (w *Worker) resolveDestination(ctx context.Context, tenantID string) (endpointURL, secret string, err error) {
	ti, found, terr := w.store.GetTenantIntegration(ctx, tenantID)
	if terr != nil {
		return "", "", fmt.Errorf("leer integración de %s: %w", tenantID, terr)
	}
	if !found || !ti.Enabled || ti.EventsAdapter != "webhook" || ti.EndpointURL == "" {
		return "", "", fmt.Errorf("tenant %s ya no tiene integración webhook habilitada", tenantID)
	}

	sec, sfound, serr := w.store.GetTenantSecret(ctx, tenantID)
	if serr != nil {
		return "", "", fmt.Errorf("leer secreto de %s: %w", tenantID, serr)
	}
	if !sfound {
		return "", "", fmt.Errorf("tenant %s no tiene secreto de firma configurado", tenantID)
	}

	return ti.EndpointURL, sec, nil
}

// fail centraliza la decisión backoff-vs-dead (D-042.4): el intento que acaba de
// fallar es item.Attempts+1 (Attempts es "intentos ya consumidos ANTES de este").
func (w *Worker) fail(ctx context.Context, item WebhookOutbox, reason string) {
	attemptNumber := item.Attempts + 1
	if attemptNumber >= w.cfg.MaxAttempts {
		if err := w.store.MarkWebhookDead(ctx, item.ID, reason); err != nil {
			w.log.Error("webhook worker: marcar dead", "error", err, "outbox_id", item.ID)
			return
		}
		w.record(StatusDead)
		w.log.Error("webhook worker: entrega DEAD (reintentos agotados)",
			"outbox_id", item.ID, "tenant", item.TenantID, "attempts", attemptNumber, "reason", reason)
		return
	}

	next := time.Now().Add(backoffDuration(attemptNumber))
	if err := w.store.MarkWebhookFailed(ctx, item.ID, next, reason); err != nil {
		w.log.Error("webhook worker: marcar failed", "error", err, "outbox_id", item.ID)
		return
	}
	w.record("failed")
	w.log.Debug("webhook worker: entrega falló, reintenta",
		"outbox_id", item.ID, "attempt", attemptNumber, "next_attempt_at", next, "reason", reason)
}

// backoffDuration es D-042.4 literal: base 30s × 2^(attempt-1), tope 1h, jitter
// ±20%. attempt es 1-based (el número del intento que acaba de fallar).
func backoffDuration(attempt int) time.Duration {
	const base = 30 * time.Second
	const cap = time.Hour

	shift := min(attempt-1, 12) // 2^12 × 30s ya excede sobradamente el tope de 1h
	d := min(base*time.Duration(1<<uint(shift)), cap)

	// Jitter ±20% sin math/rand (gosec G404, ni falta hace ser criptográfico):
	// mismo truco que postgres/tx.go:120, el reloj como fuente barata de
	// variación. Rango [0.80, 1.199...].
	jitter := 0.8 + float64(time.Now().UnixNano()%400)/1000.0
	return time.Duration(float64(d) * jitter)
}
