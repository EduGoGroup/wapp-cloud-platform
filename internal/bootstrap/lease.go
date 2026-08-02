package bootstrap

import (
	"database/sql"
	"fmt"
	"time"

	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/lease"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/config"
)

// buildLeaseManager resuelve la clave de firma del lease (archivo > base64 >
// generación de dev), construye el Manager con persistencia en PostgreSQL y
// loguea la clave pública en base64 para configurar el Validator del Edge (T6).
func buildLeaseManager(cfg config.AppConfig, db *sql.DB, log sharedlogger.Logger) (*lease.Manager, error) {
	priv, source, err := lease.ResolveSigningKey(cfg.Lease.PrivateKeyFile, cfg.Lease.PrivateKeyB64)
	if err != nil {
		return nil, fmt.Errorf("resolviendo clave de lease: %w", err)
	}

	opts := []lease.Option{}
	if cfg.Lease.TTLMinutes > 0 {
		opts = append(opts, lease.WithTTL(time.Duration(cfg.Lease.TTLMinutes)*time.Minute))
	}

	mgr, err := lease.NewManager(priv, lease.NewPostgresRepository(db), opts...)
	if err != nil {
		return nil, fmt.Errorf("construyendo lease manager: %w", err)
	}

	log.Info("clave pública del lease (configurar en el Edge)",
		"key_source", string(source),
		"public_key_base64", mgr.PublicKeyBase64(),
	)
	if source == lease.KeySourceGenerated {
		log.Warn("clave de lease EFÍMERA de dev: cambia en cada arranque (no apta para producción)")
	}
	return mgr, nil
}
