package publicapi

import (
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
)

// TestHealthRulesDerive cubre la derivación degraded/stale/limpio y su precedencia
// (Plan 031 · T4, ADR-0023).
func TestHealthRulesDerive(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	rules := HealthRules{
		DegradedAfter: 5 * time.Minute,
		StaleAfter:    2 * time.Minute,
		Now:           func() time.Time { return now },
	}

	cases := []struct {
		name string
		sess fleet.Session
		want string
	}{
		{
			name: "sana: salud fresca, sin degradado",
			sess: fleet.Session{WhatsappState: "connected", LastHealthAt: now.Add(-30 * time.Second)},
			want: "",
		},
		{
			name: "degradado sostenido > N",
			sess: fleet.Session{
				WhatsappState: "dead", DegradedReason: "dek_load_timeout",
				DegradedSince: now.Add(-6 * time.Minute), LastHealthAt: now.Add(-10 * time.Second),
			},
			want: healthDegraded,
		},
		{
			name: "degradado reciente (< N) aún no se etiqueta",
			sess: fleet.Session{
				WhatsappState: "degraded", DegradedSince: now.Add(-1 * time.Minute),
				LastHealthAt: now.Add(-10 * time.Second),
			},
			want: "",
		},
		{
			name: "stale: sin salud > M",
			sess: fleet.Session{WhatsappState: "connected", LastHealthAt: now.Add(-3 * time.Minute)},
			want: healthStale,
		},
		{
			name: "stale tiene precedencia sobre degraded",
			sess: fleet.Session{
				WhatsappState: "dead", DegradedSince: now.Add(-10 * time.Minute),
				LastHealthAt: now.Add(-3 * time.Minute),
			},
			want: healthStale,
		},
		{
			name: "Edge viejo sin salud: nunca degraded/stale",
			sess: fleet.Session{State: fleet.StateOnline},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rules.derive(tc.sess); got != tc.want {
				t.Fatalf("derive = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestHealthRulesDefaults verifica que umbrales <=0 caen a los defaults (nunca
// desactivan la derivación).
func TestHealthRulesDefaults(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	rules := HealthRules{Now: func() time.Time { return now }} // DegradedAfter/StaleAfter = 0

	// Con defaults (5m/2m): degradado hace 6m ⇒ degraded; salud hace 3m ⇒ stale.
	degraded := fleet.Session{DegradedSince: now.Add(-6 * time.Minute), LastHealthAt: now.Add(-10 * time.Second)}
	if got := rules.derive(degraded); got != healthDegraded {
		t.Fatalf("degraded con defaults = %q, want degraded", got)
	}
	stale := fleet.Session{LastHealthAt: now.Add(-3 * time.Minute)}
	if got := rules.derive(stale); got != healthStale {
		t.Fatalf("stale con defaults = %q, want stale", got)
	}
}
