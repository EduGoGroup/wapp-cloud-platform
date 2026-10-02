// Casos de aislamiento: el kind y el tenant forman parte de la clave de un contacto (N-01, N-02),
// también al pedir su Destino desde otro tenant (R-21).

package contacttest

import (
	"fmt"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
)

// casoMismoValorOtroKind (N-02): el kind es parte de la clave de dedup. El mismo valor como
// phone_e164, wa_lid y wa_username son tres contactos distintos, y cada ref sigue resolviendo
// al suyo.
func casoMismoValorOtroKind(t *testing.T, m Montaje) {
	const valor = "88887777"
	refs := []contact.Ref{
		nuevaRef(t, contact.KindPhoneE164, valor),
		nuevaRef(t, contact.KindWALID, valor),
		nuevaRef(t, contact.KindWAUsername, valor),
	}
	ids := make([]string, len(refs))
	for i, r := range refs {
		ids[i] = resolverOK(t, m, m.TenantA, r)
	}
	for i := range refs {
		for j := i + 1; j < len(refs); j++ {
			distintoID(t, fmt.Sprintf("el valor %s como %s y como %s", valor, refs[i].Kind, refs[j].Kind), ids[i], ids[j])
		}
	}
	for i, r := range refs {
		mismoID(t, "la ref "+r.Kind+" otra vez", resolverOK(t, m, m.TenantA, r), ids[i])
	}
}

// casoMismaRefOtroTenant (N-01): las mismas refs en dos tenants son dos contactos distintos, y
// nada se funde a través de un tenant: el LID que solo existe en B no se mezcla con el teléfono
// de A cuando llegan juntos en A.
func casoMismaRefOtroTenant(t *testing.T, m Montaje) {
	tel, lid := refTel(t, numeroAna), refLID(t, lidAna)

	idA := resolverOK(t, m, m.TenantA, tel, lid)
	idB := resolverOK(t, m, m.TenantB, tel, lid)

	distintoID(t, "las mismas refs en dos tenants", idA, idB)
	mismoID(t, "en A, el teléfono solo", resolverOK(t, m, m.TenantA, tel), idA)
	mismoID(t, "en B, el LID solo", resolverOK(t, m, m.TenantB, lid), idB)

	otroTel, otroLID := refTel(t, numeroBeto), refLID(t, lidBeto)
	telEnA := resolverOK(t, m, m.TenantA, otroTel)
	lidEnB := resolverOK(t, m, m.TenantB, otroLID)
	mismoID(t, "en A, el teléfono junto a un LID que solo existe en B", resolverOK(t, m, m.TenantA, otroTel, otroLID), telEnA)
	mismoID(t, "en B, el LID sigue siendo suyo", resolverOK(t, m, m.TenantB, otroLID), lidEnB)
	distintoID(t, "el contacto de A y el de B", telEnA, lidEnB)
}

// casoDestinoOtroTenant (N-01, R-21): un contact_id solo existe dentro del tenant que lo creó;
// pedirlo desde otro tenant da ErrContactNotFound, en los dos sentidos, y en el suyo sí se
// encuentra.
func casoDestinoOtroTenant(t *testing.T, m Montaje) {
	tel, lid := refTel(t, numeroAna), refLID(t, lidBeto)
	idA := resolverOK(t, m, m.TenantA, tel)
	idB := resolverOK(t, m, m.TenantB, lid)

	exigirDestino(t, m, m.TenantA, idA, tel)
	exigirDestino(t, m, m.TenantB, idB, lid)
	exigirNoEncontrado(t, m, m.TenantB, idA, "Destino en B del contacto de A")
	exigirNoEncontrado(t, m, m.TenantA, idB, "Destino en A del contacto de B")
}
