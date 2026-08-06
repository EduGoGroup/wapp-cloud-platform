package intakes

import (
	"fmt"
	"slices"
)

// Claves del ciclo de vida de una solicitud (design §D-041.10). Los
// IDENTIFICADORES van en inglés (INV-09); el nombre de negocio vive en la UI y en
// la doc, nunca en la BD ni en el wire.
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
	// (D-041.16). Ver la guarda explícita en CanTransition.
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
	// cambia su escritura»): se normaliza al leer, en un único punto.
	StatusClosedLegacy = "closed"
)

// aliases mapea cada clave LEGADA a su clave canónica. Es el ÚNICO punto donde se
// resuelve el alias en todo el sistema: si algún día hay que migrar las filas, se
// migran y esta tabla se vacía sin tocar nada más.
var aliases = map[string]string{
	StatusClosedLegacy: StatusConfirmed,
}

// transitions es el mapa COMPLETO de destinos válidos por estado de origen
// (D-041.10). Las claves ausentes son estados TERMINALES: settled, cancelled,
// rejected, abandoned y el legado expired no transicionan a ninguna parte.
//
// `confirmed → pending_approval` es la vuelta a un estado editable para
// RE-PRESUPUESTAR un pedido ya cerrado al que hay que ponerle precio (D-041.26).
// Sin ella, cobrar un añadido sin precio obligaría a cancelar y a que el cliente
// empezara de cero.
var transitions = map[string][]string{
	StatusOpen:             {StatusConfirmed, StatusPendingApproval, StatusCancelled, StatusAbandoned},
	StatusPendingApproval:  {StatusConfirmed, StatusRejected, StatusNeedsInfo, StatusCancelled},
	StatusNeedsInfo:        {StatusPendingApproval, StatusCancelled},
	StatusConfirmed:        {StatusPendingApproval, StatusDepositRequested, StatusSettled, StatusCancelled},
	StatusDepositRequested: {StatusDepositPaid, StatusCancelled},
	StatusDepositPaid:      {StatusSettled, StatusCancelled},
}

// discardable es el conjunto de estados desde los que el DUEÑO puede descartar
// una solicitud a mano (D-041.18, decisión de Jhoan del 2026-08-06). Es un mapa
// APARTE de `transitions` a propósito, y esa separación es la tarea entera:
//
//   - `transitions` responde «¿adónde puede ir esta solicitud?» y es lo que se
//     publica en allowed_transitions y lo que pinta el <select> de la consola. Meter
//     ahí expired → abandoned abriría esa transición en la API y en la pantalla,
//     que es justo lo que el dueño del producto cerró.
//   - `discardable` responde «¿es descartable?». Un destino (abandoned), dos
//     orígenes, y una sola puerta: el descarte manual por lotes.
//
// `expired` está aquí y NO en transitions porque es el legado del reloj derogado
// (D-041.16): nadie entra ya en él, pero las filas históricas que quedaron dentro
// tienen que poder limpiarse de la bandeja — si no, serían las únicas inmortales
// del sistema.
//
// ⚠️ Nace SIN LLAMANTES: su primer y único cliente es POST /api/v1/intakes/discard
// (T4.8), que consulta ESTO y jamás CanTransition. El riesgo es futuro: que alguien
// «unifique» el descarte sobre SetStatus y abra la transición en la API sin que
// nadie lo note. Lo que lo detiene no es este comentario sino el test de handler
// que exige 422 al pedir abandoned sobre una fila expired.
var discardable = map[string]bool{
	StatusOpen:    true,
	StatusExpired: true,
}

// known es el conjunto de claves de estado que el sistema reconoce como origen
// legible (incluye los terminales y el legado expired, que siguen siendo
// filtrables y exportables aunque nadie pueda entrar ni salir de ellos).
var known = map[string]bool{
	StatusOpen:             true,
	StatusPendingApproval:  true,
	StatusConfirmed:        true,
	StatusDepositRequested: true,
	StatusDepositPaid:      true,
	StatusSettled:          true,
	StatusCancelled:        true,
	StatusExpired:          true,
	StatusAbandoned:        true,
	StatusRejected:         true,
	StatusNeedsInfo:        true,
}

// NormalizeStatus resuelve la clave canónica de un estado: traduce los alias
// legados (hoy `closed` → `confirmed`) y deja el resto tal cual. Una clave vacía
// sigue vacía (el filtro "sin filtro"), y una desconocida se devuelve intacta para
// que el llamante decida si la rechaza — normalizar no es validar.
func NormalizeStatus(status string) string {
	if canonical, ok := aliases[status]; ok {
		return canonical
	}
	return status
}

// StoredVariants devuelve TODAS las formas en que un estado canónico puede estar
// escrito en BD. Es la contracara de NormalizeStatus y hace falta porque las filas
// legadas no se migran: filtrar por `confirmed` tiene que alcanzar también a las
// que el cart cerró como `closed`.
func StoredVariants(canonical string) []string {
	canonical = NormalizeStatus(canonical)
	variants := []string{canonical}
	for legacy, target := range aliases {
		if target == canonical {
			variants = append(variants, legacy)
		}
	}
	slices.Sort(variants)
	return variants
}

// IsStatus indica si la clave es un estado conocido del ciclo de vida (tras
// normalizar los alias).
func IsStatus(status string) bool {
	return known[NormalizeStatus(status)]
}

// CanTransition responde si la solicitud puede pasar de `from` a `to`. Normaliza
// ambos extremos, así que una fila legada en `closed` se evalúa como `confirmed`.
// Es la ÚNICA autoridad sobre el ciclo de vida: ni la BD tiene CHECK ni los
// handlers replican reglas.
func CanTransition(from, to string) bool {
	from, to = NormalizeStatus(from), NormalizeStatus(to)
	if from == to {
		return false // una transición a sí mismo no es una transición
	}
	// expired NO es destino de nada (D-041.16: nada vence por tiempo). La guarda
	// es explícita y no solo la ausencia en el mapa: es la clase de invariante que
	// alguien "arregla" añadiendo una entrada, y aquí queda dicho por qué no.
	if to == StatusExpired {
		return false
	}
	return slices.Contains(transitions[from], to)
}

// AllowedTransitions devuelve los destinos válidos desde `from`, en orden
// alfabético (determinista: viaja en el cuerpo del 422 y en un `<select>` de la
// consola). Un estado terminal o desconocido devuelve una lista vacía, nunca nil.
func AllowedTransitions(from string) []string {
	dest := transitions[NormalizeStatus(from)]
	out := make([]string, 0, len(dest))
	for _, d := range dest {
		if d == StatusExpired {
			continue // coherencia con la guarda de CanTransition
		}
		out = append(out, d)
	}
	slices.Sort(out)
	return out
}

// CanDiscard responde si una solicitud en el estado `from` puede ser DESCARTADA a
// mano por el dueño (destino siempre abandoned, D-041.18). Normaliza el alias
// legado igual que el resto del fichero, así que una fila guardada como `closed`
// se evalúa como `confirmed` — y no es descartable, que es lo correcto: lo
// confirmado se cancela, no se descarta.
//
// NO es una transición del ciclo de vida y por eso no sale en AllowedTransitions:
// no se le ofrece al operador en el selector de estado, se ejecuta por su propio
// endpoint por lotes. Ver el comentario de `discardable`.
func CanDiscard(from string) bool {
	return discardable[NormalizeStatus(from)]
}

// TransitionError es el rechazo de una transición inválida: dice DESDE dónde
// estaba la solicitud y ADÓNDE sí puede ir, que es lo que el llamante necesita
// para corregir sin adivinar (cuerpo del 422).
type TransitionError struct {
	From    string
	To      string
	Allowed []string
}

// Error implementa error.
func (e *TransitionError) Error() string {
	return fmt.Sprintf("transición inválida de %q a %q", e.From, e.To)
}
