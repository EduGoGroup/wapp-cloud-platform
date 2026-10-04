package entitlements_test

// Parte de postgres_test.go, partido por tamaño (F2-03, a petición de Jhoan; solo se movieron
// declaraciones): las promesas de ListEffective.

import (
	"context"
	"database/sql/driver"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// --- ListEffective ------------------------------------------------------------------------------

// TestPostgres_ListEffective_PlanAndSortedFeatures: devuelve el plan del tenant y sus features en
// orden por bytes ('_' antes que 'b'), sea cual sea el orden en que la BD las entregue.
func TestPostgres_ListEffective_PlanAndSortedFeatures(t *testing.T) {
	p, fdb, _ := newPostgres(t)
	fdb.set(func(f *fakeDB) {
		f.plans[tenantA] = "pro"
		f.features[tenantA] = []driver.Value{"menu", "contract_ab", "cart_basic", "contract_a_b"}
	})
	plan, features := mustList(t, p, tenantA)
	if plan != "pro" {
		t.Errorf("plan = %q, quería %q", plan, "pro")
	}
	if want := []string{"cart_basic", "contract_a_b", "contract_ab", "menu"}; !slices.Equal(features, want) {
		t.Errorf("features = %v, quería %v (orden alfabético por bytes)", features, want)
	}
}

// TestPostgres_ListEffective_EmptyIsNotError: un plan sin features da la lista vacía, sin error.
func TestPostgres_ListEffective_EmptyIsNotError(t *testing.T) {
	p, fdb, _ := newPostgres(t)
	fdb.set(func(f *fakeDB) { f.plans[tenantA] = "basic" })
	plan, features := mustList(t, p, tenantA)
	if plan != "basic" || len(features) != 0 {
		t.Errorf("ListEffective = (%q, %v); quería (\"basic\", vacía)", plan, features)
	}
}

// TestPostgres_ListEffective_MissingTenant: un tenant que no existe da ("", nil, nil).
func TestPostgres_ListEffective_MissingTenant(t *testing.T) {
	p, _, _ := newPostgres(t)
	plan, features, err := p.ListEffective(context.Background(), "tenant-que-no-existe")
	if plan != "" || features != nil || err != nil {
		t.Errorf("ListEffective de un tenant inexistente = (%q, %v, %v); quería (\"\", nil, nil)", plan, features, err)
	}
}

// TestPostgres_ListEffective_CachesPerTenant: dentro del TTL el mismo tenant no vuelve a
// consultar; otro tenant es otra entrada.
func TestPostgres_ListEffective_CachesPerTenant(t *testing.T) {
	p, fdb, clock := newPostgres(t)
	fdb.set(func(f *fakeDB) {
		f.plans[tenantA], f.plans[tenantB] = "pro", "basic"
		f.features[tenantA] = []driver.Value{"menu"}
	})
	mustList(t, p, tenantA)
	clock.Advance(testTTL / 2)
	fdb.set(func(f *fakeDB) { f.plans[tenantA] = "basic" })
	if plan, features := mustList(t, p, tenantA); plan != "pro" || !slices.Equal(features, []string{"menu"}) {
		t.Errorf("segundo ListEffective(A) dentro del TTL = (%q, %v); quería lo cacheado (\"pro\", [menu])", plan, features)
	}
	if n := fdb.count(queryTenantPlan); n != 1 {
		t.Errorf("dos ListEffective(A) dentro del TTL hicieron %d consultas de plan; quería 1", n)
	}
	if plan, _ := mustList(t, p, tenantB); plan != "basic" {
		t.Errorf("ListEffective(B) = plan %q; quería \"basic\" (otra entrada)", plan)
	}
	if n := fdb.count(queryTenantPlan); n != 2 {
		t.Errorf("ListEffective(B) tras ListEffective(A): %d consultas de plan, quería 2", n)
	}
}

// TestPostgres_ListEffective_ExpiresExactlyAtTTL: la misma frontera que Has.
func TestPostgres_ListEffective_ExpiresExactlyAtTTL(t *testing.T) {
	p, fdb, clock := newPostgres(t)
	fdb.set(func(f *fakeDB) { f.plans[tenantA] = "pro" })
	mustList(t, p, tenantA)
	fdb.set(func(f *fakeDB) { f.plans[tenantA] = "basic" })

	clock.Advance(testTTL - time.Nanosecond)
	if plan, _ := mustList(t, p, tenantA); plan != "pro" {
		t.Errorf("ListEffective un nanosegundo antes del vencimiento = plan %q; la entrada aún valía (\"pro\")", plan)
	}
	clock.Advance(time.Nanosecond)
	if plan, _ := mustList(t, p, tenantA); plan != "basic" {
		t.Errorf("ListEffective en el instante del vencimiento = plan %q; debía re-consultar (\"basic\")", plan)
	}
}

// TestPostgres_ListEffective_ReturnsCopy: mutar la lista devuelta —la del fallo de caché o la del
// acierto— no corrompe lo cacheado.
func TestPostgres_ListEffective_ReturnsCopy(t *testing.T) {
	p, fdb, _ := newPostgres(t)
	fdb.set(func(f *fakeDB) {
		f.plans[tenantA] = "pro"
		f.features[tenantA] = []driver.Value{"cart_basic", "menu"}
	})
	want := []string{"cart_basic", "menu"}

	_, miss := mustList(t, p, tenantA)
	miss[0] = "poisoned-on-miss"
	_, hit := mustList(t, p, tenantA)
	if !slices.Equal(hit, want) {
		t.Fatalf("tras mutar la lista del fallo de caché, el acierto devuelve %v; quería %v", hit, want)
	}
	hit[1] = "poisoned-on-hit"
	if _, again := mustList(t, p, tenantA); !slices.Equal(again, want) {
		t.Errorf("tras mutar la lista de un acierto, el siguiente devuelve %v; quería %v", again, want)
	}
	if n := fdb.count(queryTenantPlan); n != 1 {
		t.Errorf("hubo %d consultas de plan; quería 1 (todo lo demás, de la caché)", n)
	}
}

// TestPostgres_ListEffective_ErrorIsNotCached: con la BD fallando devuelve ("", nil, err); la
// llamada siguiente vuelve a consultar.
func TestPostgres_ListEffective_ErrorIsNotCached(t *testing.T) {
	p, fdb, _ := newPostgres(t)
	fdb.set(func(f *fakeDB) {
		f.plans[tenantA] = "pro"
		f.fail[queryFeatures] = errBoom
	})
	if _, _, err := p.ListEffective(context.Background(), tenantA); err == nil {
		t.Fatal("ListEffective con la BD fallando: err = nil")
	}
	fdb.set(func(f *fakeDB) { delete(f.fail, queryFeatures) })
	if plan, _ := mustList(t, p, tenantA); plan != "pro" {
		t.Errorf("ListEffective tras un error = plan %q; quería \"pro\" (el error no se cachea)", plan)
	}
}

// TestPostgres_ListEffective_ErrorTexts: cada fallo sale como ("", nil, err), envuelto y con su
// texto literal.
func TestPostgres_ListEffective_ErrorTexts(t *testing.T) {
	cases := []struct {
		name    string
		seed    func(f *fakeDB)
		prefix  string
		wrapped bool // si err envuelve errBoom
	}{
		{"planQueryFails", func(f *fakeDB) { f.fail[queryTenantPlan] = errBoom },
			"entitlements: resolver el plan del tenant: ", true},
		{"featuresQueryFails", func(f *fakeDB) { f.fail[queryFeatures] = errBoom },
			"entitlements: listar features efectivas: ", true},
		{"nullFeatureRow", func(f *fakeDB) { f.features[tenantA] = []driver.Value{"menu", nil} },
			"entitlements: scan de feature: ", false},
		{"iterationFails", func(f *fakeDB) {
			f.features[tenantA] = []driver.Value{"menu"}
			f.featuresEndErr = errBoom
		}, "entitlements: iterar features: ", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, fdb, _ := newPostgres(t)
			fdb.set(func(f *fakeDB) {
				f.plans[tenantA] = "pro"
				c.seed(f)
			})
			plan, features, err := p.ListEffective(context.Background(), tenantA)
			if plan != "" || features != nil {
				t.Errorf("ListEffective con error = (%q, %v); quería (\"\", nil)", plan, features)
			}
			if err == nil || !strings.HasPrefix(err.Error(), c.prefix) {
				t.Errorf("ListEffective: err = %v; quería el prefijo literal %q", err, c.prefix)
			}
			if c.wrapped && !errors.Is(err, errBoom) {
				t.Errorf("ListEffective: err = %v; quería que envolviera el error de la BD", err)
			}
		})
	}
}
