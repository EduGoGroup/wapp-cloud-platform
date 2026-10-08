// Porta internal/integrations/worker.go @ 36d5a04 (trozo de worker.go, E-13): la
// entrega de UNA fila — completar el payload, firmar y hacer el POST. Solo se
// movieron declaraciones; el contrato de todo ello está en Run (worker.go).

package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations/sigv1"
)

// maxDrainBody acota cuánto de la respuesta del puente se drena antes de cerrar
// la conexión (para reutilizarla) — un puente hostil o roto no debe poder
// forzar al worker a leer un body arbitrariamente grande.
const maxDrainBody = 64 * 1024

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

	now := w.now().Unix()
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
			w.logStoreError(ctx, "webhook worker: marcar delivered", "error", err, "outbox_id", item.ID)
		}
		return
	}
	w.record(StatusDelivered)
	w.log.Debug("webhook worker: entrega OK", "outbox_id", item.ID, "tenant", item.TenantID, "kind", item.Kind)
}

// completePayload decodifica la plantilla encolada, añade buyer_data (descifrado),
// customer_note y variables{} —los tres leídos AHORA, no al momento del push— y
// resuelve el endpoint + secreto vigentes del tenant. Si el tenant ya no tiene
// integración habilitada (borrada o deshabilitada después de encolar), devuelve un
// error explícito: el llamante lo trata como fallo terminal (sin destino,
// reintentar no ayuda).
//
// El mapa que se muta aquí sale de json.Unmarshal sobre item.Payload: es PROPIO de
// esta llamada, no el eff.Payload compartido por el fan-out de sinks. Mutarlo no
// alcanza a nadie más, y el `body` se re-serializa desde él sin tocar la fila.
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

	note, err := w.resolveCustomerNote(ctx, item.TenantID, intakeID)
	if err != nil {
		return nil, "", "", err
	}
	payload["customer_note"] = note

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

// resolveCustomerNote lee la indicación del cliente de public.intakes justo antes
// del POST, en vez de leerla de la plantilla congelada: así la nota —texto libre
// del cliente final, PII— no queda EN CLARO en webhook_outbox.payload. Sin
// intake_id o sin fila ⇒ cadena vacía (el contrato admite customer_note vacía, y
// la columna es NOT NULL con la cadena vacía por defecto: «sin nota» y «vacía»
// son el mismo caso, no dos).
//
// Consecuencia deliberada, la MISMA de variables{}: la nota que llega al puente es
// la del instante de la ENTREGA. Si el dueño la corrigió entre el push y un
// reintento, se entrega la corregida — que es la que quien prepara el pedido tiene
// que leer.
func (w *Worker) resolveCustomerNote(ctx context.Context, tenantID, intakeID string) (string, error) {
	if intakeID == "" {
		return "", nil
	}
	note, found, err := w.notes.GetCustomerNote(ctx, tenantID, intakeID)
	if err != nil {
		// El error del store NO cita la nota (ver intakes/customernote.go) y este
		// tampoco la añade: el mensaje acaba en el log del worker.
		return "", fmt.Errorf("leer la indicación del cliente de %s: %w", intakeID, err)
	}
	if !found {
		return "", nil
	}
	return note, nil
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
