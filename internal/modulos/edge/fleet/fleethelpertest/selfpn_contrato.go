package fleethelpertest

// Los casos del número propio (self_pn): SetSelfPn y CountLiveBySelfPn. El número se guarda y
// se compara en su forma CANÓNICA (E.164 sin "+" ni separadores): en Postgres la comparación es
// por índice ciego de ese valor, así que un doble que comparase el texto crudo contaría dos
// teléfonos donde producción cuenta uno, y el aviso del tope de dispositivos (REQ-D4) mentiría.

import (
	"context"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet"
)

// Un mismo número en tres grafías, y otro distinto. Son números de mentira.
const (
	selfPnCanonical = "56984467443"
	selfPnFormatted = "+56 9 8446-7443"
	selfPnDotted    = "(56) 9.8446.7443"
	selfPnOther     = "573001112233"
)

// caseSelfPnCanonical: lo que se guarda es el número normalizado, sea cual sea la grafía con la
// que llegó, y uno nuevo sustituye al anterior.
func caseSelfPnCanonical(t *testing.T, m Montaje) {
	tenant, edge, session := seedTenant(t, m), unique("edge"), unique("session")
	markOnline(t, m, tenant, edge, session)
	requireSelfPn(t, m, tenant, edge, session, "") // sin emparejar

	for _, spelling := range []string{selfPnFormatted, selfPnDotted, selfPnCanonical} {
		setSelfPn(t, m, tenant, edge, session, spelling)
		requireSelfPn(t, m, tenant, edge, session, selfPnCanonical)
	}
	setSelfPn(t, m, tenant, edge, session, "+"+selfPnOther)
	requireSelfPn(t, m, tenant, edge, session, selfPnOther)
}

// caseSelfPnEmpty: un self_pn vacío es un no-op. El Heartbeat de una sesión que aún no se
// emparejó no borra un valor previo bueno.
func caseSelfPnEmpty(t *testing.T, m Montaje) {
	tenant, edge, session := seedTenant(t, m), unique("edge"), unique("session")
	markOnline(t, m, tenant, edge, session)
	setSelfPn(t, m, tenant, edge, session, selfPnFormatted)
	setSelfPn(t, m, tenant, edge, session, "")
	requireSelfPn(t, m, tenant, edge, session, selfPnCanonical)
}

// caseSelfPnInvalid: un número que no normaliza —sin un solo dígito, o con más de los 15 de
// E.164— da ERROR y no escribe nada. No es un no-op silencioso: decir que salió bien haría
// creer que el número quedó guardado.
func caseSelfPnInvalid(t *testing.T, m Montaje) {
	tenant, edge, session := seedTenant(t, m), unique("edge"), unique("session")
	markOnline(t, m, tenant, edge, session)
	setSelfPn(t, m, tenant, edge, session, selfPnCanonical)

	for _, bad := range []string{"sin-digitos", "+", "1234567890123456"} {
		if err := m.Repository.SetSelfPn(context.Background(), tenant, edge, session, bad); err == nil {
			t.Errorf("SetSelfPn con un número que no normaliza (%d bytes) no devolvió error", len(bad))
		}
		requireSelfPn(t, m, tenant, edge, session, selfPnCanonical)
	}
}

// caseSelfPnUnknown: el número de una sesión que aún no se registró no es un error, y no crea
// la fila.
func caseSelfPnUnknown(t *testing.T, m Montaje) {
	tenant, edge, session := seedTenant(t, m), unique("edge"), unique("session")
	setSelfPn(t, m, tenant, edge, session, selfPnCanonical)
	if _, found, err := m.Repository.Get(context.Background(), tenant, edge, session); err != nil || found {
		t.Errorf("tras SetSelfPn de una sesión desconocida, Get = (found=%v, err=%v), quería (false, nil)", found, err)
	}
	requireCount(t, m, tenant, selfPnCanonical, 0)
}

// caseCountCanonical: se cuenta por la forma canónica. Dos sesiones que reportaron el mismo
// número con grafías distintas son el MISMO teléfono, se pregunte con la grafía que se
// pregunte; otro número no entra en la cuenta.
func caseCountCanonical(t *testing.T, m Montaje) {
	tenant, edge := seedTenant(t, m), unique("edge")
	first, second, third := unique("session"), unique("session"), unique("session")
	for _, session := range []string{first, second, third} {
		markOnline(t, m, tenant, edge, session)
	}
	setSelfPn(t, m, tenant, edge, first, selfPnFormatted)
	setSelfPn(t, m, tenant, edge, second, selfPnCanonical)
	setSelfPn(t, m, tenant, edge, third, selfPnOther)

	for _, spelling := range []string{selfPnCanonical, selfPnFormatted, selfPnDotted} {
		requireCount(t, m, tenant, spelling, 2)
	}
	requireCount(t, m, tenant, selfPnOther, 1)
	requireCount(t, m, tenant, "34600000000", 0)
}

// caseCountExcludesZombies: cuentan las sesiones VIVAS, que son todas menos la zombie. Una
// sesión offline por red sigue ocupando su plaza de dispositivo; la que WhatsApp cerró, no.
func caseCountExcludesZombies(t *testing.T, m Montaje) {
	tenant, edge := seedTenant(t, m), unique("edge")
	online, offline, zombie, retired := unique("session"), unique("session"), unique("session"), unique("session")
	for _, session := range []string{online, offline, zombie, retired} {
		markOnline(t, m, tenant, edge, session)
		setSelfPn(t, m, tenant, edge, session, selfPnCanonical)
	}
	requireCount(t, m, tenant, selfPnCanonical, 4)

	markOffline(t, m, tenant, edge, offline)
	markLoggedOut(t, m, tenant, edge, zombie)
	setState(t, m, tenant, retired, fleet.StateLoggedOut)
	requireCount(t, m, tenant, selfPnCanonical, 2)

	// La zombie que se reempareja y reconecta vuelve a contar.
	markOnline(t, m, tenant, edge, zombie)
	requireCount(t, m, tenant, selfPnCanonical, 3)
}

// caseCountPerTenant: el mismo número en otro tenant no contamina la cuenta.
func caseCountPerTenant(t *testing.T, m Montaje) {
	owner, stranger := seedTwoTenants(t, m)
	edge, mine, theirs := unique("edge"), unique("session"), unique("session")
	markOnline(t, m, owner, edge, mine)
	setSelfPn(t, m, owner, edge, mine, selfPnCanonical)
	for _, session := range []string{theirs, unique("session")} {
		markOnline(t, m, stranger, edge, session)
		setSelfPn(t, m, stranger, edge, session, selfPnCanonical)
	}

	requireCount(t, m, owner, selfPnCanonical, 1)
	requireCount(t, m, stranger, selfPnCanonical, 2)
	requireCount(t, m, seedTenant(t, m), selfPnCanonical, 0)
}

// caseCountEmpty: sin número no hay número que contar. Da 0 sin error aunque haya sesiones que
// todavía no reportaron el suyo.
func caseCountEmpty(t *testing.T, m Montaje) {
	tenant := seedTenant(t, m)
	markOnline(t, m, tenant, unique("edge"), unique("session"))
	requireCount(t, m, tenant, "", 0)
}

// caseCountInvalid: un número que no normaliza da error, no 0: «no puedo contar» y «hay cero»
// son respuestas distintas, y un 0 aquí apagaría el aviso del tope justo en el caso raro.
func caseCountInvalid(t *testing.T, m Montaje) {
	tenant := seedTenant(t, m)
	for _, bad := range []string{"sin-digitos", "1234567890123456"} {
		n, err := m.Repository.CountLiveBySelfPn(context.Background(), tenant, bad)
		if err == nil {
			t.Errorf("CountLiveBySelfPn con un número que no normaliza (%d bytes) no devolvió error", len(bad))
		}
		if n != 0 {
			t.Errorf("CountLiveBySelfPn con un número que no normaliza devolvió %d junto al error, quería 0", n)
		}
	}
}
