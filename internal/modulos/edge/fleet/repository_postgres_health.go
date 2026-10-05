// Porta internal/gateway/fleet/repository_postgres.go @ 809345b

package fleet

import (
	"context"
	"encoding/json"
	"fmt"
)

// SaveHealth persiste el snapshot de salud reportado en el Heartbeat (Plan 031 ·
// T3). UPDATE acotado por (tenant_id, edge_id, session_id): NO toca `state` (link
// CloudLink), solo las columnas de salud. degraded_since se calcula en SQL con un
// CASE que preserva el instante de entrada: al entrar en degradado usa el valor
// previo o now() (COALESCE) y al salir lo pone NULL — atómico contra el valor
// actual de la fila. Un UPDATE de 0 filas (sesión aún sin registrar) es válido.
// El bloque del WORKER (Plan 051 · T4.3, campos 9-15) se escribe en columnas
// NULLABLE: un puntero nil / un texto vacío / un mapa vacío se persisten como NULL
// («este Edge no lo sabe»), NUNCA como cero. Y se escriben SIEMPRE, también cuando
// son NULL: un snapshot que dejó de saber el taskset debe BORRAR el valor previo,
// porque conservar un "disjunta" viejo es publicar una salud inventada.
//
// Promesas que su test fija: la sentencia exacta y sus 19 argumentos ($12 es
// h.Degraded()); un fallo del driver vuelve envuelto como "fleet: persistir salud:
// …".
func (r *PostgresRepository) SaveHealth(ctx context.Context, tenantID, edgeID, sessionID string, h HealthSnapshot) error {
	// El desglose de motivos va al JSONB tal cual, SIN sumar nada (INV-051.3). Un
	// mapa nil o vacío se queda como interfaz nil ⇒ NULL, y no como '{}': el
	// contrato solo envía las claves con valor distinto de cero, así que un Edge
	// nuevo SIN omisiones y un Edge viejo llegan indistinguibles — y ante la duda
	// la lectura honesta es «no lo sé», no «cero omisiones».
	var omitted any
	if len(h.IntentOmittedByReason) > 0 {
		raw, merr := json.Marshal(h.IntentOmittedByReason)
		if merr != nil {
			return fmt.Errorf("fleet: serializar desglose de motivos: %w", merr)
		}
		omitted = raw
	}
	_, err := r.db.ExecContext(ctx, `
		UPDATE public.fleet_sessions
		SET whatsapp_state           = $4,
		    degraded_reason          = $5,
		    last_event_age_s         = $6,
		    dek_load_duration_ms     = $7,
		    intent_circuit           = $8,
		    outbox_depth             = $9,
		    binary_version           = $10,
		    uptime_s                 = $11,
		    last_health_at           = now(),
		    degraded_since           = CASE WHEN $12 THEN COALESCE(degraded_since, now()) ELSE NULL END,
		    worker_taskset           = $13,
		    intent_p50_ms            = $14,
		    intent_omitted_by_reason = $15,
		    stuck_heads              = $16,
		    stuck_head_polls         = $17,
		    failed_seal_dispatch     = $18,
		    failed_seal_budget       = $19,
		    updated_at               = now()
		WHERE tenant_id = $1 AND edge_id = $2 AND session_id = $3
	`, tenantID, edgeID, sessionID,
		h.WhatsappState, h.DegradedReason, h.LastEventAgeS, h.DekLoadDurationMs,
		h.IntentCircuit, h.OutboxDepth, h.BinaryVersion, h.UptimeS, h.Degraded(),
		nullText(h.WorkerTaskset), nullInt64(h.IntentP50Ms), omitted,
		nullInt64(h.StuckHeads), nullInt64(h.StuckHeadPolls),
		nullInt64(h.FailedSealDispatch), nullInt64(h.FailedSealBudget))
	if err != nil {
		return fmt.Errorf("fleet: persistir salud: %w", err)
	}
	return nil
}

// nullText mapea el texto vacío a NULL: en las columnas de salud del worker,
// vacío significa «no lo sé» y NULL es su representación en el esquema.
func nullText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullInt64 desreferencia el puntero a un valor que el driver entiende, o nil
// (NULL) si no hay dato. Se desreferencia a mano y no se pasa el *int64 crudo
// para no depender de cómo cada driver trate los punteros.
func nullInt64(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}
