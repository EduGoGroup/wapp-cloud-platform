// Casos de Resolve: crear, reutilizar, atar refs, refs repetidas y lista vacía (R-12 a R-15, R-18).
// Aquí se añade cualquier promesa nueva sobre qué contact_id devuelve Resolve y cuándo.

package contacttest

import (
	"errors"
	"fmt"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
)

// casoSinRefs (R-18): sin ninguna ref, tras deduplicar, Resolve devuelve ErrNoRefs y contactID
// "", se llame con nil o con []Ref{} y venga o no con push_name (el nombre solo no crea nada).
// El centinela llega SIN envolver: el texto es exactamente el suyo, sin el tenant ni otro detalle.
func casoSinRefs(t *testing.T, m Montaje) {
	listas := []struct {
		nombre string
		refs   []contact.Ref
	}{
		{"nil", nil},
		{"[]Ref{}", []contact.Ref{}},
	}
	for _, l := range listas {
		for _, nombre := range []string{"", "Ana"} {
			id, err := m.Resolver.Resolve(t.Context(), m.TenantA, l.refs, nombre)
			if !errors.Is(err, contact.ErrNoRefs) {
				t.Errorf("Resolve(refs %s, push_name %q): error %v; quiere contact.ErrNoRefs", l.nombre, nombre, err)
			}
			requireErrorText(t, err, noRefsText, fmt.Sprintf("Resolve(refs %s, push_name %q)", l.nombre, nombre))
			if id != "" {
				t.Errorf("Resolve(refs %s, push_name %q) con error devolvió contact_id %q; quiere \"\"", l.nombre, nombre, id)
			}
		}
	}
}

// casoRefNuevaCreaID (R-12): una ref desconocida crea un contact_id nuevo (un UUID), le ata la
// ref y no se confunde con el de otra ref nueva. Lo único que muestra por el puerto que la ref
// quedó atada es Destino.
func casoRefNuevaCreaID(t *testing.T, m Montaje) {
	ana, beto := refTel(t, numeroAna), refTel(t, numeroBeto)

	idAna := resolverOK(t, m, m.TenantA, ana)
	idBeto := resolverOK(t, m, m.TenantA, beto)

	distintoID(t, "dos refs nuevas distintas", idAna, idBeto)
	exigirDestino(t, m, m.TenantA, idAna, ana)
	exigirDestino(t, m, m.TenantA, idBeto, beto)
}

// casoMismaRefMismoID (R-13): el resultado es determinista por (tenant, ref): la misma ref da
// siempre el mismo contact_id, también escrita de otra forma (NewRef la normaliza a la misma
// Ref), y otra ref no se mezcla con ella.
func casoMismaRefMismoID(t *testing.T, m Montaje) {
	ana := refTel(t, numeroAna)
	id := resolverOK(t, m, m.TenantA, ana)

	for i := range 4 {
		mismoID(t, fmt.Sprintf("llamada %d con la misma ref", i+2), resolverOK(t, m, m.TenantA, ana), id)
	}

	otraForma := refTel(t, "+57 (300) 111-2233")
	if otraForma != ana {
		t.Fatalf("precondición: contact.NewRef debía normalizar el mismo número a la misma Ref: %+v vs %+v", otraForma, ana)
	}
	mismoID(t, "el mismo número con otro formato", resolverOK(t, m, m.TenantA, otraForma), id)
	distintoID(t, "otro número", resolverOK(t, m, m.TenantA, refTel(t, numeroBeto)), id)
}

// casoRefRepetidaEnLaEntrada: una ref repetida en la entrada cuenta una vez. Una lista con solo
// repetidas no es una lista vacía (no da ErrNoRefs) y crea un único contacto; repetida junto a
// una existente y a una nueva, tampoco cambia el resultado.
func casoRefRepetidaEnLaEntrada(t *testing.T, m Montaje) {
	ana, lid := refTel(t, numeroAna), refLID(t, lidAna)

	id := resolverOK(t, m, m.TenantA, ana, ana, ana)

	mismoID(t, "la ref sola tras crearla repetida", resolverOK(t, m, m.TenantA, ana), id)
	exigirDestino(t, m, m.TenantA, id, ana)
	mismoID(t, "LID nuevo repetido junto al teléfono existente", resolverOK(t, m, m.TenantA, lid, ana, lid), id)
	mismoID(t, "el LID solo", resolverOK(t, m, m.TenantA, lid), id)
}

// casoDosRefsJuntas (R-14): dos refs del mismo contacto en una llamada dan un solo contact_id,
// y cada una por separado, y las dos en otro orden, vuelven a dar ese mismo.
func casoDosRefsJuntas(t *testing.T, m Montaje) {
	tel, lid := refTel(t, numeroAna), refLID(t, lidAna)

	id := resolverOK(t, m, m.TenantA, tel, lid)

	mismoID(t, "el teléfono solo", resolverOK(t, m, m.TenantA, tel), id)
	mismoID(t, "el LID solo", resolverOK(t, m, m.TenantA, lid), id)
	mismoID(t, "las dos, en el otro orden", resolverOK(t, m, m.TenantA, lid, tel), id)
}

// casoRefNuevaJuntoAExistente (R-15): con un solo contacto existente entre las refs, Resolve
// reutiliza su contact_id y ata las que faltan, esté la nueva delante o detrás.
func casoRefNuevaJuntoAExistente(t *testing.T, m Montaje) {
	tel, lid, user := refTel(t, numeroAna), refLID(t, lidAna), refUsuario(t, usuarioAna)

	id := resolverOK(t, m, m.TenantA, tel)

	mismoID(t, "teléfono existente + LID nuevo", resolverOK(t, m, m.TenantA, tel, lid), id)
	mismoID(t, "el LID, atado, solo", resolverOK(t, m, m.TenantA, lid), id)
	mismoID(t, "username nuevo delante del LID existente", resolverOK(t, m, m.TenantA, user, lid), id)
	mismoID(t, "el username, atado, solo", resolverOK(t, m, m.TenantA, user), id)
}
