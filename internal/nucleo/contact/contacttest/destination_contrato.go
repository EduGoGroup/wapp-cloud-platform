// Casos de Destino: preferencia, solo LID, solo username, id inexistente y valor normalizado
// (R-19 a R-21, R-23).

package contacttest

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
	"github.com/google/uuid"
)

// casoDestinoPrefiereTelefono (R-19): entre las refs direccionables, el teléfono gana al LID,
// sea cual sea el orden de la entrada y el orden en que las refs se ataron, y un wa_username
// (que no es direccionable) no se lo quita.
func casoDestinoPrefiereTelefono(t *testing.T, m Montaje) {
	// Las dos en una llamada, con el LID delante.
	telAna := refTel(t, numeroAna)
	idAna := resolverOK(t, m, m.TenantA, refLID(t, lidAna), telAna)
	exigirDestino(t, m, m.TenantA, idAna, telAna)

	// El contacto nace por LID y el teléfono se ata después: antes solo hay LID.
	lidDeBeto, telDeBeto := refLID(t, lidBeto), refTel(t, numeroBeto)
	idBeto := resolverOK(t, m, m.TenantA, lidDeBeto)
	exigirDestino(t, m, m.TenantA, idBeto, lidDeBeto)
	mismoID(t, "el teléfono se ata al contacto del LID", resolverOK(t, m, m.TenantA, lidDeBeto, telDeBeto), idBeto)
	exigirDestino(t, m, m.TenantA, idBeto, telDeBeto)

	// Con un username de por medio.
	telCarla := refTel(t, numeroCarla)
	idCarla := resolverOK(t, m, m.TenantA, refUsuario(t, usuarioBeto), refLID(t, lidCarla), telCarla)
	exigirDestino(t, m, m.TenantA, idCarla, telCarla)
}

// casoDestinoSoloLID (R-19): sin teléfono, el destino es el LID. wa_username figura antes que
// wa_lid en el orden de preferencia pero hoy no es direccionable, así que en la práctica se
// degrada a wa_lid: un contacto con username y LID da el LID.
func casoDestinoSoloLID(t *testing.T, m Montaje) {
	lid := refLID(t, lidAna)
	id := resolverOK(t, m, m.TenantA, lid)
	exigirDestino(t, m, m.TenantA, id, lid)

	lidConUsername := refLID(t, lidBeto)
	idConUsername := resolverOK(t, m, m.TenantA, refUsuario(t, usuarioAna), lidConUsername)
	exigirDestino(t, m, m.TenantA, idConUsername, lidConUsername)
}

// casoDestinoSoloUsername (R-20): un contacto que existe pero solo tiene un wa_username (aún no
// direccionable) da ErrNoDestino, y no ErrContactNotFound: el contacto sí existe.
func casoDestinoSoloUsername(t *testing.T, m Montaje) {
	id := resolverOK(t, m, m.TenantA, refUsuario(t, usuarioAna))

	err := destinoError(t, m, m.TenantA, id)

	exigirErrorIs(t, err, contact.ErrNoDestino, "Destino de un contacto solo con username")
	if errors.Is(err, contact.ErrContactNotFound) {
		t.Errorf("Destino de un contacto que existe dio también ErrContactNotFound: %v", err)
	}
}

// casoDestinoInexistente (R-21): un contact_id bien formado que no existe en el tenant da
// ErrContactNotFound (no ErrNoDestino), envuelto con el id entre comillas como promete el
// puerto. Se prueba en un tenant que ya tiene un contacto, para que no sea solo «tenant vacío».
func casoDestinoInexistente(t *testing.T, m Montaje) {
	resolverOK(t, m, m.TenantA, refTel(t, numeroAna))

	id := uuid.NewString()
	err := destinoError(t, m, m.TenantA, id)

	exigirErrorIs(t, err, contact.ErrContactNotFound, "Destino de un contact_id inexistente")
	if errors.Is(err, contact.ErrNoDestino) {
		t.Errorf("Destino de un contact_id inexistente dio también ErrNoDestino: %v", err)
	}
	if !strings.Contains(err.Error(), strconv.Quote(id)) {
		t.Errorf("el error %q no lleva el contact_id entre comillas (%s)", err, strconv.Quote(id))
	}
	exigirNoEncontrado(t, m, m.TenantB, uuid.NewString(), "Destino de otro contact_id inexistente, en el tenant vacío")
}

// casoDestinoDevuelveElValorNormalizado (R-23): Destino devuelve la ref tal como quedó guardada,
// con el value normalizado y no el crudo con el que llegó. Va de ida y vuelta por el almacén
// (con Postgres, cifrado y descifrado).
func casoDestinoDevuelveElValorNormalizado(t *testing.T, m Montaje) {
	crudos := []struct{ kind, crudo string }{
		{contact.KindPhoneE164, "+57 (300) 111-2233"},
		{contact.KindWALID, "88887777:12@lid"},
	}
	for _, c := range crudos {
		ref := nuevaRef(t, c.kind, c.crudo)
		if ref.Value == c.crudo {
			t.Fatalf("precondición: el valor crudo %q de %s debía diferir del normalizado", c.crudo, c.kind)
		}
		id := resolverOK(t, m, m.TenantA, ref)

		exigirDestino(t, m, m.TenantA, id, ref)
	}
}
