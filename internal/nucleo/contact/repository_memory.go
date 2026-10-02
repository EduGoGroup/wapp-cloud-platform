// Porta internal/flujos/contact/repository_memory.go @ 77df20f

package contact

import (
	"context"
	"fmt"
	"sync"

	"github.com/google/uuid"
)

// MemoryResolver implementa Resolver en memoria. Es seguro para uso concurrente (un único cerrojo
// serializa cada Resolve y cada Destino) y está pensado para los tests y para quien no tiene base de
// datos: lo que guarda vive y muere con el valor, no persiste nada. Hoy ningún código de producción
// lo usa (el arranque inyecta PostgresResolver). Se construye con NewMemoryResolver.
//
// Imita la semántica de PostgresResolver: dedup por (tenant, kind, value); aislamiento por tenant
// (N-01), de modo que las mismas refs en dos tenants son dos contactos; y el kind es parte de la
// clave (N-02), de modo que "88887777" como phone_e164 y como wa_lid son dos contactos. Cumple TODO
// lo que fija contacthelpertest.Contrato, que se corre contra esta implementación en unitario. Y añade lo
// que solo ella tiene, el migrador de la fusión (ver Resolve), porque PostgresResolver migra el
// estado en SQL y no usa StateMigrator.
//
// Dónde NO imita a PostgresResolver, a propósito: qué push_name sobrevive (⚠️ aquí gana el último,
// ver Resolve), la atomicidad de la fusión (ver Resolve) y los identificadores mal formados (ver
// Destino).
type MemoryResolver struct {
	// mu serializa Resolve y Destino: con él, el get-or-create es atómico y la ráfaga concurrente
	// con la misma ref acaba en un solo contact_id (R-32).
	mu sync.Mutex
	// migrator migra el flow_state del huérfano al canónico en la fusión; opcional, puede ser nil.
	migrator StateMigrator
	// seq es el orden de alta: cada contacto nuevo recibe el siguiente, y el menor es el canónico
	// de una fusión (el equivalente en memoria del MIN(created_at) de Postgres).
	seq int
	// refIndex mapea "tenant\x00kind\x00value" → contactID: la clave de dedup por ref, con el
	// tenant (N-01) y el kind (N-02) dentro.
	refIndex map[string]string
	// contacts indexa contactID → estado del contacto. Un huérfano fundido se borra de aquí (N-03).
	contacts map[string]*memContact
}

// memContact es el estado en memoria de un contacto: su contact_id, el tenant dueño (Destino lo
// compara para no cruzar tenants), sus refs, el último push_name y su orden de alta.
type memContact struct {
	id       string
	tenantID string
	refs     []Ref
	pushName string
	seq      int
}

// NewMemoryResolver construye un MemoryResolver vacío: sin contactos en ningún tenant. migrator es
// el StateMigrator al que la fusión le pide pasar el estado conversacional del huérfano al canónico
// (ver Resolve); puede ser nil, y entonces no hay estado que migrar y la fusión sigue (el caso de los
// tests que no tocan flow_state). No valida nada: devuelve siempre un resolver y su firma no tiene
// error.
func NewMemoryResolver(migrator StateMigrator) *MemoryResolver {
	return &MemoryResolver{
		migrator: migrator,
		refIndex: make(map[string]string),
		contacts: make(map[string]*memContact),
	}
}

// refIndexKey es la clave de dedup de una ref dentro de un tenant. El separador \x00 no puede
// aparecer en un tenant UUID ni en un kind, así que dos ternas distintas no colisionan.
func refIndexKey(tenantID string, ref Ref) string {
	return tenantID + "\x00" + ref.Kind + "\x00" + ref.Value
}

// Resolve implementa Resolver.Resolve en memoria, bajo el cerrojo del resolver: es un get-or-create
// atómico, y llamadas simultáneas con la misma ref terminan con UN solo contact_id (R-32). Cumple lo
// que promete el puerto —deduplicación de las refs de la entrada, creación, reutilización y fusión
// (R-12 a R-17), ErrNoRefs con la lista vacía tras deduplicar (R-18) y contactID "" con error— y, en
// memoria, lo siguiente.
//
// Precondición: cada Ref viene de NewRef. Resolve no la valida ni la re-normaliza: una Ref vacía o
// no normalizable cuenta como una ref más, no se descarta.
//
// Fusión (R-16, R-17, N-03). Si las refs pertenecen a varios contact_id distintos, el canónico es el
// contacto más antiguo: en memoria, el de alta más temprana, porque cada alta recibe un número de
// orden propio; no el primero de la lista de refs. El desempate por id menor que fija
// Resolver.Resolve existe por determinismo, pero con ese contador nunca llega a darse. Las refs de
// cada huérfano pasan al canónico y el huérfano deja de existir (N-03: Destino de su id da
// ErrContactNotFound). Además, UNA vez por cada huérfano, Resolve llama a
// migrator.MigrateContactID(ctx, tenantID, huérfano, canónico), con el ctx y el tenantID de la
// llamada: fundir tres contactos en uno son dos llamadas, y una Resolve sin fusión (ninguna ref
// existente, una sola, o varias del mismo contacto) no hace ninguna. La política de conflicto de
// sesión (R-17) NO es de Resolve, es del migrador (ver StateMigrator): Resolve solo promete llamarlo
// bien.
//
// Migrador. Con migrator nil no hay estado que migrar: la fusión sigue, sin llamadas y sin error. Si
// el migrador falla, Resolve devuelve contactID "" y un error que envuelve el suyo con %w y con el
// texto exacto «contact: migrar flow_state en fusión: %w», el mismo que usa PostgresResolver; se
// inspecciona con errors.Is contra el error del migrador. A diferencia de Postgres, que revierte la
// fusión entera (una transacción), la memoria NO es atómica ante ese fallo: las refs de ese huérfano
// ya pasaron al canónico y el huérfano ya no existe, así que reintentar no vuelve a llamar al
// migrador por él y su estado queda sin migrar. Qué queda tras un fallo del migrador no es parte del
// contrato: un test que lo provoque afirma el error, no el estado posterior.
//
// push_name (N-04). Si pushName no es vacío, lo guarda para el contacto resultante; si es vacío, no
// toca el nombre. Ningún método del puerto lo devuelve (como en Postgres, no hay lector de
// push_name) y nunca cambia el contact_id. En una fusión, si el canónico no tiene nombre adopta el
// del primer huérfano que lo tenga, y el pushName de la llamada, si no es vacío, sustituye en todo
// caso al que hubiera.
//
// ⚠️ AQUÍ GANA EL ÚLTIMO NOMBRE, Y EN POSTGRES GANA EL PRIMERO (MD-046.5, R-28). Hasta el Plan 046 las
// dos implementaciones coincidían («el último gana»); desde entonces PostgresResolver sella el nombre
// con el centinela push_name_enc IS NULL, para no tomar un row-lock por cada entrante de la ráfaga de
// historial, y por tanto conserva el primer nombre no vacío. Esta implementación NO se alinea, a
// propósito: aquí no hay cifrado, ni row-locks, ni deadlock que evitar, así que copiar el centinela
// sería copiar el precio sin el motivo.
//
// 🔴 La consecuencia, para quien escriba tests: un test sobre este resolver que afirme CUÁL de dos
// nombres sobrevive no dice nada del comportamiento real. Esa propiedad solo se puede clavar contra
// Postgres, en los procesos de F9; por eso contacthelpertest.Contrato no la afirma.
//
// Errores. Solo dos: ErrNoRefs, y el del migrador, envuelto como queda dicho. No hay almacén, cifrado
// ni red que puedan fallar.
func (r *MemoryResolver) Resolve(ctx context.Context, tenantID string, refs []Ref, pushName string) (string, error) {
	refs = dedupeRefs(refs)
	if len(refs) == 0 {
		return "", ErrNoRefs
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	found := r.distinctContactIDs(tenantID, refs)

	var canonical string
	switch len(found) {
	case 0:
		canonical = r.newContact(tenantID)
	default:
		canonical = r.pickCanonical(found)
		for _, orphan := range found {
			if orphan == canonical {
				continue
			}
			if err := r.fuse(ctx, tenantID, orphan, canonical); err != nil {
				return "", err
			}
		}
	}

	for _, ref := range refs {
		r.attach(tenantID, canonical, ref)
	}
	// ⚠️ AQUÍ GANA EL ÚLTIMO NOMBRE, Y EN POSTGRES GANA EL PRIMERO. Hasta el Plan 046 · T4.2 las dos
	// implementaciones coincidían («último gana»); desde entonces el resolver de Postgres escribe el
	// sobre con centinela `push_name_enc IS NULL` —MD-046.5, para no tomar un row-lock por cada
	// entrante de la ráfaga de historial— y por tanto CONSERVA el primer nombre no vacío. Esta
	// implementación NO se alinea a propósito: aquí no hay cifrado, no hay row-locks y no hay
	// deadlock que evitar, así que copiar el centinela sería copiar el precio sin el motivo.
	//
	// 🔴 La consecuencia, para quien escriba tests: un test sobre este resolver que afirme algo sobre
	// CUÁL de dos nombres sobrevive NO dice nada del comportamiento real. Esa propiedad solo se puede
	// clavar contra Postgres (los procesos de F9; en el código viejo,
	// backfill_push_name_integration_test.go, el caso «gana el primer nombre no vacío»).
	if pushName != "" {
		r.contacts[canonical].pushName = pushName
	}
	return canonical, nil
}

// distinctContactIDs devuelve, en orden estable (el de refs), los contact_id distintos ya mapeados
// por alguna de las refs. Debe correr bajo r.mu.
func (r *MemoryResolver) distinctContactIDs(tenantID string, refs []Ref) []string {
	seen := make(map[string]struct{})
	var ids []string
	for _, ref := range refs {
		if cid, ok := r.refIndex[refIndexKey(tenantID, ref)]; ok {
			if _, dup := seen[cid]; !dup {
				seen[cid] = struct{}{}
				ids = append(ids, cid)
			}
		}
	}
	return ids
}

// pickCanonical elige el contact_id más antiguo (menor seq; desempate por id para determinismo),
// tal como PostgresResolver usa MIN(created_at). Con el contador seq el empate no llega a darse,
// pero el desempate se conserva para que la elección no dependa del orden de ids. Debe correr bajo
// r.mu.
func (r *MemoryResolver) pickCanonical(ids []string) string {
	canonical := ids[0]
	for _, id := range ids[1:] {
		c, cok := r.contacts[canonical]
		o, ook := r.contacts[id]
		if !ook {
			continue
		}
		if !cok || o.seq < c.seq || (o.seq == c.seq && id < canonical) {
			canonical = id
		}
	}
	return canonical
}

// newContact crea un contact_id opaco nuevo (UUID, como el gen_random_uuid() de Postgres), vacío de
// refs y con el siguiente orden de alta. Debe correr bajo r.mu.
func (r *MemoryResolver) newContact(tenantID string) string {
	id := uuid.NewString()
	r.seq++
	r.contacts[id] = &memContact{id: id, tenantID: tenantID, seq: r.seq}
	return id
}

// attach ata una ref al contact_id canónico si no lo estaba ya: una ref ya indexada es del canónico
// (la fusión la re-apuntó) y no se duplica en su lista. Debe correr bajo r.mu.
func (r *MemoryResolver) attach(tenantID, contactID string, ref Ref) {
	k := refIndexKey(tenantID, ref)
	if _, ok := r.refIndex[k]; ok {
		return
	}
	r.refIndex[k] = contactID
	c := r.contacts[contactID]
	c.refs = append(c.refs, ref)
}

// fuse funde el contact_id huérfano en el canónico: re-apunta sus refs, hereda su push_name si el
// canónico no tiene, borra el huérfano (N-03) y, por último, migra su flow_state. El orden importa
// y explica la no-atomicidad que documenta Resolve: cuando el migrador falla, el huérfano ya no
// existe, y un reintento no lo vuelve a encontrar. Debe correr bajo r.mu.
func (r *MemoryResolver) fuse(ctx context.Context, tenantID, orphan, canonical string) error {
	oc, ok := r.contacts[orphan]
	if !ok {
		return nil
	}
	cc := r.contacts[canonical]
	for _, ref := range oc.refs {
		r.refIndex[refIndexKey(tenantID, ref)] = canonical
		cc.refs = append(cc.refs, ref)
	}
	if oc.pushName != "" && cc.pushName == "" {
		cc.pushName = oc.pushName
	}
	delete(r.contacts, orphan)
	if r.migrator != nil {
		if err := r.migrator.MigrateContactID(ctx, tenantID, orphan, canonical); err != nil {
			// Texto observable: el mismo envoltorio que PostgresResolver (diseno.md §5).
			return fmt.Errorf("contact: migrar flow_state en fusión: %w", err)
		}
	}
	return nil
}

// Destino implementa Resolver.Destino en memoria, bajo el cerrojo del resolver: devuelve la ref
// enviable del contacto contactID dentro de tenantID, con el value NORMALIZADO tal como se guardó,
// no el crudo con el que llegó (R-23). Elige, entre las refs direccionables, por la preferencia del
// puerto phone_e164 > wa_username > wa_lid (R-19): un contacto con teléfono y LID da el teléfono, y
// uno solo con LID, el LID. wa_username figura en el orden pero hoy no es direccionable (R-20).
//
// Errores, con la Ref cero:
//   - ErrContactNotFound, envuelto con %w y el contactID entre comillas (%q), si el contacto no
//     existe, es de otro tenant (N-01) o es el de un huérfano que una fusión ya borró (R-21, N-03);
//   - ErrNoDestino, sin envolver, si el contacto existe pero ninguna de sus refs es direccionable
//     (p. ej. solo un wa_username, R-20).
//
// Un tenantID o un contactID mal formados (no UUID) dan ErrContactNotFound: la memoria no parsea
// nada y los trata como claves opacas. Postgres da en ese caso un error de parseo (ver Resolver):
// quien llama pasa siempre UUID bien formados.
func (r *MemoryResolver) Destino(_ context.Context, tenantID, contactID string) (Ref, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Un contacto de otro tenant se trata como inexistente (N-01): no se revela que el id existe.
	c, ok := r.contacts[contactID]
	if !ok || c.tenantID != tenantID {
		return Ref{}, fmt.Errorf("%w: %q", ErrContactNotFound, contactID)
	}
	return pickDestino(c.refs)
}
