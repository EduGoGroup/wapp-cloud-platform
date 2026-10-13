package entitlementshelpertest

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestFake_Contrato corre la suite del puerto contra el Fake.
//
// El Fake guarda el resultado YA RESUELTO, así que su Seed (fakeSeed) aplica la resolución de
// ADR-0022 al sembrar. Contra el Fake, la suite prueba la mecánica del doble —orden, apagadas
// fuera de la lista, error propagado, TTL, lecturas en paralelo—; las reglas de resolución en sí
// (override en los dos sentidos, plan NULL, tenant inexistente) las prueba de verdad la misma suite
// contra entitlements.Postgres en F9.
func TestFake_Contrato(t *testing.T) {
	ContratoResolver(t, func(t *testing.T) Montaje {
		t.Helper()
		const ttl = 7 * time.Second
		seed := &fakeSeed{
			fake:         &Fake{TTL: ttl},
			tenants:      []string{uuid.NewString(), uuid.NewString()},
			planFeatures: make(map[string][]string),
			plans:        make(map[string]string),
			overrides:    make(map[string]map[string]bool),
		}
		missing := uuid.NewString()
		// El Fake no sabe de tenants inexistentes: se declara uno sin plan ni features.
		seed.fake.SetPlan(missing, "")
		seed.project()
		return Montaje{
			Resolver:      seed.fake,
			TenantA:       seed.tenants[0],
			TenantB:       seed.tenants[1],
			MissingTenant: missing,
			TTL:           ttl,
			Seed:          seed,
		}
	})
}

// fakeSeed guarda las «tablas» (plan_features, tenants.plan_id, tenant_features) y, tras cada
// siembra, proyecta sobre el Fake el resultado resuelto de los tenants existentes.
type fakeSeed struct {
	fake         *Fake
	tenants      []string
	planFeatures map[string][]string        // plan → features
	plans        map[string]string          // tenant → plan; ausente o "" ⇒ NULL
	overrides    map[string]map[string]bool // tenant → feature → enabled
}

func (s *fakeSeed) AddPlanFeatures(_ *testing.T, plan string, features ...string) {
	s.planFeatures[plan] = append(s.planFeatures[plan], features...)
	s.project()
}

func (s *fakeSeed) SetPlan(_ *testing.T, tenantID, plan string) {
	s.plans[tenantID] = plan
	s.project()
}

func (s *fakeSeed) SetOverride(_ *testing.T, tenantID, feature string, enabled bool) {
	if s.overrides[tenantID] == nil {
		s.overrides[tenantID] = make(map[string]bool)
	}
	s.overrides[tenantID][feature] = enabled
	s.project()
}

func (s *fakeSeed) BreakInfra(_ *testing.T) {
	s.fake.Err = errors.New("fakeSeed: infraestructura caída")
}

// project rehace el estado de cada tenant existente: plan NULL ⇒ basic, las del plan encendidas y
// los overrides encima, en los dos sentidos.
func (s *fakeSeed) project() {
	for _, tenant := range s.tenants {
		plan := s.plans[tenant]
		if plan == "" {
			plan = "basic"
		}
		s.fake.SetPlan(tenant, plan)
		delete(s.fake.Enabled, tenant)
		for _, feature := range s.planFeatures[plan] {
			s.fake.Enable(tenant, feature)
		}
		for feature, enabled := range s.overrides[tenant] {
			if enabled {
				s.fake.Enable(tenant, feature)
			} else {
				s.fake.Disable(tenant, feature)
			}
		}
	}
}

// TestFake_ZeroValueIsUsable: &Fake{} se puebla sin NewFake (los mapas se crean al primer uso) y
// responde el TTL por defecto, el de entitlements.Postgres.
func TestFake_ZeroValueIsUsable(t *testing.T) {
	f := &Fake{}
	f.Enable("t1", "a")
	f.SetPlan("t1", "pro")
	if has, err := f.Has(context.Background(), "t1", "a"); err != nil || !has {
		t.Errorf("Has sobre el valor cero poblado = (%v, %v), quería (true, nil)", has, err)
	}
	if got := f.CacheTTL(); got != 60*time.Second {
		t.Errorf("CacheTTL() sin TTL = %v, quería 60s (el valor por defecto de entitlements.Postgres)", got)
	}
	if got := (&Fake{TTL: -time.Second}).CacheTTL(); got != 60*time.Second {
		t.Errorf("CacheTTL() con TTL negativo = %v, quería 60s", got)
	}
}

// TestFake_UnknownTenantIsBasic fija la diferencia documentada con Postgres: un tenant que el Fake
// no conoce se lista como 'basic' sin features, no como inexistente.
func TestFake_UnknownTenantIsBasic(t *testing.T) {
	plan, features, err := NewFake().ListEffective(context.Background(), "desconocido")
	if err != nil || plan != "basic" || len(features) != 0 {
		t.Errorf("ListEffective(desconocido) = (%q, %v, %v), quería (basic, [], nil)", plan, features, err)
	}
}

// TestFake_DisableIsRecorded: Disable no es «no declararla»: deja la clave registrada en false,
// fuera de la lista, aunque antes estuviera encendida.
func TestFake_DisableIsRecorded(t *testing.T) {
	f := NewFake()
	f.Enable("t1", "a")
	f.Enable("t1", "b")
	f.Disable("t1", "a")
	if enabled, ok := f.Enabled["t1"]["a"]; !ok || enabled {
		t.Errorf("Enabled[t1][a] = (%v, registrada=%v), quería false registrada", enabled, ok)
	}
	_, features, err := f.ListEffective(context.Background(), "t1")
	if err != nil || !slices.Equal(features, []string{"b"}) {
		t.Errorf("ListEffective(t1) = (%v, %v), quería ([b], nil)", features, err)
	}
}
