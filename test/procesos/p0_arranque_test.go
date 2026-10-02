//go:build integracion

package procesos

import (
	"context"
	"encoding/base64"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	// p0MsgFase es el mensaje con el que el orquestador deja constancia de cada fase del arranque
	// (internal/bootstrap/arranque/orquestador.go:117-123).
	p0MsgFase = "arranque: fase completada"
	// p0MsgMigraciones es la línea de database.go:40-44 con version, content_hash y skipped.
	p0MsgMigraciones = "migraciones aplicadas"
	// p0MsgLease y p0MsgNube son las dos líneas de «de dónde salió la clave»: la del lease
	// (lease.go:43-46) y la de cifrado de tránsito de la nube (pki.go:104-107).
	p0MsgLease = "clave pública del lease (configurar en el Edge)"
	p0MsgNube  = "clave pública de cifrado de la nube (publicada al Edge en el enrolamiento)"
	// p0MsgEfimera es la palabra que llevan los avisos de «clave EFÍMERA de dev» (lease.go:47-49,
	// pki.go:119): si aparece, el servidor no usó la clave que el arnés le dio.
	p0MsgEfimera = "EFÍMERA"
	// p0MsgHTTP y p0MsgGRPC son las líneas que cada listener escribe al ponerse a servir
	// (internal/bootstrap/arranque/servir.go:65,72).
	p0MsgHTTP = "servidor HTTP iniciado"
	p0MsgGRPC = "servidor gRPC iniciado"
	// p0MsgSenal es la línea con la que el servidor confirma que vio SIGTERM
	// (servir.go:53); la de fin limpio es textoParadaLimpia (servir.go:60).
	p0MsgSenal = "señal de parada recibida, cerrando"

	// p0Listeners es cuántos listeners tiene el proceso: admin, API pública, enrolamiento y CloudLink.
	p0Listeners = 4
	// p0TopeLog acota cuánto se espera a que el log refleje el arranque completo; p0SondeoLog es
	// cada cuánto se mira. Solo cubre el trecho entre que el servidor escribe una línea en su
	// tubería y que el arnés la copia a su búfer.
	p0TopeLog   = 10 * time.Second
	p0SondeoLog = 20 * time.Millisecond
)

// p0Fases son las nueve fases del arranque, en orden, con el valor exacto de los atributos «fase»
// y «nombre» de su línea. De dónde salen: la lista `fases` de orquestador.go:51-61 fija el orden y
// el denominador «/9» (len(plan)), y el nombre es lo que devuelve nombre() en cada fichero
// fase1_infraestructura.go … fase9_fondo.go. Medido contra el binario viejo y el nuevo: idénticas.
var p0Fases = []struct{ fase, nombre string }{
	{"1/9", "infraestructura"},
	{"2/9", "autenticación"},
	{"3/9", "almacenes"},
	{"4/9", "gateway"},
	{"5/9", "captación (stack LLM)"},
	{"6/9", "solicitudes"},
	{"7/9", "flujos"},
	{"8/9", "transporte"},
	{"9/9", "goroutines de fondo"},
}

// TestP0_Arranque es el proceso P0, el humo del arranque: arranca el binario elegido por el arnés
// contra una base ya migrada y comprueba, solo mirando por fuera —sus cuatro puertos, su log y el
// doble de S3—, que arranca como debe y para como debe. El mismo test sirve para el servidor
// viejo y el nuevo; no distingue entre ellos.
//
// Los subtests comparten UN servidor y corren EN ORDEN (ninguno es paralelo): «parada» lo apaga,
// y «sin_errores» va después para ver también el cierre. Cada uno es una función p0… pequeña.
// Evidencia, medida contra el binario viejo (y repetida en el nuevo, sin diferencias):
//
//   - healthz: GET :8100/healthz → 200 {"status":"healthy","checks":{"postgres":{…"healthy"…},
//     "self":{…"healthy"…}}} (internal/platform/httpapi/health.go y storage/postgres/health.go;
//     los checks se llaman «postgres» y «self»). GET :8103/healthz → 404 «404 page not found»: la
//     API pública no tiene sonda (contratos.md §3).
//   - fases: las 9 líneas «arranque: fase completada» en orden (ver p0Fases).
//   - migraciones: una línea «migraciones aplicadas» con skipped=true (la base clonada ya está
//     migrada por cmd/migrate) y version=0.48.0, que NO se fija a mano: se compara con la última
//     fila de public.schema_version de la base clonada, que además sigue con UNA fila (si el
//     servidor hubiera migrado de nuevo habría dos y skipped sería false).
//   - claves: la del lease loguea key_source=base64 y la de cifrado de la nube key_source=config.
//     Contradice a diseno.md §4 y arquitectura.md §2, que dicen «config» para las dos: la del lease
//     entra por WAPP_LEASE_PRIVATE_KEY_B64 y signingkey.go:21 la nombra KeySourceBase64 («base64»);
//     «config» es solo la de pki.go:105. Ambas llevan la pública que el arnés derivó de SU clave.
//   - S3: una sola petición HEAD /wapp-procesos, con Host = 127.0.0.1:<puerto del doble>. Eso
//     confirma D-F9-2: el SDK usa path-style con un endpoint IP (si usara virtual-hosted el Host
//     sería wapp-procesos.127.0.0.1:<puerto>, que no resuelve, y el servidor no arrancaría).
//   - métricas: GET :8100/metrics → 200 en formato Prometheus, con p0MetricasSinTrafico (lista
//     medida y excluidos explicados allí) y sin wapp_auth_logins_total.
//   - listeners: las cuatro líneas «servidor … iniciado» con su name, en cualquier orden (lo
//     lanzan cuatro goroutines). El addr no se asierta: el log da el de la configuración.
//   - parada: SIGTERM → código 0, «señal de parada recibida, cerrando» y después «servidor
//     detenido limpiamente».
//   - sin_errores: ninguna línea de nivel ERROR en todo el log, arranque y parada incluidos
//     (medido: cero; los únicos WARN son los cuatro de «identity sin configurar», diseñados).
func TestP0_Arranque(t *testing.T) {
	s := arrancar(t, opcionesServidor{Proceso: "p0"})
	p0EsperarListeners(t, s)

	t.Run("healthz_admin", func(t *testing.T) { p0HealthzAdmin(t, s) })
	t.Run("healthz_publica_404", func(t *testing.T) { p0HealthzPublica(t, s) })
	t.Run("nueve_fases", func(t *testing.T) { p0NueveFases(t, s) })
	t.Run("migraciones", func(t *testing.T) { p0Migraciones(t, s) })
	t.Run("claves", func(t *testing.T) { p0Claves(t, s) })
	t.Run("almacenes_s3", func(t *testing.T) { p0AlmacenesS3(t, s) })
	t.Run("metricas", func(t *testing.T) { p0Metricas(t, s) })
	t.Run("listeners", func(t *testing.T) { p0ListenersIniciados(t, s) })
	t.Run("parada", func(t *testing.T) { p0Parada(t, s) })
	t.Run("sin_errores", func(t *testing.T) { p0SinErrores(t, s) })
}

// p0EsperarListeners espera, sondeando cada 20 ms con tope de 10 s, a que el log contenga las
// cuatro líneas «servidor … iniciado». Son lo ÚLTIMO que escribe el arranque, y el log del
// subproceso llega al arnés por una tubería: arrancar vuelve en cuanto los puertos responden, y esta
// espera cierra la ventana entre que el servidor escribe una línea y que el arnés la tiene, para
// que los subtests lean un log completo. Falla (t.Fatalf) si no aparecen a tiempo.
func p0EsperarListeners(t *testing.T, s *servidor) {
	t.Helper()
	ctx, cancelar := context.WithTimeout(t.Context(), p0TopeLog)
	defer cancelar()
	for p0ContarListeners(s.LineasLog()) < p0Listeners {
		select {
		case <-ctx.Done():
			t.Fatalf("el log no llegó a tener las %d líneas «servidor … iniciado» en %s:\n%s",
				p0Listeners, p0TopeLog, ultimasLineas(s.Log(), lineasDeLog))
		case <-time.After(p0SondeoLog):
		}
	}
}

// p0ContarListeners recibe las líneas JSON del log y devuelve cuántas son «servidor HTTP iniciado»
// o «servidor gRPC iniciado». No falla.
func p0ContarListeners(lineas []map[string]any) int {
	return len(p0PorMsg(lineas, p0MsgHTTP)) + len(p0PorMsg(lineas, p0MsgGRPC))
}

// p0PorMsg recibe las líneas JSON del log y un mensaje, y devuelve, en orden, las líneas cuyo msg
// es EXACTAMENTE ese. Devuelve nil si no hay ninguna. No falla.
func p0PorMsg(lineas []map[string]any, msg string) []map[string]any {
	var res []map[string]any
	for _, l := range lineas {
		if p0Cadena(l, "msg") == msg {
			res = append(res, l)
		}
	}
	return res
}

// p0Indice recibe las líneas JSON del log y un mensaje, y devuelve la posición de la primera línea
// cuyo msg es exactamente ese, o -1 si no hay ninguna. No falla.
func p0Indice(lineas []map[string]any, msg string) int {
	return slices.IndexFunc(lineas, func(l map[string]any) bool { return p0Cadena(l, "msg") == msg })
}

// p0Cadena recibe una línea JSON del log y el nombre de un atributo, y devuelve su valor si es una
// cadena. Devuelve "" si el atributo no existe o no es una cadena. No falla.
func p0Cadena(linea map[string]any, clave string) string {
	if valor, ok := linea[clave].(string); ok {
		return valor
	}
	return ""
}

// p0HealthzAdmin comprueba GET :8100/healthz: 200 y un cuerpo con status «healthy» y los checks
// «postgres» (con component «postgres») y «self» sanos. Falla el test con t.Errorf por cada
// incumplimiento; t.Fatalf si la petición no llega o el cuerpo no es JSON.
func p0HealthzAdmin(t *testing.T, s *servidor) {
	t.Helper()
	r := s.Admin("").Get(t, "/healthz", nil)
	if r.Codigo != http.StatusOK {
		t.Errorf("GET :8100/healthz = %d, quería 200\ncuerpo: %s", r.Codigo, recortar(r.Cuerpo))
	}
	var salud struct {
		Status string `json:"status"`
		Checks map[string]struct {
			Status    string `json:"status"`
			Component string `json:"component"`
		} `json:"checks"`
	}
	r.JSON(t, &salud)
	if salud.Status != "healthy" {
		t.Errorf("status = %q, quería %q\ncuerpo: %s", salud.Status, "healthy", recortar(r.Cuerpo))
	}
	for _, nombre := range []string{"postgres", "self"} {
		check, hay := salud.Checks[nombre]
		if !hay || check.Status != "healthy" || check.Component != nombre {
			t.Errorf("check %q = %+v (presente: %v), quería status healthy y component %q\ncuerpo: %s",
				nombre, check, hay, nombre, recortar(r.Cuerpo))
		}
	}
}

// p0HealthzPublica comprueba que GET :8103/healthz da 404: la API pública no tiene sonda de salud
// (contratos.md §3). Falla el test con t.Errorf si el código es otro.
func p0HealthzPublica(t *testing.T, s *servidor) {
	t.Helper()
	r := s.Publica("").Get(t, "/healthz", nil)
	if r.Codigo != http.StatusNotFound {
		t.Errorf("GET :8103/healthz = %d, quería 404 (la API pública no tiene sonda)\ncuerpo: %s", r.Codigo, recortar(r.Cuerpo))
	}
}

// p0NueveFases comprueba que el log tiene exactamente nueve líneas «arranque: fase completada», en
// el orden de p0Fases y con su «fase» y su «nombre» exactos. Falla con t.Fatalf si no son nueve y
// con t.Errorf por cada fase que no coincide.
func p0NueveFases(t *testing.T, s *servidor) {
	t.Helper()
	lineas := p0PorMsg(s.LineasLog(), p0MsgFase)
	if len(lineas) != len(p0Fases) {
		t.Fatalf("hay %d líneas %q, quería %d: %v", len(lineas), p0MsgFase, len(p0Fases), lineas)
	}
	for i, quiere := range p0Fases {
		if fase, nombre := p0Cadena(lineas[i], "fase"), p0Cadena(lineas[i], "nombre"); fase != quiere.fase || nombre != quiere.nombre {
			t.Errorf("línea %d de fase: fase=%q nombre=%q, quería fase=%q nombre=%q", i+1, fase, nombre, quiere.fase, quiere.nombre)
		}
	}
}

// p0Migraciones comprueba que el servidor encontró la base ya migrada: una sola línea «migraciones
// aplicadas» con skipped=true, y con la version y el content_hash que ya estaban en la última fila
// de public.schema_version de su base clonada, que sigue teniendo una sola fila. Falla con
// t.Fatalf si falta la línea o la consulta, y con t.Errorf por cada discrepancia.
func p0Migraciones(t *testing.T, s *servidor) {
	t.Helper()
	lineas := p0PorMsg(s.LineasLog(), p0MsgMigraciones)
	if len(lineas) != 1 {
		t.Fatalf("hay %d líneas %q, quería 1: %v", len(lineas), p0MsgMigraciones, lineas)
	}
	linea := lineas[0]
	if saltada, ok := linea["skipped"].(bool); !ok || !saltada {
		t.Errorf("skipped = %v, quería true (la base clonada ya estaba migrada): %v", linea["skipped"], linea)
	}
	version, hash := p0Cadena(linea, "version"), p0Cadena(linea, "content_hash")
	if version == "" || hash == "" {
		t.Errorf("la línea no trae version y content_hash: %v", linea)
	}

	db := s.Base.Abrir(t)
	ultima := consultaTexto(t, db, `SELECT version || ' ' || content_hash FROM public.schema_version ORDER BY id DESC LIMIT 1`)
	if ultima != version+" "+hash {
		t.Errorf("el log dice version=%q content_hash=%q y la última fila de schema_version es %q", version, hash, ultima)
	}
	if filas := consultaEntero(t, db, `SELECT count(*) FROM public.schema_version`); filas != 1 {
		t.Errorf("schema_version tiene %d filas, quería 1 (el servidor no debía migrar otra vez)", filas)
	}
}

// p0Claves comprueba de dónde dice el servidor que salieron sus dos claves públicas: la del lease
// con key_source=base64 y la de cifrado de la nube con key_source=config, cada una con la pública
// derivada de la clave que el arnés le dio; y que no hay ningún aviso de clave efímera. Falla
// con t.Errorf por cada incumplimiento.
func p0Claves(t *testing.T, s *servidor) {
	t.Helper()
	lineas := s.LineasLog()
	p0LineaClave(t, lineas, p0MsgLease, "base64", base64.StdEncoding.EncodeToString(s.Claves.LeasePub))
	p0LineaClave(t, lineas, p0MsgNube, "config", base64.StdEncoding.EncodeToString(s.Claves.NubePub))
	for _, l := range lineas {
		if msg := p0Cadena(l, "msg"); strings.Contains(msg, p0MsgEfimera) {
			t.Errorf("el servidor generó una clave efímera en vez de usar la del arnés: %v", l)
		}
	}
}

// p0LineaClave recibe las líneas del log, el mensaje de una línea de clave, el key_source esperado y
// la pública esperada en base64, y comprueba que hay UNA línea con ese mensaje y esos dos
// atributos. Falla con t.Errorf, una vez por atributo que no coincide o una sola si la línea falta o
// se repite.
func p0LineaClave(t *testing.T, lineas []map[string]any, msg, fuente, publicaB64 string) {
	t.Helper()
	encontradas := p0PorMsg(lineas, msg)
	if len(encontradas) != 1 {
		t.Errorf("hay %d líneas %q, quería 1", len(encontradas), msg)
		return
	}
	if got := p0Cadena(encontradas[0], "key_source"); got != fuente {
		t.Errorf("%q: key_source = %q, quería %q", msg, got, fuente)
	}
	if got := p0Cadena(encontradas[0], "public_key_base64"); got != publicaB64 {
		t.Errorf("%q: public_key_base64 = %q, quería %q (la derivada de la clave del arnés)", msg, got, publicaB64)
	}
}

// p0AlmacenesS3 comprueba D-F9-2: el doble de S3 recibió EXACTAMENTE una petición HEAD /<bucket>
// (el HeadBucket de la fase «almacenes»), y todas sus peticiones llegaron con Host = la dirección
// IP del doble (path-style, nunca <bucket>.127.0.0.1…); y que esa fase está completada. Falla con
// t.Errorf por cada incumplimiento.
func p0AlmacenesS3(t *testing.T, s *servidor) {
	t.Helper()
	hostDoble := strings.TrimPrefix(s.S3.URL(), "http://")
	cabeceras := 0
	for _, p := range s.S3.Peticiones() {
		if p.Host != hostDoble {
			t.Errorf("petición %s %s con Host %q, quería %q (path-style con endpoint IP)", p.Metodo, p.Ruta, p.Host, hostDoble)
		}
		if p.Metodo == http.MethodHead && p.Ruta == "/"+bucketServidor {
			cabeceras++
		}
	}
	if cabeceras != 1 {
		t.Errorf("HEAD /%s llegó %d veces al doble, quería 1; todas: %+v", bucketServidor, cabeceras, s.S3.Peticiones())
	}
	if !slices.ContainsFunc(p0PorMsg(s.LineasLog(), p0MsgFase), func(l map[string]any) bool {
		return p0Cadena(l, "nombre") == "almacenes" && p0Cadena(l, "fase") == "3/9"
	}) {
		t.Errorf("no consta la fase 3/9 «almacenes» completada")
	}
}

// p0ListenersIniciados comprueba que el log tiene las cuatro líneas de «servidor … iniciado» con
// el name de cada listener y ninguna más, sin importar el orden (las escriben cuatro goroutines) y
// sin mirar el addr (es el de la configuración, no necesariamente el real). Falla con t.Errorf si
// el conjunto no coincide.
func p0ListenersIniciados(t *testing.T, s *servidor) {
	t.Helper()
	quiere := []string{
		p0MsgGRPC + "|CloudLink (mTLS)",
		p0MsgGRPC + "|Enrollment (TLS de servidor)",
		p0MsgHTTP + "|API pública",
		p0MsgHTTP + "|admin/health",
	}
	var got []string
	for _, l := range s.LineasLog() {
		if msg := p0Cadena(l, "msg"); msg == p0MsgHTTP || msg == p0MsgGRPC {
			got = append(got, msg+"|"+p0Cadena(l, "name"))
		}
	}
	slices.Sort(got)
	slices.Sort(quiere)
	if !slices.Equal(got, quiere) {
		t.Errorf("listeners iniciados = %q, quería %q", got, quiere)
	}
}

// p0Parada manda SIGTERM al servidor y comprueba que sale con código 0 (también en una segunda
// llamada, que no vuelve a señalar) y que el log trae «señal de parada recibida, cerrando» antes de
// «servidor detenido limpiamente». Falla con t.Errorf por cada incumplimiento.
func p0Parada(t *testing.T, s *servidor) {
	t.Helper()
	if codigo := s.Parar(t); codigo != 0 {
		t.Errorf("Parar devolvió %d, quería 0 (salida limpia)", codigo)
	}
	if codigo := s.Parar(t); codigo != 0 {
		t.Errorf("la segunda llamada a Parar devolvió %d, quería 0", codigo)
	}
	lineas := s.LineasLog()
	senal, fin := p0Indice(lineas, p0MsgSenal), p0Indice(lineas, textoParadaLimpia)
	switch {
	case senal < 0:
		t.Errorf("el log no contiene %q", p0MsgSenal)
	case fin < 0:
		t.Errorf("el log no contiene %q", textoParadaLimpia)
	case senal > fin:
		t.Errorf("%q (línea %d) llegó después de %q (línea %d)", p0MsgSenal, senal, textoParadaLimpia, fin)
	}
}

// p0SinErrores comprueba que ninguna línea JSON del log, de todo el arranque y toda la parada, es de
// nivel ERROR, y que el log no contiene ningún pánico de Go. Debe correr cuando el servidor ya
// paró. Falla con t.Errorf por cada línea de error o por el pánico.
func p0SinErrores(t *testing.T, s *servidor) {
	t.Helper()
	for _, l := range s.LineasLog() {
		if strings.EqualFold(p0Cadena(l, "level"), "ERROR") {
			t.Errorf("línea de nivel ERROR en el log: %v", l)
		}
	}
	if strings.Contains(s.Log(), "panic:") {
		t.Errorf("el log contiene un pánico:\n%s", ultimasLineas(s.Log(), lineasDeLog))
	}
}
