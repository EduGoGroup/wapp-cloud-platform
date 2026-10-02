//go:build integracion

package procesos

import (
	"encoding/json"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Los tests propios del arranque del servidor de prueba: el entorno construido desde cero, varios
// servidores en paralelo y el reintento cuando un puerto reservado ya está ocupado.
// Sale de servidor_test.go (D-F9-11: solo se movieron declaraciones).

// ---------------------------------------------------------------------------------------------
// Tests propios del arnés
// ---------------------------------------------------------------------------------------------

// TestArnes_EntornoLimpio prueba que el subproceso no hereda NADA del entorno del test: con
// variables trampa en el entorno del test, que apuntarían a otra base, otro S3 y un fichero de
// configuración inexistente, el servidor arranca igual, su /healthz dice que Postgres está sano
// (así que usa la base del contenedor), está conectado a SU base clonada, llamó a SU S3 falso y
// ninguna línea del log menciona la trampa. No es paralelo: t.Setenv no lo permite.
func TestArnes_EntornoLimpio(t *testing.T) {
	t.Setenv("WAPP_DB_HOST", "trampa")
	t.Setenv("WAPP_DB_NAME", "trampa")
	t.Setenv("WAPP_STORAGE_S3_ENDPOINT", "http://trampa.invalid")
	t.Setenv("WAPP_CONFIG_FILE", "/no/existe.yaml")
	t.Setenv("AWS_ENDPOINT_URL", "http://trampa.invalid")

	s := arrancar(t, opcionesServidor{Proceso: "arnes_entorno"})

	verificarEntornoCerrado(t, s)

	codigo, cuerpo, err := consultarHealthz(t.Context(), s.AdminAddr)
	if err != nil || codigo != http.StatusOK {
		t.Fatalf("GET /healthz: código %d, error %v", codigo, err)
	}
	var salud struct {
		Status string `json:"status"`
		Checks map[string]struct {
			Status string `json:"status"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(cuerpo, &salud); err != nil {
		t.Fatalf("el cuerpo de /healthz no es JSON: %v\n%s", err, cuerpo)
	}
	if salud.Status != "healthy" || salud.Checks["postgres"].Status != "healthy" {
		t.Errorf("/healthz no dice que Postgres está sano: %s", cuerpo)
	}

	// El servidor está conectado a SU base clonada, no a la «trampa» ni a otra.
	db := s.Base.Abrir(t)
	conectadas := consultaTexto(t, db, `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND pid <> pg_backend_pid()`)
	if conectadas == "0" {
		t.Errorf("el servidor no tiene ninguna sesión abierta en %s", s.Base.Nombre)
	}

	// El único S3 al que llamó fue el falso, con el HEAD del bucket.
	if !slices.ContainsFunc(s.S3.Peticiones(), func(p peticionS3) bool {
		return p.Metodo == http.MethodHead && p.Ruta == "/"+bucketServidor
	}) {
		t.Errorf("el S3 falso no recibió HEAD /%s: %v", bucketServidor, s.S3.Peticiones())
	}

	if strings.Contains(strings.ToLower(s.Log()), "trampa") {
		t.Errorf("el log menciona la trampa: el servidor heredó el entorno del test:\n%s", ultimasLineas(s.Log(), lineasDeLog))
	}
	if len(s.LineasLog()) == 0 {
		t.Errorf("el servidor no escribió ninguna línea JSON en el log")
	}
}

// verificarEntornoCerrado comprueba el entorno que se le dio al subproceso: solo PATH, TMPDIR,
// HOME y variables WAPP_, sin duplicados, ninguna de las que el arnés se prohíbe poner y con los
// valores que identifican la base y el S3 de ESTE servidor. Falla el test con t.Errorf por cada
// incumplimiento.
func verificarEntornoCerrado(t *testing.T, s *servidor) {
	t.Helper()
	valores := map[string]string{}
	for _, par := range s.proceso.cmd.Env {
		clave, valor, ok := strings.Cut(par, "=")
		if !ok {
			t.Errorf("entrada de entorno sin '=': %q", par)
			continue
		}
		if _, repetida := valores[clave]; repetida {
			t.Errorf("variable repetida en el entorno del subproceso: %s", clave)
		}
		valores[clave] = valor
		permitida := clave == "PATH" || clave == "TMPDIR" || clave == "HOME" || strings.HasPrefix(clave, "WAPP_")
		if !permitida {
			t.Errorf("el entorno del subproceso trae %s, que no es ni del sistema mínimo ni WAPP_*", clave)
		}
		if strings.HasPrefix(clave, "WAPP_KEK_KMS_") {
			t.Errorf("el entorno del subproceso trae %s, que el arnés no usa", clave)
		}
	}
	for _, prohibida := range []string{"WAPP_CONFIG_FILE", "WAPP_IDENTITY_URL", "WAPP_IDENTITY_API_KEY", "WAPP_LLM_PROMPTS_DIR"} {
		if _, hay := valores[prohibida]; hay {
			t.Errorf("el entorno del subproceso trae %s, que el arnés no pone", prohibida)
		}
	}
	for clave, quiere := range map[string]string{
		"WAPP_DB_NAME":             s.Base.Nombre,
		"WAPP_STORAGE_S3_ENDPOINT": s.S3.URL(),
		"WAPP_HTTP_ADDR":           s.AdminAddr,
		"WAPP_IDENTITY_JWKS_URL":   s.Identidad.JWKSURL(),
		"HOME":                     s.proceso.cmd.Dir,
	} {
		if valores[clave] != quiere {
			t.Errorf("%s = %q, quería %q", clave, valores[clave], quiere)
		}
	}
}

// ranuraParalela es el punto de encuentro de un subtest de TestArnes_ServidoresEnParalelo con el
// otro: dos canales que cada uno cierra al llegar a una etapa, y el servidor que publica.
type ranuraParalela struct {
	listo    chan struct{} // se cierra cuando srv ya está listo (o el subtest acabó sin conseguirlo)
	revisado chan struct{} // se cierra cuando ya hizo sus comprobaciones cruzadas
	srv      *servidor
}

// TestArnes_ServidoresEnParalelo arranca dos servidores a la vez desde dos subtests paralelos, con
// procesos distintos, y prueba que conviven: los dos listos y respondiendo a la vez, con puertos y
// bases distintos y sin «address already in use» en ningún log; que cada uno para limpio (código
// 0 y «servidor detenido limpiamente»), y que las opciones del webhook llegan al entorno solo
// cuando se piden.
func TestArnes_ServidoresEnParalelo(t *testing.T) {
	opciones := []opcionesServidor{
		{Proceso: "arnes_paralelo_a"},
		{Proceso: "arnes_paralelo_b", SondeoWebhook: 200 * time.Millisecond, MaxIntentosWebhook: 3},
	}
	ranuras := make([]*ranuraParalela, len(opciones))
	for i := range ranuras {
		ranuras[i] = &ranuraParalela{listo: make(chan struct{}), revisado: make(chan struct{})}
	}
	// El grupo hace que t.Run no vuelva hasta que acaben los dos subtests paralelos.
	t.Run("grupo", func(t *testing.T) {
		for i, o := range opciones {
			t.Run(o.Proceso, func(t *testing.T) {
				t.Parallel()
				paralelaCorrer(t, ranuras[i], ranuras[1-i], o)
			})
		}
	})
}

// paralelaCorrer es el cuerpo de un subtest de TestArnes_ServidoresEnParalelo: arranca su
// servidor, espera a que el otro también esté listo, hace las comprobaciones cruzadas, espera a
// que el otro haya terminado las suyas (para no parar un servidor que el otro aún mira) y para el
// suyo. Los canales se cierran con defer para que un fallo no deje colgado al otro subtest.
func paralelaCorrer(t *testing.T, propia, ajena *ranuraParalela, o opcionesServidor) {
	t.Helper()
	cerrarListo := sync.OnceFunc(func() { close(propia.listo) })
	cerrarRevisado := sync.OnceFunc(func() { close(propia.revisado) })
	defer cerrarListo()
	defer cerrarRevisado()

	s := arrancar(t, o)
	propia.srv = s
	cerrarListo()

	esperarCanal(t, ajena.listo, "a que el otro servidor esté listo")
	otro := ajena.srv
	if otro == nil {
		t.Fatalf("el otro servidor no llegó a estar listo")
	}
	paralelaVerificarDistintos(t, s, otro)
	for _, srv := range []*servidor{s, otro} {
		if codigo, _, err := consultarHealthz(t.Context(), srv.AdminAddr); err != nil || codigo != http.StatusOK {
			t.Errorf("con los dos arriba, /healthz de %s: código %d, error %v", srv.Base.Nombre, codigo, err)
		}
	}
	paralelaVerificarWebhook(t, s, o)
	cerrarRevisado()
	esperarCanal(t, ajena.revisado, "a que el otro servidor termine sus comprobaciones")

	if codigo := s.Parar(t); codigo != 0 {
		t.Errorf("Parar devolvió %d, quería 0 (salida limpia)", codigo)
	}
	if !strings.Contains(s.Log(), textoParadaLimpia) {
		t.Errorf("el log no contiene %q:\n%s", textoParadaLimpia, ultimasLineas(s.Log(), lineasDeLog))
	}
	if strings.Contains(s.Log(), textoPuertoOcupado) {
		t.Errorf("el log contiene %q:\n%s", textoPuertoOcupado, ultimasLineas(s.Log(), lineasDeLog))
	}
}

// esperarCanal espera hasta 60 s a que se cierre c. Recibe el test, el canal y una descripción de
// lo que se espera, para el mensaje. Falla el test (t.Fatalf) si vence el plazo o se cancela el
// test: con -parallel 1 los dos subtests no pueden coincidir y esto lo dice en vez de colgarse.
func esperarCanal(t *testing.T, c <-chan struct{}, que string) {
	t.Helper()
	plazo := time.NewTimer(2 * topeListo)
	defer plazo.Stop()
	select {
	case <-c:
	case <-plazo.C:
		t.Fatalf("pasaron %s esperando %s (¿se corre con -parallel 1? estos dos subtests necesitan coincidir)", 2*topeListo, que)
	case <-t.Context().Done():
		t.Fatalf("el test se canceló esperando %s", que)
	}
}

// paralelaVerificarDistintos comprueba que dos servidores vivos a la vez no comparten ninguna
// dirección, ni la base, ni el directorio de PKI, ni el endpoint del S3 falso, ni el JWKS. Falla
// el test con t.Errorf por cada coincidencia.
func paralelaVerificarDistintos(t *testing.T, a, b *servidor) {
	t.Helper()
	vistas := map[string]string{}
	for _, srv := range []*servidor{a, b} {
		for rol, addr := range map[string]string{
			"admin": srv.AdminAddr, "publica": srv.PublicaAddr, "enrolar": srv.EnrolarAddr, "conectar": srv.ConectarAddr,
		} {
			if previo, repetida := vistas[addr]; repetida {
				t.Errorf("la dirección %s se repite: %s de %s y %s", addr, rol, srv.Base.Nombre, previo)
			}
			vistas[addr] = rol + " de " + srv.Base.Nombre
		}
	}
	pares := []struct{ nombre, a, b string }{
		{"base", a.Base.Nombre, b.Base.Nombre},
		{"DSN", a.Base.DSN, b.Base.DSN},
		{"CA de la PKI", a.PKI.CACertFile, b.PKI.CACertFile},
		{"S3 falso", a.S3.URL(), b.S3.URL()},
		{"JWKS", a.Identidad.JWKSURL(), b.Identidad.JWKSURL()},
		{"clave de lease", a.Claves.LeaseSeedB64, b.Claves.LeaseSeedB64},
	}
	for _, p := range pares {
		if p.a == p.b {
			t.Errorf("los dos servidores comparten %s: %q", p.nombre, p.a)
		}
	}
}

// paralelaVerificarWebhook comprueba que las opciones del webhook llegaron al entorno del
// subproceso si y solo si se pidieron. Falla el test con t.Errorf por cada discrepancia.
func paralelaVerificarWebhook(t *testing.T, s *servidor, o opcionesServidor) {
	t.Helper()
	quiere := map[string]string{}
	if o.SondeoWebhook > 0 {
		quiere["WAPP_WEBHOOK_POLL_INTERVAL"] = o.SondeoWebhook.String()
	}
	if o.MaxIntentosWebhook > 0 {
		quiere["WAPP_WEBHOOK_MAX_ATTEMPTS"] = strconv.Itoa(o.MaxIntentosWebhook)
	}
	for _, clave := range []string{"WAPP_WEBHOOK_POLL_INTERVAL", "WAPP_WEBHOOK_MAX_ATTEMPTS"} {
		valor, hay := "", false
		for _, par := range s.proceso.cmd.Env {
			if k, v, ok := strings.Cut(par, "="); ok && k == clave {
				valor, hay = v, true
			}
		}
		esperado, debeEstar := quiere[clave]
		if hay != debeEstar || valor != esperado {
			t.Errorf("%s en el entorno de %s: hay=%v valor=%q; quería hay=%v valor=%q", clave, s.Base.Nombre, hay, valor, debeEstar, esperado)
		}
	}
}

// TestArnes_ReintentoPuertoOcupado prueba el reintento por puerto ocupado: el primer intento
// recibe un puerto de admin que otro listener ya tiene, el servidor muere con «address already in
// use» y arrancar vuelve a intentarlo, una sola vez, con puertos nuevos. Y que el intento fallido
// no deja rastro en lo que un proceso mira después: el log visible es solo el del servidor vigente
// (sin la frase del puerto ni líneas ERROR), el doble de S3 cuenta UN HEAD /<bucket> y la base
// sigue con una sola fila en schema_version. No es paralelo: cambia elegirPuertos, que solo
// restaura al terminar.
func TestArnes_ReintentoPuertoOcupado(t *testing.T) {
	ocupante, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("no se pudo ocupar un puerto: %v", err)
	}
	t.Cleanup(func() {
		if err := ocupante.Close(); err != nil {
			t.Logf("cerrar el listener ocupante: %v", err)
		}
	})

	original := elegirPuertos
	llamadas := 0
	elegirPuertos = func(n int) ([]string, error) {
		llamadas++
		direcciones, err := original(n)
		if err == nil && llamadas == 1 {
			direcciones[0] = ocupante.Addr().String() // el admin del primer intento choca
		}
		return direcciones, err
	}
	t.Cleanup(func() { elegirPuertos = original })

	s := arrancar(t, opcionesServidor{Proceso: "arnes_reintento"})

	if llamadas != 2 {
		t.Errorf("se reservaron puertos %d veces, quería 2 (el intento fallido y el reintento)", llamadas)
	}
	if s.AdminAddr == ocupante.Addr().String() {
		t.Errorf("el servidor sigue con el puerto ocupado %s", s.AdminAddr)
	}
	if strings.Contains(s.Log(), textoPuertoOcupado) {
		t.Errorf("el log del reintento aún contiene %q: no es el del intento nuevo", textoPuertoOcupado)
	}
	checkNoTraceOfFailedAttempt(t, s)
	if codigo, _, err := consultarHealthz(t.Context(), s.AdminAddr); err != nil || codigo != http.StatusOK {
		t.Errorf("tras el reintento, /healthz: código %d, error %v", codigo, err)
	}
}

// checkNoTraceOfFailedAttempt comprueba, en un servidor que arrancó tras un reintento por puerto
// ocupado, que el intento fallido no dejó rastro: ninguna línea ERROR en el log visible, UN solo
// HEAD /<bucket> en el doble de S3 y una sola fila en schema_version. Falla el test con t.Errorf
// por cada incumplimiento (t.Fatalf si no puede abrir o consultar la base).
func checkNoTraceOfFailedAttempt(t *testing.T, s *servidor) {
	t.Helper()
	for _, l := range s.LineasLog() {
		if strings.EqualFold(p0Cadena(l, "level"), "ERROR") {
			t.Errorf("el log visible arrastra una línea ERROR (¿del intento fallido?): %v", l)
		}
	}
	// El intento fallido llegó a hacer su HeadBucket antes de morir al enlazar: el doble de S3 solo
	// debe contar el del servidor que quedó (es lo que P0 exige con «exactamente 1»).
	cabeceras := 0
	for _, p := range s.S3.Peticiones() {
		if p.Metodo == http.MethodHead && p.Ruta == "/"+bucketServidor {
			cabeceras++
		}
	}
	if cabeceras != 1 {
		t.Errorf("HEAD /%s consta %d veces en el doble de S3 tras el reintento, quería 1 (solo el del servidor vigente); todas: %+v",
			bucketServidor, cabeceras, s.S3.Peticiones())
	}
	// La base es la misma en los dos intentos: el fallido no debe haberla migrado de nuevo.
	if filas := consultaEntero(t, s.Base.Abrir(t), `SELECT count(*) FROM public.schema_version`); filas != 1 {
		t.Errorf("schema_version tiene %d filas tras el reintento, quería 1", filas)
	}
}
