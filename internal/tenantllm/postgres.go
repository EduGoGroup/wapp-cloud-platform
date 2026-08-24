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
// pregunta es si está configurada.
//
// 🔧 EL DÍA QUE SE AFLOJÓ EL NOT NULL LLEGÓ (0073, T1.5-2), y el comentario que
// esta función tenía lo había previsto: `api_key_enc IS NOT NULL` ya no es
// siempre cierto sobre una fila que existe —las filas de la vía local no tienen
// sobre— y por eso NO se sustituyó nunca por un `true` literal. La expresión
// sigue diciendo la verdad sin tocarla; lo que sí cambia es que tres columnas
// pueden venir NULL y hay que escanearlas como tales: un Scan de NULL sobre
// `string`/`time.Time` es un error de driver, no un cero.
func (p *Postgres) Get(ctx context.Context, tenantID string) (Config, bool, error) {
	var cfg Config
	var provider, model sql.NullString
	var consentedAt sql.NullTime
	err := p.db.QueryRowContext(ctx, `
		SELECT tenant_id, via, provider, model, api_key_enc IS NOT NULL, consented_at, created_at, updated_at
		FROM public.tenant_llm
		WHERE tenant_id = $1
	`, tenantID).Scan(&cfg.TenantID, &cfg.Via, &provider, &model, &cfg.HasAPIKey,
		&consentedAt, &cfg.CreatedAt, &cfg.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Config{}, false, nil
	}
	if err != nil {
		return Config{}, false, fmt.Errorf("tenantllm: leer configuración de %s: %w", tenantID, err)
	}
	// El valor cero es la traducción correcta del NULL en las tres: son las
	// columnas que la vía local no tiene, y quien las lea ya sabe por `Via` si
	// tienen sentido. `via` NO se escanea como NullString a propósito — es NOT
	// NULL en la 0073, y si algún día llegara NULL, este Scan tiene que fallar
	// ruidosamente en vez de devolver una vía vacía que nadie sabría interpretar.
	cfg.Provider = provider.String
	cfg.Model = model.String
	cfg.ConsentedAt = consentedAt.Time
	return cfg, true, nil
}

// Upsert implementa Store.Upsert.
//
// El upsert reemplaza las SIETE columnas de negocio —la vía y las seis del eje
// `api`, sobre incluido—: cada PUT es la foto entera. No existe el camino
// «actualiza el modelo sin tocar la clave» que sí tiene
// integrations.UpsertTenantIntegration, y es deliberado (ver el comentario del
// puerto y el de la migración 0071).
//
// 🔴 LA VÍA DECIDE QUÉ SE ESCRIBE, y ésa es toda la novedad de T1.5-2:
//
//   - ViaAPI   ⇒ las seis columnas del eje `api` se rellenan. Es el camino de
//     antes, intacto.
//   - ViaLocal ⇒ las seis viajan como NULL. La fila queda diciendo «este tenant
//     usa su propio fierro», sin proveedor al que llamar, sin sobre que
//     descifrar y sin consentimiento que fingir.
//
// Los NULL van como `any(nil)` y no como `[]byte(nil)` o `""`: un `nil` sin tipo
// es NULL sin ambigüedad para cualquier driver, y una cadena vacía en `provider`
// reventaría contra el CHECK del vocabulario —que es exactamente el fallo que
// esta forma evita, y no uno que quisiéramos descubrir en producción—.
//
// `created_at` NO se pisa en el DO UPDATE: el alta es el alta aunque la
// configuración cambie después. `consented_at` SÍ se pisa: el cuerpo re-afirma
// el consentimiento en cada PUT de la vía API, y se BORRA al pasar a la local
// (el permiso muere con la vía que lo usaba).
func (p *Postgres) Upsert(ctx context.Context, cfg Config, apiKey string, consentedAt time.Time) error {
	if !ValidVia(cfg.Via) {
		// Guarda de programación: la API valida el vocabulario antes (400
		// invalid_via). Si se llega aquí con una vía vacía o inventada, dejar que
		// lo rechace el CHECK convertiría un error del cliente en un 500, y una
		// vía vacía escrita por descuido sería una fila que nadie sabe leer.
		return fmt.Errorf("tenantllm: upsert de %s con vía %q: fuera del vocabulario (%s|%s)",
			cfg.TenantID, cfg.Via, ViaLocal, ViaAPI)
	}

	// Los seis valores del eje `api`. Nacen nil —la forma de la vía local— y solo
	// se rellenan si la vía es `api`.
	var provider, model, enc, dek, kekID, consent any

	if cfg.Via == ViaAPI {
		if apiKey == "" {
			// La API ya rechaza el cuerpo sin clave con un 400. Si se llega aquí
			// con la clave vacía, el INSERT cifraría la cadena vacía y dejaría una
			// fila con sobre de no-valor — exactamente el estado que la 0071
			// declara imposible y que la 0073 sigue prohibiendo para esta vía
			// (tenant_llm_via_api_completa_check). Mejor un error nombrado que una
			// fila que miente.
			return fmt.Errorf("tenantllm: upsert de %s en vía %s sin API key: esa vía no existe sin credencial",
				cfg.TenantID, ViaAPI)
		}
		if consentedAt.IsZero() {
			// El consentimiento no es opcional en la vía que manda texto del
			// cliente a un tercero (ADR-0030, REQ-05). Un cero aquí escribiría el
			// año 1 en la columna, que pasaría el NOT NULL del CHECK y sería una
			// mentira con fecha; se para antes.
			return fmt.Errorf("tenantllm: upsert de %s en vía %s sin consentimiento: la fila no puede existir sin él",
				cfg.TenantID, ViaAPI)
		}
		encBytes, dekBytes, keyID, err := p.cipher.Encrypt(apiKey)
		if err != nil {
			return fmt.Errorf("tenantllm: cifrar la API key de %s: %w", cfg.TenantID, err)
		}
		provider, model = cfg.Provider, cfg.Model
		enc, dek, kekID = encBytes, dekBytes, keyID
		consent = consentedAt.UTC()
	}

	if _, err := p.db.ExecContext(ctx, `
		INSERT INTO public.tenant_llm
			(tenant_id, via, provider, model, api_key_enc, api_key_dek, api_key_kek_id, consented_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())
		ON CONFLICT (tenant_id) DO UPDATE SET
			via            = EXCLUDED.via,
			provider       = EXCLUDED.provider,
			model          = EXCLUDED.model,
			api_key_enc    = EXCLUDED.api_key_enc,
			api_key_dek    = EXCLUDED.api_key_dek,
			api_key_kek_id = EXCLUDED.api_key_kek_id,
			consented_at   = EXCLUDED.consented_at,
			updated_at     = now()
	`, cfg.TenantID, cfg.Via, provider, model, enc, dek, kekID, consent); err != nil {
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
//
// 🔴 SE LEE `via` Y SE COMPRUEBA, aunque el sobre esté ahí. Cinturón y tirantes
// con `tenant_llm_local_sin_credencial_check` (0073 · f.4): el CHECK impide que
// la fila EXISTA, y esto impide que el código la USE si alguna vez existiera —un
// restore parcial, una edición a mano, una base que se quedó sin la constraint.
// Devolver la credencial de una fila `via='local'` sería servir una clave de un
// tercero bajo la vía que declara no llamar a nadie: REQ-33 dice que mientras la
// vía sea una, la otra NO se usa JAMÁS, y «no se usa» tiene que ser cierto también
// para el camino que la descifra. El desenlace es ErrNotConfigured, el mismo que
// «no hay fila»: para el llamante, un tenant local NO TIENE credencial que pedir.
func (p *Postgres) APIKey(ctx context.Context, tenantID string) (string, error) {
	var enc, dek []byte
	var kekID sql.NullString
	var via string
	err := p.db.QueryRowContext(ctx, `
		SELECT via, api_key_enc, api_key_dek, api_key_kek_id
		FROM public.tenant_llm
		WHERE tenant_id = $1
	`, tenantID).Scan(&via, &enc, &dek, &kekID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotConfigured
	}
	if err != nil {
		return "", fmt.Errorf("tenantllm: leer la API key de %s: %w", tenantID, err)
	}
	// FILA SIN SOBRE = tenant en la vía local (0073). Es ErrNotConfigured, el
	// MISMO desenlace que «no hay fila», y a propósito: el llamante responde 422
	// llm_credentials_missing en los dos casos, así que darle dos errores
	// distintos le daría una rama que no sabría qué hacer. Se comprueba por
	// `kekID` y no por `enc` porque el CHECK del sobre garantiza que las tres van
	// juntas, y ésta es la que Decrypt necesita para elegir la KEK: sin ella no
	// hay descifrado posible ni con el blob delante.
	if !kekID.Valid {
		return "", ErrNotConfigured
	}
	// LA GUARDA DE LA VÍA, después del sobre y antes del descifrado. Va aquí y no
	// en el WHERE a propósito: un `AND via = 'api'` en el SELECT devolvería
	// ErrNoRows y no se distinguiría de «no hay fila» al depurar; así el caso
	// queda en una línea que se puede leer, y el error que sale sigue siendo el
	// mismo para quien llama.
	if via != ViaAPI {
		return "", ErrNotConfigured
	}
	plain, err := p.cipher.Decrypt(enc, dek, kekID.String)
	if err != nil {
		// El error del descifrado se envuelve SIN el valor y sin el blob: un
		// fallo de KEK no es motivo para volcar material cifrado a un log.
		return "", fmt.Errorf("tenantllm: descifrar la API key de %s: %w", tenantID, err)
	}
	return plain, nil
}
