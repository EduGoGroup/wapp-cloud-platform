package runtimehelpertest

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
)

// Los números de los casos, en su forma CANÓNICA (E.164 sin «+» ni separadores), y uno de ellos
// como lo escribiría una persona.
const (
	numberOne        = "573001112233"
	numberOneSpelled = "+57 (300) 111-2233"
	numberTwo        = "56984467443"
)

// MontajeSelfNumbers es lo que cada implementación entrega a ContratoSelfNumbers para UN caso: la
// suite llama a nuevo una vez por caso y no limpia nada entre llamadas. Todos los campos son
// obligatorios.
//
// El puerto solo pregunta, y pregunta por un índice que la suite no sabe calcular: por eso el
// Montaje trae la siembra, el observador y los DOS índices ciegos, el de la clave del Checker y el
// de otra clave (la trampa T-3: un segundo KeyProvider).
type MontajeSelfNumbers struct {
	// Checker es la implementación bajo prueba, construida con el KeyProvider de BlindIndex.
	Checker runtime.SelfNumberChecker
	// NoKeyProvider es la misma implementación sobre los MISMOS datos, construida sin KeyProvider.
	NoKeyProvider runtime.SelfNumberChecker
	// TenantA y TenantB son dos tenants distintos, UUID bien formados y sin ninguna sesión. Con
	// Postgres son filas reales de public.tenants (fleet_sessions las exige por clave foránea).
	TenantA, TenantB string
	// BlindIndex es el índice ciego con la MISMA clave que usa Checker: lo que escribiría la flota
	// en self_pn_bidx para ese tenant y ese número ya normalizado. No normaliza.
	BlindIndex func(tenantID, normalizedNumber string) string
	// OtherBlindIndex es el índice ciego con OTRA clave: el de un KeyProvider que no es el de
	// Checker. Para un mismo tenant y número tiene que dar un valor distinto del de BlindIndex.
	OtherBlindIndex func(tenantID, normalizedNumber string) string
	// Seed deja esa sesión en la tabla; si ya había una con su misma clave (tenant, edge,
	// sesión), la sustituye. Un Profile vacío NO escribe la columna (cae al DEFAULT del esquema)
	// y un SelfPnBidx vacío la deja sin número (NULL). Falla el test t si no puede sembrar.
	Seed func(t *testing.T, s Session)
	// AllowAnyProfile retira lo que impide sembrar un perfil fuera del dominio active|passive
	// (con Postgres, el CHECK fleet_sessions_profile_chk de la 0063), para el resto del caso.
	AllowAnyProfile func(t *testing.T)
	// Sessions devuelve TODAS las filas de la tabla, ordenadas por tenant, edge y sesión, con el
	// perfil que de verdad quedó guardado (nunca vacío).
	Sessions func(t *testing.T) []Session
}

// ContratoSelfNumbers ejecuta las promesas de runtime.PostgresSelfNumbers contra la implementación
// que devuelve nuevo, con un Montaje limpio por caso (nuevo se llama una vez por t.Run). No salta
// nada.
//
// 🔴 Lo que esta suite NO puede afirmar: que quien ESCRIBE el índice (la flota, al guardar el
// número propio) normalice y use la misma clave que quien lo consulta. Aquí el índice lo fabrica
// el Montaje y lo interroga el Checker: entre los dos no pasa ni una línea de la flota. Esa
// simetría la custodian la suite de la flota (fleethelpertest) y el proceso de F9 que empareja una
// sesión y le escribe desde su propio número. Lo que sí fija esta suite es la semántica de la
// CONSULTA: perfil, estado, agregación por número, aislamiento por tenant y clave del índice.
func ContratoSelfNumbers(t *testing.T, nuevo func(t *testing.T) MontajeSelfNumbers) {
	t.Helper()
	if nuevo == nil {
		t.Fatal("runtimehelpertest.ContratoSelfNumbers: nuevo es nil; hace falta una función que devuelva un MontajeSelfNumbers")
	}
	for _, c := range selfNumbersCases() {
		t.Run(c.name, func(t *testing.T) {
			m := nuevo(t)
			validateSelfNumbersMontaje(t, m)
			c.run(t, m)
		})
	}
}

// validateSelfNumbersMontaje exige lo que la suite da por hecho del Montaje: además de la parte
// común, los dos Checker, y dos índices deterministas, salados con el tenant, que no dejan ver el
// número y que difieren entre sí.
func validateSelfNumbersMontaje(t *testing.T, m MontajeSelfNumbers) {
	t.Helper()
	switch {
	case m.Checker == nil:
		t.Fatal("MontajeSelfNumbers.Checker es nil")
	case m.NoKeyProvider == nil:
		t.Fatal("MontajeSelfNumbers.NoKeyProvider es nil")
	case m.BlindIndex == nil || m.OtherBlindIndex == nil:
		t.Fatal("MontajeSelfNumbers: BlindIndex y OtherBlindIndex son obligatorios")
	}
	validateFleetMontaje(t, m.TenantA, m.TenantB, m.Seed, m.AllowAnyProfile, m.Sessions)

	index := m.BlindIndex(m.TenantA, numberOne)
	switch {
	case index == "" || strings.Contains(index, numberOne):
		t.Fatalf("MontajeSelfNumbers.BlindIndex = %q: tiene que ser un índice no vacío que no deje ver el número", index)
	case index != m.BlindIndex(m.TenantA, numberOne):
		t.Fatal("MontajeSelfNumbers.BlindIndex no es determinista")
	case index == m.BlindIndex(m.TenantB, numberOne):
		t.Fatal("MontajeSelfNumbers.BlindIndex no va salado con el tenant: dos tenants comparten índice")
	case index == m.BlindIndex(m.TenantA, numberOneSpelled):
		t.Fatal("MontajeSelfNumbers.BlindIndex normaliza: tiene que ser un HMAC sobre los bytes que recibe")
	case index == m.OtherBlindIndex(m.TenantA, numberOne):
		t.Fatal("MontajeSelfNumbers.OtherBlindIndex da el mismo índice que BlindIndex: tiene que ser de otra clave")
	}
}

// selfNumbersCase es una promesa del adaptador: su nombre (el del t.Run) y quien la afirma.
type selfNumbersCase struct {
	name string
	run  func(t *testing.T, m MontajeSelfNumbers)
}

// selfNumbersCases es la tabla de la suite. El comentario de cada fila es la promesa que fija.
func selfNumbersCases() []selfNumbersCase {
	return []selfNumbersCase{
		{"ActiveSessionNumber_IsSelf", caseSelfActive},                                // activa ⇒ bloquea
		{"PassiveSessionNumber_IsNotSelf", caseSelfPassive},                           // pasiva ⇒ no bloquea
		{"ProfileLeftToTheDefault_IsNotSelf", caseSelfDefaultProfile},                 // DEFAULT passive
		{"UnknownNumber_IsNotSelf", caseSelfUnknownNumber},                            // grupo vacío ⇒ false
		{"LoggedOutSession_IsNotSelf", caseSelfLoggedOut},                             // state <> 'loggedout'
		{"OfflineSession_StillBlocks", caseSelfOffline},                               // …y no state = 'online'
		{"PassiveLiveSessionWithAnActiveLoggedOutGhost_IsNotSelf", caseSelfGhost},     // por número, no por fila
		{"SameNumberActiveOnTwoEdges_IsSelf", caseSelfTwoEdgesActive},                 // agrega entre edges
		{"PassiveOnOneEdgeActiveOnAnother_IsSelf", caseSelfTwoEdgesMixed},             // basta una no pasiva
		{"ProfileOutOfDomain_Blocks", caseSelfUnknownProfile},                         // <> 'passive', no = 'active'
		{"NumberOfAnotherTenant_IsNotSelf", caseSelfOtherTenant},                      // INV-8
		{"IndexWrittenWithAnotherKeyProvider_NeverMatches", caseSelfOtherKeyProvider}, // T-3: muda, sin error
		{"NumberNotNormalized_DoesNotMatch", caseSelfNotNormalized},                   // RT-7: aquí no se normaliza
		{"SessionWithoutNumber_NeverMatches", caseSelfSessionWithoutNumber},           // bidx NULL
		{"EmptyNumber_FalseWithoutError", caseSelfEmptyNumber},                        // sin pregunta
		{"NoKeyProvider_ErrorNotASilentFalse", caseSelfNoKeyProvider},                 // centinela
		{"CancelledContext_WrappedErrorWithoutTheNumber", caseSelfCancelledContext},   // fallo de la base, sin PII
		{"Asking_LeavesTheTableUntouched", caseSelfReadOnly},                          // solo lee
	}
}

// seedNumber siembra en edgeOne una sesión del tenant con ese estado y perfil cuyo número propio
// es number (ya normalizado), indexado con la clave del Checker.
func seedNumber(t *testing.T, m MontajeSelfNumbers, tenantID, sessionID, state, profile, number string) {
	t.Helper()
	m.Seed(t, Session{TenantID: tenantID, EdgeID: edgeOne, SessionID: sessionID, State: state, Profile: profile, SelfPnBidx: m.BlindIndex(tenantID, number)})
}

// requireSelf afirma el veredicto del Checker para ese número, sin error.
func requireSelf(t *testing.T, m MontajeSelfNumbers, tenantID, number string, want bool, why string) {
	t.Helper()
	got, err := m.Checker.IsSelfNumber(t.Context(), tenantID, number)
	if err != nil {
		t.Fatalf("IsSelfNumber: error inesperado %v", err)
	}
	if got != want {
		t.Errorf("IsSelfNumber = %v, quería %v: %s", got, want, why)
	}
}

// caseSelfActive: el número de una sesión activa y viva es propio: bloquea el bucle.
func caseSelfActive(t *testing.T, m MontajeSelfNumbers) {
	seedNumber(t, m, m.TenantA, sessionOne, StateOnline, ProfileActive, numberOne)
	requireSelf(t, m, m.TenantA, numberOne, true, "el número de una sesión activa debe bloquear")
}

// caseSelfPassive: una pasiva nunca auto-responde, así que su número no puede cerrar un bucle; y
// no bloquearlo es lo que deja a una sesión activa atender al teléfono personal del mismo tenant.
// Con una activa de OTRO número al lado, cada número tiene su veredicto.
func caseSelfPassive(t *testing.T, m MontajeSelfNumbers) {
	seedNumber(t, m, m.TenantA, sessionOne, StateOnline, ProfilePassive, numberOne)
	requireSelf(t, m, m.TenantA, numberOne, false, "un tenant solo con sesiones pasivas no bloquea nada")

	seedNumber(t, m, m.TenantA, sessionTwo, StateOnline, ProfileActive, numberTwo)
	requireSelf(t, m, m.TenantA, numberOne, false, "el número de la pasiva no bloquea aunque otro número sí")
	requireSelf(t, m, m.TenantA, numberTwo, true, "el número de la activa bloquea")
}

// caseSelfDefaultProfile: una sesión recién emparejada, sin perfil escrito, nace pasiva (0063,
// D-07): su número no bloquea hasta que su dueño la activa.
func caseSelfDefaultProfile(t *testing.T, m MontajeSelfNumbers) {
	seedNumber(t, m, m.TenantA, sessionOne, StateOnline, "", numberOne)
	requireSelf(t, m, m.TenantA, numberOne, false, "una sesión con el perfil por defecto es pasiva")
}

// caseSelfUnknownNumber: un número que no está en la tabla no es propio (el agregado sobre cero
// filas es NULL, que se lee como false), con la tabla vacía y con otros números sembrados.
func caseSelfUnknownNumber(t *testing.T, m MontajeSelfNumbers) {
	requireSelf(t, m, m.TenantA, numberOne, false, "con la tabla vacía ningún número es propio")
	seedNumber(t, m, m.TenantA, sessionOne, StateOnline, ProfileActive, numberTwo)
	requireSelf(t, m, m.TenantA, numberOne, false, "un número que no está en la tabla no es propio")
}

// caseSelfLoggedOut: una sesión retirada no vuelve sin re-emparejar: no auto-responde y no puede
// cerrar un bucle, así que su número no bloquea aunque quedara activa.
func caseSelfLoggedOut(t *testing.T, m MontajeSelfNumbers) {
	seedNumber(t, m, m.TenantA, sessionOne, StateLoggedOut, ProfileActive, numberOne)
	requireSelf(t, m, m.TenantA, numberOne, false, "una sesión loggedout no aporta su número")
}

// caseSelfOffline es la no-regresión contra la «optimización» equivocada: offline es el stream
// caído y RECUPERABLE, la sesión auto-responde en cuanto reconecta, así que su número sí puede
// cerrar un bucle. Falla si alguien estrecha el filtro a state = 'online'.
func caseSelfOffline(t *testing.T, m MontajeSelfNumbers) {
	seedNumber(t, m, m.TenantA, sessionOne, StateOffline, ProfileActive, numberOne)
	requireSelf(t, m, m.TenantA, numberOne, true, "una sesión activa offline sigue bloqueando su número")
}

// caseSelfGhost es el fallo que motivó decidir POR NÚMERO: el número está en dos filas, la sesión
// viva ya marcada pasiva y la fila muerta de un emparejamiento anterior, que quedó activa. Marcar
// pasiva desde la consola tiene que surtir efecto: el número no bloquea.
func caseSelfGhost(t *testing.T, m MontajeSelfNumbers) {
	seedNumber(t, m, m.TenantA, sessionOne, StateOnline, ProfilePassive, numberOne)
	seedNumber(t, m, m.TenantA, sessionTwo, StateLoggedOut, ProfileActive, numberOne)
	requireSelf(t, m, m.TenantA, numberOne, false, "una fila loggedout activa no mantiene bloqueado el número de una viva pasiva")
}

// caseSelfTwoEdgesActive: el mismo número activo en dos edges bloquea (el agregado abarca todas
// las filas del número).
func caseSelfTwoEdgesActive(t *testing.T, m MontajeSelfNumbers) {
	index := m.BlindIndex(m.TenantA, numberOne)
	m.Seed(t, Session{TenantID: m.TenantA, EdgeID: edgeOne, SessionID: sessionOne, State: StateOnline, Profile: ProfileActive, SelfPnBidx: index})
	m.Seed(t, Session{TenantID: m.TenantA, EdgeID: edgeTwo, SessionID: sessionTwo, State: StateOnline, Profile: ProfileActive, SelfPnBidx: index})
	requireSelf(t, m, m.TenantA, numberOne, true, "el mismo número activo en dos edges debe bloquear")
}

// caseSelfTwoEdgesMixed: pasiva en un edge y activa en otro bloquea, esté la pasiva donde esté: si
// alguna sesión viva de ese número auto-responde, el bucle es posible.
func caseSelfTwoEdgesMixed(t *testing.T, m MontajeSelfNumbers) {
	index := m.BlindIndex(m.TenantA, numberOne)
	for _, passiveEdge := range []string{edgeOne, edgeTwo} {
		for _, edge := range []string{edgeOne, edgeTwo} {
			profile := ProfileActive
			if edge == passiveEdge {
				profile = ProfilePassive
			}
			m.Seed(t, Session{TenantID: m.TenantA, EdgeID: edge, SessionID: sessionOne, State: StateOnline, Profile: profile, SelfPnBidx: index})
		}
		requireSelf(t, m, m.TenantA, numberOne, true, "un número con alguna sesión viva no pasiva debe bloquear (pasiva en "+passiveEdge+")")
	}
}

// caseSelfUnknownProfile: un perfil fuera del dominio BLOQUEA (no sabemos si auto-responde:
// conservador hacia bloquear). Es lo único que separa `profile <> 'passive'` de `profile =
// 'active'`, y falla hacia el lado CONTRARIO que el caso gemelo de ContratoTenantResolver.
func caseSelfUnknownProfile(t *testing.T, m MontajeSelfNumbers) {
	m.AllowAnyProfile(t)
	seedNumber(t, m, m.TenantA, sessionOne, StateOnline, "supervisor", numberOne)
	requireSelf(t, m, m.TenantA, numberOne, true, "un perfil desconocido debe seguir bloqueando su número")
}

// caseSelfOtherTenant fija el aislamiento (INV-8) por sus dos mitades: el número propio de A no
// bloquea en B (el índice va salado con el tenant), y una fila de B que llevara el índice de A
// tampoco bloquea en A (la consulta filtra por tenant_id).
func caseSelfOtherTenant(t *testing.T, m MontajeSelfNumbers) {
	seedNumber(t, m, m.TenantA, sessionOne, StateOnline, ProfileActive, numberOne)
	requireSelf(t, m, m.TenantA, numberOne, true, "el número propio de A bloquea en A")
	requireSelf(t, m, m.TenantB, numberOne, false, "el número propio de A no bloquea en B")

	m.Seed(t, Session{TenantID: m.TenantB, EdgeID: edgeOne, SessionID: sessionTwo, State: StateOnline, Profile: ProfileActive, SelfPnBidx: m.BlindIndex(m.TenantA, numberTwo)})
	requireSelf(t, m, m.TenantA, numberTwo, false, "una fila de B con el índice de A no bloquea en A")
	requireSelf(t, m, m.TenantB, numberTwo, false, "…ni en B, donde ese número da otro índice")
}

// caseSelfOtherKeyProvider es la trampa T-3: si la fila se escribió con OTRO KeyProvider, el
// índice que calcula el Checker no casa con el guardado y la guarda queda MUDA: (false, nil), sin
// un solo error. Por eso el arranque tiene que pasar al runtime el mismo KeyProvider que a la
// flota. La segunda mitad demuestra que la primera no pasa en verde porque nada case nunca.
func caseSelfOtherKeyProvider(t *testing.T, m MontajeSelfNumbers) {
	m.Seed(t, Session{TenantID: m.TenantA, EdgeID: edgeOne, SessionID: sessionOne, State: StateOnline, Profile: ProfileActive, SelfPnBidx: m.OtherBlindIndex(m.TenantA, numberOne)})
	requireSelf(t, m, m.TenantA, numberOne, false, "un índice escrito con otra clave no puede casar")

	seedNumber(t, m, m.TenantA, sessionOne, StateOnline, ProfileActive, numberOne)
	requireSelf(t, m, m.TenantA, numberOne, true, "la misma fila con el índice de la clave del Checker sí casa")
}

// caseSelfNotNormalized fija RT-7 por el lado del adaptador: NO normaliza. El índice se sembró
// sobre la forma canónica; preguntar por el mismo teléfono con adornos da otro índice y no casa.
// Normalizar es del llamante, y si el adaptador «ayudara» normalizando con otra función, la
// simetría con quien escribe el índice dejaría de estar en un solo sitio.
func caseSelfNotNormalized(t *testing.T, m MontajeSelfNumbers) {
	seedNumber(t, m, m.TenantA, sessionOne, StateOnline, ProfileActive, numberOne)
	requireSelf(t, m, m.TenantA, numberOne, true, "la forma canónica del número propio bloquea")
	requireSelf(t, m, m.TenantA, numberOneSpelled, false, "el adaptador no normaliza: con adornos el índice es otro")
}

// caseSelfSessionWithoutNumber: una sesión activa sin número conocido (índice NULL) no hace
// propio a ningún número.
func caseSelfSessionWithoutNumber(t *testing.T, m MontajeSelfNumbers) {
	m.Seed(t, Session{TenantID: m.TenantA, EdgeID: edgeOne, SessionID: sessionOne, State: StateOnline, Profile: ProfileActive})
	requireSelf(t, m, m.TenantA, numberOne, false, "una sesión sin número no hace propio a ninguno")
}

// caseSelfEmptyNumber: sin número no hay pregunta: (false, nil), haya o no sesiones sin número, y
// también sin KeyProvider (el número vacío se mira ANTES que la clave).
func caseSelfEmptyNumber(t *testing.T, m MontajeSelfNumbers) {
	m.Seed(t, Session{TenantID: m.TenantA, EdgeID: edgeOne, SessionID: sessionOne, State: StateOnline, Profile: ProfileActive})
	seedNumber(t, m, m.TenantA, sessionTwo, StateOnline, ProfileActive, numberOne)

	for name, checker := range map[string]runtime.SelfNumberChecker{"Checker": m.Checker, "NoKeyProvider": m.NoKeyProvider} {
		if self, err := checker.IsSelfNumber(t.Context(), m.TenantA, ""); self || err != nil {
			t.Errorf("%s.IsSelfNumber(\"\") = (%v, %v), quería (false, nil)", name, self, err)
		}
	}
}

// caseSelfNoKeyProvider: sin KeyProvider la pregunta no tiene respuesta. Devuelve el centinela, no
// un false mudo, aunque el número SÍ sea propio: quien decide qué hacer es el llamante.
func caseSelfNoKeyProvider(t *testing.T, m MontajeSelfNumbers) {
	seedNumber(t, m, m.TenantA, sessionOne, StateOnline, ProfileActive, numberOne)

	self, err := m.NoKeyProvider.IsSelfNumber(t.Context(), m.TenantA, numberOne)
	if !errors.Is(err, runtime.ErrSelfNumbersNoKeyProvider) {
		t.Fatalf("error = %v, quería ErrSelfNumbersNoKeyProvider", err)
	}
	if self {
		t.Error("con error devolvió true")
	}
}

// caseSelfCancelledContext: con el contexto cancelado la consulta falla: (false, error) envuelto
// tras su prefijo, que no es el centinela de la clave y no lleva ni el número ni su índice.
func caseSelfCancelledContext(t *testing.T, m MontajeSelfNumbers) {
	seedNumber(t, m, m.TenantA, sessionOne, StateOnline, ProfileActive, numberOne)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	self, err := m.Checker.IsSelfNumber(ctx, m.TenantA, numberOne)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, quería uno que envuelva context.Canceled", err)
	}
	if self {
		t.Error("con error devolvió true")
	}
	if errors.Is(err, runtime.ErrSelfNumbersNoKeyProvider) {
		t.Errorf("un fallo de la consulta casa con ErrSelfNumbersNoKeyProvider: %v", err)
	}
	if prefix := "self_numbers: consulta fleet_sessions: "; !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("texto del error = %q, quería el prefijo %q", err.Error(), prefix)
	}
	for _, leak := range []string{numberOne, m.BlindIndex(m.TenantA, numberOne)} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("el mensaje de error lleva %q: %s", leak, err.Error())
		}
	}
}

// caseSelfReadOnly: preguntar —sea propio, no lo sea o falte la clave— no escribe nada.
func caseSelfReadOnly(t *testing.T, m MontajeSelfNumbers) {
	seedNumber(t, m, m.TenantA, sessionOne, StateOnline, ProfileActive, numberOne)
	seedNumber(t, m, m.TenantA, sessionTwo, StateOffline, ProfilePassive, numberTwo)
	seedNumber(t, m, m.TenantB, sessionOne, StateLoggedOut, ProfileActive, numberOne)
	before := m.Sessions(t)
	if len(before) != 3 {
		t.Fatalf("Sessions devolvió %d filas tras sembrar 3: %v", len(before), before)
	}

	for _, tenantID := range []string{m.TenantA, m.TenantB} {
		for _, number := range []string{numberOne, numberTwo, "5491100000000", ""} {
			if _, err := m.Checker.IsSelfNumber(t.Context(), tenantID, number); err != nil {
				t.Fatalf("IsSelfNumber: error inesperado %v", err)
			}
			// El error de este es el centinela; aquí importa solo el rastro.
			if _, err := m.NoKeyProvider.IsSelfNumber(t.Context(), tenantID, number); err != nil && !errors.Is(err, runtime.ErrSelfNumbersNoKeyProvider) {
				t.Fatalf("IsSelfNumber sin KeyProvider: error inesperado %v", err)
			}
		}
	}
	requireUntouched(t, m.Sessions, before)
}
