//go:build integracion

package procesos

import (
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact/contacthelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// La suite y crypto solo valen en un *_contrato_test.go.
func TestP3(t *testing.T) { _, _ = contacthelpertest.Contrato, crypto.NewFieldCipher }
