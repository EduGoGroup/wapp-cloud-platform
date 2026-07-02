// Package contact define la identidad de contacto FLEXIBLE del motor de flujos
// (Plan 010). Un contacto se identifica por una o más referencias
// contact_ref {kind, value} que resuelven a un contact_id opaco (UUID) asignado
// por la capa de resolución. La dedup se hace por (tenant_id, kind, value)
// (design.md §2.1).
//
// Este corte (T0) cubre SOLO el dominio: los tipos (Ref, Contact), las
// constantes de kind, la NORMALIZACIÓN del value y la VALIDACIÓN del kind. El
// Resolver (Resolve/Destino) y su repositorio Postgres/memory llegan en T4
// (design.md §4). No hay aquí lógica de resolución ni acceso a BD.
package contact

import (
	"errors"
	"fmt"
	"strings"
)

// Kinds de contact_ref soportados (design.md §10.B). phone_e164 y wa_lid están
// EN USO; wa_username queda PREPARADO (constante + validación) pero NO se ejerce
// hasta que WhatsApp defina el formato de username.
const (
	// KindPhoneE164 identifica al contacto por su número en formato E.164
	// normalizado (solo dígitos, sin '+' ni separadores).
	KindPhoneE164 = "phone_e164"
	// KindWALID identifica al contacto por su LID de WhatsApp (parte de usuario
	// numérica en forma canónica, sin el servidor '@lid').
	KindWALID = "wa_lid"
	// KindWAUsername identifica al contacto por su username de WhatsApp.
	// PREPARADO pero no ejercido en este corte (design.md §10.B).
	KindWAUsername = "wa_username"
)

// maxE164Digits es el máximo de dígitos de un número E.164 (recomendación E.164:
// hasta 15 dígitos, sin contar el '+').
const maxE164Digits = 15

// ErrInvalidRef es el error base de una referencia de contacto inválida (kind
// desconocido o value que no normaliza). Se inspecciona con errors.Is.
var ErrInvalidRef = errors.New("contact_ref inválida")

// Ref es una referencia a un contacto: el par (Kind, Value) donde Value ya está
// NORMALIZADO. La unidad de deduplicación es (tenant_id, Kind, Value)
// (design.md §2.1). Construir siempre vía NewRef para garantizar la
// normalización.
type Ref struct {
	Kind  string
	Value string
}

// Contact es la vista de dominio de un contacto: su contact_id opaco (UUID), las
// refs que lo identifican y el último push_name visto. Lo materializa el
// Resolver (T4); aquí solo se define el tipo.
type Contact struct {
	// ID es el contact_id opaco y estable (UUID) con el que opera el motor.
	ID string
	// Refs son todas las referencias conocidas del contacto (varios kinds).
	Refs []Ref
	// PushName es el último nombre de perfil visto (dato de negocio, opcional).
	PushName string
}

// ValidateKind comprueba que kind es uno de los soportados (design.md §10.B).
// Devuelve ErrInvalidRef si es desconocido.
func ValidateKind(kind string) error {
	switch kind {
	case KindPhoneE164, KindWALID, KindWAUsername:
		return nil
	default:
		return fmt.Errorf("%w: kind desconocido %q", ErrInvalidRef, kind)
	}
}

// Normalize valida el kind y devuelve el value normalizado (design.md §10.B):
//   - phone_e164: solo dígitos, descartando '+', espacios, guiones, paréntesis y
//     puntos; hasta maxE164Digits dígitos.
//   - wa_lid: la parte de usuario del LID en forma canónica (se descarta el
//     servidor '@lid'/'@...' y los sufijos de agente '_N' y dispositivo ':N');
//     resulta numérica.
//   - wa_username: en minúsculas y sin espacios de borde (PREPARADO, no ejercido).
//
// Devuelve ErrInvalidRef si el kind es desconocido o el value queda vacío o no
// cumple el formato esperado.
func Normalize(kind, value string) (string, error) {
	if err := ValidateKind(kind); err != nil {
		return "", err
	}
	switch kind {
	case KindPhoneE164:
		return normalizePhone(value)
	case KindWALID:
		return normalizeLID(value)
	case KindWAUsername:
		return normalizeUsername(value)
	default:
		// Inalcanzable: ValidateKind ya filtró los kinds desconocidos.
		return "", fmt.Errorf("%w: kind desconocido %q", ErrInvalidRef, kind)
	}
}

// NewRef construye una Ref con el value normalizado a partir de (kind, value).
// Es el único constructor recomendado de Ref (garantiza dedup consistente).
func NewRef(kind, value string) (Ref, error) {
	norm, err := Normalize(kind, value)
	if err != nil {
		return Ref{}, err
	}
	return Ref{Kind: kind, Value: norm}, nil
}

// normalizePhone deja solo los dígitos del número (E.164 sin '+' ni separadores).
func normalizePhone(value string) (string, error) {
	var b strings.Builder
	b.Grow(len(value))
	for _, r := range value {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	digits := b.String()
	// Higiene (design.md §8/§10.I): los mensajes de error NO embeben el value
	// crudo (PII); suben a runtime_engine.go como "error", err. Se describe la
	// causa con métricas no reversibles (longitud), nunca el número.
	if digits == "" {
		return "", fmt.Errorf("%w: phone_e164 sin dígitos", ErrInvalidRef)
	}
	if len(digits) > maxE164Digits {
		return "", fmt.Errorf("%w: phone_e164 con %d dígitos excede el máximo %d",
			ErrInvalidRef, len(digits), maxE164Digits)
	}
	return digits, nil
}

// normalizeLID extrae la parte de usuario canónica del LID (numérica), sin el
// servidor '@lid' ni los sufijos de agente '_N' o dispositivo ':N'.
func normalizeLID(value string) (string, error) {
	v := strings.TrimSpace(value)
	// Descarta el servidor: "<user>@lid" -> "<user>".
	if at := strings.IndexByte(v, '@'); at >= 0 {
		v = v[:at]
	}
	// Descarta el sufijo de agente "_N".
	if us := strings.IndexByte(v, '_'); us >= 0 {
		v = v[:us]
	}
	// Descarta el sufijo de dispositivo ":N".
	if colon := strings.IndexByte(v, ':'); colon >= 0 {
		v = v[:colon]
	}
	v = strings.TrimSpace(v)
	// Higiene (design.md §8/§10.I): sin el value crudo (PII) en el error.
	if v == "" {
		return "", fmt.Errorf("%w: wa_lid vacío", ErrInvalidRef)
	}
	for _, r := range v {
		if r < '0' || r > '9' {
			return "", fmt.Errorf("%w: wa_lid con parte de usuario no numérica (longitud %d)", ErrInvalidRef, len(v))
		}
	}
	return v, nil
}

// normalizeUsername pasa a minúsculas y recorta espacios de borde.
func normalizeUsername(value string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(value))
	if v == "" {
		return "", fmt.Errorf("%w: wa_username vacío: %q", ErrInvalidRef, value)
	}
	return v, nil
}
