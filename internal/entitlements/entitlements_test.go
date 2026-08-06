package entitlements

import (
	"context"
	"errors"
	"slices"
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

// TestFake_ListEffective comprueba las tres reglas que el endpoint publica: el
// plan efectivo (con 'basic' por defecto), las features encendidas en orden
// alfabético, y —la que importa— que una feature APAGADA por override no aparece
// en la lista, no basta con que Has diga false.
func TestFake_ListEffective(t *testing.T) {
	f := NewFake()
	f.SetPlan("tenant-a", "commerce")
	f.Enable("tenant-a", "menu")
	f.Enable("tenant-a", "cart_basic")
	f.Enable("tenant-a", "catalog_import")
	f.Disable("tenant-a", "crm_bridge") // override enabled=false

	plan, features, err := f.ListEffective(context.Background(), "tenant-a")
	if err != nil {
		t.Fatalf("ListEffective devolvió error inesperado: %v", err)
	}
	if plan != "commerce" {
		t.Fatalf("plan = %q, quería commerce", plan)
	}
	want := []string{"cart_basic", "catalog_import", "menu"}
	if !slices.Equal(features, want) {
		t.Fatalf("features = %v, quería %v (alfabético, sin la apagada)", features, want)
	}
	if slices.Contains(features, "crm_bridge") {
		t.Fatal("una feature con override enabled=false NO debe listarse")
	}

	// Tenant sin plan declarado ⇒ 'basic' y sin features.
	plan, features, err = f.ListEffective(context.Background(), "tenant-desconocido")
	if err != nil {
		t.Fatalf("ListEffective (tenant desconocido): %v", err)
	}
	if plan != "basic" || len(features) != 0 {
		t.Fatalf("tenant desconocido = (%q, %v), quería (basic, [])", plan, features)
	}
}

func TestFake_ListEffective_PropagaError(t *testing.T) {
	sentinel := errors.New("fallo de infraestructura")
	f := &Fake{Err: sentinel}
	if _, _, err := f.ListEffective(context.Background(), "t"); !errors.Is(err, sentinel) {
		t.Fatalf("ListEffective debía propagar el error inyectado, dio: %v", err)
	}
}

// TestCacheTTL comprueba que el TTL que se publica es el REAL del objeto: el
// default del paquete cuando nadie lo ajusta, y el inyectado con WithTTL.
func TestCacheTTL(t *testing.T) {
	if got := NewPostgres(nil).CacheTTL(); got != defaultCacheTTL {
		t.Fatalf("Postgres.CacheTTL() = %v, quería %v", got, defaultCacheTTL)
	}
	if got := NewPostgres(nil, WithTTL(5*time.Second)).CacheTTL(); got != 5*time.Second {
		t.Fatalf("Postgres.CacheTTL() con WithTTL = %v, quería 5s", got)
	}
	if got := NewFake().CacheTTL(); got != defaultCacheTTL {
		t.Fatalf("Fake.CacheTTL() = %v, quería %v", got, defaultCacheTTL)
	}
	if got := (&Fake{TTL: time.Second}).CacheTTL(); got != time.Second {
		t.Fatalf("Fake.CacheTTL() con TTL fijado = %v, quería 1s", got)
	}
}

// TestPostgres_EffectiveCache verifica la caché POR TENANT de ListEffective sin
// tocar la BD (mismo seam que TestPostgres_Cache): dentro del TTL no re-resuelve,
// vencido sí, la entrada de otro tenant no se cruza, y la lista devuelta es una
// COPIA (mutarla no corrompe lo cacheado).
func TestPostgres_EffectiveCache(t *testing.T) {
	var calls int
	features := []string{"cart_basic", "menu"}
	p := NewPostgres(nil, WithTTL(50*time.Millisecond))
	p.listFn = func(_ context.Context, tenantID string) (string, []string, error) {
		calls++
		return "plan-" + tenantID, slices.Clone(features), nil
	}

	ctx := context.Background()
	list := func(tenantID string) []string {
		t.Helper()
		plan, got, err := p.ListEffective(ctx, tenantID)
		if err != nil {
			t.Fatalf("ListEffective devolvió error inesperado: %v", err)
		}
		if plan != "plan-"+tenantID {
			t.Fatalf("plan = %q, quería plan-%s", plan, tenantID)
		}
		return got
	}

	first := list("t1")
	if calls != 1 {
		t.Fatalf("primer ListEffective: calls=%d, quería 1", calls)
	}
	// Mutar lo devuelto NO debe alterar la entrada cacheada.
	first[0] = "envenenada"

	if got := list("t1"); calls != 1 || !slices.Equal(got, features) {
		t.Fatalf("segundo ListEffective (dentro de TTL): calls=%d got=%v, quería 1 y %v (cache hit intacto)", calls, got, features)
	}
	// Otro tenant es otra entrada: re-resuelve.
	if list("t2"); calls != 2 {
		t.Fatalf("ListEffective de otro tenant: calls=%d, quería 2", calls)
	}
	// Vencido el TTL, vuelve a resolver.
	time.Sleep(60 * time.Millisecond)
	features = []string{"menu"}
	if got := list("t1"); calls != 3 || !slices.Equal(got, features) {
		t.Fatalf("ListEffective (TTL vencido): calls=%d got=%v, quería 3 y %v", calls, got, features)
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
