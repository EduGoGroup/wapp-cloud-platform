//go:build laboratorio

package common

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// NewCipher devuelve el cifrador REAL de la PII de negocio (envelope por valor: DEK fresca por
// campo, envuelta con la KEK) con una KEK y una llave de índice ciego ALEATORIAS de 32 bytes que viven
// solo en memoria y mueren con el proceso. Doblarlo costaría más que usarlo (design.md §5).
//
// 🔴 Homónimo: la «DEK» de este cifrador es la del envelope de PII de negocio, NO la DEK del ADR-0007
// que descifra el almacén de whatsmeow. Esa no la ve la nube nunca, tampoco en el laboratorio.
//
// Las llaves no se imprimen ni se devuelven: un escalón que necesite descifrar usa el mismo
// *FieldCipher.
func NewCipher() (*crypto.FieldCipher, crypto.KeyProvider, error) {
	kek, err := random32()
	if err != nil {
		return nil, nil, err
	}
	index, err := random32()
	if err != nil {
		return nil, nil, err
	}
	kp, err := crypto.NewEnvKeyProvider(crypto.KeyringConfig{MasterB64: kek, IndexB64: index})
	if err != nil {
		return nil, nil, fmt.Errorf("common: llavero en memoria: %w", err)
	}
	return crypto.NewFieldCipher(kp), kp, nil
}

// random32 devuelve 32 bytes aleatorios en base64 estándar.
func random32() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("common: leyendo azar: %w", err)
	}
	return base64.StdEncoding.EncodeToString(b), nil
}
