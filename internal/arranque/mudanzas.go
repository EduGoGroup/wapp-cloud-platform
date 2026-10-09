package arranque

// mudanzas.go — qué rutas del :8103 sirve ya la cara NUEVA (internal/apipublica).
//
// Fichero nuevo del arranque nuevo (no es copia del viejo: F0 · TX.4, D-10). La cara
// HTTP nueva crece por olas delante del publicapi viejo (estrangulador,
// apipublica.Componer en http.go), y cada fase muda SUS rutas según el mapa de
// documentations/reorganizacion-modular/plan/FX-cara-http/mapa-de-rutas.md, cuya
// copia ejecutable es testdata/mapa.tsv (id · listener · patrón · fase).
//
// El candado (mudanzas_test.go) exige que la cara nueva sirva EXACTAMENTE las filas
// del mapa con fase ≤ FaseActual y la vieja el resto, que no quede una familia
// partida entre las dos caras ni un comodín de la nueva solapando un literal que
// sigue en la vieja. Por eso la tarea `conmutar(<m>)` de cada fase sube FaseActual
// en el MISMO commit que monta las rutas: la tabla y el cableado avanzan juntos.

import "github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"

// FaseActual es la última fase de la reconstrucción cuyas rutas del :8103 sirve la
// cara nueva: 0 en F0 (la cara nace vacía y todo cae al publicapi viejo), 2 tras
// conmutar acceso, 3 tras conmutar edge, 4 tras conmutar inferencia, 5 tras la conmutación nominal de catalogo (no muda ninguna ruta), 6 tras conmutar solicitudes, 7 tras conmutar captacion, 8 tras conmutar conversacion. La fase de una fila del mapa se
// escribe «F<n>»; la fila pertenece a la cara nueva si n ≤ FaseActual.
const FaseActual = 7

// newFaceDeps es todo lo que la cara nueva necesita para montar sus áreas, y el ÚNICO sitio
// donde se agrupa: un campo por módulo. La fase que muda rutas declara aquí su <módulo>FaceDeps,
// le añade un campo a este struct y su Mount* a caraNueva; en la fase 8 lo rellena con su
// <módulo>DepsOfTheNewFace(c). Ni la firma ni el cuerpo de buildPublicAPIServer se tocan.
//
// Lo arman a dos manos: la fase 8 pone las áreas de los módulos (edge, inference, requests, capture) y
// buildPublicAPIServer (http.go) las cinco de acceso, cuyos servicios construye él. El candado
// de mudanzas lo arma con dobles.
type newFaceDeps struct {
	// common es lo compartido por todas las áreas: middleware, auditor y logger.
	common apipublica.Common
	// auth enciende A1–A7 (F2).
	auth apipublica.AuthDeps
	// rolePlane enciende B1–B14 (F2).
	rolePlane apipublica.RolePlaneDeps
	// audit enciende C1 (F2).
	audit apipublica.AuditDeps
	// entitlements enciende C2 (F2).
	entitlements apipublica.EntitlementsDeps
	// edge enciende D1–D6 (F3).
	edge edgeFaceDeps
	// inference enciende F1–F4 (F4).
	inference inferenceFaceDeps
	// requests enciende G1–G18 (F6): el módulo solicitudes.
	requests requestsFaceDeps
	// capture enciende H1, E1 y E2 (F7): el módulo captacion.
	capture captureFaceDeps
}

// edgeFaceDeps es lo que la cara nueva necesita para D1–D6 (F3 · conmutar(edge)). Lo arma la
// fase 8 (edgeDepsOfTheNewFace) con el gateway, la flota y el almacén de diagnóstico del
// contenedor; buildPublicAPIServer solo le añade el presupuesto de envío, que deriva de su
// propio writeTimeout.
type edgeFaceDeps struct {
	// messages enciende D1.
	messages apipublica.MessagesDeps
	// sessions enciende D2–D4.
	sessions apipublica.SessionsDeps
	// diagnostics enciende D5–D6.
	diagnostics apipublica.DiagnosticsDeps
}

// inferenceFaceDeps es lo que la cara nueva necesita para F1–F4 (F4 · conmutar(inferencia)). Lo
// arma la fase 8 (inferenceDepsOfTheNewFace) con los almacenes NUEVOS de tenant_llm y de avisos
// de degradación y el resolver de derechos del contenedor; buildPublicAPIServer no le añade nada.
type inferenceFaceDeps struct {
	// tenantLLM enciende F1–F3.
	tenantLLM apipublica.TenantLLMDeps
	// degradationNotices enciende F4.
	degradationNotices apipublica.DegradationNoticesDeps
}

// requestsFaceDeps es lo que la cara nueva necesita para G1–G18 (F6 · conmutar(solicitudes)): las
// seis áreas del módulo solicitudes. Lo arma la fase 8 (requestsDepsOfTheNewFace) con el Service,
// el generador de cotización, los almacenes, el gate y el notificador NUEVOS del contenedor;
// buildPublicAPIServer no le añade nada.
type requestsFaceDeps struct {
	// intakes enciende G1–G6 y G8 (la bandeja).
	intakes apipublica.IntakesDeps
	// intakeReports enciende G7, G9 y G10 (cotización sugerida, export y resumen).
	intakeReports apipublica.IntakeReportsDeps
	// tenantVariables enciende G11–G12.
	tenantVariables apipublica.TenantVariablesDeps
	// integrations enciende G13–G16 (la configuración del puente CRM).
	integrations apipublica.IntegrationsDeps
	// crmCallback enciende G17 (la vuelta del puente CRM; sin JWT).
	crmCallback apipublica.CRMCallbackDeps
	// eventTelemetry enciende G18.
	eventTelemetry apipublica.EventTelemetryDeps
}

// captureFaceDeps es lo que la cara nueva necesita para H1, E1 y E2 (F7 · conmutar(captacion)): las
// dos áreas del módulo captacion. Lo arma la fase 8 (captureDepsOfTheNewFace) con el servicio de
// re-análisis y el store de intenciones NUEVOS del contenedor, el resolver de derechos y el
// gateway; buildPublicAPIServer no le añade nada.
type captureFaceDeps struct {
	// reanalyze enciende H1 (`POST /api/v1/intakes/{id}/reanalyze`).
	reanalyze apipublica.ReanalyzeDeps
	// intents enciende E1–E2 (`GET` y `PUT /api/v1/intents`).
	intents apipublica.IntentsDeps
}

// caraNueva construye la cara nueva con las rutas de las fases ≤ FaseActual: desde F2,
// las 23 de acceso (A1–A7, B1–B14, C1–C2); desde F3, además las 6 de edge (D1–D6); desde F4, además las 4 de inferencia (F1–F4); desde F6, además las 18 de solicitudes (G1–G18); desde F7, además las 3 de captación (H1, E1–E2). Cada fase añade aquí su apipublica.Mount<Área>
// en el MISMO commit que sube FaseActual. Las condiciones de montaje (qué dependencia nil
// apaga qué ruta) son las de cada Mount*: aquí no se decide nada.
func caraNueva(d newFaceDeps) *apipublica.Cara {
	cara := apipublica.Nueva()
	apipublica.MountAuth(cara, d.common, d.auth)
	apipublica.MountRolePlane(cara, d.common, d.rolePlane)
	apipublica.MountAudit(cara, d.common, d.audit)
	apipublica.MountEntitlements(cara, d.common, d.entitlements)
	apipublica.MountMessages(cara, d.common, d.edge.messages)
	apipublica.MountSessions(cara, d.common, d.edge.sessions)
	apipublica.MountDiagnostics(cara, d.common, d.edge.diagnostics)
	apipublica.MountTenantLLM(cara, d.common, d.inference.tenantLLM)
	apipublica.MountDegradationNotices(cara, d.common, d.inference.degradationNotices)
	// 🔴 G2 (`GET …/intakes/{id}`, en MountIntakes) y G9 · G10 (`…/export`, `…/summary.json`, en
	// MountIntakeReports) van SIEMPRE juntas: el comodín solo en esta cara taparía los dos
	// literales de la vieja (FX mapa §4.2).
	apipublica.MountIntakes(cara, d.common, d.requests.intakes)
	apipublica.MountIntakeReports(cara, d.common, d.requests.intakeReports)
	apipublica.MountTenantVariables(cara, d.common, d.requests.tenantVariables)
	apipublica.MountIntegrations(cara, d.common, d.requests.integrations)
	apipublica.MountCRMCallback(cara, d.common, d.requests.crmCallback)
	apipublica.MountEventTelemetry(cara, d.common, d.requests.eventTelemetry)
	// H1 cuelga de `…/intakes/{id}/reanalyze`: comparte prefijo con la bandeja (G1–G10) pero es
	// de captación, y desde F7 va en la MISMA cara que ella, así que ya no hay comodín de una
	// cara tapando un literal de la otra.
	apipublica.MountReanalyze(cara, d.common, d.capture.reanalyze)
	apipublica.MountIntents(cara, d.common, d.capture.intents)
	return cara
}
