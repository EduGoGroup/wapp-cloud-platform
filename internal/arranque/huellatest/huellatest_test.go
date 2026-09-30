package huellatest

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
)

// ── Candidatos ───────────────────────────────────────────────────────────────

// TestCandidatos: literales de Handle/HandleFunc de producción, de cualquier X, sin comillas,
// ordenados y sin duplicados; fuera los tests, lo que cuelga de testdata, los argumentos que
// no son literal, y un dir que no existe no es error.
func TestCandidatos(t *testing.T) {
	got, err := Candidatos("testdata/candidatos/arbol", "uno", "dos", "noexiste")
	if err != nil {
		t.Fatalf("Candidatos: error %v", err)
	}
	want := []string{
		"/global",                  // http.Handle: cualquier X
		"/healthz",                 // en uno y en dos: sale una vez
		"GET /admin/tenants",       // Handle
		"GET /api/v1/f/{resto...}", // HandleFunc
		"POST /api/v1/x/{id}",      // literal crudo
	}
	if !slices.Equal(got, want) {
		t.Errorf("Candidatos = %q, quiere %q (sin /solo-test del _test.go, sin /ignorada de testdata, sin la variable ni la concatenación)", got, want)
	}
}

// TestCandidatosSinDirs: solo dirs inexistentes → longitud 0 y error nil.
func TestCandidatosSinDirs(t *testing.T) {
	got, err := Candidatos("testdata/candidatos/arbol", "noexiste", "tampoco")
	if err != nil || len(got) != 0 {
		t.Errorf("Candidatos(dirs inexistentes) = %q, %v; quiere longitud 0 y error nil", got, err)
	}
}

// TestCandidatosNoParsea: un fichero que no parsea es un error que nombra su ruta.
func TestCandidatosNoParsea(t *testing.T) {
	raiz := t.TempDir()
	escribirFichero(t, filepath.Join(raiz, "roto", "roto.go"), "package roto\nfunc (\n")
	got, err := Candidatos(raiz, "roto")
	if err == nil {
		t.Fatalf("Candidatos(fichero roto) = %q, nil; quiere error", got)
	}
	if !strings.Contains(err.Error(), "roto.go") {
		t.Errorf("error %q no nombra el fichero roto.go", err)
	}
}

// ── Rutas ────────────────────────────────────────────────────────────────────

// muxSonda es el mux de TestRutas: una ruta por regla del contrato.
func muxSonda() *http.ServeMux {
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/tenants", ok)
	mux.HandleFunc("/healthz", ok)
	mux.HandleFunc("POST /api/v1/x/{id}", ok)
	mux.HandleFunc("GET /api/v1/f/{resto...}", ok)
	mux.HandleFunc("/propio-404", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no existe ese contacto", http.StatusNotFound)
	})
	mux.HandleFunc("/405-sin-allow", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusMethodNotAllowed)
	})
	mux.HandleFunc("/protegida", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	mux.HandleFunc("/panico", func(http.ResponseWriter, *http.Request) { panic("boom") })
	mux.HandleFunc("/como-mux", http.NotFound)
	return mux
}

// TestRutas: montada salvo el 404 del mux o un 405 con Allow; un 404 con otro cuerpo, un 405
// sin Allow, un 401 y un panic cuentan como montada; se devuelven los candidatos tal cual,
// ordenados; y las sondas van en el orden dado, una por candidato, con método, ruta y
// RemoteAddr del contrato.
func TestRutas(t *testing.T) {
	mux := muxSonda()
	var sondas []string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sondas = append(sondas, r.Method+" "+r.URL.Path+" "+r.RemoteAddr)
		mux.ServeHTTP(w, r)
	})
	candidatos := []string{
		"POST /api/v1/x/{id}",
		"/panico",
		"DELETE /admin/tenants",
		"GET /no-existe",
		"/healthz",
		"GET /api/v1/f/{resto...}",
		"/propio-404",
		"/405-sin-allow",
		"/protegida",
		"/como-mux",
		"GET /admin/tenants",
		"/admin/tenants",
	}
	got := Rutas(h, candidatos)
	want := []string{
		"/405-sin-allow",           // 405 sin Allow: montada
		"/admin/tenants",           // sin método → GET: el candidato sale tal cual
		"/healthz",                 // 200
		"/panico",                  // panic: montada, y la sonda sigue
		"/propio-404",              // 404 con otro cuerpo: montada
		"/protegida",               // 401: montada
		"GET /admin/tenants",       // 200
		"GET /api/v1/f/{resto...}", // comodín de resto
		"POST /api/v1/x/{id}",      // comodín
	}
	if !slices.Equal(got, want) {
		t.Errorf("Rutas = %q\nquiere   %q\n(fuera: DELETE /admin/tenants → 405 con Allow; GET /no-existe → 404 del mux; /como-mux → http.NotFound, limitación aceptada)", got, want)
	}
	const ip = "192.0.2.1:1234"
	wantSondas := []string{
		"POST /api/v1/x/id " + ip,
		"GET /panico " + ip,
		"DELETE /admin/tenants " + ip,
		"GET /no-existe " + ip,
		"GET /healthz " + ip,
		"GET /api/v1/f/resto " + ip,
		"GET /propio-404 " + ip,
		"GET /405-sin-allow " + ip,
		"GET /protegida " + ip,
		"GET /como-mux " + ip,
		"GET /admin/tenants " + ip,
		"GET /admin/tenants " + ip,
	}
	if !slices.Equal(sondas, wantSondas) {
		t.Errorf("sondas = %q\nquiere   %q\n(en el orden dado, una por candidato, {x} → x, RemoteAddr fijo)", sondas, wantSondas)
	}
}

// TestRutasNingunaMontada: nada montado → longitud 0; un candidato repetido sale una vez.
func TestRutasNingunaMontada(t *testing.T) {
	if got := Rutas(http.NewServeMux(), []string{"/a", "GET /b"}); len(got) != 0 {
		t.Errorf("Rutas(mux vacío) = %q, quiere longitud 0", got)
	}
	if got := Rutas(muxSonda(), []string{"/healthz", "/healthz"}); !slices.Equal(got, []string{"/healthz"}) {
		t.Errorf("Rutas(candidato repetido) = %q, quiere [/healthz]", got)
	}
}

// ── RPC ──────────────────────────────────────────────────────────────────────

// servicio es un grpc.ServiceDesc escrito a mano: HandlerType *any lo satisface cualquiera.
func servicio(nombre string, metodos, streams []string) *grpc.ServiceDesc {
	sd := &grpc.ServiceDesc{ServiceName: nombre, HandlerType: (*any)(nil)}
	for _, m := range metodos {
		sd.Methods = append(sd.Methods, grpc.MethodDesc{MethodName: m})
	}
	for _, s := range streams {
		sd.Streams = append(sd.Streams, grpc.StreamDesc{StreamName: s, ServerStreams: true, ClientStreams: true})
	}
	return sd
}

// servidor registra cada servicio en un grpc.Server que nunca escucha.
func servidor(t *testing.T, sds ...*grpc.ServiceDesc) *grpc.Server {
	t.Helper()
	gs := grpc.NewServer()
	t.Cleanup(gs.Stop)
	for _, sd := range sds {
		gs.RegisterService(sd, struct{}{})
	}
	return gs
}

// TestRPC: métodos unarios Y streams, "servicio/método", ordenados; sin servicios, longitud 0.
func TestRPC(t *testing.T) {
	gs := servidor(t,
		servicio("wapp.prueba.Zeta", []string{"Uno"}, nil),
		servicio("wapp.prueba.Eco", []string{"Decir"}, []string{"Escuchar"}),
	)
	got := RPC(gs)
	if want := []string{"wapp.prueba.Eco/Decir", "wapp.prueba.Eco/Escuchar", "wapp.prueba.Zeta/Uno"}; !slices.Equal(got, want) {
		t.Errorf("RPC = %q, quiere %q", got, want)
	}
	if got := RPC(servidor(t)); len(got) != 0 {
		t.Errorf("RPC(sin servicios) = %q, quiere longitud 0", got)
	}
}

// ── Metricas ─────────────────────────────────────────────────────────────────

const exposicion = `# HELP wapp_solo_help sin TYPE no cuenta
# HELP wapp_b_total ayuda
# TYPE wapp_b_total counter
wapp_b_total 1
# TYPE go_goroutines gauge
go_goroutines 7
# TYPE wapp_a gauge
wapp_a 2
# TYPE wappx_no gauge
# TYPE wapp_a gauge
`

// TestMetricas: UNA petición GET /metrics por el admin entero; solo `# TYPE wapp_…`,
// ordenadas y sin duplicados.
func TestMetricas(t *testing.T) {
	var peticiones []string
	admin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peticiones = append(peticiones, r.Method+" "+r.URL.Path)
		if _, err := io.WriteString(w, exposicion); err != nil {
			t.Error(err)
		}
	})
	got, err := Metricas(admin)
	if err != nil {
		t.Fatalf("Metricas: error %v", err)
	}
	if want := []string{"wapp_a", "wapp_b_total"}; !slices.Equal(got, want) {
		t.Errorf("Metricas = %q, quiere %q (sin HELP sueltos, sin go_*, sin wappx_, sin repetir)", got, want)
	}
	if want := []string{"GET /metrics"}; !slices.Equal(peticiones, want) {
		t.Errorf("peticiones = %q, quiere %q", peticiones, want)
	}
}

// TestMetricasError: un estado distinto de 200 es un error con el estado; un admin sin
// /metrics no es «cero familias».
func TestMetricasError(t *testing.T) {
	roto := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "fallo", http.StatusInternalServerError)
	})
	if got, err := Metricas(roto); err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("Metricas(500) = %q, %v; quiere un error que diga 500", got, err)
	}
	if got, err := Metricas(http.NewServeMux()); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("Metricas(admin sin /metrics) = %q, %v; quiere un error que diga 404", got, err)
	}
}

// adminConMetricas monta /metrics de un registro de prometheus con las familias dadas (y
// /healthz, para que sea un admin y no el handler suelto).
func adminConMetricas(familias ...string) http.Handler {
	reg := prometheus.NewRegistry()
	for _, f := range familias {
		g := prometheus.NewGauge(prometheus.GaugeOpts{Name: f, Help: "prueba"})
		reg.MustRegister(g)
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return mux
}

// ── Goroutines y Hooks ───────────────────────────────────────────────────────

// goroutinesBase es la huella estática de testdata/fondo/base.
var goroutinesBase = []string{
	"fondo.Arrancar.func", // go func(){…}()
	"fondo.Arrancar.func", // el literal dentro del literal cuenta como el de fuera
	"fondo.Worker.Lanzar.func",
	"fondo.Worker.Run", // receptor *Worker, sin puntero
	"fondo.tarea",
	"fondo.tarea", // multiconjunto: go tarea(1) y go tarea(2)
	"http.Server.ListenAndServe",
	"runtime.GC",
}

// TestGoroutines: las tres formas normalizadas, multiconjunto ordenado, sin los _test.go.
func TestGoroutines(t *testing.T) {
	got, err := Goroutines("testdata/fondo/base")
	if err != nil {
		t.Fatalf("Goroutines(base): error %v", err)
	}
	if !slices.Equal(got, goroutinesBase) {
		t.Errorf("Goroutines(base) = %q\nquiere %q", got, goroutinesBase)
	}
	got, err = Goroutines("testdata/fondo/muerde")
	if err != nil {
		t.Fatalf("Goroutines(muerde): error %v", err)
	}
	want := slices.Clone(goroutinesBase)
	want = slices.Insert(want, 4, "fondo.Worker.Run")
	if !slices.Equal(got, want) {
		t.Errorf("Goroutines(muerde) = %q\nquiere %q", got, want)
	}
}

// TestGoroutinesError: un dir inexistente, sin producción, que no parsea o que no comprueba
// tipos es un error, nunca «ninguna goroutine».
func TestGoroutinesError(t *testing.T) {
	soloTest := t.TempDir()
	escribirFichero(t, filepath.Join(soloTest, "a_test.go"), "package a\n")
	noParsea := t.TempDir()
	escribirFichero(t, filepath.Join(noParsea, "a.go"), "package a\nfunc (\n")
	noTipa := t.TempDir()
	escribirFichero(t, filepath.Join(noTipa, "a.go"), "package a\n\nfunc F() { go g() }\n")
	for nombre, dir := range map[string]string{
		"no existe":          filepath.Join(t.TempDir(), "noexiste"),
		"solo tests":         soloTest,
		"no parsea":          noParsea,
		"no comprueba tipos": noTipa,
	} {
		if got, err := Goroutines(dir); err == nil {
			t.Errorf("Goroutines(%s) = %q, nil; quiere error", nombre, got)
		}
	}
}

// TestHooks: selectores X.M con M en metodos (llamada o valor), multiconjunto ordenado, sin
// declaraciones de método ni _test.go; un nombre ausente no aporta.
func TestHooks(t *testing.T) {
	metodos := []string{"RegisterDBStats", "InstrumentHTTP", "NoExiste"}
	for _, dir := range []string{"testdata/fondo/base", "testdata/fondo/muerde"} {
		got, err := Hooks(dir, metodos)
		if err != nil {
			t.Fatalf("Hooks(%s): error %v", dir, err)
		}
		if want := []string{"InstrumentHTTP", "InstrumentHTTP", "RegisterDBStats"}; !slices.Equal(got, want) {
			t.Errorf("Hooks(%s) = %q, quiere %q", dir, got, want)
		}
	}
	got, err := Hooks("testdata/fondo/base", []string{"NoExiste"})
	if err != nil || len(got) != 0 {
		t.Errorf("Hooks(ninguno aparece) = %q, %v; quiere longitud 0 y error nil", got, err)
	}
	if got, err := Hooks(filepath.Join(t.TempDir(), "noexiste"), metodos); err == nil {
		t.Errorf("Hooks(dir inexistente) = %q, nil; quiere error", got)
	}
}

// ── Entorno ──────────────────────────────────────────────────────────────────

// TestEntorno: Getenv, LookupEnv y Environ (con alias), "<ruta>:<línea> os.<F>", ordenado;
// fuera Setenv, los tests, testdata e internal/platform/config; dirs inexistentes, sin error.
func TestEntorno(t *testing.T) {
	const raiz = "testdata/entorno/arbol"
	got, err := Entorno(raiz, "a", "b", "internal", "limpio", "noexiste")
	if err != nil {
		t.Fatalf("Entorno: error %v", err)
	}
	want := []string{
		"a/lee.go:11 os.Environ",
		"a/lee.go:8 os.Getenv",
		"a/lee.go:9 os.LookupEnv",
		"b/alias.go:7 os.Getenv", // sistema.Getenv
		"internal/platform/otro/otro.go:7 os.LookupEnv",
	}
	if !slices.Equal(got, want) {
		t.Errorf("Entorno = %q\nquiere   %q", got, want)
	}
	got, err = Entorno(raiz, "internal/platform/config", "limpio")
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("Entorno(sin lecturas) = %#v, %v; quiere lista vacía NO nil y error nil", got, err)
	}
}

// TestEntornoNoParsea: un fichero que no parsea es un error que nombra su ruta.
func TestEntornoNoParsea(t *testing.T) {
	raiz := t.TempDir()
	escribirFichero(t, filepath.Join(raiz, "x", "malo.go"), "package x\nfunc (\n")
	if got, err := Entorno(raiz, "x"); err == nil || !strings.Contains(err.Error(), "malo.go") {
		t.Errorf("Entorno(fichero roto) = %q, %v; quiere un error que nombre malo.go", got, err)
	}
}

// ── Leer y Escribir ──────────────────────────────────────────────────────────

// jsonMinimo son los bytes exactos de Escribir para huellaMinima: dos espacios, claves de
// mapa ordenadas, omitempty en la parte estática, salto de línea final.
const jsonMinimo = `[
  {
    "perfil": "minimo",
    "rutas": {
      ":8100": [
        "/healthz"
      ],
      ":8103": [
        "GET /api/v1/x"
      ]
    },
    "rpc": {
      ":8101": [
        "s/M"
      ]
    },
    "metricas": [
      "wapp_x"
    ]
  }
]
`

func huellaMinima() Huella {
	return Huella{
		Perfil:   "minimo",
		Rutas:    map[string][]string{":8103": {"GET /api/v1/x"}, ":8100": {"/healthz"}},
		RPC:      map[string][]string{":8101": {"s/M"}},
		Metricas: []string{"wapp_x"},
	}
}

// TestEscribir: bytes exactos, deterministas (dos escrituras iguales).
func TestEscribir(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.json"), filepath.Join(dir, "b.json")
	for _, ruta := range []string{a, b} {
		if err := Escribir(ruta, []Huella{huellaMinima()}); err != nil {
			t.Fatalf("Escribir(%s): %v", ruta, err)
		}
	}
	ba, bb := leerFichero(t, a), leerFichero(t, b)
	if string(ba) != jsonMinimo {
		t.Errorf("Escribir escribió\n%s\nquiere\n%s", ba, jsonMinimo)
	}
	if !bytes.Equal(ba, bb) {
		t.Error("dos escrituras de lo mismo dan bytes distintos")
	}
}

// huellaCompleta tiene todos los campos, con listas SIN ordenar: Escribir no las reordena.
func huellaCompleta() Huella {
	return Huella{
		Perfil:     "m2m",
		Rutas:      map[string][]string{":8100": {"/b", "/a"}},
		RPC:        map[string][]string{":8102": {"e/R"}},
		Metricas:   []string{"wapp_z", "wapp_a"},
		Goroutines: []string{"p.T.Run", "p.T.Run"},
		Hooks:      []string{"InstrumentHTTP"},
		Entorno:    []string{"a.go:1 os.Getenv"},
	}
}

// TestEscribirLeer: ida y vuelta con los dos perfiles, las huellas en orden y las listas sin
// reordenar; una lista omitempty vacía vuelve como nil. Escribir trunca.
func TestEscribirLeer(t *testing.T) {
	ruta := filepath.Join(t.TempDir(), "huella.json")
	if err := Escribir(ruta, []Huella{huellaMinima(), huellaMinima(), huellaMinima()}); err != nil {
		t.Fatalf("Escribir (primera): %v", err)
	}
	conVacia := huellaMinima()
	conVacia.Goroutines = []string{}
	if err := Escribir(ruta, []Huella{huellaCompleta(), conVacia}); err != nil {
		t.Fatalf("Escribir: %v", err)
	}
	got, err := Leer(ruta)
	if err != nil {
		t.Fatalf("Leer: %v", err)
	}
	want := []Huella{huellaCompleta(), huellaMinima()} // Goroutines []string{} → nil
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Leer(Escribir(hs)) = %#v\nquiere %#v", got, want)
	}
}

// TestLeerEscribirErrores: un fichero que no existe o no es JSON, y un directorio padre que
// no existe, son errores; los de Leer nombran la ruta.
func TestLeerEscribirErrores(t *testing.T) {
	dir := t.TempDir()
	noExiste := filepath.Join(dir, "noexiste.json")
	if got, err := Leer(noExiste); err == nil || !strings.Contains(err.Error(), "noexiste.json") {
		t.Errorf("Leer(no existe) = %v, %v; quiere un error que nombre la ruta", got, err)
	}
	basura := filepath.Join(dir, "basura.json")
	escribirFichero(t, basura, "{no es json")
	if got, err := Leer(basura); err == nil || !strings.Contains(err.Error(), "basura.json") {
		t.Errorf("Leer(no JSON) = %v, %v; quiere un error que nombre la ruta", got, err)
	}
	if err := Escribir(filepath.Join(dir, "sin", "padre.json"), nil); err == nil {
		t.Error("Escribir(padre inexistente) = nil; quiere error (no crea directorios)")
	}
}

// ── Diferencia ───────────────────────────────────────────────────────────────

// TestDiferencia: formatos exactos, multiconjuntos por número de apariciones, nil = vacía,
// listener ausente = lista vacía, líneas ordenadas.
func TestDiferencia(t *testing.T) {
	casos := []struct {
		nombre        string
		quiere, tiene Huella
		want          []string
	}{
		{
			nombre: "iguales: ninguna línea",
			quiere: huellaMinima(),
			tiene:  huellaMinima(),
			want:   nil,
		},
		{
			nombre: "nil, vacía y listener ausente son iguales",
			quiere: Huella{Perfil: "p", Rutas: map[string][]string{":8100": {}}, Goroutines: []string{}},
			tiene:  Huella{Perfil: "p", RPC: map[string][]string{":8101": nil}},
			want:   nil,
		},
		{
			nombre: "perfil distinto",
			quiere: Huella{Perfil: "minimo"},
			tiene:  Huella{Perfil: "m2m"},
			want:   []string{"perfil: quiere minimo, tiene m2m"},
		},
		{
			nombre: "rutas: falta y sobra por listener",
			quiere: Huella{Rutas: map[string][]string{":8100": {"GET /admin/tenants", "/healthz"}}},
			tiene:  Huella{Rutas: map[string][]string{":8100": {"/healthz"}, ":8103": {"/x"}}},
			want:   []string{":8100 falta GET /admin/tenants", ":8103 sobra /x"},
		},
		{
			nombre: "rpc",
			quiere: Huella{RPC: map[string][]string{":8101": {"svc/M", "svc/N"}}},
			tiene:  Huella{RPC: map[string][]string{":8101": {"svc/N"}, ":8102": {"e/R"}}},
			want:   []string{"rpc :8101 falta svc/M", "rpc :8102 sobra e/R"},
		},
		{
			nombre: "metricas",
			quiere: Huella{Metricas: []string{"wapp_a", "wapp_b"}},
			tiene:  Huella{Metricas: []string{"wapp_b", "wapp_c"}},
			want:   []string{"metricas falta wapp_a", "metricas sobra wapp_c"},
		},
		{
			nombre: "goroutines: multiconjunto, una línea por aparición",
			quiere: Huella{Goroutines: []string{"p.a", "p.a", "p.b"}},
			tiene:  Huella{Goroutines: []string{"p.a", "p.b", "p.b", "p.b"}},
			want:   []string{"goroutines falta p.a", "goroutines sobra p.b", "goroutines sobra p.b"},
		},
		{
			nombre: "hooks",
			quiere: Huella{Hooks: []string{"InstrumentHTTP", "RegisterDBStats"}},
			tiene:  Huella{Hooks: []string{"RegisterDBStats"}},
			want:   []string{"hooks falta InstrumentHTTP"},
		},
		{
			nombre: "entorno",
			quiere: Huella{},
			tiene:  Huella{Entorno: []string{"a/b.go:3 os.Getenv"}},
			want:   []string{"entorno sobra a/b.go:3 os.Getenv"},
		},
		{
			nombre: "todo a la vez: líneas ordenadas",
			quiere: Huella{Perfil: "a", Rutas: map[string][]string{":8103": {"/y"}}, Hooks: []string{"H"}},
			tiene:  Huella{Perfil: "b", Metricas: []string{"wapp_m"}, RPC: map[string][]string{":8101": {"s/M"}}},
			want: []string{
				":8103 falta /y",
				"hooks falta H",
				"metricas sobra wapp_m",
				"perfil: quiere a, tiene b",
				"rpc :8101 sobra s/M",
			},
		},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			got := Diferencia(c.quiere, c.tiene)
			if len(c.want) == 0 {
				if len(got) != 0 {
					t.Errorf("Diferencia = %q, quiere longitud 0", got)
				}
				return
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("Diferencia = %q\nquiere     %q", got, c.want)
			}
		})
	}
}

// TestDiferenciaPura: no toca sus argumentos (ni reordena sus listas).
func TestDiferenciaPura(t *testing.T) {
	desordenadas := func() (Huella, Huella) {
		return Huella{Perfil: "a", Rutas: map[string][]string{":8100": {"/z", "/a"}}, Goroutines: []string{"z", "a"}},
			Huella{Perfil: "b", Metricas: []string{"wapp_z", "wapp_a"}}
	}
	quiere, tiene := desordenadas()
	_ = Diferencia(quiere, tiene)
	copiaQ, copiaT := desordenadas()
	if !reflect.DeepEqual(quiere, copiaQ) || !reflect.DeepEqual(tiene, copiaT) {
		t.Errorf("Diferencia modificó sus argumentos: quiere=%#v tiene=%#v", quiere, tiene)
	}
}

// TestDiferenciaMuerde (T0.12): cada componente calculado de verdad, con UNA pieza de más o de
// menos, exige exactamente su línea en Diferencia.
func TestDiferenciaMuerde(t *testing.T) {
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }
	admin := func(patrones ...string) *http.ServeMux {
		mux := http.NewServeMux()
		for _, p := range patrones {
			mux.HandleFunc(p, ok)
		}
		return mux
	}
	base := []string{"GET /admin/tenants", "/healthz", "POST /admin/crypto/rekey"}
	candidatos := append(slices.Clone(base), "GET /admin/extra")
	rutasBase := Huella{Perfil: "minimo", Rutas: map[string][]string{":8100": Rutas(admin(base...), candidatos)}}

	t.Run("mux con una ruta de más", func(t *testing.T) {
		mas := admin(append(slices.Clone(base), "GET /admin/extra")...)
		tiene := Huella{Perfil: "minimo", Rutas: map[string][]string{":8100": Rutas(mas, candidatos)}}
		exigir(t, Diferencia(rutasBase, tiene), ":8100 sobra GET /admin/extra")
	})
	t.Run("mux con una ruta de menos", func(t *testing.T) {
		menos := admin("GET /admin/tenants", "/healthz")
		tiene := Huella{Perfil: "minimo", Rutas: map[string][]string{":8100": Rutas(menos, candidatos)}}
		exigir(t, Diferencia(rutasBase, tiene), ":8100 falta POST /admin/crypto/rekey")
	})
	t.Run("grpc.Server con un servicio de más", func(t *testing.T) {
		eco := servicio("wapp.prueba.Eco", []string{"Decir"}, nil)
		quiere := Huella{RPC: map[string][]string{":8101": RPC(servidor(t, eco))}}
		otro := servicio("wapp.prueba.Otro", []string{"Ping"}, nil)
		tiene := Huella{RPC: map[string][]string{":8101": RPC(servidor(t, eco, otro))}}
		exigir(t, Diferencia(quiere, tiene), "rpc :8101 sobra wapp.prueba.Otro/Ping")
	})
	t.Run("/metrics con una familia de menos", func(t *testing.T) {
		mq, err := Metricas(adminConMetricas("wapp_uno", "wapp_dos"))
		if err != nil {
			t.Fatalf("Metricas(quiere): %v", err)
		}
		mt, err := Metricas(adminConMetricas("wapp_uno"))
		if err != nil {
			t.Fatalf("Metricas(tiene): %v", err)
		}
		exigir(t, Diferencia(Huella{Metricas: mq}, Huella{Metricas: mt}), "metricas falta wapp_dos")
	})
	t.Run("paquete con una go de más", func(t *testing.T) {
		gq, err := Goroutines("testdata/fondo/base")
		if err != nil {
			t.Fatalf("Goroutines(base): %v", err)
		}
		gt, err := Goroutines("testdata/fondo/muerde")
		if err != nil {
			t.Fatalf("Goroutines(muerde): %v", err)
		}
		exigir(t, Diferencia(Huella{Goroutines: gq}, Huella{Goroutines: gt}), "goroutines sobra fondo.Worker.Run")
	})
}

// ── Utilidades ───────────────────────────────────────────────────────────────

// exigir: la diferencia es exactamente una línea, la dada.
func exigir(t *testing.T, got []string, linea string) {
	t.Helper()
	if !slices.Equal(got, []string{linea}) {
		t.Errorf("Diferencia = %q, quiere exactamente [%q]", got, linea)
	}
}

func escribirFichero(t *testing.T, ruta, contenido string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(ruta), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ruta, []byte(contenido), 0o600); err != nil {
		t.Fatal(err)
	}
}

func leerFichero(t *testing.T, ruta string) []byte {
	t.Helper()
	b, err := os.ReadFile(ruta) //nolint:gosec // ruta de t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	return b
}
