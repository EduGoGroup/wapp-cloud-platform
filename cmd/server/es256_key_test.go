package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/config"
)

// recordingLogger captura los mensajes Warn para verificar los avisos de
// material EFÍMERO de dev (no apto para producción). Es concurrency-safe por si
// algún día se usa desde varias goroutines en un test.
type recordingLogger struct {
	mu    sync.Mutex
	warns []string
}

func (l *recordingLogger) record(dst *[]string, msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	*dst = append(*dst, msg)
}

func (l *recordingLogger) Debug(string, ...any) {}
func (l *recordingLogger) Info(string, ...any)  {}
func (l *recordingLogger) Warn(msg string, _ ...any) {
	l.record(&l.warns, msg)
}
func (l *recordingLogger) Error(string, ...any) {}
func (l *recordingLogger) With(...any) sharedlogger.Logger {
	return l
}

func (l *recordingLogger) warnCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.warns)
}

// writeECKeyPEM escribe una clave EC en PEM (PKCS#8 o SEC1) con los permisos
// dados y devuelve su ruta. Falla el test si algo no cuadra.
func writeECKeyPEM(t *testing.T, key *ecdsa.PrivateKey, pkcs8 bool, perm os.FileMode) string {
	t.Helper()
	var der []byte
	var err error
	if pkcs8 {
		der, err = x509.MarshalPKCS8PrivateKey(key)
	} else {
		der, err = x509.MarshalECPrivateKey(key)
	}
	if err != nil {
		t.Fatalf("serializando clave EC: %v", err)
	}
	blockType := "PRIVATE KEY"
	if !pkcs8 {
		blockType = "EC PRIVATE KEY"
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})

	path := filepath.Join(t.TempDir(), "jwt-es256.pem")
	if err := os.WriteFile(path, pemBytes, perm); err != nil {
		t.Fatalf("escribiendo PEM: %v", err)
	}
	// os.WriteFile respeta umask; forzamos el modo exacto para el chequeo de permisos.
	if err := os.Chmod(path, perm); err != nil {
		t.Fatalf("chmod PEM: %v", err)
	}
	return path
}

func newP256Key(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generando clave P-256: %v", err)
	}
	return key
}

func TestParseECP256PrivateKeyPEM_PKCS8(t *testing.T) {
	key := newP256Key(t)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("PKCS#8: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})

	got, err := parseECP256PrivateKeyPEM(pemBytes)
	if err != nil {
		t.Fatalf("parse PKCS#8: %v", err)
	}
	if !got.Equal(key) {
		t.Fatal("la clave parseada no coincide con la original (PKCS#8)")
	}
}

func TestParseECP256PrivateKeyPEM_SEC1(t *testing.T) {
	key := newP256Key(t)
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("SEC1: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})

	got, err := parseECP256PrivateKeyPEM(pemBytes)
	if err != nil {
		t.Fatalf("parse SEC1: %v", err)
	}
	if !got.Equal(key) {
		t.Fatal("la clave parseada no coincide con la original (SEC1)")
	}
}

func TestParseECP256PrivateKeyPEM_WrongCurve(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("generando P-384: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("PKCS#8 P-384: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})

	if _, err := parseECP256PrivateKeyPEM(pemBytes); err == nil {
		t.Fatal("se esperaba error por curva no P-256")
	}
}

func TestParseECP256PrivateKeyPEM_NotPEM(t *testing.T) {
	if _, err := parseECP256PrivateKeyPEM([]byte("no soy un PEM")); err == nil {
		t.Fatal("se esperaba error por PEM inválido")
	}
}

func TestBuildES256Key_FileOK_PKCS8AndSEC1(t *testing.T) {
	for _, pkcs8 := range []bool{true, false} {
		name := "SEC1"
		if pkcs8 {
			name = "PKCS8"
		}
		t.Run(name, func(t *testing.T) {
			key := newP256Key(t)
			path := writeECKeyPEM(t, key, pkcs8, 0o600)
			cfg := config.AppConfig{Env: "prod", JWT: config.JWTConfig{ECPrivateKeyFile: path}}

			got, err := buildES256Key(cfg, &recordingLogger{})
			if err != nil {
				t.Fatalf("buildES256Key: %v", err)
			}
			if !got.Equal(key) {
				t.Fatal("la clave cargada no coincide con el archivo")
			}
		})
	}
}

func TestBuildES256Key_ProdLaxPermsRejected(t *testing.T) {
	key := newP256Key(t)
	path := writeECKeyPEM(t, key, true, 0o644)
	cfg := config.AppConfig{Env: "prod", JWT: config.JWTConfig{ECPrivateKeyFile: path}}

	if _, err := buildES256Key(cfg, &recordingLogger{}); err == nil {
		t.Fatal("se esperaba fail-fast en prod por permisos laxos (0644)")
	}
}

func TestBuildES256Key_DevLaxPermsAccepted(t *testing.T) {
	// En dev los permisos laxos NO bloquean: solo prod endurece.
	key := newP256Key(t)
	path := writeECKeyPEM(t, key, true, 0o644)
	cfg := config.AppConfig{Env: "dev", JWT: config.JWTConfig{ECPrivateKeyFile: path}}

	if _, err := buildES256Key(cfg, &recordingLogger{}); err != nil {
		t.Fatalf("dev con permisos laxos debería cargar: %v", err)
	}
}

func TestBuildES256Key_ProdMissingFileFailsFast(t *testing.T) {
	cfg := config.AppConfig{Env: "prod", JWT: config.JWTConfig{ECPrivateKeyFile: ""}}
	if _, err := buildES256Key(cfg, &recordingLogger{}); err == nil {
		t.Fatal("se esperaba fail-fast en prod sin WAPP_JWT_EC_PRIVATE_KEY_FILE")
	}
}

func TestBuildES256Key_DevEphemeralWithWarning(t *testing.T) {
	cfg := config.AppConfig{Env: "dev", JWT: config.JWTConfig{ECPrivateKeyFile: ""}}
	log := &recordingLogger{}

	got, err := buildES256Key(cfg, log)
	if err != nil {
		t.Fatalf("dev sin archivo debería generar par efímero: %v", err)
	}
	if got == nil || got.Curve != elliptic.P256() {
		t.Fatal("par efímero de dev inválido (curva != P-256)")
	}
	if log.warnCount() == 0 {
		t.Fatal("se esperaba un warning por clave EFÍMERA de dev")
	}
	if !strings.Contains(strings.ToLower(log.warns[0]), "efímera") {
		t.Fatalf("warning inesperado: %q", log.warns[0])
	}
}

func TestBuildES256Key_ProdInvalidFileFailsFast(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.pem")
	if err := os.WriteFile(path, []byte("basura"), 0o600); err != nil {
		t.Fatalf("escribiendo archivo inválido: %v", err)
	}
	cfg := config.AppConfig{Env: "prod", JWT: config.JWTConfig{ECPrivateKeyFile: path}}
	if _, err := buildES256Key(cfg, &recordingLogger{}); err == nil {
		t.Fatal("se esperaba error por clave inválida")
	}
}
