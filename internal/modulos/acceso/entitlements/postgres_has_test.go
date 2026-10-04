package entitlements_test

// Parte de postgres_test.go, partido por tamaño (F2-03, a petición de Jhoan; solo se movieron
// declaraciones): las promesas de Has.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// --- Has ----------------------------------------------------------------------------------------

// TestPostgres_Has_Resolution: el override manda en los dos sentidos; sin override manda lo que
// diga el plan; un tenant inexistente no tiene la feature, sin error.
func TestPostgres_Has_Resolution(t *testing.T) {
	cases := []struct {
		name     string
		override *bool
		planHas  bool
		want     bool
	}{
		{"overrideEnabled_WinsOverPlanWithout", new(true), false, true},
		{"overrideDisabled_WinsOverPlanWith", new(false), true, false},
		{"noOverride_PlanHasIt", nil, true, true},
		{"noOverride_PlanLacksIt", nil, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, fdb, _ := newPostgres(t)
			fdb.set(func(f *fakeDB) {
				if c.override != nil {
					f.overrides[pair{tenantA, featureF}] = *c.override
				}
				f.planHas[pair{tenantA, featureF}] = c.planHas
			})
			if got := mustHas(t, p, tenantA, featureF); got != c.want {
				t.Errorf("Has = %v, quería %v", got, c.want)
			}
		})
	}
}

// TestPostgres_Has_MissingTenantIsFalseWithoutError: un tenant que no existe no tiene derechos, y
// no es un error.
func TestPostgres_Has_MissingTenantIsFalseWithoutError(t *testing.T) {
	p, _, _ := newPostgres(t)
	if mustHas(t, p, "tenant-que-no-existe", featureF) {
		t.Error("Has de un tenant inexistente = true; no tiene derechos")
	}
}

// TestPostgres_Has_CachesPerPair: dentro del TTL el mismo par no vuelve a consultar; otro feature
// del mismo tenant u otro tenant con el mismo feature son entradas propias.
func TestPostgres_Has_CachesPerPair(t *testing.T) {
	p, fdb, clock := newPostgres(t)
	fdb.set(func(f *fakeDB) { f.overrides[pair{tenantA, featureF}] = true })

	if !mustHas(t, p, tenantA, featureF) {
		t.Fatal("primer Has(A, f) = false, quería true")
	}
	clock.Advance(testTTL / 2)
	fdb.set(func(f *fakeDB) { f.overrides[pair{tenantA, featureF}] = false })
	if !mustHas(t, p, tenantA, featureF) {
		t.Error("segundo Has(A, f) dentro del TTL = false: debía servirse de la caché (true)")
	}
	if n := fdb.count(queryOverride); n != 1 {
		t.Errorf("dos Has(A, f) dentro del TTL hicieron %d consultas de override; quería 1", n)
	}

	mustHas(t, p, tenantA, featureG)
	if n := fdb.count(queryOverride); n != 2 {
		t.Errorf("Has(A, g) tras Has(A, f): %d consultas de override, quería 2 (otro par, otra entrada)", n)
	}
	mustHas(t, p, tenantB, featureF)
	if n := fdb.count(queryOverride); n != 3 {
		t.Errorf("Has(B, f) tras Has(A, f): %d consultas de override, quería 3 (otro tenant, otra entrada)", n)
	}
}

// TestPostgres_Has_CachesFalse: el false también se cachea.
func TestPostgres_Has_CachesFalse(t *testing.T) {
	p, fdb, _ := newPostgres(t)
	if mustHas(t, p, tenantA, featureF) {
		t.Fatal("primer Has = true sin override ni plan; quería false")
	}
	fdb.set(func(f *fakeDB) { f.overrides[pair{tenantA, featureF}] = true })
	if mustHas(t, p, tenantA, featureF) {
		t.Error("segundo Has dentro del TTL = true: el false debía quedar en la caché")
	}
	if n := fdb.count(queryOverride); n != 1 {
		t.Errorf("dos Has con respuesta false hicieron %d consultas de override; quería 1 (el false se cachea)", n)
	}
}

// TestPostgres_Has_ExpiresExactlyAtTTL: un instante antes del vencimiento la entrada vale; en el
// instante exacto ya no, y se vuelve a consultar.
func TestPostgres_Has_ExpiresExactlyAtTTL(t *testing.T) {
	p, fdb, clock := newPostgres(t)
	fdb.set(func(f *fakeDB) { f.overrides[pair{tenantA, featureF}] = true })
	mustHas(t, p, tenantA, featureF)
	fdb.set(func(f *fakeDB) { f.overrides[pair{tenantA, featureF}] = false })

	clock.Advance(testTTL - time.Nanosecond)
	if !mustHas(t, p, tenantA, featureF) {
		t.Error("Has un nanosegundo antes del vencimiento = false: la entrada aún valía (true)")
	}
	clock.Advance(time.Nanosecond)
	if mustHas(t, p, tenantA, featureF) {
		t.Error("Has en el instante del vencimiento = true: la entrada ya no valía y debía re-consultar (false)")
	}
	if n := fdb.count(queryOverride); n != 2 {
		t.Errorf("hubo %d consultas de override; quería 2 (la inicial y la del vencimiento)", n)
	}
}

// TestPostgres_Has_ErrorIsNotCached: con la BD fallando, Has devuelve (false, err); la llamada
// siguiente vuelve a consultar.
func TestPostgres_Has_ErrorIsNotCached(t *testing.T) {
	p, fdb, _ := newPostgres(t)
	fdb.set(func(f *fakeDB) {
		f.fail[queryOverride] = errBoom
		f.overrides[pair{tenantA, featureF}] = true
	})
	has, err := p.Has(context.Background(), tenantA, featureF)
	if err == nil || has {
		t.Fatalf("Has con la BD fallando = (%v, %v); quería (false, error)", has, err)
	}
	fdb.set(func(f *fakeDB) { delete(f.fail, queryOverride) })
	if !mustHas(t, p, tenantA, featureF) {
		t.Error("Has tras un error = false: el error no debía cachearse y la BD ya contesta true")
	}
}

// TestPostgres_Has_ErrorTexts: cada fallo de la BD sale envuelto, con su texto literal.
func TestPostgres_Has_ErrorTexts(t *testing.T) {
	cases := []struct {
		name   string
		kind   queryKind
		prefix string
	}{
		{"overrideQueryFails", queryOverride, "entitlements: leer override de feature: "},
		{"planQueryFails", queryPlanFeature, "entitlements: resolver feature del plan: "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, fdb, _ := newPostgres(t)
			fdb.set(func(f *fakeDB) { f.fail[c.kind] = errBoom })
			has, err := p.Has(context.Background(), tenantA, featureF)
			if has {
				t.Error("Has con error = true; un error nunca concede la feature")
			}
			if !errors.Is(err, errBoom) {
				t.Errorf("Has: err = %v; quería que envolviera el error de la BD", err)
			}
			if err == nil || !strings.HasPrefix(err.Error(), c.prefix) {
				t.Errorf("Has: err = %v; quería el prefijo literal %q", err, c.prefix)
			}
		})
	}
}
