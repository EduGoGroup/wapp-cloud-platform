//go:build pendiente

package fleet

// Los tests de fichero del eje de perfil en fleet.PostgresRepository: SetProfile (la única
// escritura que mueve profile_updated_at) y ProfilesByTenant (la foto que alimenta el
// kind:"filters", que no está en fleet.Repository).

import (
	"context"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Las dos sentencias, byte a byte y escritas a mano (ver repository_postgres_test.go).
const (
	sqlSetProfile = "\n" +
		"\t\tUPDATE public.fleet_sessions\n" +
		"\t\tSET profile = $3, profile_updated_at = now(), updated_at = now()\n" +
		"\t\tWHERE tenant_id = $1 AND session_id = $2\n" +
		"\t"
	// ProfilesByTenant: una fila por session_id; si las filas de una sesión discrepan, max() hace
	// ganar a 'passive'; y la versión sale de profile_updated_at, no de updated_at.
	sqlProfilesByTenant = "\n" +
		"\t\tSELECT session_id,\n" +
		"\t\t       max(COALESCE(profile, 'passive')) AS profile,\n" +
		"\t\t       max(profile_updated_at)           AS profile_updated_at\n" +
		"\t\tFROM public.fleet_sessions\n" +
		"\t\tWHERE tenant_id = $1\n" +
		"\t\tGROUP BY session_id\n" +
		"\t\tORDER BY session_id\n" +
		"\t"
)

// TestPostgresRepository_SetProfile_InvalidProfile: un perfil fuera de active|passive se rechaza
// con el centinela y SIN que llegue nada al driver.
func TestPostgresRepository_SetProfile_InvalidProfile(t *testing.T) {
	for _, profile := range []Profile{"", "ACTIVE", "bot", "human"} {
		f := newFixture(t, reply{affected: 1})
		found, err := f.repo.SetProfile(context.Background(), pgTenant, pgSession, profile)
		if found || !errors.Is(err, ErrInvalidProfile) {
			t.Errorf("SetProfile(%q) = (%v, %v), quería (false, ErrInvalidProfile)", profile, found, err)
		}
		requireStatements(t, f.fake)
	}
}

// TestPostgresRepository_SetProfile: la sentencia exacta, el perfil como texto en $3, y found
// según las filas afectadas (cero ⇒ no existe o es de otro tenant; varias ⇒ una fila por Edge).
func TestPostgresRepository_SetProfile(t *testing.T) {
	cases := []struct {
		profile  Profile
		affected int64
		want     bool
	}{
		{ProfileActive, 0, false},
		{ProfileActive, 1, true},
		{ProfilePassive, 3, true},
	}
	for _, c := range cases {
		f := newFixture(t, reply{affected: c.affected})
		found, err := f.repo.SetProfile(context.Background(), pgTenant, pgSession, c.profile)
		if err != nil || found != c.want {
			t.Errorf("SetProfile(%q) con %d filas = (%v, %v), quería (%v, nil)", c.profile, c.affected, found, err, c.want)
		}
		requireStatements(t, f.fake, statement{sqlSetProfile, []driver.Value{pgTenant, pgSession, string(c.profile)}})
	}
}

// TestPostgresRepository_SetProfile_Errors: el fallo del driver y el de leer las filas afectadas
// vuelven envueltos, cada uno con su prefijo, y con found=false.
func TestPostgresRepository_SetProfile_Errors(t *testing.T) {
	cases := []struct {
		name   string
		reply  reply
		prefix string
	}{
		{"driver error", reply{err: errBoom}, "fleet: fijar perfil: "},
		{"rows affected error", reply{affected: 1, affectedErr: errBoom}, "fleet: filas afectadas al fijar perfil: "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, c.reply)
			found, err := f.repo.SetProfile(context.Background(), pgTenant, pgSession, ProfileActive)
			if found {
				t.Error("found = true con error, quería false")
			}
			requireWrapped(t, err, errBoom, c.prefix)
		})
	}
}

// TestPostgresRepository_OnlySetProfileMovesTheAxisClock: de las ocho escrituras del adaptador,
// solo la de SetProfile nombra profile_updated_at. Es toda la garantía de que la versión de
// TenantProfiles avanza solo cuando avanza el contenido (no hay trigger que la imponga).
func TestPostgresRepository_OnlySetProfileMovesTheAxisClock(t *testing.T) {
	if !strings.Contains(sqlSetProfile, "profile_updated_at = now()") {
		t.Errorf("la sentencia de SetProfile no mueve profile_updated_at:\n%s", sqlSetProfile)
	}
	others := map[string]string{
		"MarkOnline": sqlMarkOnline, "MarkOffline": sqlMarkOffline, "MarkLoggedOut": sqlMarkLoggedOut,
		"SetState": sqlSetState, "SetSelfPn": sqlSetSelfPn, "MarkGreeted": sqlMarkGreeted,
		"SaveHealth": sqlSaveHealth,
	}
	for name, query := range others {
		if strings.Contains(query, "profile_updated_at") {
			t.Errorf("la sentencia de %s toca profile_updated_at: solo SetProfile mueve el reloj del eje", name)
		}
	}
}

// TestPostgresRepository_ProfilesByTenant_NoRows: un tenant sin sesiones es una respuesta
// legítima: mapa vacío pero NO nil, versión 0 y sin error. Con la sentencia exacta.
func TestPostgresRepository_ProfilesByTenant_NoRows(t *testing.T) {
	f := newFixture(t)
	tp, err := f.repo.ProfilesByTenant(context.Background(), pgTenant)
	if err != nil {
		t.Fatalf("ProfilesByTenant: error inesperado %v", err)
	}
	if tp.Sessions == nil || len(tp.Sessions) != 0 || tp.Version != 0 {
		t.Errorf("ProfilesByTenant sin filas = %+v, quería Sessions vacío no nil y Version 0", tp)
	}
	requireStatements(t, f.fake, statement{sqlProfilesByTenant, []driver.Value{pgTenant}})
}

// TestPostgresRepository_ProfilesByTenant_Snapshot: la foto trae cada sesión con el perfil que da
// la sentencia (tal cual, también uno desconocido), y la versión es el MÁXIMO de
// profile_updated_at en microsegundos unix, venga en el orden que venga.
func TestPostgresRepository_ProfilesByTenant_Snapshot(t *testing.T) {
	base := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	newest := base.Add(time.Hour + 123456*time.Microsecond)
	f := newFixture(t, reply{rows: [][]driver.Value{
		{"s-1", "active", base},
		{"s-2", "passive", newest},
		{"s-3", "bot", base.Add(time.Minute)},
	}})
	tp, err := f.repo.ProfilesByTenant(context.Background(), pgTenant)
	if err != nil {
		t.Fatalf("ProfilesByTenant: error inesperado %v", err)
	}
	want := TenantProfiles{
		Version:  newest.UnixMicro(),
		Sessions: map[string]Profile{"s-1": ProfileActive, "s-2": ProfilePassive, "s-3": "bot"},
	}
	if !reflect.DeepEqual(tp, want) {
		t.Errorf("ProfilesByTenant = %+v, quería %+v", tp, want)
	}
	if tp.Version%1_000_000 != 123456 {
		t.Errorf("Version = %d perdió el microsegundo, quería que acabara en 123456", tp.Version)
	}
}

// TestPostgresRepository_ProfilesByTenant_Errors: cada fallo vuelve con su prefijo y con
// TenantProfiles cero (sin foto parcial).
//
// El cierre de las filas falla DESPUÉS de una iteración completa, y database/sql lo entrega por
// rows.Err(): por eso sale con el prefijo de la iteración y no con «fleet: cerrar filas de
// perfiles: ».
func TestPostgresRepository_ProfilesByTenant_Errors(t *testing.T) {
	at := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	good := []driver.Value{"s-1", "active", at}
	cases := []struct {
		name   string
		reply  reply
		prefix string
		cause  error
	}{
		{"query fails", reply{err: errBoom}, "fleet: leer perfiles del tenant: ", errBoom},
		{"iteration fails", reply{rows: [][]driver.Value{good}, endErr: errBoom}, "fleet: iterar perfiles del tenant: ", errBoom},
		{"close fails", reply{rows: [][]driver.Value{good}, closeErr: errBoom}, "fleet: iterar perfiles del tenant: ", errBoom},
		{"row does not scan", reply{rows: [][]driver.Value{good, {"s-2", "active", "no-es-una-fecha"}}}, "fleet: escanear perfil de sesión: ", nil},
		{"null clock does not scan", reply{rows: [][]driver.Value{{"s-1", "active", nil}}}, "fleet: escanear perfil de sesión: ", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, c.reply)
			tp, err := f.repo.ProfilesByTenant(context.Background(), pgTenant)
			if !reflect.DeepEqual(tp, TenantProfiles{}) {
				t.Errorf("ProfilesByTenant con error = %+v, quería TenantProfiles cero", tp)
			}
			if err == nil || !strings.HasPrefix(err.Error(), c.prefix) {
				t.Fatalf("err = %v, quería el prefijo %q", err, c.prefix)
			}
			if c.cause != nil && !errors.Is(err, c.cause) {
				t.Errorf("err = %q no envuelve la causa %q", err, c.cause)
			}
		})
	}
}
