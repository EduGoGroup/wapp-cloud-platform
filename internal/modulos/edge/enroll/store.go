// Porta internal/gateway/enroll/store.go @ 8896f13

package enroll

import (
	"context"
	"errors"
)

// Errores de consumo de código (sentinela). El transporte (Server.EnrollEdge)
// los traduce todos a codes.PermissionDenied; se mantienen agnósticos al
// transporte.
//
// El doble en memoria (enrollhelpertest.MemoriaCodeStore; en el paquete viejo,
// MemoryStore, que vivía aquí) distingue las tres causas (útil en tests
// unitarios). El store Postgres NO las distingue a propósito: el consumo es un
// único UPDATE atómico que, si no afecta filas, no puede (ni debe) revelar si
// el código no existe, expiró o ya se usó; en todos esos casos devuelve
// ErrCodeInvalid. La respuesta de seguridad es la misma (PermissionDenied) y
// así no se filtra información.
//
// Los textos son observables y se portan literales.
var (
	// ErrCodeNotFound indica que el código no existe en el store.
	ErrCodeNotFound = errors.New("enroll: código de activación desconocido")
	// ErrCodeExpired indica que el código existe pero su TTL ya venció.
	ErrCodeExpired = errors.New("enroll: código de activación expirado")
	// ErrCodeUsed indica que el código ya fue consumido (es de un solo uso).
	ErrCodeUsed = errors.New("enroll: código de activación ya utilizado")
	// ErrCodeInvalid indica que el código no es consumible (ausente, expirado o
	// usado), sin distinguir la causa. Lo usa el store Postgres.
	ErrCodeInvalid = errors.New("enroll: código de activación inválido")
)

// CodeStore valida y consume códigos de activación de un solo uso. Sus
// promesas comunes a las dos implementaciones las fija la suite
// enrollhelpertest.ContratoCodeStore.
type CodeStore interface {
	// Consume valida que el código exista, no esté expirado ni usado; al éxito lo
	// marca como usado (atómico) y devuelve el tenant asociado. En fallo devuelve
	// uno de los ErrCode* sentinela.
	//
	// El código se compara TAL CUAL: ninguna implementación lo normaliza (ni
	// espacios, ni mayúsculas, ni rechazo del vacío).
	Consume(ctx context.Context, code string) (tenantID string, err error)
}
