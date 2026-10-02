// Aserciones compartidas por los casos: comparar contact_id, exigir Destino, errores (centinela y
// texto exacto) y dueño del estado.

package contacttest

import (
	"errors"
	"strconv"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
)

// Los textos observables de los errores del puerto que las dos implementaciones devuelven byte a
// byte iguales. Van como literales, no leídos de contact.ErrXxx.Error(): así un caso falla tanto si
// una implementación envuelve o reescribe el error como si alguien cambia el texto del centinela.
const (
	// noRefsText es el de Resolve sin refs: ErrNoRefs sin envolver.
	noRefsText = "contact: se requiere al menos una contact_ref"
	// noDestinationText es el de Destino sin ref direccionable: ErrNoDestino sin envolver (sin el
	// detalle del kind que añade Ref.Sendable).
	noDestinationText = "contact: sin destino enviable para el contact_id"
	// notFoundTextPrefix precede al contact_id entre comillas en el de Destino de un contacto que no
	// está en el tenant: ErrContactNotFound envuelto con "%w: %q".
	notFoundTextPrefix = "contact: contact_id no encontrado: "
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

// requireErrorText falla el test si el texto de err no es exactamente want: fija el texto
// observable, que errors.Is no mira (un error envuelto con más detalle sigue pasando errors.Is).
func requireErrorText(t *testing.T, err error, want, what string) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: sin error; quiere uno con el texto %q", what, want)
		return
	}
	if got := err.Error(); got != want {
		t.Errorf("%s: texto observable %q; quiere exactamente %q", what, got, want)
	}
}

// notFoundText es el texto exacto del error de Destino para un contactID que no está en el tenant:
// el del centinela, ": " y el contactID tal como se pidió, entre comillas (%q).
func notFoundText(contactID string) string {
	return notFoundTextPrefix + strconv.Quote(contactID)
}

// exigirNoEncontrado exige que Destino del contactID en el tenant dé ErrContactNotFound, con el
// texto exacto que promete el puerto (ver notFoundText). Vale para las tres formas de no estar: no
// existir, ser de otro tenant y ser el huérfano de una fusión.
func exigirNoEncontrado(t *testing.T, m Montaje, tenantID, contactID, que string) {
	t.Helper()
	err := destinoError(t, m, tenantID, contactID)
	exigirErrorIs(t, err, contact.ErrContactNotFound, que)
	requireErrorText(t, err, notFoundText(contactID), que)
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
