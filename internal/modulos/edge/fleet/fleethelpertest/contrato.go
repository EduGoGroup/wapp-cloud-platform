// Package fleethelpertest es la suite de contrato del puerto fleet.Repository, su doble en
// memoria, Memoria (D-F3-1: el doble vivía en el paquete de producción viejo, como
// MemoryRepository), y el decorador SlowRepository (en el paquete viejo, fleettest). Ningún
// código de producción lo importa: arrastra "testing".
//
//   - contrato.go: la entrada. Montaje, ContratoRepository, la tabla de casos y las ayudas.
//   - state_contrato.go: el estado del link (MarkOnline, MarkOffline, MarkLoggedOut, SetState,
//     Get, List) y el aislamiento por tenant.
//   - profile_contrato.go: el eje `profile` y la foto que alimenta el kind:"filters".
//   - health_contrato.go: SaveHealth, la marca degraded_since y el bloque del worker.
//   - selfpn_contrato.go: SetSelfPn y CountLiveBySelfPn (el tope de dispositivos, REQ-D4).
//   - memoria.go: Memoria, el Repository en memoria que usan los tests del gateway.
//   - slowrepo.go: SlowRepository, el decorador que inyecta latencia cancelable.
//
// La suite la corren las dos implementaciones del puerto: Memoria en unitario (memoria_test.go)
// y el adaptador Postgres en los procesos de F9 (sesión F3-05), con el arnés de testcontainers.
//
// Los casos salen de plan/F3-edge/diseno.md §2 y de los tests viejos de internal/gateway/fleet
// @ 809345b (fleet_test.go, worker_health_test.go y los *_integration_test.go), leídos, no
// portados.
package fleethelpertest

import (
	"context"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet"
	"github.com/google/uuid"
)

// Montaje es lo que cada implementación entrega a la suite para UN caso. Tiene que venir
// limpio —sin filas de sesión— porque ContratoRepository llama a nuevo una vez por caso.
type Montaje struct {
	// Repository es la implementación bajo prueba.
	Repository fleet.Repository
	// SeedTenant devuelve el id de un tenant que EXISTE para el repositorio. El id tiene forma
	// de UUID y es distinto en cada llamada. Con Postgres es una fila de public.tenants
	// (public.fleet_sessions la referencia por clave foránea); con Memoria basta un UUID nuevo,
	// porque el doble no sabe de tenants que no existen. Falla el test t si no puede sembrar.
	SeedTenant func(t *testing.T) string
	// Profiles lee la foto de perfiles del tenant: ProfilesByTenant NO está en fleet.Repository
	// a propósito (es la lectura de un solo consumidor), así que la suite la pide por aquí.
	// Falla el test t si la lectura da error.
	Profiles func(t *testing.T, tenantID string) fleet.TenantProfiles
}

// ContratoRepository ejecuta las promesas de fleet.Repository contra la implementación que
// devuelve nuevo, con un Montaje limpio por caso (nuevo se llama una vez por t.Run). No salta
// nada. Toda siembra y toda observación pasan por el puerto o por las funciones del Montaje:
// los tenants salen siempre de SeedTenant y cada caso usa edge_id y session_id propios.
//
// Lo que la suite NO afirma, a propósito:
//   - que MarkOffline o MarkLoggedOut de una sesión desconocida creen o no la fila: Memoria la
//     crea (pasiva) y Postgres no (UPDATE de 0 filas). El doble se porta tal cual; la suite solo
//     afirma que no es un error. Lo que hace el doble lo fija memoria_test.go.
//   - el orden de List: Postgres ordena por (edge_id, session_id) y Memoria recorre un mapa.
//   - el valor de una marca de tiempo que nunca se fijó: time.Time{} en Memoria; en Postgres,
//     lo que traiga la columna. Solo se afirma IsZero donde las dos lo garantizan
//     (DegradedSince de una sesión sana).
//   - qué pasa con un ctx cancelado: Memoria lo ignora y Postgres devuelve su error.
//   - un perfil desconocido dentro de la foto: por el puerto no se puede escribir (SetProfile
//     lo rechaza), así que no es observable desde aquí.
//   - el aviso de sesión pasiva (PendingGreeting, MarkGreeted): no está en el puerto; es de su
//     consumidor.
//   - un tenant_id que no es un UUID: Postgres devuelve un error de parseo y Memoria no.
//   - el cifrado en reposo del self_pn y su índice ciego: solo existen en Postgres (F9).
func ContratoRepository(t *testing.T, nuevo func(t *testing.T) Montaje) {
	t.Helper()
	if nuevo == nil {
		t.Fatal("fleethelpertest.ContratoRepository: nuevo es nil; hace falta una función que devuelva un Montaje")
	}
	for _, c := range cases() {
		t.Run(c.name, func(t *testing.T) {
			m := nuevo(t)
			validateMontaje(t, m)
			c.run(t, m)
		})
	}
}

// contractCase es una promesa del puerto: su nombre (el del t.Run) y la función que la afirma.
type contractCase struct {
	name string
	run  func(t *testing.T, m Montaje)
}

// cases es la tabla de la suite. El comentario de cada fila es la promesa que fija.
func cases() []contractCase {
	return []contractCase{
		// El estado del link (state_contrato.go).
		{"MarkOnline_NewSession_IsOnlineWithTimestamps", caseMarkOnlineNew},               // alta: online, con sus dos marcas
		{"MarkOffline_OnlineSession_GoesOfflineAndBack", caseOnlineOfflineOnline},         // online→offline→online
		{"MarkOffline_UnknownSession_NoError", caseMarkOfflineUnknown},                    // 0 filas no es un error
		{"MarkLoggedOut_OnlineSession_IsNotOffline", caseMarkLoggedOut},                   // zombie ≠ offline por red
		{"MarkLoggedOut_UnknownSession_NoError", caseMarkLoggedOutUnknown},                // 0 filas no es un error
		{"MarkOffline_OneEdgeRow_LeavesTheOtherEdgeRow", caseRowsKeyedByEdge},             // clave (tenant, edge, session)
		{"SetState_InvalidState_ErrInvalidStateWithoutMutation", caseSetStateInvalid},     // solo offline|loggedout
		{"SetState_UnknownSession_NotFound", caseSetStateUnknown},                         // found=false sin error
		{"SetState_SessionUnderTwoEdges_TouchesAllItsRows", caseSetStateAllRows},          // acota por tenant+session
		{"SetState_OtherTenant_NotFoundAndUntouched", caseSetStateOtherTenant},            // INV-8: 404 opaco
		{"Get_UnknownSession_ZeroAndNotFound", caseGetUnknown},                            // (zero, false, nil)
		{"List_TwoTenants_ReturnsOnlyTheAskedTenant", caseListByTenant},                   // filtra por tenant
		{"MarkOnline_SameIDsInTwoTenants_AreIndependentRows", caseSameIDsInTwoTenants},    // el tenant es parte de la clave
		{"MarkOnline_NewSession_IsBornPassive", caseBornPassive},                          // privacidad por defecto (D-07)
		{"SetProfile_ThenReconnect_KeepsTheProfile", caseProfileSurvivesReconnect},        // reconectar no es cambiar de perfil
		{"SetProfile_ThenOfflineAndLoggedOut_KeepsTheProfile", caseProfileSurvivesStates}, // el link no es el perfil
		{"SetProfile_InvalidProfile_ErrInvalidProfileWithoutMutation", caseSetProfileInvalid},
		{"SetProfile_UnknownSession_NotFound", caseSetProfileUnknown},                  // found=false sin error
		{"SetProfile_SessionUnderTwoEdges_TouchesAllItsRows", caseSetProfileAllRows},   // acota por tenant+session
		{"SetProfile_OtherTenant_NotFoundAndUntouched", caseSetProfileOtherTenant},     // INV-8: 404 opaco
		{"Profiles_MixedSessions_IncludesActiveOnes", caseProfilesFullPhoto},           // foto COMPLETA (fail-open del Edge)
		{"Profiles_DiscordantRowsOfOneSession_PassiveWins", caseProfilesDiscordant},    // la lectura segura
		{"Profiles_TenantWithoutSessions_EmptyMapVersionZero", caseProfilesEmpty},      // mapa no nil, versión 0
		{"Profiles_OtherTenant_IsNotInThePhoto", caseProfilesOtherTenant},              // la foto es del tenant
		{"Profiles_FirstSession_VersionIsPositive", caseVersionPositive},               // el alta fija el reloj del eje
		{"Profiles_RowNoise_DoesNotMoveTheVersion", caseVersionIgnoresNoise},           // 0065: solo SetProfile
		{"Profiles_SetProfile_VersionGrowsStrictly", caseVersionGrows},                 // active→passive→active no se cancela
		{"SaveHealth_OnlineSession_DoesNotTouchTheLink", caseHealthLeavesLink},         // socket ≠ link CloudLink
		{"SaveHealth_DegradedSince_SetOnEntryKeptAndClearedOnExit", caseDegradedSince}, // se fija al ENTRAR
		{"SaveHealth_UnknownSession_NoOpNoError", caseHealthUnknown},                   // 0 filas no es un error
		{"SaveHealth_WorkerBlockUnknown_IsNotZero", caseWorkerUnknown},                 // nil ≠ 0 (T4.3)
		{"SaveHealth_WorkerBlockKnown_RoundTrips", caseWorkerRoundTrip},                // un 0 medido vuelve como &0
		{"SaveHealth_StaleWorkerReport_ClearsThePrevious", caseWorkerStale},            // un parte rancio borra
		{"SetSelfPn_FormattedNumber_StoresTheCanonicalForm", caseSelfPnCanonical},      // se guarda normalizado
		{"SetSelfPn_Empty_KeepsThePreviousValue", caseSelfPnEmpty},                     // "" es un no-op
		{"SetSelfPn_NotNormalizable_ErrorAndNoWrite", caseSelfPnInvalid},               // error, no no-op silencioso
		{"SetSelfPn_UnknownSession_NoError", caseSelfPnUnknown},                        // 0 filas no es un error
		{"CountLiveBySelfPn_TwoSpellings_CountAsOneNumber", caseCountCanonical},        // cuenta por forma canónica
		{"CountLiveBySelfPn_LoggedOutSession_IsExcluded", caseCountExcludesZombies},    // solo el zombie no cuenta
		{"CountLiveBySelfPn_OtherTenant_IsNotCounted", caseCountPerTenant},             // no cruza tenants
		{"CountLiveBySelfPn_Empty_Zero", caseCountEmpty},                               // sin número no hay cuenta
		{"CountLiveBySelfPn_NotNormalizable_Error", caseCountInvalid},                  // «no puedo contar» ≠ 0
	}
}

// validateMontaje exige lo que la suite da por hecho de un Montaje.
func validateMontaje(t *testing.T, m Montaje) {
	t.Helper()
	switch {
	case m.Repository == nil:
		t.Fatal("Montaje.Repository es nil")
	case m.SeedTenant == nil:
		t.Fatal("Montaje.SeedTenant es nil: la suite necesita sembrar tenants")
	case m.Profiles == nil:
		t.Fatal("Montaje.Profiles es nil: la suite necesita leer la foto de perfiles")
	}
}

// seedTenant siembra un tenant y comprueba que su id es un UUID bien formado.
func seedTenant(t *testing.T, m Montaje) string {
	t.Helper()
	tenant := m.SeedTenant(t)
	if _, err := uuid.Parse(tenant); err != nil {
		t.Fatalf("Montaje.SeedTenant devolvió %q, que no es un UUID bien formado: %v", tenant, err)
	}
	return tenant
}

// seedTwoTenants siembra dos tenants y exige que sean distintos.
func seedTwoTenants(t *testing.T, m Montaje) (first, second string) {
	t.Helper()
	first, second = seedTenant(t, m), seedTenant(t, m)
	if first == second {
		t.Fatalf("Montaje.SeedTenant devolvió dos veces el mismo tenant %q", first)
	}
	return first, second
}

// unique devuelve un edge_id o session_id propio del caso: prefix más un UUID nuevo.
func unique(prefix string) string { return prefix + "-" + uuid.NewString() }

// markOnline llama a MarkOnline y falla el test si devuelve error.
func markOnline(t *testing.T, m Montaje, tenant, edge, session string) {
	t.Helper()
	if err := m.Repository.MarkOnline(context.Background(), tenant, edge, session); err != nil {
		t.Fatalf("MarkOnline(%s, %s, %s): error inesperado %v", tenant, edge, session, err)
	}
}

// markOffline llama a MarkOffline y falla el test si devuelve error.
func markOffline(t *testing.T, m Montaje, tenant, edge, session string) {
	t.Helper()
	if err := m.Repository.MarkOffline(context.Background(), tenant, edge, session); err != nil {
		t.Fatalf("MarkOffline(%s, %s, %s): error inesperado %v", tenant, edge, session, err)
	}
}

// markLoggedOut llama a MarkLoggedOut y falla el test si devuelve error.
func markLoggedOut(t *testing.T, m Montaje, tenant, edge, session string) {
	t.Helper()
	if err := m.Repository.MarkLoggedOut(context.Background(), tenant, edge, session); err != nil {
		t.Fatalf("MarkLoggedOut(%s, %s, %s): error inesperado %v", tenant, edge, session, err)
	}
}

// setState llama a SetState con un estado admitido sobre una sesión que EXISTE: un found=false
// silencioso convertiría «no cambió nada» en un falso verde del caso que lo use.
func setState(t *testing.T, m Montaje, tenant, session string, state fleet.State) {
	t.Helper()
	found, err := m.Repository.SetState(context.Background(), tenant, session, state)
	if err != nil || !found {
		t.Fatalf("SetState(%s, %s, %q) = (found=%v, err=%v), quería (true, nil)", tenant, session, state, found, err)
	}
}

// setProfile llama a SetProfile con un perfil válido sobre una sesión que EXISTE (ver setState).
func setProfile(t *testing.T, m Montaje, tenant, session string, profile fleet.Profile) {
	t.Helper()
	found, err := m.Repository.SetProfile(context.Background(), tenant, session, profile)
	if err != nil || !found {
		t.Fatalf("SetProfile(%s, %s, %q) = (found=%v, err=%v), quería (true, nil)", tenant, session, profile, found, err)
	}
}

// saveHealth llama a SaveHealth y falla el test si devuelve error.
func saveHealth(t *testing.T, m Montaje, tenant, edge, session string, h fleet.HealthSnapshot) {
	t.Helper()
	if err := m.Repository.SaveHealth(context.Background(), tenant, edge, session, h); err != nil {
		t.Fatalf("SaveHealth(%s, %s, %s): error inesperado %v", tenant, edge, session, err)
	}
}

// setSelfPn llama a SetSelfPn y falla el test si devuelve error.
func setSelfPn(t *testing.T, m Montaje, tenant, edge, session, selfPn string) {
	t.Helper()
	if err := m.Repository.SetSelfPn(context.Background(), tenant, edge, session, selfPn); err != nil {
		t.Fatalf("SetSelfPn(%s, %s, %s): error inesperado %v", tenant, edge, session, err)
	}
}

// mustGet devuelve la sesión, que tiene que existir y decir de quién es.
func mustGet(t *testing.T, m Montaje, tenant, edge, session string) fleet.Session {
	t.Helper()
	s, found, err := m.Repository.Get(context.Background(), tenant, edge, session)
	if err != nil {
		t.Fatalf("Get(%s, %s, %s): error inesperado %v", tenant, edge, session, err)
	}
	if !found {
		t.Fatalf("Get(%s, %s, %s): found = false, quería la sesión", tenant, edge, session)
	}
	if s.TenantID != tenant || s.EdgeID != edge || s.SessionID != session {
		t.Errorf("Get(%s, %s, %s) devolvió la fila de (%s, %s, %s)", tenant, edge, session, s.TenantID, s.EdgeID, s.SessionID)
	}
	return s
}

// requireState afirma el estado del link de una sesión que existe, y la devuelve.
func requireState(t *testing.T, m Montaje, tenant, edge, session string, want fleet.State) fleet.Session {
	t.Helper()
	s := mustGet(t, m, tenant, edge, session)
	if s.State != want {
		t.Errorf("Get(%s, %s): state = %q, quería %q", edge, session, s.State, want)
	}
	return s
}

// requireProfile afirma el perfil de una sesión que existe.
func requireProfile(t *testing.T, m Montaje, tenant, edge, session string, want fleet.Profile) {
	t.Helper()
	if s := mustGet(t, m, tenant, edge, session); s.Profile != want {
		t.Errorf("Get(%s, %s): profile = %q, quería %q", edge, session, s.Profile, want)
	}
}

// requireSelfPn afirma el número propio de una sesión que existe.
func requireSelfPn(t *testing.T, m Montaje, tenant, edge, session, want string) {
	t.Helper()
	if s := mustGet(t, m, tenant, edge, session); s.SelfPn != want {
		t.Errorf("Get(%s, %s): self_pn = %q, quería %q", edge, session, s.SelfPn, want)
	}
}

// profiles lee la foto del tenant por el Montaje y exige lo que TenantProfiles promete de
// cualquier implementación: el mapa nunca es nil.
func profiles(t *testing.T, m Montaje, tenant string) fleet.TenantProfiles {
	t.Helper()
	tp := m.Profiles(t, tenant)
	if tp.Sessions == nil {
		t.Fatalf("Profiles(%s): Sessions es nil; tiene que ser un mapa, vacío si no hay filas", tenant)
	}
	return tp
}

// requireCount afirma lo que cuenta CountLiveBySelfPn.
func requireCount(t *testing.T, m Montaje, tenant, selfPn string, want int) {
	t.Helper()
	got, err := m.Repository.CountLiveBySelfPn(context.Background(), tenant, selfPn)
	if err != nil {
		t.Fatalf("CountLiveBySelfPn(%s): error inesperado %v", tenant, err)
	}
	if got != want {
		t.Errorf("CountLiveBySelfPn(%s) = %d, quería %d", tenant, got, want)
	}
}
