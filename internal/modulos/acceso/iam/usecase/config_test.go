package usecase

import (
	"testing"
	"time"
)

// DefaultAccessTTL es 15 minutos: el número lo leen los consumidores web, que re-canjean solos
// cada ~13 min (ver in.ActiveTenantSelector).
func TestDefaultAccessTTL_IsFifteenMinutes(t *testing.T) {
	if DefaultAccessTTL != 15*time.Minute {
		t.Fatalf("DefaultAccessTTL = %v; quiere 15m", DefaultAccessTTL)
	}
}

// R-U33: un AccessTTL en cero toma el default y uno distinto de cero se respeta tal cual.
func TestConfig_ZeroTakesTheDefault(t *testing.T) {
	cases := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"zero_takes_default", 0, DefaultAccessTTL},
		{"explicit_value_is_kept", 5 * time.Minute, 5 * time.Minute},
		{"value_longer_than_default_is_kept", time.Hour, time.Hour},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Config{AccessTTL: c.in}.withDefaults()
			if got.AccessTTL != c.want {
				t.Fatalf("withDefaults(AccessTTL=%v).AccessTTL = %v; quiere %v", c.in, got.AccessTTL, c.want)
			}
		})
	}
}

// R-U33: el default se aplica sobre una copia; la Config del llamante no cambia.
func TestConfig_DefaultsDoNotMutateTheCaller(t *testing.T) {
	cfg := Config{}
	_ = cfg.withDefaults()
	if cfg.AccessTTL != 0 {
		t.Fatalf("withDefaults modificó la Config original: AccessTTL = %v", cfg.AccessTTL)
	}
}
