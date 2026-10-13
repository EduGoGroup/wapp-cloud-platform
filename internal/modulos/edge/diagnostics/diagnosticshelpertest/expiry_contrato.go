package diagnosticshelpertest

// Trozo de la suite ContratoStore (contrato.go): el vencimiento (ErrExpired y borrado perezoso),
// la purga al crear y el borrado de una solicitud (rollback).

import (
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/diagnostics"
)

func caseExpiredPending(t *testing.T, m Montaje) {
	cmd := newCommand()
	create(t, m, m.TenantA, cmd, alive())
	m.Expire(t, m.TenantA, cmd)
	requireGetError(t, m, m.TenantA, cmd, diagnostics.ErrExpired)
	// Borrado perezoso: detectarla vencida la borró.
	requireGetError(t, m, m.TenantA, cmd, diagnostics.ErrNotFound)
}

func caseExpiredReady(t *testing.T, m Montaje) {
	cmd := newCommand()
	create(t, m, m.TenantA, cmd, alive())
	requireSave(t, m, m.TenantA, sessionMain, cmd, sampleBundle, true)
	m.Expire(t, m.TenantA, cmd)
	// Vencida gana a lista: el bundle ya no se entrega.
	requireGetError(t, m, m.TenantA, cmd, diagnostics.ErrExpired)
	requireGetError(t, m, m.TenantA, cmd, diagnostics.ErrNotFound)
}

// caseCreatedExpired crea la solicitud con el vencimiento ya pasado (un TTL negativo): nace
// vencida, y la purga de su propia creación no se la lleva porque corre antes de insertarla.
func caseCreatedExpired(t *testing.T, m Montaje) {
	cmd := newCommand()
	create(t, m, m.TenantA, cmd, expired())
	requireSave(t, m, m.TenantA, sessionMain, cmd, sampleBundle, false)
	requireGetError(t, m, m.TenantA, cmd, diagnostics.ErrExpired)
	requireGetError(t, m, m.TenantA, cmd, diagnostics.ErrNotFound)
}

func caseBundleForExpired(t *testing.T, m Montaje) {
	cmd := newCommand()
	create(t, m, m.TenantA, cmd, alive())
	m.Expire(t, m.TenantA, cmd)
	requireSave(t, m, m.TenantA, sessionMain, cmd, sampleBundle, false)
	requireGetError(t, m, m.TenantA, cmd, diagnostics.ErrExpired)
}

// caseCreatePurges distingue la purga del borrado perezoso por el centinela: una vencida que
// nadie ha descargado daría ErrExpired; si crear otra la purgó, da ErrNotFound. La purga es de
// todos los tenants.
func caseCreatePurges(t *testing.T, m Montaje) {
	oldA, oldB, fresh := newCommand(), newCommand(), newCommand()
	create(t, m, m.TenantA, oldA, alive())
	create(t, m, m.TenantB, oldB, alive())
	m.Expire(t, m.TenantA, oldA)
	m.Expire(t, m.TenantB, oldB)
	create(t, m, m.TenantA, fresh, alive())
	requireGetError(t, m, m.TenantA, oldA, diagnostics.ErrNotFound)
	requireGetError(t, m, m.TenantB, oldB, diagnostics.ErrNotFound)
	requireGetError(t, m, m.TenantA, fresh, diagnostics.ErrPending)
}

func caseCreateKeepsLive(t *testing.T, m Montaje) {
	pending, ready, fresh := newCommand(), newCommand(), newCommand()
	create(t, m, m.TenantA, pending, alive())
	create(t, m, m.TenantB, ready, alive())
	requireSave(t, m, m.TenantB, sessionMain, ready, sampleBundle, true)
	create(t, m, m.TenantA, fresh, alive())
	requireGetError(t, m, m.TenantA, pending, diagnostics.ErrPending)
	requireRecord(t, m, m.TenantB, ready, sampleBundle)
}

func caseDelete(t *testing.T, m Montaje) {
	cmd := newCommand()
	create(t, m, m.TenantA, cmd, alive())
	deleteRequest(t, m, m.TenantA, cmd)
	requireGetError(t, m, m.TenantA, cmd, diagnostics.ErrNotFound)
}

func caseDeleteFromOtherTenant(t *testing.T, m Montaje) {
	cmd := newCommand()
	create(t, m, m.TenantA, cmd, alive())
	deleteRequest(t, m, m.TenantB, cmd)
	requireGetError(t, m, m.TenantA, cmd, diagnostics.ErrPending)
}

func caseDeleteUnknown(t *testing.T, m Montaje) {
	deleteRequest(t, m, m.TenantA, newCommand())
}

func caseBundleAfterDelete(t *testing.T, m Montaje) {
	cmd := newCommand()
	create(t, m, m.TenantA, cmd, alive())
	deleteRequest(t, m, m.TenantA, cmd)
	requireSave(t, m, m.TenantA, sessionMain, cmd, sampleBundle, false)
}
