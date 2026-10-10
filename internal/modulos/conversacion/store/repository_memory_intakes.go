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
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// newestOpenLocked devuelve, con el mutex YA tomado, la solicitud "open" del (tenant, contacto):
// la que GetOpenIntake enseña y la que CloseIntake cierra.
//
// Divergencia deliberada del viejo, F8-01: si hay VARIAS abiertas —el negocio promete una, pero
// nada lo impone— gana la MÁS RECIENTE por created_at, como el `ORDER BY created_at DESC LIMIT 1`
// del Postgres. El viejo devolvía la primera del recorrido del mapa, una distinta en cada vuelta.
// A igual created_at (que Postgres tampoco desempata) gana el id mayor, solo para que el
// resultado sea estable.
func (r *MemoryRepository) newestOpenLocked(tenantID, contactID string) (Intake, bool) {
	var (
		out   Intake
		found bool
	)
	for _, o := range r.intakes {
		if o.TenantID != tenantID || o.ContactID != contactID || o.Status != "open" {
			continue
		}
		if !found || o.CreatedAt.After(out.CreatedAt) ||
			(o.CreatedAt.Equal(out.CreatedAt) && o.ID > out.ID) {
			out, found = o, true
		}
	}
	return out, found
}

// headerRead es la cabecera tal como la devuelven las dos lecturas del puerto (GetOpenIntake y
// GetIntakeByEvent): sin la nota del cliente.
//
// Divergencia deliberada del viejo, F8-01: el viejo devolvía la fila entera, nota incluida; la
// proyección del Postgres no lee customer_note, y las dos lecturas tienen que dar la misma foto
// en los dos adaptadores. La fila entera se mira con Intakes().
func headerRead(o Intake) Intake {
	o.CustomerNote = ""
	return o
}

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
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	if prev, ok := r.intakes[o.ID]; ok {
		o.CreatedAt = prev.CreatedAt
		// Misma semántica que el COALESCE(intakes.event_id, EXCLUDED.event_id) del
		// Postgres (D-043.21): un padre ya declarado JAMÁS se pisa; un vacío legado
		// sí se estampa. Si aquí se sobrescribiera, un test unitario daría por
		// buena una escritura que en producción no puede ocurrir.
		if prev.EventID != "" {
			o.EventID = prev.EventID
		}
		// Divergencia deliberada del viejo, F8-01: la nota NO se pisa. El viejo guardaba
		// la del argumento y por tanto BORRABA la que puso el cierre; el UPDATE del
		// Postgres ni menciona customer_note.
		o.CustomerNote = prev.CustomerNote
	} else {
		o.CreatedAt = now
		// Divergencia deliberada del viejo, F8-01 (la misma): en el alta la nota nace
		// vacía, como el DEFAULT '' de la columna, traiga lo que traiga el argumento.
		o.CustomerNote = ""
	}
	o.UpdatedAt = now
	r.intakes[o.ID] = o
	return nil
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
	r.mu.Lock()
	defer r.mu.Unlock()
	open, found := r.newestOpenLocked(tenantID, contactID)
	if !found {
		return Intake{}, false, nil
	}
	return headerRead(open), true, nil
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
	if eventID == "" {
		return Intake{}, false, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var (
		out   Intake
		found bool
	)
	for _, o := range r.intakes {
		if o.TenantID != tenantID || o.EventID != eventID {
			continue
		}
		if !found || o.CreatedAt.Before(out.CreatedAt) ||
			(o.CreatedAt.Equal(out.CreatedAt) && o.ID < out.ID) {
			out, found = o, true
		}
	}
	return headerRead(out), found, nil
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
	if _, err := uuid.Parse(intakeID); err != nil {
		return nil, fmt.Errorf("store: listar líneas de solicitud: id %q inválido: %w", intakeID, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	src := r.intakeItems[intakeID]
	out := make([]IntakeItem, len(src))
	copy(out, src)
	return out, nil
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
// cabecera (ni su updated_at).
//
// Devuelve error, SIN modificar nada, si la escritura dejaría la solicitud con más de
// una línea de envío (ver checkShippingLocked). En cualquier otro caso, nil.
//
// Que este adaptador reemplace y el otro también NO es cosmética: si aquí siguiera
// acumulando, los tests unitarios verían un pedido sin duplicados que en Postgres
// SÍ los tendría, y estarían mintiendo justo sobre lo que esta tarea arregla.
func (r *MemoryRepository) ReplaceIntakeItems(ctx context.Context, intakeID string, items []IntakeItem) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.checkShippingLocked(intakeID, items); err != nil {
		return err
	}
	r.replaceIntakeItemsLocked(intakeID, items)
	return nil
}

// shippingSKU es el sku de la línea estándar de envío (D-041.11). El literal se
// repite aquí en vez de importarse de solicitudes, igual que reservedSKUPrefix y por
// lo mismo; lo que impide que diverjan es el test propio de este fichero.
const shippingSKU = "_shipping"

// errSecondShippingLine es el rechazo de checkShippingLocked. No se exporta: el
// Postgres devuelve aquí la violación de único de pgx, así que ningún llamante puede
// distinguir este error por identidad en los dos adaptadores.
var errSecondShippingLine = errors.New("una solicitud no admite una segunda línea de envío (" + shippingSKU + ")")

// checkShippingLocked dice, con el mutex YA tomado y ANTES de escribir nada, si
// reemplazar las líneas de cliente de la solicitud por `items` la dejaría con más de
// una línea de envío: la que ya tuviera (las de la plataforma sobreviven al
// reemplazo) más las que traiga `items`. intakeID "" es una solicitud que aún no
// existe: solo cuentan las de `items`.
//
// Divergencia deliberada del viejo, F8-01 (hallazgo 7): el viejo duplicaba la línea;
// Postgres la rechaza por `intake_items_shipping_uniq` (índice único parcial sobre
// intake_id WHERE sku = '_shipping', migración 0045) y deshace la transacción entera.
// Por eso quien llama comprueba PRIMERO y no toca nada si hay error.
//
// Textos de error (literales, con errSecondShippingLine envuelto en %w):
//   - "store: insertar líneas de solicitud: %w"
func (r *MemoryRepository) checkShippingLocked(intakeID string, items []IntakeItem) error {
	n := 0
	if intakeID != "" {
		for _, it := range r.intakeItems[intakeID] {
			if it.SKU == shippingSKU {
				n++
			}
		}
	}
	for _, it := range items {
		if it.SKU == shippingSKU {
			n++
		}
	}
	if n > 1 {
		return fmt.Errorf("store: insertar líneas de solicitud: %w", errSecondShippingLine)
	}
	return nil
}

// replaceIntakeItemsLocked es el reemplazo con el mutex YA tomado: lo comparten
// ReplaceIntakeItems y CloseIntake, que necesita hacerlo dentro de su propia sección
// crítica (el equivalente de su transacción). No puede fallar: quien lo llama ha
// pasado antes por checkShippingLocked.
//
// Conserva las líneas de LA PLATAFORMA (prefijo reservado) al frente, que es donde
// las deja Postgres cuando existen: se escriben antes de cualquier reemplazo posterior
// y la lectura ordena por added_at.
func (r *MemoryRepository) replaceIntakeItemsLocked(intakeID string, items []IntakeItem) {
	now := r.now()
	kept := make([]IntakeItem, 0, len(items))
	for _, it := range r.intakeItems[intakeID] {
		if strings.HasPrefix(it.SKU, reservedSKUPrefix) {
			kept = append(kept, it)
		}
	}
	for _, it := range items {
		it.IntakeID = intakeID
		if it.AddedAt.IsZero() {
			it.AddedAt = now
		}
		kept = append(kept, it)
	}
	if len(kept) == 0 {
		delete(r.intakeItems, intakeID)
		return
	}
	r.intakeItems[intakeID] = kept
}

// MarkIntakeStatus implementa Repository: transiciona el estado de la solicitud (por
// ID) y fija su total, refrescando updated_at (Plan 016 · T2/T3). Si la solicitud no
// existe es un no-op sin error (misma semántica que el UPDATE sin filas). Busca por
// ID y nada más: no acota por tenant ni valida `status`. No toca las líneas ni ningún
// otro campo de la cabecera. Nunca devuelve error.
func (r *MemoryRepository) MarkIntakeStatus(ctx context.Context, intakeID, status string, total float64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if o, ok := r.intakes[intakeID]; ok {
		o.Status = status
		o.Total = total
		o.UpdatedAt = r.now()
		r.intakes[intakeID] = o
	}
	return nil
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
// solicitud sin líneas de cliente).
//
// Devuelve ("", err), SIN cerrar ni crear nada, si el cierre dejaría la solicitud con
// más de una línea de envío (ver checkShippingLocked): es la transacción del Postgres,
// que se deshace entera. En cualquier otro caso el error es nil.
func (r *MemoryRepository) CloseIntake(ctx context.Context, in IntakeClose) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	o, found := r.newestOpenLocked(in.TenantID, in.ContactID)
	// Antes de tocar la cabecera: o.ID es "" si no hay abierta (la que nacería no
	// tiene líneas todavía).
	if err := r.checkShippingLocked(o.ID, in.Items); err != nil {
		return "", err
	}
	var intakeID string
	if found {
		intakeID = o.ID
		o.Status = "closed"
		o.Total = in.Total
		o.CustomerNote = in.CustomerNote
		// COALESCE(event_id, $n), como el UPDATE del Postgres (D-043.21): el
		// cierre rellena un NULL legado y jamás pisa un padre ya declarado.
		if o.EventID == "" {
			o.EventID = in.EventID
		}
		o.UpdatedAt = now
		r.intakes[intakeID] = o
	} else {
		intakeID = uuid.NewString()
		r.intakes[intakeID] = Intake{
			ID:           intakeID,
			TenantID:     in.TenantID,
			ContactID:    in.ContactID,
			SessionID:    in.SessionID,
			Status:       "closed",
			Total:        in.Total,
			CustomerNote: in.CustomerNote,
			EventID:      in.EventID,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
	}
	// REEMPLAZO, no acumulación: la solicitud puede llegar al cierre con las líneas
	// que la proyección de item_added ya materializó (mismo motivo, y mismo orden de
	// operaciones, que en el PostgresRepository).
	r.replaceIntakeItemsLocked(intakeID, in.Items)
	return intakeID, nil
}

// Intakes devuelve una copia de TODAS las solicitudes guardadas (las de UpsertIntake
// y las que pare CloseIntake), de todos los tenants, SIN orden garantizado y con la
// fila ENTERA, CustomerNote incluida. Es un helper de test; devuelve una copia para no
// exponer el mapa interno.
func (r *MemoryRepository) Intakes() []Intake {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Intake, 0, len(r.intakes))
	for _, o := range r.intakes {
		out = append(out, o)
	}
	return out
}

// IntakeItems devuelve una copia de las líneas persistidas para intakeID por
// ReplaceIntakeItems / CloseIntake, EN EL ORDEN en que se escribieron (que es el
// orden del carrito, y el que da la lectura real por added_at, id). Es un helper de
// test; devuelve una copia para no exponer el slice interno.
func (r *MemoryRepository) IntakeItems(intakeID string) []IntakeItem {
	r.mu.Lock()
	defer r.mu.Unlock()
	src := r.intakeItems[intakeID]
	out := make([]IntakeItem, len(src))
	copy(out, src)
	return out
}
