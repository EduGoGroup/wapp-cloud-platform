package fleet_test

import (
	"context"
	"errors"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
)

// TestMemoryProfilesByTenant_SoloSetProfileMueveLaVersion es, en el doble en memoria,
// la regla que en Postgres sostiene la columna profile_updated_at (migración 0065):
// la versión del kind:"filters" avanza cuando avanza EL MAPA, y no cuando se toca la
// fila por cualquier otro motivo.
//
// 🔴 Antes de la 0065 la versión salía de `updated_at`, que mueve CUALQUIER escritura.
// MarkOnline corre inmediatamente antes de pushConfigsOnConnect, así que un Edge con N
// sesiones del tenant publicaba N versiones distintas y crecientes CON EL MAPA
// IDÉNTICO en cada reconexión; el Edge las re-aplicaba, las re-persistía, y las que
// llegaran desordenadas le hacían emitir su WARN «versión anterior o igual» en
// operación normal — enterrando la única línea que delataría una anomalía real.
//
// Este test vigila el doble; su gemelo contra Postgres real es
// TestIntegration_ProfilesByTenant_ElRuidoDeLaFilaNoMueveLaVersion. Los dos hacen
// falta: la regla NO la impone el motor (no hay trigger), es de código, y si el doble
// no la espejara los tests en memoria mentirían sobre producción.
func TestMemoryProfilesByTenant_SoloSetProfileMueveLaVersion(t *testing.T) {
	t.Parallel()
	repo := fleet.NewMemoryRepository()
	ctx := context.Background()

	for _, s := range []string{"s1", "s2"} {
		if err := repo.MarkOnline(ctx, "t", "edge-1", s); err != nil {
			t.Fatalf("MarkOnline %s: %v", s, err)
		}
	}
	tp0 := leerPerfiles(t, repo, "t", "alta")
	if tp0.Version == 0 {
		t.Fatal("version = 0 con dos sesiones dadas de alta: el alta fija el reloj del eje")
	}

	escribirRuidoEnMemoria(t, repo)

	tp1 := leerPerfiles(t, repo, "t", "tras el ruido")
	if tp1.Version != tp0.Version {
		t.Fatalf("la version se movió sin que cambiara ningún perfil: %d -> %d. "+
			"Alguien está tocando el reloj del eje desde una escritura que no es SetProfile",
			tp0.Version, tp1.Version)
	}

	// Y el cambio de perfil SÍ la mueve, estrictamente hacia arriba.
	activarPerfil(t, repo, "t", "s1", "el que sí debe mover")
	tp2 := leerPerfiles(t, repo, "t", "tras SetProfile")
	if tp2.Version <= tp1.Version {
		t.Fatalf("un cambio de perfil NO movió la version: %d -> %d", tp1.Version, tp2.Version)
	}
}

// repoDePerfiles es lo MÍNIMO que los helpers de este paquete de test necesitan de un
// repositorio: leer la foto del eje `profile` y mover un perfil. La cumplen tanto
// *fleet.MemoryRepository como *fleet.PostgresRepository, y por eso los tests del doble
// en memoria y los de Postgres real comparten helper — que es lo que los hace GEMELOS de
// verdad y no dos tests que se parecen. Si mañana divergieran, divergirían a la vista.
type repoDePerfiles interface {
	ProfilesByTenant(ctx context.Context, tenantID string) (fleet.TenantProfiles, error)
	SetProfile(ctx context.Context, tenantID, sessionID string, profile fleet.Profile) (bool, error)
}

// leerPerfiles lee la foto de perfiles del tenant y aborta si falla. Está extraída —y no
// en línea— porque los `if err != nil` de cada lectura le suben la complejidad ciclómatica
// al test madre por encima del límite de gocyclo (15) SIN añadir nada que se lea. Ojo:
// subtests inline NO la bajarían, porque los FuncLit anidados se le imputan igual a la
// función madre; hace falta una función NOMBRADA. Lo que queda arriba es la regla, desnuda.
func leerPerfiles(t *testing.T, repo repoDePerfiles, tenantID, etapa string) fleet.TenantProfiles {
	t.Helper()
	tp, err := repo.ProfilesByTenant(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("ProfilesByTenant (%s): %v", etapa, err)
	}
	return tp
}

// activarPerfil pone la sesión en `active` y exige que la fila EXISTIERA: un found=false
// silencioso convertiría «la version no se movió porque el UPDATE no tocó nada» en un
// falso verde del test de monotonicidad, que es exactamente la conclusión contraria.
func activarPerfil(t *testing.T, repo repoDePerfiles, tenantID, sessionID, etapa string) {
	t.Helper()
	found, err := repo.SetProfile(context.Background(), tenantID, sessionID, fleet.ProfileActive)
	if err != nil || !found {
		t.Fatalf("SetProfile %s (%s): found=%v err=%v", sessionID, etapa, found, err)
	}
}

// escribirRuidoEnMemoria ejecuta sobre una sesión viva TODAS las escrituras que fleet hace
// en el día a día y que NO son un cambio de perfil. Es, literalmente, la lista que la regla
// de la migración 0065 tiene que sobrevivir. Su gemela contra Postgres real es
// escribirRuidoEnPostgres, en profiles_integration_test.go.
//
// 🔴 SI NACE UNA ESCRITURA NUEVA SOBRE fleet_sessions, VA AQUÍ Y EN SU GEMELA. Esta lista es
// la única definición ejecutable de «ruido» que tenemos: la exclusividad de profile_updated_at
// no la impone el motor (no hay trigger), así que una escritura nueva que tocara el perfil sin
// pasar por SetProfile no pondría este test en rojo — simplemente dejaría de estar vigilada.
func escribirRuidoEnMemoria(t *testing.T, repo fleet.Repository) {
	t.Helper()
	ctx := context.Background()
	if err := repo.MarkOnline(ctx, "t", "edge-1", "s1"); err != nil { // reconexión
		t.Fatalf("reconexión: %v", err)
	}
	if err := repo.SaveHealth(ctx, "t", "edge-1", "s1", fleet.HealthSnapshot{}); err != nil {
		t.Fatalf("SaveHealth: %v", err)
	}
	if err := repo.SetSelfPn(ctx, "t", "edge-1", "s1", "+34600000000"); err != nil {
		t.Fatalf("SetSelfPn: %v", err)
	}
	if _, err := repo.SetState(ctx, "t", "s1", fleet.StateOffline); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	if err := repo.MarkOffline(ctx, "t", "edge-1", "s1"); err != nil {
		t.Fatalf("MarkOffline: %v", err)
	}
	if err := repo.MarkLoggedOut(ctx, "t", "edge-1", "s2"); err != nil {
		t.Fatalf("MarkLoggedOut: %v", err)
	}
}

func TestMemoryOnlineThenOffline(t *testing.T) {
	t.Parallel()
	repo := fleet.NewMemoryRepository()
	ctx := context.Background()

	if err := repo.MarkOnline(ctx, "t", "edge-1", "s1"); err != nil {
		t.Fatalf("MarkOnline: %v", err)
	}

	s, found, err := repo.Get(ctx, "t", "edge-1", "s1")
	if err != nil || !found {
		t.Fatalf("Get: found=%v err=%v", found, err)
	}
	if s.State != fleet.StateOnline {
		t.Fatalf("estado: got %q, want online", s.State)
	}
	if s.LastConnectedAt.IsZero() || s.LastSeenAt.IsZero() {
		t.Fatal("last_connected_at/last_seen_at deberían estar poblados")
	}

	if err := repo.MarkOffline(ctx, "t", "edge-1", "s1"); err != nil {
		t.Fatalf("MarkOffline: %v", err)
	}
	s, _, err = repo.Get(ctx, "t", "edge-1", "s1")
	if err != nil {
		t.Fatalf("Get tras offline: %v", err)
	}
	if s.State != fleet.StateOffline {
		t.Fatalf("estado: got %q, want offline", s.State)
	}
}

func TestMemoryOfflineUnknownIsNoError(t *testing.T) {
	t.Parallel()
	repo := fleet.NewMemoryRepository()
	if err := repo.MarkOffline(context.Background(), "t", "e", "missing"); err != nil {
		t.Fatalf("MarkOffline de sesión inexistente no debería fallar: %v", err)
	}
}

// getSession lee la sesión y falla el test si no está o hay error. Toma la interfaz
// Repository ⇒ sirve a la impl en memoria y a la de Postgres (integración).
// Para el llamante que YA tiene un ctx en mano está getSessionCtx: propagarlo es lo
// que exige contextcheck, y además hace la lectura cancelable con el resto del test.
func getSession(t *testing.T, repo fleet.Repository, tenantID, edgeID, sessionID string) fleet.Session {
	t.Helper()
	return getSessionCtx(context.Background(), t, repo, tenantID, edgeID, sessionID)
}

// getSessionCtx es getSession propagando el contexto del llamante.
func getSessionCtx(ctx context.Context, t *testing.T, repo fleet.Repository, tenantID, edgeID, sessionID string) fleet.Session {
	t.Helper()
	s, found, err := repo.Get(ctx, tenantID, edgeID, sessionID)
	if err != nil || !found {
		t.Fatalf("Get(%s): found=%v err=%v", sessionID, found, err)
	}
	return s
}

// snapshotMatches compara los campos escalares del snapshot de salud persistido.
func snapshotMatches(s fleet.Session, w fleet.HealthSnapshot) bool {
	return s.WhatsappState == w.WhatsappState && s.DegradedReason == w.DegradedReason &&
		s.LastEventAgeS == w.LastEventAgeS && s.DekLoadDurationMs == w.DekLoadDurationMs &&
		s.IntentCircuit == w.IntentCircuit && s.OutboxDepth == w.OutboxDepth &&
		s.BinaryVersion == w.BinaryVersion && s.UptimeS == w.UptimeS
}

// TestMemorySaveHealthDegradedTransitions verifica la marca degraded_since: se fija
// al ENTRAR en degradado y se limpia al SALIR (Plan 031 · T3).
func TestMemorySaveHealthDegradedTransitions(t *testing.T) {
	t.Parallel()
	repo := fleet.NewMemoryRepository()
	ctx := context.Background()
	if err := repo.MarkOnline(ctx, "t", "e", "s1"); err != nil {
		t.Fatalf("MarkOnline: %v", err)
	}

	// Entra en degradado (dead + motivo) ⇒ degraded_since se fija, snapshot completo.
	want := fleet.HealthSnapshot{
		WhatsappState: "dead", DegradedReason: "dek_load_timeout",
		LastEventAgeS: 1860, OutboxDepth: 2, BinaryVersion: "v0.9.0", UptimeS: 60,
	}
	if err := repo.SaveHealth(ctx, "t", "e", "s1", want); err != nil {
		t.Fatalf("SaveHealth degradado: %v", err)
	}
	s := getSession(t, repo, "t", "e", "s1")
	if !snapshotMatches(s, want) || s.DegradedSince.IsZero() || s.LastHealthAt.IsZero() {
		t.Fatalf("snapshot degradado inesperado: %+v", s)
	}
	if s.State != fleet.StateOnline {
		t.Fatalf("SaveHealth no debe tocar el link state: got %q", s.State)
	}
	since := s.DegradedSince

	// Sigue degradado ⇒ degraded_since NO se mueve (preserva el instante de entrada).
	if err := repo.SaveHealth(ctx, "t", "e", "s1", fleet.HealthSnapshot{
		WhatsappState: "degraded", DegradedReason: "ws_dial_timeout",
	}); err != nil {
		t.Fatalf("SaveHealth sigue degradado: %v", err)
	}
	if s = getSession(t, repo, "t", "e", "s1"); !s.DegradedSince.Equal(since) {
		t.Fatalf("degraded_since no debe moverse si sigue degradado: %v != %v", s.DegradedSince, since)
	}

	// Sale de degradado (connected sin motivo) ⇒ degraded_since se limpia.
	if err := repo.SaveHealth(ctx, "t", "e", "s1", fleet.HealthSnapshot{
		WhatsappState: "connected",
	}); err != nil {
		t.Fatalf("SaveHealth sano: %v", err)
	}
	if s = getSession(t, repo, "t", "e", "s1"); !s.DegradedSince.IsZero() || s.DegradedReason != "" {
		t.Fatalf("degraded_since/reason deberían limpiarse al salir: %+v", s)
	}
}

// TestMemorySaveHealthUnknownIsNoOp verifica que persistir salud de una sesión que
// no existe aún no falla (espeja el UPDATE de 0 filas de Postgres).
func TestMemorySaveHealthUnknownIsNoOp(t *testing.T) {
	t.Parallel()
	repo := fleet.NewMemoryRepository()
	if err := repo.SaveHealth(context.Background(), "t", "e", "missing", fleet.HealthSnapshot{WhatsappState: "dead"}); err != nil {
		t.Fatalf("SaveHealth de sesión inexistente no debería fallar: %v", err)
	}
	if _, found, err := repo.Get(context.Background(), "t", "e", "missing"); err != nil || found {
		t.Fatalf("SaveHealth no debe crear la sesión: found=%v err=%v", found, err)
	}
}

// TestMemoryDefaultProfileIsPassive: una sesión recién marcada online nace con
// perfil PASIVO (espeja el DEFAULT 'passive' de la 0063, Plan 046 · T1.1 · D-07).
//
// 📌 Este test hablaba de TestMemoryDefaultRoleIsBot como su vecino divergente; ese
// test se fue con la columna `role` (0064) y hoy solo existe este eje. Cubre el
// llamante MarkOnline; sus hermanos ...OnMarkOffline y ...OnMarkLoggedOut (abajo)
// cubren los otros dos llamantes de defaultProfile, porque un espejo arreglado a
// medias pasa el test de la reconexión y falla el del zombie (T3.1, criterio (b)).
func TestMemoryDefaultProfileIsPassive(t *testing.T) {
	t.Parallel()
	repo := fleet.NewMemoryRepository()
	ctx := context.Background()
	if err := repo.MarkOnline(ctx, "t", "e", "s1"); err != nil {
		t.Fatalf("MarkOnline: %v", err)
	}
	s, _, err := repo.Get(ctx, "t", "e", "s1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if s.Profile != fleet.ProfilePassive {
		t.Fatalf("perfil por defecto: got %q, want passive", s.Profile)
	}
}

// TestMemoryDefaultProfileIsPassiveOnMarkOffline: el SEGUNDO llamante de
// defaultProfile. MarkOffline sobre una sesión que no existía la crea, y esa alta
// también debe nacer pasiva: la política vive en defaultProfile, no en el llamante.
// Se pone rojo si defaultProfile devolviera otra cosa que ProfilePassive para el
// perfil vacío, o si MarkOffline dejara de invocarlo (Profile quedaría "").
func TestMemoryDefaultProfileIsPassiveOnMarkOffline(t *testing.T) {
	t.Parallel()
	repo := fleet.NewMemoryRepository()
	ctx := context.Background()
	if err := repo.MarkOffline(ctx, "t", "e", "s-off"); err != nil {
		t.Fatalf("MarkOffline: %v", err)
	}
	s, found, err := repo.Get(ctx, "t", "e", "s-off")
	if err != nil || !found {
		t.Fatalf("Get: found=%v err=%v", found, err)
	}
	if s.Profile != fleet.ProfilePassive {
		t.Fatalf("alta vía MarkOffline debe nacer pasiva: got %q", s.Profile)
	}
}

// TestMemoryDefaultProfileIsPassiveOnMarkLoggedOut: el TERCER llamante de
// defaultProfile — el que un test que solo ejercite la reconexión nunca caza
// (T3.1: «dejar fuera el de MarkLoggedOut deja la mitad del espejo sin cambiar»).
// Un zombie recién creado también nace pasivo.
func TestMemoryDefaultProfileIsPassiveOnMarkLoggedOut(t *testing.T) {
	t.Parallel()
	repo := fleet.NewMemoryRepository()
	ctx := context.Background()
	if err := repo.MarkLoggedOut(ctx, "t", "e", "s-zombi"); err != nil {
		t.Fatalf("MarkLoggedOut: %v", err)
	}
	s, found, err := repo.Get(ctx, "t", "e", "s-zombi")
	if err != nil || !found {
		t.Fatalf("Get: found=%v err=%v", found, err)
	}
	if s.Profile != fleet.ProfilePassive {
		t.Fatalf("alta vía MarkLoggedOut debe nacer pasiva: got %q", s.Profile)
	}
}

// TestMemorySetProfilePassivePreservedOnReconnect: SetProfile a passive persiste y
// una reconexión (MarkOnline) NO revierte el perfil (lo gobierna SetProfile, no la
// señal de conexión).
//
// 📌 Convertido desde su versión sobre `role` al retirarse esa columna (0064): la
// conducta que protege —una reconexión no pisa lo que el dueño configuró— es la
// misma y NO tenía gemelo por el eje nuevo, así que borrarlo habría perdido cobertura.
func TestMemorySetProfilePassivePreservedOnReconnect(t *testing.T) {
	t.Parallel()
	repo := fleet.NewMemoryRepository()
	ctx := context.Background()
	if err := repo.MarkOnline(ctx, "t", "e", "s1"); err != nil {
		t.Fatalf("MarkOnline: %v", err)
	}
	found, err := repo.SetProfile(ctx, "t", "s1", fleet.ProfilePassive)
	if err != nil || !found {
		t.Fatalf("SetProfile: found=%v err=%v", found, err)
	}
	if err := repo.MarkOnline(ctx, "t", "e", "s1"); err != nil {
		t.Fatalf("MarkOnline reconexión: %v", err)
	}
	s, _, err := repo.Get(ctx, "t", "e", "s1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if s.Profile != fleet.ProfilePassive {
		t.Fatalf("el perfil pasivo debería preservarse al reconectar, got %q", s.Profile)
	}
}

// TestMemorySetProfileActivePreservedOnReconnect es el hermano DISCRIMINANTE del
// anterior (T3.1, criterio (c)): como el default de nacimiento ES pasivo, el caso
// passive-tras-reconexión pasaría igual si MarkOnline reseteara el perfil al
// default — no distingue «preservado» de «pisado». El caso ACTIVO sí: una sesión
// que su dueño activó debe seguir activa tras reconectar, y aquí un reset al
// default se ve en rojo.
func TestMemorySetProfileActivePreservedOnReconnect(t *testing.T) {
	t.Parallel()
	repo := fleet.NewMemoryRepository()
	ctx := context.Background()
	if err := repo.MarkOnline(ctx, "t", "e", "s1"); err != nil {
		t.Fatalf("MarkOnline: %v", err)
	}
	found, err := repo.SetProfile(ctx, "t", "s1", fleet.ProfileActive)
	if err != nil || !found {
		t.Fatalf("SetProfile: found=%v err=%v", found, err)
	}
	if err := repo.MarkOnline(ctx, "t", "e", "s1"); err != nil {
		t.Fatalf("MarkOnline reconexión: %v", err)
	}
	s, _, err := repo.Get(ctx, "t", "e", "s1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if s.Profile != fleet.ProfileActive {
		t.Fatalf("una sesión activada por su dueño debe seguir activa tras reconectar, got %q", s.Profile)
	}
}

// TestMemorySetProfileInvalid: un perfil desconocido se rechaza con
// ErrInvalidProfile y no muta nada. Convertido desde su versión sobre `role` (0064).
func TestMemorySetProfileInvalid(t *testing.T) {
	t.Parallel()
	repo := fleet.NewMemoryRepository()
	ctx := context.Background()
	if err := repo.MarkOnline(ctx, "t", "e", "s1"); err != nil {
		t.Fatalf("MarkOnline: %v", err)
	}
	if _, err := repo.SetProfile(ctx, "t", "s1", fleet.Profile("supervisor")); !errors.Is(err, fleet.ErrInvalidProfile) {
		t.Fatalf("perfil inválido debería dar ErrInvalidProfile, dio: %v", err)
	}
}

// TestMemorySetProfileTenantIsolation: SetProfile solo toca sesiones del tenant
// dado. Una sesión con el MISMO session_id bajo otro tenant queda intacta y
// found=false para el tenant que no la posee (INV-8 del Plan 018). Convertido desde
// su versión sobre `role` (0064); tampoco tenía gemelo.
func TestMemorySetProfileTenantIsolation(t *testing.T) {
	t.Parallel()
	repo := fleet.NewMemoryRepository()
	ctx := context.Background()
	if err := repo.MarkOnline(ctx, "t1", "e", "shared-sess"); err != nil {
		t.Fatalf("MarkOnline t1: %v", err)
	}
	if err := repo.MarkOnline(ctx, "t2", "e", "shared-sess"); err != nil {
		t.Fatalf("MarkOnline t2: %v", err)
	}

	// t1 marca su sesión ACTIVA — distinto del default pasivo, así el cambio se nota.
	found, err := repo.SetProfile(ctx, "t1", "shared-sess", fleet.ProfileActive)
	if err != nil || !found {
		t.Fatalf("SetProfile t1: found=%v err=%v", found, err)
	}
	// La sesión de t2 (mismo session_id) NO se ve afectada: sigue en su default pasivo.
	s2, _, err := repo.Get(ctx, "t2", "e", "shared-sess")
	if err != nil {
		t.Fatalf("Get t2: %v", err)
	}
	if s2.Profile != fleet.ProfilePassive {
		t.Fatalf("aislamiento roto: la sesión de t2 cambió a %q", s2.Profile)
	}
	// Un tenant que no posee la sesión no la encuentra (found=false).
	found, err = repo.SetProfile(ctx, "t-otro", "shared-sess", fleet.ProfileActive)
	if err != nil {
		t.Fatalf("SetProfile t-otro: %v", err)
	}
	if found {
		t.Fatal("un tenant ajeno no debería encontrar la sesión (found=true)")
	}
}

// TestMemoryMarkLoggedOut: MarkLoggedOut deja la sesión en StateLoggedOut, un
// estado DISTINTO de offline (zombie por señal explícita, no offline por red).
func TestMemoryMarkLoggedOut(t *testing.T) {
	t.Parallel()
	repo := fleet.NewMemoryRepository()
	ctx := context.Background()
	if err := repo.MarkOnline(ctx, "t", "e", "s1"); err != nil {
		t.Fatalf("MarkOnline: %v", err)
	}
	if err := repo.MarkLoggedOut(ctx, "t", "e", "s1"); err != nil {
		t.Fatalf("MarkLoggedOut: %v", err)
	}
	s, found, err := repo.Get(ctx, "t", "e", "s1")
	if err != nil || !found {
		t.Fatalf("Get: found=%v err=%v", found, err)
	}
	if s.State != fleet.StateLoggedOut {
		t.Fatalf("estado: got %q, want loggedout", s.State)
	}
	if s.State == fleet.StateOffline {
		t.Fatal("loggedout no debe confundirse con offline")
	}
}

// TestMemoryMarkLoggedOutUnknownIsNoError: marcar zombie una sesión inexistente
// no falla (mismo contrato que MarkOffline).
func TestMemoryMarkLoggedOutUnknownIsNoError(t *testing.T) {
	t.Parallel()
	repo := fleet.NewMemoryRepository()
	if err := repo.MarkLoggedOut(context.Background(), "t", "e", "missing"); err != nil {
		t.Fatalf("MarkLoggedOut de sesión inexistente no debería fallar: %v", err)
	}
}

// TestMemorySetStateValidationAndIsolation: SetState rechaza estados no admin
// (online / arbitrario) con ErrInvalidState, y solo toca sesiones del tenant dado
// (aislamiento INV-8; found=false para un tenant ajeno).
func TestMemorySetStateValidationAndIsolation(t *testing.T) {
	t.Parallel()
	repo := fleet.NewMemoryRepository()
	ctx := context.Background()
	if err := repo.MarkOnline(ctx, "t1", "e", "s1"); err != nil {
		t.Fatalf("MarkOnline t1: %v", err)
	}
	if err := repo.MarkOnline(ctx, "t2", "e", "s1"); err != nil {
		t.Fatalf("MarkOnline t2: %v", err)
	}

	// online NO es admin-admitido: ErrInvalidState.
	if _, err := repo.SetState(ctx, "t1", "s1", fleet.StateOnline); !errors.Is(err, fleet.ErrInvalidState) {
		t.Fatalf("StateOnline debería dar ErrInvalidState, dio: %v", err)
	}
	// loggedout sí: retira la sesión de t1.
	found, err := repo.SetState(ctx, "t1", "s1", fleet.StateLoggedOut)
	if err != nil || !found {
		t.Fatalf("SetState loggedout: found=%v err=%v", found, err)
	}
	// La sesión de t2 (mismo session_id) NO se ve afectada: sigue online.
	s2, _, err := repo.Get(ctx, "t2", "e", "s1")
	if err != nil {
		t.Fatalf("Get t2: %v", err)
	}
	if s2.State != fleet.StateOnline {
		t.Fatalf("aislamiento roto: la sesión de t2 cambió a %q", s2.State)
	}
	// Un tenant ajeno no encuentra la sesión (found=false).
	found, err = repo.SetState(ctx, "t-otro", "s1", fleet.StateOffline)
	if err != nil {
		t.Fatalf("SetState t-otro: %v", err)
	}
	if found {
		t.Fatal("un tenant ajeno no debería encontrar la sesión (found=true)")
	}
}

// TestMemoryCountLiveBySelfPn: cuenta sesiones vivas por self_pn dentro del tenant;
// una sesión zombie (loggedout) NO cuenta; otro tenant no contamina el conteo.
func TestMemoryCountLiveBySelfPn(t *testing.T) {
	t.Parallel()
	repo := fleet.NewMemoryRepository()
	ctx := context.Background()
	const pn = "593999000111"

	// Tres sesiones del mismo tenant con el mismo self_pn: dos vivas, una zombie.
	for _, sess := range []string{"s1", "s2", "s3"} {
		if err := repo.MarkOnline(ctx, "t", "e", sess); err != nil {
			t.Fatalf("MarkOnline %s: %v", sess, err)
		}
		if err := repo.SetSelfPn(ctx, "t", "e", sess, pn); err != nil {
			t.Fatalf("SetSelfPn %s: %v", sess, err)
		}
	}
	if err := repo.MarkLoggedOut(ctx, "t", "e", "s3"); err != nil {
		t.Fatalf("MarkLoggedOut s3: %v", err)
	}
	// Otro tenant con el mismo número no debe contaminar.
	if err := repo.MarkOnline(ctx, "t2", "e", "x1"); err != nil {
		t.Fatalf("MarkOnline t2: %v", err)
	}
	if err := repo.SetSelfPn(ctx, "t2", "e", "x1", pn); err != nil {
		t.Fatalf("SetSelfPn t2: %v", err)
	}

	n, err := repo.CountLiveBySelfPn(ctx, "t", pn)
	if err != nil {
		t.Fatalf("CountLiveBySelfPn: %v", err)
	}
	if n != 2 {
		t.Fatalf("conteo vivas: got %d, want 2 (s3 zombie no cuenta; t2 es otro tenant)", n)
	}
	// selfPn vacío ⇒ 0 sin error.
	if n, err := repo.CountLiveBySelfPn(ctx, "t", ""); err != nil || n != 0 {
		t.Fatalf("CountLiveBySelfPn vacío: n=%d err=%v, want 0/nil", n, err)
	}
}

func TestMemoryListByTenant(t *testing.T) {
	t.Parallel()
	repo := fleet.NewMemoryRepository()
	ctx := context.Background()

	for _, s := range []struct{ tenant, edge, sess string }{
		{"t1", "e1", "s1"},
		{"t1", "e1", "s2"},
		{"t2", "e9", "s9"},
	} {
		if err := repo.MarkOnline(ctx, s.tenant, s.edge, s.sess); err != nil {
			t.Fatalf("MarkOnline: %v", err)
		}
	}

	got, err := repo.List(ctx, "t1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List(t1): got %d sesiones, want 2", len(got))
	}
}
