// Copia de internal/bootstrap/arranque/pki_test.go @ 80807ba (F0 · 05 §6). Desde F8
// (conmutar(conversacion)) el arranque que este test ejercita no cablea ningún paquete viejo.
package arranque

import (
	"crypto/tls"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/config"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/logging"
)

func TestBuildCloudEncKeypair_Table(t *testing.T) {
	log := logging.New(config.AppConfig{})
	validB64 := base64.StdEncoding.EncodeToString(make([]byte, 32))

	tests := []struct {
		name    string
		b64     string
		wantErr bool
	}{
		{
			name:    "ephemeral key when config is empty",
			b64:     "",
			wantErr: false,
		},
		{
			name:    "valid 32-byte base64 private key",
			b64:     validB64,
			wantErr: false,
		},
		{
			name:    "invalid base64 encoding",
			b64:     "invalid!base64",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.AppConfig{
				Crypto: config.CryptoConfig{
					CloudEncPrivKeyB64: tt.b64,
				},
			}
			pub, priv, err := buildCloudEncKeypair(cfg, log)
			if (err != nil) != tt.wantErr {
				t.Errorf("buildCloudEncKeypair() error = %v, wantErr = %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if len(pub) != 32 || len(priv) != 32 {
					t.Errorf("medidas de llaves invalidas: pub=%d, priv=%d (quiero 32, 32)", len(pub), len(priv))
				}
			}
		})
	}
}

func TestPKILoaders_Table(t *testing.T) {
	tmpDir := t.TempDir()
	caCertFile := filepath.Join(tmpDir, "ca.crt")
	if err := os.WriteFile(caCertFile, []byte("fake-cert"), 0600); err != nil {
		t.Fatalf("WriteFile ca.crt err: %v", err)
	}

	tests := []struct {
		name    string
		cfg     config.AppConfig
		wantErr bool
	}{
		{
			name: "missing ca cert and key files",
			cfg: config.AppConfig{
				PKI: config.PKIConfig{
					CACertFile:     "/nonexistent/ca.crt",
					CAKeyFile:      "/nonexistent/ca.key",
					ServerCertFile: "/nonexistent/server.crt",
					ServerKeyFile:  "/nonexistent/server.key",
				},
			},
			wantErr: true,
		},
		{
			name: "ca cert exists but ca key missing",
			cfg: config.AppConfig{
				PKI: config.PKIConfig{
					CACertFile: caCertFile,
					CAKeyFile:  filepath.Join(tmpDir, "ca.key"),
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := loadPKI(tt.cfg)
			if err == nil {
				t.Errorf("loadPKI() debió retornar error")
			}
			_, err = loadCA(tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("loadCA() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestEnrollServerCreds(t *testing.T) {
	cert := tls.Certificate{}
	creds := EnrollServerCreds(cert)
	if creds == nil {
		t.Fatal("creds no debe ser nil")
	}
	if creds.Info().SecurityProtocol != "tls" {
		t.Fatalf("SecurityProtocol = %s, quiero tls", creds.Info().SecurityProtocol)
	}
}
