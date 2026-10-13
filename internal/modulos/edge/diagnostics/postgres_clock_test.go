package diagnostics

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Tests del reloj no exportado de Postgres (nacen con el verde, 05 E-4): el borde exacto del
// vencimiento en GetBundle, que con el reloj real no se puede fijar.

// TestNewPostgres_UsesTheProcessClock: el reloj de GetBundle es el del proceso, no uno fijo.
func TestNewPostgres_UsesTheProcessClock(t *testing.T) {
	store, _ := newPostgres(t)
	before := time.Now()
	got := store.now()
	after := time.Now()
	if got.Before(before) || got.After(after) {
		t.Errorf("el reloj de Postgres dio %v, quería un instante entre %v y %v", got, before, after)
	}
}

// TestPostgres_GetBundle_ExpiryBoundary: una solicitud vive mientras su vencimiento sea POSTERIOR
// al reloj del proceso; en el instante exacto del vencimiento ya está vencida.
func TestPostgres_GetBundle_ExpiryBoundary(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name       string
		expiresAt  time.Time
		want       error
		statements int
	}{
		{"one nanosecond left is alive", now.Add(time.Nanosecond), nil, 1},
		{"the exact instant is expired", now, ErrExpired, 2},
		{"one nanosecond late is expired", now.Add(-time.Nanosecond), ErrExpired, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store, fake := newPostgres(t,
				reply{row: bundleRow(c.expiresAt, "ready", receivedAt, "log", "dump", "{}")},
				reply{affected: 1},
			)
			store.now = func() time.Time { return now }
			_, err := store.GetBundle(context.Background(), "tenant-1", "cmd-1")
			if !errors.Is(err, c.want) {
				t.Errorf("err = %v, quería %v", err, c.want)
			}
			if got := len(fake.seen()); got != c.statements {
				t.Errorf("llegaron %d sentencias, quería %d", got, c.statements)
			}
		})
	}
}
