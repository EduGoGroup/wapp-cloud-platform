package bootstrap

import (
	"database/sql"
	"errors"
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
	// Fail-fast en prod: sin clave de firma configurada, ResolveSigningKey
	// generaría en silencio (salvo el log.Warn de más abajo) una clave Ed25519
	// EFÍMERA que cambia en cada arranque -- el kill-switch quedaría atado a una
	// clave que el Edge no puede validar de forma estable (ADR-0007). En dev el
	// comportamiento no cambia: se sigue generando la clave efímera.
	if cfg.Env == "prod" && cfg.Lease.PrivateKeyFile == "" && cfg.Lease.PrivateKeyB64 == "" {
		return nil, errors.New("WAPP_LEASE_PRIVATE_KEY_FILE o WAPP_LEASE_PRIVATE_KEY_B64 es obligatorio en prod (ADR-0007: el lease no puede firmarse con una clave efímera)")
	}

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
