package integrations

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
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

// Etiquetas de la métrica wapp_webhook_deliveries_total que NO son estados de la
// tabla (los que sí lo son viven en store.go como StatusX). Cardinalidad FIJA.
const (
	// statusFailed: el intento falló y la entrega volverá a intentarse.
	statusFailed = "failed"
	// statusClaimLost: este worker perdió el claim antes de poder cerrar la fila
	// (Plan 042 · Ola 3.1). No es un fallo de entrega ni un éxito: es este proceso
	// llegando tarde. Se cuenta aparte porque un valor distinto de cero significa
	// que el lease se está quedando corto para la carga real, y eso se corrige
	// subiendo ClaimLease, no reintentando.
	statusClaimLost = "claim_lost"
)

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
	// ClaimLease es cuánto puede estar una entrega reclamada ('delivering') antes
	// de que se la considere huérfana y otro worker pueda rescatarla (Plan 042 ·
	// Ola 3.1, migración 0049). Es también la cadencia del rescate.
	//
	// DEFAULT DERIVADO, no un número suelto: 3× Timeout, con piso de 1 minuto. El
	// razonamiento es que una entrega legítima NO puede durar más que su propio
	// timeout HTTP (lo impone el context de deliver), así que a 3× el margen ya
	// cubre las dos escrituras a la base que la rodean y cualquier pausa razonable
	// del proceso. Ponerlo por debajo del Timeout sería el bug de vuelta: se
	// rescatarían entregas vivas. Como BatchSize, no tiene env porque D-042.4 no
	// lo pide; quien lo necesite lo fija en código.
	ClaimLease time.Duration
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
	// Se calcula DESPUÉS de Timeout a propósito: el lease se deriva de él.
	if c.ClaimLease <= 0 {
		c.ClaimLease = max(3*c.Timeout, time.Minute)
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
//
// DOS RELOJES (Plan 042 · Ola 3.1). El rápido reclama y entrega
// (PollInterval, 5s). El lento rescata entregas cuyo claim venció (ClaimLease).
// Van separados porque responden preguntas distintas: el poll pregunta "¿hay algo
// que entregar?" y el rescate "¿alguien murió a medias?". Meter el rescate dentro
// del poll costaría un UPDATE de rango cada 5 segundos para responder que no.
//
// El rescate corre además de forma PERIÓDICA, no solo al arrancar como decía el
// D-042.4 original: con una sola instancia el arranque ERA el rescate, pero con
// más de una réplica —que es lo que SKIP LOCKED existe para permitir— el trabajo
// de la que muere tiene que recogerlo una viva, sin esperar a que un humano
// reinicie algo.
func (w *Worker) Run(ctx context.Context) {
	w.recoverOrphans(ctx)

	poll := time.NewTicker(w.cfg.PollInterval)
	defer poll.Stop()
	reclaim := time.NewTicker(w.cfg.ClaimLease)
	defer reclaim.Stop()

	w.pollOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			w.log.Info("webhook worker: apagando (contexto cancelado)")
			return
		case <-reclaim.C:
			w.recoverOrphans(ctx)
		case <-poll.C:
			w.pollOnce(ctx)
		}
	}
}

// recoverOrphans devuelve a pending las entregas cuyo claim venció. Se avisa a
// nivel WARN y no INFO a propósito: en régimen normal esto no rescata nada, así
// que una línea aquí significa que un worker murió a medias o que el lease se
// quedó corto — las dos cosas que alguien querría ver en el log.
func (w *Worker) recoverOrphans(ctx context.Context) {
	n, err := w.store.RecoverOrphanDeliveries(ctx, w.cfg.ClaimLease)
	if err != nil {
		w.log.Error("webhook worker: rescatar entregas con el claim vencido", "error", err)
		return
	}
	if n > 0 {
		w.log.Warn("webhook worker: entregas con el claim vencido devueltas a pending",
			"count", n, "lease", w.cfg.ClaimLease)
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

	if err := w.store.MarkWebhookDelivered(ctx, item); err != nil {
		if !w.claimLost(err, item, StatusDelivered) {
			w.log.Error("webhook worker: marcar delivered", "error", err, "outbox_id", item.ID)
		}
		return
	}
	w.record(StatusDelivered)
	w.log.Debug("webhook worker: entrega OK", "outbox_id", item.ID, "tenant", item.TenantID, "kind", item.Kind)
}

// claimLost distingue "llegué tarde" de "falló la escritura". Si el claim ya no
// era vigente (el lease venció y otro worker reclamó la fila), NO es un fallo de
// entrega: la fila tiene otro dueño que la va a resolver, y este worker solo tiene
// que apartarse. Devuelve true si ese era el caso, para que el llamante no lo
// loguee además como error.
//
// Se avisa en WARN porque tiene una consecuencia visible para el cliente: si esto
// pasa DESPUÉS de un POST con 2xx, el puente ya recibió la entrega y la va a
// recibir otra vez (at-least-once, el receptor debe deduplicar por
// intake_id + revision_no — contrato wapp-crm-v1 §3).
func (w *Worker) claimLost(err error, item WebhookOutbox, transicion string) bool {
	if !errors.Is(err, ErrClaimLost) {
		return false
	}
	w.log.Warn("webhook worker: el claim expiró antes de cerrar la entrega; la resolverá quien la reclamó después",
		"outbox_id", item.ID, "tenant", item.TenantID, "transicion", transicion, "lease", w.cfg.ClaimLease)
	w.record(statusClaimLost)
	return true
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
		if err := w.store.MarkWebhookDead(ctx, item, reason); err != nil {
			if !w.claimLost(err, item, StatusDead) {
				w.log.Error("webhook worker: marcar dead", "error", err, "outbox_id", item.ID)
			}
			return
		}
		w.record(StatusDead)
		w.log.Error("webhook worker: entrega DEAD (reintentos agotados)",
			"outbox_id", item.ID, "tenant", item.TenantID, "attempts", attemptNumber, "reason", reason)
		return
	}

	next := time.Now().Add(backoffDuration(attemptNumber))
	if err := w.store.MarkWebhookFailed(ctx, item, next, reason); err != nil {
		if !w.claimLost(err, item, statusFailed) {
			w.log.Error("webhook worker: marcar failed", "error", err, "outbox_id", item.ID)
		}
		return
	}
	w.record(statusFailed)
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

	// Jitter ±20%, rango [0.800, 1.199].
	//
	// La fuente es crypto/rand y NO el reloj. El truco de postgres/tx.go:120
	// (time.Now().UnixNano() % N) no sirve aquí: depende de la resolución del
	// reloj, y medido el 2026-08-08 sobre go1.26.5 esa resolución es de 1 µs en
	// darwin/arm64 — UnixNano() acaba siempre en "000", así que % 400 colapsa a
	// {0, 200} y el jitter degenera en la moneda {0.800, 1.000}: sesgado hacia
	// abajo y sin cubrir el rango. En linux/amd64 daba 48 de 400 residuos. Ahí
	// el truco es tolerable porque su base son 50 ms; la de aquí llega a 1 h y
	// su razón de ser es des-correlacionar reintentos entre entregas, que es
	// justo lo que dos valores discretos no hacen.
	//
	// gosec: crypto/rand no dispara G404 (math/rand sí) y el coste es
	// irrelevante — esto corre una vez por entrega FALLIDA, no por entrega.
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return d // sin jitter antes que sin backoff
	}
	jitter := 0.8 + float64(binary.BigEndian.Uint16(b[:])%400)/1000.0
	return time.Duration(float64(d) * jitter)
}
