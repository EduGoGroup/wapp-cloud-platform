// Porta internal/flujos/store/repository_memory.go @ c0c0c03
//
// Trozo de repository_memory.go (05 E-13): las solicitudes del carrito (intakes) y
// sus líneas (intake_items) en el gemelo en memoria. Las reglas comunes del gemelo
// están en la cabecera de repository_memory.go.
//
// Lo que este trozo NO puede imitar, porque es del esquema y no del puerto: el NOT
// NULL y la clave foránea de intakes.event_id (aquí una solicitud puede nacer sin
// padre, como una fila legada pre-0054) y el índice único parcial
// intakes_event_id_uidx (aquí dos solicitudes pueden declarar el mismo evento). Las
// reglas que dependen de eso —el relleno de un padre vacío, el desempate de
// GetIntakeByEvent— las prueba el test propio de este fichero, no la suite común.
//
// 🔧 Divergencia deliberada con el viejo (mejora): con VARIAS solicitudes "open" del
// mismo (tenant, contacto) —que el negocio no produce— GetOpenIntake y CloseIntake
// eligen la MÁS RECIENTE por created_at, como el `ORDER BY created_at DESC LIMIT 1`
// del adaptador Postgres. El viejo devolvía «la primera coincidente» del recorrido de
// un mapa, o sea una distinta en cada vuelta.

package store

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// UpsertIntake implementa Repository: inserta o actualiza (por ID) la solicitud,
// imitando el upsert en public.intakes (Plan 016 · T0). Idempotente por o.ID: en el
// alta fija created_at/updated_at al instante del reloj (SetClock); en la
// actualización preserva el created_at almacenado y refresca updated_at (misma
// semántica que el DEFAULT + ON CONFLICT del Postgres). El CreatedAt y el UpdatedAt
// del argumento se ignoran siempre.
//
// La actualización pisa tenant, contacto, sesión, estado, total y ExpiresAt con los
// del argumento, y deja DOS campos como estaban:
//
//   - EventID: misma semántica que el COALESCE(intakes.event_id, EXCLUDED.event_id)
//     del Postgres (D-043.21): un padre ya declarado JAMÁS se pisa —ni con otro valor
//     ni con ""—; un vacío legado sí se estampa con el del argumento;
//   - CustomerNote: UpsertIntake NO la escribe, ni en el alta (nace "") ni en la
//     actualización (se conserva la que hubiera puesto el cierre). 🔧 Divergencia con
//     el viejo, que guardaba la del argumento y por tanto BORRABA la nota de una
//     solicitud cerrada: es lo contrario de lo que promete store.Intake.CustomerNote y
//     de lo que hace el UPDATE del Postgres, que ni menciona la columna.
//
// Nunca devuelve error.
func (r *MemoryRepository) UpsertIntake(ctx context.Context, o Intake) error {
	panic(pendiente.Implementar("store.MemoryRepository.UpsertIntake"))
}

// GetOpenIntake implementa Repository: devuelve la solicitud "open" del contacto para
// (tenantID, contactID); found=false sin error si no hay (Plan 016 · T2/T3).
// Identidad de negocio: UNA solicitud "open" por (tenant_id, contact_id) (design.md
// §3.4). Si hubiera varias devuelve la MÁS RECIENTE por created_at, como el
// `ORDER BY created_at DESC LIMIT 1` del Postgres (el viejo devolvía «la primera
// coincidente» del recorrido del mapa; ver la cabecera del fichero).
//
// La cabecera sale con CustomerNote "" aunque la fila la tenga: es la MISMA
// proyección que la del Postgres, que no lee esa columna (store.Intake.CustomerNote).
// Quien quiera ver la nota en un test usa Intakes(). Nunca devuelve error.
func (r *MemoryRepository) GetOpenIntake(ctx context.Context, tenantID, contactID string) (Intake, bool, error) {
	panic(pendiente.Implementar("store.MemoryRepository.GetOpenIntake"))
}

// GetIntakeByEvent implementa IntakeReader: la solicitud del tenant que declara
// `eventID` como padre, SIN filtro de estado (D-044.46, hallazgo #24).
//
// En producción el índice único parcial intakes_event_id_uidx garantiza que sea a lo
// sumo UNA; aquí no hay índice que lo imponga, así que ante varias se devuelve la
// MÁS ANTIGUA (created_at, id) — la que ya estaba, que es justo la que los dos
// productores tienen que reusar. Sin ese desempate el resultado dependería del orden
// de recorrido del mapa y un test podría salir verde o rojo según la vuelta.
//
// eventID vacío ⇒ no hay nada que buscar (misma puerta que AbandonByEvent): la
// cadena vacía es el NULL de las filas legadas pre-0054, y "todas las legadas" no es
// la respuesta a "¿qué tiene ESTE evento?".
//
// ⚠️ ASIMETRÍA CONOCIDA CON EL POSTGRES, y escrita para que nadie la descubra
// depurando: allí la columna es de tipo `uuid` y un eventID que no parsea sale
// found=false por el guard; aquí los ids son cadenas opacas y se comparan tal cual.
// Solo diverge para ids que en la base NO PUEDEN existir.
//
// La cabecera sale con CustomerNote "", igual que en GetOpenIntake y por lo mismo: las
// dos lecturas devuelven exactamente la misma foto. Nunca devuelve error.
func (r *MemoryRepository) GetIntakeByEvent(ctx context.Context, tenantID, eventID string) (Intake, bool, error) {
	panic(pendiente.Implementar("store.MemoryRepository.GetIntakeByEvent"))
}

// ListIntakeItems implementa Repository: devuelve las líneas de la solicitud en el
// orden en que se escribieron (el del carrito), que es el mismo que da el
// PostgresRepository al ordenar por (added_at, id).
//
// Valida el UUID aunque aquí las claves sean un mapa de cadenas y no haga ninguna
// falta técnica: el camino Postgres rechaza un id malformado y los dos adaptadores
// tienen que contestar lo mismo a la misma pregunta. Si aquí un `""` devolviera «sin
// líneas», un test unitario daría por bueno un resumen vacío que en producción sería
// un error.
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "store: listar líneas de solicitud: id %q inválido: %w"
func (r *MemoryRepository) ListIntakeItems(ctx context.Context, intakeID string) ([]IntakeItem, error) {
	panic(pendiente.Implementar("store.MemoryRepository.ListIntakeItems"))
}

// ReplaceIntakeItems implementa Repository: deja las líneas de CLIENTE de la
// solicitud exactamente en `items`, imitando el DELETE+INSERT transaccional del
// PostgresRepository (Plan 043 · Ola 3). len(items)==0 BORRA las de cliente, igual
// que el SQL. Fija IntakeID en cada línea y AddedAt al instante del reloj (SetClock)
// en las que lo traigan a cero —una línea que llega con AddedAt lo conserva, cosa que
// el Postgres no hace: allí manda siempre el DEFAULT now()—; copia por valor (structs
// sin punteros) para no compartir estado.
//
// Conserva las líneas de LA PLATAFORMA (sku con el prefijo reservado "_") AL FRENTE y
// tal cual estaban, que es donde las deja Postgres al ordenar por added_at. No
// comprueba que la solicitud exista ni que intakeID sea un UUID, y NO toca la
// cabecera (ni su updated_at). Nunca devuelve error.
//
// Que este adaptador reemplace y el otro también NO es cosmética: si aquí siguiera
// acumulando, los tests unitarios verían un pedido sin duplicados que en Postgres
// SÍ los tendría, y estarían mintiendo justo sobre lo que esta tarea arregla.
func (r *MemoryRepository) ReplaceIntakeItems(ctx context.Context, intakeID string, items []IntakeItem) error {
	panic(pendiente.Implementar("store.MemoryRepository.ReplaceIntakeItems"))
}

// MarkIntakeStatus implementa Repository: transiciona el estado de la solicitud (por
// ID) y fija su total, refrescando updated_at (Plan 016 · T2/T3). Si la solicitud no
// existe es un no-op sin error (misma semántica que el UPDATE sin filas). Busca por
// ID y nada más: no acota por tenant ni valida `status`. No toca las líneas ni ningún
// otro campo de la cabecera. Nunca devuelve error.
func (r *MemoryRepository) MarkIntakeStatus(ctx context.Context, intakeID, status string, total float64) error {
	panic(pendiente.Implementar("store.MemoryRepository.MarkIntakeStatus"))
}

// CloseIntake implementa Repository: cierra atómicamente (bajo el mutex del repo) la
// solicitud "open" del contacto —o crea una "closed" si no la hubiera— e inserta sus
// líneas, imitando la transacción del PostgresRepository (Plan 027 · Ola 1 · T4).
// Devuelve el id de la solicitud cerrada, igual que el store real.
//
// CON solicitud "open" del (tenant, contacto) —la más reciente por created_at si
// hubiera varias—: Status "closed", Total y CustomerNote los del argumento, UpdatedAt
// al instante del reloj; EventID con la semántica del COALESCE (rellena un vacío
// legado, jamás pisa un padre declarado); TenantID, ContactID, SessionID y CreatedAt
// quedan como estaban (in.SessionID NO se usa). SIN ella: nace una fila "closed" con
// id uuid nuevo, in.SessionID, in.EventID, Total y CustomerNote, y CreatedAt =
// UpdatedAt = el reloj. Una solicitud en cualquier otro estado ni se cierra ni cuenta.
//
// En los dos casos las líneas se REEMPLAZAN por in.Items con la misma regla que
// ReplaceIntakeItems (las de la plataforma sobreviven; in.Items vacío deja la
// solicitud sin líneas de cliente). Nunca devuelve error.
func (r *MemoryRepository) CloseIntake(ctx context.Context, in IntakeClose) (string, error) {
	panic(pendiente.Implementar("store.MemoryRepository.CloseIntake"))
}

// Intakes devuelve una copia de TODAS las solicitudes guardadas (las de UpsertIntake
// y las que pare CloseIntake), de todos los tenants, SIN orden garantizado y con la
// fila ENTERA, CustomerNote incluida. Es un helper de test; devuelve una copia para no
// exponer el mapa interno.
func (r *MemoryRepository) Intakes() []Intake {
	panic(pendiente.Implementar("store.MemoryRepository.Intakes"))
}

// IntakeItems devuelve una copia de las líneas persistidas para intakeID por
// ReplaceIntakeItems / CloseIntake, EN EL ORDEN en que se escribieron (que es el
// orden del carrito, y el que da la lectura real por added_at, id). Es un helper de
// test; devuelve una copia para no exponer el slice interno.
func (r *MemoryRepository) IntakeItems(intakeID string) []IntakeItem {
	panic(pendiente.Implementar("store.MemoryRepository.IntakeItems"))
}
