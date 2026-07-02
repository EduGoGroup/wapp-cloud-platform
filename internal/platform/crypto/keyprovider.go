// Package crypto es el fundamento criptográfico de la Plataforma Cloud para
// cifrar PII en reposo (Plan 011, ADR-0017): envelope encryption por-valor con
// una KEK gestionada e índice ciego (HMAC) para poder deduplicar y buscar
// valores cifrados sin exponerlos.
//
// Desde el Plan 012 la KEK deja de ser única: el KeyProvider custodia un
// KEYRING versionado (key_id → KEK) con una KEK current (la que envuelve) y las
// KEK retiradas (solo para desenvolver). Esto permite ROTAR la KEK re-envolviendo
// las DEK (WrapDEK con la current) sin re-cifrar el dato ni tocar el índice ciego.
//
// Piezas:
//   - KeyProvider: custodia el keyring (key_id → KEK) y la indexKey, separados
//     del dato (v1 = env/secret store; KMS-ready por estar detrás de la interfaz).
//   - FieldCipher: cifra/descifra un campo reusando wapp-shared/envelope
//     (AES-256-GCM), con una DEK fresca por valor envuelta por la KEK current.
//
// La KEK es distinta y SEPARADA de la DEK del Edge (ADR-0007, zero-knowledge):
// esta KEK es de la nube, recuperable y rotable, y solo protege PII de negocio.
// El key_id de la KEK NO viaja al Edge (§10.I).
package crypto

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"

	"github.com/EduGoGroup/wapp-shared/envelope"
	"golang.org/x/crypto/hkdf"
)

// blindIndexInfo es el contexto fijo de derivación HKDF de la indexKey a partir
// de la KEK current cuando no se provee una indexKey explícita (solo dev, §10.C).
// Cambiarlo invalidaría todos los índices ciegos ya calculados.
const blindIndexInfo = "wapp-blind-index-v1"

// indexKeySize es el tamaño en bytes de la indexKey del índice ciego (HMAC-SHA256).
const indexKeySize = 32

// compatKeyID es el key_id asignado a la KEK cargada por el camino de
// compatibilidad (solo WAPP_KEK_MASTER_B64, sin keyring): idéntico al Plan 011.
// El backfill de la migración del Plan 012 usa este mismo valor.
const compatKeyID = "1"

// ErrMasterKeySize indica que la KEK maestra (camino compat) no mide envelope.DEKSize bytes.
var ErrMasterKeySize = fmt.Errorf("la KEK maestra debe medir exactamente %d bytes (AES-256)", envelope.DEKSize)

// ErrKEKSize indica que una KEK del keyring no mide envelope.DEKSize bytes.
var ErrKEKSize = fmt.Errorf("cada KEK del keyring debe medir exactamente %d bytes (AES-256)", envelope.DEKSize)

// ErrMasterKeyMissing indica que no se proporcionó material de clave: ni keyring
// (WAPP_KEK_KEYRING) ni KEK maestra compat (WAPP_KEK_MASTER_B64).
var ErrMasterKeyMissing = errors.New("falta material de KEK: define WAPP_KEK_KEYRING+WAPP_KEK_CURRENT o WAPP_KEK_MASTER_B64")

// ErrCurrentKeyMissing indica que el key_id current está vacío o no existe en el keyring.
var ErrCurrentKeyMissing = errors.New("el key_id current (WAPP_KEK_CURRENT) debe existir en el keyring")

// ErrKEKNotInKeyring indica que se pidió desenvolver con un key_id ausente del
// keyring (fail-safe §10.J: error claro, nunca corrupción ni panic).
var ErrKEKNotInKeyring = errors.New("la KEK referenciada no está en el keyring")

// ErrIndexKeySize indica que la indexKey explícita decodificada no mide indexKeySize bytes.
var ErrIndexKeySize = fmt.Errorf("la indexKey debe medir exactamente %d bytes", indexKeySize)

// ErrIndexKeyRequiredInProd indica que en producción NO se permite derivar la
// indexKey de la KEK: debe proveerse explícita (WAPP_KEK_INDEX_B64) y estable de
// por vida (§10.C). Derivarla de la KEK acoplaría el índice ciego a la rotación.
var ErrIndexKeyRequiredInProd = errors.New("en producción WAPP_KEK_INDEX_B64 es obligatoria (indexKey explícita y estable); no se deriva de la KEK")

// KeyProvider custodia el keyring versionado (key_id → KEK) y la indexKey,
// separados del dato de negocio. Envuelve/desenvuelve DEKs por-valor con la KEK
// seleccionada por key_id y calcula el índice ciego.
//
// La v1 (envKeyProvider) lee las claves de entorno; la interfaz permite
// sustituir por GCP KMS / Vault sin tocar FieldCipher ni los repos (KMS-ready).
type KeyProvider interface {
	// WrapDEK envuelve una DEK con la KEK current y devuelve su key_id, para que
	// el llamador lo persista (columna value_kek_id) y sepa desenvolverla luego.
	WrapDEK(dek []byte) (wrapped []byte, keyID string, err error)
	// UnwrapDEK desenvuelve una DEK seleccionando la KEK por key_id. Si el key_id
	// no está en el keyring devuelve error claro (fail-safe §10.J), no panic.
	UnwrapDEK(wrapped []byte, keyID string) (dek []byte, err error)
	// BlindIndex calcula HMAC-SHA256(indexKey, tenantID || 0x00 || value) en hex.
	// Determinista (dedup/lookup por igualdad), no invertible sin la indexKey.
	// Estable ante rotación de la KEK: la indexKey es independiente del keyring.
	BlindIndex(tenantID, value string) (bidx string)
	// CurrentKeyID devuelve el key_id de la KEK current (la que hoy envuelve),
	// para consultas y para la operación de rotación (re-wrap al current).
	CurrentKeyID() string
}

// envKeyProvider es la implementación v1 del KeyProvider: el keyring y la
// indexKey viven en variables de entorno (decodificadas en la construcción).
type envKeyProvider struct {
	keys      map[string][]byte // key_id → KEK (32B): current + retiradas.
	currentID string            // key_id de la KEK current (la que envuelve).
	indexKey  []byte            // clave HMAC del índice ciego (32B), INDEPENDIENTE del keyring (§10.C).
}

// KeyringConfig agrupa el material de clave para construir el KeyProvider v1.
// Precedencia: si KeyringB64 viene, se usa el keyring versionado (con CurrentID);
// si no, se cae al camino de compatibilidad con la KEK maestra única (MasterB64).
type KeyringConfig struct {
	// KeyringB64 es el keyring versionado: entradas "id:base64" separadas por
	// coma (WAPP_KEK_KEYRING). El base64 estándar no contiene ':' así que es
	// separador seguro. Vacío = usar el camino compat (MasterB64).
	KeyringB64 string
	// CurrentID es el key_id de la KEK current dentro del keyring (WAPP_KEK_CURRENT).
	// Obligatorio y debe existir en el keyring cuando KeyringB64 viene.
	CurrentID string
	// MasterB64 es la KEK maestra única del Plan 011 (WAPP_KEK_MASTER_B64). Solo
	// se usa como compat cuando no hay keyring: se carga como key_id compatKeyID.
	MasterB64 string
	// IndexB64 es la indexKey explícita del índice ciego (WAPP_KEK_INDEX_B64).
	// Obligatoria en prod (§10.C); vacía en dev deriva por HKDF con warning.
	IndexB64 string
	// Prod activa la política de producción: sin IndexB64 explícita, fail-fast.
	Prod bool
}

// NewEnvKeyProvider construye el KeyProvider v1 (keyring versionado) desde config.
//
// Modo keyring: KeyringB64 = "id1:base64,id2:base64,…" (cada KEK 32B) + CurrentID
// (debe existir en el keyring). Modo compat: solo MasterB64 → se carga como el
// key_id inicial (compatKeyID "1") y es la current (comportamiento idéntico al 011).
//
// indexKey: si IndexB64 viene, se usa tal cual (debe medir indexKeySize bytes).
// Si está vacía, en prod se falla-rápido (§10.C) y en dev se deriva por HKDF de la
// KEK current con warning visible.
func NewEnvKeyProvider(cfg KeyringConfig) (KeyProvider, error) {
	keys, currentID, err := buildKeyring(cfg)
	if err != nil {
		return nil, err
	}

	indexKey, err := resolveIndexKey(cfg, keys[currentID])
	if err != nil {
		return nil, err
	}

	return &envKeyProvider{keys: keys, currentID: currentID, indexKey: indexKey}, nil
}

// buildKeyring decodifica el material de clave en un mapa key_id → KEK y elige la
// current. Modo keyring (KeyringB64) o modo compat (MasterB64 → key_id "1").
func buildKeyring(cfg KeyringConfig) (map[string][]byte, string, error) {
	switch {
	case cfg.KeyringB64 != "":
		keys, err := parseKeyring(cfg.KeyringB64)
		if err != nil {
			return nil, "", err
		}
		current := strings.TrimSpace(cfg.CurrentID)
		if current == "" {
			return nil, "", ErrCurrentKeyMissing
		}
		if _, ok := keys[current]; !ok {
			return nil, "", fmt.Errorf("%w: %q no está en el keyring", ErrCurrentKeyMissing, current)
		}
		return keys, current, nil

	case cfg.MasterB64 != "":
		master, err := base64.StdEncoding.DecodeString(cfg.MasterB64)
		if err != nil {
			return nil, "", fmt.Errorf("KEK maestra: base64 inválido: %w", err)
		}
		if len(master) != envelope.DEKSize {
			return nil, "", ErrMasterKeySize
		}
		return map[string][]byte{compatKeyID: master}, compatKeyID, nil

	default:
		return nil, "", ErrMasterKeyMissing
	}
}

// parseKeyring decodifica "id1:base64,id2:base64,…" en un mapa key_id → KEK (32B).
// Cada entrada se parte por el PRIMER ':' (el base64 estándar no lo contiene).
func parseKeyring(spec string) (map[string][]byte, error) {
	keys := make(map[string][]byte)
	for entry := range strings.SplitSeq(spec, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		i := strings.IndexByte(entry, ':')
		if i <= 0 {
			return nil, fmt.Errorf("entrada de keyring inválida %q: se espera id:base64", entry)
		}
		id := strings.TrimSpace(entry[:i])
		b64 := strings.TrimSpace(entry[i+1:])
		if id == "" {
			return nil, fmt.Errorf("entrada de keyring con key_id vacío: %q", entry)
		}
		if _, dup := keys[id]; dup {
			return nil, fmt.Errorf("key_id duplicado en el keyring: %q", id)
		}
		kek, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("KEK %q: base64 inválido: %w", id, err)
		}
		if len(kek) != envelope.DEKSize {
			return nil, fmt.Errorf("%w (key_id %q)", ErrKEKSize, id)
		}
		keys[id] = kek
	}
	if len(keys) == 0 {
		return nil, errors.New("keyring vacío (WAPP_KEK_KEYRING sin entradas válidas)")
	}
	return keys, nil
}

// resolveIndexKey obtiene la indexKey del índice ciego. Explícita (IndexB64) tiene
// prioridad y es la única aceptada en prod (§10.C). En dev, sin explícita, deriva
// por HKDF de la KEK current con warning visible. La indexKey NO se acopla al
// keyring: rotar la KEK NO cambia BlindIndex (con indexKey explícita, estable).
func resolveIndexKey(cfg KeyringConfig, currentKEK []byte) ([]byte, error) {
	if cfg.IndexB64 != "" {
		indexKey, err := base64.StdEncoding.DecodeString(cfg.IndexB64)
		if err != nil {
			return nil, fmt.Errorf("indexKey: base64 inválido: %w", err)
		}
		if len(indexKey) != indexKeySize {
			return nil, ErrIndexKeySize
		}
		return indexKey, nil
	}

	if cfg.Prod {
		return nil, ErrIndexKeyRequiredInProd
	}

	log.Printf("[wapp][crypto][WARN] WAPP_KEK_INDEX_B64 no provista: derivando la indexKey de la KEK por HKDF (SOLO dev). " +
		"En prod es OBLIGATORIA, explícita y estable de por vida (cambiarla = reindexar value_bidx).")
	return deriveIndexKey(currentKEK)
}

// deriveIndexKey deriva la indexKey (indexKeySize bytes) de una KEK vía
// HKDF-SHA256 con el contexto fijo blindIndexInfo (sin salt: la KEK ya es
// material de alta entropía). Solo se usa en dev (§10.C).
func deriveIndexKey(kek []byte) ([]byte, error) {
	r := hkdf.New(sha256.New, kek, nil, []byte(blindIndexInfo))
	key := make([]byte, indexKeySize)
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, err
	}
	return key, nil
}

// WrapDEK envuelve una DEK con la KEK current (envelope AES-256-GCM) y devuelve su
// key_id, para que el llamador lo persista y sepa desenvolverla más tarde.
func (p *envKeyProvider) WrapDEK(dek []byte) ([]byte, string, error) {
	env, err := envelope.NewEnvelope(p.keys[p.currentID])
	if err != nil {
		return nil, "", fmt.Errorf("envelope con la KEK current %q: %w", p.currentID, err)
	}
	wrapped, err := env.Seal(dek)
	if err != nil {
		return nil, "", fmt.Errorf("WrapDEK con la KEK current %q: %w", p.currentID, err)
	}
	return wrapped, p.currentID, nil
}

// UnwrapDEK desenvuelve una DEK seleccionando la KEK por key_id. Si el key_id no
// está en el keyring devuelve error claro (fail-safe §10.J), nunca panic ni
// corrupción. Si la KEK es incorrecta o el blob fue manipulado, GCM falla el tag.
func (p *envKeyProvider) UnwrapDEK(wrapped []byte, keyID string) ([]byte, error) {
	kek, ok := p.keys[keyID]
	if !ok {
		return nil, fmt.Errorf("%w: key_id %q", ErrKEKNotInKeyring, keyID)
	}
	env, err := envelope.NewEnvelope(kek)
	if err != nil {
		return nil, fmt.Errorf("envelope con la KEK %q: %w", keyID, err)
	}
	return env.Open(wrapped)
}

// CurrentKeyID devuelve el key_id de la KEK current (la que hoy envuelve).
func (p *envKeyProvider) CurrentKeyID() string { return p.currentID }

// BlindIndex calcula el índice ciego determinista del valor: hex de
// HMAC-SHA256(indexKey, tenantID || 0x00 || value). El separador 0x00 evita
// ambigüedades entre tenantID y value (p. ej. "a"+"bc" vs "ab"+"c"). Es estable
// ante rotación de la KEK porque la indexKey es independiente del keyring (§10.C).
func (p *envKeyProvider) BlindIndex(tenantID, value string) string {
	mac := hmac.New(sha256.New, p.indexKey)
	mac.Write([]byte(tenantID))
	mac.Write([]byte{0x00})
	mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}
