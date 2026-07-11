package entitlements

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestFake_Has(t *testing.T) {
	f := NewFake()
	f.Enable("tenant-a", FeatureLLMIntent)

	cases := []struct {
		name    string
		tenant  string
		feature string
		wantHas bool
	}{
		{"habilitada", "tenant-a", FeatureLLMIntent, true},
		{"otra feature del mismo tenant", "tenant-a", "otra", false},
		{"otro tenant sin la feature", "tenant-b", FeatureLLMIntent, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := f.Has(context.Background(), tc.tenant, tc.feature)
			if err != nil {
				t.Fatalf("Has devolvió error inesperado: %v", err)
			}
			if got != tc.wantHas {
				t.Fatalf("Has(%q,%q) = %v, quería %v", tc.tenant, tc.feature, got, tc.wantHas)
			}
		})
	}
}

func TestFake_Has_PropagaError(t *testing.T) {
	sentinel := errors.New("fallo de infraestructura")
	f := &Fake{Err: sentinel}
	if _, err := f.Has(context.Background(), "t", "f"); !errors.Is(err, sentinel) {
		t.Fatalf("Has debía propagar el error inyectado, dio: %v", err)
	}
}

// TestPostgres_Cache verifica la caché SIN tocar la BD: sustituye el lookup por un
// stub contable y comprueba que un segundo Has dentro del TTL no vuelve a resolver,
// y que expirado el TTL sí re-resuelve. Se ejercita el objeto real (Postgres) con
// db=nil porque el stub cortocircuita el acceso a la BD.
func TestPostgres_Cache(t *testing.T) {
	var calls int
	result := true
	p := NewPostgres(nil, WithTTL(50*time.Millisecond))
	p.lookupFn = func(_ context.Context, _, _ string) (bool, error) {
		calls++
		return result, nil
	}

	ctx := context.Background()
	has := func() bool {
		t.Helper()
		got, err := p.Has(ctx, "t", "f")
		if err != nil {
			t.Fatalf("Has devolvió error inesperado: %v", err)
		}
		return got
	}
	// Primer Has: miss ⇒ una resolución.
	if got := has(); !got || calls != 1 {
		t.Fatalf("primer Has: got=%v calls=%d, quería true/1", got, calls)
	}
	// Segundo Has dentro del TTL: hit ⇒ sin nueva resolución.
	if got := has(); !got || calls != 1 {
		t.Fatalf("segundo Has (dentro de TTL): got=%v calls=%d, quería true/1 (cache hit)", got, calls)
	}
	// Tras expirar el TTL: re-resuelve.
	time.Sleep(60 * time.Millisecond)
	result = false
	if got := has(); got || calls != 2 {
		t.Fatalf("tercer Has (TTL vencido): got=%v calls=%d, quería false/2 (re-lookup)", got, calls)
	}
}
