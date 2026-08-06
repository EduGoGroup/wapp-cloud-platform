package gatewaygrpc_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"

	gatewaygrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/grpc"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/session"
)

// TestSendTextSesiónOfflineDevuelveSendErrorConCommandID: el command_id se genera
// DENTRO del envío, así que si el fallo se lo tragara el llamante se quedaría sin
// el único hilo que correlaciona lo que la nube intentó con el outbox del Edge y
// con los acuses del Plan 013.
//
// Este test ata los dos extremos del notificador de solicitudes (Plan 041 · T4.2):
// allí el command_id se saca por duck-typing (`interface{ CommandID() string }`) y
// lo que se prueba con un doble es el CONTRATO; lo que se prueba aquí es que el
// Gateway real lo cumple.
// No usa el harness de bufconn a propósito: una sesión offline se resuelve en el
// Registry, antes de que haya nada que mandar por gRPC, así que levantar un
// servidor y un cliente para esto solo añadiría partes que pueden fallar por
// motivos que no son los del test.
func TestSendTextSesiónOfflineDevuelveSendErrorConCommandID(t *testing.T) {
	t.Parallel()
	srv := gatewaygrpc.New(session.NewRegistry(), logger.New(logger.WithWriter(io.Discard)))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Registry vacío: el Push no encuentra a quién empujar.
	_, err := srv.SendText(ctx, "fantasma", "57301", "hola")
	if err == nil {
		t.Fatal("SendText a una sesión offline debería fallar")
	}

	// La causa sigue siendo inspeccionable: los writeSendError de los handlers
	// traducen ESTO a un 502, y envolver no puede haberlo roto.
	if !errors.Is(err, session.ErrSessionOffline) {
		t.Fatalf("errors.Is(err, ErrSessionOffline) = false; err = %v", err)
	}

	var sendErr *gatewaygrpc.SendError
	if !errors.As(err, &sendErr) {
		t.Fatalf("el error no es *SendError: %v", err)
	}
	if sendErr.CommandID() == "" {
		t.Fatal("el *SendError no lleva command_id: el operador se queda sin correlación")
	}
	if sendErr.SessionID() != "fantasma" {
		t.Fatalf("session_id = %q, quiero fantasma", sendErr.SessionID())
	}

	// Cero PII en el texto del error: ni el destino ni el contenido del mensaje.
	if msg := sendErr.Error(); strings.Contains(msg, "57301") || strings.Contains(msg, "hola") {
		t.Fatalf("el error filtra el destino o el texto del mensaje: %q", msg)
	}
}
