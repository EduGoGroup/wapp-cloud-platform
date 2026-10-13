package lease_test

// El espía del repositorio que usan los tests de lease.Manager. Va en su fichero para que
// lease_test.go no pase del tamaño de E-13.

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/lease"
)

// spyRepo envuelve un Repository: cuenta las llamadas y puede hacer fallar cada operación. Es lo
// que deja afirmar «no escribió nada» y «no leyó nada», y simular la base caída.
type spyRepo struct {
	lease.Repository

	upserts, markRevokeds, gets            atomic.Int64
	tenantReads, tenantMarks, tenantResets atomic.Int64

	errUpsert, errMarkRevoked, errGet                 error
	errTenantRevoked, errMarkTenant, errRestoreTenant error
}

func (r *spyRepo) Upsert(ctx context.Context, s lease.State) error {
	r.upserts.Add(1)
	if r.errUpsert != nil {
		return r.errUpsert
	}
	return r.Repository.Upsert(ctx, s)
}

func (r *spyRepo) MarkRevoked(ctx context.Context, tenantID, edgeID string, expiresAt time.Time) error {
	r.markRevokeds.Add(1)
	if r.errMarkRevoked != nil {
		return r.errMarkRevoked
	}
	return r.Repository.MarkRevoked(ctx, tenantID, edgeID, expiresAt)
}

func (r *spyRepo) Get(ctx context.Context, tenantID, edgeID string) (lease.State, bool, error) {
	r.gets.Add(1)
	if r.errGet != nil {
		return lease.State{}, false, r.errGet
	}
	return r.Repository.Get(ctx, tenantID, edgeID)
}

func (r *spyRepo) TenantRevoked(ctx context.Context, tenantID string) (bool, error) {
	r.tenantReads.Add(1)
	if r.errTenantRevoked != nil {
		return false, r.errTenantRevoked
	}
	return r.Repository.TenantRevoked(ctx, tenantID)
}

func (r *spyRepo) MarkTenantRevoked(ctx context.Context, tenantID string) error {
	r.tenantMarks.Add(1)
	if r.errMarkTenant != nil {
		return r.errMarkTenant
	}
	return r.Repository.MarkTenantRevoked(ctx, tenantID)
}

func (r *spyRepo) RestoreTenant(ctx context.Context, tenantID string) error {
	r.tenantResets.Add(1)
	if r.errRestoreTenant != nil {
		return r.errRestoreTenant
	}
	return r.Repository.RestoreTenant(ctx, tenantID)
}

// writes es cuántas escrituras, de cualquier clase, llegaron al repositorio.
func (r *spyRepo) writes() int64 {
	return r.upserts.Load() + r.markRevokeds.Load() + r.tenantMarks.Load() + r.tenantResets.Load()
}
