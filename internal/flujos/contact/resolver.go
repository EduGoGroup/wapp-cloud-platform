package contact

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// lidServer es el servidor JID de los LID de WhatsApp (whatsmeow
// types.HiddenUserServer). Se usa para formatear un wa_lid como destino
// direccionable ("<lid>@lid"); ver Ref.Sendable.
const lidServer = "lid"

// ErrNoRefs lo devuelve Resolve cuando no recibe ninguna referencia utilizable
// (todas vacías o no normalizables). Se inspecciona con errors.Is.
var ErrNoRefs = errors.New("contact: se requiere al menos una contact_ref")

// ErrNoDestino lo devuelve Destino (o Ref.Sendable) cuando el contacto no tiene
// ninguna referencia DIRECCIONABLE (p. ej. solo un wa_username, aún no enviable,
// o ninguna ref). El runtime lo trata como fallo claro, sin emitir un `to`
// inválido (design.md §10.E, R4). Se inspecciona con errors.Is.
var ErrNoDestino = errors.New("contact: sin destino enviable para el contact_id")

// ErrContactNotFound lo devuelve Destino cuando el contact_id no existe (o no
// pertenece al tenant). Se inspecciona con errors.Is.
var ErrContactNotFound = errors.New("contact: contact_id no encontrado")

// Resolver encapsula la tabla de identidad `contacts`: traduce entre las
// referencias del mundo (número, LID, username) y el contact_id opaco con el que
// opera el motor de flujos (design.md §4).
type Resolver interface {
	// Resolve devuelve (creando si hace falta) el contact_id de un conjunto de
	// refs del MISMO contacto. Hace upsert de cada ref: si ALGUNA ya mapea a un
	// contact_id, reusa ese y ata las refs faltantes; si varias mapean a
	// contact_id DISTINTOS, los FUNDE (canónico = el más antiguo) y migra el
	// flow_state del huérfano (fusión + reconciliación tardía, design.md §5). Si
	// ninguna existe, crea un contact_id nuevo con todas las refs. Es determinista
	// por (tenant, ref). Si pushName no es vacío, actualiza el push_name.
	Resolve(ctx context.Context, tenantID string, refs []Ref, pushName string) (contactID string, err error)
	// Destino devuelve una ref ENVIABLE del contact_id, eligiendo por preferencia
	// phone_e164 > wa_username > wa_lid (design.md §10.E). Devuelve ErrNoDestino
	// si no hay ninguna direccionable y ErrContactNotFound si el id no existe.
	Destino(ctx context.Context, tenantID, contactID string) (Ref, error)
}

// StateMigrator re-clava el estado conversacional de un contact_id a otro dentro
// del mismo tenant. Lo usa el MemoryResolver durante la fusión para migrar el
// flow_state del contact_id huérfano al canónico (design.md §5, §10.D). El
// PostgresResolver NO lo usa: hace esa migración en SQL dentro de su MISMA
// transacción (atomicidad). Política de conflicto: si el canónico ya tiene
// estado en esa sesión, se CONSERVA el del canónico y se descarta el del
// huérfano (el canónico es la identidad autoritativa; ver PostgresResolver).
type StateMigrator interface {
	MigrateContactID(ctx context.Context, tenantID, fromContactID, toContactID string) error
}

// destinoPref fija la preferencia de destino enviable (menor = mejor):
// phone_e164 > wa_username > wa_lid (design.md §10.E). wa_username figura en el
// orden pero hoy NO es direccionable (Sendable lo rechaza), así que en la
// práctica se degrada a wa_lid.
var destinoPref = map[string]int{
	KindPhoneE164:  0,
	KindWAUsername: 1,
	KindWALID:      2,
}

// Sendable devuelve la cadena de destino que el Edge sabe direccionar para esta
// Ref (ADR-0005: el cloud resuelve el destino real): el número crudo para
// phone_e164 (el Edge le añade "@s.whatsapp.net") y "<lid>@lid" para wa_lid.
// wa_username todavía NO es direccionable → ErrNoDestino (design.md §10.E).
func (r Ref) Sendable() (string, error) {
	switch r.Kind {
	case KindPhoneE164:
		return r.Value, nil
	case KindWALID:
		return r.Value + "@" + lidServer, nil
	default:
		return "", fmt.Errorf("%w: kind %q no direccionable", ErrNoDestino, r.Kind)
	}
}

// RefsFrom construye las Ref de un entrante a partir de la identidad enriquecida
// del Edge (from_pn/from_lid, R5) y, como último recurso, del JID crudo `from`
// (infiere el kind por el servidor del JID). Descarta en silencio las que no
// normalizan (tolerancia: el mapeo LID↔número puede no existir aún, design.md
// §5). Puede devolver una lista vacía si nada es utilizable.
func RefsFrom(fromPn, fromLid, from string) []Ref {
	refs := make([]Ref, 0, 2)
	if fromPn != "" {
		if ref, err := NewRef(KindPhoneE164, fromPn); err == nil {
			refs = append(refs, ref)
		}
	}
	if fromLid != "" {
		if ref, err := NewRef(KindWALID, fromLid); err == nil {
			refs = append(refs, ref)
		}
	}
	if len(refs) == 0 && from != "" {
		kind := KindPhoneE164
		if strings.Contains(from, "@"+lidServer) {
			kind = KindWALID
		}
		if ref, err := NewRef(kind, from); err == nil {
			refs = append(refs, ref)
		}
	}
	return refs
}

// pickDestino elige la ref DIRECCIONABLE de mejor preferencia (design.md §10.E).
// Descarta las no enviables (p. ej. wa_username). ErrNoDestino si ninguna sirve.
func pickDestino(refs []Ref) (Ref, error) {
	best := Ref{}
	bestRank := -1
	for _, ref := range refs {
		rank, known := destinoPref[ref.Kind]
		if !known {
			continue
		}
		if _, err := ref.Sendable(); err != nil {
			continue
		}
		if bestRank == -1 || rank < bestRank {
			bestRank = rank
			best = ref
		}
	}
	if bestRank == -1 {
		return Ref{}, ErrNoDestino
	}
	return best, nil
}

// dedupeRefs elimina refs repetidas por (kind, value), preservando el orden.
func dedupeRefs(refs []Ref) []Ref {
	if len(refs) < 2 {
		return refs
	}
	seen := make(map[Ref]struct{}, len(refs))
	out := make([]Ref, 0, len(refs))
	for _, ref := range refs {
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		out = append(out, ref)
	}
	return out
}
