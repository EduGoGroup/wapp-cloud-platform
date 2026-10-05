package fleethelpertest

// Los casos del eje `profile` (Plan 046): una sesión nace PASIVA, su perfil solo lo mueve
// SetProfile, y la foto del tenant —la que se empuja al Edge como kind:"filters"— es completa y
// su versión avanza cuando avanza el mapa, no cuando se toca la fila por otro motivo (0065).

import (
	"context"
	"errors"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet"
)

// caseBornPassive: una sesión recién registrada nace pasiva (privacidad por defecto, D-07): no
// auto-responde hasta que su dueño la active.
func caseBornPassive(t *testing.T, m Montaje) {
	tenant, edge, session := seedTenant(t, m), unique("edge"), unique("session")
	markOnline(t, m, tenant, edge, session)
	requireProfile(t, m, tenant, edge, session, fleet.ProfilePassive)
	if got := profiles(t, m, tenant).Sessions[session]; got != fleet.ProfilePassive {
		t.Errorf("la foto dice %q para una sesión recién nacida, quería passive", got)
	}
}

// caseProfileSurvivesReconnect: reconectar no es cambiar de perfil. Vale para los dos: la
// activa no vuelve a pasiva (dejaría mudo al negocio) y la pasiva no pasa a activa.
func caseProfileSurvivesReconnect(t *testing.T, m Montaje) {
	tenant, edge := seedTenant(t, m), unique("edge")
	active, passive := unique("session"), unique("session")
	markOnline(t, m, tenant, edge, active)
	markOnline(t, m, tenant, edge, passive)
	setProfile(t, m, tenant, active, fleet.ProfileActive)
	setProfile(t, m, tenant, passive, fleet.ProfilePassive)

	markOnline(t, m, tenant, edge, active)
	markOnline(t, m, tenant, edge, passive)
	requireProfile(t, m, tenant, edge, active, fleet.ProfileActive)
	requireProfile(t, m, tenant, edge, passive, fleet.ProfilePassive)
}

// caseProfileSurvivesStates: el estado del link no es el perfil. Ni la caída, ni el zombie, ni
// un SetState de admin, ni la vuelta a online lo mueven.
func caseProfileSurvivesStates(t *testing.T, m Montaje) {
	tenant, edge, session := seedTenant(t, m), unique("edge"), unique("session")
	markOnline(t, m, tenant, edge, session)
	setProfile(t, m, tenant, session, fleet.ProfileActive)

	markOffline(t, m, tenant, edge, session)
	requireProfile(t, m, tenant, edge, session, fleet.ProfileActive)
	markLoggedOut(t, m, tenant, edge, session)
	requireProfile(t, m, tenant, edge, session, fleet.ProfileActive)
	setState(t, m, tenant, session, fleet.StateOffline)
	requireProfile(t, m, tenant, edge, session, fleet.ProfileActive)
	markOnline(t, m, tenant, edge, session)
	requireProfile(t, m, tenant, edge, session, fleet.ProfileActive)
}

// caseSetProfileInvalid: solo active|passive. El vacío, los valores del eje retirado y otra
// caja dan ErrInvalidProfile, found=false, y no cambian ni el perfil ni la versión de la foto.
func caseSetProfileInvalid(t *testing.T, m Montaje) {
	tenant, edge, session := seedTenant(t, m), unique("edge"), unique("session")
	markOnline(t, m, tenant, edge, session)
	setProfile(t, m, tenant, session, fleet.ProfileActive)
	before := profiles(t, m, tenant)

	for _, profile := range []fleet.Profile{"", "bot", "human", "ACTIVE"} {
		found, err := m.Repository.SetProfile(context.Background(), tenant, session, profile)
		if !errors.Is(err, fleet.ErrInvalidProfile) {
			t.Errorf("SetProfile(%q): err = %v, quería ErrInvalidProfile", profile, err)
		}
		if found {
			t.Errorf("SetProfile(%q): found = true con un perfil inválido", profile)
		}
	}
	requireProfile(t, m, tenant, edge, session, fleet.ProfileActive)
	if after := profiles(t, m, tenant); after.Version != before.Version {
		t.Errorf("un SetProfile rechazado movió la versión de %d a %d", before.Version, after.Version)
	}
}

// caseSetProfileUnknown: una sesión que no existe da found=false sin error, y no nace.
func caseSetProfileUnknown(t *testing.T, m Montaje) {
	tenant, session := seedTenant(t, m), unique("session")
	found, err := m.Repository.SetProfile(context.Background(), tenant, session, fleet.ProfileActive)
	if err != nil || found {
		t.Fatalf("SetProfile de una sesión desconocida = (found=%v, err=%v), quería (false, nil)", found, err)
	}
	if tp := profiles(t, m, tenant); len(tp.Sessions) != 0 || tp.Version != 0 {
		t.Errorf("SetProfile de una sesión desconocida dejó la foto %+v, quería vacía y versión 0", tp)
	}
}

// caseSetProfileAllRows: SetProfile acota por tenant + session, así que mueve TODAS las filas
// de esa sesión (una por Edge) y ninguna de otra sesión.
func caseSetProfileAllRows(t *testing.T, m Montaje) {
	tenant, session, other := seedTenant(t, m), unique("session"), unique("session")
	edgeOne, edgeTwo := unique("edge"), unique("edge")
	markOnline(t, m, tenant, edgeOne, session)
	markOnline(t, m, tenant, edgeTwo, session)
	markOnline(t, m, tenant, edgeOne, other)

	setProfile(t, m, tenant, session, fleet.ProfileActive)
	requireProfile(t, m, tenant, edgeOne, session, fleet.ProfileActive)
	requireProfile(t, m, tenant, edgeTwo, session, fleet.ProfileActive)
	requireProfile(t, m, tenant, edgeOne, other, fleet.ProfilePassive)
}

// caseSetProfileOtherTenant: la sesión de otro tenant no se encuentra (404 opaco) ni se toca:
// ni su perfil ni la versión de su foto.
func caseSetProfileOtherTenant(t *testing.T, m Montaje) {
	owner, stranger := seedTwoTenants(t, m)
	edge, session := unique("edge"), unique("session")
	markOnline(t, m, owner, edge, session)
	before := profiles(t, m, owner)

	found, err := m.Repository.SetProfile(context.Background(), stranger, session, fleet.ProfileActive)
	if err != nil || found {
		t.Fatalf("SetProfile desde otro tenant = (found=%v, err=%v), quería (false, nil)", found, err)
	}
	requireProfile(t, m, owner, edge, session, fleet.ProfilePassive)
	if after := profiles(t, m, owner); after.Version != before.Version {
		t.Errorf("un SetProfile desde otro tenant movió la versión del dueño de %d a %d", before.Version, after.Version)
	}
}

// caseProfilesFullPhoto: la foto trae TODAS las sesiones, activas incluidas, estén online o
// no. Una sesión ausente el Edge la asume activa (fail-open): omitir las activas coincidiría
// por casualidad hoy y mentiría el día que una pase de pasiva a activa.
func caseProfilesFullPhoto(t *testing.T, m Montaje) {
	tenant, edge := seedTenant(t, m), unique("edge")
	active, passive, offline := unique("session"), unique("session"), unique("session")
	for _, session := range []string{active, passive, offline} {
		markOnline(t, m, tenant, edge, session)
	}
	setProfile(t, m, tenant, active, fleet.ProfileActive)
	setProfile(t, m, tenant, offline, fleet.ProfileActive)
	markOffline(t, m, tenant, edge, offline)

	want := map[string]fleet.Profile{
		active: fleet.ProfileActive, passive: fleet.ProfilePassive, offline: fleet.ProfileActive,
	}
	requirePhoto(t, profiles(t, m, tenant), want)
}

// caseProfilesDiscordant: una sesión con una fila por Edge es UNA entrada de la foto, y si las
// filas discrepan gana passive: la lectura segura es «no auto-responde».
func caseProfilesDiscordant(t *testing.T, m Montaje) {
	tenant, session := seedTenant(t, m), unique("session")
	edgeOne, edgeTwo := unique("edge"), unique("edge")
	markOnline(t, m, tenant, edgeOne, session)
	setProfile(t, m, tenant, session, fleet.ProfileActive)
	// La misma sesión aparece bajo otro Edge DESPUÉS de activarse: esa fila nace pasiva.
	markOnline(t, m, tenant, edgeTwo, session)
	requireProfile(t, m, tenant, edgeOne, session, fleet.ProfileActive)
	requireProfile(t, m, tenant, edgeTwo, session, fleet.ProfilePassive)

	requirePhoto(t, profiles(t, m, tenant), map[string]fleet.Profile{session: fleet.ProfilePassive})

	// Y deja de discrepar en cuanto SetProfile las iguala.
	setProfile(t, m, tenant, session, fleet.ProfileActive)
	requirePhoto(t, profiles(t, m, tenant), map[string]fleet.Profile{session: fleet.ProfileActive})
}

// caseProfilesEmpty: un tenant sin sesiones tiene una foto vacía —mapa no nil— con versión 0.
// No es un error: se empuja igual.
func caseProfilesEmpty(t *testing.T, m Montaje) {
	tp := profiles(t, m, seedTenant(t, m))
	if len(tp.Sessions) != 0 {
		t.Errorf("la foto de un tenant sin sesiones trae %v", tp.Sessions)
	}
	if tp.Version != 0 {
		t.Errorf("la foto de un tenant sin sesiones tiene versión %d, quería 0", tp.Version)
	}
}

// caseProfilesOtherTenant: la foto es del tenant pedido. Las sesiones de otro no aparecen, y
// lo que el otro haga con sus perfiles no mueve la versión de este.
func caseProfilesOtherTenant(t *testing.T, m Montaje) {
	owner, stranger := seedTwoTenants(t, m)
	edge, mine, theirs := unique("edge"), unique("session"), unique("session")
	markOnline(t, m, owner, edge, mine)
	before := profiles(t, m, owner)

	markOnline(t, m, stranger, edge, theirs)
	setProfile(t, m, stranger, theirs, fleet.ProfileActive)

	after := profiles(t, m, owner)
	requirePhoto(t, after, map[string]fleet.Profile{mine: fleet.ProfilePassive})
	if after.Version != before.Version {
		t.Errorf("la actividad de otro tenant movió la versión de %d a %d", before.Version, after.Version)
	}
	requirePhoto(t, profiles(t, m, stranger), map[string]fleet.Profile{theirs: fleet.ProfileActive})
}

// caseVersionPositive: el alta de la primera sesión fija el reloj del eje: la versión deja de
// ser 0, que significa «ni una fila».
func caseVersionPositive(t *testing.T, m Montaje) {
	tenant := seedTenant(t, m)
	markOnline(t, m, tenant, unique("edge"), unique("session"))
	if tp := profiles(t, m, tenant); tp.Version <= 0 {
		t.Errorf("versión = %d con una sesión dada de alta, quería > 0", tp.Version)
	}
}

// caseVersionIgnoresNoise es la regla de la migración 0065: la versión la mueve SOLO
// SetProfile. Todas las escrituras del día a día que no son un cambio de perfil la dejan
// quieta, y el mapa también.
//
// 🔴 Si nace una escritura nueva en fleet.Repository, va a writeRowNoise: esta lista es la
// única definición ejecutable de «ruido». La exclusividad no la impone el motor (no hay
// trigger), es de código.
func caseVersionIgnoresNoise(t *testing.T, m Montaje) {
	tenant, edge := seedTenant(t, m), unique("edge")
	first, second := unique("session"), unique("session")
	markOnline(t, m, tenant, edge, first)
	markOnline(t, m, tenant, edge, second)
	setProfile(t, m, tenant, first, fleet.ProfileActive)
	before := profiles(t, m, tenant)

	writeRowNoise(t, m, tenant, edge, first, second)

	after := profiles(t, m, tenant)
	if after.Version != before.Version {
		t.Errorf("la versión se movió de %d a %d sin que cambiara ningún perfil: una escritura que no es SetProfile toca el reloj del eje",
			before.Version, after.Version)
	}
	requirePhoto(t, after, map[string]fleet.Profile{first: fleet.ProfileActive, second: fleet.ProfilePassive})
}

// writeRowNoise ejecuta sobre dos sesiones vivas todas las escrituras del puerto que NO son un
// cambio de perfil: reconexión, salud (sana y degradada), número propio, estado de admin, caída
// y zombie.
func writeRowNoise(t *testing.T, m Montaje, tenant, edge, first, second string) {
	t.Helper()
	markOnline(t, m, tenant, edge, first) // reconexión
	saveHealth(t, m, tenant, edge, first, fleet.HealthSnapshot{WhatsappState: "connected"})
	saveHealth(t, m, tenant, edge, first, fleet.HealthSnapshot{WhatsappState: "degraded", DegradedReason: "dek_load_timeout"})
	setSelfPn(t, m, tenant, edge, first, "+34600000000")
	setState(t, m, tenant, first, fleet.StateOffline)
	markOffline(t, m, tenant, edge, first)
	markLoggedOut(t, m, tenant, edge, second)
	setState(t, m, tenant, second, fleet.StateLoggedOut)
	markOnline(t, m, tenant, edge, second)
}

// caseVersionGrows: cada SetProfile sube la versión, estrictamente. Por eso la versión es un
// reloj y no un hash del mapa: active→passive→active deja el mapa como estaba y aun así son
// tres versiones distintas y crecientes, y el Edge no descarta la última.
func caseVersionGrows(t *testing.T, m Montaje) {
	tenant, session := seedTenant(t, m), unique("session")
	markOnline(t, m, tenant, unique("edge"), session)
	last := profiles(t, m, tenant).Version

	for _, profile := range []fleet.Profile{fleet.ProfileActive, fleet.ProfilePassive, fleet.ProfileActive} {
		setProfile(t, m, tenant, session, profile)
		tp := profiles(t, m, tenant)
		if tp.Version <= last {
			t.Errorf("SetProfile(%q) dejó la versión en %d, quería mayor que %d", profile, tp.Version, last)
		}
		last = tp.Version
		requirePhoto(t, tp, map[string]fleet.Profile{session: profile})
	}
}

// requirePhoto afirma que la foto trae exactamente esas sesiones con esos perfiles.
func requirePhoto(t *testing.T, tp fleet.TenantProfiles, want map[string]fleet.Profile) {
	t.Helper()
	if len(tp.Sessions) != len(want) {
		t.Errorf("la foto trae %d sesiones, quería %d: %v", len(tp.Sessions), len(want), tp.Sessions)
	}
	for session, profile := range want {
		got, ok := tp.Sessions[session]
		if !ok {
			t.Errorf("la foto no trae la sesión %s: una ausente el Edge la asume activa", session)
			continue
		}
		if got != profile {
			t.Errorf("la foto dice %q para la sesión %s, quería %q", got, session, profile)
		}
	}
}
