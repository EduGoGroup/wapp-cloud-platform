// Porta internal/flujos/store/repository_postgres.go @ c0c0c03
//
// Trozo de repository_postgres.go (05 E-13): public.tenant_settings y
// public.conversation_welcomes. Las reglas comunes del adaptador están en la cabecera
// de repository_postgres.go.
//
// El auxiliar no exportado que decodifica buyer_fields (parseBuyerFields en el viejo)
// nace con el verde. Su regla es del contrato de GetTenantSettings: es TOLERANTE a
// propósito, y un blob ilegible o de otra forma devuelve el checklist VACÍO en vez de
// un error (esa lectura está en el camino de CADA mensaje del cliente, no en un
// endpoint de administración donde un 400 sería útil).
//
// 🔴 MUTANTES (nivel complejo): TouchContact y MarkWelcomed llevan las guardas que la
// suite tiene que morder una a una — el estado PREVIO (no el tocado), el toque que no
// pisa welcomed_at, los tres predicados de la clave, y el compare-and-set
// `IS NOT DISTINCT FROM` sobre el testigo (ni `IS NULL`, ni `=`, ni sin condición).

package store

import (
	"context"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// GetTenantSettings devuelve la config del carrito para tenantID desde
// public.tenant_settings (Plan 016 · T0). Si el tenant no tiene fila, devuelve los
// DEFAULTS de DefaultTenantSettings SIN error (design.md §9.E/§9.G).
//
// HAY FILA vs NO HAY FILA SON DOS CAMINOS DISTINTOS, Y ESO ES EL PUNTO (Plan 043 ·
// T1.3). Con fila, los valores se devuelven TAL CUAL vienen de la columna, sin
// sustituir ceros por defaults: `event_inactivity_ttl_seconds = 0` es el override
// explícito «sin vencimiento» de una empresa (D-043.7 / E-6), no un hueco que
// rellenar. Como 0 es además el cero de Go, un `if x == 0 { x = Default }` aquí
// convertiría ese override en 2 h sin que nadie se entere: no lo introduzcas.
//
// Mapeo de la fila (diez columnas, en este orden: page_size, order_ttl_seconds,
// conversation_ttl_seconds, buyer_fields, event_inactivity_ttl_seconds,
// event_history_ttl_seconds, aggregation_window_seconds, aggregation_max_seconds,
// welcome_text, welcome_silence_seconds): las seis columnas `_seconds` salen como
// time.Duration de ese número de segundos; welcome_text sale TAL CUAL, ” incluido (el
// ” significa «el texto de plataforma» y quien lo traduce es el runtime, no este
// método); buyer_fields se decodifica con TOLERANCIA: un blob vacío, ilegible o de
// otra forma da el checklist vacío y el resto de la fila se devuelve igual. TenantID
// es el argumento.
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "store: leer config de tenant: %w"
func (r *PostgresRepository) GetTenantSettings(ctx context.Context, tenantID string) (TenantSettings, error) {
	panic(pendiente.Implementar("store.PostgresRepository.GetTenantSettings"))
}

// TouchContact implementa WelcomeStore: registra que el contacto acaba de escribir
// y devuelve el estado que había ANTES de este turno.
//
// # UNA SOLA SENTENCIA, Y POR QUÉ ESO IMPORTA
//
// Corre EN LÍNEA con el mensaje del cliente, en el mismo tramo que ya tiene
// presupuesto escrito para el agregador (D-044.26: una sentencia, cero lecturas,
// cero cripto, cero red). Partirlo en un SELECT y un UPSERT sería duplicar los
// round-trips del camino caliente para responder una pregunta que la BD puede
// contestar de una vez.
//
// 🔴 EL CTE `previo` VE LA FILA VIEJA, Y ESA ES TODA LA MECÁNICA. En PostgreSQL
// todas las sub-sentencias de un mismo statement comparten UN snapshot: `previo`
// es un SELECT normal, así que lee lo que había antes de que `toque` escribiera,
// aunque el planificador los ejecute en el orden que quiera. Un `RETURNING` sobre
// el `ON CONFLICT DO UPDATE` NO serviría —devuelve la fila YA actualizada, o sea
// `last_incoming_at = now`— y el umbral de silencio saldría 0 siempre: la
// bienvenida no volvería nunca después de la primera. Es la clase de defecto que
// solo se ve con un reloj falso y varias horas de diferencia.
//
// El CTE de escritura se ejecuta SIEMPRE, aunque la consulta principal no lea ni
// una fila suya: es garantía documentada de PostgreSQL para las sentencias
// modificadoras dentro de WITH. Por eso `previo` puede venir vacío (contacto nuevo)
// sin que el toque se pierda.
//
// Ese caso vacío —`sql.ErrNoRows`— es el contacto que escribe por primera vez: se
// devuelve el WelcomeMark CERO sin error, que es exactamente «nunca habló, nunca
// se le saludó».
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "store: registrar actividad del contacto (bienvenida): %w"
func (r *PostgresRepository) TouchContact(ctx context.Context, key Key, now time.Time) (WelcomeMark, error) {
	panic(pendiente.Implementar("store.PostgresRepository.TouchContact"))
}

// MarkWelcomed implementa WelcomeStore: sella la bienvenida como entregada, con
// CENTINELA sobre el testigo que TouchContact devolvió.
//
// # POR QUÉ EL CENTINELA ES UN COMPARE-AND-SET Y NO UN `IS NULL`
//
// El precedente directo —fleet_sessions.greeted_at (0066)— usa `WHERE greeted_at IS
// NULL`, y allí basta porque aquella marca se pone UNA vez y para siempre. Esta
// vuelve a ponerse cada vez que el contacto reaparece tras el silencio, así que un
// `IS NULL` solo protegería la PRIMERA bienvenida y dejaría todas las demás sin
// centinela. `IS NOT DISTINCT FROM` compara incluyendo el NULL (un `=` con NULL da
// NULL, o sea ninguna fila, y la primera bienvenida no se marcaría JAMÁS: el
// contacto la recibiría en cada mensaje).
//
// Devuelve false SIN error cuando el centinela no casa: otro turno ganó la carrera
// entre el TouchContact y este UPDATE. La BD queda bien; lo que ya no tiene arreglo
// es que el mensaje de ESTE camino salió, y ese duplicado se ve en el log del
// llamante y en ningún otro sitio — misma honestidad que documenta greeting.go.
//
// Textos de error (literales, con el error de origen envuelto en %w):
//   - "store: marcar bienvenida entregada: %w"
//   - "store: filas afectadas al marcar bienvenida: %w"
func (r *PostgresRepository) MarkWelcomed(ctx context.Context, key Key, witness WelcomeMark, now time.Time) (bool, error) {
	panic(pendiente.Implementar("store.PostgresRepository.MarkWelcomed"))
}
