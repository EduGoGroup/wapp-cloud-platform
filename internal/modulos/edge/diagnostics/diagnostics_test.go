//go:build pendiente

package diagnostics_test

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/diagnostics"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/diagnostics/diagnosticshelpertest"
)

// bundleSink es, copiada, la cara que el gateway gRPC usa del almacén al recibir un
// DiagnosticsBundle: la firma de SaveBundle no puede cambiar.
type bundleSink interface {
	SaveBundle(ctx context.Context, tenantID, sessionID, commandID string, b diagnostics.Bundle) (found bool, err error)
}

// requestStore son, copiados, los cuatro métodos que la API pública pide a su almacén de
// diagnósticos: sus firmas no pueden cambiar.
type requestStore interface {
	ConsentEnabled(ctx context.Context, tenantID string) (bool, error)
	CreateRequest(ctx context.Context, tenantID, sessionID, commandID, requestedBy string, expiresAt time.Time) error
	DeleteRequest(ctx context.Context, tenantID, commandID string) error
	GetBundle(ctx context.Context, tenantID, commandID string) (diagnostics.Record, error)
}

// Las dos implementaciones son Store; un Store sirve como BundleReceiver, y los dos sirven donde
// sus consumidores piden lo suyo: lo comprueba el compilador.
var (
	_ diagnostics.Store          = (*diagnostics.Postgres)(nil)
	_ diagnostics.Store          = (*diagnosticshelpertest.Memoria)(nil)
	_ diagnostics.BundleReceiver = diagnostics.Store(nil)
	_ bundleSink                 = diagnostics.BundleReceiver(nil)
	_ requestStore               = diagnostics.Store(nil)
)

// TestSentinels_TextsAreLiteral: los tres centinelas llevan el texto de siempre y son tres errores
// distintos: quien los traduce a 404, 410 y 202 los distingue por identidad.
func TestSentinels_TextsAreLiteral(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"not found", diagnostics.ErrNotFound, "diagnóstico no encontrado"},
		{"expired", diagnostics.ErrExpired, "diagnóstico expirado"},
		{"pending", diagnostics.ErrPending, "diagnóstico pendiente"},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.err.Error() != c.want {
				t.Errorf("texto = %q, quería %q", c.err.Error(), c.want)
			}
			for j, other := range cases {
				if i != j && errors.Is(c.err, other.err) {
					t.Errorf("%q se confunde con %q", c.want, other.want)
				}
			}
		})
	}
}

// uuidV4 es el formato de un UUIDv4 en minúsculas: versión 4 y variante 10 (8, 9, a o b).
var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// TestNewCommandID_IsAUUIDv4: cada command_id tiene el formato UUIDv4, siempre (la versión y la
// variante no dependen del azar).
func TestNewCommandID_IsAUUIDv4(t *testing.T) {
	for range 200 {
		id, err := diagnostics.NewCommandID()
		if err != nil {
			t.Fatalf("NewCommandID: error inesperado %v", err)
		}
		if !uuidV4.MatchString(id) {
			t.Fatalf("NewCommandID() = %q, que no es un UUIDv4 en minúsculas", id)
		}
	}
}

// TestNewCommandID_DoesNotRepeat: es aleatorio: 1000 seguidos son 1000 distintos.
func TestNewCommandID_DoesNotRepeat(t *testing.T) {
	const n = 1000
	seen := make(map[string]bool, n)
	for range n {
		id, err := diagnostics.NewCommandID()
		if err != nil {
			t.Fatalf("NewCommandID: error inesperado %v", err)
		}
		if seen[id] {
			t.Fatalf("NewCommandID repitió %q", id)
		}
		seen[id] = true
	}
}

// TestRecord_CarriesTheBundle: un Record es la solicitud con su bundle dentro; los dos son valores
// comparables (la suite del puerto los compara con ==).
func TestRecord_CarriesTheBundle(t *testing.T) {
	bundle := diagnostics.Bundle{LogTail: "log", GoroutineDump: "dump", SubsystemsJSON: `{"x":1}`}
	at := time.Unix(1_700_000_000, 0).UTC()
	rec := diagnostics.Record{
		CommandID: "cmd-1", SessionID: "session-1", RequestedBy: "user-1",
		RequestedAt: at, ReceivedAt: at.Add(time.Second), Bundle: bundle,
	}
	if rec.Bundle != bundle {
		t.Errorf("Record.Bundle = %+v, quería %+v", rec.Bundle, bundle)
	}
	if rec == (diagnostics.Record{}) {
		t.Error("un Record con datos es igual al Record vacío")
	}
	if (diagnostics.Bundle{}) == bundle {
		t.Error("un Bundle con datos es igual al Bundle vacío")
	}
}
