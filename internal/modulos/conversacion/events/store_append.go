// Porta internal/flujos/events/store.go @ 9d5a4b6 (trozo: las cinco puertas de escritura del
// historial; ver el reparto, D-1 y el homónimo DEK en store.go).
//
// Lo que las cinco comparten y el contrato de cada una da por dicho:
//
//   - NUMERACIÓN SIN HUECOS: la entrada lleva seq = MAX(seq)+1 del evento, calculado DENTRO de la
//     misma sentencia (no con una secuencia: un INSERT que falla no consume número). Si otro
//     escritor se llevó ese seq, el UNIQUE (event_id, seq) lo rechaza y la puerta REINTENTA
//     (hasta 5 intentos en total); agotados, devuelve error en vez de girar. El primer seq de un
//     evento es 1.
//   - EL GRADO, EL ROL Y EL ORIGEN LOS CLAVA CADA PUERTA, no el llamador (INV-11). El origen es
//     `whatsapp` salvo en AppendPastedMessage.
//   - O payload en claro (nivel 1) o cuerpo cifrado (nivel 2), nunca las dos cosas: es lo que
//     exige conversation_event_messages_grade_chk. Lo que no va, va a NULL (ni cadena ni blob
//     vacíos).
//
// Textos de error comunes (literales, con el error de origen envuelto en %w):
//   - "events: insertar entrada %q del historial: %w" (el %q es el entry_kind) ante cualquier fallo
//     de la base que no sea una violación de unicidad;
//   - "events: numerar la entrada del historial tras %d intentos: %w" (5) al agotar los reintentos,
//     envolviendo la última violación de unicidad.

package events

import (
	"context"
	"encoding/json"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// AppendSummary añade el RESUMEN determinista que emitimos nosotros al dejar de
// ser activo un evento (ADR-0029 E-4), con role='system' fijo.
//
// El cuerpo es json.RawMessage y no string A PROPÓSITO. Un resumen es nivel 1 del
// ADR-0034: estructura EN CLARO en payload, porque es negocio cuantificable y
// cifrarlo destruiría su valor sin proteger a nadie. Pedir estructura ya
// serializada es lo que impide que por esta puerta entre prosa: el texto libre no
// tiene sitio aquí, tiene AppendMessage, y allí se cifra siempre.
//
// El rol fijo no es comodidad: toda fila emitida por nosotros va marcada como
// nuestra, para que quien analice el hilo después no cuente dos veces la decisión
// que ya está en la tabla y ADEMÁS aparece dentro del resumen (INV-11).
//
// Devuelve el seq asignado.
//
// La fila: role = 'system', entry_kind = 'summary', origin = 'whatsapp', payload = body, y las tres
// columnas del sobre a NULL. No necesita FieldCipher. Un body vacío no es JSON: ErrSummaryNotJSON,
// sin ir a la base.
func (s *Store) AppendSummary(ctx context.Context, eventID string, body json.RawMessage) (int, error) {
	panic(pendiente.Implementar("events.Store.AppendSummary"))
}

// AppendDecision añade una DECISIÓN estructurada del cliente al hilo (D-043.23,
// D-043.13): la línea agregada o quitada del carrito, la respuesta de encuesta —
// el efecto ya decidido, no la charla que llevó a él.
//
// Es nivel 1 del ADR-0034: ESTRUCTURA en claro en payload, con role='client'
// FIJO, porque la decisión es del cliente — la voz de la entrada es suya aunque
// quien la serialice seamos nosotros. Igual que en AppendSummary, el payload se
// valida como JSON (ErrSummaryNotJSON si no lo es): la prosa no tiene por dónde
// entrar en el nivel que no cifra.
//
// INV-11 sigue intacto: nada NUESTRO se escribe como `decision`. Lo que emite la
// plataforma (los resúmenes de abandono) entra por AppendSummary con
// entry_kind='summary' y role='system'; esta puerta registra lo que el cliente
// decidió, y por eso no hay doble contabilidad entre el resumen y sus decisiones.
//
// La fila: role = 'client', entry_kind = 'decision', origin = 'whatsapp', payload = payload, y las
// tres columnas del sobre a NULL. No necesita FieldCipher y no devuelve el seq.
func (s *Store) AppendDecision(ctx context.Context, eventID string, payload []byte) error {
	panic(pendiente.Implementar("events.Store.AppendDecision"))
}

// AppendMessage añade el TEXTO LITERAL de una interacción, SIEMPRE CIFRADO
// (envelope AES-256-GCM con DEK fresca por valor envuelta por la KEK; el mismo
// FieldCipher que usa contacts).
//
// Es la única puerta de texto libre del store y no tiene variante en claro: no
// existe parámetro, bandera ni camino alternativo que persista body sin cifrar. La
// fila queda con payload NULL, como exige el CHECK de grado — el literal jamás se
// cuela en el nivel 1.
//
// Devuelve el seq asignado.
//
// La fila: el role dado, entry_kind = 'message', origin = 'whatsapp', payload NULL y el sobre de tres
// piezas (body_enc, body_dek, body_kek_id) que da el FieldCipher. El orden de las guardas es: rol,
// cipher, cifrar; ninguna de las tres llega a la base si falla.
//
// Textos de error (literales):
//   - "%w: %q": ErrInvalidRole con el rol recibido;
//   - ErrNoCipher, a secas, si el store no tiene cipher;
//   - "events: cifrar el cuerpo de la entrada: %w".
func (s *Store) AppendMessage(ctx context.Context, eventID string, role Role, body string) (int, error) {
	panic(pendiente.Implementar("events.Store.AppendMessage"))
}

// AppendOutOfTurnMessage añade al hilo un SALIENTE FUERA DE TURNO: el texto que la
// plataforma le manda al cliente sin que nazca de un entrante suyo (Plan 044 ·
// T1.6, D-044.24) — el resumen del rescate, la confirmación de un `event_stop`, el
// recordatorio de la seña.
//
// Es AppendMessage con OTRO grado, y nada más: mismo cifrado, mismo sobre de tres
// piezas, mismo nivel 2 del ADR-0034 y misma numeración sin huecos. La única
// diferencia es `entry_kind='message_out_of_turn'`, que es lo que permite a quien
// lea el hilo (T1.4) meter el texto como CONTEXTO y no como pedido del cliente. El
// porqué de esa forma —y por qué no un `role` nuevo ni un flag en `payload`— está
// en events.go, junto al grado `message_out_of_turn`.
//
// 🔴 EL ROL ES FIJO Y ES `business`, y no es comodidad: un saliente es, por
// definición, la voz del negocio. Dejar que el llamante lo eligiera abriría la
// puerta a registrar texto del CLIENTE como contexto fuera de turno, que es
// exactamente la doble contabilidad que INV-11 existe para impedir — el mismo
// criterio con el que AppendSummary clava `system` y AppendDecision clava `client`.
//
// Devuelve el seq asignado.
//
// La fila: role = 'business', entry_kind = 'message_out_of_turn', origin = 'whatsapp', payload NULL
// y el sobre cifrado.
//
// Textos de error (literales):
//   - ErrNoCipher, a secas, si el store no tiene cipher;
//   - "events: cifrar el cuerpo del saliente fuera de turno: %w".
func (s *Store) AppendOutOfTurnMessage(ctx context.Context, eventID string, body string) (int, error) {
	panic(pendiente.Implementar("events.Store.AppendOutOfTurnMessage"))
}

// AppendPastedMessage añade al hilo la TRANSCRIPCIÓN EXTERNA que el dueño pegó en
// el cuerpo de `POST /api/v1/intakes/{id}/reanalyze` (Plan 044 · Ola 4 · T4.6;
// REQ-32 / D-044.17, cierre de MD-044.2).
//
// Es AppendMessage con OTRO origen y NADA más: mismo `entry_kind='message'`, mismo
// cifrado, mismo sobre de tres piezas, mismo nivel 2 del ADR-0034 y misma numeración
// sin huecos. La única diferencia viaja en `origin`, que es la columna que responde
// «por dónde entró» y que el LLM no ve.
//
// 🔴 EL ROL ES FIJO Y ES `client`, Y ESO ES LA DECISIÓN, NO UN DESCUIDO (D-044.17).
// Lo que el dueño pega es lo que DIJO EL CLIENTE por otro canal —un audio que
// transcribió, un mensaje que le llegó por Instagram—, así que su voz es la del
// cliente. Marcarlo `business` lo convertiría en CONTEXTO para el compositor
// y el pipeline dejaría de extraer de ahí ni un ítem, que es
// exactamente lo contrario de para qué se pega. El rastro de que lo escribió el
// dueño no se pierde: vive en `origin`, que es la columna de esa pregunta.
//
// 🔴 EL SANEO NO ESTÁ AQUÍ, Y TAMPOCO ES UN OLVIDO. `intakes.SanitizeNote` (en el viejo, `cart.SanitizeNote`) es la
// puerta de contenido del texto libre del dueño y la aplica quien recibe la
// petición (`captacion/reanalisis`), antes de decidir si la fila es un duplicado —
// porque el dedupe se hace por el hash del texto YA SANEADO. Sanear otra vez aquí
// sería una segunda regla en un segundo sitio, y este paquete no importa `cart`.
//
// Devuelve el seq asignado.
//
// La fila: role = 'client', entry_kind = 'message', origin = 'owner_pasted', payload NULL y el sobre
// cifrado.
//
// Textos de error (literales; ninguno cita el cuerpo, que es texto del cliente):
//   - ErrNoCipher, a secas, si el store no tiene cipher;
//   - "events: cifrar la transcripción pegada por el dueño: %w".
func (s *Store) AppendPastedMessage(ctx context.Context, eventID string, body string) (int, error) {
	panic(pendiente.Implementar("events.Store.AppendPastedMessage"))
}
