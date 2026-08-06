package intakes_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// señaEnBD escribe la config de la seña del tenant tal cual lo hará la consola.
// Limpia su fila al terminar: tenant_settings no la borra seedPG.
func señaEnBD(t *testing.T, db *sql.DB, tenant, template string, dueDays int) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO public.tenant_settings (tenant_id, deposit_template, deposit_due_days)
		VALUES ($1, $2, $3)
		ON CONFLICT (tenant_id) DO UPDATE
		   SET deposit_template = EXCLUDED.deposit_template,
		       deposit_due_days = EXCLUDED.deposit_due_days
	`, tenant, template, dueDays); err != nil {
		t.Fatalf("sembrando la config de la seña: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(),
			`DELETE FROM public.tenant_settings WHERE tenant_id = $1`, tenant); err != nil {
			t.Logf("limpiando tenant_settings: %v", err)
		}
	})
}

// TestNotifySettingsLeeLaConfiguraciónDelTenant: la consulta de T4.2 contra las
// columnas REALES de la 0045. Es lo único de esta tarea que no puede probar un
// doble: que `deposit_template` y `deposit_due_days` se llamen así y sean legibles
// del mismo sitio que shipping_zones.
func TestNotifySettingsLeeLaConfiguraciónDelTenant(t *testing.T) {
	db := openTestDB(t)
	tenant := tenantA
	señaEnBD(t, db, tenant, "Abona {total} a la cuenta 001-2 en {plazo} días.", 7)

	cfg, err := intakes.NewPostgres(db).NotifySettings(context.Background(), tenant)
	if err != nil {
		t.Fatalf("NotifySettings: %v", err)
	}
	if cfg.DepositTemplate != "Abona {total} a la cuenta 001-2 en {plazo} días." {
		t.Fatalf("plantilla = %q", cfg.DepositTemplate)
	}
	if cfg.DepositDueDays != 7 {
		t.Fatalf("plazo = %d, quiero 7", cfg.DepositDueDays)
	}
}

// TestNotifySettingsTenantSinFilaEsElEstadoDeArranque: un tenant que nunca
// configuró nada NO es un error. Devuelve lo que es cierto —sin plantilla, plazo
// por defecto—, y sin plantilla el notificador no manda nada.
func TestNotifySettingsTenantSinFilaEsElEstadoDeArranque(t *testing.T) {
	db := openTestDB(t)

	cfg, err := intakes.NewPostgres(db).NotifySettings(context.Background(), tenantB)
	if err != nil {
		t.Fatalf("NotifySettings de un tenant sin fila: %v", err)
	}
	if cfg.DepositTemplate != "" {
		t.Fatalf("plantilla = %q, quiero vacía", cfg.DepositTemplate)
	}
	if cfg.DepositDueDays != intakes.DefaultDepositDueDays {
		t.Fatalf("plazo = %d, quiero el default %d", cfg.DepositDueDays, intakes.DefaultDepositDueDays)
	}
}
