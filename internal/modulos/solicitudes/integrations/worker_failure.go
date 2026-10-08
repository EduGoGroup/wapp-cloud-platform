// Porta internal/integrations/worker.go @ 36d5a04 (trozo de worker.go, E-13): qué
// pasa cuando una entrega falla —backoff o dead—, el claim perdido y la parada
// (D-F6-7). Solo se movieron declaraciones; el contrato está en Run (worker.go).

package integrations

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"time"
)

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

// fail centraliza la decisión backoff-vs-dead (D-042.4): el intento que acaba de
// fallar es item.Attempts+1 (Attempts es "intentos ya consumidos ANTES de este").
//
// LA PARADA NO ES UN INTENTO (D-F6-7, decisión de la sesión F6-04). Si el contexto
// ya está cancelado, lo que "falló" no fue el puente: fue el proceso apagándose
// (un POST cortado a medias, una lectura abortada). No se cuenta el intento, no se
// toca la métrica y no se escribe nada: la fila queda en `delivering` con su claim
// y la rescata el lease, que es justo el mecanismo que existe para una entrega que
// nadie llegó a resolver. El worker viejo seguía adelante y pedía el cierre con el
// contexto muerto; Postgres lo rechazaba y eso salía como ERROR en cada parada.
func (w *Worker) fail(ctx context.Context, item WebhookOutbox, reason string) {
	if ctx.Err() != nil {
		return
	}
	attemptNumber := item.Attempts + 1
	if attemptNumber >= w.cfg.MaxAttempts {
		if err := w.store.MarkWebhookDead(ctx, item, reason); err != nil {
			if !w.claimLost(err, item, StatusDead) {
				w.logStoreError(ctx, "webhook worker: marcar dead", "error", err, "outbox_id", item.ID)
			}
			return
		}
		w.record(StatusDead)
		w.log.Error("webhook worker: entrega DEAD (reintentos agotados)",
			"outbox_id", item.ID, "tenant", item.TenantID, "attempts", attemptNumber, "reason", reason)
		return
	}

	next := w.now().Add(backoffDuration(attemptNumber))
	if err := w.store.MarkWebhookFailed(ctx, item, next, reason); err != nil {
		if !w.claimLost(err, item, statusFailed) {
			w.logStoreError(ctx, "webhook worker: marcar failed", "error", err, "outbox_id", item.ID)
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

// logStoreError registra en ERROR un fallo del almacén SALVO que el contexto ya
// esté cancelado (D-F6-7): con el proceso apagándose, que una llamada a la base
// devuelva "context canceled" no es una avería que alguien tenga que mirar, y un
// arranque-parada limpio no puede dejar líneas a ERROR (F9, P0 `sin_errores`). Lo
// usan los cinco sitios que el worker viejo registraba sin mirar: el rescate, el
// reclamo y los tres cierres.
func (w *Worker) logStoreError(ctx context.Context, msg string, args ...any) {
	if ctx.Err() != nil {
		return
	}
	w.log.Error(msg, args...)
}
