//go:build integracion

package procesos

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// peticionS3 es lo que el doble de S3 anota de cada petición que recibe: el método, la ruta ya
// decodificada (r.URL.Path) y la cabecera Host tal como llegó. Host es lo que distingue el
// path-style (Host = 127.0.0.1:<puerto>) del virtual-hosted (Host = <bucket>.127.0.0.1:<puerto>).
type peticionS3 struct{ Metodo, Ruta, Host string }

// s3Falso es el doble del almacén de objetos (D-F9-2). El servidor hace UNA sola llamada de red a
// S3 en toda su vida: el HeadBucket de fail-fast al arrancar. El doble la contesta 200, contesta
// 404 a cualquier otra cosa y deja constancia de cada petición para que un test pueda afirmar
// que el servidor no pidió nada más. Escucha siempre en 127.0.0.1 (una IP, nunca "localhost"):
// con un endpoint IP el SDK de AWS usa path-style aunque UsePathStyle sea false, y así llega
// HEAD /<bucket> en vez de HEAD / con el bucket en el Host.
type s3Falso struct {
	servidor *httptest.Server
	bucket   string

	mu        sync.Mutex
	recibidas []peticionS3
}

// nuevoS3Falso levanta el doble en 127.0.0.1:<puerto efímero> para el bucket dado y registra su
// cierre en t.Cleanup. Recibe el nombre del bucket (no vacío, sin "/" ni espacios) y devuelve el
// doble ya escuchando, de modo que el servidor puede apuntarle en cuanto lo arranque. Falla
// (t.Fatalf) si el bucket no es válido o si el listener no quedó en una IPv4 de loopback.
func nuevoS3Falso(t *testing.T, bucket string) *s3Falso {
	t.Helper()
	if bucket == "" || strings.ContainsAny(bucket, "/ \t\r\n") {
		t.Fatalf("nuevoS3Falso: bucket %q no válido (vacío o con '/' o espacios)", bucket)
	}
	s := &s3Falso{bucket: bucket}
	s.servidor = httptest.NewServer(http.HandlerFunc(s.atender))
	t.Cleanup(s.servidor.Close)
	// httptest cae a [::1] si no hay IPv4; sin la IP literal el SDK no usaría path-style.
	if !strings.HasPrefix(s.servidor.URL, "http://127.0.0.1:") {
		t.Fatalf("nuevoS3Falso: el doble debe escuchar en 127.0.0.1 y escucha en %s", s.servidor.URL)
	}
	return s
}

// atender anota la petición y contesta: 200 a HEAD /<bucket>, 404 a todo lo demás. Corre en la
// goroutine de cada conexión; el registro va bajo candado.
func (s *s3Falso) atender(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.recibidas = append(s.recibidas, peticionS3{Metodo: r.Method, Ruta: r.URL.Path, Host: r.Host})
	s.mu.Unlock()

	if r.Method == http.MethodHead && r.URL.Path == "/"+s.bucket {
		w.WriteHeader(http.StatusOK)
		return
	}
	http.NotFound(w, r)
}

// URL devuelve el endpoint del doble, "http://127.0.0.1:<puerto>". No recibe nada ni falla.
func (s *s3Falso) URL() string { return s.servidor.URL }

// Peticiones devuelve una COPIA, en orden de llegada, de todo lo que el doble ha recibido hasta
// ahora. Es segura para uso concurrente con peticiones en vuelo; modificar el resultado no altera
// el registro. Devuelve un slice vacío (no nil) si no llegó nada. No falla.
func (s *s3Falso) Peticiones() []peticionS3 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]peticionS3{}, s.recibidas...)
}

// forget borra todo lo que el doble ha registrado hasta ahora; el doble sigue escuchando en la misma
// URL y contestando igual. Lo usa arrancar cuando un intento de arranque muere por puerto ocupado:
// ese intento ya hizo su HeadBucket, y sin olvidarlo el servidor que queda tras el reintento
// aparecería con DOS, cuando hizo uno (P0 exige exactamente uno). Solo es correcto llamarlo cuando
// quien hizo las peticiones ya no existe: una petición en vuelo se anotaría después del borrado. Es
// segura entre goroutines. No recibe nada ni falla.
func (s *s3Falso) forget() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recibidas = nil
}

// Entorno devuelve las variables "K=V" que apuntan al servidor a este doble: endpoint, bucket,
// región y un par de credenciales fijas (el doble no las comprueba, pero el SDK exige que
// existan). Devuelve un slice nuevo en cada llamada. No falla.
func (s *s3Falso) Entorno() []string {
	return []string{
		"WAPP_STORAGE_S3_ENDPOINT=" + s.URL(),
		"WAPP_STORAGE_S3_BUCKET=" + s.bucket,
		"WAPP_STORAGE_S3_REGION=us-east-1",
		"WAPP_STORAGE_S3_ACCESS_KEY_ID=procesos",
		"WAPP_STORAGE_S3_SECRET_ACCESS_KEY=procesos-secreto",
	}
}

// s3Intentar hace una petición sin cuerpo al url con el método dado, con un cliente de conexión
// de un solo uso (sin keep-alive: no deja sockets abiertos que retrasen el cierre del doble), y
// devuelve el código de estado. Devuelve error si no pudo conectar o leer; es la variante apta
// para goroutines que no son la del test (no llama a t).
func s3Intentar(ctx context.Context, metodo, url string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, metodo, url, nil)
	if err != nil {
		return 0, fmt.Errorf("construyendo %s %s: %w", metodo, url, err)
	}
	cliente := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := cliente.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%s %s: %w", metodo, url, err)
	}
	if err := resp.Body.Close(); err != nil {
		return 0, fmt.Errorf("cerrando la respuesta de %s %s: %w", metodo, url, err)
	}
	return resp.StatusCode, nil
}

// s3Pedir es s3Intentar para la goroutine del test: devuelve el código de estado y falla el
// test (t.Fatalf) si la petición no se pudo hacer.
func s3Pedir(t *testing.T, metodo, url string) int {
	t.Helper()
	codigo, err := s3Intentar(t.Context(), metodo, url)
	if err != nil {
		t.Fatalf("petición al doble de S3: %v", err)
	}
	return codigo
}

// s3Bucket es el bucket que usa TestArnes_S3Falso.
const s3Bucket = "bucket-procesos"

// TestArnes_S3Falso fija el contrato del doble de S3: HEAD /<bucket> da 200 con el Host de
// path-style, cualquier otra cosa da 404, todo queda registrado (también bajo concurrencia), el
// registro que se entrega es una copia, forget lo vacía sin parar el doble, el entorno apunta al
// doble y el Cleanup lo cierra.
// Los subtests van en orden y comparten el doble: cada uno parte del registro que dejó el
// anterior. No necesita Docker.
func TestArnes_S3Falso(t *testing.T) {
	s3 := nuevoS3Falso(t, s3Bucket)

	t.Run("la URL es una IP de loopback con puerto", func(t *testing.T) { probarS3URL(t, s3) })
	t.Run("HEAD del bucket responde 200 y otras rutas 404", func(t *testing.T) { probarS3Respuestas(t, s3) })
	t.Run("Peticiones entrega una copia", func(t *testing.T) { probarS3Copia(t, s3) })
	t.Run("forget olvida lo registrado y el doble sigue sirviendo", func(t *testing.T) { probarS3Forget(t, s3) })
	t.Run("el registro es seguro bajo concurrencia", func(t *testing.T) { probarS3Concurrencia(t, s3) })
	t.Run("Entorno apunta al doble", func(t *testing.T) { probarS3Entorno(t, s3) })
	t.Run("el Cleanup lo cierra", probarS3Cierre)
}

// probarS3URL comprueba que la URL es http://127.0.0.1:<puerto efímero> y que el doble recién
// creado no tiene peticiones registradas.
func probarS3URL(t *testing.T, s3 *s3Falso) {
	t.Helper()
	host, puerto, err := net.SplitHostPort(strings.TrimPrefix(s3.URL(), "http://"))
	if err != nil {
		t.Fatalf("la URL %q no tiene host:puerto: %v", s3.URL(), err)
	}
	if host != "127.0.0.1" || puerto == "" || puerto == "0" {
		t.Fatalf("host=%q puerto=%q: se esperaba 127.0.0.1 y un puerto efímero", host, puerto)
	}
	if got := s3.Peticiones(); len(got) != 0 {
		t.Fatalf("recién creado el doble no debería tener peticiones y tiene %v", got)
	}
}

// probarS3Respuestas hace una serie de peticiones y comprueba el código de cada una (200 solo
// para HEAD /<bucket>) y que el registro las anotó, en orden, con el Host de path-style.
func probarS3Respuestas(t *testing.T, s3 *s3Falso) {
	t.Helper()
	casos := []struct {
		metodo, ruta string
		quiere       int
	}{
		{http.MethodHead, "/" + s3Bucket, http.StatusOK},
		{http.MethodHead, "/otro-bucket", http.StatusNotFound},
		{http.MethodHead, "/" + s3Bucket + "/objeto", http.StatusNotFound},
		{http.MethodGet, "/" + s3Bucket, http.StatusNotFound},
		{http.MethodHead, "/", http.StatusNotFound},
	}
	host := strings.TrimPrefix(s3.URL(), "http://")
	want := make([]peticionS3, 0, len(casos))
	for _, c := range casos {
		if got := s3Pedir(t, c.metodo, s3.URL()+c.ruta); got != c.quiere {
			t.Errorf("%s %s: código %d, se esperaba %d", c.metodo, c.ruta, got, c.quiere)
		}
		want = append(want, peticionS3{Metodo: c.metodo, Ruta: c.ruta, Host: host})
	}
	if got := s3.Peticiones(); !slices.Equal(got, want) {
		t.Fatalf("registro del doble:\n got %v\nwant %v", got, want)
	}
}

// probarS3Copia comprueba que modificar lo que devuelve Peticiones no altera el registro.
func probarS3Copia(t *testing.T, s3 *s3Falso) {
	t.Helper()
	primera := s3.Peticiones()
	if len(primera) == 0 {
		t.Fatal("se esperaba el registro del subtest anterior")
	}
	primera[0].Ruta = "/alterada"
	if got := s3.Peticiones()[0].Ruta; got == "/alterada" {
		t.Fatal("modificar el resultado de Peticiones alteró el registro interno")
	}
}

// probarS3Forget comprueba que forget deja el registro vacío (un slice vacío, no nil, como recién
// creado), que el doble sigue contestando en la misma URL y que lo que llega después se anota desde
// cero. Deja en el registro una petición, para el subtest siguiente.
func probarS3Forget(t *testing.T, s3 *s3Falso) {
	t.Helper()
	if len(s3.Peticiones()) == 0 {
		t.Fatal("se esperaba el registro de los subtests anteriores")
	}
	url := s3.URL()
	s3.forget()
	if got := s3.Peticiones(); got == nil || len(got) != 0 {
		t.Fatalf("tras forget el registro debe ser un slice vacío no nil, es %#v", got)
	}
	if s3.URL() != url {
		t.Fatalf("forget cambió la URL del doble: %q, era %q", s3.URL(), url)
	}
	if got := s3Pedir(t, http.MethodHead, url+"/"+s3Bucket); got != http.StatusOK {
		t.Fatalf("tras forget, HEAD /%s: código %d, se esperaba 200", s3Bucket, got)
	}
	want := []peticionS3{{Metodo: http.MethodHead, Ruta: "/" + s3Bucket, Host: strings.TrimPrefix(url, "http://")}}
	if got := s3.Peticiones(); !slices.Equal(got, want) {
		t.Fatalf("registro tras forget y una petición:\n got %v\nwant %v", got, want)
	}
}

// probarS3Concurrencia lanza peticiones desde muchas goroutines mientras otras leen el registro
// (lo caza -race) y comprueba que no se perdió ninguna.
func probarS3Concurrencia(t *testing.T, s3 *s3Falso) {
	t.Helper()
	const goroutines = 32
	antes := len(s3.Peticiones())
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	for i := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ruta := "/" + s3Bucket
			if i%2 == 1 {
				ruta = "/concurrente"
			}
			if _, err := s3Intentar(t.Context(), http.MethodHead, s3.URL()+ruta); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
			if len(s3.Peticiones()) == 0 {
				mu.Lock()
				errs = append(errs, errors.New("el registro quedó vacío con peticiones ya servidas"))
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatalf("peticiones concurrentes: %v", err)
	}
	if got := len(s3.Peticiones()) - antes; got != goroutines {
		t.Fatalf("registradas %d peticiones nuevas, se esperaban %d", got, goroutines)
	}
}

// probarS3Entorno comprueba las cinco variables de Entorno y que cada llamada entrega un slice
// nuevo.
func probarS3Entorno(t *testing.T, s3 *s3Falso) {
	t.Helper()
	want := []string{
		"WAPP_STORAGE_S3_ENDPOINT=" + s3.URL(),
		"WAPP_STORAGE_S3_BUCKET=" + s3Bucket,
		"WAPP_STORAGE_S3_REGION=us-east-1",
		"WAPP_STORAGE_S3_ACCESS_KEY_ID=procesos",
		"WAPP_STORAGE_S3_SECRET_ACCESS_KEY=procesos-secreto",
	}
	if got := s3.Entorno(); !slices.Equal(got, want) {
		t.Fatalf("Entorno:\n got %v\nwant %v", got, want)
	}
	alterado := s3.Entorno()
	alterado[0] = "alterada"
	if got := s3.Entorno()[0]; got != want[0] {
		t.Fatalf("Entorno compartió estado entre llamadas: %q", got)
	}
}

// probarS3Cierre comprueba que el Cleanup cierra el doble: lo crea en un subtest, que al
// terminar ejecuta su Cleanup antes de que t.Run devuelva, y después la conexión debe fallar.
func probarS3Cierre(t *testing.T) {
	t.Helper()
	var url string
	t.Run("vida", func(t *testing.T) {
		efimero := nuevoS3Falso(t, "efimero")
		url = efimero.URL()
		if got := s3Pedir(t, http.MethodHead, url+"/efimero"); got != http.StatusOK {
			t.Fatalf("HEAD /efimero: código %d, se esperaba 200", got)
		}
	})
	if url == "" {
		t.Fatal("el subtest de vida no llegó a levantar el doble")
	}
	if codigo, err := s3Intentar(t.Context(), http.MethodHead, url+"/efimero"); err == nil {
		t.Fatalf("el doble sigue contestando tras el Cleanup (código %d)", codigo)
	}
}
