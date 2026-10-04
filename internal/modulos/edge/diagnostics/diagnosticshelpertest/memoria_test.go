package diagnosticshelpertest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/diagnostics"
)

// TestMemoria_Contrato corre la suite del puerto contra la Memoria. Lo que el puerto no deja
// hacer —fijar el consentimiento y hacer vencer una solicitud— lo hacen SetConsent y Expire.
func TestMemoria_Contrato(t *testing.T) {
	ContratoStore(t, func(t *testing.T) Montaje {
		t.Helper()
		m := NewMemoria()
		return Montaje{
			Store:   m,
			TenantA: uuid.NewString(),
			TenantB: uuid.NewString(),
			SetConsent: func(_ *testing.T, tenantID string, enabled bool) {
				m.SetConsent(tenantID, enabled)
			},
			Expire: func(t *testing.T, tenantID, commandID string) {
				t.Helper()
				if !m.Expire(tenantID, commandID) {
					t.Fatalf("Expire(%q, %q): el tenant no tiene esa solicitud", tenantID, commandID)
				}
			},
		}
	})
}

// TestMemoria_Expire_OnlyTheOwnersRequest: Expire no toca la solicitud de otro tenant ni una que
// no existe, y lo dice devolviendo false.
func TestMemoria_Expire_OnlyTheOwnersRequest(t *testing.T) {
	ctx := context.Background()
	m := NewMemoria()
	if err := m.CreateRequest(ctx, "tenant-1", "session-1", "cmd-1", "user-1", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("CreateRequest: error inesperado %v", err)
	}
	if m.Expire("tenant-2", "cmd-1") {
		t.Error("Expire desde otro tenant devolvió true")
	}
	if m.Expire("tenant-1", "cmd-unknown") {
		t.Error("Expire de una solicitud que no existe devolvió true")
	}
	if _, err := m.GetBundle(ctx, "tenant-1", "cmd-1"); !errors.Is(err, diagnostics.ErrPending) {
		t.Fatalf("tras dos Expire que no aplican: err = %v, quería ErrPending", err)
	}
	if !m.Expire("tenant-1", "cmd-1") {
		t.Error("Expire del dueño devolvió false")
	}
	if _, err := m.GetBundle(ctx, "tenant-1", "cmd-1"); !errors.Is(err, diagnostics.ErrExpired) {
		t.Errorf("tras Expire: err = %v, quería ErrExpired", err)
	}
}

// TestMemoria_CreateRequest_SameCommandOverwrites fija la diferencia documentada con Postgres:
// repetir un command_id pisa la solicitud anterior en vez de fallar.
func TestMemoria_CreateRequest_SameCommandOverwrites(t *testing.T) {
	ctx := context.Background()
	m := NewMemoria()
	for _, tenant := range []string{"tenant-1", "tenant-2"} {
		if err := m.CreateRequest(ctx, tenant, "session-1", "cmd-1", "user-1", time.Now().Add(time.Hour)); err != nil {
			t.Fatalf("CreateRequest(%q): error inesperado %v", tenant, err)
		}
	}
	if _, err := m.GetBundle(ctx, "tenant-1", "cmd-1"); !errors.Is(err, diagnostics.ErrNotFound) {
		t.Errorf("la solicitud pisada: err = %v, quería ErrNotFound", err)
	}
	if _, err := m.GetBundle(ctx, "tenant-2", "cmd-1"); !errors.Is(err, diagnostics.ErrPending) {
		t.Errorf("la solicitud que pisó: err = %v, quería ErrPending", err)
	}
}
