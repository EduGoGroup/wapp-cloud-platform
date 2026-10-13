package lease_test

// Los tests de la resolución de la clave de firma del lease. Todas las claves se generan en el
// test y los ficheros viven en t.TempDir(): ninguna clave del repositorio.

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/lease"
)

// writePEM escribe un bloque PEM en un fichero temporal y devuelve su ruta.
func writePEM(t *testing.T, blockType string, der []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}), 0o600); err != nil {
		t.Fatalf("escribiendo el PEM del test: %v", err)
	}
	return path
}

// writeEd25519PEM escribe priv como PKCS#8 en PEM y devuelve la ruta.
func writeEd25519PEM(t *testing.T, priv ed25519.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("serializando la clave del test: %v", err)
	}
	return writePEM(t, "PRIVATE KEY", der)
}

// seedBase64 es la semilla de priv en base64 estándar: la forma en que se configura.
func seedBase64(priv ed25519.PrivateKey) string {
	return base64.StdEncoding.EncodeToString(priv.Seed())
}

// TestKeySource_Values: los valores son los que el arranque escribe en el log.
func TestKeySource_Values(t *testing.T) {
	values := map[lease.KeySource]string{
		lease.KeySourceFile:      "file",
		lease.KeySourceBase64:    "base64",
		lease.KeySourceGenerated: "generated-dev",
	}
	for got, want := range values {
		if string(got) != want {
			t.Errorf("KeySource = %q, quería %q", got, want)
		}
	}
}

// TestGenerateDevKey_IsFreshEachCall: una clave Ed25519 válida y DISTINTA cada vez.
func TestGenerateDevKey_IsFreshEachCall(t *testing.T) {
	first, err := lease.GenerateDevKey()
	if err != nil {
		t.Fatalf("GenerateDevKey: error inesperado %v", err)
	}
	second, err := lease.GenerateDevKey()
	if err != nil {
		t.Fatalf("GenerateDevKey: error inesperado %v", err)
	}
	if len(first) != ed25519.PrivateKeySize || len(second) != ed25519.PrivateKeySize {
		t.Fatalf("tamaños %d y %d, quería %d", len(first), len(second), ed25519.PrivateKeySize)
	}
	if first.Equal(second) {
		t.Error("dos llamadas devolvieron la misma clave: no puede haber una clave fija")
	}
	pub, ok := first.Public().(ed25519.PublicKey)
	if !ok {
		t.Fatal("la pública de la clave generada no es Ed25519")
	}
	if msg := []byte("lease"); !ed25519.Verify(pub, msg, ed25519.Sign(first, msg)) {
		t.Error("la clave generada no firma y verifica")
	}
}

// TestParsePrivateKeyBase64: acepta la semilla (32) y la clave expandida (64), y las dos dan la
// MISMA clave; rechaza lo demás con su texto.
func TestParsePrivateKeyBase64(t *testing.T) {
	priv := newKey(t)

	fromSeed, err := lease.ParsePrivateKeyBase64(seedBase64(priv))
	if err != nil || !fromSeed.Equal(priv) {
		t.Errorf("desde la semilla = (igual=%v, err=%v), quería la clave original", err == nil && fromSeed.Equal(priv), err)
	}
	fromFull, err := lease.ParsePrivateKeyBase64(base64.StdEncoding.EncodeToString(priv))
	if err != nil || !fromFull.Equal(priv) {
		t.Errorf("desde la clave expandida = (igual=%v, err=%v), quería la clave original", err == nil && fromFull.Equal(priv), err)
	}

	bad := []struct {
		name, input, wantPrefix string
	}{
		{"not base64", "%%% no es base64 %%%", "lease: base64 de clave inválido: "},
		{"url alphabet is not standard base64", "-_-_", "lease: base64 de clave inválido: "},
		{"empty", "", "lease: tamaño de clave Ed25519 inesperado: 0 bytes"},
		{"31 bytes", base64.StdEncoding.EncodeToString(make([]byte, 31)), "lease: tamaño de clave Ed25519 inesperado: 31 bytes"},
		{"33 bytes", base64.StdEncoding.EncodeToString(make([]byte, 33)), "lease: tamaño de clave Ed25519 inesperado: 33 bytes"},
		{"63 bytes", base64.StdEncoding.EncodeToString(make([]byte, 63)), "lease: tamaño de clave Ed25519 inesperado: 63 bytes"},
		{"seed with surrounding space", " " + seedBase64(priv), "lease: base64 de clave inválido: "},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			key, err := lease.ParsePrivateKeyBase64(c.input)
			if err == nil || key != nil {
				t.Fatalf("ParsePrivateKeyBase64 = (%d bytes, %v), quería (nil, error)", len(key), err)
			}
			if !strings.HasPrefix(err.Error(), c.wantPrefix) {
				t.Errorf("error = %q, quería el prefijo %q", err, c.wantPrefix)
			}
		})
	}
}

// TestLoadPrivateKeyPEM: carga una Ed25519 en PKCS#8 y rechaza, con su texto, lo que no lo es.
func TestLoadPrivateKeyPEM(t *testing.T) {
	priv := newKey(t)
	got, err := lease.LoadPrivateKeyPEM(writeEd25519PEM(t, priv))
	if err != nil || !got.Equal(priv) {
		t.Fatalf("LoadPrivateKeyPEM = (igual=%v, err=%v), quería la clave escrita", err == nil && got.Equal(priv), err)
	}

	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generando la clave EC del test: %v", err)
	}
	ecDER, err := x509.MarshalPKCS8PrivateKey(ecKey)
	if err != nil {
		t.Fatalf("serializando la clave EC del test: %v", err)
	}
	notPEM := filepath.Join(t.TempDir(), "no.pem")
	if err := os.WriteFile(notPEM, []byte("esto no es PEM"), 0o600); err != nil {
		t.Fatalf("escribiendo el fichero del test: %v", err)
	}

	bad := []struct {
		name, path, want string
	}{
		{"missing file", filepath.Join(t.TempDir(), "no-existe.pem"), "lease: leer clave PEM "},
		{"not pem", notPEM, "no es PEM válido"},
		{"pem that is not pkcs8", writePEM(t, "PRIVATE KEY", []byte("basura")), "lease: parsear clave PKCS#8: "},
		{"pkcs8 of another algorithm", writePEM(t, "PRIVATE KEY", ecDER), "lease: la clave PEM no es Ed25519"},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			key, err := lease.LoadPrivateKeyPEM(c.path)
			if err == nil || key != nil {
				t.Fatalf("LoadPrivateKeyPEM = (%d bytes, %v), quería (nil, error)", len(key), err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %q, quería que contuviera %q", err, c.want)
			}
		})
	}
}

// TestResolveSigningKey_Precedence: fichero > base64 > efímera, y la fuente que devuelve.
func TestResolveSigningKey_Precedence(t *testing.T) {
	fileKey, b64Key := newKey(t), newKey(t)
	pemPath := writeEd25519PEM(t, fileKey)

	cases := []struct {
		name            string
		pemFile, base64 string
		wantSource      lease.KeySource
		wantKey         ed25519.PrivateKey // nil: una efímera, distinta de las configuradas
	}{
		{"file wins over base64", pemPath, seedBase64(b64Key), lease.KeySourceFile, fileKey},
		{"file alone", pemPath, "", lease.KeySourceFile, fileKey},
		{"base64 alone", "", seedBase64(b64Key), lease.KeySourceBase64, b64Key},
		{"nothing configured is ephemeral", "", "", lease.KeySourceGenerated, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			key, source, err := lease.ResolveSigningKey(c.pemFile, c.base64)
			if err != nil {
				t.Fatalf("ResolveSigningKey: error inesperado %v", err)
			}
			if source != c.wantSource {
				t.Errorf("fuente = %q, quería %q", source, c.wantSource)
			}
			if len(key) != ed25519.PrivateKeySize {
				t.Fatalf("clave de %d bytes, quería %d", len(key), ed25519.PrivateKeySize)
			}
			if c.wantKey != nil && !key.Equal(c.wantKey) {
				t.Error("la clave resuelta no es la de la fuente que debía ganar")
			}
			if c.wantKey == nil && (key.Equal(fileKey) || key.Equal(b64Key)) {
				t.Error("la efímera coincide con una de las configuradas")
			}
		})
	}
}

// TestResolveSigningKey_ConfiguredIsStable_GeneratedIsNot: reglas.md T-10. Una clave
// configurada es la misma en cada llamada (los Edge siguen validando tras un reinicio); la
// generada cambia SIEMPRE. No existe una clave por defecto fija a la que «caer».
func TestResolveSigningKey_ConfiguredIsStable_GeneratedIsNot(t *testing.T) {
	priv := newKey(t)
	pemPath := writeEd25519PEM(t, priv)

	for name, resolve := range map[string]func() (ed25519.PrivateKey, lease.KeySource, error){
		"file": func() (ed25519.PrivateKey, lease.KeySource, error) { return lease.ResolveSigningKey(pemPath, "") },
		"base64": func() (ed25519.PrivateKey, lease.KeySource, error) {
			return lease.ResolveSigningKey("", seedBase64(priv))
		},
	} {
		first, _, err1 := resolve()
		second, _, err2 := resolve()
		if err1 != nil || err2 != nil || !first.Equal(second) || !first.Equal(priv) {
			t.Errorf("%s: dos llamadas no dieron la misma clave configurada (err %v, %v)", name, err1, err2)
		}
	}

	seen := make(map[string]bool)
	for range 5 {
		key, source, err := lease.ResolveSigningKey("", "")
		if err != nil || source != lease.KeySourceGenerated {
			t.Fatalf("ResolveSigningKey sin configurar = (fuente %q, err %v)", source, err)
		}
		if seen[string(key)] {
			t.Fatal("la clave generada se repitió entre llamadas: tiene que ser efímera, nunca fija")
		}
		seen[string(key)] = true
	}
}

// TestResolveSigningKey_FailureDoesNotFallBack: si la fuente elegida falla, devuelve el error con
// SU fuente y sin clave; un fichero ilegible no degrada a base64 ni a una efímera.
func TestResolveSigningKey_FailureDoesNotFallBack(t *testing.T) {
	good := seedBase64(newKey(t))
	missing := filepath.Join(t.TempDir(), "no-existe.pem")

	key, source, err := lease.ResolveSigningKey(missing, good)
	if err == nil || key != nil || source != lease.KeySourceFile {
		t.Errorf("fichero ilegible con base64 bueno = (%d bytes, fuente %q, err %v), quería (nil, file, error)", len(key), source, err)
	}
	key, source, err = lease.ResolveSigningKey("", "no-es-base64-!!")
	if err == nil || key != nil || source != lease.KeySourceBase64 {
		t.Errorf("base64 inválido = (%d bytes, fuente %q, err %v), quería (nil, base64, error)", len(key), source, err)
	}
}
