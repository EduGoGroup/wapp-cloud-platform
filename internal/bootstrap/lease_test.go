package bootstrap

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/lease"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/config"
)

// TestBuildLeaseManager_ProdMissingKeyFailsFast cubre T1.1: en producción, sin
// WAPP_LEASE_PRIVATE_KEY_FILE ni WAPP_LEASE_PRIVATE_KEY_B64 configurados,
// buildLeaseManager debe abortar el arranque (ADR-0007) en vez de generar en
// silencio una clave Ed25519 efímera. El mensaje de error debe citar los
// nombres literales de las dos variables de entorno.
func TestBuildLeaseManager_ProdMissingKeyFailsFast(t *testing.T) {
	cfg := config.AppConfig{Env: "prod", Lease: config.LeaseConfig{}}

	mgr, err := buildLeaseManager(cfg, nil, &recordingLogger{})
	if err == nil {
		t.Fatal("se esperaba fail-fast en prod sin clave de lease configurada")
	}
	if mgr != nil {
		t.Fatal("buildLeaseManager no debería devolver Manager cuando falla")
	}
	if !strings.Contains(err.Error(), "WAPP_LEASE_PRIVATE_KEY_FILE") {
		t.Errorf("el error debería citar WAPP_LEASE_PRIVATE_KEY_FILE, got: %v", err)
	}
	if !strings.Contains(err.Error(), "WAPP_LEASE_PRIVATE_KEY_B64") {
		t.Errorf("el error debería citar WAPP_LEASE_PRIVATE_KEY_B64, got: %v", err)
	}
}

// TestBuildLeaseManager_DevEphemeralWithWarning es la regresión de T1.1: en dev,
// sin clave configurada, el comportamiento NO cambia -- se sigue generando la
// clave Ed25519 efímera y se sigue logueando el mismo log.Warn de siempre.
func TestBuildLeaseManager_DevEphemeralWithWarning(t *testing.T) {
	cfg := config.AppConfig{Env: "dev", Lease: config.LeaseConfig{}}
	log := &recordingLogger{}

	mgr, err := buildLeaseManager(cfg, nil, log)
	if err != nil {
		t.Fatalf("dev sin clave configurada debería generar una efímera y arrancar: %v", err)
	}
	if mgr == nil {
		t.Fatal("se esperaba un Manager construido con la clave efímera")
	}
	if log.warnCount() == 0 {
		t.Fatal("se esperaba un warning por clave de lease EFÍMERA de dev")
	}
	if !strings.Contains(strings.ToLower(log.warns[0]), "efímera") {
		t.Fatalf("warning inesperado: %q", log.warns[0])
	}
}

// TestBuildLeaseManager_ProdWithConfiguredKeySucceeds confirma que el fail-fast
// solo dispara cuando falta la clave: con WAPP_LEASE_PRIVATE_KEY_B64 configurado
// en prod, buildLeaseManager arranca normalmente (KeySourceBase64, sin warning
// de efímero).
func TestBuildLeaseManager_ProdWithConfiguredKeySucceeds(t *testing.T) {
	priv, err := lease.GenerateDevKey()
	if err != nil {
		t.Fatalf("generando clave de prueba: %v", err)
	}
	cfg := config.AppConfig{
		Env: "prod",
		Lease: config.LeaseConfig{
			PrivateKeyB64: base64.StdEncoding.EncodeToString(priv),
		},
	}
	log := &recordingLogger{}

	mgr, err := buildLeaseManager(cfg, nil, log)
	if err != nil {
		t.Fatalf("prod con clave configurada debería arrancar: %v", err)
	}
	if mgr == nil {
		t.Fatal("se esperaba un Manager construido")
	}
	if log.warnCount() != 0 {
		t.Fatalf("no se esperaba warning de clave efímera con clave configurada, got: %v", log.warns)
	}
}
