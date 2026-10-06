// Porta internal/publicapi/health.go @ 115a4ba (HealthRules y su derivación, Alerter,
// NoopAlerter), sobre el fleet.Session del módulo edge NUEVO.
//
// health.go — LA SALUD DERIVADA DE UNA SESIÓN (ADR-0023, Plan 031 · T4). El estado se calcula al
// SERVIR GET /api/v1/sessions (D2, sessions.go): no se persiste y no hay job de fondo.
//
// En el rojo solo existían HealthRules, Alerter y NoopAlerter; la derivación (derive) y sus
// umbrales por defecto nacieron con el verde.

package apipublica

import (
	"context"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet"
)

// Estados de salud DERIVADOS que GET /api/v1/sessions calcula al servir (no hay
// job de fondo): "" sano/sin dato, healthDegraded o healthStale (ADR-0023).
const (
	healthDegraded = "degraded"
	healthStale    = "stale"
)

// Umbrales por defecto de la derivación (se afinan con el e2e real, ADR-0023
// §Puntos abiertos). Un valor <=0 en HealthRules cae a estos.
const (
	defaultDegradedAfter = 5 * time.Minute
	defaultStaleAfter    = 2 * time.Minute
)

// HealthRules deriva el estado de salud consultable de una sesión a partir de su snapshot
// persistido (Plan 031 · T4, ADR-0023). Es lo que D2 publica en el campo "health" de cada fila:
//
//   - "stale" si HAY un snapshot previo (LastHealthAt no es cero) y envejeció MÁS de StaleAfter:
//     el dato ya no es confiable. TIENE PRECEDENCIA sobre "degraded": no tiene sentido llamar
//     «degradada» a una sesión de la que hace rato no se sabe nada;
//   - "degraded" si lleva en degradado (DegradedSince no es cero) MÁS de DegradedAfter;
//   - "" (el campo se omite) en cualquier otro caso: sana, degradada hace poco, o sin salud
//     reportada aún. Un Edge viejo, que nunca reportó salud, JAMÁS se etiqueta: no hay dato que
//     juzgar.
//
// Las dos comparaciones son estrictas (justo en el umbral todavía no se etiqueta).
//
// DegradedAfter (N) y StaleAfter (M) se cablean desde la configuración (env global
// WAPP_HEALTH_*). Un valor <= 0 cae al valor por defecto —5 min para DegradedAfter, 2 min para
// StaleAfter—: nunca desactiva la derivación por accidente. Now inyecta el reloj en los tests;
// nil ⇒ time.Now. El valor cero de HealthRules es, por tanto, usable.
type HealthRules struct {
	DegradedAfter time.Duration
	StaleAfter    time.Duration
	Now           func() time.Time
}

// now devuelve el instante actual usando el reloj inyectado o time.Now.
func (hr HealthRules) now() time.Time {
	if hr.Now != nil {
		return hr.Now()
	}
	return time.Now()
}

// degradedAfter/staleAfter normalizan los umbrales: <=0 cae al default (nunca
// desactiva la derivación por accidente).
func (hr HealthRules) degradedAfter() time.Duration {
	if hr.DegradedAfter <= 0 {
		return defaultDegradedAfter
	}
	return hr.DegradedAfter
}

func (hr HealthRules) staleAfter() time.Duration {
	if hr.StaleAfter <= 0 {
		return defaultStaleAfter
	}
	return hr.StaleAfter
}

// derive calcula el estado de salud consultable de la sesión:
//   - "stale" si HAY un snapshot previo (last_health_at) pero envejeció más de M
//     (el dato ya no es confiable) — TIENE PRECEDENCIA: no tiene sentido llamar
//     "degradado" a una sesión de la que hace rato no sabemos nada.
//   - "degraded" si lleva en degradado (degraded_since) más de N.
//   - "" en cualquier otro caso (sana, o sin salud reportada aún: un Edge viejo
//     nunca se etiqueta degraded/stale porque no hay dato que juzgar).
func (hr HealthRules) derive(s fleet.Session) string {
	now := hr.now()
	if !s.LastHealthAt.IsZero() && now.Sub(s.LastHealthAt) > hr.staleAfter() {
		return healthStale
	}
	if !s.DegradedSince.IsZero() && now.Sub(s.DegradedSince) > hr.degradedAfter() {
		return healthDegraded
	}
	return ""
}

// Alerter es el PUNTO DE EXTENSIÓN para el alerting push (email/webhook/UI) sobre una salud
// derivada degraded/stale (ADR-0023 §Decisión / §Puntos abiertos). En este corte el estado es
// CONSULTABLE (GET /api/v1/sessions) y el único implementador es NoopAlerter: nada se empuja
// todavía. Un futuro alerting real (con dedupe) colgará de aquí sin tocar la ingesta ni la API.
//
// D2 lo invoca una vez por cada sesión cuya salud derivada no es vacía, con el tenant del token,
// el session_id y el estado ("degraded" o "stale"). Es best-effort: su error no altera la
// respuesta.
type Alerter interface {
	Alert(ctx context.Context, tenantID, sessionID, derivedState string) error
}

// NoopAlerter es la implementación no-op registrada por defecto: descarta la alerta.
type NoopAlerter struct{}

// Alert implementa Alerter sin efecto: devuelve nil siempre, con cualquier argumento.
func (NoopAlerter) Alert(context.Context, string, string, string) error { return nil }
