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

// Encrypt cifra plaintext y devuelve (valueEnc, valueDEK):
//   - valueEnc: nonce||ciphertext||tag del plaintext bajo una DEK fresca.
//   - valueDEK: esa DEK envuelta por la KEK del KeyProvider.
//
// Un plaintext vacío se cifra igual (el envelope no distingue nil/vacío; esa
// semántica se delega al consumidor).
func (c *FieldCipher) Encrypt(plaintext string) (valueEnc, valueDEK []byte, err error) {
	dek := make([]byte, envelope.DEKSize)
	if _, err = io.ReadFull(rand.Reader, dek); err != nil {
		return nil, nil, fmt.Errorf("no se pudo generar la DEK: %w", err)
	}

	env, err := envelope.NewEnvelope(dek)
	if err != nil {
		return nil, nil, fmt.Errorf("envelope con la DEK por-valor: %w", err)
	}

	valueEnc, err = env.Seal([]byte(plaintext))
	if err != nil {
		return nil, nil, fmt.Errorf("sellado del valor: %w", err)
	}

	valueDEK, err = c.kp.WrapDEK(dek)
	if err != nil {
		return nil, nil, fmt.Errorf("WrapDEK de la DEK por-valor: %w", err)
	}

	return valueEnc, valueDEK, nil
}

// Decrypt es el inverso de Encrypt: desenvuelve la DEK con la KEK y descifra
// valueEnc. Devuelve error si la DEK envuelta o el blob cifrado fueron
// manipulados (GCM verifica el tag de autenticidad).
func (c *FieldCipher) Decrypt(valueEnc, valueDEK []byte) (string, error) {
	dek, err := c.kp.UnwrapDEK(valueDEK)
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
