package store

import (
	"context"
	"testing"
	"time"
)

// Aserciones de compilación de lo que promete repository_postgres_settings.go.
var (
	_ func(*PostgresRepository, context.Context, string) (TenantSettings, error)            = (*PostgresRepository).GetTenantSettings
	_ func(*PostgresRepository, context.Context, Key, time.Time) (WelcomeMark, error)       = (*PostgresRepository).TouchContact
	_ func(*PostgresRepository, context.Context, Key, WelcomeMark, time.Time) (bool, error) = (*PostgresRepository).MarkWelcomed
)

// pgSettingsRow es una fila de tenant_settings con las diez columnas que GetTenantSettings lee, en
// su orden, y diez valores distintos entre sí.
func pgSettingsRow(buyerFields string) pgReply {
	return pgOne(int64(8), int64(11), int64(12), []byte(buyerFields), int64(13), int64(14), int64(15), int64(16),
		"Gracias, ya lo estamos viendo.", int64(17))
}

// pgSettings es lo que pgSettingsRow da una vez leído, sin el checklist.
func pgSettings() TenantSettings {
	return TenantSettings{
		TenantID: pgTenant, PageSize: 8, OrderTTL: 11 * time.Second, ConversationTTL: 12 * time.Second,
		EventInactivityTTL: 13 * time.Second, EventHistoryTTL: 14 * time.Second,
		AggregationWindow: 15 * time.Second, AggregationMax: 16 * time.Second,
		WelcomeText: "Gracias, ya lo estamos viendo.", WelcomeSilence: 17 * time.Second,
	}
}

// requireSettings compara una configuración leída con la esperada: las nueve claves escalares y el
// checklist, campo a campo.
func requireSettings(t *testing.T, what string, got, want TenantSettings) {
	t.Helper()
	requireEqual(t, what+": campos del checklist", len(got.BuyerFields), len(want.BuyerFields))
	for i := range min(len(got.BuyerFields), len(want.BuyerFields)) {
		requireEqual(t, what+": campo del checklist", got.BuyerFields[i], want.BuyerFields[i])
	}
	got.BuyerFields, want.BuyerFields = nil, nil
	if fg, fw := got, want; fg.TenantID != fw.TenantID || fg.PageSize != fw.PageSize || fg.OrderTTL != fw.OrderTTL ||
		fg.ConversationTTL != fw.ConversationTTL || fg.EventInactivityTTL != fw.EventInactivityTTL ||
		fg.EventHistoryTTL != fw.EventHistoryTTL || fg.AggregationWindow != fw.AggregationWindow ||
		fg.AggregationMax != fw.AggregationMax || fg.WelcomeText != fw.WelcomeText || fg.WelcomeSilence != fw.WelcomeSilence {
		t.Errorf("%s = %+v, quería %+v", what, got, want)
	}
}

// TestPostgres_GetTenantSettings_MapsTheRow: cada columna sale por su campo y los segundos como
// duración; el checklist, en su orden; una sentencia suelta acotada por el tenant.
func TestPostgres_GetTenantSettings_MapsTheRow(t *testing.T) {
	h := newFakeRepository(t,
		pgSettingsRow(`[{"key":"rut","label":"RUT","required":true},{"key":"ref","label":"","required":false}]`))
	got, err := h.repo.GetTenantSettings(t.Context(), pgTenant)
	requireNoError(t, "GetTenantSettings", err)
	want := pgSettings()
	want.BuyerFields = []BuyerField{{Key: "rut", Label: "RUT", Required: true}, {Key: "ref"}}
	requireSettings(t, "GetTenantSettings", got, want)
	requireArgs(t, "GetTenantSettings", loose(t, h.fake, 1)[0], pgTenant)
}

// TestPostgres_GetTenantSettings_NoRowZerosAndFailure: sin fila salen los valores de plataforma;
// una fila a cero sale a cero —un 0 es un override, no un hueco—; y el fallo de la base va
// envuelto, con la configuración cero.
func TestPostgres_GetTenantSettings_NoRowZerosAndFailure(t *testing.T) {
	h := newFakeRepository(t, pgReply{},
		pgOne(int64(0), int64(0), int64(0), []byte(`[]`), int64(0), int64(0), int64(0), int64(0), "", int64(0)),
		pgFails())

	got, err := h.repo.GetTenantSettings(t.Context(), pgTenant)
	requireNoError(t, "GetTenantSettings sin fila", err)
	requireSettings(t, "GetTenantSettings sin fila", got, DefaultTenantSettings(pgTenant))
	requireEqual(t, "ConversationTTL sin fila", got.ConversationTTL, DefaultConversationTTL)

	got, err = h.repo.GetTenantSettings(t.Context(), pgTenant)
	requireNoError(t, "GetTenantSettings con la fila a cero", err)
	requireSettings(t, "GetTenantSettings con la fila a cero", got, TenantSettings{TenantID: pgTenant})

	got, err = h.repo.GetTenantSettings(t.Context(), pgTenant)
	requirePgWrapped(t, err, "store: leer config de tenant: ", errPgBoom)
	requireSettings(t, "GetTenantSettings junto al error", got, TenantSettings{})
	loose(t, h.fake, 3)
}

// TestPostgres_GetTenantSettings_BuyerFieldsAreTolerant: un checklist vacío, ilegible o de otra
// forma NO rompe la lectura: sale vacío y el resto de la fila se devuelve igual.
func TestPostgres_GetTenantSettings_BuyerFieldsAreTolerant(t *testing.T) {
	for _, raw := range []string{``, `[]`, `["rut","direccion"]`, `{"key":"rut"}`, `{rota`} {
		t.Run(raw, func(t *testing.T) {
			h := newFakeRepository(t, pgSettingsRow(raw))
			got, err := h.repo.GetTenantSettings(t.Context(), pgTenant)
			requireNoError(t, "GetTenantSettings", err)
			requireSettings(t, "GetTenantSettings", got, pgSettings())
			loose(t, h.fake, 1)
		})
	}
}

// TestPostgres_TouchContact_OneStatementReturnsThePrevious: UNA sentencia suelta con la clave y el
// instante del llamante. Devuelve la fila que la base contesta (la previa); sin fila —contacto
// nuevo— es la marca cero sin error, y welcomed_at NULL es el instante cero.
func TestPostgres_TouchContact_OneStatementReturnsThePrevious(t *testing.T) {
	h := newFakeRepository(t, pgReply{}, pgOne(pgAt, nil), pgOne(pgAt, pgAt.Add(time.Minute)), pgFails())
	now := pgAt.Add(time.Hour)

	for _, want := range []WelcomeMark{{}, {LastIncomingAt: pgAt}, {LastIncomingAt: pgAt, WelcomedAt: pgAt.Add(time.Minute)}} {
		got, err := h.repo.TouchContact(t.Context(), pgKey, now)
		requireNoError(t, "TouchContact", err)
		requireEqual(t, "estado previo", got, want)
	}
	got, err := h.repo.TouchContact(t.Context(), pgKey, now)
	requirePgWrapped(t, err, "store: registrar actividad del contacto (bienvenida): ", errPgBoom)
	requireEqual(t, "estado previo junto al error", got, WelcomeMark{})

	for _, stmt := range loose(t, h.fake, 4) {
		requireArgs(t, "TouchContact", stmt, pgTenant, "sess-1", pgContact, now)
	}
}

// TestPostgres_MarkWelcomed_CompareAndSet: UNA sentencia suelta con cinco argumentos. El testigo
// sin bienvenida viaja como NULL —no como la fecha cero, que no casaría con nada— y el que la
// tiene, como su instante. Devuelve true solo si la sentencia afectó a una fila.
func TestPostgres_MarkWelcomed_CompareAndSet(t *testing.T) {
	h := newFakeRepository(t, pgReply{}, pgReply{noneAffected: true}, pgFails())
	now := pgAt.Add(time.Hour)

	ok, err := h.repo.MarkWelcomed(t.Context(), pgKey, WelcomeMark{LastIncomingAt: pgAt}, now)
	requireNoError(t, "MarkWelcomed que afecta a una fila", err)
	requireEqual(t, "MarkWelcomed que afecta a una fila", ok, true)

	ok, err = h.repo.MarkWelcomed(t.Context(), pgKey, WelcomeMark{WelcomedAt: pgAt}, now)
	requireNoError(t, "MarkWelcomed que no afecta a ninguna fila", err)
	requireEqual(t, "MarkWelcomed que no afecta a ninguna fila", ok, false)

	ok, err = h.repo.MarkWelcomed(t.Context(), pgKey, WelcomeMark{}, now)
	requirePgWrapped(t, err, "store: marcar bienvenida entregada: ", errPgBoom)
	requireEqual(t, "MarkWelcomed junto al error", ok, false)

	stmts := loose(t, h.fake, 3)
	requireEqual(t, "clase de la sentencia", stmts[0].kind, pgExec)
	requireArgs(t, "testigo sin bienvenida", stmts[0], pgTenant, "sess-1", pgContact, now, nil)
	requireArgs(t, "testigo con bienvenida", stmts[1], pgTenant, "sess-1", pgContact, now, pgAt)
}
