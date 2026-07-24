package ratelimit

import (
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func TestNewLimiter_Table(t *testing.T) {
	tests := []struct {
		name      string
		rate      rate.Limit
		burst     int
		wantRate  rate.Limit
		wantBurst int
	}{
		{
			name:      "normal limits",
			rate:      rate.Limit(5),
			burst:     10,
			wantRate:  rate.Limit(5),
			wantBurst: 10,
		},
		{
			name:      "zero burst normalized to 1",
			rate:      rate.Limit(10),
			burst:     0,
			wantRate:  rate.Limit(10),
			wantBurst: 1,
		},
		{
			name:      "negative burst normalized to 1",
			rate:      rate.Limit(2),
			burst:     -5,
			wantRate:  rate.Limit(2),
			wantBurst: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lim := NewLimiter(tt.rate, tt.burst)
			if lim.Rate() != tt.wantRate {
				t.Errorf("Rate() = %v, quiero %v", lim.Rate(), tt.wantRate)
			}
			if lim.burst != tt.wantBurst {
				t.Errorf("burst = %d, quiero %d", lim.burst, tt.wantBurst)
			}
		})
	}
}

func TestLimiter_Allow_Table(t *testing.T) {
	tests := []struct {
		name         string
		rate         rate.Limit
		burst        int
		key          string
		attempts     int
		wantResults []bool
	}{
		{
			name:         "rate 1 burst 2 allows exactly 2 immediate calls",
			rate:         rate.Limit(1),
			burst:        2,
			key:          "tenant-a",
			attempts:     3,
			wantResults: []bool{true, true, false},
		},
		{
			name:         "normalized burst 1 allows 1 call",
			rate:         rate.Limit(10),
			burst:        0,
			key:          "tenant-b",
			attempts:     2,
			wantResults: []bool{true, false},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lim := NewLimiter(tt.rate, tt.burst)
			for i := 0; i < tt.attempts; i++ {
				got := lim.Allow(tt.key)
				if got != tt.wantResults[i] {
					t.Errorf("Allow intoy %d = %v, quiero %v", i+1, got, tt.wantResults[i])
				}
			}
		})
	}
}

func TestLimiter_Eviction_Table(t *testing.T) {
	tests := []struct {
		name           string
		staleAge       time.Duration
		staleShouldEvict bool
	}{
		{
			name:           "buckets older than 10m are evicted",
			staleAge:       15 * time.Minute,
			staleShouldEvict: true,
		},
		{
			name:           "recent buckets are preserved",
			staleAge:       1 * time.Minute,
			staleShouldEvict: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lim := NewLimiter(rate.Limit(10), 5)
			key := "test-key"

			lim.mu.Lock()
			lim.buckets[key] = &bucket{
				lim:      rate.NewLimiter(10, 5),
				lastSeen: time.Now().Add(-tt.staleAge),
			}
			lim.evictStaleLocked()
			_, exists := lim.buckets[key]
			lim.mu.Unlock()

			if tt.staleShouldEvict && exists {
				t.Errorf("clave %q no debió existir tras evicción", key)
			}
			if !tt.staleShouldEvict && !exists {
				t.Errorf("clave %q debió conservarse", key)
			}
		})
	}
}

func TestLimiter_EvictionTriggerOnMaxBuckets(t *testing.T) {
	lim := NewLimiter(rate.Limit(10), 5)
	oldTime := time.Now().Add(-15 * time.Minute)

	lim.mu.Lock()
	for i := 0; i < maxBuckets; i++ {
		lim.buckets[time.Now().String()+string(rune(i))] = &bucket{
			lim:      rate.NewLimiter(10, 5),
			lastSeen: oldTime,
		}
	}
	lim.mu.Unlock()

	if !lim.Allow("trigger-key") {
		t.Fatal("Allow debe retornar true al crear nuevo bucket")
	}

	lim.mu.Lock()
	count := len(lim.buckets)
	lim.mu.Unlock()

	if count > maxBuckets {
		t.Fatalf("cantidad de buckets (%d) supera el máximo (%d)", count, maxBuckets)
	}
}
