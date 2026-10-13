package runtimehelpertest

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
)

// MontajeTenantResolver es lo que cada implementación entrega a ContratoTenantResolver para UN
// caso: la suite llama a nuevo una vez por caso y no limpia nada entre llamadas. Todos los campos
// son obligatorios.
//
// La siembra y el observador existen porque el puerto solo pregunta: no hay forma de dejar ni de
// ver una sesión por él. En memoria salen de FleetSessions; contra Postgres, de SQL directo.
type MontajeTenantResolver struct {
	// Resolver es la implementación bajo prueba, sobre la tabla que siembra Seed.
	Resolver runtime.TenantResolver
	// TenantA y TenantB son dos tenants distintos, UUID bien formados y sin ninguna sesión. Con
	// Postgres son filas reales de public.tenants (fleet_sessions las exige por clave foránea).
	TenantA, TenantB string
	// Seed deja esa sesión en la tabla; si ya había una con su misma clave (tenant, edge,
	// sesión), la sustituye. Un Profile vacío NO escribe la columna: la deja al DEFAULT del
	// esquema. Falla el test t si no puede sembrar.
	Seed func(t *testing.T, s Session)
	// AllowAnyProfile retira lo que impide sembrar un perfil fuera del dominio active|passive
	// (con Postgres, el CHECK fleet_sessions_profile_chk de la 0063), para el resto del caso.
	AllowAnyProfile func(t *testing.T)
	// Sessions devuelve TODAS las filas de la tabla, ordenadas por tenant, edge y sesión, con el
	// perfil que de verdad quedó guardado (nunca vacío).
	Sessions func(t *testing.T) []Session
}

// ContratoTenantResolver ejecuta las promesas de runtime.PostgresTenantResolver contra la
// implementación que devuelve nuevo, con un Montaje limpio por caso (nuevo se llama una vez por
// t.Run). No salta nada.
//
// Lo que la suite NO afirma, a propósito: los fallos al leer o al recorrer las filas (los
// prefijos «resolver tenant: scan: » y «resolver tenant: iterar filas: »), que no se pueden
// provocar por el puerto. Los prueba el test de fichero del adaptador, con su driver de mentira.
func ContratoTenantResolver(t *testing.T, nuevo func(t *testing.T) MontajeTenantResolver) {
	t.Helper()
	if nuevo == nil {
		t.Fatal("runtimehelpertest.ContratoTenantResolver: nuevo es nil; hace falta una función que devuelva un MontajeTenantResolver")
	}
	for _, c := range tenantResolverCases() {
		t.Run(c.name, func(t *testing.T) {
			m := nuevo(t)
			if m.Resolver == nil {
				t.Fatal("MontajeTenantResolver.Resolver es nil")
			}
			validateFleetMontaje(t, m.TenantA, m.TenantB, m.Seed, m.AllowAnyProfile, m.Sessions)
			c.run(t, m)
		})
	}
}

// tenantResolverCase es una promesa del adaptador: su nombre (el del t.Run) y quien la afirma.
type tenantResolverCase struct {
	name string
	run  func(t *testing.T, m MontajeTenantResolver)
}

// tenantResolverCases es la tabla de la suite. El comentario de cada fila es la promesa que fija.
func tenantResolverCases() []tenantResolverCase {
	return []tenantResolverCase{
		{"ActiveSession_ResolvesItsTenantAsActive", caseResolveActive},                       // un tenant, activa
		{"PassiveSession_ResolvesPassive", caseResolvePassive},                               // un tenant, pasiva
		{"ProfileLeftToTheDefault_ResolvesPassive", caseResolveDefaultProfile},               // D-07: nace pasiva
		{"UnknownSession_ErrTenantNotResolved", caseResolveUnknownSession},                   // 0 filas, con su texto
		{"EmptySessionID_ErrTenantNotResolved", caseResolveEmptySession},                     // vacío = 0 filas
		{"SeveralEdgesOfOneTenant_AllActive_ResolvesActive", caseResolveSeveralEdgesActive},  // varios edges, un tenant
		{"SeveralEdgesOfOneTenant_OnePassive_ResolvesPassive", caseResolveSeveralEdgesMixed}, // bool_or conservador
		{"ProfileOutOfDomain_FallsToPassive", caseResolveUnknownProfile},                     // <> 'active', no = 'passive'
		{"SameSessionUnderTwoTenants_Ambiguous", caseResolveAmbiguous},                       // N tenants, con su texto
		{"AnyState_ResolvesTheSame", caseResolveAnyState},                                    // no filtra por state
		{"EachSessionResolvesToItsOwnTenant", caseResolveEachItsOwn},                         // no mezcla sesiones
		{"CancelledContext_WrappedErrorNotTheSentinel", caseResolveCancelledContext},         // fallo de la base
		{"Asking_LeavesTheTableUntouched", caseResolveReadOnly},                              // solo lee
	}
}

// requireResolved afirma que la sesión resuelve, sin error, a ese tenant con ese perfil.
func requireResolved(t *testing.T, m MontajeTenantResolver, sessionID, wantTenant, wantProfile string) {
	t.Helper()
	tenantID, profile, err := m.Resolver.ResolveTenant(t.Context(), sessionID)
	if err != nil {
		t.Fatalf("ResolveTenant(%q): error inesperado %v", sessionID, err)
	}
	if tenantID != wantTenant || profile != wantProfile {
		t.Errorf("ResolveTenant(%q) = (%q, %q), quería (%q, %q)", sessionID, tenantID, profile, wantTenant, wantProfile)
	}
}

// requireNotResolved afirma que la sesión NO resuelve: valores vacíos y un error que casa con
// runtime.ErrTenantNotResolved y cuyo texto es exactamente el centinela seguido de detail.
func requireNotResolved(t *testing.T, m MontajeTenantResolver, sessionID, detail string) {
	t.Helper()
	tenantID, profile, err := m.Resolver.ResolveTenant(t.Context(), sessionID)
	if !errors.Is(err, runtime.ErrTenantNotResolved) {
		t.Fatalf("ResolveTenant(%q): error = %v, quería ErrTenantNotResolved", sessionID, err)
	}
	if want := "no se pudo resolver tenant para la sesión: " + detail; err.Error() != want {
		t.Errorf("ResolveTenant(%q): texto del error = %q, quería %q", sessionID, err.Error(), want)
	}
	if tenantID != "" || profile != "" {
		t.Errorf("ResolveTenant(%q) con error devolvió (%q, %q), quería los dos vacíos", sessionID, tenantID, profile)
	}
}

// caseResolveActive: una sesión activa de un tenant resuelve a ese tenant y a "active".
func caseResolveActive(t *testing.T, m MontajeTenantResolver) {
	m.Seed(t, Session{TenantID: m.TenantA, EdgeID: edgeOne, SessionID: sessionOne, State: StateOnline, Profile: ProfileActive})
	requireResolved(t, m, sessionOne, m.TenantA, ProfileActive)
}

// caseResolvePassive: una sesión pasiva resuelve a su tenant y a "passive".
func caseResolvePassive(t *testing.T, m MontajeTenantResolver) {
	m.Seed(t, Session{TenantID: m.TenantA, EdgeID: edgeOne, SessionID: sessionOne, State: StateOnline, Profile: ProfilePassive})
	requireResolved(t, m, sessionOne, m.TenantA, ProfilePassive)
}

// caseResolveDefaultProfile fija D-07 (Plan 046 · T1.1): una sesión sembrada SIN tocar el perfil
// cae al DEFAULT del esquema y resuelve PASIVA: recién emparejada, no auto-responde hasta que su
// dueño la activa. Si sale activa, alguien devolvió el DEFAULT a activo y con él la captura de
// tráfico por defecto que ese plan cerró.
func caseResolveDefaultProfile(t *testing.T, m MontajeTenantResolver) {
	m.Seed(t, Session{TenantID: m.TenantA, EdgeID: edgeOne, SessionID: sessionOne, State: StateOnline})
	requireResolved(t, m, sessionOne, m.TenantA, ProfilePassive)
}

// caseResolveUnknownSession: una sesión que no está en la tabla —aunque haya otras— no resuelve.
func caseResolveUnknownSession(t *testing.T, m MontajeTenantResolver) {
	m.Seed(t, Session{TenantID: m.TenantA, EdgeID: edgeOne, SessionID: sessionOne, State: StateOnline, Profile: ProfileActive})
	requireNotResolved(t, m, sessionTwo, "session_id="+sessionTwo+" (0 filas en fleet_sessions)")
}

// caseResolveEmptySession: el session_id vacío es una sesión que no existe, no un comodín.
func caseResolveEmptySession(t *testing.T, m MontajeTenantResolver) {
	m.Seed(t, Session{TenantID: m.TenantA, EdgeID: edgeOne, SessionID: sessionOne, State: StateOnline, Profile: ProfileActive})
	requireNotResolved(t, m, "", "session_id= (0 filas en fleet_sessions)")
}

// caseResolveSeveralEdgesActive: el mismo session_id bajo dos edges del MISMO tenant no es
// ambiguo: cuenta como un tenant, y con todas sus filas activas resuelve activa.
func caseResolveSeveralEdgesActive(t *testing.T, m MontajeTenantResolver) {
	m.Seed(t, Session{TenantID: m.TenantA, EdgeID: edgeOne, SessionID: sessionOne, State: StateOnline, Profile: ProfileActive})
	m.Seed(t, Session{TenantID: m.TenantA, EdgeID: edgeTwo, SessionID: sessionOne, State: StateOnline, Profile: ProfileActive})
	requireResolved(t, m, sessionOne, m.TenantA, ProfileActive)
}

// caseResolveSeveralEdgesMixed: si CUALQUIER fila de la sesión bajo ese tenant no es activa, el
// perfil efectivo es pasivo (ante un binding mixto no se auto-responde), esté en el edge que esté.
func caseResolveSeveralEdgesMixed(t *testing.T, m MontajeTenantResolver) {
	for _, passiveEdge := range []string{edgeOne, edgeTwo} {
		for _, edge := range []string{edgeOne, edgeTwo} {
			profile := ProfileActive
			if edge == passiveEdge {
				profile = ProfilePassive
			}
			m.Seed(t, Session{TenantID: m.TenantA, EdgeID: edge, SessionID: sessionOne, State: StateOnline, Profile: profile})
		}
		requireResolved(t, m, sessionOne, m.TenantA, ProfilePassive)
	}
}

// caseResolveUnknownProfile fija la regla «ante la duda, PASIVA» en el único punto donde el
// runtime decide si una sesión auto-responde. Sobre el dominio de dos valores, `profile =
// 'passive'` y `profile <> 'active'` son equivalentes; ante un valor fuera del dominio el primero
// daría una sesión que AUTO-RESPONDE. Este caso es lo único que separa las dos formulaciones.
func caseResolveUnknownProfile(t *testing.T, m MontajeTenantResolver) {
	m.AllowAnyProfile(t)
	m.Seed(t, Session{TenantID: m.TenantA, EdgeID: edgeOne, SessionID: sessionOne, State: StateOnline, Profile: "supervisor"})
	requireResolved(t, m, sessionOne, m.TenantA, ProfilePassive)

	// Y una fila activa en otro edge no lo arregla: basta una que no sea 'active'.
	m.Seed(t, Session{TenantID: m.TenantA, EdgeID: edgeTwo, SessionID: sessionOne, State: StateOnline, Profile: ProfileActive})
	requireResolved(t, m, sessionOne, m.TenantA, ProfilePassive)
}

// caseResolveAmbiguous: el mismo session_id bajo dos tenants distintos no elige ninguno.
func caseResolveAmbiguous(t *testing.T, m MontajeTenantResolver) {
	m.Seed(t, Session{TenantID: m.TenantA, EdgeID: edgeOne, SessionID: sessionOne, State: StateOnline, Profile: ProfileActive})
	m.Seed(t, Session{TenantID: m.TenantB, EdgeID: edgeOne, SessionID: sessionOne, State: StateOnline, Profile: ProfileActive})
	requireNotResolved(t, m, sessionOne, "session_id="+sessionOne+" ambiguo (2 tenants)")
}

// caseResolveAnyState: la pregunta es «¿de quién es?», no «¿está viva?»: una sesión offline o
// retirada resuelve igual que una online, y su perfil es el suyo.
func caseResolveAnyState(t *testing.T, m MontajeTenantResolver) {
	for _, state := range []string{StateOnline, StateOffline, StateLoggedOut} {
		m.Seed(t, Session{TenantID: m.TenantA, EdgeID: edgeOne, SessionID: sessionOne, State: state, Profile: ProfileActive})
		requireResolved(t, m, sessionOne, m.TenantA, ProfileActive)
	}
	// Una fila retirada también pesa en el agregado del perfil.
	m.Seed(t, Session{TenantID: m.TenantA, EdgeID: edgeTwo, SessionID: sessionOne, State: StateLoggedOut, Profile: ProfilePassive})
	requireResolved(t, m, sessionOne, m.TenantA, ProfilePassive)
}

// caseResolveEachItsOwn: con dos sesiones de dos tenants, cada una resuelve a su tenant y a su
// perfil; la pasiva de uno no vuelve pasiva a la del otro.
func caseResolveEachItsOwn(t *testing.T, m MontajeTenantResolver) {
	m.Seed(t, Session{TenantID: m.TenantA, EdgeID: edgeOne, SessionID: sessionOne, State: StateOnline, Profile: ProfileActive})
	m.Seed(t, Session{TenantID: m.TenantB, EdgeID: edgeOne, SessionID: sessionTwo, State: StateOnline, Profile: ProfilePassive})
	// Otra sesión pasiva del MISMO tenant A, en el mismo edge: tampoco contamina a sessionOne.
	m.Seed(t, Session{TenantID: m.TenantA, EdgeID: edgeOne, SessionID: "sess-tres", State: StateOnline, Profile: ProfilePassive})

	requireResolved(t, m, sessionOne, m.TenantA, ProfileActive)
	requireResolved(t, m, sessionTwo, m.TenantB, ProfilePassive)
}

// caseResolveCancelledContext: con el contexto cancelado la consulta falla, y eso NO es «la sesión
// no existe»: valores vacíos y un error envuelto tras su prefijo que no casa con el centinela.
func caseResolveCancelledContext(t *testing.T, m MontajeTenantResolver) {
	m.Seed(t, Session{TenantID: m.TenantA, EdgeID: edgeOne, SessionID: sessionOne, State: StateOnline, Profile: ProfileActive})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	tenantID, profile, err := m.Resolver.ResolveTenant(ctx, sessionOne)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, quería uno que envuelva context.Canceled", err)
	}
	if errors.Is(err, runtime.ErrTenantNotResolved) {
		t.Errorf("un fallo de la consulta casa con ErrTenantNotResolved: %v", err)
	}
	if prefix := "resolver tenant: consulta fleet_sessions: "; !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("texto del error = %q, quería el prefijo %q", err.Error(), prefix)
	}
	if tenantID != "" || profile != "" {
		t.Errorf("con error devolvió (%q, %q), quería los dos vacíos", tenantID, profile)
	}
}

// caseResolveReadOnly: preguntar —resuelva, no resuelva o sea ambiguo— no escribe nada.
func caseResolveReadOnly(t *testing.T, m MontajeTenantResolver) {
	m.Seed(t, Session{TenantID: m.TenantA, EdgeID: edgeOne, SessionID: sessionOne, State: StateOnline, Profile: ProfileActive})
	m.Seed(t, Session{TenantID: m.TenantA, EdgeID: edgeTwo, SessionID: sessionTwo, State: StateOffline, Profile: ProfilePassive, SelfPnBidx: "indice-de-prueba"})
	m.Seed(t, Session{TenantID: m.TenantB, EdgeID: edgeOne, SessionID: sessionTwo, State: StateLoggedOut, Profile: ProfileActive})
	before := m.Sessions(t)
	if len(before) != 3 {
		t.Fatalf("Sessions devolvió %d filas tras sembrar 3: %v", len(before), before)
	}

	for _, sessionID := range []string{sessionOne, sessionTwo, "sess-que-no-existe"} {
		// El veredicto de cada una lo fijan los otros casos; aquí importa solo el rastro.
		if _, _, err := m.Resolver.ResolveTenant(t.Context(), sessionID); err != nil && !errors.Is(err, runtime.ErrTenantNotResolved) {
			t.Fatalf("ResolveTenant(%q): error inesperado %v", sessionID, err)
		}
	}
	requireUntouched(t, m.Sessions, before)
}
