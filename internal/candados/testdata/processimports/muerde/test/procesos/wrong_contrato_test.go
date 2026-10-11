//go:build integracion

package procesos

import (
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact/contacthelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/metrics"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
)

// Una suite de contrato que usa del puerto algo más que el constructor, e importa un paquete
// de dominio y dos de platform que no están admitidos: uno cualquiera y el PADRE de uno admitido.
func TestWrong(t *testing.T) {
	_ = contact.NewPostgresResolver
	_, _ = contact.Normalize("phone_e164", "573001112233")
	_, _ = contact.Normalize("wa_lid", "1")
	var _ contact.Ref
	_, _, _ = contacthelpertest.Contrato, store.New, postgres.WithTx
	_ = metrics.New
}
