// Porta internal/intakes/status.go @ 64c181a

// status.go es la MÁQUINA DE ESTADOS de la solicitud (design §D-041.10; regla
// R-09): las once claves del ciclo de vida, el alias legado `closed`, qué
// transiciones existen, qué se puede descartar a mano y el error tipado que viaja
// en el 422. Es la ÚNICA autoridad sobre el ciclo de vida: ni la BD tiene CHECK ni
// los handlers replican reglas.
//
// Todo el fichero compara claves BYTE A BYTE. No recorta espacios, no pliega
// mayúsculas y no normaliza Unicode: `CLOSED`, ` closed`, `closed\n` y un `closed`
// con un U+200B dentro son claves DESCONOCIDAS, no el alias. Quien recibe texto de
// fuera lo limpia antes de llegar aquí.

package intakes

import "github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"

// Claves del ciclo de vida de una solicitud (design §D-041.10). Los
// IDENTIFICADORES van en inglés (INV-09, I-CP-8; regla R-09): el nombre de negocio
// —«por aprobar», «señado»— vive en la UI y en la doc, nunca en la BD ni en el
// wire. Los valores son claves de wire y de columna: no cambian ni un byte.
const (
	// StatusOpen — abierto: carrito en curso.
	StatusOpen = "open"
	// StatusPendingApproval — por aprobar: presupuesto esperando al dueño.
	StatusPendingApproval = "pending_approval"
	// StatusConfirmed — confirmado: pedido aceptado. Es la clave canónica del
	// `closed` con el que el módulo cart cierra desde el Plan 016.
	StatusConfirmed = "confirmed"
	// StatusDepositRequested — seña solicitada: plantilla de seña enviada.
	StatusDepositRequested = "deposit_requested"
	// StatusDepositPaid — señado: seña recibida (marca manual).
	StatusDepositPaid = "deposit_paid"
	// StatusSettled — saldado: pagado completo (terminal).
	StatusSettled = "settled"
	// StatusCancelled — cancelado: alguien se pronunció sobre la SOLICITUD (terminal).
	StatusCancelled = "cancelled"
	// StatusExpired — vencido: terminal LEGADO. Se conserva porque hay filas
	// históricas con él, pero ya nadie ENTRA en él: nada vence por tiempo
	// (D-041.16). Ver CanTransition.
	StatusExpired = "expired"
	// StatusAbandoned — abandonado: la conversación que lo sostenía se canceló, o
	// el dueño descartó el pedido huérfano. La solicitud sobrevive (terminal).
	StatusAbandoned = "abandoned"
	// StatusRejected — rechazado: el dueño rechaza el presupuesto (terminal).
	StatusRejected = "rejected"
	// StatusNeedsInfo — falta info: el dueño pide datos; vuelve a pending_approval.
	StatusNeedsInfo = "needs_info"

	// StatusClosedLegacy es la clave con la que el módulo cart cierra una solicitud
	// desde el Plan 016 y que sigue viva en BD. NO se migra (D-041.10: «el cart no
	// cambia su escritura»): se normaliza al leer, en un único punto
	// (NormalizeStatus). No es un duodécimo estado: es otra forma de escribir
	// `confirmed`.
	StatusClosedLegacy = "closed"
)

// NormalizeStatus resuelve la clave canónica de un estado: traduce los alias
// legados (hoy uno solo, `closed` → `confirmed`) y deja el resto tal cual. Es el
// ÚNICO punto del sistema donde se resuelve el alias.
//
// Normalizar NO es validar: una clave vacía sigue vacía (el filtro «sin filtro») y
// una desconocida se devuelve INTACTA, byte a byte, para que el llamante decida si
// la rechaza. La comparación es exacta: `CLOSED`, `Closed`, ` closed`, `closed `
// y `closed\n` NO son el alias y salen como entraron.
func NormalizeStatus(status string) string {
	panic(pendiente.Implementar("intakes.NormalizeStatus"))
}

// StoredVariants devuelve TODAS las formas en que un estado canónico puede estar
// escrito en BD, en orden alfabético. Es la contracara de NormalizeStatus y hace
// falta porque las filas legadas no se migran: filtrar por `confirmed` tiene que
// alcanzar también a las que el cart cerró como `closed`.
//
// Normaliza antes de expandir, así que `confirmed` y `closed` devuelven lo MISMO:
// [closed confirmed]. Un estado sin alias devuelve solo su clave. No valida: una
// clave desconocida —o la vacía— devuelve una lista con esa sola clave.
func StoredVariants(canonical string) []string {
	panic(pendiente.Implementar("intakes.StoredVariants"))
}

// StoredVariantsOf expande TODOS los estados de un filtro a sus claves tal como
// están ALMACENADAS. Es StoredVariants aplicado a CADA elemento, no al primero: un
// filtro `[open, confirmed]` tiene que alcanzar también las filas legadas en
// `closed`, y una expansión que solo mirara la cabeza de la lista las perdería en
// silencio en cuanto el llamante pidiera dos estados (D-044.47 §2, Plan 044 · T4.1).
//
// El resultado sale ORDENADO y SIN REPETIDOS. El orden determinista importa porque
// viaja a un `= ANY($n)` y a los tests: dos filtros con los mismos estados en otro
// orden no pueden producir dos consultas distintas, y pedir a la vez una clave y su
// alias no puede sacar la variante dos veces.
//
// Una lista vacía —nil o `[]`— devuelve nil y no un slice vacío: nil es «sin filtro
// por estado»; un array vacío en `status = ANY('{}')` no casaría con NADA y la
// bandeja saldría en blanco. Como StoredVariants, no valida ni descarta: una clave
// vacía o desconocida dentro de la lista viaja tal cual al resultado.
func StoredVariantsOf(canonicals []string) []string {
	panic(pendiente.Implementar("intakes.StoredVariantsOf"))
}

// IsStatus indica si la clave es un estado conocido del ciclo de vida, tras
// normalizar los alias: los once estados y el legado `closed`. Conocidos son
// también los terminales y `expired`, que siguen siendo filtrables y exportables
// aunque nadie pueda entrar ni salir de ellos. La vacía, una clave inventada y una
// clave válida en mayúsculas (`CONFIRMED`) NO lo son.
func IsStatus(status string) bool {
	panic(pendiente.Implementar("intakes.IsStatus"))
}

// CanTransition responde si la solicitud puede pasar de `from` a `to`. Normaliza
// ambos extremos, así que `closed` se evalúa como `confirmed` como origen Y como
// destino. El mapa COMPLETO de D-041.10 son 18 transiciones:
//
//	open              → confirmed | pending_approval | cancelled | abandoned
//	pending_approval  → confirmed | rejected | needs_info | cancelled
//	needs_info        → pending_approval | cancelled
//	confirmed         → pending_approval | deposit_requested | settled | cancelled
//	deposit_requested → deposit_paid | cancelled
//	deposit_paid      → settled | cancelled
//
// Todo lo demás es false, y cada negativa tiene su razón:
//
//   - Una transición a sí mismo no es una transición; `closed` → `confirmed`
//     tampoco, porque son la MISMA clave.
//   - settled, cancelled, rejected, abandoned y el legado expired son TERMINALES
//     ABSORBENTES: no salen a ninguna parte. `abandoned` en particular no revive
//     —el Plan 043 abandona lo que colgaba de un evento cancelado dando por hecho
//     que no vuelve solo—; su ENTRADA es open → abandoned.
//   - `expired` NO es destino de nada, desde ningún origen (D-041.16: nada vence
//     por tiempo). Es la clase de invariante que alguien «arregla» añadiendo una
//     entrada al mapa; no se añade.
//   - expired → abandoned tampoco: el descarte manual va por CanDiscard y por su
//     propia puerta, no por aquí.
//   - Una clave desconocida o vacía, en cualquiera de los dos extremos.
//
// `confirmed → pending_approval` SÍ existe: es la vuelta a un estado editable para
// RE-PRESUPUESTAR un pedido ya cerrado al que hay que ponerle precio (D-041.26).
// Sin ella, cobrar un añadido sin precio obligaría a cancelar y a que el cliente
// empezara de cero.
func CanTransition(from, to string) bool {
	panic(pendiente.Implementar("intakes.CanTransition"))
}

// AllowedTransitions devuelve los destinos válidos desde `from` (normalizado: una
// fila en `closed` ofrece lo que ofrece `confirmed`), en orden ALFABÉTICO
// —determinista: viaja en el cuerpo del 422 y en un `<select>` de la consola—. Las
// claves que devuelve son siempre canónicas: nunca `closed` y nunca `expired`.
//
// Un estado terminal o desconocido devuelve una lista VACÍA, nunca nil.
func AllowedTransitions(from string) []string {
	panic(pendiente.Implementar("intakes.AllowedTransitions"))
}

// CanDiscard responde si una solicitud en el estado `from` puede ser DESCARTADA a
// mano por el dueño (destino siempre abandoned, D-041.18, decisión de Jhoan del
// 2026-08-06). Solo dos orígenes: `open` y `expired`. Normaliza el alias legado
// igual que el resto del fichero, así que una fila guardada como `closed` se evalúa
// como `confirmed` — y no es descartable, que es lo correcto: lo confirmado se
// cancela, no se descarta.
//
// NO es una transición del ciclo de vida y es una pregunta APARTE a propósito:
// CanTransition responde «¿adónde puede ir esta solicitud?» y es lo que se publica
// en allowed_transitions; esto responde «¿es descartable?». `expired` es
// descartable y NO transiciona porque es el legado del reloj derogado (D-041.16):
// nadie entra ya en él, pero las filas históricas que quedaron dentro tienen que
// poder limpiarse de la bandeja — si no, serían las únicas inmortales del sistema.
// Abrirlo aquí NO abre expired → abandoned en CanTransition ni lo hace salir en
// AllowedTransitions: se ejecuta por su propio endpoint por lotes
// (POST /api/v1/intakes/discard), que consulta ESTO y jamás CanTransition.
func CanDiscard(from string) bool {
	panic(pendiente.Implementar("intakes.CanDiscard"))
}

// TransitionError es el rechazo de una transición inválida: dice DESDE dónde
// estaba la solicitud y ADÓNDE sí puede ir, que es lo que el llamante necesita
// para corregir sin adivinar (cuerpo del 422). Se devuelve como PUNTERO y así se
// recoge con errors.As.
type TransitionError struct {
	From    string
	To      string
	Allowed []string
}

// Error devuelve `transición inválida de "<From>" a "<To>"`, con los dos estados
// entrecomillados con %q (comillas y saltos de línea salen escapados). Allowed no
// entra en el texto: viaja aparte en el cuerpo del 422. Es un texto observable y se
// conserva byte a byte.
func (e *TransitionError) Error() string {
	panic(pendiente.Implementar("intakes.TransitionError.Error"))
}
