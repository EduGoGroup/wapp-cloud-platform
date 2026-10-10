package runtimehelpertest_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime/runtimehelpertest"
)

// Aserciones de compilación: los dos dobles satisfacen los puertos que declara
// runtime_engine.go.
var (
	_ runtime.ReplyLimiter    = (*runtimehelpertest.ReplyLimiter)(nil)
	_ runtime.DepositReminder = (*runtimehelpertest.DepositReminder)(nil)
)

// TestTenantResolver_FixedAnswerCallsAndInjectedError: contesta el tenant y el perfil fijados, tal
// cual (también un perfil vacío), sea cual sea la sesión; apunta las sesiones; con un error
// inyectado devuelve los dos vacíos.
func TestTenantResolver_FixedAnswerCallsAndInjectedError(t *testing.T) {
	resolver := runtimehelpertest.NewTenantResolver("tenant-1", "")
	down := errors.New("flota caída")

	if tenantID, profile, err := resolver.ResolveTenant(t.Context(), "s1"); tenantID != "tenant-1" || profile != "" || err != nil {
		t.Fatalf("ResolveTenant = (%q, %q, %v), quería (tenant-1, vacío, nil)", tenantID, profile, err)
	}
	resolver.SetProfile(runtimehelpertest.ProfilePassive)
	if _, profile, err := resolver.ResolveTenant(t.Context(), "s2"); profile != runtimehelpertest.ProfilePassive || err != nil {
		t.Errorf("tras SetProfile = (%q, %v), quería passive", profile, err)
	}
	resolver.Fail(down)
	if tenantID, profile, err := resolver.ResolveTenant(t.Context(), "s3"); tenantID != "" || profile != "" || !errors.Is(err, down) {
		t.Errorf("con error inyectado = (%q, %q, %v), quería vacíos y el error", tenantID, profile, err)
	}
	resolver.Fail(nil)
	if _, _, err := resolver.ResolveTenant(t.Context(), "s4"); err != nil {
		t.Errorf("tras retirar el error: %v", err)
	}
	if got, want := resolver.Sessions(), []string{"s1", "s2", "s3", "s4"}; !slices.Equal(got, want) {
		t.Errorf("Sessions = %v, quería %v", got, want)
	}
}

// TestSelfNumbers_ExactMatchPerTenant: casa por igualdad EXACTA y por tenant (un «+» de más no
// casa, otro tenant tampoco), apunta el número tal cual llegó y, con error inyectado, (false, error).
func TestSelfNumbers_ExactMatchPerTenant(t *testing.T) {
	self := runtimehelpertest.NewSelfNumbers()
	self.Add("tenant-1", "573001112233", "56984467443")
	down := errors.New("flota caída")

	cases := []struct {
		tenantID, number string
		want             bool
	}{
		{"tenant-1", "573001112233", true},
		{"tenant-1", "56984467443", true},
		{"tenant-1", "+573001112233", false},
		{"tenant-2", "573001112233", false},
		{"tenant-1", "", false},
	}
	wantQueries := make([]runtimehelpertest.SelfNumberQuery, 0, len(cases)+2)
	for _, c := range cases {
		got, err := self.IsSelfNumber(t.Context(), c.tenantID, c.number)
		if err != nil || got != c.want {
			t.Errorf("IsSelfNumber(%q, %q) = (%v, %v), quería (%v, nil)", c.tenantID, c.number, got, err, c.want)
		}
		wantQueries = append(wantQueries, runtimehelpertest.SelfNumberQuery{TenantID: c.tenantID, Number: c.number})
	}

	self.Fail(down)
	if got, err := self.IsSelfNumber(t.Context(), "tenant-1", "573001112233"); got || !errors.Is(err, down) {
		t.Errorf("con error inyectado = (%v, %v), quería (false, error)", got, err)
	}
	wantQueries = append(wantQueries, runtimehelpertest.SelfNumberQuery{TenantID: "tenant-1", Number: "573001112233"})
	self.Fail(nil)
	if got, err := self.IsSelfNumber(t.Context(), "tenant-1", "573001112233"); !got || err != nil {
		t.Errorf("tras retirar el error = (%v, %v), quería (true, nil)", got, err)
	}
	wantQueries = append(wantQueries, wantQueries[len(wantQueries)-1])
	if got := self.Queries(); !slices.Equal(got, wantQueries) {
		t.Errorf("Queries = %v, quería %v", got, wantQueries)
	}
}

// TestIngestDeduper_RemembersKeys: la primera vez que ve una clave contesta false y desde la
// segunda true; la clave son la sesión Y el mensaje; MarkSeen la deja vista sin contar como
// llamada; con un error inyectado devuelve (false, error) y no la registra.
func TestIngestDeduper_RemembersKeys(t *testing.T) {
	deduper := runtimehelpertest.NewIngestDeduper()
	ctx := t.Context()
	down := errors.New("ingesta caída")
	seen := func(sessionID, waMessageID string, want bool) {
		t.Helper()
		got, err := deduper.Seen(ctx, sessionID, waMessageID)
		if err != nil || got != want {
			t.Errorf("Seen(%q, %q) = (%v, %v), quería (%v, nil)", sessionID, waMessageID, got, err, want)
		}
	}

	seen("s1", "wamid.a", false)
	seen("s1", "wamid.b", false)
	seen("s1", "wamid.a", true) // intercalado, no solo la re-entrega inmediata
	seen("s2", "wamid.a", false)

	deduper.MarkSeen("s1", "wamid.old")
	seen("s1", "wamid.old", true)

	deduper.Fail(down)
	if got, err := deduper.Seen(ctx, "s1", "wamid.c"); got || !errors.Is(err, down) {
		t.Errorf("con error inyectado = (%v, %v), quería (false, error)", got, err)
	}
	deduper.Fail(nil)
	seen("s1", "wamid.c", false) // el intento fallido no la dejó vista

	want := []runtimehelpertest.IngestKey{
		{SessionID: "s1", WaMessageID: "wamid.a"}, {SessionID: "s1", WaMessageID: "wamid.b"},
		{SessionID: "s1", WaMessageID: "wamid.a"}, {SessionID: "s2", WaMessageID: "wamid.a"},
		{SessionID: "s1", WaMessageID: "wamid.old"}, {SessionID: "s1", WaMessageID: "wamid.c"},
		{SessionID: "s1", WaMessageID: "wamid.c"},
	}
	if got := deduper.Calls(); !slices.Equal(got, want) {
		t.Errorf("Calls = %v, quería %v", got, want)
	}
}

// TestReplyLimiter_BudgetPerKey: sin tope lo concede todo; con Limit(n) cada clave tiene n tokens
// en total (contando los ya gastados), una denegada no consume, Limit(0) lo niega todo y un valor
// negativo quita el tope. Cada petición queda apuntada con su veredicto.
func TestReplyLimiter_BudgetPerKey(t *testing.T) {
	limiter := runtimehelpertest.NewReplyLimiter()
	var want []runtimehelpertest.LimiterCall
	allow := func(key string, wantAllowed bool) {
		t.Helper()
		if got := limiter.Allow(key); got != wantAllowed {
			t.Errorf("Allow(%q) = %v, quería %v (llamada %d)", key, got, wantAllowed, len(want)+1)
		}
		want = append(want, runtimehelpertest.LimiterCall{Key: key, Allowed: wantAllowed})
	}

	allow("a", true)
	allow("a", true)
	limiter.Limit(3)
	allow("a", true)  // el tercero de "a": los dos de antes cuentan
	allow("a", false) // agotado
	allow("a", false) // y sigue agotado: la denegada no consumió ni devolvió nada
	allow("b", true)  // otra clave, su propio cupo
	limiter.Limit(0)
	allow("c", false)
	limiter.Limit(-1)
	allow("a", true)

	if got := limiter.Calls(); !slices.Equal(got, want) {
		t.Errorf("Calls = %v, quería %v", got, want)
	}
}

// TestDepositReminder_ReturnsItsTextsAndRecordsTheTouch: sin textos devuelve nil («no procedía»);
// con ellos, una copia en su orden en cada toque; SetTexts los cambia; cada toque queda apuntado.
func TestDepositReminder_ReturnsItsTextsAndRecordsTheTouch(t *testing.T) {
	ctx := t.Context()
	silent := runtimehelpertest.NewDepositReminder()
	if got := silent.RemindContact(ctx, "tenant-1", "contact-1"); got != nil {
		t.Errorf("sin textos devolvió %v, quería nil", got)
	}

	reminder := runtimehelpertest.NewDepositReminder("recuerda la seña", "vence hoy")
	first := reminder.RemindContact(ctx, "tenant-1", "contact-1")
	if !slices.Equal(first, []string{"recuerda la seña", "vence hoy"}) {
		t.Fatalf("RemindContact = %v", first)
	}
	first[0] = "tocado por fuera"
	if again := reminder.RemindContact(ctx, "tenant-1", "contact-2"); !slices.Equal(again, []string{"recuerda la seña", "vence hoy"}) {
		t.Errorf("el segundo toque devolvió %v: el primero entregó sus textos, no una copia", again)
	}
	reminder.SetTexts()
	if got := reminder.RemindContact(ctx, "tenant-2", "contact-3"); got != nil {
		t.Errorf("tras SetTexts() devolvió %v, quería nil", got)
	}

	want := []runtimehelpertest.ReminderCall{
		{TenantID: "tenant-1", ContactID: "contact-1"}, {TenantID: "tenant-1", ContactID: "contact-2"},
		{TenantID: "tenant-2", ContactID: "contact-3"},
	}
	if got := reminder.Calls(); !slices.Equal(got, want) {
		t.Errorf("Calls = %v, quería %v", got, want)
	}
}
