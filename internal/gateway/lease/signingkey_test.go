package lease

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func TestParsePrivateKeyBase64_Table(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey err: %v", err)
	}

	b64Full := base64.StdEncoding.EncodeToString(priv)
	b64Seed := base64.StdEncoding.EncodeToString(priv.Seed())
	b64Short := base64.StdEncoding.EncodeToString([]byte("corto"))

	tests := []struct {
		name    string
		b64     string
		wantErr bool
		wantLen int
	}{
		{
			name:    "full 64-byte private key",
			b64:     b64Full,
			wantErr: false,
			wantLen: 64,
		},
		{
			name:    "32-byte seed",
			b64:     b64Seed,
			wantErr: false,
			wantLen: 64,
		},
		{
			name:    "invalid base64 encoding",
			b64:     "invalid!b64",
			wantErr: true,
		},
		{
			name:    "invalid byte length",
			b64:     b64Short,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParsePrivateKeyBase64(tt.b64)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParsePrivateKeyBase64() error = %v, wantErr = %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && len(got) != tt.wantLen {
				t.Errorf("ParsePrivateKeyBase64() len = %d, wantLen = %d", len(got), tt.wantLen)
			}
		})
	}
}

func TestResolveSigningKey_Table(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey err: %v", err)
	}
	validB64 := base64.StdEncoding.EncodeToString(priv)

	tests := []struct {
		name       string
		pemFile    string
		base64Key  string
		wantSource KeySource
		wantErr    bool
	}{
		{
			name:       "valid base64 key",
			pemFile:    "",
			base64Key:  validB64,
			wantSource: KeySourceBase64,
			wantErr:    false,
		},
		{
			name:       "fallback to generated dev key when empty",
			pemFile:    "",
			base64Key:  "",
			wantSource: KeySourceGenerated,
			wantErr:    false,
		},
		{
			name:       "invalid pem file path returns error",
			pemFile:    "/nonexistent/path/key.pem",
			base64Key:  "",
			wantSource: KeySourceFile,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, src, err := ResolveSigningKey(tt.pemFile, tt.base64Key)
			if (err != nil) != tt.wantErr {
				t.Errorf("ResolveSigningKey() error = %v, wantErr = %v", err, tt.wantErr)
				return
			}
			if src != tt.wantSource {
				t.Errorf("ResolveSigningKey() source = %v, wantSource = %v", src, tt.wantSource)
			}
			if !tt.wantErr && len(key) != 64 {
				t.Errorf("ResolveSigningKey() key length = %d, want 64", len(key))
			}
		})
	}
}

func TestLoadPrivateKeyPEM_Table(t *testing.T) {
	tmpDir := t.TempDir()
	invalidPEMFile := filepath.Join(tmpDir, "invalid.pem")
	if err := os.WriteFile(invalidPEMFile, []byte("contenido invalido no pem"), 0600); err != nil {
		t.Fatalf("WriteFile PEM invalido err: %v", err)
	}

	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{
			name:    "file does not exist",
			path:    filepath.Join(tmpDir, "nonexistent.pem"),
			wantErr: true,
		},
		{
			name:    "file exists but is not valid PEM",
			path:    invalidPEMFile,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadPrivateKeyPEM(tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("LoadPrivateKeyPEM() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

// TestResolveSigningKey_ConfiguradaEstable_GeneradaEfimera cubre T1.2 midiendo
// la ÚNICA propiedad que justifica el fail-fast de buildLeaseManager en prod:
// la clave configurada (base64) es ESTABLE entre "arranques" del proceso,
// mientras que el camino sin configurar (KeySourceGenerated) devuelve una
// clave DISTINTA en cada llamada -- por eso firmar leases con ella deja al
// Edge sin poder validar nada de forma estable (ADR-0007).
// Se pone rojo si: alguien "arregla" el problema cacheando la clave efímera en
// un paquete global (haciéndola estable y el fail-fast aparentemente
// innecesario), o si ResolveSigningKey deja de honrar el base64 configurado.
func TestResolveSigningKey_ConfiguradaEstable_GeneradaEfimera(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey err: %v", err)
	}
	b64 := base64.StdEncoding.EncodeToString(priv)

	priv1, src1, err := ResolveSigningKey("", b64)
	if err != nil {
		t.Fatalf("ResolveSigningKey() primera llamada err: %v", err)
	}
	priv2, src2, err := ResolveSigningKey("", b64)
	if err != nil {
		t.Fatalf("ResolveSigningKey() segunda llamada err: %v", err)
	}
	if src1 != KeySourceBase64 || src2 != KeySourceBase64 {
		t.Fatalf("KeySource = (%v, %v), want (%v, %v)", src1, src2, KeySourceBase64, KeySourceBase64)
	}
	if !bytes.Equal(priv1, priv) || !bytes.Equal(priv2, priv) {
		t.Fatal("la clave resuelta desde base64 no es la configurada")
	}

	// El contraste: sin configurar, cada llamada trae una clave NUEVA.
	gen1, srcG1, err := ResolveSigningKey("", "")
	if err != nil {
		t.Fatalf("ResolveSigningKey() generada 1 err: %v", err)
	}
	gen2, srcG2, err := ResolveSigningKey("", "")
	if err != nil {
		t.Fatalf("ResolveSigningKey() generada 2 err: %v", err)
	}
	if srcG1 != KeySourceGenerated || srcG2 != KeySourceGenerated {
		t.Fatalf("KeySource generada = (%v, %v), want (%v, %v)", srcG1, srcG2, KeySourceGenerated, KeySourceGenerated)
	}
	if bytes.Equal(gen1, gen2) {
		t.Fatal("la clave de dev debe ser EFÍMERA (distinta en cada llamada): si se cachea, el fail-fast de prod pierde su motivo")
	}
}

// TestResolveSigningKey_PEMFile_CargaLaClaveDelArchivo cubre el camino de
// archivo de ResolveSigningKey/LoadPrivateKeyPEM, del que hasta ahora solo
// había casos de ERROR (TestLoadPrivateKeyPEM_Table cubre inexistente y PEM
// inválido; TestResolveSigningKey_Table, la ruta inexistente). Aquí se
// comprueba el camino feliz: el PEM PKCS#8 escrito en disco se resuelve a la
// MISMA clave privada, con KeySourceFile.
// Se pone rojo si: LoadPrivateKeyPEM devuelve una clave distinta de la del
// archivo (p. ej. cayendo al fallback de generación) o si la prioridad
// archivo > base64 > generación se rompe.
func TestResolveSigningKey_PEMFile_CargaLaClaveDelArchivo(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey err: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey err: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})

	path := filepath.Join(t.TempDir(), "lease-signing-key.pem")
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatalf("escribiendo PEM temporal: %v", err)
	}

	// Se pasa TAMBIÉN un base64 distinto para verificar de paso la prioridad
	// documentada (archivo > base64): debe ganar el archivo.
	_, otra, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey (otra) err: %v", err)
	}
	got, src, err := ResolveSigningKey(path, base64.StdEncoding.EncodeToString(otra))
	if err != nil {
		t.Fatalf("ResolveSigningKey() err: %v", err)
	}
	if src != KeySourceFile {
		t.Fatalf("KeySource = %v, want %v", src, KeySourceFile)
	}
	if !bytes.Equal(got, priv) {
		t.Fatal("ResolveSigningKey no devolvió la clave del archivo PEM (¿ganó el base64 o el fallback?)")
	}
}
