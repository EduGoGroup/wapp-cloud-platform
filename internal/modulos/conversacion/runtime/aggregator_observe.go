// Porta internal/flujos/runtime/aggregator.go @ e0159171

package runtime

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
)

// Trozo de aggregator.go (E-13): Observe, lo único del agregador que corre en línea con el
// mensaje del cliente, y sus tres auxiliares (las guardas baratas, la memoria del último
// mensaje y la petición de clasificación).

// featureIntakeAggregation es el gate del agregador: la MISMA feature `llm_intake`
// que abre el productor de filas `message` del hilo (thread.go). No es casualidad
// ni ahorro: el agregador es el LECTOR de ese hilo, así que encender uno sin el
// otro deja media función construida. Un tenant sin la feature produce CERO jobs.
//
// 🔴 Y ES EL ÚNICO GATE DE ESTE CAMINO: aquí NO se consulta `api_llm` (ADR-0044,
// D-044.28). La vía —local o API— es una configuración DENTRO del nivel, y
// decidirla no es asunto de la ventana: un tenant de vía local abre ventana,
// compone su `source_text` cifrado y deja su job en `pending` exactamente igual.
// Quien venga a añadir aquí una segunda pregunta al resolver, que lea antes el
// docstring de entitlements.FeatureAPILLM: lo que busca no se decide en este
// fichero.
const featureIntakeAggregation = entitlements.FeatureLLMIntake

// Observe mete UN entrante en su ventana. Es lo ÚNICO de este fichero que corre en
// línea con el mensaje del cliente.
//
// # AG-2 · No devuelve error (INV-10)
//
// La firma no tiene valor de error: un fallo de aquí NUNCA tumba el turno del
// cliente. Cualquier fallo se LOGUEA y Observe vuelve con normalidad, sin panic.
// Perder una ventana de captación es perder un presupuesto automático; cortar el turno
// es dejar al cliente sin respuesta.
//
// # AG-1 · El presupuesto de I/O (D-044.26), por entrante admitido
//
//   - EXACTAMENTE 1 escritura: jobs.OpenOrAppend, que abre la ventana si no existía y
//     le añade las referencias si ya existía. Lleva la Key, el MessageTS y las
//     referencias [WaMessageID, MediaRefs...] en ese orden. NUNCA el Text.
//   - CERO lecturas de `intake_jobs` (ni ListAggregating ni CloseWindow ni
//     PutSourceText), CERO lecturas de `tenant_settings`, cero cripto y cero red. El
//     cierre —también el adelantado por intent— lo ejecuta el barrido, nunca Observe.
//   - COMO MUCHO 1 pregunta al resolver de derechos (que cachea con TTL).
//
// El orden de los pasos es parte del contrato:
//
//  1. GUARDAS BARATAS, sin preguntarle nada a nadie: receptor nil, agregador sin log,
//     jobs o ents, WaMessageID "" (un entrante sin identificador no puede aportar una
//     referencia opaca), o Key incompleta (sin evento vivo no hay ventana: un saludo
//     suelto, el LIMBO, no abre nada). Vuelve sin escribir y SIN consultar el resolver.
//  2. EL GATE: ents.Has(ctx, tenant, entitlements.FeatureLLMIntake), UNA vez. Es el
//     ÚNICO gate de este camino: no se consulta `api_llm` (ADR-0044, D-044.28: la vía
//     —local o API— no es asunto de la ventana) ni ninguna otra feature. Sin la
//     feature vuelve en silencio: cero ventanas, cero jobs. Si el resolver FALLA es
//     fail-closed: no escribe y deja en Warn "agregador: no se pudo resolver la feature
//     llm_intake; el entrante no entra en ninguna ventana", con "error", "tenant_id" y
//     "session_id".
//  3. EL MISMO MENSAJE DOS VECES: si el WaMessageID es el ÚLTIMO que se observó para
//     esa misma Key, vuelve sin escribir (red SECUNDARIA; la primera es el dedupe
//     persistente de ingesta). Sin ella, un doble Observe duplicaría la referencia: el
//     UPSERT concatena a ciegas.
//  4. LA SENTENCIA: OpenOrAppend. Si falla, deja en Error "agregador: no se pudo
//     abrir/ampliar la ventana de captación; el turno sigue", con "error",
//     "tenant_id", "session_id" y "wa_message_id", y vuelve sin pedir nada.
//  5. EL ADELANTO, lo último: si hay AheadRequester y Text no es "", llama a
//     Request(Key, Text) UNA vez. Va DESPUÉS de que la ventana exista de verdad. Sin
//     texto (un mensaje de solo media) no se pide: es un motivo SANO (REQ-38).
//
// # AG-5 · La memoria del último mensaje NO se borra al cerrar la ventana
//
// La memoria del paso 3 guarda UN id por Key (el último) y sobrevive al cierre de la
// ventana. Consecuencias que el contrato promete:
//
//   - una RE-ENTREGA del mismo wa_message_id DESPUÉS del flush NO reabre ventana ni
//     escribe nada (trampa T-10: borrarla «por limpieza» reabriría ventanas con
//     mensajes ya procesados);
//   - un mensaje DISTINTO sí pasa y abre la ventana SIGUIENTE sobre el mismo evento
//     (el índice único de la 0072 es PARCIAL a propósito: un cliente puede volver a
//     pedir);
//   - vive en el proceso: un agregador NUEVO sobre el mismo almacén no la tiene.
//
// ⚠️ Rareza portada (aggregator.go:511-525): el id se anota como visto ANTES de la
// sentencia, así que si OpenOrAppend falla, la re-entrega inmediata de ESE MISMO id se
// descarta en el paso 3 y no reintenta la escritura.
//
// # PII
//
// Ninguna línea de log lleva el Text ni las MediaRefs: solo tenant_id, session_id y
// wa_message_id.
func (s *IntakeAggregator) Observe(ctx context.Context, ref IncomingRef) {
	if !s.acceptable(ref) {
		return
	}
	// GATE, y va DESPUÉS de las guardas baratas (patrón thread.go): sin la feature
	// no se escribe nada, y un fallo del resolver se trata como «no» (fail-closed,
	// mismo criterio que entitlements.Resolver). La caché con TTL hace que esto no
	// sea una consulta por mensaje.
	has, err := s.ents.Has(ctx, ref.Key.TenantID, featureIntakeAggregation)
	if err != nil {
		s.log.Warn("agregador: no se pudo resolver la feature llm_intake; el entrante no entra en ninguna ventana",
			"error", err, "tenant_id", ref.Key.TenantID, "session_id", ref.Key.SessionID)
		return
	}
	if !has {
		return
	}
	// ⚠️ Rareza portada tal cual: el id queda anotado como visto ANTES de la sentencia.
	if s.alreadySeen(ref) {
		return
	}
	refs := append([]string{ref.WaMessageID}, ref.MediaRefs...)
	// LA SENTENCIA. Una, y ninguna lectura.
	if err := s.jobs.OpenOrAppend(ctx, intake.Append{
		Key:       ref.Key,
		MessageTS: ref.MessageTS,
		Refs:      refs,
	}); err != nil {
		s.log.Error("agregador: no se pudo abrir/ampliar la ventana de captación; el turno sigue",
			"error", err, "tenant_id", ref.Key.TenantID, "session_id", ref.Key.SessionID,
			"wa_message_id", ref.WaMessageID)
		return
	}
	// EL ADELANTO (T1.2, reescrito por T1.6-4). Es lo último y NO ES UNA LLAMADA AL
	// MODELO: es un encolado que vuelve en nanosegundos. La inferencia —segundos— la
	// hace un pool aparte y su respuesta entra por OnClassified. El cierre lo sigue
	// ejecutando el barrido, fuera del camino del mensaje (D-044.26 intacta).
	s.requestAhead(ref)
}

// requestAhead pide la clasificación de este entrante. Va DESPUÉS de que la ventana
// exista de verdad: preguntar por una ventana que el INSERT no llegó a abrir sería
// gastar una inferencia para anotar una pista que no casa con nada.
//
// Sin texto no se pide: un mensaje de solo media no tiene nada que clasificar, y es
// además uno de los motivos SANOS que REQ-38 saca del canal de avisos.
func (s *IntakeAggregator) requestAhead(ref IncomingRef) {
	if s.ahead == nil || ref.Text == "" {
		return
	}
	s.ahead.Request(ref.Key, ref.Text)
}

// acceptable agrupa las GUARDAS BARATAS: las que no cuestan ni una consulta y por
// eso van primero (patrón thread.go). Una clave incompleta no es «una ventana
// rara»: las cuatro columnas son NOT NULL en la 0072 y el INSERT reventaría.
func (s *IntakeAggregator) acceptable(ref IncomingRef) bool {
	switch {
	case s == nil, s.log == nil, s.jobs == nil, s.ents == nil:
		return false
	case ref.WaMessageID == "":
		// Un entrante sin identificador no puede aportar una referencia opaca, y
		// `source_refs` es justo eso. No se inventa una.
		return false
	case !ref.Key.Valid():
		// Sin evento vivo no hay ventana: `intake_jobs.event_id` es NOT NULL y la
		// fuente del literal (el hilo) cuelga del evento. Un saludo suelto —el
		// LIMBO, sin evento— no abre nada, y es correcto.
		return false
	}
	return true
}

// alreadySeen descarta el MISMO `wa_message_id` observado dos veces sobre la misma
// ventana. Red SECUNDARIA (la primera es el dedupe persistente de ingesta, Plan 028 ·
// T6): sin ella, un doble Observe duplicaría la referencia dentro de `source_refs`
// porque el `DO UPDATE` concatena a ciegas y no puede comprobar nada (no lee).
func (s *IntakeAggregator) alreadySeen(ref IncomingRef) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen[ref.Key] == ref.WaMessageID {
		return true
	}
	s.seen[ref.Key] = ref.WaMessageID
	return false
}

// 🔴 NO HAY UN `forget(key)` AL CERRAR LA VENTANA, Y ESO ES DELIBERADO (AG-5, trampa
// T-10). Lo hubo, y era un defecto: al cerrar la ventana se borraba su entrada de
// `seen`, con lo que una RE-ENTREGA del mismo `wa_message_id` después del flush volvía
// a pasar la guarda y ABRÍA UNA VENTANA NUEVA con el mensaje que ya se había procesado
// — justo lo que T1.7 (b) prohíbe. La entrada se conserva.
//
// Lo que se paga por conservarla, dicho con el número: el mapa guarda UNA entrada
// —dos cadenas cortas— por tupla (tenant, sesión, contacto, evento) vista desde el
// arranque. Un tenant que abriera mil eventos al día suma del orden de 100 KB
// diarios, y el mapa se vacía en cada reinicio. Si algún día el parque hace que eso
// importe, la salida es una caché ACOTADA (LRU por tamaño), no volver a borrar al
// cerrar: borrar al cerrar reabre el defecto.
//
// Y conservarla NO impide reabrir ventana sobre el mismo evento, que es legítimo
// —el índice de la 0072 es PARCIAL a propósito, un cliente puede volver a pedir—:
// `seen` guarda el ÚLTIMO id visto, así que un mensaje DISTINTO pasa la guarda y
// abre la ventana siguiente. Lo único que se descarta es el mismo mensaje dos veces.
