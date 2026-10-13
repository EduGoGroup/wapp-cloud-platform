package runtime

import (
	"context"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
)

// runtime.go solo declara datos (cinco interfaces y dos grupos de constantes): no tiene cuerpo
// que implementar, así que su test no lleva la etiqueta `pendiente` y pasa desde el primer día.
// Lo que fija es lo que un cambio distraído rompería sin que nada más lo notara: la forma EXACTA
// de cada puerto y los literales del vocabulario.

// Las formas de los cinco puertos, escritas a mano. Cada puerto se asigna a su forma y la forma al
// puerto: si a un puerto le sobra, le falta o le cambia un método, una de las dos líneas deja de
// compilar.
type (
	// senderShape es la de los métodos SendText y SendMedia del servidor gRPC del Gateway, que
	// implementa Sender sin adaptador (trampa T-13).
	senderShape interface {
		SendText(ctx context.Context, sessionID, to, text string) (*cloudlinkv1.Ack, error)
		SendMedia(ctx context.Context, sessionID, to, presignedURL, filename, mime, caption, kind string) (*cloudlinkv1.Ack, error)
	}
	presignerShape interface {
		GenerateDownloadURL(ctx context.Context, key string) (string, time.Time, error)
	}
	tenantResolverShape interface {
		ResolveTenant(ctx context.Context, sessionID string) (string, string, error)
	}
	selfNumberCheckerShape interface {
		IsSelfNumber(ctx context.Context, tenantID, normalizedNumber string) (bool, error)
	}
	ingestDeduperShape interface {
		Seen(ctx context.Context, sessionID, waMessageID string) (bool, error)
	}
)

var (
	_ Sender      = senderShape(nil)
	_ senderShape = Sender(nil)

	_ Presigner      = presignerShape(nil)
	_ presignerShape = Presigner(nil)

	_ TenantResolver      = tenantResolverShape(nil)
	_ tenantResolverShape = TenantResolver(nil)

	_ SelfNumberChecker      = selfNumberCheckerShape(nil)
	_ selfNumberCheckerShape = SelfNumberChecker(nil)

	_ IngestDeduper      = ingestDeduperShape(nil)
	_ ingestDeduperShape = IngestDeduper(nil)
)

// TestProfiles_Vocabulary: los dos perfiles son los literales de la columna
// fleet_sessions.profile (CHECK de la 0063) y no se tocan.
func TestProfiles_Vocabulary(t *testing.T) {
	if profileActive != "active" {
		t.Errorf("profileActive = %q, quería %q", profileActive, "active")
	}
	if profilePassive != "passive" {
		t.Errorf("profilePassive = %q, quería %q", profilePassive, "passive")
	}
}

// TestReasons_FixedCardinalityOfFour: los motivos son la etiqueta `reason` del contador
// wapp_flow_reactive_blocked_total, byte a byte, y son CUATRO distintos: ni uno más ni dos iguales.
func TestReasons_FixedCardinalityOfFour(t *testing.T) {
	reasons := map[string]string{
		reasonPassive:    "passive",
		reasonSelfLoop:   "self_loop",
		reasonRateLimit:  "rate_limit",
		reasonSaturation: "saturation",
	}
	if len(reasons) != 4 {
		t.Fatalf("hay %d motivos distintos, quería 4: %v", len(reasons), reasons)
	}
	for got, want := range reasons {
		if got != want {
			t.Errorf("motivo = %q, quería %q", got, want)
		}
	}
}
