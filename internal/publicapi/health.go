package publicapi

import (
	"context"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
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

// HealthRules deriva el estado de salud consultable de una sesión a partir de su
// snapshot persistido (Plan 031 · T4, ADR-0023). Mecanismo simple primero: el
// estado se calcula al SERVIR, sin persistir ni jobs. DegradedAfter (N) y StaleAfter
// (M) son configurables (env global WAPP_HEALTH_*); Now permite inyectar el reloj en
// tests (nil ⇒ time.Now).
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

// Alerter es el PUNTO DE EXTENSIÓN para el alerting push (email/webhook/UI) sobre
// una salud derivada degraded/stale (ADR-0023 §Decisión / §Puntos abiertos). En
// este corte el estado es CONSULTABLE (GET /api/v1/sessions) y el único implementador
// es NoopAlerter: nada se empuja todavía. Un futuro alerting real (con dedupe) colgará
// de aquí sin tocar la ingesta ni la API.
type Alerter interface {
	Alert(ctx context.Context, tenantID, sessionID, derivedState string) error
}

// NoopAlerter es la implementación no-op registrada por defecto: descarta la alerta.
type NoopAlerter struct{}

// Alert implementa Alerter sin efecto.
func (NoopAlerter) Alert(context.Context, string, string, string) error { return nil }
