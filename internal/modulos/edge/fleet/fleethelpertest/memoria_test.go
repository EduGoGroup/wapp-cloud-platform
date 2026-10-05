//go:build pendiente

package fleethelpertest

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet"
)

// steppingClock devuelve un reloj de mentira que avanza un milisegundo en cada lectura, desde
// un instante fijo. Con él la suite corre sin reloj real (T-16) y dos escrituras nunca caen en
// el mismo instante; el caso contrario, el del mismo microsegundo, lo cubre el test del clamp.
func steppingClock() func() time.Time {
	current := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	return func() time.Time {
		current = current.Add(time.Millisecond)
		return current
	}
}

// TestMemoria_Contrato corre la suite del puerto contra el doble. La misma suite corre contra
// el adaptador Postgres en F9: lo que pasa aquí es lo que los tests del gateway pueden dar por
// bueno cuando usan Memoria en lugar de Postgres.
func TestMemoria_Contrato(t *testing.T) {
	ContratoRepository(t, func(t *testing.T) Montaje {
		t.Helper()
		m := NewMemoria()
		m.now = steppingClock()
		return Montaje{
			Repository: m,
			// El doble no sabe de tenants: uno «sembrado» es un UUID nuevo.
			SeedTenant: func(*testing.T) string { return uuid.NewString() },
			Profiles: func(t *testing.T, tenantID string) fleet.TenantProfiles {
				t.Helper()
				tp, err := m.ProfilesByTenant(context.Background(), tenantID)
				if err != nil {
					t.Fatalf("ProfilesByTenant(%s): error inesperado %v", tenantID, err)
				}
				return tp
			},
		}
	})
}

// TestMemoria_UnknownSession_IsCreatedPassive fija la diferencia documentada con Postgres: el
// doble CREA la fila cuando marca offline o zombie una sesión que nadie registró (allí el
// UPDATE no toca nada). La suite no lo afirma. Y la fila nace PASIVA: con MarkOnline son los
// tres llamantes del perfil por defecto, y ninguno puede dar de alta una sesión que auto-responda.
func TestMemoria_UnknownSession_IsCreatedPassive(t *testing.T) {
	ctx := context.Background()
	writes := []struct {
		name  string
		write func(m *Memoria) error
		want  fleet.State
	}{
		{"MarkOnline", func(m *Memoria) error { return m.MarkOnline(ctx, "t", "e", "s") }, fleet.StateOnline},
		{"MarkOffline", func(m *Memoria) error { return m.MarkOffline(ctx, "t", "e", "s") }, fleet.StateOffline},
		{"MarkLoggedOut", func(m *Memoria) error { return m.MarkLoggedOut(ctx, "t", "e", "s") }, fleet.StateLoggedOut},
	}
	for _, w := range writes {
		t.Run(w.name, func(t *testing.T) {
			m := NewMemoria()
			m.now = steppingClock()
			if err := w.write(m); err != nil {
				t.Fatalf("%s de una sesión desconocida: error inesperado %v", w.name, err)
			}
			s, found, err := m.Get(ctx, "t", "e", "s")
			if err != nil || !found {
				t.Fatalf("Get = (found=%v, err=%v), quería la fila que el doble crea", found, err)
			}
			if s.State != w.want || s.Profile != fleet.ProfilePassive {
				t.Errorf("la fila nació con state=%q profile=%q, quería %q y passive", s.State, s.Profile, w.want)
			}
			if s.LastSeenAt.IsZero() {
				t.Error("la fila nació con last_seen_at a cero")
			}
			tp, err := m.ProfilesByTenant(ctx, "t")
			if err != nil {
				t.Fatalf("ProfilesByTenant: error inesperado %v", err)
			}
			if tp.Sessions["s"] != fleet.ProfilePassive || tp.Version <= 0 {
				t.Errorf("la foto tras el alta es %+v, quería la sesión pasiva y versión > 0", tp)
			}
		})
	}
}

// TestMemoria_ProfileVersion_ClampsWithinTheSameMicrosecond: con el reloj PARADO, cada
// SetProfile sube la versión exactamente en uno. En Postgres dos sentencias tienen dos now()
// distintos; en memoria dos llamadas seguidas pueden caer en el mismo microsegundo, y una
// versión repetida haría que el Edge descartara el segundo cambio. Cuando el reloj avanza, la
// versión vuelve a ser la del reloj.
func TestMemoria_ProfileVersion_ClampsWithinTheSameMicrosecond(t *testing.T) {
	ctx := context.Background()
	frozen := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	current := frozen
	m := NewMemoria()
	m.now = func() time.Time { return current }
	version := func() int64 {
		t.Helper()
		tp, err := m.ProfilesByTenant(ctx, "t")
		if err != nil {
			t.Fatalf("ProfilesByTenant: error inesperado %v", err)
		}
		return tp.Version
	}
	setProfile := func(profile fleet.Profile) {
		t.Helper()
		if found, err := m.SetProfile(ctx, "t", "s", profile); err != nil || !found {
			t.Fatalf("SetProfile(%q) = (found=%v, err=%v), quería (true, nil)", profile, found, err)
		}
	}

	if err := m.MarkOnline(ctx, "t", "e", "s"); err != nil {
		t.Fatalf("MarkOnline: error inesperado %v", err)
	}
	base := frozen.UnixMicro()
	if got := version(); got != base {
		t.Fatalf("versión tras el alta = %d, quería el reloj en microsegundos, %d", got, base)
	}
	setProfile(fleet.ProfileActive)
	setProfile(fleet.ProfilePassive)
	if got := version(); got != base+2 {
		t.Errorf("versión tras dos SetProfile con el reloj parado = %d, quería %d (una más por cambio)", got, base+2)
	}

	// Una reconexión con el reloj parado no la mueve: no es un cambio de perfil.
	if err := m.MarkOnline(ctx, "t", "e", "s"); err != nil {
		t.Fatalf("MarkOnline: error inesperado %v", err)
	}
	if got := version(); got != base+2 {
		t.Errorf("la reconexión movió la versión a %d, quería %d", got, base+2)
	}

	current = frozen.Add(time.Second)
	setProfile(fleet.ProfileActive)
	if got, want := version(), current.UnixMicro(); got != want {
		t.Errorf("versión con el reloj ya avanzado = %d, quería la del reloj, %d", got, want)
	}
}

// TestMemoria_Reads_AreDetachedFromTheStore: List devuelve un slice no nil también cuando no
// hay nada, y lo que entregan List y Get no comparte respaldo con el doble: mutar el mapa o
// los punteros del bloque del worker de una Session leída no cambia lo guardado.
func TestMemoria_Reads_AreDetachedFromTheStore(t *testing.T) {
	ctx := context.Background()
	m := NewMemoria()
	m.now = steppingClock()

	empty, err := m.List(ctx, "t")
	if err != nil {
		t.Fatalf("List: error inesperado %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Errorf("List sin sesiones = %#v, quería un slice vacío no nil", empty)
	}

	if err := m.MarkOnline(ctx, "t", "e", "s"); err != nil {
		t.Fatalf("MarkOnline: error inesperado %v", err)
	}
	stuck := int64(3)
	err = m.SaveHealth(ctx, "t", "e", "s", fleet.HealthSnapshot{
		IntentOmittedByReason: map[string]int64{"breaker": 1}, StuckHeads: &stuck,
	})
	if err != nil {
		t.Fatalf("SaveHealth: error inesperado %v", err)
	}

	listed, err := m.List(ctx, "t")
	if err != nil || len(listed) != 1 {
		t.Fatalf("List = (%d sesiones, err=%v), quería una", len(listed), err)
	}
	got, _, err := m.Get(ctx, "t", "e", "s")
	if err != nil {
		t.Fatalf("Get: error inesperado %v", err)
	}
	for _, s := range []fleet.Session{listed[0], got} {
		s.IntentOmittedByReason["breaker"] = 999
		*s.StuckHeads = 999
	}

	stored, _, err := m.Get(ctx, "t", "e", "s")
	if err != nil {
		t.Fatalf("Get: error inesperado %v", err)
	}
	if stored.IntentOmittedByReason["breaker"] != 1 || stored.StuckHeads == nil || *stored.StuckHeads != 3 {
		t.Errorf("mutar una Session leída cambió lo guardado: motivos=%v stuck_heads=%v", stored.IntentOmittedByReason, stored.StuckHeads)
	}
}

// TestDefaultProfile_OnlyTheEmptyBecomesPassive: el perfil por defecto del doble es el de la
// columna (pasivo, D-07) y solo convierte el vacío: un perfil desconocido pasa intacto, no se
// «arregla» a pasivo ni a activo.
func TestDefaultProfile_OnlyTheEmptyBecomesPassive(t *testing.T) {
	cases := map[fleet.Profile]fleet.Profile{
		"":                   fleet.ProfilePassive,
		fleet.ProfilePassive: fleet.ProfilePassive,
		fleet.ProfileActive:  fleet.ProfileActive,
		"bot":                "bot",
		" ":                  " ",
	}
	for in, want := range cases {
		if got := defaultProfile(in); got != want {
			t.Errorf("defaultProfile(%q) = %q, quería %q", in, got, want)
		}
	}
}
