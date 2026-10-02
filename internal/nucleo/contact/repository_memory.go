// Porta internal/flujos/contact/repository_memory.go @ 77df20f

package contact

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// MemoryResolver implementa Resolver en memoria. Es seguro para uso concurrente (un único cerrojo
// serializa cada Resolve y cada Destino) y está pensado para los tests y para quien no tiene base de
// datos: lo que guarda vive y muere con el valor, no persiste nada. Hoy ningún código de producción
// lo usa (el arranque inyecta PostgresResolver). Se construye con NewMemoryResolver.
//
// Imita la semántica de PostgresResolver: dedup por (tenant, kind, value); aislamiento por tenant
// (N-01), de modo que las mismas refs en dos tenants son dos contactos; y el kind es parte de la
// clave (N-02), de modo que "88887777" como phone_e164 y como wa_lid son dos contactos. Cumple TODO
// lo que fija contacttest.Contrato, que se corre contra esta implementación en unitario. Y añade lo
// que solo ella tiene, el migrador de la fusión (ver Resolve), porque PostgresResolver migra el
// estado en SQL y no usa StateMigrator.
//
// Dónde NO imita a PostgresResolver, a propósito: qué push_name sobrevive (⚠️ aquí gana el último,
// ver Resolve), la atomicidad de la fusión (ver Resolve) y los identificadores mal formados (ver
// Destino).
type MemoryResolver struct{}

// NewMemoryResolver construye un MemoryResolver vacío: sin contactos en ningún tenant. migrator es
// el StateMigrator al que la fusión le pide pasar el estado conversacional del huérfano al canónico
// (ver Resolve); puede ser nil, y entonces no hay estado que migrar y la fusión sigue (el caso de los
// tests que no tocan flow_state). No valida nada: devuelve siempre un resolver y su firma no tiene
// error.
func NewMemoryResolver(migrator StateMigrator) *MemoryResolver {
	panic(pendiente.Implementar("contact.NewMemoryResolver"))
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
// Postgres, en los procesos de F9; por eso contacttest.Contrato no la afirma.
//
// Errores. Solo dos: ErrNoRefs, y el del migrador, envuelto como queda dicho. No hay almacén, cifrado
// ni red que puedan fallar.
func (r *MemoryResolver) Resolve(ctx context.Context, tenantID string, refs []Ref, pushName string) (string, error) {
	panic(pendiente.Implementar("contact.MemoryResolver.Resolve"))
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
//   - ErrNoDestino si el contacto existe pero ninguna de sus refs es direccionable (p. ej. solo un
//     wa_username, R-20).
//
// Un tenantID o un contactID mal formados (no UUID) dan ErrContactNotFound: la memoria no parsea
// nada y los trata como claves opacas. Postgres da en ese caso un error de parseo (ver Resolver):
// quien llama pasa siempre UUID bien formados.
func (r *MemoryResolver) Destino(ctx context.Context, tenantID, contactID string) (Ref, error) {
	panic(pendiente.Implementar("contact.MemoryResolver.Destino"))
}
