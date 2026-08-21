package bootstrap

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/config"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/objectstore"
)

// flowRuntimeDeps agrupa las dependencias del Motor de Flujos que se construyen
// con fail-fast a partir de secretos de config: el stack de cifrado de PII (Plan
// 011) y el almacén de objetos R2 (Plan 017). Se devuelven juntas para que el
// arranque tenga UNA sola rama de error (cualquier fallo aborta el proceso).
type flowRuntimeDeps struct {
	// contacts resuelve la identidad OPACA del contacto (cifra/descifra PII).
	contacts contact.Resolver
	// contactsPG es EXACTAMENTE LA MISMA INSTANCIA que contacts, sin el envoltorio
	// de la interfaz. Existe por una sola razón: el backfill de arranque de T4.2
	// (BackfillPushName) es una operación de MANTENIMIENTO del almacén, no parte del
	// contrato Resolver —el runtime del motor de flujos no tiene por qué poder
	// dispararla— así que no se añade al interfaz, y desde fuera hace falta el tipo
	// concreto para llamarla.
	//
	// 🔴 SE COMPARTE LA INSTANCIA, NO SE CONSTRUYE UNA SEGUNDA. Un segundo
	// NewPostgresResolver aquí traería su propio cipher y su propio KeyProvider, y
	// los sobres que escribiera el backfill quedarían cerrados con un keyring que la
	// persistencia no conoce: el mismo modo de fallo que el aviso de fleetRepo en
	// bootstrap.go describe para la flota. La alternativa —una aserción de tipo sobre
	// `contacts`— haría lo mismo pero fallando en tiempo de ejecución el día que
	// alguien meta un decorador por en medio.
	contactsPG *contact.PostgresResolver
	// cipher y kp son el stack de cifrado de PII (Plan 011); el runtime los usa vía
	// el resolver, y el endpoint admin /admin/crypto/rekey los necesita en crudo
	// para la rotación de KEK (Plan 012).
	cipher *crypto.FieldCipher
	kp     crypto.KeyProvider
	// presign firma la key de un adjunto al despachar un nodo media (Plan 017).
	presign objectstore.PresignClient
}

// buildFlowRuntimeDeps construye, con fail-fast, las dependencias anteriores: el
// KeyProvider de PII (ADR-0017: la KEK vive separada del dato — en el KMS con
// WAPP_KEK_PROVIDER=kms, o en env como fallback de dev local; ADR-0036 / Plan 042
// · T9.1) y el PresignClient de Cloudflare R2 (§3/§8: valida el bucket con
// HeadBucket; sin bucket/credenciales el proceso no levanta). Mismo R2 en dev y
// prod (sin MinIO local); credenciales por WAPP_STORAGE_S3_* (.env, no versionado).
func buildFlowRuntimeDeps(ctx context.Context, cfg config.AppConfig, db *sql.DB) (flowRuntimeDeps, error) {
	kp, err := crypto.NewKeyProvider(ctx, crypto.ProviderConfig{
		Provider: cfg.Crypto.KEKProvider,
		Prod:     cfg.Env == "prod",
		Env: crypto.KeyringConfig{
			KeyringB64: cfg.Crypto.KEKKeyring,
			CurrentID:  cfg.Crypto.KEKCurrent,
			MasterB64:  cfg.Crypto.KEKMasterB64,
			IndexB64:   cfg.Crypto.KEKIndexB64,
		},
		KMS: crypto.KMSConfig{
			KeyName:            cfg.Crypto.KEKKMSKey,
			KeyringB64:         cfg.Crypto.KEKKMSKeyring,
			CurrentID:          cfg.Crypto.KEKCurrent,
			IndexB64:           cfg.Crypto.KEKIndexB64,
			IndexCiphertextB64: cfg.Crypto.KEKKMSIndexB64,
		},
	})
	if err != nil {
		return flowRuntimeDeps{}, fmt.Errorf("construyendo KeyProvider de PII (Plan 011; provider %q): %w",
			cfg.Crypto.KEKProvider, err)
	}
	cipher := crypto.NewFieldCipher(kp)

	presignClient, err := objectstore.NewR2PresignClient(ctx, objectstore.R2Config{
		Region:          cfg.Storage.Region,
		Bucket:          cfg.Storage.Bucket,
		AccessKeyID:     cfg.Storage.AccessKeyID,
		SecretAccessKey: cfg.Storage.SecretAccessKey,
		Endpoint:        cfg.Storage.Endpoint,
		PresignExpiry:   cfg.Storage.PresignExpiry,
	})
	if err != nil {
		return flowRuntimeDeps{}, fmt.Errorf("construyendo PresignClient R2 (Plan 017): %w", err)
	}
	// Una sola instancia, dos vistas: la interfaz para el runtime, el tipo concreto
	// para el backfill de arranque de T4.2 (ver el comentario de contactsPG).
	contacts := contact.NewPostgresResolver(db, cipher, kp)
	return flowRuntimeDeps{
		contacts:   contacts,
		contactsPG: contacts,
		cipher:     cipher,
		kp:         kp,
		presign:    presignClient,
	}, nil
}
