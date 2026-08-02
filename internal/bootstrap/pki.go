package bootstrap

import (
	"crypto/tls"
	"database/sql"
	"encoding/base64"
	"fmt"
	"os"

	"github.com/EduGoGroup/wapp-shared/envelope"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"
	"golang.org/x/crypto/curve25519"
	"google.golang.org/grpc/credentials"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/enroll"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/config"
)

// EnrollServerCreds construye credentials de TLS de servidor SOLAMENTE (sin
// exigir cert de cliente): el Edge enrola aquí antes de tener cert. NO se puede
// usar mtls.ServerCreds porque exige RequireAndVerifyClientCert.
func EnrollServerCreds(serverCert tls.Certificate) credentials.TransportCredentials {
	return credentials.NewTLS(&tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{serverCert},
	})
}

// loadPKI carga la CA firmante y el cert de servidor desde las rutas de config,
// las dos piezas PKI que comparten los listeners de enroll y CloudLink.
func loadPKI(cfg config.AppConfig) (*enroll.CA, tls.Certificate, error) {
	ca, err := loadCA(cfg)
	if err != nil {
		return nil, tls.Certificate{}, err
	}
	serverCert, err := tls.LoadX509KeyPair(cfg.PKI.ServerCertFile, cfg.PKI.ServerKeyFile)
	if err != nil {
		return nil, tls.Certificate{}, fmt.Errorf("cargando cert de servidor (%s / %s): %w",
			cfg.PKI.ServerCertFile, cfg.PKI.ServerKeyFile, err)
	}
	return ca, serverCert, nil
}

// loadCA carga la CA (cert + clave PEM) desde las rutas de config. La clave es
// necesaria para firmar CSRs en el enrolamiento; el cert alimenta el Pool del
// mTLS de CloudLink.
func loadCA(cfg config.AppConfig) (*enroll.CA, error) {
	certPEM, err := os.ReadFile(cfg.PKI.CACertFile)
	if err != nil {
		return nil, fmt.Errorf("leyendo cert de CA %q: %w", cfg.PKI.CACertFile, err)
	}
	keyPEM, err := os.ReadFile(cfg.PKI.CAKeyFile)
	if err != nil {
		return nil, fmt.Errorf("leyendo clave de CA %q: %w", cfg.PKI.CAKeyFile, err)
	}
	ca, err := enroll.LoadCAFromPEM(certPEM, keyPEM, 0)
	if err != nil {
		return nil, fmt.Errorf("cargando CA: %w", err)
	}
	return ca, nil
}

// buildEnrollServer construye el servidor de enrolamiento y resuelve el par
// X25519 de cifrado de tránsito de la nube (Plan 011 §10.F): publica la pública
// al Edge en el enrolamiento y devuelve la privada para que el gateway abra el
// enc_payload al ingreso.
func buildEnrollServer(cfg config.AppConfig, db *sql.DB, ca *enroll.CA, log sharedlogger.Logger) (*enroll.Server, []byte, error) {
	cloudEncPub, cloudEncPriv, err := buildCloudEncKeypair(cfg, log)
	if err != nil {
		return nil, nil, err
	}
	enrollSvc := enroll.NewService(
		enroll.NewPostgresCodeStore(db),
		ca,
		enroll.NewPostgresEdgeCertRepository(db),
	)
	return enroll.NewServer(enrollSvc, log, enroll.WithCloudEncPubkey(cloudEncPub)), cloudEncPriv, nil
}

// buildCloudEncKeypair resuelve el par X25519 de cifrado de tránsito de la nube
// (Plan 011 §10.F). Si WAPP_CLOUD_ENC_PRIVKEY_B64 está, decodifica la privada
// (32B) y deriva la pública multiplicando por el punto base de la curva; si falta,
// genera un par efímero de dev (con warning, como la clave del lease). Loguea la
// pública en base64 para diagnóstico y para configurar el Edge fuera de banda.
func buildCloudEncKeypair(cfg config.AppConfig, log sharedlogger.Logger) (pub, priv []byte, err error) {
	if b64 := cfg.Crypto.CloudEncPrivKeyB64; b64 != "" {
		priv, err = base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, nil, fmt.Errorf("clave de cifrado de la nube: base64 inválido: %w", err)
		}
		if len(priv) != envelope.PrivateKeySize {
			return nil, nil, fmt.Errorf("clave de cifrado de la nube: debe medir %d bytes (X25519), mide %d",
				envelope.PrivateKeySize, len(priv))
		}
		pub, err = curve25519.X25519(priv, curve25519.Basepoint)
		if err != nil {
			return nil, nil, fmt.Errorf("derivando pública de cifrado de la nube: %w", err)
		}
		log.Info("clave pública de cifrado de la nube (publicada al Edge en el enrolamiento)",
			"key_source", "config",
			"public_key_base64", base64.StdEncoding.EncodeToString(pub),
		)
		return pub, priv, nil
	}

	pub, priv, err = envelope.GenerateKeyPair()
	if err != nil {
		return nil, nil, fmt.Errorf("generando par de cifrado de la nube: %w", err)
	}
	log.Info("clave pública de cifrado de la nube (publicada al Edge en el enrolamiento)",
		"key_source", "generated",
		"public_key_base64", base64.StdEncoding.EncodeToString(pub),
	)
	log.Warn("clave de cifrado de la nube EFÍMERA de dev: cambia en cada arranque (no apta para producción)")
	return pub, priv, nil
}
