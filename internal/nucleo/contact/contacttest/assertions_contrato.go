// Aserciones compartidas por los casos: comparar contact_id, exigir Destino, errores y dueño del estado.

package contacttest

import (
	"errors"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
)

// mismoID falla el test si obtenido no es esperado.
func mismoID(t *testing.T, que, obtenido, esperado string) {
	t.Helper()
	if obtenido != esperado {
		t.Errorf("%s: contact_id %q; quiere %q", que, obtenido, esperado)
	}
}

// distintoID falla el test si a y b son el mismo contact_id.
func distintoID(t *testing.T, que, a, b string) {
	t.Helper()
	if a == b {
		t.Errorf("%s: dieron el mismo contact_id %q; quiere dos distintos", que, a)
	}
}

// exigirDestino exige que Destino del contacto devuelva exactamente la ref quiere.
func exigirDestino(t *testing.T, m Montaje, tenantID, contactID string, quiere contact.Ref) {
	t.Helper()
	got, err := m.Resolver.Destino(t.Context(), tenantID, contactID)
	if err != nil {
		t.Errorf("Destino(tenant %s, %s): %v; quiere %+v", tenantID, contactID, err, quiere)
		return
	}
	if got != quiere {
		t.Errorf("Destino(tenant %s, %s) = %+v; quiere %+v", tenantID, contactID, got, quiere)
	}
}

// destinoError llama a Destino y exige que falle: devuelve el error.
func destinoError(t *testing.T, m Montaje, tenantID, contactID string) error {
	t.Helper()
	ref, err := m.Resolver.Destino(t.Context(), tenantID, contactID)
	if err == nil {
		t.Fatalf("Destino(tenant %s, %s) = %+v sin error; quiere un error", tenantID, contactID, ref)
	}
	return err
}

// exigirErrorIs falla el test si err no es, según errors.Is, el centinela quiere.
func exigirErrorIs(t *testing.T, err, quiere error, que string) {
	t.Helper()
	if !errors.Is(err, quiere) {
		t.Errorf("%s: error %v; quiere %v (errors.Is)", que, err, quiere)
	}
}

// exigirNoEncontrado exige que Destino del contactID en el tenant dé ErrContactNotFound.
func exigirNoEncontrado(t *testing.T, m Montaje, tenantID, contactID, que string) {
	t.Helper()
	exigirErrorIs(t, destinoError(t, m, tenantID, contactID), contact.ErrContactNotFound, que)
}

// exigirDueno exige que el estado de la sesión del tenant pertenezca a quiere.
func exigirDueno(t *testing.T, m Montaje, tenantID, sessionID, quiere string) {
	t.Helper()
	got, ok := m.Estado.Dueno(t, tenantID, sessionID)
	switch {
	case !ok:
		t.Errorf("la sesión %q del tenant %s no tiene estado; quiere el de %q", sessionID, tenantID, quiere)
	case got != quiere:
		t.Errorf("el estado de la sesión %q del tenant %s es de %q; quiere el de %q", sessionID, tenantID, got, quiere)
	}
}
