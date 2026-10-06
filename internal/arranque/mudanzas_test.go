package arranque

// mudanzas_test.go — cubre el contrato de mudanzas.go: FaseActual y caraNueva contra
// testdata/mapa.tsv (F0 · TX.4, RX.3.a–c, RX.2.a).
//
// Cómo se enumera una cara sin poder listar un ServeMux (FX diseño §6): por cada fila
// del mapa se sintetiza una petición (el método del patrón, o GET si no lleva; cada
// comodín → «x») y se pregunta al Compuesto con Resolver, que dice qué cara la
// serviría y con qué patrón SIN servirla. Un patrón de más en la cara nueva lo
// detecta Cara.Patrones() (⊆ tabla); en la vieja, la huella y el panic de conflicto.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements/entitlementshelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/platformadmin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// filaDelMapa es una fila de testdata/mapa.tsv: id · listener · patrón · fase.
type filaDelMapa struct {
	id, listener, patron, fase string
}

// rutaDelMapa es la copia ejecutable de FX-cara-http/mapa-de-rutas.md.
const rutaDelMapa = "testdata/mapa.tsv"

// leerMapa lee testdata/mapa.tsv: cabecera «id\tlistener\tpatron\tfase» y cuatro campos
// por fila.
func leerMapa(t *testing.T) []filaDelMapa {
	t.Helper()
	datos, err := os.ReadFile(rutaDelMapa)
	if err != nil {
		t.Fatalf("mapa: %v", err)
	}
	lineas := strings.Split(strings.TrimSuffix(string(datos), "\n"), "\n")
	if lineas[0] != "id\tlistener\tpatron\tfase" {
		t.Fatalf("mapa %s: cabecera %q", rutaDelMapa, lineas[0])
	}
	filas := make([]filaDelMapa, 0, len(lineas)-1)
	for n, linea := range lineas[1:] {
		campos := strings.Split(linea, "\t")
		if len(campos) != 4 {
			t.Fatalf("mapa %s:%d: %d campos, se esperan 4 (id · listener · patrón · fase)", rutaDelMapa, n+2, len(campos))
		}
		filas = append(filas, filaDelMapa{campos[0], campos[1], campos[2], campos[3]})
	}
	return filas
}

// numeroDeFase convierte «F<n>» en n. «-» (una ruta del :8100 que no cambia) no es fase.
func numeroDeFase(fase string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimPrefix(fase, "F"))
	return n, err == nil && strings.HasPrefix(fase, "F")
}

var comodin = regexp.MustCompile(`\{[^}]*\}`)

// peticionDe sintetiza la petición que casa con un patrón: su método (GET si no lleva)
// y cada comodín sustituido por «x».
func peticionDe(patron string) *http.Request {
	metodo, camino := http.MethodGet, patron
	if i := strings.IndexAny(patron, " \t"); i >= 0 {
		metodo, camino = patron[:i], strings.TrimSpace(patron[i+1:])
	}
	return httptest.NewRequest(metodo, comodin.ReplaceAllString(camino, "x"), nil)
}

// caminoDe quita el método de un patrón: las filas con el mismo camino son una familia.
func caminoDe(patron string) string {
	if i := strings.IndexAny(patron, " \t"); i >= 0 {
		return strings.TrimSpace(patron[i+1:])
	}
	return patron
}

// problemasDeMudanza aplica el candado a un compuesto: cada fila del :8103 con fase ≤
// fase la sirve la cara nueva y el resto la vieja, las dos con el MISMO texto de patrón
// (RX.2.a, RX.3.a); una fila que la nueva le quita a la vieja con otro patrón es un
// solape roto (RX.3.b); una familia servida por las dos caras está partida (RX.3.c); y
// la cara nueva no registra nada que el mapa no le asigne. Devuelve una línea por fallo.
func problemasDeMudanza(filas []filaDelMapa, cara *apipublica.Cara, x *apipublica.Compuesto, fase int) []string {
	var problemas []string
	asignados := map[string]bool{}
	carasPorFamilia := map[string]map[string]bool{}
	for _, f := range filas {
		if f.listener != ":8103" {
			continue
		}
		n, ok := numeroDeFase(f.fase)
		if !ok {
			problemas = append(problemas, fmt.Sprintf("%s %q: fase %q no es F<n>", f.id, f.patron, f.fase))
			continue
		}
		esperada := "vieja"
		if n <= fase {
			esperada = "nueva"
			asignados[f.patron] = true
		}
		got, patron := x.Resolver(peticionDe(f.patron))
		switch {
		case got == "nueva" && esperada == "vieja" && patron != f.patron:
			problemas = append(problemas, fmt.Sprintf("solape roto: %s %q sigue en la vieja pero la nueva se lo lleva con %q", f.id, f.patron, patron))
		case got != esperada || patron != f.patron:
			problemas = append(problemas, fmt.Sprintf("%s %q: resuelve (%q, %q); se espera (%q, %q)", f.id, f.patron, got, patron, esperada, f.patron))
		}
		familia := caminoDe(f.patron)
		if carasPorFamilia[familia] == nil {
			carasPorFamilia[familia] = map[string]bool{}
		}
		carasPorFamilia[familia][got] = true
	}
	for familia, caras := range carasPorFamilia {
		if len(caras) > 1 {
			problemas = append(problemas, fmt.Sprintf("familia partida: %q la sirven %d caras", familia, len(caras)))
		}
	}
	for _, p := range cara.Patrones() {
		if !asignados[p] {
			problemas = append(problemas, fmt.Sprintf("la cara nueva registra %q y el mapa no se lo asigna con fase ≤ F%d", p, fase))
		}
	}
	slices.Sort(problemas)
	return problemas
}

// carasDePrueba monta un mapa de prueba: la vieja con todas sus filas, la nueva con las
// que se le dan. Los handlers no importan: Resolver no sirve.
func carasDePrueba(filas []filaDelMapa, enLaNueva ...string) (*apipublica.Cara, *apipublica.Compuesto) {
	nada := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	vieja := http.NewServeMux()
	for _, f := range filas {
		vieja.Handle(f.patron, nada)
	}
	cara := apipublica.Nueva()
	for _, p := range enLaNueva {
		cara.Handle(p, nada)
	}
	return cara, apipublica.Componer(cara, vieja)
}

// TestMudanzas_ElMapa fija la forma de testdata/mapa.tsv: las 95 rutas del arranque
// viejo (73 del :8103 + 22 del :8100), ids y patrones sin repetir, y una fase válida
// por fila («-» solo en el :8100, para lo que no cambia).
func TestMudanzas_ElMapa(t *testing.T) {
	filas := leerMapa(t)
	porListener := map[string]int{}
	ids, patrones := map[string]bool{}, map[string]bool{}
	for _, f := range filas {
		porListener[f.listener]++
		if ids[f.id] {
			t.Errorf("id repetido: %s", f.id)
		}
		ids[f.id] = true
		if clave := f.listener + " " + f.patron; patrones[clave] {
			t.Errorf("patrón repetido: %s", clave)
		} else {
			patrones[clave] = true
		}
		if _, ok := numeroDeFase(f.fase); !ok && (f.fase != "-" || f.listener != ":8100") {
			t.Errorf("%s: fase %q inválida en %s", f.id, f.fase, f.listener)
		}
	}
	if len(filas) != 95 || porListener[":8103"] != 73 || porListener[":8100"] != 22 {
		t.Errorf("mapa: %d filas (%d en :8103, %d en :8100); se esperan 95 = 73 + 22",
			len(filas), porListener[":8103"], porListener[":8100"])
	}
}

// TestMudanzas_FaseActual: con FaseActual la cara nueva (caraNueva, montada con dobles de
// TODAS sus dependencias, FX diseño §6) sirve exactamente las filas del :8103 con fase ≤
// FaseActual y no registra nada más (RX.3.a); el resto cae a la vieja. Desde F3
// (conmutar(edge)), FaseActual = 3 y la cara sirve 29 rutas: las 23 de acceso (A1–A7, B1–B14,
// C1–C2) y las 6 de edge (D1–D6).
func TestMudanzas_FaseActual(t *testing.T) {
	if FaseActual != 3 {
		t.Fatalf("FaseActual = %d; conmutar(edge) la deja en 3 y solo la sube la tarea conmutar(<m>) de la fase siguiente", FaseActual)
	}
	filas := leerMapa(t)
	cara := caraNueva(newFaceDepsWithDoubles())

	var del8103 []filaDelMapa
	var esperadas []string
	for _, f := range filas {
		if f.listener != ":8103" {
			continue
		}
		del8103 = append(del8103, f)
		if n, ok := numeroDeFase(f.fase); ok && n <= FaseActual {
			esperadas = append(esperadas, f.patron)
		}
	}
	if len(esperadas) != 29 {
		t.Errorf("el mapa da %d filas del :8103 con fase ≤ F%d; acceso muda 23 (A1–A7, B1–B14, C1–C2) y edge 6 (D1–D6): 29", len(esperadas), FaseActual)
	}
	patrones := cara.Patrones()
	slices.Sort(patrones)
	slices.Sort(esperadas)
	if !slices.Equal(patrones, esperadas) {
		t.Errorf("caraNueva registra:\n%s\ny el mapa le asigna:\n%s", strings.Join(patrones, "\n"), strings.Join(esperadas, "\n"))
	}

	// Sola, sin la vieja detrás: cada fila con fase ≤ FaseActual la resuelve la nueva con su
	// MISMO patrón; ninguna otra fila casa con nada de la nueva (ni por comodín).
	sinVieja := apipublica.Componer(cara, http.NewServeMux())
	for _, f := range del8103 {
		got, p := sinVieja.Resolver(peticionDe(f.patron))
		if n, _ := numeroDeFase(f.fase); n <= FaseActual {
			if got != "nueva" || p != f.patron {
				t.Errorf("%s %q: la cara nueva sola la resuelve como (%q, %q); se espera (\"nueva\", %q)", f.id, f.patron, got, p, f.patron)
			}
		} else if got != "" {
			t.Errorf("con FaseActual = %d la cara nueva resuelve %s %q (fase %s) como (%q, %q)", FaseActual, f.id, f.patron, f.fase, got, p)
		}
	}
	// Con la vieja entera detrás (las 73 filas): las de fase ≤ FaseActual por la nueva, el
	// resto por la vieja, sin solapes rotos ni familias partidas.
	if p := problemasDeMudanza(filas, cara, apipublica.Componer(cara, muxDe(del8103)), FaseActual); len(p) > 0 {
		t.Errorf("candado de mudanzas con FaseActual = %d:\n%s", FaseActual, strings.Join(p, "\n"))
	}
}

// newFaceDepsWithDoubles devuelve las dependencias de caraNueva con un doble NO nil en cada
// campo que enciende rutas: así se monta todo lo que la fase puede montar. Los dobles no se
// llaman nunca (Resolver no sirve): cada uno es un valor que satisface su puerto.
func newFaceDepsWithDoubles() newFaceDeps {
	var signupStore struct {
		platformadmin.AccessRequestStore
	}
	return newFaceDeps{
		common: apipublica.Common{
			MW:      httpapi.NewMiddleware(nil, nil),
			Auditor: struct{ httpapi.AuditRecorder }{},
			Log:     quietLogger(),
		},
		auth: apipublica.AuthDeps{
			Verifier:       struct{ in.TokenVerifier }{},
			Exchanger:      struct{ in.Exchanger }{},
			Redeemer:       struct{ in.InvitationRedeemer }{},
			TenantSelector: struct{ in.ActiveTenantSelector }{},
			TenantLister:   struct{ in.TenantLister }{},
			SignupRequests: signupStore,
			M2M:            struct{ out.IdentityM2MClient }{},
		},
		rolePlane: apipublica.RolePlaneDeps{
			Roles:       struct{ in.RoleAdmin }{},
			Members:     struct{ in.MembershipAdmin }{},
			Invitations: struct{ in.InvitationAdmin }{},
		},
		audit:        apipublica.AuditDeps{Audit: struct{ apipublica.AuditReader }{}},
		entitlements: apipublica.EntitlementsDeps{Entitlements: entitlementshelpertest.NewFake()},
		// F3 · edge: D1 se monta siempre; D2–D4 cada una con su almacén; D5–D6 con los tres.
		messages: apipublica.MessagesDeps{
			Sender:   struct{ apipublica.MessageSender }{},
			Sessions: struct{ apipublica.SessionLister }{},
		},
		sessions: apipublica.SessionsDeps{
			Sessions:        struct{ apipublica.SessionLister }{},
			SessionProfiles: struct{ apipublica.SessionProfileStore }{},
			ProfilePush:     struct{ apipublica.ProfilePusher }{},
			SessionStatus:   struct{ apipublica.SessionStatusStore }{},
		},
		diagnostics: apipublica.DiagnosticsDeps{
			Diagnostics: struct{ apipublica.DiagnosticsStore }{},
			DiagnosticsRequester: struct {
				apipublica.DiagnosticsRequester
			}{},
			Sessions: struct{ apipublica.SessionLister }{},
		},
	}
}

// muxDe registra las filas en un ServeMux con un handler vacío.
func muxDe(filas []filaDelMapa) *http.ServeMux {
	mux := http.NewServeMux()
	for _, f := range filas {
		mux.Handle(f.patron, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	}
	return mux
}

// TestMudanzas_Coherente es el control: un mapa de prueba bien mudado no da ningún fallo,
// para que los dos casos que siguen fallen por lo que prueban y no por el mapa.
func TestMudanzas_Coherente(t *testing.T) {
	filas := []filaDelMapa{
		{"X1", ":8103", "GET /api/v1/cosas/{id}", "F2"},
		{"X2", ":8103", "DELETE /api/v1/cosas/{id}", "F2"},
		{"X3", ":8103", "GET /api/v1/otras", "F3"},
	}
	cara, x := carasDePrueba(filas, "GET /api/v1/cosas/{id}", "DELETE /api/v1/cosas/{id}")
	if p := problemasDeMudanza(filas, cara, x, 2); len(p) > 0 {
		t.Errorf("un mapa coherente da fallos:\n%s", strings.Join(p, "\n"))
	}
}

// TestMudanzas_SolapeRoto (RX.3.b): la fase 2 muda un comodín mientras el literal que
// solapa con él sigue en la vieja → la cara nueva se lleva el literal y el candado falla.
func TestMudanzas_SolapeRoto(t *testing.T) {
	filas := []filaDelMapa{
		{"X1", ":8103", "GET /api/v1/cosas/{id}", "F2"},
		{"X2", ":8103", "GET /api/v1/cosas/especial", "F3"},
	}
	cara, x := carasDePrueba(filas, "GET /api/v1/cosas/{id}")
	p := problemasDeMudanza(filas, cara, x, 2)
	if !slices.ContainsFunc(p, func(s string) bool { return strings.HasPrefix(s, "solape roto: X2") }) {
		t.Errorf("el solape del comodín mudado sobre el literal viejo no falla; problemas: %q", p)
	}
}

// TestMudanzas_FamiliaPartida (RX.3.c): mismo camino, dos métodos, cada uno en una cara
// → el candado falla aunque cada fila, por separado, resuelva donde dice el mapa.
func TestMudanzas_FamiliaPartida(t *testing.T) {
	filas := []filaDelMapa{
		{"Y1", ":8103", "GET /api/v1/y", "F2"},
		{"Y2", ":8103", "POST /api/v1/y", "F3"},
	}
	cara, x := carasDePrueba(filas, "GET /api/v1/y")
	p := problemasDeMudanza(filas, cara, x, 2)
	if !slices.Equal(p, []string{`familia partida: "/api/v1/y" la sirven 2 caras`}) {
		t.Errorf("la familia partida no falla (o falla por otra cosa): %q", p)
	}
}

// TestMudanzas_SobraEnLaNueva: la cara nueva registra un patrón que el mapa no le da.
func TestMudanzas_SobraEnLaNueva(t *testing.T) {
	filas := []filaDelMapa{{"X1", ":8103", "GET /api/v1/cosas", "F3"}}
	cara, x := carasDePrueba(filas, "GET /api/v1/extra")
	p := problemasDeMudanza(filas, cara, x, 2)
	if !slices.Contains(p, `la cara nueva registra "GET /api/v1/extra" y el mapa no se lo asigna con fase ≤ F2`) {
		t.Errorf("el patrón de más en la cara nueva no falla: %q", p)
	}
}

// TestMudanzas_HuellaPorElCompuesto (RX.2.a): sobre el arranque NUEVO real (el contenedor
// de la huella, en los dos perfiles), las 73 filas del :8103 las resuelve el compuesto que
// sirve publicSrv —cada una por la cara que le toca con FaseActual y con el MISMO texto de
// patrón que el arranque viejo— y el candado de mudanzas no da ningún fallo, mirando los
// Patrones() de la cara REAL que la fase 8 compuso (no de una rearmada aquí).
func TestMudanzas_HuellaPorElCompuesto(t *testing.T) {
	filas := leerMapa(t)
	for _, perfil := range perfilesDeHuella {
		t.Run(perfil, func(t *testing.T) {
			c := contenedorDeHuella(t, perfil)
			if c.publicCompuesto == nil || c.publicCara == nil {
				t.Fatal("la fase 8 no guardó el compuesto del :8103 (o su cara nueva) en el contenedor")
			}
			resueltas := 0
			for _, f := range filas {
				if f.listener != ":8103" {
					continue
				}
				if cara, p := c.publicCompuesto.Resolver(peticionDe(f.patron)); cara != "" && p == f.patron {
					resueltas++
				}
			}
			if resueltas != 73 {
				t.Errorf("el compuesto resuelve %d de las 73 rutas del :8103 con su mismo patrón", resueltas)
			}
			if p := problemasDeMudanza(filas, c.publicCara, c.publicCompuesto, FaseActual); len(p) > 0 {
				t.Errorf("candado de mudanzas sobre el arranque real:\n%s", strings.Join(p, "\n"))
			}
		})
	}
}
