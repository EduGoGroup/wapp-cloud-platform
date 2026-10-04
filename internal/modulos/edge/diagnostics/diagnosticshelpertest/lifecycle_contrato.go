package diagnosticshelpertest

// Trozo de la suite ContratoStore (contrato.go): el ciclo solicitud ⇒ bundle ⇒ descarga y su
// correlación por command_id + (tenant, sesión).

import (
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/diagnostics"
)

func casePending(t *testing.T, m Montaje) {
	cmd := newCommand()
	create(t, m, m.TenantA, cmd, alive())
	requireGetError(t, m, m.TenantA, cmd, diagnostics.ErrPending)
	// Preguntar no la consume: sigue pendiente.
	requireGetError(t, m, m.TenantA, cmd, diagnostics.ErrPending)
}

func caseRoundTrip(t *testing.T, m Montaje) {
	cmd := newCommand()
	create(t, m, m.TenantA, cmd, alive())
	requireSave(t, m, m.TenantA, sessionMain, cmd, sampleBundle, true)
	requireRecord(t, m, m.TenantA, cmd, sampleBundle)
	// La descarga no la consume: se puede repetir mientras viva.
	requireRecord(t, m, m.TenantA, cmd, sampleBundle)
}

func caseEmptyBundle(t *testing.T, m Montaje) {
	cmd := newCommand()
	create(t, m, m.TenantA, cmd, alive())
	requireSave(t, m, m.TenantA, sessionMain, cmd, diagnostics.Bundle{}, true)
	requireRecord(t, m, m.TenantA, cmd, diagnostics.Bundle{})
}

func caseOrphanBundle(t *testing.T, m Montaje) {
	requireSave(t, m, m.TenantA, sessionMain, newCommand(), sampleBundle, false)
}

func caseBundleFromOtherTenant(t *testing.T, m Montaje) {
	cmd := newCommand()
	create(t, m, m.TenantA, cmd, alive())
	requireSave(t, m, m.TenantB, sessionMain, cmd, sampleBundle, false)
	// El intento ajeno no la consumió ni le dejó nada: sigue pendiente y el bueno casa.
	requireGetError(t, m, m.TenantA, cmd, diagnostics.ErrPending)
	requireSave(t, m, m.TenantA, sessionMain, cmd, sampleBundle, true)
}

func caseBundleFromOtherSession(t *testing.T, m Montaje) {
	cmd := newCommand()
	create(t, m, m.TenantA, cmd, alive())
	requireSave(t, m, m.TenantA, sessionOther, cmd, sampleBundle, false)
	requireGetError(t, m, m.TenantA, cmd, diagnostics.ErrPending)
	requireSave(t, m, m.TenantA, sessionMain, cmd, sampleBundle, true)
}

func caseSecondBundle(t *testing.T, m Montaje) {
	cmd := newCommand()
	create(t, m, m.TenantA, cmd, alive())
	requireSave(t, m, m.TenantA, sessionMain, cmd, sampleBundle, true)
	other := diagnostics.Bundle{LogTail: "second", GoroutineDump: "second", SubsystemsJSON: "{}"}
	requireSave(t, m, m.TenantA, sessionMain, cmd, other, false)
	// El segundo no pisa al primero.
	requireRecord(t, m, m.TenantA, cmd, sampleBundle)
}

func caseUnknownCommand(t *testing.T, m Montaje) {
	requireGetError(t, m, m.TenantA, newCommand(), diagnostics.ErrNotFound)
}

func caseDownloadFromOtherTenant(t *testing.T, m Montaje) {
	cmd := newCommand()
	create(t, m, m.TenantA, cmd, alive())
	requireSave(t, m, m.TenantA, sessionMain, cmd, sampleBundle, true)
	requireGetError(t, m, m.TenantB, cmd, diagnostics.ErrNotFound)
	// Y el intento ajeno no la borró.
	requireRecord(t, m, m.TenantA, cmd, sampleBundle)
}
