// Valores y ayudas compartidos por los casos: los datos de las refs, su construcción con
// contact.NewRef y la llamada a Resolve que exige éxito.

package contacthelpertest

import (
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
	"github.com/google/uuid"
)

// Los valores de las refs de los casos. Cada caso parte de un Montaje limpio, así que pueden
// repetirse de un caso a otro. Los LID y los números son válidos para contact.NewRef.
const (
	numeroAna   = "573001112233"
	numeroBeto  = "573004445566"
	numeroCarla = "573007778899"
	lidAna      = "88887777"
	lidBeto     = "99991111"
	lidCarla    = "55554444"
	usuarioAna  = "juanito"
	usuarioBeto = "betico"
)

// nuevaRef construye la Ref de (kind, valor) con contact.NewRef, el único constructor que el
// puerto admite.
func nuevaRef(t *testing.T, kind, valor string) contact.Ref {
	t.Helper()
	r, err := contact.NewRef(kind, valor)
	if err != nil {
		t.Fatalf("contact.NewRef(%q, %q): %v", kind, valor, err)
	}
	return r
}

// refTel es la Ref de un teléfono E.164.
func refTel(t *testing.T, valor string) contact.Ref {
	t.Helper()
	return nuevaRef(t, contact.KindPhoneE164, valor)
}

// refLID es la Ref de un LID de WhatsApp.
func refLID(t *testing.T, valor string) contact.Ref {
	t.Helper()
	return nuevaRef(t, contact.KindWALID, valor)
}

// refUsuario es la Ref de un username de WhatsApp.
func refUsuario(t *testing.T, valor string) contact.Ref {
	t.Helper()
	return nuevaRef(t, contact.KindWAUsername, valor)
}

// resolverOK llama a Resolve sin push_name y exige que salga bien.
func resolverOK(t *testing.T, m Montaje, tenantID string, refs ...contact.Ref) string {
	t.Helper()
	return resolverConNombre(t, m, tenantID, "", refs...)
}

// resolverConNombre llama a Resolve con pushName y exige que salga bien: sin error y con un
// contact_id que sea un UUID.
func resolverConNombre(t *testing.T, m Montaje, tenantID, pushName string, refs ...contact.Ref) string {
	t.Helper()
	id, err := m.Resolver.Resolve(t.Context(), tenantID, refs, pushName)
	if err != nil {
		t.Fatalf("Resolve(tenant %s, refs %+v, push_name %q): %v", tenantID, refs, pushName, err)
	}
	if _, perr := uuid.Parse(id); perr != nil {
		t.Fatalf("Resolve(tenant %s, refs %+v) devolvió %q, que no es un UUID: %v", tenantID, refs, id, perr)
	}
	return id
}
