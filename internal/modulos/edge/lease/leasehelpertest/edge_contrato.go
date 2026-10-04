package leasehelpertest

// Los casos de las filas por Edge: Upsert, MarkRevoked y Get. Es el sujeto de corte ANTI-CLON
// (ADR-0007, D-055.1): una instalación revocada no vuelve a estar vigente por ninguna escritura
// de este puerto.

import (
	"context"
	"sync"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/lease"
)

// caseGetNeverSeen: un Edge nunca visto no tiene fila, y eso no es un error.
func caseGetNeverSeen(t *testing.T, m Montaje) {
	tenant := seedTenant(t, m)
	st, found, err := m.Repository.Get(context.Background(), tenant, edgeOne)
	if err != nil {
		t.Fatalf("Get de un Edge nunca visto: error inesperado %v", err)
	}
	if found {
		t.Errorf("Get de un Edge nunca visto: found = true con %+v, quería false", st)
	}
	if st != (lease.State{}) {
		t.Errorf("Get de un Edge nunca visto devolvió %+v, quería el State cero", st)
	}
}

// caseUpsertNewRow: la primera emisión crea la fila, vigente, con su counter y su expiración.
func caseUpsertNewRow(t *testing.T, m Montaje) {
	tenant := seedTenant(t, m)
	upsert(t, m, lease.State{TenantID: tenant, EdgeID: edgeOne, Counter: 1, ExpiresAt: expiryFirst})
	st := requireRow(t, m, tenant, edgeOne, 1, expiryFirst, false)
	if st.IssuedAt.IsZero() {
		t.Error("Upsert de una fila nueva dejó issued_at a cero")
	}
	if st.UpdatedAt.IsZero() {
		t.Error("Upsert de una fila nueva dejó updated_at a cero")
	}
}

// caseUpsertIgnoresRevokedTrue: s.Revoked se IGNORA también cuando es true. Upsert no sirve
// para revocar (para eso está MarkRevoked), ni en una fila nueva ni en una existente.
func caseUpsertIgnoresRevokedTrue(t *testing.T, m Montaje) {
	tenant := seedTenant(t, m)
	upsert(t, m, lease.State{TenantID: tenant, EdgeID: edgeOne, Counter: 1, ExpiresAt: expiryFirst, Revoked: true})
	requireRow(t, m, tenant, edgeOne, 1, expiryFirst, false)
	upsert(t, m, lease.State{TenantID: tenant, EdgeID: edgeOne, Counter: 2, ExpiresAt: expirySecond, Revoked: true})
	requireRow(t, m, tenant, edgeOne, 2, expirySecond, false)
}

// caseUpsertExisting: una renovación mueve el counter y la expiración, y conserva issued_at (el
// de la primera emisión).
func caseUpsertExisting(t *testing.T, m Montaje) {
	tenant := seedTenant(t, m)
	upsert(t, m, lease.State{TenantID: tenant, EdgeID: edgeOne, Counter: 1, ExpiresAt: expiryFirst})
	first := mustGet(t, m, tenant, edgeOne)
	upsert(t, m, lease.State{TenantID: tenant, EdgeID: edgeOne, Counter: 8, ExpiresAt: expirySecond})
	second := requireRow(t, m, tenant, edgeOne, 8, expirySecond, false)
	if !second.IssuedAt.Equal(first.IssuedAt) {
		t.Errorf("la renovación movió issued_at de %v a %v; es el de la primera emisión", first.IssuedAt, second.IssuedAt)
	}
	if second.UpdatedAt.Before(first.UpdatedAt) {
		t.Errorf("la renovación dejó updated_at (%v) antes del anterior (%v)", second.UpdatedAt, first.UpdatedAt)
	}
}

// caseUpsertDoesNotResurrect es «Upsert no resucita» (R-L2, T-8 de reglas.md): llamar a Upsert
// a pelo sobre una fila ya revocada, con s.Revoked=false, NO la devuelve a vigente. Es la
// segunda guarda del kill-switch; los tests de lease.Manager no la alcanzan, porque con la
// guarda de la lectura previa puesta Upsert ni siquiera se llama en el camino revocado.
func caseUpsertDoesNotResurrect(t *testing.T, m Montaje) {
	tenant := seedTenant(t, m)
	upsert(t, m, lease.State{TenantID: tenant, EdgeID: edgeOne, Counter: 3, ExpiresAt: expiryFirst})
	markRevoked(t, m, tenant, edgeOne, expirySecond)
	upsert(t, m, lease.State{TenantID: tenant, EdgeID: edgeOne, Counter: 4, ExpiresAt: expiryThird, Revoked: false})
	// El counter y la expiración sí se escriben: lo que Upsert no toca es la revocación.
	requireRow(t, m, tenant, edgeOne, 4, expiryThird, true)

	// Tampoco sobre una fila que NACIÓ revocada (un Edge que solo conoce el kill-switch).
	markRevoked(t, m, tenant, edgeTwo, expiryFirst)
	upsert(t, m, lease.State{TenantID: tenant, EdgeID: edgeTwo, Counter: 1, ExpiresAt: expirySecond})
	requireRow(t, m, tenant, edgeTwo, 1, expirySecond, true)
}

// caseMarkRevokedSticky: revocar conserva el counter, fija la expiración dada y es pegajoso
// (repetirlo no lo deshace).
func caseMarkRevokedSticky(t *testing.T, m Montaje) {
	tenant := seedTenant(t, m)
	upsert(t, m, lease.State{TenantID: tenant, EdgeID: edgeOne, Counter: 7, ExpiresAt: expiryFirst})
	before := mustGet(t, m, tenant, edgeOne)
	markRevoked(t, m, tenant, edgeOne, expirySecond)
	after := requireRow(t, m, tenant, edgeOne, 7, expirySecond, true)
	if !after.IssuedAt.Equal(before.IssuedAt) {
		t.Errorf("MarkRevoked movió issued_at de %v a %v", before.IssuedAt, after.IssuedAt)
	}
	markRevoked(t, m, tenant, edgeOne, expiryThird)
	requireRow(t, m, tenant, edgeOne, 7, expiryThird, true)
}

// caseMarkRevokedNeverSeen: el kill-switch siempre deja rastro, también sobre un Edge sin fila
// (nace revocada, con counter 0: «nunca emitido»).
func caseMarkRevokedNeverSeen(t *testing.T, m Montaje) {
	tenant := seedTenant(t, m)
	markRevoked(t, m, tenant, edgeOne, expiryFirst)
	requireRow(t, m, tenant, edgeOne, 0, expiryFirst, true)
}

// caseRowsIsolated: la clave es (tenant, edge). Revocar un Edge no toca a otro Edge del mismo
// tenant ni al Edge del mismo nombre de otro tenant.
func caseRowsIsolated(t *testing.T, m Montaje) {
	tenantA, tenantB := seedTenant(t, m), seedTenant(t, m)
	if tenantA == tenantB {
		t.Fatalf("Montaje.SeedTenant devolvió dos veces el mismo tenant %q", tenantA)
	}
	upsert(t, m, lease.State{TenantID: tenantA, EdgeID: edgeOne, Counter: 1, ExpiresAt: expiryFirst})
	upsert(t, m, lease.State{TenantID: tenantA, EdgeID: edgeTwo, Counter: 2, ExpiresAt: expirySecond})
	upsert(t, m, lease.State{TenantID: tenantB, EdgeID: edgeOne, Counter: 3, ExpiresAt: expiryThird})
	markRevoked(t, m, tenantA, edgeOne, expiryThird)

	requireRow(t, m, tenantA, edgeOne, 1, expiryThird, true)
	requireRow(t, m, tenantA, edgeTwo, 2, expirySecond, false)
	requireRow(t, m, tenantB, edgeOne, 3, expiryThird, false)
	if _, found, err := m.Repository.Get(context.Background(), tenantB, edgeTwo); err != nil || found {
		t.Errorf("Get(tenantB, %s) = (found=%v, err=%v), quería (false, nil): esa fila no existe", edgeTwo, found, err)
	}
}

// caseConcurrentRevoke: una revocación que compite con N renovaciones termina SIEMPRE revocada,
// llegue antes, después o en medio: ninguna escritura de Upsert la deshace.
func caseConcurrentRevoke(t *testing.T, m Montaje) {
	tenant := seedTenant(t, m)
	upsert(t, m, lease.State{TenantID: tenant, EdgeID: edgeOne, Counter: 1, ExpiresAt: expiryFirst})

	const renewals = 16
	ctx := context.Background()
	start := make(chan struct{})
	errs := make(chan error, renewals+1)
	var wg sync.WaitGroup
	for i := range renewals {
		wg.Go(func() {
			<-start
			errs <- m.Repository.Upsert(ctx, lease.State{
				TenantID: tenant, EdgeID: edgeOne, Counter: int64(i + 2), ExpiresAt: expirySecond,
			})
		})
	}
	wg.Go(func() {
		<-start
		errs <- m.Repository.MarkRevoked(ctx, tenant, edgeOne, expiryThird)
	})
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("escritura concurrente: error inesperado %v", err)
		}
	}
	if st := mustGet(t, m, tenant, edgeOne); !st.Revoked {
		t.Errorf("tras revocar en paralelo con %d renovaciones la fila quedó vigente: %+v", renewals, st)
	}
}
