// Porta internal/intakes/memory.go @ 64c181a
//
// El fichero viejo medía 907 líneas y aquí NACE PARTIDO por tema (05 E-13), solo
// repartiendo declaraciones:
//
//   - memory.go (este): el tipo, el constructor, lo que el doble satisface y los
//     mutadores de SIEMBRA y CONFIGURACIÓN que usan los tests (ninguno es de un puerto).
//   - memory_read.go: las lecturas (List, ListDetails, Get, las revisiones, la
//     configuración del tenant y el historial aprobado, que venía de aprobadas.go).
//   - memory_write.go: las escrituras del puerto Store, InsertRevision y PutBuyerField.
//   - memory_reminders.go: los compare-and-swap de los dos recordatorios.

package intakes

import (
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// MemoryStore es el gemelo EN MEMORIA de la persistencia de solicitudes, para tests.
// Reproduce las MISMAS semánticas que el store Postgres —rango [From, To), variantes
// legadas del estado, orden por created_at con desempate por id, paginación y total
// sin paginar, los compare-and-swap, la línea de envío única, la numeración de
// revisiones— para que un test de handler contra él diga algo verdadero sobre
// producción. Que lo hace lo comprueba intakeshelpertest.Contrato, la MISMA suite que
// corre contra Postgres.
//
// Guarda el estado EN CRUDO (como la BD, con su `closed` legado) y normaliza al leer:
// así el camino de normalización se ejercita de verdad.
//
// Lo que imita, además de la tabla de solicitudes y sus líneas:
//
//   - intake_revisions, con el literal del cliente (nivel 2) guardado APARTE del
//     payload y sujeto a la retención por TTL (literal.go);
//   - tenant_settings: zonas de envío, plantilla y plazo de la seña, TTL del literal;
//   - conversation_events reducida a `eventID → status` (`open` | `closed` |
//     `cancelled`) y la ligadura intakes.event_id (D-043.21);
//   - intake_buyer_data, EN CLARO. Es correcto que lo esté: este doble vive en memoria
//     y muere con el proceso; lo que reproduce es la semántica (fusión campo a campo,
//     una entrada por solicitud), no el cifrado, que se prueba contra Postgres. 🔴 El
//     «DEK» de ese cifrado es el del envelope de PII de negocio, no la DEK del
//     ADR-0007 (T-1). Nadie debe cablear este doble en producción.
//
// UN SOLO RELOJ (SetClock) fecha todo lo que el store real fecha con el now() de su
// transacción: el CreatedAt de una revisión, el plazo de la seña, el UpdatedAt de la
// cabecera, y mide además la edad de una revisión para la retención del literal.
//
// 🔴 UpdatedAt SE REFRESCA en toda escritura de la cabecera, como hace Postgres
// (`updated_at = now()` en el CAS del estado, en el recálculo del total, en el
// descarte y en el abandono por evento) y NO en los dos recordatorios. El doble viejo
// solo lo movía en AbandonByEvent, y esa divergencia importa: UpdatedAt es la base del
// plazo del presupuesto (Overdue). La suite vigila la fila entera (hallazgo 35 de F1)
// y lo exige.
//
// Se puede usar desde varias goroutines a la vez: cada método es atómico.
type MemoryStore struct{}

// Lo que el doble satisface, igual que *Postgres: el puerto de la bandeja, el de
// escritura de revisiones, los dos de recordatorio y la lectura de la configuración.
var (
	_ Store          = (*MemoryStore)(nil)
	_ RevisionWriter = (*MemoryStore)(nil)
	_ DepositStore   = (*MemoryStore)(nil)
	_ ExpiryStore    = (*MemoryStore)(nil)
	_ SettingsReader = (*MemoryStore)(nil)
)

// NewMemoryStore construye un store vacío y listo para usar: ningún tenant tiene
// solicitudes, zonas, plantilla ni eventos; el reloj es el del proceso mientras no se
// inyecte otro (SetClock); el TTL del literal es DefaultLiteralTTL, como el DEFAULT de
// la columna; y la poda sale por logger.Default().
func NewMemoryStore() *MemoryStore {
	panic(pendiente.Implementar("intakes.NewMemoryStore"))
}

// SetClock fija el reloj del store. Desde la llamada, todo lo que el store fecha sale
// de `now`, y la edad de una revisión se mide contra él: un test puede fijar un plazo y
// cruzarlo, o envejecer una revisión doce meses, sin esperar ni sembrar fechas a mano.
// No remarca nada de lo ya guardado. Un `now` nil se IGNORA (queda el reloj anterior).
// Mutador de tests; no es parte de ningún puerto.
func (m *MemoryStore) SetClock(now func() time.Time) {
	panic(pendiente.Implementar("intakes.MemoryStore.SetClock"))
}

// SetLiteralTTL cambia la retención del literal de las revisiones, como haría un
// UPDATE de tenant_settings.intake_literal_ttl_seconds. 🔴 0 = RETENCIÓN INDEFINIDA
// (sin poda), igual que en la columna: leerlo al revés destruiría el literal en la
// primera lectura y sin vuelta. Vale para todos los tenants del store.
func (m *MemoryStore) SetLiteralTTL(ttl time.Duration) {
	panic(pendiente.Implementar("intakes.MemoryStore.SetLiteralTTL"))
}

// SetRetentionLog sustituye el logger por el que sale el evento de la poda. Existe
// para lo mismo que WithRetentionLog en el store real: cablear el de la aplicación y,
// sobre todo, poder OBSERVAR la poda en un test — un criterio que no se puede observar
// no se puede verificar. Un logger nil se IGNORA. Era `SetLogDeRetencion` en el
// paquete viejo.
func (m *MemoryStore) SetRetentionLog(l logger.Logger) {
	panic(pendiente.Implementar("intakes.MemoryStore.SetRetentionLog"))
}

// Add siembra una solicitud del tenant con sus líneas, al final de las que tuviera.
// `in` se guarda entero y `in.Status` TAL CUAL: puede ser la clave legada `closed`,
// que las lecturas normalizan. No valida nada (ni id repetido, ni estado conocido) y
// no declara el evento padre: sin BindEvent la solicitud queda como una fila LEGADA
// pre-0054. Las líneas se guardan en el orden dado, con el AddedAt que traigan.
func (m *MemoryStore) Add(tenantID string, in Intake, items ...Item) {
	panic(pendiente.Implementar("intakes.MemoryStore.Add"))
}

// SetEvent siembra (o reescribe) el estado de un evento conversacional —`open`,
// `closed` o `cancelled`—, como una fila de public.conversation_events. No valida la
// clave. Mutador de tests.
func (m *MemoryStore) SetEvent(eventID, status string) {
	panic(pendiente.Implementar("intakes.MemoryStore.SetEvent"))
}

// EventStatus devuelve el estado sembrado o escrito del evento, y "" si no existe. Es
// el mirador de SetEvent: deja comprobar qué le pasó al contenedor de una solicitud
// sin abrir el store.
func (m *MemoryStore) EventStatus(eventID string) string {
	panic(pendiente.Implementar("intakes.MemoryStore.EventStatus"))
}

// BindEvent declara el padre de una solicitud (intakes.event_id, D-043.21), como lo
// hace el proyector del carrito al parir la fila. Repetirlo lo sustituye; con
// eventID "" la solicitud vuelve a quedar sin ligadura. No comprueba que la solicitud
// ni el evento existan. Mutador de tests.
func (m *MemoryStore) BindEvent(intakeID, eventID string) {
	panic(pendiente.Implementar("intakes.MemoryStore.BindEvent"))
}

// SetShippingZones siembra las zonas de envío del tenant, sustituyendo las que
// hubiera, como un UPDATE de tenant_settings.shipping_zones. Sin llamarla el tenant no
// tiene zonas, que es el DEFAULT de la columna. El store se queda con una COPIA:
// cambiar el slice del llamante después no cambia lo guardado.
func (m *MemoryStore) SetShippingZones(tenantID string, zones ...ShippingZone) {
	panic(pendiente.Implementar("intakes.MemoryStore.SetShippingZones"))
}

// SetShippingPrice precifica a mano la línea de envío (ShippingSKU) de una solicitud y
// cuadra el total de la cabecera con sus líneas: lo que hace el dueño desde la consola
// con un «Envío por confirmar» (D-041.11). Si la solicitud no es del tenant o no tiene
// línea de envío, no hace nada. Mutador de tests; no es parte de ningún puerto.
func (m *MemoryStore) SetShippingPrice(tenantID, intakeID string, price float64) {
	panic(pendiente.Implementar("intakes.MemoryStore.SetShippingPrice"))
}

// SetDepositTemplate siembra la plantilla de la seña del tenant y su plazo en días,
// sustituyendo lo que hubiera, como un UPDATE de tenant_settings.deposit_template /
// deposit_due_days. Sin llamarla el tenant no tiene plantilla —el estado de arranque
// de cualquier tenant real— y su plazo es DefaultDepositDueDays. Un `dueDays` ≤ 0 se
// lee como el plazo por defecto, tanto en NotifySettings como al pedir la seña.
func (m *MemoryStore) SetDepositTemplate(tenantID, template string, dueDays int) {
	panic(pendiente.Implementar("intakes.MemoryStore.SetDepositTemplate"))
}
