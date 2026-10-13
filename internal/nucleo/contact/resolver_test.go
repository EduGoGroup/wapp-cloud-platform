package contact

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
)

// dobleResolver es el doble mínimo con el que este fichero comprueba, EN COMPILACIÓN, la forma
// exacta de los dos puertos: un mismo tipo puede implementar Resolver y StateMigrator a la vez.
// Es un contacto que nunca existe y no promete nada más. Lo que Resolve y Destino prometen
// (fusión, preferencia de destino, aislamiento por tenant, concurrencia) lo fija la suite de
// contrato del puerto, que corre contra las implementaciones reales; sin una implementación no
// hay nada que afirmar aquí sobre ello.
type dobleResolver struct{}

func (dobleResolver) Resolve(_ context.Context, _ string, _ []Ref, _ string) (string, error) {
	return "", ErrNoRefs
}

func (dobleResolver) Destino(_ context.Context, _, contactID string) (Ref, error) {
	return Ref{}, fmt.Errorf("%w: %q", ErrContactNotFound, contactID)
}

func (dobleResolver) MigrateContactID(_ context.Context, _, _, _ string) error {
	return nil
}

var (
	_ Resolver      = (*dobleResolver)(nil)
	_ StateMigrator = (*dobleResolver)(nil)
)

// Los textos de los tres centinelas son observables y no cambian.
func TestCentinelas_Texto(t *testing.T) {
	for _, c := range []struct {
		nombre string
		err    error
		texto  string
	}{
		{"ErrNoRefs", ErrNoRefs, "contact: se requiere al menos una contact_ref"},
		{"ErrNoDestino", ErrNoDestino, "contact: sin destino enviable para el contact_id"},
		{"ErrContactNotFound", ErrContactNotFound, "contact: contact_id no encontrado"},
	} {
		t.Run(c.nombre, func(t *testing.T) {
			if got := c.err.Error(); got != c.texto {
				t.Errorf("%s = %q; quiere %q", c.nombre, got, c.texto)
			}
		})
	}
}

// Los centinelas se inspeccionan con errors.Is, aunque el adaptador los envuelva con %w y un
// detalle, y cada uno es distinto de los demás y de ErrInvalidRef.
func TestCentinelas_SeInspeccionanConErrorsIs(t *testing.T) {
	centinelas := []struct {
		nombre string
		err    error
	}{
		{"ErrNoRefs", ErrNoRefs},
		{"ErrNoDestino", ErrNoDestino},
		{"ErrContactNotFound", ErrContactNotFound},
		{"ErrInvalidRef", ErrInvalidRef},
	}
	for _, c := range centinelas {
		t.Run(c.nombre, func(t *testing.T) {
			envuelto := fmt.Errorf("%w: %q", c.err, "detalle")
			for _, otro := range centinelas {
				quiere := otro.nombre == c.nombre
				if got := errors.Is(envuelto, otro.err); got != quiere {
					t.Errorf("errors.Is(%s envuelto, %s) = %v; quiere %v", c.nombre, otro.nombre, got, quiere)
				}
			}
		})
	}
}

// R-20: un phone_e164 es direccionable tal cual y un wa_lid con su servidor.
func TestSendable_Direccionables(t *testing.T) {
	for _, c := range []struct {
		nombre string
		ref    Ref
		quiere string
	}{
		{"phone: el número tal cual, sin servidor", Ref{Kind: KindPhoneE164, Value: "573001112233"}, "573001112233"},
		{"lid: el LID con su servidor", Ref{Kind: KindWALID, Value: "88887777"}, "88887777@lid"},
	} {
		t.Run(c.nombre, func(t *testing.T) {
			got, err := c.ref.Sendable()
			if err != nil {
				t.Fatalf("%+v.Sendable(): error inesperado: %v", c.ref, err)
			}
			if got != c.quiere {
				t.Errorf("%+v.Sendable() = %q; quiere %q", c.ref, got, c.quiere)
			}
		})
	}
}

// R-20: cualquier otro kind, wa_username incluido, no es direccionable: ErrNoDestino con el
// kind en el texto, y la cadena de destino vacía.
func TestSendable_NoDireccionables(t *testing.T) {
	for _, c := range []struct {
		nombre string
		ref    Ref
		texto  string
	}{
		{"username: aún no direccionable", Ref{Kind: KindWAUsername, Value: "juanito"},
			`contact: sin destino enviable para el contact_id: kind "wa_username" no direccionable`},
		{"kind desconocido", Ref{Kind: "email", Value: "ana"},
			`contact: sin destino enviable para el contact_id: kind "email" no direccionable`},
		{"Ref cero", Ref{},
			`contact: sin destino enviable para el contact_id: kind "" no direccionable`},
	} {
		t.Run(c.nombre, func(t *testing.T) {
			got, err := c.ref.Sendable()
			if !errors.Is(err, ErrNoDestino) {
				t.Fatalf("%+v.Sendable() = %q, %v; quiere un error que envuelva ErrNoDestino", c.ref, got, err)
			}
			if err.Error() != c.texto {
				t.Errorf("texto observable = %q; quiere %q", err.Error(), c.texto)
			}
			if got != "" {
				t.Errorf("con error la cadena de destino debe ser \"\"; dio %q", got)
			}
		})
	}
}

// R-22: RefsFrom construye las Ref de un entrante con fromPn y fromLid y, solo como último
// recurso, con el JID crudo; descarta en silencio lo que no normaliza y puede dar vacío.
func TestRefsFrom(t *testing.T) {
	phone := Ref{Kind: KindPhoneE164, Value: "573001112233"}
	lid := Ref{Kind: KindWALID, Value: "88887777"}
	for _, c := range []struct {
		nombre, fromPn, fromLid, from string
		quiere                        []Ref
	}{
		{"número y LID: dos refs, primero el número", "573001112233", "88887777", "88887777@lid",
			[]Ref{phone, lid}},
		{"normaliza cada ref: número con separadores y LID con dispositivo", "+57 300 111 2233", "88887777:3@lid", "",
			[]Ref{phone, lid}},
		{"solo fromPn", "573001112233", "", "", []Ref{phone}},
		{"solo fromLid", "", "88887777", "", []Ref{lid}},
		{"con fromPn utilizable, el JID crudo se ignora", "573001112233", "", "999@lid", []Ref{phone}},
		{"con fromLid utilizable, el JID crudo se ignora", "abc", "88887777", "573001112233@s.whatsapp.net",
			[]Ref{lid}},
		{"JID crudo con @lid: infiere wa_lid", "", "", "88887777@lid", []Ref{lid}},
		{"JID crudo con @s.whatsapp.net: infiere phone_e164", "", "", "573001112233@s.whatsapp.net",
			[]Ref{phone}},
		{"JID crudo de LID con dispositivo: pierde el sufijo", "", "", "88887777:5@lid", []Ref{lid}},
		// El contrato dice «si CONTIENE "@lid"», no «si termina en "@lid"»: con el "@lid" en medio
		// del JID crudo el kind inferido sigue siendo wa_lid (y Normalize corta en el "@"). Con
		// «termina en», estas dos entradas darían el phone_e164 "88887777".
		{"raw JID with text after @lid: still infers wa_lid", "", "", "88887777@lid.whatsapp.net", []Ref{lid}},
		{"raw JID with a trailing blank after @lid: still infers wa_lid", "", "", "88887777@lid ", []Ref{lid}},
		{"fromPn no normaliza: cae al JID crudo", "abc", "", "573001112233@s.whatsapp.net", []Ref{phone}},
		{"fromPn y fromLid no normalizan: caen al JID crudo", "abc", "xyz", "573001112233@s.whatsapp.net",
			[]Ref{phone}},
		{"descarta en silencio el fromPn inválido y conserva el LID", "abc", "88887777", "", []Ref{lid}},
		{"descarta en silencio el fromLid inválido y conserva el número", "573001112233", "abc", "",
			[]Ref{phone}},
		{"JID crudo que no normaliza: vacío", "", "", "@s.whatsapp.net", nil},
		{"JID crudo de LID que no normaliza: vacío", "", "", "abc@lid", nil},
		{"todo inválido y sin JID crudo: vacío", "abc", "xyz", "", nil},
		{"nada: vacío", "", "", "", nil},
	} {
		t.Run(c.nombre, func(t *testing.T) {
			got := RefsFrom(c.fromPn, c.fromLid, c.from)
			if !slices.Equal(got, c.quiere) {
				t.Errorf("RefsFrom(%q, %q, %q) = %+v; quiere %+v", c.fromPn, c.fromLid, c.from, got, c.quiere)
			}
		})
	}
}

// N-05: fija el comportamiento ACTUAL, que no es necesariamente el deseable. En el respaldo, un
// JID de dispositivo normaliza como teléfono CON el dígito del dispositivo (Normalize conserva
// todo dígito). Si se decide corregirlo, se hace a la vez en el paquete anterior y en este, y
// este test cambia con ellos.
func TestRefsFrom_JIDDeDispositivoConservaElDigito(t *testing.T) {
	got := RefsFrom("", "", "573001112233:5@s.whatsapp.net")
	quiere := []Ref{{Kind: KindPhoneE164, Value: "5730011122335"}}
	if !slices.Equal(got, quiere) {
		t.Errorf("RefsFrom con JID de dispositivo = %+v; quiere %+v (el dígito del dispositivo se conserva)", got, quiere)
	}
}

// pickDestino (E-7: lo usan repository_memory.go y repository_postgres.go) elige la ref
// direccionable de mejor preferencia según destinoPref: phone_e164 > wa_username > wa_lid (R-19).
// Las no direccionables (wa_username) y los kinds fuera de destinoPref se saltan; entre dos del
// mismo kind gana la primera de la lista.
func TestPickDestino_ChoosesBestSendable(t *testing.T) {
	phone := Ref{Kind: KindPhoneE164, Value: "573001112233"}
	phone2 := Ref{Kind: KindPhoneE164, Value: "573009998877"}
	lid := Ref{Kind: KindWALID, Value: "88887777"}
	user := Ref{Kind: KindWAUsername, Value: "juanito"}
	unknown := Ref{Kind: "email", Value: "ana"}
	for _, c := range []struct {
		name string
		refs []Ref
		want Ref
	}{
		{"phone beats lid", []Ref{lid, phone}, phone},
		{"phone beats lid regardless of order", []Ref{phone, lid}, phone},
		{"username is skipped: degrades to lid", []Ref{user, lid}, lid},
		{"phone beats username and lid", []Ref{user, lid, phone}, phone},
		{"unknown kind is skipped", []Ref{unknown, lid}, lid},
		{"same kind: the first one wins", []Ref{phone, phone2}, phone},
		{"same kind, reversed: the first one wins", []Ref{phone2, lid, phone}, phone2},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := pickDestino(c.refs)
			if err != nil {
				t.Fatalf("pickDestino(%+v): error inesperado: %v", c.refs, err)
			}
			if got != c.want {
				t.Errorf("pickDestino(%+v) = %+v; quiere %+v", c.refs, got, c.want)
			}
		})
	}
}

// pickDestino devuelve ErrNoDestino SIN envolver (el texto exacto del centinela) y la Ref cero
// cuando ninguna ref es direccionable, incluida la lista vacía o nil.
func TestPickDestino_NoSendable(t *testing.T) {
	for _, c := range []struct {
		name string
		refs []Ref
	}{
		{"nil", nil},
		{"empty", []Ref{}},
		{"only username", []Ref{{Kind: KindWAUsername, Value: "juanito"}}},
		{"only unknown kinds", []Ref{{Kind: "email", Value: "ana"}, {}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := pickDestino(c.refs)
			if !errors.Is(err, ErrNoDestino) || err.Error() != ErrNoDestino.Error() {
				t.Fatalf("pickDestino(%+v) err = %v; quiere ErrNoDestino sin envolver", c.refs, err)
			}
			if got != (Ref{}) {
				t.Errorf("con error la Ref debe ser la cero; dio %+v", got)
			}
		})
	}
}

// destinoPref fija el orden phone_e164 < wa_username < wa_lid (menor = mejor) y solo conoce
// esos tres kinds.
func TestDestinoPref_Order(t *testing.T) {
	if destinoPref[KindPhoneE164] >= destinoPref[KindWAUsername] {
		t.Errorf("destinoPref = %v; quiere phone_e164 < wa_username", destinoPref)
	}
	if destinoPref[KindWAUsername] >= destinoPref[KindWALID] {
		t.Errorf("destinoPref = %v; quiere wa_username < wa_lid", destinoPref)
	}
	if len(destinoPref) != 3 {
		t.Errorf("destinoPref tiene %d kinds; quiere los 3 de Kind*", len(destinoPref))
	}
}

// dedupeRefs (E-7: primer paso de Resolve en los dos adaptadores) quita las refs repetidas por
// (kind, value) y conserva el orden de la primera aparición; el kind es parte de la clave.
func TestDedupeRefs(t *testing.T) {
	phone := Ref{Kind: KindPhoneE164, Value: "88887777"}
	lid := Ref{Kind: KindWALID, Value: "88887777"}
	other := Ref{Kind: KindPhoneE164, Value: "573001112233"}
	for _, c := range []struct {
		name string
		in   []Ref
		want []Ref
	}{
		{"no duplicates: same list, same order", []Ref{lid, phone, other}, []Ref{lid, phone, other}},
		{"duplicates removed, first occurrence order kept", []Ref{other, phone, other, lid, phone}, []Ref{other, phone, lid}},
		{"all the same: one left", []Ref{phone, phone, phone}, []Ref{phone}},
		{"same value, different kind: both kept", []Ref{phone, lid}, []Ref{phone, lid}},
		{"single ref", []Ref{phone}, []Ref{phone}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := dedupeRefs(c.in); !slices.Equal(got, c.want) {
				t.Errorf("dedupeRefs(%+v) = %+v; quiere %+v", c.in, got, c.want)
			}
		})
	}
}

// dedupeRefs con la entrada vacía devuelve vacía: nil sigue siendo nil y []Ref{} sigue vacía, que
// es lo que Resolve convierte en ErrNoRefs.
func TestDedupeRefs_Empty(t *testing.T) {
	if got := dedupeRefs(nil); got != nil {
		t.Errorf("dedupeRefs(nil) = %#v; quiere nil", got)
	}
	if got := dedupeRefs([]Ref{}); len(got) != 0 {
		t.Errorf("dedupeRefs([]Ref{}) = %#v; quiere vacía", got)
	}
}

// lidServer es el servidor JID de los LID de WhatsApp: "lid", parte del protocolo.
func TestLidServer(t *testing.T) {
	if lidServer != "lid" {
		t.Errorf("lidServer = %q; quiere \"lid\"", lidServer)
	}
}
