package runtimehelpertest_test

import (
	"bytes"
	"encoding/base64"
	"testing"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime/runtimehelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// newKeyProvider devuelve un KeyProvider de verdad (el del envelope de PII de negocio, NO la DEK
// del ADR-0007) con una KEK y una clave de índice fijas, derivadas de seed: dos seeds distintas
// son dos despliegues que no comparten clave de índice.
func newKeyProvider(t *testing.T, seed byte) crypto.KeyProvider {
	t.Helper()
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{seed}, 32))
	index := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{seed + 1}, 32))
	kp, err := crypto.NewEnvKeyProvider(crypto.KeyringConfig{MasterB64: key, IndexB64: index})
	if err != nil {
		t.Fatalf("KeyProvider de prueba: %v", err)
	}
	return kp
}

// seedInto devuelve la siembra de un Montaje sobre la tabla en memoria.
func seedInto(fleet *runtimehelpertest.FleetSessions) func(*testing.T, runtimehelpertest.Session) {
	return func(t *testing.T, s runtimehelpertest.Session) {
		t.Helper()
		if err := fleet.Seed(s); err != nil {
			t.Fatalf("sembrar la sesión %+v: %v", s, err)
		}
	}
}

// TestMemoryTenantResolver_Contrato corre la suite del resolver de tenant contra su gemelo en
// memoria, sin BD: cada caso monta una tabla nueva.
func TestMemoryTenantResolver_Contrato(t *testing.T) {
	runtimehelpertest.ContratoTenantResolver(t, func(*testing.T) runtimehelpertest.MontajeTenantResolver {
		fleet := runtimehelpertest.NewFleetSessions()
		return runtimehelpertest.MontajeTenantResolver{
			Resolver:        fleet.TenantResolver(),
			TenantA:         uuid.NewString(),
			TenantB:         uuid.NewString(),
			Seed:            seedInto(fleet),
			AllowAnyProfile: func(*testing.T) { fleet.AllowAnyProfile() },
			Sessions:        func(*testing.T) []runtimehelpertest.Session { return fleet.Rows() },
		}
	})
}

// TestMemorySelfNumbers_Contrato corre la suite de los números propios contra su gemelo en
// memoria, sin BD: cada caso monta una tabla nueva, un gemelo con KeyProvider y otro sin él.
func TestMemorySelfNumbers_Contrato(t *testing.T) {
	kp, other := newKeyProvider(t, 0x11), newKeyProvider(t, 0x77)
	runtimehelpertest.ContratoSelfNumbers(t, func(*testing.T) runtimehelpertest.MontajeSelfNumbers {
		fleet := runtimehelpertest.NewFleetSessions()
		return runtimehelpertest.MontajeSelfNumbers{
			Checker:         fleet.SelfNumbers(kp),
			NoKeyProvider:   fleet.SelfNumbers(nil),
			TenantA:         uuid.NewString(),
			TenantB:         uuid.NewString(),
			BlindIndex:      kp.BlindIndex,
			OtherBlindIndex: other.BlindIndex,
			Seed:            seedInto(fleet),
			AllowAnyProfile: func(*testing.T) { fleet.AllowAnyProfile() },
			Sessions:        func(*testing.T) []runtimehelpertest.Session { return fleet.Rows() },
		}
	})
}

// TestFleetSessions_Seed_ImitatesTheSchema: la tabla en memoria imita lo que del esquema pesa en
// las consultas: el DEFAULT passive del perfil, la sustitución por clave primaria, el CHECK del
// estado y el del perfil (que AllowAnyProfile retira); un Seed rechazado no toca la tabla.
func TestFleetSessions_Seed_ImitatesTheSchema(t *testing.T) {
	fleet := runtimehelpertest.NewFleetSessions()
	base := runtimehelpertest.Session{TenantID: "t", EdgeID: "e", SessionID: "s", State: runtimehelpertest.StateOnline}

	if err := fleet.Seed(base); err != nil {
		t.Fatalf("Seed sin perfil: %v", err)
	}
	if rows := fleet.Rows(); len(rows) != 1 || rows[0].Profile != runtimehelpertest.ProfilePassive {
		t.Fatalf("una sesión sembrada sin perfil quedó como %v, quería una fila passive", rows)
	}

	again := base
	again.State, again.Profile, again.SelfPnBidx = runtimehelpertest.StateOffline, runtimehelpertest.ProfileActive, "indice"
	if err := fleet.Seed(again); err != nil {
		t.Fatalf("Seed sobre la misma clave: %v", err)
	}
	if rows := fleet.Rows(); len(rows) != 1 || rows[0] != again {
		t.Fatalf("sembrar la misma clave dejó %v, quería solo %v", rows, again)
	}

	rejected := []struct {
		name    string
		session runtimehelpertest.Session
	}{
		{"empty tenant", runtimehelpertest.Session{EdgeID: "e", SessionID: "s", State: runtimehelpertest.StateOnline}},
		{"empty edge", runtimehelpertest.Session{TenantID: "t", SessionID: "s", State: runtimehelpertest.StateOnline}},
		{"empty session", runtimehelpertest.Session{TenantID: "t", EdgeID: "e", State: runtimehelpertest.StateOnline}},
		{"state out of domain", runtimehelpertest.Session{TenantID: "t", EdgeID: "e", SessionID: "otra", State: "paused"}},
		{"profile out of domain", runtimehelpertest.Session{TenantID: "t", EdgeID: "e", SessionID: "otra", State: runtimehelpertest.StateOnline, Profile: "supervisor"}},
	}
	for _, c := range rejected {
		if err := fleet.Seed(c.session); err == nil {
			t.Errorf("%s: Seed lo aceptó", c.name)
		}
	}
	if rows := fleet.Rows(); len(rows) != 1 || rows[0] != again {
		t.Fatalf("un Seed rechazado tocó la tabla: %v", rows)
	}

	fleet.AllowAnyProfile()
	if err := fleet.Seed(rejected[4].session); err != nil {
		t.Errorf("tras AllowAnyProfile, Seed rechazó un perfil fuera de dominio: %v", err)
	}
	if err := fleet.Seed(rejected[3].session); err == nil {
		t.Error("AllowAnyProfile retiró también el CHECK del estado")
	}
}

// TestFleetSessions_Rows_SortedCopies: Rows ordena por tenant, edge y sesión y devuelve una copia.
func TestFleetSessions_Rows_SortedCopies(t *testing.T) {
	fleet := runtimehelpertest.NewFleetSessions()
	for _, key := range [][3]string{{"b", "e1", "s1"}, {"a", "e2", "s1"}, {"a", "e1", "s2"}, {"a", "e1", "s1"}} {
		if err := fleet.Seed(runtimehelpertest.Session{TenantID: key[0], EdgeID: key[1], SessionID: key[2], State: runtimehelpertest.StateOnline}); err != nil {
			t.Fatalf("Seed(%v): %v", key, err)
		}
	}
	rows := fleet.Rows()
	want := [][3]string{{"a", "e1", "s1"}, {"a", "e1", "s2"}, {"a", "e2", "s1"}, {"b", "e1", "s1"}}
	if len(rows) != len(want) {
		t.Fatalf("Rows devolvió %d filas, quería %d", len(rows), len(want))
	}
	for i, w := range want {
		if got := [3]string{rows[i].TenantID, rows[i].EdgeID, rows[i].SessionID}; got != w {
			t.Errorf("fila %d = %v, quería %v", i, got, w)
		}
	}
	rows[0].State = "tocada por fuera"
	if fleet.Rows()[0].State != runtimehelpertest.StateOnline {
		t.Error("Rows entrega la tabla, no una copia")
	}
}
