package tenantllm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// Postgres es la implementación real de Store sobre database/sql (mismo estilo
// que internal/integrations/postgres.go: SQL raw con placeholders $1..$n, sin
// ORM).
type Postgres struct {
	db     *sql.DB
	cipher *crypto.FieldCipher
}

// NewPostgres construye el store con la conexión y el cifrador de campo que
// custodia la API key (el MISMO KeyProvider de los planes 011/012 que ya usan
// internal/intakes para buyer_data e internal/integrations para el secreto HMAC
// — patrón replicado por la migración 0071). Un único keyring, una única
// rotación que gestionar.
func NewPostgres(db *sql.DB, cipher *crypto.FieldCipher) *Postgres {
	return &Postgres{db: db, cipher: cipher}
}

// Get implementa Store.Get.
//
// NO selecciona api_key_enc para leerlo, solo para saber si existe: la
// credencial no tiene por qué materializarse en memoria cuando lo que se
// pregunta es si está configurada. Como la columna es NOT NULL, `IS NOT NULL`
// sobre una fila que existe es siempre cierto — se escribe igual, y no se
// sustituye por un `true` literal, para que el día que alguien afloje el NOT
// NULL la respuesta siga diciendo la verdad.
func (p *Postgres) Get(ctx context.Context, tenantID string) (Config, bool, error) {
	var cfg Config
	err := p.db.QueryRowContext(ctx, `
		SELECT tenant_id, provider, model, api_key_enc IS NOT NULL, consented_at, created_at, updated_at
		FROM public.tenant_llm
		WHERE tenant_id = $1
	`, tenantID).Scan(&cfg.TenantID, &cfg.Provider, &cfg.Model, &cfg.HasAPIKey,
		&cfg.ConsentedAt, &cfg.CreatedAt, &cfg.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Config{}, false, nil
	}
	if err != nil {
		return Config{}, false, fmt.Errorf("tenantllm: leer configuración de %s: %w", tenantID, err)
	}
	return cfg, true, nil
}

// Upsert implementa Store.Upsert.
//
// El upsert reemplaza las SEIS columnas de negocio, incluidas las tres del
// sobre: cada PUT trae una clave y esa clave sustituye a la anterior. No existe
// el camino «actualiza el modelo sin tocar la clave» que sí tiene
// integrations.UpsertTenantIntegration, y es deliberado (ver el comentario del
// puerto y el de la migración 0071).
//
// `created_at` NO se pisa en el DO UPDATE: el alta es el alta aunque la
// configuración cambie después. `consented_at` SÍ se pisa: el cuerpo re-afirma
// el consentimiento en cada PUT.
func (p *Postgres) Upsert(ctx context.Context, cfg Config, apiKey string, consentedAt time.Time) error {
	if apiKey == "" {
		// Guarda de programación, no validación de entrada: la API ya rechaza el
		// cuerpo sin clave con un 400. Si se llega aquí con la clave vacía, el
		// INSERT cifraría la cadena vacía y dejaría una fila con sobre de
		// no-valor — exactamente el estado que la 0071 declara imposible. Mejor
		// un error nombrado que una fila que miente.
		return fmt.Errorf("tenantllm: upsert de %s sin API key: la fila no puede existir sin credencial", cfg.TenantID)
	}
	enc, dek, kekID, err := p.cipher.Encrypt(apiKey)
	if err != nil {
		return fmt.Errorf("tenantllm: cifrar la API key de %s: %w", cfg.TenantID, err)
	}
	_, err = p.db.ExecContext(ctx, `
		INSERT INTO public.tenant_llm
			(tenant_id, provider, model, api_key_enc, api_key_dek, api_key_kek_id, consented_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, now())
		ON CONFLICT (tenant_id) DO UPDATE SET
			provider       = EXCLUDED.provider,
			model          = EXCLUDED.model,
			api_key_enc    = EXCLUDED.api_key_enc,
			api_key_dek    = EXCLUDED.api_key_dek,
			api_key_kek_id = EXCLUDED.api_key_kek_id,
			consented_at   = EXCLUDED.consented_at,
			updated_at     = now()
	`, cfg.TenantID, cfg.Provider, cfg.Model, enc, dek, kekID, consentedAt.UTC())
	if err != nil {
		return fmt.Errorf("tenantllm: upsert de %s: %w", cfg.TenantID, err)
	}
	return nil
}

// Delete implementa Store.Delete: borra la fila entera y con ella la credencial
// cifrada y el consentimiento. Es la ÚNICA forma de retirar la clave (el PUT
// nunca la borra: siempre la reemplaza por otra).
func (p *Postgres) Delete(ctx context.Context, tenantID string) error {
	if _, err := p.db.ExecContext(ctx, `
		DELETE FROM public.tenant_llm WHERE tenant_id = $1
	`, tenantID); err != nil {
		return fmt.Errorf("tenantllm: borrar configuración de %s: %w", tenantID, err)
	}
	return nil
}

// APIKey implementa Store.APIKey: descifra con la KEK QUE ENVOLVIÓ ESTA FILA
// (api_key_kek_id), no la current — tras una rotación parcial del Plan 012
// coexisten filas envueltas por distintas KEK, igual que intake_buyer_data
// (buyerdata.go) y tenant_integrations (integrations/postgres.go:241-243).
func (p *Postgres) APIKey(ctx context.Context, tenantID string) (string, error) {
	var enc, dek []byte
	var kekID string
	err := p.db.QueryRowContext(ctx, `
		SELECT api_key_enc, api_key_dek, api_key_kek_id
		FROM public.tenant_llm
		WHERE tenant_id = $1
	`, tenantID).Scan(&enc, &dek, &kekID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotConfigured
	}
	if err != nil {
		return "", fmt.Errorf("tenantllm: leer la API key de %s: %w", tenantID, err)
	}
	plain, err := p.cipher.Decrypt(enc, dek, kekID)
	if err != nil {
		// El error del descifrado se envuelve SIN el valor y sin el blob: un
		// fallo de KEK no es motivo para volcar material cifrado a un log.
		return "", fmt.Errorf("tenantllm: descifrar la API key de %s: %w", tenantID, err)
	}
	return plain, nil
}
