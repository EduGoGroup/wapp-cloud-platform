//go:build pendiente

package lease_test

// Las reglas de revocación de lease.Manager (R-L2…R-L6): es el kill-switch anti-clon, la mitad
// servidora de la doble llave (ADR-0007). Las ayudas (spyRepo, newManager, requireLive,
// requireRevocation…) están en lease_test.go.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/lease"
)

// issueFunc es una de las dos emisiones (IssueInitial o Renew): las guardas de revocación son
// las mismas en las dos, y cada regla se prueba contra ambas.
type issueFunc func(ctx context.Context, mgr *lease.Manager, tenantID, edgeID string) (*cloudlinkv1.LeaseUpdate, error)

func issuers() map[string]issueFunc {
	return map[string]issueFunc{
		"IssueInitial": func(ctx context.Context, mgr *lease.Manager, tenantID, edgeID string) (*cloudlinkv1.LeaseUpdate, error) {
			return mgr.IssueInitial(ctx, tenantID, edgeID)
		},
		"Renew": func(ctx context.Context, mgr *lease.Manager, tenantID, edgeID string) (*cloudlinkv1.LeaseUpdate, error) {
			return mgr.Renew(ctx, tenantID, edgeID, 7)
		},
	}
}

// TestRevoke_PersistsStickyRevocation: Revoke entrega la revocación firmada y deja la fila
// revocada, conservando el counter, con la expiración del LeaseUpdate.
func TestRevoke_PersistsStickyRevocation(t *testing.T) {
	mgr, repo := newManager(t)
	ctx := context.Background()
	if _, err := mgr.Renew(ctx, tenantOne, edgeOne, 4); err != nil {
		t.Fatalf("Renew: error inesperado %v", err)
	}
	lu, err := mgr.Revoke(ctx, tenantOne, edgeOne)
	if err != nil {
		t.Fatalf("Revoke: error inesperado %v", err)
	}
	requireRevocation(t, mgr, lu)
	st := mustState(t, repo, tenantOne, edgeOne)
	if !st.Revoked {
		t.Error("tras Revoke la fila no quedó revocada")
	}
	if st.Counter != 5 {
		t.Errorf("Revoke cambió el counter a %d, quería que conservara el 5", st.Counter)
	}
	if want := time.Unix(lu.GetExpiresUnix(), 0).UTC(); !st.ExpiresAt.Equal(want) {
		t.Errorf("expires_at persistido = %v, quería %v (el de la revocación firmada)", st.ExpiresAt, want)
	}
}

// TestRevoke_ThenIssue_StaysRevoked: R-L2 (REQ-055.3). Tras revocar, ni IssueInitial ni Renew
// vuelven a emitir un lease vigente: devuelven la revocación y no escriben NADA.
func TestRevoke_ThenIssue_StaysRevoked(t *testing.T) {
	for name, issue := range issuers() {
		t.Run(name, func(t *testing.T) {
			mgr, repo := newManager(t)
			ctx := context.Background()
			if _, err := mgr.IssueInitial(ctx, tenantOne, edgeOne); err != nil {
				t.Fatalf("IssueInitial: error inesperado %v", err)
			}
			if _, err := mgr.Revoke(ctx, tenantOne, edgeOne); err != nil {
				t.Fatalf("Revoke: error inesperado %v", err)
			}
			before := mustState(t, repo, tenantOne, edgeOne)
			writesBefore := repo.writes()

			lu, err := issue(ctx, mgr, tenantOne, edgeOne)
			if err != nil {
				t.Fatalf("emitir sobre un Edge revocado: error inesperado %v", err)
			}
			requireRevocation(t, mgr, lu)
			if got := repo.writes(); got != writesBefore {
				t.Errorf("emitir sobre un Edge revocado hizo %d escrituras, quería 0", got-writesBefore)
			}
			if after := mustState(t, repo, tenantOne, edgeOne); after != before {
				t.Errorf("la fila del Edge revocado cambió: %+v → %+v", before, after)
			}
		})
	}
}

// TestRevoke_SurvivesANewManager: la revocación vive en el almacén, no en el Manager: otro
// Manager sobre el mismo repositorio (un reinicio) sigue sin emitir. Con Postgres de verdad lo
// prueba el proceso de F9 (REQ-055.4).
func TestRevoke_SurvivesANewManager(t *testing.T) {
	mgr, repo := newManager(t)
	ctx := context.Background()
	if _, err := mgr.Revoke(ctx, tenantOne, edgeOne); err != nil {
		t.Fatalf("Revoke: error inesperado %v", err)
	}
	restarted, err := lease.NewManager(newKey(t), repo)
	if err != nil {
		t.Fatalf("NewManager: error inesperado %v", err)
	}
	lu, err := restarted.IssueInitial(ctx, tenantOne, edgeOne)
	if err != nil {
		t.Fatalf("IssueInitial tras el reinicio: error inesperado %v", err)
	}
	requireRevocation(t, restarted, lu)
}

// TestRevokedTenant_WinsOverNeverSeenEdge: R-L3 (T3.2). Una instalación NUEVA de un tenant
// cortado nace revocada: no hace falta que exista fila en leases, y no se crea.
func TestRevokedTenant_WinsOverNeverSeenEdge(t *testing.T) {
	for name, issue := range issuers() {
		t.Run(name, func(t *testing.T) {
			mgr, repo := newManager(t)
			ctx := context.Background()
			if err := mgr.RevokeTenant(ctx, tenantOne); err != nil {
				t.Fatalf("RevokeTenant: error inesperado %v", err)
			}
			lu, err := issue(ctx, mgr, tenantOne, "edge-nunca-visto")
			if err != nil {
				t.Fatalf("emitir para un tenant revocado: error inesperado %v", err)
			}
			requireRevocation(t, mgr, lu)
			requireNoState(t, repo, tenantOne, "edge-nunca-visto")
			if repo.upserts.Load() != 0 || repo.markRevokeds.Load() != 0 {
				t.Errorf("escrituras por Edge: %d Upsert y %d MarkRevoked, quería 0 y 0",
					repo.upserts.Load(), repo.markRevokeds.Load())
			}

			// El corte es del tenant: otro tenant, con el mismo Edge, sigue emitiendo vigente.
			other, err := issue(ctx, mgr, "tenant-2", "edge-nunca-visto")
			if err != nil {
				t.Fatalf("emitir para otro tenant: error inesperado %v", err)
			}
			requireLive(t, mgr, other)
		})
	}
}

// TestRevokedTenant_WinsOverLiveEdge: el tenant cortado gana también sobre un Edge que ya tenía
// lease vigente, y su fila NO se marca (R-L4: los dos sujetos de corte son independientes).
func TestRevokedTenant_WinsOverLiveEdge(t *testing.T) {
	mgr, repo := newManager(t)
	ctx := context.Background()
	if _, err := mgr.IssueInitial(ctx, tenantOne, edgeOne); err != nil {
		t.Fatalf("IssueInitial: error inesperado %v", err)
	}
	before := mustState(t, repo, tenantOne, edgeOne)
	if err := mgr.RevokeTenant(ctx, tenantOne); err != nil {
		t.Fatalf("RevokeTenant: error inesperado %v", err)
	}
	if repo.tenantMarks.Load() != 1 || repo.markRevokeds.Load() != 0 {
		t.Errorf("RevokeTenant: %d MarkTenantRevoked y %d MarkRevoked, quería 1 y 0",
			repo.tenantMarks.Load(), repo.markRevokeds.Load())
	}
	lu, err := mgr.Renew(ctx, tenantOne, edgeOne, 1)
	if err != nil {
		t.Fatalf("Renew con el tenant cortado: error inesperado %v", err)
	}
	requireRevocation(t, mgr, lu)
	if after := mustState(t, repo, tenantOne, edgeOne); after != before {
		t.Errorf("cortar el tenant tocó la fila del Edge: %+v → %+v", before, after)
	}
}

// TestRestoreTenant_UnblocksFutureIssue: R-L4. Restaurar reactiva TODAS las instalaciones del
// tenant de una vez… salvo la que estaba revocada individualmente, que sigue cortada.
func TestRestoreTenant_UnblocksFutureIssue(t *testing.T) {
	mgr, repo := newManager(t)
	ctx := context.Background()
	if _, err := mgr.IssueInitial(ctx, tenantOne, edgeOne); err != nil {
		t.Fatalf("IssueInitial: error inesperado %v", err)
	}
	if _, err := mgr.Revoke(ctx, tenantOne, "edge-clon"); err != nil {
		t.Fatalf("Revoke: error inesperado %v", err)
	}
	if err := mgr.RevokeTenant(ctx, tenantOne); err != nil {
		t.Fatalf("RevokeTenant: error inesperado %v", err)
	}
	if err := mgr.RestoreTenant(ctx, tenantOne); err != nil {
		t.Fatalf("RestoreTenant: error inesperado %v", err)
	}
	if repo.tenantResets.Load() != 1 || repo.upserts.Load() != 1 {
		t.Errorf("RestoreTenant: %d RestoreTenant y %d Upsert en total, quería 1 y 1 (no re-emite nada)",
			repo.tenantResets.Load(), repo.upserts.Load())
	}

	for _, edge := range []string{edgeOne, "edge-nuevo"} {
		lu, err := mgr.Renew(ctx, tenantOne, edge, 1)
		if err != nil {
			t.Fatalf("Renew(%s) tras restaurar: error inesperado %v", edge, err)
		}
		requireLive(t, mgr, lu)
	}
	lu, err := mgr.IssueInitial(ctx, tenantOne, "edge-clon")
	if err != nil {
		t.Fatalf("IssueInitial del Edge revocado: error inesperado %v", err)
	}
	requireRevocation(t, mgr, lu)
}

// TestSignTenantRevocation_SignsWithoutPersisting: R-L4. Firma la notificación de revocación
// para un Edge y NO toca el almacén: ni lee ni escribe, ni marca al Edge.
func TestSignTenantRevocation_SignsWithoutPersisting(t *testing.T) {
	mgr, repo := newManager(t)
	lu, err := mgr.SignTenantRevocation(edgeOne, tenantOne)
	if err != nil {
		t.Fatalf("SignTenantRevocation: error inesperado %v", err)
	}
	requireRevocation(t, mgr, lu)
	if repo.writes() != 0 || repo.gets.Load() != 0 || repo.tenantReads.Load() != 0 {
		t.Errorf("SignTenantRevocation tocó el almacén: %d escrituras, %d Get, %d TenantRevoked",
			repo.writes(), repo.gets.Load(), repo.tenantReads.Load())
	}
	requireNoState(t, repo, tenantOne, edgeOne)

	// Como no persistió nada, el Edge sigue pudiendo recibir un lease vigente.
	live, err := mgr.IssueInitial(context.Background(), tenantOne, edgeOne)
	if err != nil {
		t.Fatalf("IssueInitial: error inesperado %v", err)
	}
	requireLive(t, mgr, live)
}

// TestIssue_ReadFailure_FailsClosed: R-L5 (D-055.1). Si no se puede leer el estado previo —el
// del tenant o el del Edge— NO se emite un lease vigente en su ausencia: error, ningún
// LeaseUpdate y ninguna escritura.
func TestIssue_ReadFailure_FailsClosed(t *testing.T) {
	cause := errors.New("base caída")
	failures := []struct {
		name   string
		breakRepo func(r *spyRepo)
		prefix string
	}{
		{"tenant read fails", func(r *spyRepo) { r.errTenantRevoked = cause }, "lease: decidir corte por tenant: "},
		{"edge read fails", func(r *spyRepo) { r.errGet = cause }, "lease: consultar estado previo: "},
	}
	for _, f := range failures {
		for name, issue := range issuers() {
			t.Run(f.name+"/"+name, func(t *testing.T) {
				mgr, repo := newManager(t)
				f.breakRepo(repo)
				lu, err := issue(context.Background(), mgr, tenantOne, edgeOne)
				if lu != nil {
					t.Errorf("con la lectura caída se entregó un LeaseUpdate (revoked=%v): fail-closed exige ninguno", lu.GetRevoked())
				}
				if !errors.Is(err, cause) {
					t.Fatalf("error = %v, quería uno que envuelva la causa", err)
				}
				if !strings.HasPrefix(err.Error(), f.prefix) {
					t.Errorf("error = %q, quería el prefijo %q", err, f.prefix)
				}
				if repo.writes() != 0 {
					t.Errorf("con la lectura caída hubo %d escrituras, quería 0", repo.writes())
				}
			})
		}
	}
}

// TestRevoke_DoesNotDependOnCounterOrState: R-L6. El kill-switch se dispara siempre: sobre un
// Edge nunca visto (sin counter), con cualquier counter, repetido, y con las lecturas caídas
// (Revoke no lee: no hay estado previo que pueda impedirlo).
func TestRevoke_DoesNotDependOnCounterOrState(t *testing.T) {
	t.Run("never seen edge", func(t *testing.T) {
		mgr, repo := newManager(t)
		lu, err := mgr.Revoke(context.Background(), tenantOne, edgeOne)
		if err != nil {
			t.Fatalf("Revoke de un Edge nunca visto: error inesperado %v", err)
		}
		requireRevocation(t, mgr, lu)
		if st := mustState(t, repo, tenantOne, edgeOne); !st.Revoked || st.Counter != 0 {
			t.Errorf("fila = revoked %v, counter %d; quería true y 0", st.Revoked, st.Counter)
		}
	})
	t.Run("reads down", func(t *testing.T) {
		mgr, repo := newManager(t)
		repo.errGet = errors.New("lectura caída")
		repo.errTenantRevoked = errors.New("lectura caída")
		lu, err := mgr.Revoke(context.Background(), tenantOne, edgeOne)
		if err != nil {
			t.Fatalf("Revoke con las lecturas caídas: error inesperado %v", err)
		}
		requireRevocation(t, mgr, lu)
		if repo.gets.Load() != 0 || repo.tenantReads.Load() != 0 {
			t.Errorf("Revoke leyó el estado (%d Get, %d TenantRevoked): no debe depender de él",
				repo.gets.Load(), repo.tenantReads.Load())
		}
		if repo.markRevokeds.Load() != 1 {
			t.Errorf("Revoke hizo %d MarkRevoked, quería 1", repo.markRevokeds.Load())
		}
	})
	t.Run("any counter and repeated", func(t *testing.T) {
		mgr, repo := newManager(t)
		ctx := context.Background()
		if _, err := mgr.Renew(ctx, tenantOne, edgeOne, 1_000_000); err != nil {
			t.Fatalf("Renew: error inesperado %v", err)
		}
		for range 2 {
			lu, err := mgr.Revoke(ctx, tenantOne, edgeOne)
			if err != nil {
				t.Fatalf("Revoke: error inesperado %v", err)
			}
			requireRevocation(t, mgr, lu)
		}
		if st := mustState(t, repo, tenantOne, edgeOne); !st.Revoked || st.Counter != 1_000_001 {
			t.Errorf("fila = revoked %v, counter %d; quería true y 1000001", st.Revoked, st.Counter)
		}
	})
	t.Run("revoked tenant", func(t *testing.T) {
		mgr, repo := newManager(t)
		ctx := context.Background()
		if err := mgr.RevokeTenant(ctx, tenantOne); err != nil {
			t.Fatalf("RevokeTenant: error inesperado %v", err)
		}
		lu, err := mgr.Revoke(ctx, tenantOne, edgeOne)
		if err != nil {
			t.Fatalf("Revoke: error inesperado %v", err)
		}
		requireRevocation(t, mgr, lu)
		if st := mustState(t, repo, tenantOne, edgeOne); !st.Revoked {
			t.Error("la fila del Edge no quedó revocada")
		}
	})
}

// TestWriteFailures_AreWrappedAndReturned: un fallo al persistir una revocación o un corte por
// tenant se devuelve envuelto, con su texto, y Revoke no entrega un LeaseUpdate que no persistió.
func TestWriteFailures_AreWrappedAndReturned(t *testing.T) {
	cause := errors.New("base caída")
	ctx := context.Background()

	mgr, repo := newManager(t)
	repo.errMarkRevoked = cause
	lu, err := mgr.Revoke(ctx, tenantOne, edgeOne)
	if lu != nil || !errors.Is(err, cause) || !strings.HasPrefix(err.Error(), "lease: persistir revocación: ") {
		t.Errorf("Revoke con MarkRevoked caído = (%v, %v), quería (nil, \"lease: persistir revocación: …\")", lu, err)
	}

	mgr, repo = newManager(t)
	repo.errMarkTenant = cause
	if err := mgr.RevokeTenant(ctx, tenantOne); !errors.Is(err, cause) || !strings.HasPrefix(err.Error(), "lease: revocar tenant: ") {
		t.Errorf("RevokeTenant con el almacén caído = %v, quería \"lease: revocar tenant: …\"", err)
	}

	mgr, repo = newManager(t)
	repo.errRestoreTenant = cause
	if err := mgr.RestoreTenant(ctx, tenantOne); !errors.Is(err, cause) || !strings.HasPrefix(err.Error(), "lease: restaurar tenant: ") {
		t.Errorf("RestoreTenant con el almacén caído = %v, quería \"lease: restaurar tenant: …\"", err)
	}
}
