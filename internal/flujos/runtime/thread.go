// thread.go — el productor de filas `message` del hilo del evento (Plan 043 ·
// Ola 4.5 · T4.5.7b, D-043.23): el TEXTO LITERAL del turno —lo que el cliente
// escribió y lo que el negocio contestó— persiste vía AppendMessage (cifrado,
// nivel 2 del ADR-0034) SOLO si el tenant tiene la feature `llm_intake`.
//
// El Plan 044 (pipeline de captación LLM) es el LECTOR de este hilo; aquí solo se
// alimenta. TODO(Plan 044): cuando el lector exista, revisar si el gate debe
// cubrir también los salientes que no nacen de un turno entrante (resúmenes de
// rescate, coletillas) — hoy, a propósito, solo se persiste el turno del motor.
package runtime

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/engine"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
)

// featureThreadMessages es la feature que ENCIENDE el productor de `message`
// (D-043.23, tabla de productores). Es la clave del pipeline LLM del Plan 044 —
// `llm_intake`, que YA existe en el registro de entitlements: la constante Go
// (entitlements.FeatureLLMIntake) y su siembra en plan_features
// (0039_seed_plan_taxonomy.sql: planes `advisor_ai_pro` y `pro`). No se siembra
// nada nuevo aquí: un tenant sin esos planes tiene el gate APAGADO por defecto,
// que es exactamente el estado de entrega de esta ola — lo enciende contratar el
// plan, y lo explota el 044.
const featureThreadMessages = entitlements.FeatureLLMIntake

// persistTurnMessages deja en el hilo del evento el literal del turno que el motor
// acaba de procesar: la entrada del cliente (RoleClient) y las respuestas que el
// flujo produjo (RoleBusiness), en ese orden — el orden de la conversación.
//
// Tres decisiones que no son de estilo:
//
//   - SOLO dentro de evento vivo: eventID es el turnEventID capturado ANTES del
//     cierre natural (advanceLive) — los textos del turno que TERMINA el flujo
//     pertenecen al evento que estaba vivo mientras se produjeron, igual que sus
//     efectos (T4.5.1). Sin evento no hay hilo, y no se inventa uno.
//   - GATE de verdad (misma mecánica que buildSignal, ADR-0022): rt.entitlements
//     decide por tenant; sin resolver cableado, con fallo del resolver o sin la
//     feature ⇒ no se persiste nada (fail-closed: un fallo transitorio no abre
//     una capacidad de pago).
//   - Se persiste lo que el motor PRODUJO, antes del envío, aunque sendReply
//     luego falle o el rate-limit lo calle: es el MISMO tradeoff aceptado del
//     orden Save-antes-de-Send (el estado ya avanzó con esa respuesta; el hilo
//     cuenta el turno del motor, no la entrega del transporte).
//
// BEST-EFFORT integral (patrón PersistSummary): cualquier fallo se LOGUEA y el
// turno sigue — el hilo jamás tumba la conversación. No escribe `summary` ni
// `decision` (INV-11: cada voz por su puerta; el store marca el rol de cada fila).
func (rt *Runtime) persistTurnMessages(ctx context.Context, tenantID, sessionID, eventID, clientText string, outs []engine.Output) {
	if rt.events == nil || eventID == "" || rt.entitlements == nil {
		return
	}
	has, err := rt.entitlements.Has(ctx, tenantID, featureThreadMessages)
	if err != nil {
		rt.log.Warn("runtime: no se pudo resolver la feature llm_intake; el turno no se persiste en el hilo",
			"error", err, "tenant_id", tenantID, "session_id", sessionID)
		return
	}
	if !has {
		return
	}
	if clientText != "" {
		if _, err := rt.events.AppendMessage(ctx, eventID, events.RoleClient, clientText); err != nil {
			rt.log.Warn("runtime: no se pudo escribir el mensaje del cliente en el hilo; el turno sigue",
				"error", err, "session_id", sessionID)
		}
	}
	for _, out := range outs {
		if out.Text == "" {
			continue // un adjunto sin texto no tiene literal que guardar.
		}
		if _, err := rt.events.AppendMessage(ctx, eventID, events.RoleBusiness, out.Text); err != nil {
			rt.log.Warn("runtime: no se pudo escribir la respuesta del negocio en el hilo; el turno sigue",
				"error", err, "session_id", sessionID)
		}
	}
}
