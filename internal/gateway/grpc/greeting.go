package gatewaygrpc

import (
	"context"
	"errors"
)

// avisoSesionPasivaID identifica la VERSIÓN del literal que se emite. Va en los logs
// (no es PII: es el nombre del texto, no el texto) para que un operador que vea un
// saludo raro en un teléfono pueda decir CUÁL de las versiones salió. Si el texto
// cambia, cambia este ID: `_V1` → `_V2`, y con él los dos canales y sus dos tests de
// render, en el mismo commit (regla del runbook §4).
const avisoSesionPasivaID = "AVISO_SESION_PASIVA_V1"

// avisoSesionPasivaV1 es el aviso que la nube le entrega a la sesión recién
// emparejada, EN SU PROPIO NÚMERO (Plan 046 · T3.2 (b), D-046.8).
//
// 🔒 FUENTE ÚNICA: el texto canónico vive en `docs/runbooks/perfiles-de-sesion.md`
// §4, bajo el ID de arriba, y esto es su transcripción CARÁCTER A CARÁCTER. No se
// edita aquí: se edita allí y se vuelve a copiar. Lo vigila el golden de
// greeting_internal_test.go, que compara estos bytes contra ese fichero cuando está
// disponible.
//
// Tres reglas del contrato que se ven en los bytes y conviene no "arreglar":
//
//   - TEXTO PLANO, SIN MARCADO. Ni Markdown ni HTML. El otro canal es una pantalla
//     web (wapp-ctl) y las dos sintaxis no coinciden: un '*' aquí sería negrita en
//     WhatsApp y un asterisco literal en la pantalla — dos textos distintos.
//   - LAS MAYÚSCULAS HACEN DE NEGRITA. Son el único énfasis que sobrevive igual en
//     los dos canales.
//   - CERO PII. No nombra al dueño, ni un tercero, ni el propio self_pn. El
//     destinatario ya sabe qué teléfono es: lo tiene en la mano.
//
// ⚠️ La otra mitad de T3.2 —la pantalla de éxito de `wapp-ctl`— vive en OTRO REPO
// (edge/wapp-edge-agent), así que no puede importar esta constante: son dos
// transcripciones del mismo runbook, cada una con su golden. Ese es el precio de que
// el literal sea contrato documental y no una librería compartida.
const avisoSesionPasivaV1 = `Tu WhatsApp quedó vinculado a wApp, y esta sesión nació en perfil PASIVA.

Qué significa: por esta sesión SOLO SE ENVÍAN mensajes. Lo que te escriban NO SALE
DE ESTE EQUIPO: se queda aquí y no sube a la nube, así que wApp todavía no responde
solo.

Para que responda, cambia el perfil de la sesión a ACTIVA desde el panel de wApp, o
llama a POST /api/v1/sessions/{id}/profile con {"profile":"active"}.`

// sessionGreeter es la parte del repositorio de flota que el saludo necesita: saber
// si una sesión está pendiente de aviso y dejar constancia de que ya se le dio.
//
// 🔴 POR QUÉ ES UN PUERTO PROPIO Y NO DOS MÉTODOS MÁS EN fleet.Repository. Es el
// mismo criterio de interfaz-segregación que ya usan filtercfg.go:77 y
// flujos/admin/sessions.go (SessionProfileStore): quien solo necesita dos preguntas
// no depende de las trece del repositorio entero. Aquí además evita hacerle crecer el
// contrato a TODOS los dobles de prueba de fleet por una funcionalidad que solo este
// camino usa.
//
// ⚠️ EL PRECIO, ESCRITO PARA QUE NO SORPRENDA: como s.fleet se declara
// fleet.Repository, la capacidad se descubre con una ASERCIÓN DE TIPO en tiempo de
// ejecución. *fleet.PostgresRepository —lo que monta bootstrap.go:173— la cumple, así
// que en producción funciona. Un DECORADOR que envuelva el repo POR DELEGACIÓN y no
// por embebido (el molde de fleettest.SlowRepository) NO la cumpliría, y el saludo
// dejaría de emitirse EN SILENCIO. De ahí el Debug del camino de abajo: es la única
// línea que lo delataría. Si algún día ese decorador llega a producción, la salida es
// promover estos dos métodos a fleet.Repository (con su espejo en MemoryRepository y
// su delegación en SlowRepository), no parchear aquí.
type sessionGreeter interface {
	// PendingGreeting devuelve el número propio de la sesión y pending=true si hay
	// que saludarla: self_pn conocido, greeted_at IS NULL y —🔴 la tercera, que no
	// estaba y hacía que el aviso llegara a sesiones ACTIVAS, a las que miente—
	// profile = 'passive'. El número es PII.
	PendingGreeting(ctx context.Context, tenantID, edgeID, sessionID string) (selfPn string, pending bool, err error)
	// MarkGreeted marca la sesión como saludada y devuelve marked=true solo si esta
	// llamada fue la que puso la marca (centinela greeted_at IS NULL).
	MarkGreeted(ctx context.Context, tenantID, edgeID, sessionID string) (marked bool, err error)
}

// greetIfNeeded le manda a la sesión recién emparejada, A SU PROPIO NÚMERO, el aviso
// de que nació en perfil pasivo (Plan 046 · T3.2 (b), MD-046.3 ✅ salida 2).
//
// POR QUÉ LO EMITE LA NUBE Y NO EL EDGE (MD-046.3, decisión de Jhoan del 2026-08-21).
// El Edge no tiene NINGÚN camino local de envío: el único es el SendText que llega de
// la nube y pasa por el gate del lease. Construirle uno para un mensaje de cortesía
// sería abrirle al kill-switch una puerta lateral (ADR-0007). El mensaje sale por la
// misma puerta que todos.
//
// 🔴 POR QUÉ EL PRIMER INTENTO ESTÁ CONDENADO A FALLAR, Y POR QUÉ ESO ESTÁ BIEN. El
// Edge manda un Heartbeat INMEDIATO al registrar (adapters/cloudlink/adapter.go:374 →
// sendHeartbeat:965-966, que ya lleva SelfPn) y la nube registra la sesión con ese
// mismo frame (connect.go, register-on-first-frame), así que este código corre a ~4 ms
// del emparejamiento. Pero el Validator del lease del Edge NACE CERRADO y tarda
// 0,5-1,1 s en abrirse (dos arranques medidos en campo, 18-08 y 21-08): el SendText
// muere ahí con Ack{ok=false, "lease no vigente"} y SIN error de Go
// (adapter.go:725-727). La respuesta NO es esperar: es NO MARCAR. El latido siguiente
// —30 s después, con la puerta ya abierta— reintenta solo. Sin temporizadores, sin
// goroutines de espera, sin backoff propio: el latido YA es el reintento.
//
// ⚠️ DÓNDE CUELGA Y POR QUÉ AHÍ. Va en el job del latido, el ÚLTIMO de los cuatro:
// después de persistSelfPn (que es quien deja el número en la fila que este SELECT
// lee) y también después de renewLease. Los cuatro comparten UN presupuesto
// (workBudget, 5 s), no uno cada uno, así que el orden ES el reparto: puesto al final,
// el saludo solo puede gastar el reloj que los otros tres no gastaron, y nunca al
// revés. Puesto ANTES de renewLease le robaría el presupuesto al lease —y este paso
// espera un Ack, que es lo más lento que hay en el carril—, dejando sin renovar
// precisamente el lease que hace falta para que el saludo salga. Sería el defecto
// mordiéndose la cola.
//
// ⚠️ INTERACCIÓN CONOCIDA Y ACOTADA (no es un defecto nuevo, pero conviene tenerla
// escrita): esta es la primera espera de Ack que ocurre DENTRO del carril. El Ack lo
// entrega el bucle Recv inline (connect.go, case EdgeToCloud_Ack), y ese bucle puede
// estar frenado en submitJob si la cola de esta sesión llegó a su tope. Si eso pasa
// justo mientras se espera este acuse, la espera no se resuelve hasta que vence el
// presupuesto del job: entonces SendText devuelve DeadlineExceeded, no se marca, y el
// siguiente latido reintenta. Se rinde sola, no se cuelga — y por eso importa que el
// ackTimeout (8 s) sea MAYOR que el presupuesto del job (5 s): quien corta es el
// presupuesto, que es de este carril, y no el reloj del acuse.
//
// COSTE EN ESTADO ESTABLE, dicho sin adornos: una consulta indexada por la PK
// (tenant_id, edge_id, session_id) por latido y por sesión, para siempre — la misma
// fila que el job ya escribió dos veces (SetSelfPn, SaveHealth). Es el precio de no
// llevar en memoria un registro de «a quién ya saludé», que se perdería en cada
// reinicio y volvería a preguntar igual. Si algún día aparece en un perfil, la salida
// es memoizar el «ya saludada» por sesión, no quitar la marca de la BD.
//
// CERO PII EN LOS LOGS: ni el número (que solo viaja a SendText) ni el texto. Solo
// IDs opacos, el ID del literal y el command_id — el mismo criterio que
// intakes/notifier.go.
func (s *Server) greetIfNeeded(ctx context.Context, cc connCtx) {
	if s.fleet == nil || !cc.hasIdentity || cc.sessionID == "" {
		return
	}
	greeter, ok := s.fleet.(sessionGreeter)
	if !ok {
		// Ver el ⚠️ del docstring de sessionGreeter: sin esta línea, un repositorio
		// decorado dejaría de saludar sin que nadie se enterara nunca.
		s.log.Debug("saludo: el repositorio de flota no sabe marcar sesiones saludadas; no se avisa a nadie",
			"session_id", cc.sessionID, "edge_id", cc.edgeID)
		return
	}

	to, pending, err := greeter.PendingGreeting(ctx, cc.tenantID, cc.edgeID, cc.sessionID)
	if err != nil {
		s.log.Warn("saludo: no se pudo consultar si la sesión está pendiente de aviso",
			"session_id", cc.sessionID, "edge_id", cc.edgeID, "error", err)
		return
	}
	if !pending {
		// Ya saludada, sin número todavía, YA ACTIVA, o sin fila (el canal de
		// control). Lo de «ya activa» no es un caso raro: el aviso describe el perfil
		// pasivo y a una sesión activa le mentiría, así que PendingGreeting lo filtra
		// en el SQL (ver su docstring, decisión del 2026-08-21).
		return
	}

	// A partir de aquí `to` es PII en memoria: se pasa a SendText y NO se loguea.
	ack, err := s.SendText(ctx, cc.sessionID, to, avisoSesionPasivaV1)
	if err != nil {
		// Warn y no Error: en la ventana del lease esto es lo ESPERADO, y el
		// reintento está garantizado por el siguiente latido. Un Error aquí
		// entrenaría al operador a ignorar la línea.
		s.log.Warn("saludo: el envío del aviso de sesión pasiva falló; se reintentará en el siguiente latido",
			"session_id", cc.sessionID, "edge_id", cc.edgeID,
			"literal", avisoSesionPasivaID, "command_id", commandIDDe(err), "error", err)
		return
	}
	if !ack.GetOk() {
		// 🔴 ESTE ES EL CAMINO NORMAL DEL PRIMER LATIDO, no una anomalía: el Edge
		// acusó el comando y avisó de que NO lo envió (típicamente «lease no
		// vigente», adapter.go:725-727). No se marca ⇒ el siguiente latido reintenta.
		s.log.Warn("saludo: el Edge rechazó el aviso de sesión pasiva; NO se marca y se reintentará en el siguiente latido",
			"session_id", cc.sessionID, "edge_id", cc.edgeID,
			"literal", avisoSesionPasivaID,
			"command_id", ack.GetAckedCommandId(), "edge_error", ack.GetError())
		return
	}

	marked, err := greeter.MarkGreeted(ctx, cc.tenantID, cc.edgeID, cc.sessionID)
	switch {
	case err != nil:
		// El mensaje YA SALIÓ y la marca no se puso: el siguiente latido lo mandará
		// otra vez. Es Error y no Warn porque el precio lo paga el dueño en su
		// teléfono, con un mensaje repetido, y porque —a diferencia del rechazo del
		// Edge— aquí no hay nada que se arregle solo.
		s.log.Error("saludo: el aviso se entregó pero no se pudo marcar; el dueño recibirá un duplicado",
			"session_id", cc.sessionID, "edge_id", cc.edgeID,
			"literal", avisoSesionPasivaID, "command_id", ack.GetAckedCommandId(), "error", err)
	case !marked:
		// Otro latido ganó la carrera entre el SELECT y este UPDATE (dos streams
		// durante una reconexión). El centinela hizo su trabajo en la BD, pero el
		// mensaje de ESTE camino ya se mandó: el duplicado se ve aquí y en ningún
		// otro sitio.
		s.log.Warn("saludo: otro latido marcó el aviso primero; este envío fue un duplicado",
			"session_id", cc.sessionID, "edge_id", cc.edgeID,
			"literal", avisoSesionPasivaID, "command_id", ack.GetAckedCommandId())
	default:
		s.log.Info("saludo: aviso de sesión pasiva entregado al número de la propia sesión",
			"session_id", cc.sessionID, "edge_id", cc.edgeID,
			"literal", avisoSesionPasivaID, "command_id", ack.GetAckedCommandId())
	}
}

// commandIDDe extrae el command_id de un error de envío, si lo lleva; cadena vacía si
// el fallo ocurrió antes de que hubiera comando. Es el mismo duck-typing que usa
// intakes/notifier.go (commandIDOf) y que send.go documenta sobre CommandID: aquí se
// resuelve contra *SendError sin acoplarse a él, por si el camino de envío devuelve
// mañana otro error que también sepa decir su comando. Un command_id inventado sería
// peor que ninguno: quien lo buscara en los acuses del Edge no encontraría nada.
func commandIDDe(err error) string {
	var carrier interface{ CommandID() string }
	if !errors.As(err, &carrier) {
		return ""
	}
	return carrier.CommandID()
}
