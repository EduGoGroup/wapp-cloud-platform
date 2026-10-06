// Porta internal/gateway/grpc/connect.go @ cbc5736 (el registro de una sesión, el canal de
// control, el cierre y el seguimiento de las sesiones vivas por Edge). Trozo de connect.go,
// partido por E-13.
//
// Aquí vive lo que el bucle Recv de Connect hace cuando una sesión APARECE
// (onSessionRegistered → registerSession), cuando un stream usa el canal de control
// (onControlChannel, ADR-0048) y cuando el stream SE VA (onStreamClosed), más el índice
// `edgeSessions` que esas tres cosas escriben y que el kill-switch, el fan-out de config y la
// elección de sesión para inferir leen.
//
// Nombres (E-11), viejo → nuevo: calientaPorRegistro → warmOnRegister (readiness.go). El
// resto ya estaba en inglés. Los textos y las claves de log son los del viejo, literales.

package grpc

import (
	"context"
	"errors"

	cltransport "github.com/EduGoGroup/wapp-cloudlink/transport"
)

// onSessionRegistered marca la sesión online en fleet, la rastrea para el
// kill-switch y empuja el lease inicial al Edge. No hace nada sin identidad mTLS.
//
// 🔴 SIGUE INLINE en la goroutine del bucle Recv, y eso es decisión explícita
// (ADR-0040 §Decisión.3): el handshake ORDENA —el Edge no puede recibir un
// LeaseUpdate de renovación antes que su lease inicial— y el carril, que es
// asíncrono, no puede garantizar ese orden frente al resto del bucle. Lo que T3.4
// le añade NO es carril: es RELOJ. Antes de esta tarea el registro corría sobre el
// ctx crudo del stream gRPC, que NO trae deadline (el Edge no lo pone), así que las
// seis o más idas y vueltas a Postgres que cuelgan de aquí podían colgar el bucle
// Recv de ese Edge indefinidamente contra una base atascada.
//
// UN reloj para todo el registro, no cuatro sueltos, porque lo que se protege es la
// unidad entera: el evento de auditoría, el MarkOnline, los TRES viajes de
// IssueInitial (TenantRevoked + Get + Upsert), el push del lease y el
// ConfigsForConnect de pushConfigsOnConnect. El presupuesto es s.workBudget
// (WAPP_GATEWAY_WORK_TIMEOUT, 5 s por defecto): el mismo que una unidad de trabajo
// del carril, y a propósito el mismo — este trabajo es del mismo tamaño y la misma
// naturaleza, solo que servido en otro sitio. NO tiene variable propia.
//
// ⚠️ El reparto es el que ya documenta jobHeartbeat: los seis viajes COMPARTEN un
// plazo, no tienen uno cada uno. El primero que tarde se come el reloj del último,
// y entonces lo que se pierde es lo de más abajo en esta función (típicamente el
// push del lease inicial o el de configs). Es una propiedad CONOCIDA y aceptada, no
// un descuido: el Edge reintenta el lease en su siguiente latido, y subir el
// presupuesto «para que quepa» sería decisión de ADR, no un ajuste.
//
// ⚠️ El padre es el ctx del STREAM y no context.WithoutCancel(streamCtx), que es lo
// contrario de lo que hace el carril (worklane.go) — y la diferencia es deliberada.
// El carril desacopla porque su trabajo importa PRECISAMENTE cuando el stream ya
// murió (persistir que un Edge se fue). Aquí es al revés: si el stream muere a mitad
// del handshake, terminar de marcar la sesión `online` y empujarle un lease a nadie
// es escribir una mentira — la misma que DEUDA-050.1 viene a evitar por el otro
// lado. Al morir el stream, el registro se rinde, y eso es correcto.
//
// Cancelar este ctx derivado NO toca al streamCtx: en Go un cancel solo se propaga
// hacia los hijos. El defer cancel() de abajo libera el timer y nada más.
//
// Cuando el plazo vence, Connect NO se cuelga: la llamada vuelve, el bucle Recv
// sigue atendiendo frames y queda el Warn de abajo diciendo qué sesión se quedó a
// medio registrar. Lo que NO hay es reintento: el Edge lo provoca reconectando.
func (s *Server) onSessionRegistered(ctx context.Context, cc connCtx) {
	if !cc.hasIdentity {
		return
	}

	regCtx, cancel := context.WithTimeout(ctx, s.workBudget)
	defer cancel()

	s.registerSession(regCtx, cc)

	// 📌 EL AVISO DE CALENTAMIENTO YA NO ESTÁ AQUÍ (T1.8-6). Estuvo en este punto
	// exacto desde T1.7-4 y se mudó al bucle Connect, DETRÁS de route, porque disparaba
	// antes de leer el primer latido de la sesión —que es el frame que suele provocar
	// este mismo registro— y por tanto antes de saber si el Edge dice que puede servir
	// inferencia. El porqué completo y qué NO se movió están en Connect y en
	// warmOnRegister. Se anota aquí porque quien busque el disparador leerá esta
	// función primero.

	// El molde es el de runJob (worklane.go): un trabajo que se pasa del presupuesto
	// DEJA RASTRO. Sin este aviso, un handshake que se rindió a medias —sesión sin
	// lease inicial, o sin su config— sería indistinguible de uno completo.
	//
	// ⚠️ Y se distinguen las DOS causas, porque el ctx derivado muere por dos motivos
	// muy distintos y confundirlos manda a investigar al sitio equivocado: si venció
	// el plazo, el sospechoso es Postgres y el presupuesto; si lo que se canceló fue
	// el stream —el Edge se fue a media conexión, que es corriente y no es una
	// avería—, culpar al presupuesto sería una pista falsa.
	switch err := regCtx.Err(); {
	case errors.Is(err, context.DeadlineExceeded):
		s.log.Warn("handshake: el registro de la sesión no terminó dentro de su presupuesto",
			"session_id", cc.sessionID, "edge_id", cc.edgeID,
			"budget", s.workBudget, "error", err)
	case err != nil:
		s.log.Info("handshake: el stream se fue antes de terminar el registro de la sesión",
			"session_id", cc.sessionID, "edge_id", cc.edgeID, "error", err)
	}
}

// onControlChannel es lo ÚNICO que el gateway hace cuando un stream usa el canal de
// control, y corre UNA VEZ POR STREAM (Plan 057 · Ola 2 · T2.2).
//
// Es la mitad que SÍ se conserva de onSessionRegistered para este id. La otra mitad
// —registrar en el Registry, indexar en edgeSessions, marcar flota, emitir lease— se
// retiró en T2.1 y el porqué está en el `case` de Connect.
//
// 🔴 QUÉ PASARÍA SI ESTA FUNCIÓN NO EXISTIERA, que es la razón de que exista. El push
// de config al conectar (ADR-0021) vivía DENTRO de registerSession. Para un Edge
// recién arrancado SIN NINGÚN TELÉFONO EMPAREJADO, el frame de auth era el único que
// provocaba registro: ese Edge recibía su catálogo de intenciones por el canal de
// control y por ningún otro sitio. Quitar el registro sin reponer esto lo dejaría
// arrancando con el catálogo vacío, sin un solo error en ningún log. Ver
// pushConfigsInBand.
//
// El reloj es el mismo molde que onSessionRegistered —ctx del STREAM acotado por
// workBudget— y por el mismo motivo: si el stream muere a mitad, empujarle config a
// nadie no sirve de nada, así que este trabajo se rinde con él. Corre INLINE en la
// goroutine del bucle Recv, igual que el registro de una sesión normal, y por eso el
// presupuesto no es opcional: lo que hay dentro toca la base (ConfigsForConnect) y la
// red (el envío), y sin techo retendría el bucle que lee los frames del Edge.
func (s *Server) onControlChannel(ctx context.Context, cc connCtx) {
	if !cc.hasIdentity {
		return
	}
	s.log.Info("canal de control en uso (no se registra como sesión)",
		"edge_id", cc.edgeID, "tenant_id", cc.tenantID)

	ctlCtx, cancel := context.WithTimeout(ctx, s.workBudget)
	defer cancel()
	s.pushConfigsInBand(ctlCtx, cc)

	// Mismo aviso, y misma distinción de causas, que en onSessionRegistered: un plazo
	// vencido acusa a Postgres y al presupuesto; un stream que se fue es corriente y
	// culpar al presupuesto mandaría a investigar al sitio equivocado.
	switch err := ctlCtx.Err(); {
	case errors.Is(err, context.DeadlineExceeded):
		s.log.Warn("canal de control: la config inicial no salió dentro de su presupuesto",
			"edge_id", cc.edgeID, "budget", s.workBudget, "error", err)
	case err != nil:
		s.log.Info("canal de control: el stream se fue antes de empujar la config inicial",
			"edge_id", cc.edgeID, "error", err)
	}
}

// registerSession es el cuerpo del registro de sesión, ya acotado por el reloj que
// le pone onSessionRegistered. Está separado solo para que ese reloj y su aviso se
// lean de un vistazo; el orden de sus pasos no cambió con T3.4.
func (s *Server) registerSession(ctx context.Context, cc connCtx) {
	s.trackSession(cc)
	// Evento del plano de máquina (identity Plan 003 · design.md Ola 3 §1.3): el
	// actor es el EdgeID del cert mTLS, no una persona.
	s.recordEdgeSession(ctx, cc)

	// 🔴 EL CANAL DE CONTROL NO ES FLOTA (MP-11). Todo lo de arriba SÍ le corresponde —se
	// registra en el Registry (sin eso el login del operador no tiene por dónde volver), se
	// trackea y se audita—, pero NO se persiste como sesión: no hay ningún teléfono detrás.
	//
	// Sin esta guarda nace una fila en `fleet_sessions` que el CLIENTE ve en su dashboard como
	// si fuera un teléfono más: con `self_pn` vacío la plantilla cae al `session_id`, así que
	// lee «__wapp_control__» en la columna del número, con su selector de perfil —marcarlo
	// pasivo movería la `version` del mapa de filters del tenant ENTERO— y seleccionable como
	// destino de envío.
	//
	// El criterio no es de gusto: lo fija la razón de ser de la flota (funcionalidad 31,
	// ADR-0023), que nació de una consola diciendo `online` con el socket muerto hacía 31
	// minutos — «estar registrado no es estar sano». Esta fila NUNCA podrá reportar un
	// SessionHealth, porque no tiene socket. Es justo lo que esa funcionalidad existe para
	// desenmascarar.
	//
	// ⚠️ Consecuencia ACEPTADA (MP-11 §5): un Edge con el operador logueado y CERO teléfonos
	// emparejados deja de aparecer en `fleet.List`, así que `RevokeTenant` no cosecha su
	// edge_id y no le empuja la revocación. La revocación SIGUE SIENDO EFECTIVA —
	// MarkTenantRevoked escribe en la BD antes del fan-out— y ese Edge no envía ni recibe
	// nada; se pierde solo el aviso inmediato.
	if s.fleet != nil && cc.sessionID != cltransport.ControlSessionID {
		if err := s.fleet.MarkOnline(ctx, cc.tenantID, cc.edgeID, cc.sessionID); err != nil {
			s.log.Error("fleet: marcar online", "error", err,
				"edge_id", cc.edgeID, "session_id", cc.sessionID)
		}
	}

	if s.leaseMgr == nil {
		// Sin lease no hay identidad de kill-switch, pero el push de config al
		// conectar (ADR-0021) es independiente: se intenta igual.
		s.pushConfigsOnConnect(ctx, cc)
		return
	}
	lu, err := s.leaseMgr.IssueInitial(ctx, cc.tenantID, cc.edgeID)
	if err != nil {
		s.log.Error("lease: emitir inicial", "error", err, "edge_id", cc.edgeID)
		return
	}
	if err := s.registry.Push(ctx, cc.sessionID, leaseToCloud(cc.sessionID, lu)); err != nil {
		s.log.Error("lease: push inicial", "error", err, "session_id", cc.sessionID)
	}

	// Push de la config vigente del tenant (ADR-0021) tras el lease inicial, en el
	// MISMO punto donde ya se reconcilia estado del servidor al conectar.
	s.pushConfigsOnConnect(ctx, cc)
}

// onStreamClosed deja de rastrear la sesión y ENCOLA su MarkOffline como ÚLTIMO job
// de esa sesión en el carril (T1.11, D-050.2).
//
// 🔴 No se ejecuta por fuera del carril, y esa es la tarea entera. Con carril, un
// SaveHealth del mismo session_id puede seguir pendiente cuando el stream cae; si el
// MarkOffline lo adelantara, el SaveHealth escribiría después y la flota mostraría
// «online» un Edge que ya se fue. Encolado, la cola FIFO de la sesión impone el orden
// —MarkOffline es lo último que se escribe— y por eso el cierre DRENA en vez de
// cancelar: un context.Cancel mataría precisamente el trabajo que hay que respetar.
//
// El contexto ya no se construye aquí: se lo da el carril, cuya base es
// context.WithoutCancel(streamCtx) acotada por workBudget. Es el mismo molde
// (desacoplado del stream ya cancelado + reloj propio) que este método montaba a
// mano con offlinePersistTimeout, y por defecto vale lo mismo: 5 s.
//
// Lo llama closeStream ANTES del seal() —ver allí por qué ese orden y no el
// inverso—, así que este submit no puede rebotar por carrera con la muerte de un
// worker. Si aun así rebotara, submitJob lo grita: no hay pérdida muda.
func (s *Server) onStreamClosed(lane *workLane, cc connCtx) {
	if !cc.hasIdentity || cc.sessionID == "" {
		return
	}
	s.untrackClosedSession(cc)

	if s.fleet == nil {
		return
	}
	// 🔴 DEUDA-050.1 — la carrera de la RECONEXIÓN RÁPIDA. Declarada el 2026-08-18 y
	// CERRADA el mismo día (Plan 050 · Ola 3) con la mitigación que hay debajo. Ya no
	// es una deuda abierta; es una carrera con red, y con el alcance de esa red
	// escrito.
	//
	// La carrera: este MarkOffline es DIFERIDO (hasta el presupuesto de drain, 5 s por
	// defecto, o sin techo si el drenaje se abandona), mientras que el MarkOnline de
	// onSessionRegistered es INLINE E INMEDIATO. Si el Edge reconecta rápido —cae el
	// stream A y se encola su jobOffline; el stream B registra la sesión y hace
	// MarkOnline YA; el jobOffline de A aterriza DESPUÉS— la fila quedaba `offline`
	// con la sesión VIVA, y nada la corregía hasta la siguiente reconexión. Antes de
	// esta ola el MarkOffline era síncrono en el defer: la ventana era de
	// milisegundos, no de segundos.
	//
	// La mitigación: la pregunta «¿sigue caída esta sesión?» se hace DENTRO del
	// closure, o sea AL EJECUTAR el job, no al encolarlo. Que es lo único que importa,
	// porque es entre esos dos instantes donde cabe la reconexión. session.Registry
	// es última-gana con comparación de identidad (registry.go, Register/release), de
	// modo que «hay entrada para este session_id» tras el release() del stream que
	// cae significa exactamente «alguien reconectó». Es el MISMO mecanismo con el que
	// la Ola 2 decide si cancelar los acuses en vuelo (closeStream, en connect.go).
	//
	// ⚠️ Lo que esta red NO cubre, y no se oculta:
	//
	//   - Registry.Online indexa por session_id GLOBAL, sin tenant. Dos tenants con el
	//     mismo session_id se confundirían aquí (hoy no ocurre: el session_id lo genera
	//     el Edge y es un UUID).
	//   - No cubre un REINICIO del proceso entre la caída y el job: el Registry vive en
	//     memoria y el job muere con él, así que no hay escritura que pisar — pero
	//     tampoco queda quien marque offline, y la fila se queda `online` hasta la
	//     siguiente reconexión. Eso es el estado previo, no una regresión de esta
	//     mitigación.
	//
	// Y por qué NO se hizo en SQL, que es lo que proponía el plan (un
	// `UPDATE … WHERE last_connected_at <= $epoch`): esa condición compara DOS RELOJES
	// DISTINTOS —MarkOnline escribe con el now() de Postgres (repository_postgres.go)
	// y el $epoch saldría del reloj de Go—, así que un desfase de reloj podía dejar una
	// sesión MUERTA marcada `online` para siempre. Peor que la deuda que arregla.
	// Descartada por Jhoan el 2026-08-18 junto con la variante de cambiar la firma de
	// fleet.Repository.
	//
	// Lo reproducen TestStreamClosedKeepsOnlineASessionThatReconnected y su gemelo
	// TestStreamClosedMarksOfflineWithoutReconnection (connect_session_offline_test.go),
	// que hay que leer en pareja: el segundo es el que impide que la mitigación degenere
	// en «dejar de marcar offline».
	s.submitJob(lane, cc, jobOffline, func(ctx context.Context) {
		if s.registry.Online(cc.sessionID) {
			s.log.Info("fleet: no se marca offline; la sesión ya reconectó por otro stream",
				"edge_id", cc.edgeID, "session_id", cc.sessionID)
			return
		}
		if err := s.fleet.MarkOffline(ctx, cc.tenantID, cc.edgeID, cc.sessionID); err != nil {
			s.log.Error("fleet: marcar offline", "error", err,
				"edge_id", cc.edgeID, "session_id", cc.sessionID)
		}
	})
}

// trackSession añade la sesión al conjunto vivo de su Edge.
func (s *Server) trackSession(cc connCtx) {
	s.trackMu.Lock()
	defer s.trackMu.Unlock()
	k := edgeKey{tenantID: cc.tenantID, edgeID: cc.edgeID}
	set := s.edgeSessions[k]
	if set == nil {
		set = make(map[string]struct{})
		s.edgeSessions[k] = set
	}
	set[cc.sessionID] = struct{}{}
}

// untrackSession quita la sesión del conjunto vivo de su Edge, y con la ÚLTIMA de
// ellas olvida también lo que ese Edge decía sobre su capacidad de inferencia.
//
// 🔴 EL OLVIDO DEL READINESS VA BAJO LA MISMA CONDICIÓN QUE EL DEL EDGE, no suelto
// (Plan 044 · Ola 1.8 · T1.8-6). La trampa, que no se ve desde aquí: onStreamClosed
// —el único llamante— se invoca UNA VEZ POR SESIÓN del stream (closeStream itera
// `releases`), y `edgeReadiness` está indexado POR EDGE. Un delete incondicional
// borraría lo aprendido de un Edge que todavía tiene otras sesiones vivas en el mismo
// stream: la siguiente vez que una de ellas latiera READY se leería como flanco y
// dispararía un calentamiento inventado, y mientras tanto el disparador de
// compatibilidad volvería a creer que ese Edge no dice nada. Colgarlo del `len(set)
// == 0` que ya decide que este Edge se quedó sin sesiones es lo que hace que las dos
// estructuras nazcan y mueran juntas.
//
// El `return` de arriba (set == nil) tampoco deja nada colgando: ese caso es un
// untrack de un Edge que YA se vació, y quien lo vació borró las dos entradas.
func (s *Server) untrackSession(cc connCtx) {
	s.trackMu.Lock()
	defer s.trackMu.Unlock()
	s.untrackLocked(cc)
}

// untrackClosedSession es el untrack del CIERRE de un stream: deja de rastrear la sesión
// solo si se quedó sin nadie. Si el Edge ya reconectó por otro stream, la sesión sigue
// viva y sigue rastreada: sin esta guarda el cierre del stream viejo la borraba del
// seguimiento y nada volvía a apuntarla, de modo que PushConfig, RevokeLease, warmEdges
// y la elección de plaza no la alcanzaban hasta su siguiente reconexión (D-F3-9; el
// viejo no la tiene). Es la misma pregunta que closeStream hace para los acuses y que
// el jobOffline hace para la flota: tras el release() del stream que cae, «hay entrada
// en el Registry» significa «alguien reconectó».
//
// La pregunta se hace BAJO trackMu y eso la cierra del todo: el stream nuevo registra
// en el Registry ANTES de rastrear, así que o ya se le ve aquí, o su trackSession
// espera a este candado y apunta después del borrado.
func (s *Server) untrackClosedSession(cc connCtx) {
	s.trackMu.Lock()
	defer s.trackMu.Unlock()
	if s.registry.Online(cc.sessionID) {
		return
	}
	s.untrackLocked(cc)
}

// untrackLocked es el cuerpo del untrack. Exige trackMu tomado.
func (s *Server) untrackLocked(cc connCtx) {
	k := edgeKey{tenantID: cc.tenantID, edgeID: cc.edgeID}
	set := s.edgeSessions[k]
	if set == nil {
		return
	}
	delete(set, cc.sessionID)
	if len(set) == 0 {
		delete(s.edgeSessions, k)
		delete(s.edgeReadiness, k)
	}
}

// sessionsForEdge devuelve una copia de las sesiones vivas del Edge dado: las de
// ESE (tenant, Edge) y ninguna más —el mismo edge_id bajo otro tenant es otro Edge—.
// Sin sesiones devuelve una lista vacía, no nil. El orden no está definido.
func (s *Server) sessionsForEdge(tenantID, edgeID string) []string {
	s.trackMu.Lock()
	defer s.trackMu.Unlock()
	set := s.edgeSessions[edgeKey{tenantID: tenantID, edgeID: edgeID}]
	out := make([]string, 0, len(set))
	for sid := range set {
		out = append(out, sid)
	}
	return out
}
