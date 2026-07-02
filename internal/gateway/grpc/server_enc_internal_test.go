package gatewaygrpc

import (
	"bytes"
	"strings"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"github.com/EduGoGroup/wapp-shared/envelope"
	"github.com/EduGoGroup/wapp-shared/logger"
	"golang.org/x/crypto/curve25519"
	"google.golang.org/protobuf/proto"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
)

// testCloudPriv es una privada X25519 fija (32B) para construir el par de cifrado
// de la nube en los tests, de forma determinista.
func testCloudPriv() []byte {
	priv := make([]byte, envelope.PrivateKeySize)
	for i := range priv {
		priv[i] = byte(i + 7)
	}
	return priv
}

// testCloudPub deriva la pública correspondiente a testCloudPriv().
func testCloudPub(t *testing.T) []byte {
	t.Helper()
	pub, err := curve25519.X25519(testCloudPriv(), curve25519.Basepoint)
	if err != nil {
		t.Fatalf("derivando pública de test: %v", err)
	}
	return pub
}

// sealSensitive sella un SensitivePayload hacia la pública de la nube de test.
func sealSensitive(t *testing.T, sp *cloudlinkv1.SensitivePayload) []byte {
	t.Helper()
	raw, err := proto.Marshal(sp)
	if err != nil {
		t.Fatalf("Marshal SensitivePayload: %v", err)
	}
	sealed, err := envelope.SealFor(testCloudPub(t), raw)
	if err != nil {
		t.Fatalf("SealFor: %v", err)
	}
	return sealed
}

func newEncServer(w *bytes.Buffer) *Server {
	return New(session.NewRegistry(), logger.New(logger.WithWriter(w)), WithCloudEncPrivKey(testCloudPriv()))
}

// TestDecodeIncoming_AbreYRepoblar: un enc_payload sellado se abre y repuebla los
// campos sensibles en memoria (§6.5).
func TestDecodeIncoming_AbreYRepoblar(t *testing.T) {
	t.Parallel()
	var logBuf bytes.Buffer
	s := newEncServer(&logBuf)

	sealed := sealSensitive(t, &cloudlinkv1.SensitivePayload{
		Text:     "hola secreta",
		PushName: "Juan",
		FromPn:   "573001112233",
		FromLid:  "111@lid",
	})
	msg := &cloudlinkv1.IncomingMessage{
		From:        "573001112233@s.whatsapp.net",
		TsUnix:      1234,
		WaMessageId: "wamid.enc",
		EncPayload:  sealed,
	}

	if !s.decodeIncoming(msg) {
		t.Fatal("decodeIncoming devolvió false para un sellado válido")
	}
	if msg.GetText() != "hola secreta" || msg.GetPushName() != "Juan" ||
		msg.GetFromPn() != "573001112233" || msg.GetFromLid() != "111@lid" {
		t.Fatalf("campos no repoblados: %+v", msg)
	}
}

// TestDecodeIncoming_Corrupto: un enc_payload manipulado → error claro, descarte
// (false), SIN crash y SIN loguear el contenido (§10.I).
func TestDecodeIncoming_Corrupto(t *testing.T) {
	t.Parallel()
	var logBuf bytes.Buffer
	s := newEncServer(&logBuf)

	sealed := sealSensitive(t, &cloudlinkv1.SensitivePayload{Text: "contenido-supersecreto"})
	// Corromper el sellado (flip de bytes) para que OpenWith falle.
	sealed[len(sealed)-1] ^= 0xFF
	sealed[0] ^= 0xFF

	msg := &cloudlinkv1.IncomingMessage{WaMessageId: "wamid.corrupto", EncPayload: sealed}

	if s.decodeIncoming(msg) {
		t.Fatal("decodeIncoming devolvió true para un sellado corrupto")
	}
	if msg.GetText() != "" {
		t.Fatalf("no debería haber repoblado texto: %q", msg.GetText())
	}
	logs := logBuf.String()
	if !strings.Contains(logs, "wamid.corrupto") {
		t.Fatalf("el log de error debería incluir el wa_message_id; log=%q", logs)
	}
	if strings.Contains(logs, "contenido-supersecreto") {
		t.Fatalf("el log NO debe contener el contenido; log=%q", logs)
	}
}

// TestDecodeIncoming_SinEncPayload: sin enc_payload se usan los campos planos tal
// cual (compat §10.H).
func TestDecodeIncoming_SinEncPayload(t *testing.T) {
	t.Parallel()
	var logBuf bytes.Buffer
	s := newEncServer(&logBuf)

	msg := &cloudlinkv1.IncomingMessage{
		Text:        "en claro",
		PushName:    "Ana",
		WaMessageId: "wamid.plano",
	}
	if !s.decodeIncoming(msg) {
		t.Fatal("decodeIncoming devolvió false sin enc_payload")
	}
	if msg.GetText() != "en claro" || msg.GetPushName() != "Ana" {
		t.Fatalf("campos planos alterados: %+v", msg)
	}
}

// TestDecodeIncoming_SinClavePrivada: con enc_payload presente pero sin clave
// privada configurada, el mensaje se descarta (no se puede recuperar).
func TestDecodeIncoming_SinClavePrivada(t *testing.T) {
	t.Parallel()
	var logBuf bytes.Buffer
	s := New(session.NewRegistry(), logger.New(logger.WithWriter(&logBuf))) // sin WithCloudEncPrivKey

	sealed := sealSensitive(t, &cloudlinkv1.SensitivePayload{Text: "x"})
	msg := &cloudlinkv1.IncomingMessage{WaMessageId: "wamid.sinclave", EncPayload: sealed}

	if s.decodeIncoming(msg) {
		t.Fatal("decodeIncoming debería descartar sin clave privada")
	}
	if !strings.Contains(logBuf.String(), "wamid.sinclave") {
		t.Fatalf("el log debería incluir el wa_message_id; log=%q", logBuf.String())
	}
}
