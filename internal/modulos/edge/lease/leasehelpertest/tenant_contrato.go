package leasehelpertest

// Los casos del corte por TENANT (el kill-switch comercial, D-055.2) y de su independencia de
// las filas por Edge: son dos sujetos de corte, y ninguno escribe en el otro. Por eso
// restaurar un tenant reactiva todas sus instalaciones de una vez sin deshacer la revocación
// individual de ninguna.

import (
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/lease"
	"github.com/google/uuid"
)

// caseTenantActiveOrUnknown: un tenant activo no está revocado, y uno que no existe tampoco
// (la ausencia de estado no es un «sí», mismo criterio que Get con found=false).
func caseTenantActiveOrUnknown(t *testing.T, m Montaje) {
	requireTenantRevoked(t, m, seedTenant(t, m), false)
	requireTenantRevoked(t, m, uuid.NewString(), false)
}

// caseTenantRevokedSticky: marcar un tenant lo deja revocado, repetirlo no lo deshace, y no
// alcanza a otro tenant.
func caseTenantRevokedSticky(t *testing.T, m Montaje) {
	tenantA, tenantB := seedTenant(t, m), seedTenant(t, m)
	markTenantRevoked(t, m, tenantA)
	requireTenantRevoked(t, m, tenantA, true)
	markTenantRevoked(t, m, tenantA)
	requireTenantRevoked(t, m, tenantA, true)
	requireTenantRevoked(t, m, tenantB, false)
}

// caseRestoreTenant: restaurar levanta el corte; restaurar un tenant que no estaba cortado no es
// un error ni lo corta; y se puede volver a cortar después.
func caseRestoreTenant(t *testing.T, m Montaje) {
	tenantA, tenantB := seedTenant(t, m), seedTenant(t, m)
	markTenantRevoked(t, m, tenantA)
	markTenantRevoked(t, m, tenantB)
	restoreTenant(t, m, tenantA)
	requireTenantRevoked(t, m, tenantA, false)
	requireTenantRevoked(t, m, tenantB, true)

	never := seedTenant(t, m)
	restoreTenant(t, m, never)
	requireTenantRevoked(t, m, never, false)

	markTenantRevoked(t, m, tenantA)
	requireTenantRevoked(t, m, tenantA, true)
}

// caseTenantCutLeavesEdgeRows: cortar el tenant NO marca sus instalaciones, ni crea filas, ni
// impide escribirlas. Quien decide que un Edge de un tenant cortado no recibe lease es
// lease.Manager, que lee los dos sujetos; el almacén los guarda separados.
func caseTenantCutLeavesEdgeRows(t *testing.T, m Montaje) {
	tenant := seedTenant(t, m)
	upsert(t, m, lease.State{TenantID: tenant, EdgeID: edgeOne, Counter: 5, ExpiresAt: expiryFirst})
	markTenantRevoked(t, m, tenant)

	requireRow(t, m, tenant, edgeOne, 5, expiryFirst, false)
	if _, found, err := m.Repository.Get(t.Context(), tenant, edgeTwo); err != nil || found {
		t.Errorf("cortar el tenant creó una fila para %s: (found=%v, err=%v)", edgeTwo, found, err)
	}
	upsert(t, m, lease.State{TenantID: tenant, EdgeID: edgeOne, Counter: 6, ExpiresAt: expirySecond})
	requireRow(t, m, tenant, edgeOne, 6, expirySecond, false)

	restoreTenant(t, m, tenant)
	requireRow(t, m, tenant, edgeOne, 6, expirySecond, false)
}

// caseRestoreKeepsEdgeRevocation: restaurar el tenant no des-revoca una instalación revocada
// individualmente (las filas de leases no tienen reverso por instalación).
func caseRestoreKeepsEdgeRevocation(t *testing.T, m Montaje) {
	tenant := seedTenant(t, m)
	upsert(t, m, lease.State{TenantID: tenant, EdgeID: edgeOne, Counter: 2, ExpiresAt: expiryFirst})
	upsert(t, m, lease.State{TenantID: tenant, EdgeID: edgeTwo, Counter: 9, ExpiresAt: expiryFirst})
	markRevoked(t, m, tenant, edgeOne, expirySecond)
	markTenantRevoked(t, m, tenant)
	restoreTenant(t, m, tenant)

	requireTenantRevoked(t, m, tenant, false)
	requireRow(t, m, tenant, edgeOne, 2, expirySecond, true)
	requireRow(t, m, tenant, edgeTwo, 9, expiryFirst, false)
}

// caseEdgeCutLeavesTenant: revocar una instalación no corta a su tenant.
func caseEdgeCutLeavesTenant(t *testing.T, m Montaje) {
	tenant := seedTenant(t, m)
	markRevoked(t, m, tenant, edgeOne, expiryFirst)
	requireTenantRevoked(t, m, tenant, false)
}
