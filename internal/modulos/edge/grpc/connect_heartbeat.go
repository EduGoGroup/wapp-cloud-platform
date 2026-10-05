// Porta internal/gateway/grpc/connect.go @ cbc5736 (lo que el latido persiste y renueva).
// Trozo de connect.go, partido por E-13.
//
// Aquí vive lo que hace cada parte del job del latido, sin el reparto: quién llama a qué y
// en qué orden lo decide submitHeartbeat (connect_route.go). Todo va por puertos —fleet,
// lease, inferstats— y nada guarda estado propio.
//
// Nombres (E-11), viejo → nuevo: muestrasDe → samplesOf; inferstats.Clave/Parte/Observa son
// inferstats.Key/Report/Observe. El resto ya estaba en inglés. Los textos y las claves de
// log (incluidas `sesiones_vivas` y `tope`) son los del viejo, literales.

package grpc

import (
	"context"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/inferstats"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
)

// persistSelfPn durabiliza el número propio (self_pn) que el Edge reporta en el
// Heartbeat (Plan 020 · T2). Lo NORMALIZA a E.164 (mismo normalizador que el
// motor de flujos usa al comparar el remitente) para que el conjunto persistido
// sea canónico, y lo escribe acotado por la identidad mTLS de la sesión. Es
// best-effort: sin fleet, sin identidad, sin self_pn o si no normaliza, es un
// no-op silencioso (NUNCA loguea el número: PII); un fallo de BD se LOGUEA con
// IDs opacos y no tumba el stream. Un self_pn vacío NO sobrescribe el previo
// (la impl de fleet lo trata como no-op). Solo si la escritura salió bien se mira
// el tope de dispositivos (warnDeviceLimit), con el número ya normalizado.
//
// 🔴 EL VALOR SE NORMALIZA CRUDO, SIN LIMPIAR ANTES EL JID (hallazgos 23 y 32 de F3;
// conducta del viejo, copiada y afirmada en connect_heartbeat_test.go, NO corregida).
// contact.Normalize con phone_e164 se queda con TODOS los dígitos ASCII del texto, así
// que si el Edge mandara el JID entero, `573001112233:5@s.whatsapp.net`, el dígito del
// dispositivo se concatenaría y se persistiría `5730011122335`: otro número, con otro
// índice ciego, por el que además contaría warnDeviceLimit. Que el Edge mande hoy el
// número ya limpio no se puede comprobar desde este repo (vive en wapp-edge-agent).
func (s *Server) persistSelfPn(ctx context.Context, cc connCtx, hb *cloudlinkv1.Heartbeat) {
	if s.fleet == nil || !cc.hasIdentity || cc.sessionID == "" {
		return
	}
	raw := hb.GetSelfPn()
	if raw == "" {
		return // sesión sin emparejar aún: no se toca el valor previo.
	}
	norm, err := contact.Normalize(contact.KindPhoneE164, raw)
	if err != nil {
		// Un self_pn no normalizable (formato inesperado) se descarta: no se
		// persiste basura. Sin el número crudo en el log (PII), solo el hecho.
		s.log.Debug("heartbeat: self_pn no normalizable; se descarta",
			"session_id", cc.sessionID, "edge_id", cc.edgeID)
		return
	}
	if err := s.fleet.SetSelfPn(ctx, cc.tenantID, cc.edgeID, cc.sessionID, norm); err != nil {
		s.log.Error("fleet: persistir self_pn", "error", err,
			"edge_id", cc.edgeID, "session_id", cc.sessionID)
		return
	}
	s.warnDeviceLimit(ctx, cc, norm)
}

// warnDeviceLimit avisa (Warn, sin PII) cuando el número self_pn recién persistido
// tiene más sesiones VIVAS que el tope de dispositivos de WhatsApp (REQ-D4). Es
// solo DETECCIÓN: no bloquea (WhatsApp ya rechaza la 5.ª vinculación en origen; un
// bloqueo duro aquí sería frágil y podría cortar sesiones legítimas por un conteo
// desincronizado). NUNCA loguea el número (PII): solo el conteo, el tope y los IDs
// opacos. Best-effort: un fallo del conteo se traga en Debug (no tumba el stream).
func (s *Server) warnDeviceLimit(ctx context.Context, cc connCtx, selfPn string) {
	n, err := s.fleet.CountLiveBySelfPn(ctx, cc.tenantID, selfPn)
	if err != nil {
		s.log.Debug("fleet: contar sesiones por self_pn para aviso de tope", "error", err,
			"edge_id", cc.edgeID, "session_id", cc.sessionID)
		return
	}
	if n > fleet.DeviceLimit {
		s.log.Warn("un número supera el tope de dispositivos de WhatsApp",
			"session_id", cc.sessionID, "edge_id", cc.edgeID,
			"sesiones_vivas", n, "tope", fleet.DeviceLimit)
	}
}

// markLoggedOut marca la sesión como ZOMBIE (StateLoggedOut) en fleet: WhatsApp
// cerró el device (Plan 020 · T3). NO renueva el lease (sesión muerta) y se
// distingue del offline-por-red (que produce onStreamClosed→MarkOffline al caer el
// stream). No hace nada sin fleet, sin identidad mTLS o sin session_id.
//
// Desde T1.7 corre dentro de un job del carril, así que el ctx es el del JOB
// (base desacoplada del stream, presupuesto workBudget), no el del stream. El
// cambio va a favor: el logout se persiste aunque el Edge cuelgue justo después de
// anunciarlo.
//
// ⚠️ Ese job es un `jobLogout`, exento de coalescencia, y no un `jobHeartbeat`: con
// este último, un latido normal posterior sustituía a este trabajo en la cola y la
// sesión zombi ni se marcaba ni dejaba de renovar el lease. Ver submitHeartbeat.
func (s *Server) markLoggedOut(ctx context.Context, cc connCtx) {
	if s.fleet == nil || !cc.hasIdentity || cc.sessionID == "" {
		return
	}
	s.log.Info("heartbeat: la sesión reportó logout de WhatsApp; marcada zombie",
		"session_id", cc.sessionID, "edge_id", cc.edgeID)
	if err := s.fleet.MarkLoggedOut(ctx, cc.tenantID, cc.edgeID, cc.sessionID); err != nil {
		s.log.Error("fleet: marcar loggedout", "error", err,
			"edge_id", cc.edgeID, "session_id", cc.sessionID)
	}
}

// persistHealth durabiliza el snapshot de salud (SessionHealth) que el Edge adjunta
// al Heartbeat (Plan 031 · T3, ADR-0023). Es la ingesta que cierra el HUECO del
// incidente del 2026-07-11: el Cloud gana la verdad del socket (whatsapp_state),
// SEPARADA del estado del stream CloudLink (fleet.State). Best-effort: sin fleet, sin
// identidad, sin session_id o sin SessionHealth (Edge viejo) es un no-op silencioso
// que NO pisa los campos de salud previos; un fallo de BD se LOGUEA con IDs opacos y
// no tumba el stream. Solo metadatos de salud: CERO PII/llaves/credenciales.
func (s *Server) persistHealth(ctx context.Context, cc connCtx, hb *cloudlinkv1.Heartbeat) {
	if s.fleet == nil || !cc.hasIdentity || cc.sessionID == "" {
		return
	}
	sh := hb.GetSessionHealth()
	if sh == nil {
		return // Edge viejo (sin salud): no se tocan los campos de salud.
	}
	snap := fleet.HealthSnapshot{
		WhatsappState:     whatsappStateString(sh.GetWhatsappSocketState()),
		DegradedReason:    sh.GetDegradedReason(),
		LastEventAgeS:     sh.GetLastInboundEventAgeS(),
		DekLoadDurationMs: sh.GetDekLoadDurationMs(),
		IntentCircuit:     sh.GetIntentCircuit(),
		OutboxDepth:       sh.GetOutboxDepth(),
		BinaryVersion:     sh.GetBinaryVersion(),
		UptimeS:           sh.GetDaemonUptimeS(),

		// Bloque del WORKER (Plan 051 · T4.3, campos 9-15). 🔴 El contrato transporta «no lo sé»
		// como el CERO del tipo, así que se traduce AQUÍ, antes de tocar el dominio:
		// worker_taskset vacío e intent_p50_ms 0 significan «este Edge no lo sabe», NUNCA
		// "disjunta" ni "0 ms" (el Edge manda los tres a su cero A PROPÓSITO cuando el parte del
		// worker lleva >90 s sin refrescarse). El mapa va TAL CUAL: sin sumar nada (INV-051.3) y
		// sin asumir las ocho claves — solo llegan las que no valen cero, y puede llegar nil.
		WorkerTaskset:         sh.GetWorkerTaskset(),
		IntentP50Ms:           nonZeroOrNil(sh.GetIntentP50Ms()),
		IntentOmittedByReason: sh.GetIntentOmittedByReason(),
		// Los cuatro contadores del despachador (T3.12) NO tienen presencia en proto3: un Edge
		// viejo y un Edge nuevo sin incidencias llegan igual (0). Se persiste ese 0 tal cual.
		// 🔴 failed_seal_dispatch y failed_seal_budget NUNCA se agregan: solo el PRIMERO implica
		// mensajes duplicados.
		StuckHeads:         ptrInt64(sh.GetStuckHeads()),
		StuckHeadPolls:     ptrInt64(sh.GetStuckHeadPolls()),
		FailedSealDispatch: ptrInt64(sh.GetFailedSealDispatch()),
		FailedSealBudget:   ptrInt64(sh.GetFailedSealBudget()),
	}
	if err := s.fleet.SaveHealth(ctx, cc.tenantID, cc.edgeID, cc.sessionID, snap); err != nil {
		s.log.Error("fleet: persistir salud", "error", err,
			"edge_id", cc.edgeID, "session_id", cc.sessionID)
	}
}

// observeInference recoge el bloque de inferencia del latido para que /metrics lo
// publique (Plan 044 · Ola 1.7 · T1.7-9). Es memoria pura: no toca la base, no acota
// nada y por eso NO recibe ctx.
//
// 🔴 VA APARTE DE persistHealth Y NO DENTRO, aunque lea el mismo SessionHealth. Aquel
// se rinde sin `fleet` —no hay dónde durabilizar—, y esa guarda es correcta para él y
// equivocada para esto: un despliegue sin repositorio de flota seguiría sirviendo
// /metrics, y colgar la recogida de ahí la haría desaparecer sin un solo error. Son
// dos destinos con dos condiciones, no uno con dos pasos.
//
// ⚠️ Corre dentro de un job COALESCIBLE del carril, así que un latido intermedio puede
// descartarse (D-050.4). Da igual: lo que llega son ACUMULADOS del Edge, no deltas, y
// el siguiente latido trae el mismo total o uno mayor. Si algún día esto pasara a
// contar diferencias, la coalescencia dejaría de ser inocua — y ese es el motivo por
// el que aquí no se resta nada.
func (s *Server) observeInference(cc connCtx, hb *cloudlinkv1.Heartbeat) {
	if s.inferStats == nil || !cc.hasIdentity {
		return
	}
	sh := hb.GetSessionHealth()
	if sh == nil {
		return // Edge viejo: no reporta salud.
	}
	// 🔴 LA CLAVE ES EL EDGE, NO LA SESIÓN. Los contadores los lleva el PROCESO del
	// Edge (un cajero, un Ollama) pero viajan en el latido de CADA sesión suya: un Edge
	// con tres teléfonos manda tres latidos con LOS MISMOS totales. Indexar por sesión
	// multiplicaría por tres las inferencias del mundo con una serie creíble y falsa.
	// Es la misma lección que el calentamiento de T1.7-4, y por el mismo motivo
	// (ADR-0008: N sesiones sobre un proceso).
	s.inferStats.Observe(
		inferstats.Key{TenantID: cc.tenantID, EdgeID: cc.edgeID},
		inferstats.Report{
			ByRegime:        sh.GetInferenceByRegime(),
			ByClass:         sh.GetInferenceByClass(),
			SkippedByReason: sh.GetIntentOmittedByReason(),
			// 🔴 PRESENCIA NATIVA, NO EL CERO. Los dos son sub-mensajes: ausente
			// significa «este Edge no mide esa fase», y convertirlo en 0 lo volvería
			// «cero observaciones», que es una afirmación distinta y publicable.
			PrefillSamples:    samplesOf(sh.GetInferencePrefill()),
			GenerationSamples: samplesOf(sh.GetInferenceGeneration()),
		})
}

// samplesOf extrae el `n` de un InferenceLatency conservando su ausencia. El
// sub-mensaje lleva el cuantil y su `n` JUNTOS a propósito —aquí un cuantil sin saber
// cuántas muestras lo sostienen ya fabricó una conclusión falsa—, y lo que se publica
// es el `n`: ver descMuestrasLatencia para por qué el cuantil no sube a /metrics.
func samplesOf(l *cloudlinkv1.InferenceLatency) *int64 {
	if l == nil {
		return nil
	}
	n := l.GetSamples()
	return &n
}

// whatsappStateString mapea el enum WhatsappSocketState del contrato CloudLink al
// texto canónico que persiste fleet (el dominio no importa el proto). UNSPECIFIED
// (Edge que aún no mide) cae a "" para que la API lo omita.
func whatsappStateString(st cloudlinkv1.WhatsappSocketState) string {
	switch st {
	case cloudlinkv1.WhatsappSocketState_WHATSAPP_SOCKET_STATE_CONNECTED:
		return "connected"
	case cloudlinkv1.WhatsappSocketState_WHATSAPP_SOCKET_STATE_CONNECTING:
		return "connecting"
	case cloudlinkv1.WhatsappSocketState_WHATSAPP_SOCKET_STATE_DEGRADED:
		return "degraded"
	case cloudlinkv1.WhatsappSocketState_WHATSAPP_SOCKET_STATE_DEAD:
		return "dead"
	default:
		return ""
	}
}

// nonZeroOrNil traduce a puntero un entero del contrato cuyo CERO significa «no
// medible» y no «cero medido» (SessionHealth.intent_p50_ms, campo 10): 0 ⇒ nil,
// para que la nube no publique "0 ms" sobre un dato que el Edge no tiene.
func nonZeroOrNil(v int64) *int64 {
	if v == 0 {
		return nil
	}
	return &v
}

// ptrInt64 devuelve un puntero al valor tal cual. Se usa con los contadores
// acumulados del despachador (campos 12-15), donde el contrato define 0 como «no
// ocurrió (o el Edge no lo mide)» y proto3 no ofrece presencia para separarlos.
func ptrInt64(v int64) *int64 { return &v }

// renewLease renueva el lease del Edge a partir del counter del Heartbeat y
// empuja el LeaseUpdate. No hace nada sin lease o sin identidad.
//
// Desde T1.7 es la TERCERA parte de un jobHeartbeat y hereda el ctx del job, que ya
// viene gastado por las dos escrituras anteriores. Ese ctx manda sobre el reloj
// interno del Registry (defaultSendTimeout, 10 s): si el presupuesto se agota antes,
// el Push vuelve al instante con el error de cancelación y el Edge NO recibe el
// LeaseUpdate. El fallo se loguea aquí y el carril lo grita en su Warn — un lease
// que no se pudo empujar no debe darse por renovado ante el Edge, que lo reintenta
// en el siguiente latido.
func (s *Server) renewLease(ctx context.Context, cc connCtx, heartbeatCounter int64) {
	if s.leaseMgr == nil || !cc.hasIdentity || cc.sessionID == "" {
		return
	}
	lu, err := s.leaseMgr.Renew(ctx, cc.tenantID, cc.edgeID, heartbeatCounter)
	if err != nil {
		s.log.Error("lease: renovar", "error", err, "edge_id", cc.edgeID)
		return
	}
	if err := s.registry.Push(ctx, cc.sessionID, leaseToCloud(cc.sessionID, lu)); err != nil {
		s.log.Debug("lease: push renovación", "error", err, "session_id", cc.sessionID)
	}
}
