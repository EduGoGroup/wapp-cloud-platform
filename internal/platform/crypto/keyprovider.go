// Package crypto es el fundamento criptográfico de la Plataforma Cloud para
// cifrar PII en reposo (Plan 011, ADR-0017): envelope encryption por-valor con
// una KEK maestra gestionada e índice ciego (HMAC) para poder deduplicar y
// buscar valores cifrados sin exponerlos.
//
// Piezas:
//   - KeyProvider: custodia la KEK maestra y la indexKey, separadas del dato
//     (v1 = env/secret store; KMS-ready por estar detrás de la interfaz).
//   - FieldCipher: cifra/descifra un campo reusando wapp-shared/envelope
//     (AES-256-GCM), con una DEK fresca por valor envuelta por la KEK.
//
// La KEK es distinta y SEPARADA de la DEK del Edge (ADR-0007, zero-knowledge):
// esta KEK es de la nube, recuperable y rotable, y solo protege PII de negocio.
package crypto

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/EduGoGroup/wapp-shared/envelope"
	"golang.org/x/crypto/hkdf"
)

// blindIndexInfo es el contexto fijo de derivación HKDF de la indexKey a partir
// de la KEK maestra cuando no se provee una indexKey explícita. Cambiarlo
// invalidaría todos los índices ciegos ya calculados.
const blindIndexInfo = "wapp-blind-index-v1"

// indexKeySize es el tamaño en bytes de la indexKey del índice ciego (HMAC-SHA256).
const indexKeySize = 32

// ErrMasterKeySize indica que la KEK maestra decodificada no mide envelope.DEKSize bytes.
var ErrMasterKeySize = fmt.Errorf("la KEK maestra debe medir exactamente %d bytes (AES-256)", envelope.DEKSize)

// ErrMasterKeyMissing indica que no se proporcionó la KEK maestra (base64 vacío).
var ErrMasterKeyMissing = errors.New("la KEK maestra es obligatoria (WAPP_KEK_MASTER_B64 vacío)")

// ErrIndexKeySize indica que la indexKey explícita decodificada no mide indexKeySize bytes.
var ErrIndexKeySize = fmt.Errorf("la indexKey debe medir exactamente %d bytes", indexKeySize)

// KeyProvider custodia la KEK maestra y la indexKey, separadas del dato de
// negocio. Envuelve/desenvuelve DEKs por-valor y calcula el índice ciego.
//
// La v1 (envKeyProvider) lee las claves de entorno; la interfaz permite
// sustituir por GCP KMS / Vault sin tocar FieldCipher ni los repos.
type KeyProvider interface {
	// WrapDEK envuelve una DEK con la KEK maestra (AES-256-GCM sobre la KEK).
	WrapDEK(dek []byte) (wrapped []byte, err error)
	// UnwrapDEK desenvuelve una DEK previamente envuelta con WrapDEK.
	UnwrapDEK(wrapped []byte) (dek []byte, err error)
	// BlindIndex calcula HMAC-SHA256(indexKey, tenantID || 0x00 || value) en hex.
	// Determinista (dedup/lookup por igualdad), no invertible sin la indexKey.
	BlindIndex(tenantID, value string) (bidx string)
}

// envKeyProvider es la implementación v1 del KeyProvider: la KEK maestra y la
// indexKey viven en variables de entorno (decodificadas en la construcción).
type envKeyProvider struct {
	master   []byte // KEK maestra (32B) para envolver/desenvolver DEKs.
	indexKey []byte // clave HMAC del índice ciego (32B).
}

// NewEnvKeyProvider construye el KeyProvider v1 a partir de las claves en base64
// estándar.
//
// masterB64 es obligatorio y debe decodificar a exactamente envelope.DEKSize
// bytes (falla-rápido con error claro si falta o el tamaño es incorrecto).
//
// indexB64 es opcional: si viene, debe decodificar a indexKeySize bytes y se usa
// tal cual; si está vacío, la indexKey se deriva de la KEK maestra vía
// HKDF-SHA256 con el contexto fijo blindIndexInfo.
func NewEnvKeyProvider(masterB64, indexB64 string) (KeyProvider, error) {
	if masterB64 == "" {
		return nil, ErrMasterKeyMissing
	}
	master, err := base64.StdEncoding.DecodeString(masterB64)
	if err != nil {
		return nil, fmt.Errorf("KEK maestra: base64 inválido: %w", err)
	}
	if len(master) != envelope.DEKSize {
		return nil, ErrMasterKeySize
	}

	var indexKey []byte
	if indexB64 == "" {
		indexKey, err = deriveIndexKey(master)
		if err != nil {
			return nil, fmt.Errorf("derivación HKDF de la indexKey: %w", err)
		}
	} else {
		indexKey, err = base64.StdEncoding.DecodeString(indexB64)
		if err != nil {
			return nil, fmt.Errorf("indexKey: base64 inválido: %w", err)
		}
		if len(indexKey) != indexKeySize {
			return nil, ErrIndexKeySize
		}
	}

	return &envKeyProvider{master: master, indexKey: indexKey}, nil
}

// deriveIndexKey deriva la indexKey (indexKeySize bytes) de la KEK maestra vía
// HKDF-SHA256 con el contexto fijo blindIndexInfo (sin salt: la KEK ya es
// material de alta entropía).
func deriveIndexKey(master []byte) ([]byte, error) {
	r := hkdf.New(sha256.New, master, nil, []byte(blindIndexInfo))
	key := make([]byte, indexKeySize)
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, err
	}
	return key, nil
}

// WrapDEK envuelve una DEK con la KEK maestra usando el envelope AES-256-GCM.
func (p *envKeyProvider) WrapDEK(dek []byte) ([]byte, error) {
	env, err := envelope.NewEnvelope(p.master)
	if err != nil {
		return nil, fmt.Errorf("envelope con la KEK maestra: %w", err)
	}
	return env.Seal(dek)
}

// UnwrapDEK desenvuelve una DEK envuelta por WrapDEK. Si la KEK es incorrecta o
// el blob fue manipulado, GCM falla en la verificación del tag y se devuelve error.
func (p *envKeyProvider) UnwrapDEK(wrapped []byte) ([]byte, error) {
	env, err := envelope.NewEnvelope(p.master)
	if err != nil {
		return nil, fmt.Errorf("envelope con la KEK maestra: %w", err)
	}
	return env.Open(wrapped)
}

// BlindIndex calcula el índice ciego determinista del valor: hex de
// HMAC-SHA256(indexKey, tenantID || 0x00 || value). El separador 0x00 evita
// ambigüedades entre tenantID y value (p. ej. "a"+"bc" vs "ab"+"c").
func (p *envKeyProvider) BlindIndex(tenantID, value string) string {
	mac := hmac.New(sha256.New, p.indexKey)
	mac.Write([]byte(tenantID))
	mac.Write([]byte{0x00})
	mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}
