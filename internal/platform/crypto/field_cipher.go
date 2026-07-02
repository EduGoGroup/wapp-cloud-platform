package crypto

import (
	"crypto/rand"
	"fmt"
	"io"

	"github.com/EduGoGroup/wapp-shared/envelope"
)

// FieldCipher aplica envelope encryption a un campo de texto (p. ej. el
// identificador de un contacto) reusando wapp-shared/envelope.
//
// Cada Encrypt genera una DEK fresca (por-valor, §10.B del diseño): el valor se
// cifra con la DEK (AES-256-GCM, nonce fresco) y la DEK se envuelve con la KEK
// del KeyProvider. La fila guarda ambos: value_enc (dato cifrado) y value_dek
// (DEK envuelta).
type FieldCipher struct {
	kp KeyProvider
}

// NewFieldCipher construye un FieldCipher que usa kp para envolver/desenvolver
// las DEKs por-valor.
func NewFieldCipher(kp KeyProvider) *FieldCipher {
	return &FieldCipher{kp: kp}
}

// Encrypt cifra plaintext y devuelve (valueEnc, valueDEK, keyID):
//   - valueEnc: nonce||ciphertext||tag del plaintext bajo una DEK fresca.
//   - valueDEK: esa DEK envuelta por la KEK current del KeyProvider.
//   - keyID: el key_id de la KEK current que envolvió la DEK (se persiste en
//     value_kek_id para saber con qué KEK desenvolver y para la rotación).
//
// Un plaintext vacío se cifra igual (el envelope no distingue nil/vacío; esa
// semántica se delega al consumidor).
func (c *FieldCipher) Encrypt(plaintext string) (valueEnc, valueDEK []byte, keyID string, err error) {
	dek := make([]byte, envelope.DEKSize)
	if _, err = io.ReadFull(rand.Reader, dek); err != nil {
		return nil, nil, "", fmt.Errorf("no se pudo generar la DEK: %w", err)
	}

	env, err := envelope.NewEnvelope(dek)
	if err != nil {
		return nil, nil, "", fmt.Errorf("envelope con la DEK por-valor: %w", err)
	}

	valueEnc, err = env.Seal([]byte(plaintext))
	if err != nil {
		return nil, nil, "", fmt.Errorf("sellado del valor: %w", err)
	}

	valueDEK, keyID, err = c.kp.WrapDEK(dek)
	if err != nil {
		return nil, nil, "", fmt.Errorf("WrapDEK de la DEK por-valor: %w", err)
	}

	return valueEnc, valueDEK, keyID, nil
}

// Decrypt es el inverso de Encrypt: desenvuelve la DEK con la KEK identificada por
// keyID (el leído de value_kek_id) y descifra valueEnc. Devuelve error si el key_id
// no está en el keyring (fail-safe §10.J) o si la DEK envuelta o el blob cifrado
// fueron manipulados (GCM verifica el tag de autenticidad).
func (c *FieldCipher) Decrypt(valueEnc, valueDEK []byte, keyID string) (string, error) {
	dek, err := c.kp.UnwrapDEK(valueDEK, keyID)
	if err != nil {
		return "", fmt.Errorf("UnwrapDEK: %w", err)
	}

	env, err := envelope.NewEnvelope(dek)
	if err != nil {
		return "", fmt.Errorf("envelope con la DEK desenvuelta: %w", err)
	}

	pt, err := env.Open(valueEnc)
	if err != nil {
		return "", fmt.Errorf("apertura del valor: %w", err)
	}

	return string(pt), nil
}

// ReWrap re-envuelve una DEK ya envuelta (valueDEK, envuelta por la KEK oldKeyID)
// hacia la KEK current, SIN tocar value_enc. Es el núcleo de la rotación de KEK
// (§7): desenvuelve la DEK con la KEK vieja y la vuelve a envolver con la current,
// devolviendo la nueva DEK envuelta y el nuevo key_id. El value NUNCA se descifra;
// la DEK solo vive en memoria durante el re-wrap. Devuelve error claro si la KEK
// oldKeyID no está en el keyring (fail-safe §10.J), sin dejar estado a medias.
func (c *FieldCipher) ReWrap(valueDEK []byte, oldKeyID string) (newValueDEK []byte, newKeyID string, err error) {
	dek, err := c.kp.UnwrapDEK(valueDEK, oldKeyID)
	if err != nil {
		return nil, "", fmt.Errorf("ReWrap: UnwrapDEK con key_id %q: %w", oldKeyID, err)
	}

	newValueDEK, newKeyID, err = c.kp.WrapDEK(dek)
	if err != nil {
		return nil, "", fmt.Errorf("ReWrap: WrapDEK con la KEK current: %w", err)
	}

	return newValueDEK, newKeyID, nil
}
