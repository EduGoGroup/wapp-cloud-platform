// Porta internal/flujos/store/repository_postgres.go @ c0c0c03
//
// Trozo de repository_postgres.go (05 E-13): public.intakes y public.intake_items.
// Las reglas comunes del adaptador están en la cabecera de repository_postgres.go.
//
// Los auxiliares no exportados del viejo (execer, intakeItemCols, reservedSKUPrefix,
// replaceIntakeItemsTx, insertIntakeItems, esUUID, cabeceraIntakeCols y
// escanearCabeceraIntake) nacen con el verde, con nombre en inglés (E-11). Dos reglas
// suyas son del contrato y por eso se escriben aquí:
//
//   - el prefijo RESERVADO de los skus de la plataforma es el literal "_" (hoy solo
//     la línea de envío, D-041.11). Es el MISMO literal que el del dominio de
//     solicitudes y se repite aquí en vez de importarlo: el almacén del motor de
//     flujos no depende del dominio de solicitudes para escribir una tabla. Lo que
//     impide que diverjan es un test, que los compara;
//   - las dos lecturas de cabecera (GetOpenIntake y GetIntakeByEvent) comparten UNA
//     proyección: tienen que devolver exactamente la misma foto.
//
// 🔴 MUTANTES (nivel complejo): CloseIntake lleva las guardas que la suite tiene que
// morder una a una — el filtro por tenant, por contacto y por status = 'open', el
// ORDER BY created_at DESC, el FOR UPDATE, el COALESCE de event_id y el reemplazo (no
// inserción) de las líneas.

package store

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// UpsertIntake inserta o actualiza (upsert por id) la solicitud en public.intakes
// (Plan 016 · T0/T2). Idempotente por o.ID. ExpiresAt zero se materializa como
// NULL. created_at/updated_at usan now() (updated_at se refresca en el UPDATE).
//
// event_id (D-043.21, migración 0054) se escribe al NACER la fila y en el UPDATE
// va protegido con COALESCE(intakes.event_id, EXCLUDED.event_id): un event_id ya
// declarado NO se pisa jamás —ni con otro valor ni con NULL—, y un NULL legado
// (fila pre-0054) SÍ se estampa cuando el proyector reusa la solicitud con el
// evento en la mano. La política de QUÉ estampar es del proyector
// (cart.ensureOpenIntake); esta sentencia solo garantiza que ninguna escritura
// pueda des-declarar a un padre.
//
// customer_note NO se escribe: ni el INSERT ni el UPDATE mencionan la columna, así que
// nace con su DEFAULT (”) y un upsert sobre una solicitud ya cerrada no puede borrar
// la nota que puso el cierre. El CreatedAt, el UpdatedAt y el CustomerNote del
// argumento se ignoran.
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "store: upsert solicitud: %w"
func (r *PostgresRepository) UpsertIntake(ctx context.Context, o Intake) error {
	panic(pendiente.Implementar("store.PostgresRepository.UpsertIntake"))
}

// GetOpenIntake devuelve la solicitud "open" del contacto para (tenantID, contactID);
// found=false sin error si no hay (Plan 016 · T2/T3). Usa el índice intakes_open_idx.
// Si hubiera varias "open", la más reciente (ORDER BY created_at DESC LIMIT 1). La
// proyección NO trae customer_note: la cabecera sale con CustomerNote "".
// expires_at y event_id NULL salen como el cero de Go.
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "store: leer solicitud abierta: %w"
func (r *PostgresRepository) GetOpenIntake(ctx context.Context, tenantID, contactID string) (Intake, bool, error) {
	panic(pendiente.Implementar("store.PostgresRepository.GetOpenIntake"))
}

// GetIntakeByEvent implementa IntakeReader: la solicitud que declara `eventID` como
// padre, SIN filtro de estado (D-044.46). La sirve el índice único parcial
// intakes_event_id_uidx, que además garantiza que la fila sea a lo sumo una.
//
// Un eventID que no parsea como UUID no puede estar en la columna (es de tipo
// `uuid`): mismo destino que "no hay" —found=false, sin error—, sin molestar a
// Postgres con un 22P02. Es el mismo guard, y por la misma razón, que el de
// intakes.Postgres.AbandonByEvent, la escritura gemela de esta lectura.
//
// Misma proyección que GetOpenIntake (sin customer_note): las dos lecturas devuelven
// exactamente la misma foto de la misma fila.
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "store: leer la solicitud del evento: %w"
func (r *PostgresRepository) GetIntakeByEvent(ctx context.Context, tenantID, eventID string) (Intake, bool, error) {
	panic(pendiente.Implementar("store.PostgresRepository.GetIntakeByEvent"))
}

// ListIntakeItems devuelve las líneas de la solicitud en el orden en que las ve el
// cliente (added_at, id), que es el MISMO ORDEN Y LA MISMA PROYECCIÓN que usa
// intakes.itemsOf: dos lecturas de la misma tabla que se contradijeran en el orden
// enseñarían el pedido de dos formas distintas según por dónde se mire.
//
// El UUID se valida ANTES de consultar para no depender del error 22P02 de Postgres
// (el repositorio en memoria no lo daría, y las dos implementaciones tienen que
// contestar lo mismo a la misma pregunta).
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "store: listar líneas de solicitud: id %q inválido: %w"
//   - "store: listar líneas de solicitud: %w"
//   - "store: cerrar filas de líneas: %w"
//   - "store: escanear línea de solicitud: %w"
//   - "store: iterar líneas de solicitud: %w"
func (r *PostgresRepository) ListIntakeItems(ctx context.Context, intakeID string) (out []IntakeItem, err error) {
	panic(pendiente.Implementar("store.PostgresRepository.ListIntakeItems"))
}

// ReplaceIntakeItems deja las líneas de cliente de la solicitud EXACTAMENTE en
// `items`, en UNA transacción (Plan 043 · Ola 3): borrar y volver a escribir tienen
// que ser un solo acto o existiría un instante en el que el pedido no tiene líneas,
// y ese instante lo puede leer el CRM.
//
// El DELETE excluye el prefijo reservado (`left(sku, 1) <> "_"`): las líneas de la
// plataforma —hoy la de envío, D-041.11— llevan su precio puesto a mano y no son del
// carrito. Después, UN INSERT multi-fila con las líneas en el orden de `items` (la
// lectura ordena por (added_at, id) y el BIGSERIAL sigue el orden de los VALUES);
// len(items)==0 no inserta nada, así que BORRA las de cliente. customization viaja
// SIEMPRE, aunque esté vacía (NOT NULL: su vacío es «sin personalización», D-041.17);
// added_at es el DEFAULT now() y el AddedAt del argumento se ignora. No toca la
// cabecera de la solicitud.
//
// Textos de error (literales, con el error de origen envuelto en %w; los comparte
// CloseIntake, que reemplaza las líneas con el mismo camino):
//   - "store: retirar líneas de solicitud: %w"
//   - "store: insertar líneas de solicitud: %w"
func (r *PostgresRepository) ReplaceIntakeItems(ctx context.Context, intakeID string, items []IntakeItem) error {
	panic(pendiente.Implementar("store.PostgresRepository.ReplaceIntakeItems"))
}

// MarkIntakeStatus transiciona el estado de una solicitud (por id) y fija su total,
// refrescando updated_at (Plan 016 · T2/T3). status es "closed" | "cancelled" |
// "expired", pero no se valida. Busca por id y nada más (no acota por tenant); si la
// solicitud no existe es un no-op sin error. No toca las líneas ni ninguna otra
// columna.
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "store: marcar estado de solicitud: %w"
func (r *PostgresRepository) MarkIntakeStatus(ctx context.Context, intakeID, status string, total float64) error {
	panic(pendiente.Implementar("store.PostgresRepository.MarkIntakeStatus"))
}

// CloseIntake cierra ATÓMICAMENTE la solicitud abierta del contacto e inserta sus
// líneas en la MISMA transacción (Plan 027 · Ola 1 · T4, cierra H4), vía el helper
// único postgres.WithTx (rollback inmune a panic + retry 40P01/40001). Bloquea la
// solicitud "open" con FOR UPDATE: dos cierres concurrentes del mismo contacto se
// serializan (el segundo la ve ya "closed" y no crea otra). Si no había solicitud
// abierta, crea una "closed" coherente. Garantiza que una solicitud closed nunca quede
// sin líneas.
//
// Devuelve el ID de la solicitud cerrada porque quien cierra necesita saber SOBRE
// QUÉ cerró: es lo que permite colgarle la revisión 1 (ADR-0031 §3). Sin él, el
// llamante tendría que releer "la última cerrada de este contacto", que es una
// carrera con el siguiente carrito.
//
// CON solicitud "open" del (tenant, contacto) —la más reciente por created_at—:
// status 'closed', total y customer_note los del argumento, updated_at = now();
// event_id con COALESCE (rellena un NULL legado, JAMÁS pisa un padre declarado);
// tenant, contacto, sesión y created_at quedan como estaban (in.SessionID NO se usa).
// SIN ella: nace una fila 'closed' con id uuid nuevo, in.SessionID, in.EventID, total
// y customer_note. Una solicitud en cualquier otro estado ni se cierra ni cuenta.
//
// En los dos casos las líneas se REEMPLAZAN por in.Items con la regla de
// ReplaceIntakeItems (las del prefijo reservado sobreviven; in.Items vacío deja la
// solicitud sin líneas de cliente), en la misma transacción. Si la transacción falla
// devuelve ("", err) y no queda nada escrito.
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "store: insertar solicitud cerrada: %w"
//   - "store: bloquear solicitud abierta: %w"
//   - "store: cerrar solicitud: %w"
func (r *PostgresRepository) CloseIntake(ctx context.Context, in IntakeClose) (string, error) {
	panic(pendiente.Implementar("store.PostgresRepository.CloseIntake"))
}
