// Copia de internal/bootstrap/arranque/flows.go @ 80807ba (F0 · 05 §6): cablea paquetes VIEJOS,
// salvo el resolver de contactos, que desde F1 (T1.16, conmutar(nucleo)) es el de
// internal/nucleo/contact detrás del adaptador contactBridge (bridge_contact.go).
package arranque

import (
	"context"
	"database/sql"
	"fmt"

	viejo "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/config"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/objectstore"
)

// flowRuntimeDeps agrupa las dependencias del Motor de Flujos que se construyen
// con fail-fast a partir de secretos de config: el stack de cifrado de PII (Plan
// 011) y el almacén de objetos R2 (Plan 017). Se devuelven juntas para que el
// arranque tenga UNA sola rama de error (cualquier fallo aborta el proceso).
type flowRuntimeDeps struct {
	// contacts resuelve la identidad OPACA del contacto (cifra/descifra PII). Es el
	// viejo.Resolver que piden flowruntime.New e intakes.NewNotifier, pero detrás hay
	// el resolver NUEVO (nucleo/contact) envuelto en contactBridge (F1 · T1.16).
	//
	// ✎ F1 (T-8 de la spec de F1): la copia ya no tiene el campo contactsPG del viejo.
	// Se escribía y no se leía en ningún sitio: su único motivo, el backfill de arranque
	// BackfillPushName (T4.2), murió en 58e92a2 (T5.4).
	contacts viejo.Resolver
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
	// 🔴 El resolver se construye con ESTE cipher y ESTE kp, los mismos que reciben
	// fleet, events, intakes, integrations y tenantllm en la fase 3: otro KeyProvider
	// calcularía otro value_bidx y duplicaría contactos en silencio.
	contacts := newContactResolver(db, cipher, kp)
	return flowRuntimeDeps{
		contacts: contacts,
		cipher:   cipher,
		kp:       kp,
		presign:  presignClient,
	}, nil
}
