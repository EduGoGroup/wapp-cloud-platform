//go:build pendiente

package fleet

// Los tests de fichero de fleet.PostgresRepository.SaveHealth: una sentencia y diecinueve
// argumentos. Aquí se afirma qué viaja al driver; que degraded_since se fije al entrar y se
// limpie al salir lo hace el CASE de la sentencia y lo prueba la suite contra Postgres (F3-05).

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"
)

// La sentencia, byte a byte y escrita a mano (ver repository_postgres_test.go). No toca `state`
// (el link CloudLink) ni el perfil; el bloque del worker ($13-$19) se escribe SIEMPRE.
const sqlSaveHealth = "\n" +
	"\t\tUPDATE public.fleet_sessions\n" +
	"\t\tSET whatsapp_state           = $4,\n" +
	"\t\t    degraded_reason          = $5,\n" +
	"\t\t    last_event_age_s         = $6,\n" +
	"\t\t    dek_load_duration_ms     = $7,\n" +
	"\t\t    intent_circuit           = $8,\n" +
	"\t\t    outbox_depth             = $9,\n" +
	"\t\t    binary_version           = $10,\n" +
	"\t\t    uptime_s                 = $11,\n" +
	"\t\t    last_health_at           = now(),\n" +
	"\t\t    degraded_since           = CASE WHEN $12 THEN COALESCE(degraded_since, now()) ELSE NULL END,\n" +
	"\t\t    worker_taskset           = $13,\n" +
	"\t\t    intent_p50_ms            = $14,\n" +
	"\t\t    intent_omitted_by_reason = $15,\n" +
	"\t\t    stuck_heads              = $16,\n" +
	"\t\t    stuck_head_polls         = $17,\n" +
	"\t\t    failed_seal_dispatch     = $18,\n" +
	"\t\t    failed_seal_budget       = $19,\n" +
	"\t\t    updated_at               = now()\n" +
	"\t\tWHERE tenant_id = $1 AND edge_id = $2 AND session_id = $3\n" +
	"\t"

// saveHealth llama a SaveHealth y afirma la sentencia exacta con esos argumentos tras $1-$3.
func saveHealth(t *testing.T, h HealthSnapshot, want ...driver.Value) {
	t.Helper()
	f := newFixture(t)
	if err := f.repo.SaveHealth(context.Background(), pgTenant, pgEdge, pgSession, h); err != nil {
		t.Fatalf("SaveHealth: error inesperado %v (cero filas afectadas no es un error)", err)
	}
	args := append([]driver.Value{pgTenant, pgEdge, pgSession}, want...)
	requireStatements(t, f.fake, statement{sqlSaveHealth, args})
}

// TestPostgresRepository_SaveHealth_AllArguments: cada campo del snapshot en su parámetro, con
// $12 = h.Degraded() y el desglose de motivos como JSON.
func TestPostgresRepository_SaveHealth_AllArguments(t *testing.T) {
	h := HealthSnapshot{
		WhatsappState: "degraded", DegradedReason: "dek_load_timeout",
		LastEventAgeS: 11, DekLoadDurationMs: 12, IntentCircuit: "open",
		OutboxDepth: 13, BinaryVersion: "v1.2.3", UptimeS: 14,
		WorkerTaskset: "solapada", IntentP50Ms: int64p(15),
		IntentOmittedByReason: map[string]int64{"fastlane": 5, "breaker": 2},
		StuckHeads:            int64p(16), StuckHeadPolls: int64p(17),
		FailedSealDispatch: int64p(18), FailedSealBudget: int64p(19),
	}
	saveHealth(t, h,
		"degraded", "dek_load_timeout", int64(11), int64(12), "open", int64(13), "v1.2.3", int64(14),
		true,
		"solapada", int64(15), []byte(`{"breaker":2,"fastlane":5}`),
		int64(16), int64(17), int64(18), int64(19),
	)
}

// TestPostgresRepository_SaveHealth_UnknownTravelsAsNull: el snapshot cero (un Edge que aún no
// sabe su salud) escribe $12 = false y el bloque del worker ENTERO en NULL —también el texto
// vacío y el mapa vacío—: «no lo sé» no es cero, y borra lo que hubiera.
func TestPostgresRepository_SaveHealth_UnknownTravelsAsNull(t *testing.T) {
	for name, h := range map[string]HealthSnapshot{
		"zero snapshot": {},
		"empty map":     {IntentOmittedByReason: map[string]int64{}},
	} {
		t.Run(name, func(t *testing.T) {
			saveHealth(t, h,
				"", "", int64(0), int64(0), "", int64(0), "", int64(0),
				false,
				nil, nil, nil,
				nil, nil, nil, nil,
			)
		})
	}
}

// TestPostgresRepository_SaveHealth_MeasuredZeroIsNotNull: un 0 MEDIDO viaja como 0, no como NULL.
func TestPostgresRepository_SaveHealth_MeasuredZeroIsNotNull(t *testing.T) {
	h := HealthSnapshot{
		WhatsappState: "connected",
		IntentP50Ms:   int64p(0), StuckHeads: int64p(0), StuckHeadPolls: int64p(0),
		FailedSealDispatch: int64p(0), FailedSealBudget: int64p(0),
	}
	saveHealth(t, h,
		"connected", "", int64(0), int64(0), "", int64(0), "", int64(0),
		false,
		nil, int64(0), nil,
		int64(0), int64(0), int64(0), int64(0),
	)
}

// TestPostgresRepository_SaveHealth_DegradedFlag: $12 es h.Degraded(), que es lo que gobierna
// degraded_since en el CASE de la sentencia.
func TestPostgresRepository_SaveHealth_DegradedFlag(t *testing.T) {
	cases := []HealthSnapshot{
		{WhatsappState: "connected"},
		{WhatsappState: "connecting"},
		{WhatsappState: "degraded"},
		{WhatsappState: "dead"},
		{WhatsappState: "connected", DegradedReason: "dek_load_timeout"},
	}
	for _, h := range cases {
		f := newFixture(t)
		if err := f.repo.SaveHealth(context.Background(), pgTenant, pgEdge, pgSession, h); err != nil {
			t.Fatalf("SaveHealth(%+v): error inesperado %v", h, err)
		}
		if got := f.fake.seen()[0].args[11]; got != h.Degraded() {
			t.Errorf("SaveHealth(%q, motivo %q): $12 = %v, quería %v", h.WhatsappState, h.DegradedReason, got, h.Degraded())
		}
	}
}

// TestPostgresRepository_SaveHealth_DriverError: el fallo del driver vuelve envuelto.
func TestPostgresRepository_SaveHealth_DriverError(t *testing.T) {
	f := newFixture(t, reply{err: errBoom})
	err := f.repo.SaveHealth(context.Background(), pgTenant, pgEdge, pgSession, HealthSnapshot{})
	requireWrapped(t, err, errBoom, "fleet: persistir salud: ")
}

// TestPostgresRepository_SaveHealth_LeavesStateAndProfileAlone: la sentencia no escribe `state`
// (es del link CloudLink) ni nombra el perfil.
func TestPostgresRepository_SaveHealth_LeavesStateAndProfileAlone(t *testing.T) {
	for _, column := range []string{" state ", "profile"} {
		if strings.Contains(sqlSaveHealth, column) {
			t.Errorf("la sentencia de SaveHealth nombra %q:\n%s", column, sqlSaveHealth)
		}
	}
}
