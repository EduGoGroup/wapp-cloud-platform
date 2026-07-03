package gatewaygrpc_test

import (
	"context"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
)

// TestConnectSendMediaAck ejerce el gateway SendMedia extremo a extremo (Plan 017
// §6.1): empuja el comando por el stream vivo, verifica que el Edge recibe un
// SendMedia con la URL prefirmada, los metadatos y el MediaKind mapeado desde
// kind="image", y que al responder el Ack la llamada retorna. Espeja
// TestConnectSendTextAck (mismo patrón de correlación por command_id).
func TestConnectSendMediaAck(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := h.client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	if sendErr := stream.Send(heartbeat("s1")); sendErr != nil {
		t.Fatalf("Send heartbeat: %v", sendErr)
	}
	waitOnline(t, h.registry, "s1", true)

	type result struct {
		ack *cloudlinkv1.Ack
		err error
	}
	res := make(chan result, 1)
	go func() {
		ack, sendErr := h.srv.SendMedia(ctx, "s1", "57301",
			"https://r2.example/presigned?sig=abc", "orden-291798.png", "image/png",
			"Tu comprobante 📎", "image")
		res <- result{ack: ack, err: sendErr}
	}()

	cmd, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv comando: %v", err)
	}
	assertSendMediaCmd(t, cmd)

	if sendErr := stream.Send(&cloudlinkv1.EdgeToCloud{
		SessionId: "s1",
		Payload: &cloudlinkv1.EdgeToCloud_Ack{
			Ack: &cloudlinkv1.Ack{AckedCommandId: cmd.GetCommandId(), Ok: true},
		},
	}); sendErr != nil {
		t.Fatalf("Send ack: %v", sendErr)
	}

	select {
	case r := <-res:
		if r.err != nil {
			t.Fatalf("SendMedia devolvió error: %v", r.err)
		}
		if !r.ack.GetOk() {
			t.Fatalf("Ack.Ok = false, quiero true: %+v", r.ack)
		}
	case <-ctx.Done():
		t.Fatal("timeout esperando el retorno de SendMedia")
	}
}

// assertSendMediaCmd verifica que el comando recibido por el Edge es un SendMedia
// con la URL prefirmada, los metadatos y el MediaKind mapeado desde kind="image",
// que usa presigned_url (no inline bytes en el MVP) y que trae command_id. Se
// aísla del test para acotar su complejidad ciclomática.
func assertSendMediaCmd(t *testing.T, cmd *cloudlinkv1.CloudToEdge) {
	t.Helper()
	sm := cmd.GetSendMedia()
	if sm == nil {
		t.Fatalf("comando recibido no es SendMedia: %+v", cmd)
	}
	if sm.GetTo() != "57301" || sm.GetPresignedUrl() != "https://r2.example/presigned?sig=abc" {
		t.Fatalf("SendMedia to/url no coinciden: %+v", sm)
	}
	if sm.GetFilename() != "orden-291798.png" || sm.GetMime() != "image/png" || sm.GetCaption() != "Tu comprobante 📎" {
		t.Fatalf("SendMedia metadatos no coinciden: %+v", sm)
	}
	if sm.GetKind() != cloudlinkv1.MediaKind_MEDIA_KIND_IMAGE {
		t.Fatalf("MediaKind = %v, quiero IMAGE (mapeo de kind=image)", sm.GetKind())
	}
	if sm.GetInline() != nil {
		t.Fatalf("MVP usa presigned_url, no inline bytes: %+v", sm)
	}
	if cmd.GetCommandId() == "" {
		t.Fatal("comando sin command_id")
	}
}
