package events

// store_append_cipher_failure_test.go: las tres puertas de NIVEL 2 cuando CIFRAR falla. El orden
// de las guardas que promete store_append.go acaba en «cifrar», y ninguna llega a la base si
// falla. El fallo lo pone un KeyProvider de mentira que no envuelve la DEK por valor.

import (
	"errors"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// errWrapDown es el fallo del KeyProvider al envolver la DEK por valor.
var errWrapDown = errors.New("kek fuera de servicio-zzq")

// brokenKeyProvider es un KeyProvider cuya KEK no envuelve: todo Encrypt del FieldCipher que lo
// use falla con errWrapDown. Es la KEK del envelope de PII de negocio, NO la DEK del ADR-0007.
type brokenKeyProvider struct{}

func (brokenKeyProvider) WrapDEK([]byte) ([]byte, string, error) { return nil, "", errWrapDown }
func (brokenKeyProvider) UnwrapDEK([]byte, string) ([]byte, error) {
	return nil, errWrapDown
}
func (brokenKeyProvider) BlindIndex(string, string) string { return "" }
func (brokenKeyProvider) CurrentKeyID() string             { return "kek-rota" }

// TestAppendLevelTwo_EncryptFailure_NeverReachesTheDatabase: si el cipher no puede sellar el
// cuerpo, cada puerta devuelve (0, su texto literal envolviendo la causa) y a la base no llega
// ninguna sentencia. Ningún texto cita el cuerpo, que es texto del cliente (ADR-0034).
func TestAppendLevelTwo_EncryptFailure_NeverReachesTheDatabase(t *testing.T) {
	cases := []struct {
		name   string
		prefix string
		call   func(s *Store) (int, error)
	}{
		{"AppendMessage", "events: cifrar el cuerpo de la entrada: ", func(s *Store) (int, error) {
			return s.AppendMessage(t.Context(), pgEvent, RoleClient, testClientText)
		}},
		{"AppendOutOfTurnMessage", "events: cifrar el cuerpo del saliente fuera de turno: ", func(s *Store) (int, error) {
			return s.AppendOutOfTurnMessage(t.Context(), pgEvent, testClientText)
		}},
		{"AppendPastedMessage", "events: cifrar la transcripción pegada por el dueño: ", func(s *Store) (int, error) {
			return s.AppendPastedMessage(t.Context(), pgEvent, testClientText)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newFakeStoreWith(t, crypto.NewFieldCipher(brokenKeyProvider{}), pgOne(int64(7)))

			seq, err := tc.call(h.store)

			if seq != 0 {
				t.Errorf("seq = %d, quería 0: no se asigna seq a lo que no se escribió", seq)
			}
			requireWrapped(t, tc.name, err, tc.prefix, errWrapDown)
			if err != nil && strings.Contains(err.Error(), testClientText) {
				t.Errorf("el error cita el cuerpo: %v", err)
			}
			requireStatements(t, h.fake, 0)
		})
	}
}
