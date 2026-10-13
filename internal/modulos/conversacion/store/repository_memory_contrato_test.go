package store_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store/storehelpertest"
)

// La suite de contrato contra el gemelo en memoria. Va en fichero propio porque ejercita los
// CUATRO trozos de repository_memory a la vez: llevó la etiqueta `pendiente` hasta que el último
// (repository_memory_settings.go) estuvo en verde.

// ofTenant filtra por tenant lo que un mirador del gemelo devuelve de todos.
func ofTenant[T any](all []T, tenant string, tenantOf func(T) string) []T {
	out := make([]T, 0, len(all))
	for _, v := range all {
		if tenantOf(v) == tenant {
			out = append(out, v)
		}
	}
	return out
}

// TestMemoryRepository_Contrato corre la suite de la persistencia del motor de flujos contra
// MemoryRepository, sin BD y sin reloj real: cada caso monta un repositorio nuevo con su reloj, y
// Advance lo adelanta un segundo. Los observadores son los miradores del gemelo.
func TestMemoryRepository_Contrato(t *testing.T) {
	storehelpertest.Contrato(t, func(*testing.T) storehelpertest.Montaje {
		repo, clock := newMemoryRepository()
		return storehelpertest.Montaje{
			Store:    repo,
			TenantA:  uuid.NewString(),
			TenantB:  uuid.NewString(),
			NewEvent: func(*testing.T, string) string { return uuid.NewString() },
			SetSettings: func(_ *testing.T, s storehelpertest.Settings) {
				repo.SetTenantSettings(s)
			},
			FlowEvents: func(_ *testing.T, tenantID string) []storehelpertest.FlowEvent {
				return ofTenant(repo.FlowEvents(), tenantID, func(e store.FlowEvent) string { return e.TenantID })
			},
			SurveyResults: func(_ *testing.T, tenantID string) []storehelpertest.SurveyResult {
				return ofTenant(repo.SurveyResults(), tenantID, func(r store.SurveyResult) string { return r.TenantID })
			},
			ContentVersions: func(_ *testing.T, tenantID, ref string) []storehelpertest.ContentVersion {
				return repo.TenantContentVersions(tenantID, ref)
			},
			Intakes: func(_ *testing.T, tenantID string) []storehelpertest.Intake {
				return ofTenant(repo.Intakes(), tenantID, func(in store.Intake) string { return in.TenantID })
			},
			IntakeItems: func(_ *testing.T, intakeID string) []storehelpertest.IntakeItem {
				return repo.IntakeItems(intakeID)
			},
			Welcome: func(_ *testing.T, tenantID, sessionID, contactID string) storehelpertest.WelcomeMark {
				return repo.Welcome(store.Key{TenantID: tenantID, SessionID: sessionID, ContactID: contactID})
			},
			Now:     func(*testing.T) time.Time { return clock.Now() },
			Advance: func(*testing.T) { clock.Advance(time.Second) },
		}
	})
}
