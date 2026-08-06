package intakes_test

import (
	"slices"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// transición es un par origen→destino del ciclo de vida (D-041.10).
type transición struct{ from, to string }

// todasLasVálidas es el mapa COMPLETO de D-041.10, escrito a mano aquí para que el
// test no dependa del mismo dato que valida: si alguien toca el mapa de producción,
// esta lista lo delata en vez de moverse con él.
var todasLasVálidas = []transición{
	// open → confirmed | pending_approval | cancelled | abandoned
	{intakes.StatusOpen, intakes.StatusConfirmed},
	{intakes.StatusOpen, intakes.StatusPendingApproval},
	{intakes.StatusOpen, intakes.StatusCancelled},
	{intakes.StatusOpen, intakes.StatusAbandoned},
	// pending_approval → confirmed | rejected | needs_info | cancelled
	{intakes.StatusPendingApproval, intakes.StatusConfirmed},
	{intakes.StatusPendingApproval, intakes.StatusRejected},
	{intakes.StatusPendingApproval, intakes.StatusNeedsInfo},
	{intakes.StatusPendingApproval, intakes.StatusCancelled},
	// needs_info → pending_approval | cancelled
	{intakes.StatusNeedsInfo, intakes.StatusPendingApproval},
	{intakes.StatusNeedsInfo, intakes.StatusCancelled},
	// confirmed → pending_approval | deposit_requested | settled | cancelled
	// (pending_approval es la vuelta a editable para re-presupuestar, D-041.26)
	{intakes.StatusConfirmed, intakes.StatusPendingApproval},
	{intakes.StatusConfirmed, intakes.StatusDepositRequested},
	{intakes.StatusConfirmed, intakes.StatusSettled},
	{intakes.StatusConfirmed, intakes.StatusCancelled},
	// deposit_requested → deposit_paid | cancelled
	{intakes.StatusDepositRequested, intakes.StatusDepositPaid},
	{intakes.StatusDepositRequested, intakes.StatusCancelled},
	// deposit_paid → settled | cancelled
	{intakes.StatusDepositPaid, intakes.StatusSettled},
	{intakes.StatusDepositPaid, intakes.StatusCancelled},
}

// TestCanTransition_TodasLasVálidas recorre el mapa COMPLETO de D-041.10.
func TestCanTransition_TodasLasVálidas(t *testing.T) {
	for _, tr := range todasLasVálidas {
		t.Run(tr.from+"→"+tr.to, func(t *testing.T) {
			if !intakes.CanTransition(tr.from, tr.to) {
				t.Fatalf("CanTransition(%q,%q)=false; D-041.10 la declara VÁLIDA", tr.from, tr.to)
			}
		})
	}
	if len(todasLasVálidas) != 18 {
		t.Fatalf("el mapa de D-041.10 tiene 18 transiciones; el test enumera %d", len(todasLasVálidas))
	}
}

// TestCanTransition_Inválidas cubre las que el ciclo de vida NIEGA, cada una por
// una razón distinta: saltos de etapa, vuelta atrás, terminales absorbentes,
// destino legado y claves inexistentes.
func TestCanTransition_Inválidas(t *testing.T) {
	casos := []struct {
		nombre   string
		from, to string
	}{
		{"salto de etapa: abierto a saldado", intakes.StatusOpen, intakes.StatusSettled},
		{"salto de etapa: abierto a seña solicitada", intakes.StatusOpen, intakes.StatusDepositRequested},
		{"salto de etapa: confirmado a señado", intakes.StatusConfirmed, intakes.StatusDepositPaid},
		{"vuelta atrás: confirmado a abierto", intakes.StatusConfirmed, intakes.StatusOpen},
		{"terminal absorbente: cancelado no revive", intakes.StatusCancelled, intakes.StatusConfirmed},
		{"terminal absorbente: saldado no se cancela", intakes.StatusSettled, intakes.StatusCancelled},
		{"terminal absorbente: abandonado no vuelve a abierto", intakes.StatusAbandoned, intakes.StatusOpen},
		{"terminal absorbente: rechazado no se confirma", intakes.StatusRejected, intakes.StatusConfirmed},
		{"legado: vencido no tiene salida", intakes.StatusExpired, intakes.StatusCancelled},
		{"clave inexistente como destino", intakes.StatusOpen, "en_camino"},
		{"clave inexistente como origen", "en_camino", intakes.StatusConfirmed},
		{"a sí mismo no es transición", intakes.StatusConfirmed, intakes.StatusConfirmed},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if intakes.CanTransition(c.from, c.to) {
				t.Fatalf("CanTransition(%q,%q)=true; debe ser INVÁLIDA", c.from, c.to)
			}
		})
	}
}

// TestCanTransition_ExpiredNuncaEsDestino: D-041.16 derogó el vencimiento por
// tiempo. `expired` sobrevive como valor legible de filas históricas, pero NADA
// puede entrar en él desde ningún origen conocido.
func TestCanTransition_ExpiredNuncaEsDestino(t *testing.T) {
	orígenes := []string{
		intakes.StatusOpen, intakes.StatusPendingApproval, intakes.StatusConfirmed,
		intakes.StatusDepositRequested, intakes.StatusDepositPaid, intakes.StatusSettled,
		intakes.StatusCancelled, intakes.StatusExpired, intakes.StatusAbandoned,
		intakes.StatusRejected, intakes.StatusNeedsInfo, intakes.StatusClosedLegacy,
	}
	for _, from := range orígenes {
		if intakes.CanTransition(from, intakes.StatusExpired) {
			t.Fatalf("CanTransition(%q,\"expired\")=true; nada vence por tiempo (D-041.16)", from)
		}
		if slices.Contains(intakes.AllowedTransitions(from), intakes.StatusExpired) {
			t.Fatalf("AllowedTransitions(%q) ofrece \"expired\"; nadie puede entrar ahí", from)
		}
	}
}

// TestCanTransition_AbandonedEsAbsorbente es el criterio de T4.6 escrito como RED,
// no como trabajo: `abandoned` ya era terminal por construcción —es una clave
// AUSENTE del mapa `transitions`— y esto pasa sin tocar producción.
//
// Se deja igualmente porque la ausencia es una propiedad frágil y los tests de
// arriba no la cubren entera: solo miran `abandoned → open`. Quien añadiera una
// salida para "poder reabrir un abandonado" no rompería ninguno de ellos, y el Plan
// 043 se apoya justo en lo contrario — abandona lo que colgaba de un evento
// cancelado dando por hecho que no revive solo.
func TestCanTransition_AbandonedEsAbsorbente(t *testing.T) {
	// La lista va escrita a mano, como todo en este fichero: derivarla del mapa de
	// producción haría que el test se moviera con lo que debe vigilar.
	destinos := []string{
		intakes.StatusOpen, intakes.StatusPendingApproval, intakes.StatusConfirmed,
		intakes.StatusClosedLegacy, intakes.StatusDepositRequested, intakes.StatusDepositPaid,
		intakes.StatusSettled, intakes.StatusCancelled, intakes.StatusExpired,
		intakes.StatusAbandoned, intakes.StatusRejected, intakes.StatusNeedsInfo,
		"en_camino", "",
	}
	for _, to := range destinos {
		if intakes.CanTransition(intakes.StatusAbandoned, to) {
			t.Fatalf("CanTransition(\"abandoned\",%q)=true; es terminal ABSORBENTE (D-041.10)", to)
		}
	}
	if got := intakes.AllowedTransitions(intakes.StatusAbandoned); len(got) != 0 {
		t.Fatalf("AllowedTransitions(abandoned)=%v; una solicitud abandonada no ofrece destinos", got)
	}
	// La ENTRADA es la otra mitad y es la que el Plan 043 invoca al cancelar el
	// evento: lo que estaba abierto se abandona.
	if !intakes.CanTransition(intakes.StatusOpen, intakes.StatusAbandoned) {
		t.Fatal("CanTransition(\"open\",\"abandoned\")=false; el evento cancelado abandona lo abierto")
	}
}

// TestCanDiscard_OpenYExpired: el descarte manual (D-041.18) es una pregunta
// APARTE de la máquina de transiciones, y este test fija las dos mitades a la vez
// —lo que CanDiscard permite y lo que CanTransition sigue negando— porque la
// tentación futura es exactamente fundirlas.
//
// `expired` es descartable: son las filas que dejó el reloj derogado (D-041.16) y,
// si no se pudieran descartar, serían las únicas inmortales de la bandeja. Pero
// descartarlas NO es una transición ofrecida: no sale en allowed_transitions y
// POST /intakes/{id}/status la rechaza (ver el test de handler en publicapi).
func TestCanDiscard_OpenYExpired(t *testing.T) {
	for _, from := range []string{intakes.StatusOpen, intakes.StatusExpired} {
		if !intakes.CanDiscard(from) {
			t.Fatalf("CanDiscard(%q)=false; el dueño tiene que poder descartarlo (D-041.18)", from)
		}
	}
	for _, from := range []string{
		intakes.StatusPendingApproval, intakes.StatusConfirmed, intakes.StatusClosedLegacy,
		intakes.StatusDepositRequested, intakes.StatusDepositPaid, intakes.StatusSettled,
		intakes.StatusCancelled, intakes.StatusAbandoned, intakes.StatusRejected,
		intakes.StatusNeedsInfo, "en_camino", "",
	} {
		if intakes.CanDiscard(from) {
			t.Fatalf("CanDiscard(%q)=true; solo se descarta lo abierto y lo vencido", from)
		}
	}
	// La otra mitad: abrirlo por CanDiscard NO abrió la transición. Si esto se pone
	// verde al revés, el <select> de la consola empezaría a ofrecer «abandonar»
	// sobre filas vencidas, que es justo lo que la decisión del 2026-08-06 prohíbe.
	if intakes.CanTransition(intakes.StatusExpired, intakes.StatusAbandoned) {
		t.Fatal("expired→abandoned NO es transición: el descarte va por su propia puerta (T4.8)")
	}
	if got := intakes.AllowedTransitions(intakes.StatusExpired); len(got) != 0 {
		t.Fatalf("AllowedTransitions(expired)=%v; una fila vencida no ofrece destinos", got)
	}
}

// TestCanTransition_ClosedLegado: la clave con la que el módulo cart cierra desde
// el Plan 016 se comporta EXACTAMENTE como `confirmed`, en los dos extremos de la
// transición. Es lo que permite no migrar las filas históricas.
func TestCanTransition_ClosedLegado(t *testing.T) {
	if !intakes.CanTransition(intakes.StatusClosedLegacy, intakes.StatusDepositRequested) {
		t.Fatal("una solicitud legada en `closed` debe poder pedir seña: es un `confirmed`")
	}
	if !intakes.CanTransition(intakes.StatusOpen, intakes.StatusClosedLegacy) {
		t.Fatal("pedir `closed` como destino debe valer como pedir `confirmed`")
	}
	if intakes.CanTransition(intakes.StatusClosedLegacy, intakes.StatusConfirmed) {
		t.Fatal("`closed`→`confirmed` es la MISMA clave: no es una transición")
	}
}

func TestNormalizeStatus(t *testing.T) {
	casos := []struct{ in, want string }{
		{intakes.StatusClosedLegacy, intakes.StatusConfirmed},
		{intakes.StatusConfirmed, intakes.StatusConfirmed},
		{intakes.StatusOpen, intakes.StatusOpen},
		{intakes.StatusExpired, intakes.StatusExpired},
		{"", ""},
		{"en_camino", "en_camino"}, // normalizar no es validar
	}
	for _, c := range casos {
		if got := intakes.NormalizeStatus(c.in); got != c.want {
			t.Fatalf("NormalizeStatus(%q)=%q, quiero %q", c.in, got, c.want)
		}
	}
}

// TestStoredVariants: filtrar por `confirmed` tiene que alcanzar también a las
// filas que el cart escribió como `closed` — si no, la bandeja pierde todo lo
// cerrado antes del Plan 041.
func TestStoredVariants(t *testing.T) {
	got := intakes.StoredVariants(intakes.StatusConfirmed)
	if !slices.Equal(got, []string{intakes.StatusClosedLegacy, intakes.StatusConfirmed}) {
		t.Fatalf("StoredVariants(confirmed)=%v, quiero [closed confirmed]", got)
	}
	if got := intakes.StoredVariants(intakes.StatusClosedLegacy); !slices.Equal(got, []string{intakes.StatusClosedLegacy, intakes.StatusConfirmed}) {
		t.Fatalf("StoredVariants(closed)=%v; el alias resuelve a las MISMAS variantes", got)
	}
	if got := intakes.StoredVariants(intakes.StatusOpen); !slices.Equal(got, []string{intakes.StatusOpen}) {
		t.Fatalf("StoredVariants(open)=%v, quiero [open] (sin alias)", got)
	}
}

// TestAllowedTransitions_OrdenYTerminales: el 422 y el `<select>` de la consola
// leen esta lista, así que tiene que ser determinista y no-nil.
func TestAllowedTransitions_OrdenYTerminales(t *testing.T) {
	got := intakes.AllowedTransitions(intakes.StatusConfirmed)
	want := []string{"cancelled", "deposit_requested", "pending_approval", "settled"}
	if !slices.Equal(got, want) {
		t.Fatalf("AllowedTransitions(confirmed)=%v, quiero %v (alfabético)", got, want)
	}
	for _, terminal := range []string{
		intakes.StatusSettled, intakes.StatusCancelled, intakes.StatusRejected,
		intakes.StatusAbandoned, intakes.StatusExpired, "en_camino",
	} {
		got := intakes.AllowedTransitions(terminal)
		if got == nil {
			t.Fatalf("AllowedTransitions(%q)=nil; debe ser lista vacía, no nula", terminal)
		}
		if len(got) != 0 {
			t.Fatalf("AllowedTransitions(%q)=%v; es terminal/desconocido", terminal, got)
		}
	}
}

func TestIsStatus(t *testing.T) {
	for _, s := range []string{
		intakes.StatusOpen, intakes.StatusPendingApproval, intakes.StatusConfirmed,
		intakes.StatusDepositRequested, intakes.StatusDepositPaid, intakes.StatusSettled,
		intakes.StatusCancelled, intakes.StatusExpired, intakes.StatusAbandoned,
		intakes.StatusRejected, intakes.StatusNeedsInfo, intakes.StatusClosedLegacy,
	} {
		if !intakes.IsStatus(s) {
			t.Fatalf("IsStatus(%q)=false; es una clave del ciclo de vida", s)
		}
	}
	for _, s := range []string{"", "en_camino", "CONFIRMED"} {
		if intakes.IsStatus(s) {
			t.Fatalf("IsStatus(%q)=true; no es una clave del ciclo de vida", s)
		}
	}
}
