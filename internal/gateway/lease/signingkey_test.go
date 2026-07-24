package lease

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
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
		name      string
		b64       string
		wantErr   bool
		wantLen   int
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
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
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
	os.WriteFile(invalidPEMFile, []byte("contenido invalido no pem"), 0600)

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
