// Porta internal/diagnostics/diagnostics.go @ 8896f13

// Package diagnostics implementa el lado Cloud del diagnóstico remoto bajo demanda
// (Plan 031 · T5, ADR-0023 capa 3): persiste las solicitudes (DiagnosticsRequest
// emitido al Edge) y correlaciona sus bundles (DiagnosticsBundle recibido), con un
// gate de consentimiento por tenant (default ON, opt-out) y retención por TTL.
//
// El bundle es material OPERATIVO de diagnóstico saneado EN ORIGEN por el Edge (gate
// zero-knowledge verificable, Plan 031 · T8): el Cloud solo lo almacena OPACO. La
// frontera ZK (ADR-0007) la garantiza el Edge; aquí no hay llaves/DEK/credenciales.
//
// El doble en memoria del puerto Store no vive aquí (D-F3-1): es
// diagnosticshelpertest.Memoria, junto a la suite diagnosticshelpertest.ContratoStore.
package diagnostics

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"
)

// ErrNotFound lo devuelve GetBundle cuando no hay ninguna solicitud del tenant con
// ese command_id (nunca se pidió, o pertenece a otro tenant: 404 opaco, INV-8).
var ErrNotFound = errors.New("diagnóstico no encontrado")

// ErrExpired lo devuelve GetBundle cuando la solicitud existió pero venció su TTL de
// retención (410 Gone). La fila se borra de forma perezosa al detectarlo.
var ErrExpired = errors.New("diagnóstico expirado")

// ErrPending lo devuelve GetBundle cuando la solicitud sigue viva pero el Edge aún no
// entregó el bundle (202 Accepted: reintentar la descarga más tarde).
var ErrPending = errors.New("diagnóstico pendiente")

// Bundle es el contenido OPACO que el Edge sube en respuesta a un DiagnosticsRequest.
// Lista CERRADA de partes (ADR-0023): ring buffer de logs, dump de goroutines y
// snapshot JSON de subsistemas. Saneado en origen: CERO llaves/DEK/credenciales/PII.
type Bundle struct {
	LogTail        string
	GoroutineDump  string
	SubsystemsJSON string
}

// Record es una solicitud de diagnóstico con su bundle ya recibido (status ready),
// tal como la devuelve GetBundle para la descarga.
type Record struct {
	CommandID   string
	SessionID   string
	RequestedBy string
	RequestedAt time.Time
	ReceivedAt  time.Time
	Bundle      Bundle
}

// Store persiste las solicitudes de diagnóstico y sus bundles, y resuelve el
// consentimiento por tenant. Toda operación va acotada al tenant (INV-8). Lo
// satisfacen *Postgres y, en los tests, *diagnosticshelpertest.Memoria; las promesas
// comunes a los dos las fija la suite diagnosticshelpertest.ContratoStore.
//
// Los cuatro métodos propios los consume la API pública estructuralmente (declara una
// interfaz con estas mismas firmas): no se cambian.
type Store interface {
	// ConsentEnabled indica si el tenant CONSIENTE el diagnóstico remoto (ADR-0023):
	// default ON (opt-out). Ausencia de fila ⇒ true; una fila enabled=FALSE ⇒ false.
	ConsentEnabled(ctx context.Context, tenantID string) (bool, error)
	// CreateRequest inserta una solicitud pendiente (command_id PK) para correlacionar
	// el bundle que llegará. expiresAt fija la retención (requested_at + TTL). De paso
	// purga de forma perezosa las solicitudes ya vencidas (sin jobs), de CUALQUIER
	// tenant.
	CreateRequest(ctx context.Context, tenantID, sessionID, commandID, requestedBy string, expiresAt time.Time) error
	// DeleteRequest borra una solicitud del tenant (rollback si el push del request al
	// Edge falla: no se deja una fila pending que nunca recibirá bundle). Borrar una que
	// no existe, o la de otro tenant, no hace nada y no es un error.
	DeleteRequest(ctx context.Context, tenantID, commandID string) error
	// GetBundle devuelve el bundle almacenado para (tenant, command_id). ErrNotFound si
	// no existe; ErrExpired (410) si venció el TTL —esté pendiente o lista—, y la
	// solicitud se borra en ese momento; ErrPending (202) si el Edge aún no respondió.
	// Con cualquier error el Record vuelve vacío.
	GetBundle(ctx context.Context, tenantID, commandID string) (Record, error)
	BundleReceiver
}

// BundleReceiver es la cara de RECEPCIÓN que consume el Gateway al demultiplexar un
// DiagnosticsBundle del stream (subconjunto de Store). Se declara aparte para que el
// Gateway dependa solo de lo que usa.
type BundleReceiver interface {
	// SaveBundle correlaciona un bundle recibido con su solicitud PENDING por command_id,
	// acotado por (tenant_id, session_id) de la identidad mTLS del stream, y lo marca
	// ready. found=false si ninguna solicitud pendiente casa (bundle HUÉRFANO: llegó sin
	// solicitud, o venció, o vino de otra sesión/tenant, o la solicitud ya tenía su
	// bundle) ⇒ el Gateway lo ignora con log. Un huérfano no es un error.
	SaveBundle(ctx context.Context, tenantID, sessionID, commandID string, b Bundle) (found bool, err error)
}

// NewCommandID genera un command_id con formato UUIDv4 (crypto/rand): 36 caracteres,
// hexadecimal en minúsculas, versión 4 y variante 10. Es inadivinable, lo que hace
// segura la correlación request↔bundle por él. Mismo formato que el command_id que el
// Gateway usa para sus comandos. Si la fuente de azar falla devuelve "" y el error
// con el prefijo "diagnostics: generando command_id: ".
func NewCommandID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("diagnostics: generando command_id: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // versión 4
	b[8] = (b[8] & 0x3f) | 0x80 // variante 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
