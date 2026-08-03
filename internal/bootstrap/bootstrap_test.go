package bootstrap

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

func TestBuildJWTManagers_Table(t *testing.T) {
	log := logging.New(config.AppConfig{})

	tests := []struct {
		name    string
		cfg     config.AppConfig
		wantErr bool
		wantKid string
	}{
		{
			name:    "dev environment returns ephemeral bundle",
			cfg:     config.AppConfig{Env: "dev"},
			wantErr: false,
			wantKid: defaultES256Kid,
		},
		{
			// En prod el `kid` es obligatorio: es lo que ata cada token a su
			// entrada de verificación cuando la clave rota.
			name: "prod missing WAPP_JWT_KID returns error",
			cfg: config.AppConfig{
				Env: "prod",
				JWT: config.JWTConfig{Kid: ""},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bundle, err := buildJWTManagers(tt.cfg, log)
			if (err != nil) != tt.wantErr {
				t.Errorf("buildJWTManagers() error = %v, wantErr = %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if bundle == nil {
					t.Error("bundle es nil")
				} else if bundle.kid != tt.wantKid {
					t.Errorf("bundle.kid = %s, quiero %s", bundle.kid, tt.wantKid)
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

func TestBuildLeaseManager_Ephemeral(t *testing.T) {
	log := logging.New(config.AppConfig{})
	cfg := config.AppConfig{
		Lease: config.LeaseConfig{
			TTLMinutes: 5,
		},
	}

	mgr, err := buildLeaseManager(cfg, nil, log)
	if err != nil {
		t.Fatalf("buildLeaseManager err: %v", err)
	}
	if mgr == nil {
		t.Fatal("mgr no debe ser nil")
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
