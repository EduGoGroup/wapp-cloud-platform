package apipublica

// health_derive_test.go — la tabla de HealthRules.derive, el auxiliar no exportado que lleva la
// regla de negocio de health.go (05 E-4, P6): test INTERNO que nació con el verde. Lo observable
// por D2 (el campo "health" y el Alerter) lo cubre health_test.go.

import (
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet"
)

func TestHealthRules_Derive(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	given := HealthRules{DegradedAfter: 5 * time.Minute, StaleAfter: 2 * time.Minute, Now: clock}
	wide := HealthRules{DegradedAfter: time.Hour, StaleAfter: 30 * time.Minute, Now: clock}
	cases := []struct {
		name  string
		rules HealthRules
		s     fleet.Session
		want  string
	}{
		{"healthy_fresh_health", given, fleet.Session{LastHealthAt: ago(10 * time.Second)}, ""},
		{"sustained_degraded", given, fleet.Session{DegradedSince: ago(6 * time.Minute), LastHealthAt: ago(30 * time.Second)}, healthDegraded},
		{"recent_degraded_is_not_labelled_yet", given, fleet.Session{DegradedSince: ago(time.Minute), LastHealthAt: ago(30 * time.Second)}, ""},
		{"stale_health", given, fleet.Session{LastHealthAt: ago(3 * time.Minute)}, healthStale},
		{"stale_wins_over_degraded", given, fleet.Session{DegradedSince: ago(10 * time.Minute), LastHealthAt: ago(3 * time.Minute)}, healthStale},
		{"an_old_edge_without_health_is_never_labelled", given, fleet.Session{}, ""},
		{"degraded_without_any_health_instant", given, fleet.Session{DegradedSince: ago(6 * time.Minute)}, healthDegraded},
		{"exactly_at_the_stale_threshold_is_not_stale", given, fleet.Session{LastHealthAt: ago(2 * time.Minute)}, ""},
		{"just_past_the_stale_threshold", given, fleet.Session{LastHealthAt: ago(2*time.Minute + time.Nanosecond)}, healthStale},
		{"exactly_at_the_degraded_threshold_is_not_degraded", given, fleet.Session{DegradedSince: ago(5 * time.Minute), LastHealthAt: now}, ""},
		{"just_past_the_degraded_threshold", given, fleet.Session{DegradedSince: ago(5*time.Minute + time.Nanosecond), LastHealthAt: now}, healthDegraded},
		{"given_thresholds_replace_the_defaults", wide, fleet.Session{DegradedSince: ago(20 * time.Minute), LastHealthAt: ago(10 * time.Minute)}, ""},
		{"zero_rules_use_five_minutes", HealthRules{Now: clock}, fleet.Session{DegradedSince: ago(6 * time.Minute), LastHealthAt: ago(10 * time.Second)}, healthDegraded},
		{"zero_rules_use_two_minutes", HealthRules{Now: clock}, fleet.Session{LastHealthAt: ago(3 * time.Minute)}, healthStale},
		{"negative_thresholds_fall_to_the_defaults", HealthRules{DegradedAfter: -1, StaleAfter: -1, Now: clock}, fleet.Session{LastHealthAt: ago(time.Minute)}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.rules.derive(tc.s); got != tc.want {
				t.Errorf("derive = %q, quiero %q", got, tc.want)
			}
		})
	}
}
