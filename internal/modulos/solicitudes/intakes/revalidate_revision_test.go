package intakes

import (
	"math"
	"testing"
)

// Vectores calculados con el fichero viejo (internal/intakes/revalidate.go @ 64c181a).

// TestRevalidatedRevision_IsASystemRevalidatedRevision: la revisión que escriben los stores es
// `revalidated`, de `system`, con el payload v1 y el texto mandado tal cual; ni número ni fecha.
func TestRevalidatedRevision_IsASystemRevalidatedRevision(t *testing.T) {
	t.Parallel()
	rv := Revalidate(martaLines(), catalogOfThe15th())
	rev, err := revalidatedRevision("id-marta", rv, "aviso")
	if err != nil {
		t.Fatalf("revalidatedRevision devolvió el error %v, quería nil", err)
	}
	const wantPayload = `{"version":1,"repriced":[{"sku":"PAN","from":2,"to":2.5}],"removed":[{"sku":"QUESO","label":"Queso","qty":1,"unit_price":3}],"total_before":7,"total_after":5}`
	if rev.IntakeID != "id-marta" || rev.Kind != "revalidated" || rev.CreatedBy != "system" || rev.RenderedText != "aviso" {
		t.Errorf("revisión = (%q, %q, %q, %q), quería (id-marta, revalidated, system, aviso)",
			rev.IntakeID, rev.Kind, rev.CreatedBy, rev.RenderedText)
	}
	if string(rev.Payload) != wantPayload {
		t.Errorf("payload = %s\nquería    %s", rev.Payload, wantPayload)
	}
	if rev.RevisionNo != 0 || !rev.CreatedAt.IsZero() {
		t.Errorf("la revisión trae número %d y fecha %v: eso lo pone el store", rev.RevisionNo, rev.CreatedAt)
	}
}

// TestRevalidatedRevision_PropagatesThePayloadError: si el payload no se puede serializar no hay
// revisión a medias.
func TestRevalidatedRevision_PropagatesThePayloadError(t *testing.T) {
	t.Parallel()
	rev, err := revalidatedRevision("id-marta", Revalidation{TotalBefore: math.NaN()}, "aviso")
	const want = "intakes: serializar payload de la revisión de revalidación: json: unsupported value: NaN"
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, quería %q", err, want)
	}
	if rev.IntakeID != "" || rev.Kind != "" || rev.Payload != nil || rev.RenderedText != "" || rev.CreatedBy != "" {
		t.Errorf("revisión = %+v junto al error, quería la revisión vacía", rev)
	}
}

// TestItemsTotal_IgnoresCustomization: el total es qty × unit_price y la personalización no entra (INV-13).
func TestItemsTotal_IgnoresCustomization(t *testing.T) {
	t.Parallel()
	plain := []Item{{SKU: "PAN", Qty: 2, UnitPrice: 2}, {SKU: "_shipping", Qty: 1, UnitPrice: 4}}
	custom := []Item{{SKU: "PAN", Qty: 2, UnitPrice: 2, Customization: "sin sal"}, {SKU: "_shipping", Qty: 1, UnitPrice: 4}}
	if got := itemsTotal(plain); got != 8 {
		t.Errorf("itemsTotal = %v, quería 8", got)
	}
	if itemsTotal(custom) != itemsTotal(plain) {
		t.Errorf("la personalización movió el total: %v", itemsTotal(custom))
	}
	if got := itemsTotal(nil); got != 0 {
		t.Errorf("itemsTotal(nil) = %v, quería 0", got)
	}
}
