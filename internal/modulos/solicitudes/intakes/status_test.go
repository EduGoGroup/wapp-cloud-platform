//go:build pendiente

package intakes

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// Las salidas esperadas de este fichero son LITERALES calculados con el fichero
// viejo (internal/intakes/status.go @ 64c181a): el candado de fronteras impide
// importarlo desde aquí, así que la equivalencia viejo ↔ nuevo se fija con lo que
// el viejo devolvió para este mismo corpus. Las claves van escritas a mano, no
// con las constantes, para que el test no se mueva con lo que vigila.

// lifecycleKeys son las doce claves que el fichero reconoce: los once estados y el
// alias legado `closed`.
var lifecycleKeys = []string{
	"open", "pending_approval", "confirmed", "deposit_requested", "deposit_paid", "settled",
	"cancelled", "expired", "abandoned", "rejected", "needs_info", "closed",
}

// adversarialKeys son claves que se PARECEN a una válida y no lo son: el fichero
// compara byte a byte, sin recortar, sin plegar mayúsculas y sin normalizar Unicode.
var adversarialKeys = []string{
	"CLOSED", "Closed", " closed", "closed ", "closed\n", "\tclosed", "clo\u200bsed",
	"ｃlosed", "closed\u00a0", "", " ", "en_camino", "CONFIRMED", "OPEN", " open",
	"closedclosed", "closed,open",
}

// TestStatusKeys_AreTheEnglishWireKeys: las claves son de wire y de columna, en inglés (R-09), y
// no cambian ni un byte.
func TestStatusKeys_AreTheEnglishWireKeys(t *testing.T) {
	t.Parallel()
	cases := []struct{ got, want string }{
		{StatusOpen, "open"},
		{StatusPendingApproval, "pending_approval"},
		{StatusConfirmed, "confirmed"},
		{StatusDepositRequested, "deposit_requested"},
		{StatusDepositPaid, "deposit_paid"},
		{StatusSettled, "settled"},
		{StatusCancelled, "cancelled"},
		{StatusExpired, "expired"},
		{StatusAbandoned, "abandoned"},
		{StatusRejected, "rejected"},
		{StatusNeedsInfo, "needs_info"},
		{StatusClosedLegacy, "closed"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("clave = %q, quería %q", c.got, c.want)
		}
	}
}

// TestNormalizeStatus_ResolvesOnlyTheExactLegacyAlias: `closed` → `confirmed` y nada más; el
// resto, vacía y desconocidas incluidas, sale byte a byte como entró.
func TestNormalizeStatus_ResolvesOnlyTheExactLegacyAlias(t *testing.T) {
	t.Parallel()
	if got := NormalizeStatus("closed"); got != "confirmed" {
		t.Errorf("NormalizeStatus(\"closed\") = %q, quería \"confirmed\"", got)
	}
	untouched := append(slices.Clone(adversarialKeys), lifecycleKeys[:11]...)
	for _, in := range untouched {
		if got := NormalizeStatus(in); got != in {
			t.Errorf("NormalizeStatus(%q) = %q, quería la misma clave intacta", in, got)
		}
	}
}

// TestStoredVariants_ReachesTheLegacyRows: `confirmed` y su alias dan las MISMAS dos variantes,
// en orden alfabético; lo demás, solo su clave (sin validar).
func TestStoredVariants_ReachesTheLegacyRows(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want []string
	}{
		{"confirmed", []string{"closed", "confirmed"}},
		{"closed", []string{"closed", "confirmed"}},
		{"open", []string{"open"}},
		{"expired", []string{"expired"}},
		{"needs_info", []string{"needs_info"}},
		{"", []string{""}},
		{"en_camino", []string{"en_camino"}},
		{"CLOSED", []string{"CLOSED"}},
		{"CONFIRMED", []string{"CONFIRMED"}},
		{" closed", []string{" closed"}},
		{"closed,open", []string{"closed,open"}},
	}
	for _, c := range cases {
		if got := StoredVariants(c.in); !slices.Equal(got, c.want) {
			t.Errorf("StoredVariants(%q) = %q, quería %q", c.in, got, c.want)
		}
	}
}

// TestStoredVariantsOf_ExpandsEveryElementSortedAndDeduplicated: se expande CADA elemento (el
// `confirmed` del final también), y el resultado sale ordenado y sin repetidos.
func TestStoredVariantsOf_ExpandsEveryElementSortedAndDeduplicated(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"alias holder last", []string{"open", "confirmed"}, []string{"closed", "confirmed", "open"}},
		{"key and its alias once", []string{"confirmed", "closed"}, []string{"closed", "confirmed"}},
		{"input order one way", []string{"cancelled", "open"}, []string{"cancelled", "open"}},
		{"input order the other way", []string{"open", "cancelled"}, []string{"cancelled", "open"}},
		{"repeated key", []string{"open", "open", "open"}, []string{"open"}},
		{"empty key travels", []string{""}, []string{""}},
		{"empty key next to a real one", []string{"", "open"}, []string{"", "open"}},
		{"unknown repeated around the alias", []string{"en_camino", "closed", "en_camino"}, []string{"closed", "confirmed", "en_camino"}},
		{"uppercase is not the alias", []string{"CLOSED", "closed"}, []string{"CLOSED", "closed", "confirmed"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := StoredVariantsOf(c.in); !slices.Equal(got, c.want) {
				t.Errorf("StoredVariantsOf(%q) = %q, quería %q", c.in, got, c.want)
			}
		})
	}
}

// TestStoredVariantsOf_EmptyListIsNil: nil y `[]` devuelven nil («sin filtro»), no un slice
// vacío que en un `= ANY` no casaría con nada.
func TestStoredVariantsOf_EmptyListIsNil(t *testing.T) {
	t.Parallel()
	if got := StoredVariantsOf(nil); got != nil {
		t.Errorf("StoredVariantsOf(nil) = %q, quería nil", got)
	}
	if got := StoredVariantsOf([]string{}); got != nil {
		t.Errorf("StoredVariantsOf([]) = %q, quería nil", got)
	}
}

// TestIsStatus_KnowsTheTwelveKeysAndNothingElse: los once estados y el alias; ni la vacía, ni
// una inventada, ni una válida con mayúsculas o espacios.
func TestIsStatus_KnowsTheTwelveKeysAndNothingElse(t *testing.T) {
	t.Parallel()
	for _, key := range lifecycleKeys {
		if !IsStatus(key) {
			t.Errorf("IsStatus(%q) = false, quería true: es una clave del ciclo de vida", key)
		}
	}
	for _, key := range adversarialKeys {
		if IsStatus(key) {
			t.Errorf("IsStatus(%q) = true, quería false", key)
		}
	}
}

// TestCanTransition_WholeMatrix recorre TODOS los pares origen→destino de las doce claves más
// las adversarias y exige que sean válidos exactamente los 24 que da el fichero viejo: las 18
// transiciones de D-041.10 y las 6 que añade el alias `closed` (4 como origen, 2 como destino).
// Todo lo demás es false: a sí mismo, `closed`↔`confirmed`, terminales absorbentes, `expired`
// como destino, expired → abandoned y cualquier clave desconocida.
func TestCanTransition_WholeMatrix(t *testing.T) {
	t.Parallel()
	valid := map[string]bool{
		"open→pending_approval": true, "open→confirmed": true, "open→cancelled": true,
		"open→abandoned": true, "open→closed": true,
		"pending_approval→confirmed": true, "pending_approval→cancelled": true,
		"pending_approval→rejected": true, "pending_approval→needs_info": true,
		"pending_approval→closed":    true,
		"confirmed→pending_approval": true, "confirmed→deposit_requested": true,
		"confirmed→settled": true, "confirmed→cancelled": true,
		"deposit_requested→deposit_paid": true, "deposit_requested→cancelled": true,
		"deposit_paid→settled": true, "deposit_paid→cancelled": true,
		"needs_info→pending_approval": true, "needs_info→cancelled": true,
		"closed→pending_approval": true, "closed→deposit_requested": true,
		"closed→settled": true, "closed→cancelled": true,
	}
	if len(valid) != 24 {
		t.Fatalf("la tabla del test enumera %d pares, quería 24", len(valid))
	}
	keys := append(slices.Clone(lifecycleKeys), adversarialKeys...)
	seen := 0
	for _, from := range keys {
		for _, to := range keys {
			want := valid[from+"→"+to]
			if got := CanTransition(from, to); got != want {
				t.Errorf("CanTransition(%q, %q) = %v, quería %v", from, to, got, want)
			}
			if want {
				seen++
			}
		}
	}
	if seen != 24 {
		t.Errorf("la matriz recorrió %d pares válidos, quería 24", seen)
	}
}

// TestCanTransition_ExpiredIsNeverADestination: nada vence por tiempo (D-041.16); ningún origen
// entra en `expired` ni lo ve ofrecido.
func TestCanTransition_ExpiredIsNeverADestination(t *testing.T) {
	t.Parallel()
	for _, from := range lifecycleKeys {
		if CanTransition(from, "expired") {
			t.Errorf("CanTransition(%q, \"expired\") = true, quería false", from)
		}
		if slices.Contains(AllowedTransitions(from), "expired") {
			t.Errorf("AllowedTransitions(%q) ofrece \"expired\"", from)
		}
	}
}

// TestCanTransition_AbandonedIsAbsorbingButReachableFromOpen: lo abandonado no revive hacia
// ninguna clave; su entrada es open → abandoned.
func TestCanTransition_AbandonedIsAbsorbingButReachableFromOpen(t *testing.T) {
	t.Parallel()
	for _, to := range append(slices.Clone(lifecycleKeys), "en_camino", "") {
		if CanTransition("abandoned", to) {
			t.Errorf("CanTransition(\"abandoned\", %q) = true, quería false", to)
		}
	}
	if !CanTransition("open", "abandoned") {
		t.Error("CanTransition(\"open\", \"abandoned\") = false, quería true")
	}
}

// TestCanTransition_ConfirmedCanGoBackToPendingApproval: la vuelta a editable para
// re-presupuestar (D-041.26), también desde una fila legada.
func TestCanTransition_ConfirmedCanGoBackToPendingApproval(t *testing.T) {
	t.Parallel()
	for _, from := range []string{"confirmed", "closed"} {
		if !CanTransition(from, "pending_approval") {
			t.Errorf("CanTransition(%q, \"pending_approval\") = false, quería true", from)
		}
	}
}

// TestAllowedTransitions_SortedCanonicalAndNeverNil: orden alfabético, claves canónicas, y lista
// vacía —no nil— para terminales y desconocidas.
func TestAllowedTransitions_SortedCanonicalAndNeverNil(t *testing.T) {
	t.Parallel()
	cases := []struct {
		from string
		want []string
	}{
		{"open", []string{"abandoned", "cancelled", "confirmed", "pending_approval"}},
		{"pending_approval", []string{"cancelled", "confirmed", "needs_info", "rejected"}},
		{"confirmed", []string{"cancelled", "deposit_requested", "pending_approval", "settled"}},
		{"closed", []string{"cancelled", "deposit_requested", "pending_approval", "settled"}},
		{"deposit_requested", []string{"cancelled", "deposit_paid"}},
		{"deposit_paid", []string{"cancelled", "settled"}},
		{"needs_info", []string{"cancelled", "pending_approval"}},
		{"settled", []string{}},
		{"cancelled", []string{}},
		{"expired", []string{}},
		{"abandoned", []string{}},
		{"rejected", []string{}},
	}
	for _, key := range adversarialKeys {
		cases = append(cases, struct {
			from string
			want []string
		}{key, []string{}})
	}
	for _, c := range cases {
		got := AllowedTransitions(c.from)
		if got == nil {
			t.Errorf("AllowedTransitions(%q) = nil, quería una lista no nula", c.from)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("AllowedTransitions(%q) = %q, quería %q", c.from, got, c.want)
		}
		if slices.Contains(got, "closed") {
			t.Errorf("AllowedTransitions(%q) ofrece el alias \"closed\"", c.from)
		}
	}
}

// TestCanDiscard_OnlyOpenAndExpired: se descarta lo abierto y lo vencido; lo confirmado —y su
// alias `closed`— se cancela, no se descarta.
func TestCanDiscard_OnlyOpenAndExpired(t *testing.T) {
	t.Parallel()
	for _, key := range append(slices.Clone(lifecycleKeys), adversarialKeys...) {
		want := key == "open" || key == "expired"
		if got := CanDiscard(key); got != want {
			t.Errorf("CanDiscard(%q) = %v, quería %v", key, got, want)
		}
	}
}

// TestCanDiscard_DoesNotOpenTheTransition: que `expired` sea descartable NO abre
// expired → abandoned en la máquina ni lo ofrece en el selector.
func TestCanDiscard_DoesNotOpenTheTransition(t *testing.T) {
	t.Parallel()
	if !CanDiscard("expired") {
		t.Fatal("CanDiscard(\"expired\") = false, quería true")
	}
	if CanTransition("expired", "abandoned") {
		t.Error("CanTransition(\"expired\", \"abandoned\") = true, quería false")
	}
	if got := AllowedTransitions("expired"); len(got) != 0 {
		t.Errorf("AllowedTransitions(\"expired\") = %q, quería una lista vacía", got)
	}
}

// TestTransitionError_TextIsLiteral: el texto es observable; los dos estados van con %q y
// Allowed no entra en él.
func TestTransitionError_TextIsLiteral(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err  *TransitionError
		want string
	}{
		{&TransitionError{From: "open", To: "settled", Allowed: []string{"abandoned", "cancelled"}}, `transición inválida de "open" a "settled"`},
		{&TransitionError{}, `transición inválida de "" a ""`},
		{&TransitionError{From: "closed", To: `en "camino"`}, `transición inválida de "closed" a "en \"camino\""`},
		{&TransitionError{From: "señado", To: "a\nb"}, `transición inválida de "señado" a "a\nb"`},
	}
	for _, c := range cases {
		if got := c.err.Error(); got != c.want {
			t.Errorf("Error() = %q, quería %q", got, c.want)
		}
	}
	if got := cases[0].err.Error(); strings.Contains(got, "abandoned") {
		t.Errorf("Error() = %q: Allowed no debe entrar en el texto", got)
	}
}

// TestTransitionError_IsRecoveredAsPointer: se devuelve como puntero y errors.As lo recoge, con
// sus tres campos, también envuelto.
func TestTransitionError_IsRecoveredAsPointer(t *testing.T) {
	t.Parallel()
	wrapped := fmt.Errorf("set status: %w", &TransitionError{From: "open", To: "settled", Allowed: []string{"cancelled"}})
	var transition *TransitionError
	if !errors.As(wrapped, &transition) {
		t.Fatalf("errors.As no recoge un *TransitionError envuelto: %v", wrapped)
	}
	if transition.From != "open" || transition.To != "settled" || !slices.Equal(transition.Allowed, []string{"cancelled"}) {
		t.Errorf("el error lleva %+v, quería (open, settled, [cancelled])", *transition)
	}
}
