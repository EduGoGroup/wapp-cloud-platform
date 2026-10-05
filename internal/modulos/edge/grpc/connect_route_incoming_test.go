package grpc

// El entrante sellado (decodeIncoming; R-G20, Plan 011 §6.5): el enc_payload se abre con la
// privada de la nube y repuebla los campos sensibles EN MEMORIA antes del motor; lo que no se
// puede abrir se descarta sin tumbar nada y sin que el contenido toque el log. Las claves
// nacen en el test.

import (
	"strings"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/envelope"
	"google.golang.org/protobuf/proto"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/session"
)

// Lo que viaja sellado. De mentira, y lo que ningún log puede contener.
var sealedContent = &cloudlinkv1.SensitivePayload{
	Text: "hola secreta", PushName: "Juana", FromPn: "573001112233", FromLid: "111@lid",
}

// sealingServer es un Server con la privada de cifrado de la nube y lo que el Edge sellaría
// hacia su pública: sealedContent, o los bytes dados si no son nil.
func sealingServer(t *testing.T, raw []byte) (*Server, *logBuffer, []byte) {
	t.Helper()
	pub, priv, err := envelope.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	if raw == nil {
		if raw, err = proto.Marshal(sealedContent); err != nil {
			t.Fatalf("Marshal: %v", err)
		}
	}
	sealed, err := envelope.SealFor(pub, raw)
	if err != nil {
		t.Fatalf("SealFor: %v", err)
	}
	log, buf := capturedLog()
	return New(session.NewRegistry(), log, WithCloudEncPrivKey(priv)), buf, sealed
}

// requireNoContent afirma que ni un campo sellado llegó al log.
func requireNoContent(t *testing.T, log *logBuffer) {
	t.Helper()
	for _, secret := range []string{sealedContent.GetText(), sealedContent.GetPushName(), sealedContent.GetFromPn(), sealedContent.GetFromLid()} {
		if log.contains(secret) {
			t.Errorf("el log contiene %q, que viajaba sellado: %q", secret, log.String())
		}
	}
}

// Un sellado válido se abre y repuebla texto, nombre, número y LID; lo demás del mensaje no
// se toca. Queda la evidencia de que llegó sellado —tamaños, nunca contenido—.
func TestDecodeIncomingOpensTheSealedPayload(t *testing.T) {
	t.Parallel()
	srv, log, sealed := sealingServer(t, nil)
	msg := &cloudlinkv1.IncomingMessage{From: "111@lid", TsUnix: 1234, WaMessageId: "wamid.enc", EncPayload: sealed}

	if !srv.decodeIncoming(msg) {
		t.Fatalf("un sellado válido se descartó: %q", log.String())
	}
	if msg.GetText() != "hola secreta" || msg.GetPushName() != "Juana" || msg.GetFromPn() != "573001112233" || msg.GetFromLid() != "111@lid" {
		t.Errorf("campos repoblados = (%q, %q, %q, %q)", msg.GetText(), msg.GetPushName(), msg.GetFromPn(), msg.GetFromLid())
	}
	if msg.GetFrom() != "111@lid" || msg.GetTsUnix() != 1234 || msg.GetWaMessageId() != "wamid.enc" {
		t.Errorf("abrir el sellado tocó los campos en claro: %v", msg)
	}
	for _, want := range []string{
		"level=INFO", `msg="ingreso: enc_payload sellado abierto"`, "wa_message_id=wamid.enc",
		"text_plano_en_cable_len=0", "enc_payload_bytes=",
	} {
		if !log.contains(want) {
			t.Errorf("al log le falta %q: %q", want, log.String())
		}
	}
	requireNoContent(t, log)
}

// Sin enc_payload (compatibilidad), los campos planos se usan tal cual y no hay nada que decir.
func TestDecodeIncomingKeepsPlainFieldsWithoutSealedPayload(t *testing.T) {
	t.Parallel()
	srv, log, _ := sealingServer(t, nil)
	msg := &cloudlinkv1.IncomingMessage{WaMessageId: "wamid.plain", Text: "en claro", PushName: "Ana"}

	if !srv.decodeIncoming(msg) {
		t.Fatal("un mensaje sin sellado se descartó")
	}
	if msg.GetText() != "en claro" || msg.GetPushName() != "Ana" || log.String() != "" {
		t.Errorf("mensaje = (%q, %q), log = %q; se esperaba intacto y en silencio", msg.GetText(), msg.GetPushName(), log.String())
	}
}

// Lo que no se puede abrir se DESCARTA (false) con un error que nombra el mensaje por su id y
// nunca por su contenido, y los campos del mensaje quedan como llegaron: un sellado
// manipulado, uno que abre pero no deserializa, y uno que llega a una nube sin clave privada.
func TestDecodeIncomingDiscardsWhatItCannotOpen(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		raw      []byte
		tamper   bool
		withKey  bool
		wantLine string
	}{
		"tampered":         {nil, true, true, `msg="ingreso: no se pudo abrir enc_payload; mensaje descartado"`},
		"not a payload":    {[]byte{0xff, 0xff, 0xff}, false, true, `msg="ingreso: enc_payload abierto pero no deserializa; mensaje descartado"`},
		"no cloud key":     {nil, false, false, `msg="ingreso: enc_payload presente pero la nube no tiene clave de cifrado; mensaje descartado"`},
		"tampered, no key": {nil, true, false, `msg="ingreso: enc_payload presente pero la nube no tiene clave de cifrado; mensaje descartado"`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			srv, log, sealed := sealingServer(t, tc.raw)
			if tc.tamper {
				sealed[0] ^= 0xff
				sealed[len(sealed)-1] ^= 0xff
			}
			if !tc.withKey {
				srv.cloudEncPriv = nil
			}
			msg := &cloudlinkv1.IncomingMessage{WaMessageId: "wamid.bad", EncPayload: sealed}

			if srv.decodeIncoming(msg) {
				t.Fatal("un sellado ilegible se dio por bueno")
			}
			if msg.GetText() != "" || msg.GetPushName() != "" || msg.GetFromPn() != "" || msg.GetFromLid() != "" {
				t.Errorf("un mensaje descartado quedó con campos repoblados: %v", msg)
			}
			if !log.contains("level=ERROR") || !log.contains(tc.wantLine) || !log.contains("wa_message_id=wamid.bad") {
				t.Errorf("al log le falta el error con el id del mensaje: %q", log.String())
			}
			if strings.Count(log.String(), "\n") != 1 {
				t.Errorf("se esperaba UNA línea de log: %q", log.String())
			}
			requireNoContent(t, log)
		})
	}
}

// Visto desde route: el motor recibe el mensaje YA abierto, y un mensaje descartado no llega
// al hook (el stream sigue: route no devuelve nada que lo tumbe).
func TestRouteOpensTheIncomingBeforeTheHookAndDropsTheUnreadable(t *testing.T) {
	t.Parallel()
	srv, _, sealed := sealingServer(t, nil)
	var got []*cloudlinkv1.IncomingMessage
	srv.OnIncoming = func(_ string, m *cloudlinkv1.IncomingMessage) { got = append(got, m) }
	lane := newWorkLane(t.Context(), 1, 0, quietLog())
	t.Cleanup(func() { closeLane(t, lane) })
	cc := phone("tenant-1", "edge-1", "s-1")

	srv.route(lane, cc, incomingFrame("s-1", &cloudlinkv1.IncomingMessage{WaMessageId: "wamid.ok", EncPayload: sealed}))
	if len(got) != 1 || got[0].GetText() != "hola secreta" {
		t.Fatalf("el hook recibió %v, se esperaba el mensaje ya abierto", got)
	}

	broken := append([]byte(nil), sealed...)
	broken[0] ^= 0xff
	srv.route(lane, cc, incomingFrame("s-1", &cloudlinkv1.IncomingMessage{WaMessageId: "wamid.bad", EncPayload: broken}))
	if len(got) != 1 {
		t.Fatalf("un mensaje ilegible llegó al hook: %v", got[1])
	}
}
