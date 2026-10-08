package intakes

import (
	"slices"
	"testing"
)

// Las salidas esperadas de este fichero son LITERALES calculados con el fichero
// viejo (internal/intakes/crm.go @ 64c181a): el candado de fronteras impide
// importarlo desde aquí.

// TestCRMStatuses_WireValues: los cuatro estados canónicos del contrato wapp-crm-v1, byte a byte
// (son el enum del schema publicado y el CHECK de la migración 0048).
func TestCRMStatuses_WireValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		got  string
		want string
	}{
		{name: "paid", got: CRMStatusPaid, want: "paid"},
		{name: "preparing", got: CRMStatusPreparing, want: "preparing"},
		{name: "delivered", got: CRMStatusDelivered, want: "delivered"},
		{name: "rejected", got: CRMStatusRejected, want: "rejected"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("CRMStatus %s = %q, quería %q", c.name, c.got, c.want)
		}
	}
}

// TestIsCRMStatus: la lista es cerrada (cuatro) y la comparación es exacta, sin plegar mayúsculas
// ni recortar espacios.
func TestIsCRMStatus(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		status string
		want   bool
	}{
		{name: "paid", status: CRMStatusPaid, want: true},
		{name: "preparing", status: CRMStatusPreparing, want: true},
		{name: "delivered", status: CRMStatusDelivered, want: true},
		{name: "rejected", status: CRMStatusRejected, want: true},

		{name: "capitalized", status: "Paid"},
		{name: "upper case", status: "PAID"},
		{name: "capitalized rejected", status: "Rejected"},
		{name: "leading space", status: " paid"},
		{name: "trailing space", status: "paid "},
		{name: "trailing newline", status: "paid\n"},
		{name: "empty", status: ""},
		{name: "prefix of a canonical status", status: "pai"},
		{name: "canonical status with a suffix", status: "paidd"},
		{name: "two canonical statuses joined", status: "delivered,paid"},
		// La segunda letra es la a cirílica (U+0430): se ve igual y no es el mismo estado.
		{name: "cyrillic homoglyph", status: "p\u0430id"},
		// Estados del ciclo de vida de wApp: otro vocabulario.
		{name: "lifecycle confirmed", status: "confirmed"},
		{name: "lifecycle closed alias", status: "closed"},
		{name: "lifecycle open", status: "open"},
		{name: "lifecycle cancelled", status: "cancelled"},
		{name: "unknown word", status: "pending"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := IsCRMStatus(c.status); got != c.want {
				t.Errorf("IsCRMStatus(%q) = %v, quería %v", c.status, got, c.want)
			}
		})
	}
}

// TestCRMReflection_FoundAndChangedAreIndependent: el valor cero es «no encontrada», y Found y
// Changed se leen por separado (un reintento del puente es Found sin Changed).
func TestCRMReflection_FoundAndChangedAreIndependent(t *testing.T) {
	t.Parallel()
	var zero CRMReflection
	if zero.Found || zero.Changed || zero.Intake.ID != "" || zero.Intake.ContactID != "" {
		t.Errorf("CRMReflection{} = %+v, quería no encontrada, sin cambio y con la solicitud vacía", zero)
	}

	retry := CRMReflection{Found: true, Intake: Intake{ID: "id-1", ContactID: "contacto-opaco-1", SessionID: "sess-a"}}
	if !retry.Found || retry.Changed {
		t.Errorf("reintento = (Found %v, Changed %v), quería (true, false)", retry.Found, retry.Changed)
	}
	if retry.Intake.ID != "id-1" || retry.Intake.ContactID != "contacto-opaco-1" || retry.Intake.SessionID != "sess-a" {
		t.Errorf("la solicitud reflejada no conserva contacto y sesión: %+v", retry.Intake)
	}
}

// TestCRMCanonicalStatuses_ClosedListInSchemaOrder: la lista es cerrada y va en el orden del enum
// del schema publicado y del CHECK de la migración 0048.
func TestCRMCanonicalStatuses_ClosedListInSchemaOrder(t *testing.T) {
	t.Parallel()
	want := []string{"paid", "preparing", "delivered", "rejected"}
	if !slices.Equal(crmCanonicalStatuses, want) {
		t.Errorf("crmCanonicalStatuses = %q, quería %q", crmCanonicalStatuses, want)
	}
}
