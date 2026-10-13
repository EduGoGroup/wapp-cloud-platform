package grpc

// El número propio del latido (self_pn) y el aviso del tope de dispositivos (REQ-D4).
//
// 🔴 CORPUS DE EQUIVALENCIA (reglas §0; hallazgos 23 y 32 de F3). persistSelfPn normaliza el
// valor CRUDO del latido —sin limpiar antes el JID— con la regla de phone_e164: se queda con
// los dígitos ASCII, en su orden, y descarta el resto; sin dígitos, o con más de 15, no
// normaliza. Los valores esperados están escritos A MANO a partir de lo que hace el paquete
// viejo, sin llamarlo ni llamar al normalizador: si la regla cambiara un byte, el índice ciego
// de fleet_sessions.self_pn_bidx dejaría de casar sin un solo error.
//
// Lo que el corpus deja afirmado, NO corregido: un JID con sufijo de dispositivo persiste
// OTRO número (el dígito del dispositivo se concatena).

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
)

// selfPnCorpus: lo que el Edge manda en Heartbeat.self_pn y lo que se persiste. want vacío =
// no normaliza: se descarta y no se llama a fleet.
var selfPnCorpus = []struct {
	name string
	raw  string
	want string
}{
	// Lo que se espera que mande un Edge sano.
	{"clean number", "573001112233", "573001112233"},
	{"leading plus", "+573001112233", "573001112233"},
	{"human separators", "+57 (300) 111-22.33", "573001112233"},
	{"leading zeros are kept", "00573001112233", "00573001112233"},
	{"a single digit is a number", "7", "7"},
	{"exactly fifteen digits", "123456789012345", "123456789012345"},
	{"sixteen digits is not a phone", "1234567890123456", ""},

	// 🔴 JID sin limpiar: el servidor se cae solo (no tiene dígitos), el dispositivo NO.
	{"jid without device", "573001112233@s.whatsapp.net", "573001112233"},
	{"jid with device suffix", "573001112233:5@s.whatsapp.net", "5730011122335"},
	{"jid with two-digit device", "573001112233:12@s.whatsapp.net", "57300111223312"},
	{"jid with agent and device", "573001112233.0:5@s.whatsapp.net", "57300111223305"},
	{"jid whose device fills the fifteen digits", "573001112233:123@s.whatsapp.net", "573001112233123"},
	{"jid whose device overflows the fifteen digits", "573001112233:1234@s.whatsapp.net", ""},
	{"lid jid is taken as a phone", "123456789012345@lid", "123456789012345"},

	// Separadores repetidos.
	{"repeated separator without digits", "a@@b", ""},
	{"repeated separator between digits", "57300@@1112233", "573001112233"},
	{"only separators", "@@::..", ""},

	// Dígitos no ASCII: no cuentan, y entre dígitos ASCII se pierden en silencio.
	{"arabic-indic digits", "٥٧٣٠٠١١١٢٢٣٣", ""},
	{"extended arabic-indic digits", "۵۷۳۰۰۱۱۱۲۲۳۳", ""},
	{"fullwidth digits", "５７３００１１１２２３３", ""},
	{"devanagari digits", "५७३००१११२२३३", ""},
	{"one non-ascii digit among ascii ones is dropped", "57٣001112233", "57001112233"},
	{"superscript digits are not digits", "57³001112233", "57001112233"},

	// Espacios Unicode e invisibles: se descartan como cualquier otro no-dígito.
	{"no-break space", "57\u00a0300\u00a0111\u00a02233", "573001112233"},
	{"em space and zero width space", "57\u2003300\u200b1112233", "573001112233"},
	{"byte order mark around", "\ufeff573001112233\ufeff", "573001112233"},
	{"ideographic space and tab", "57\u3000300\t111\n2233", "573001112233"},
	{"only unicode spaces", "\u00a0\u2003\u200b\ufeff", ""},
	{"only ascii spaces", "   ", ""},
}

// requireNoNumber afirma que el log no contiene el número (PII), ni crudo ni normalizado.
func requireNoNumber(t *testing.T, log *logBuffer, values ...string) {
	t.Helper()
	for _, v := range values {
		if v != "" && log.contains(v) {
			t.Errorf("el log contiene el número %q (PII): %q", v, log.String())
		}
	}
}

// El corpus, caso a caso: lo que normaliza llega a fleet YA normalizado —acotado por el
// (tenant, Edge, sesión) del canal— y por ese mismo valor se cuenta después; lo que no
// normaliza se descarta en debug sin llamar a fleet. El número no aparece nunca en el log.
func TestPersistSelfPnNormalizesTheRawHeartbeatValue(t *testing.T) {
	t.Parallel()
	for _, tc := range selfPnCorpus {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rig := newHeartbeatRig()
			cc := phone("tenant-1", "edge-1", "s-1")
			rig.online(t, cc)

			rig.srv.persistSelfPn(context.Background(), cc, &cloudlinkv1.Heartbeat{SelfPn: tc.raw})

			var want []fleetCall
			if tc.want != "" {
				want = []fleetCall{
					{"SetSelfPn", "tenant-1", "edge-1", "s-1", tc.want},
					{method: "CountLiveBySelfPn", tenantID: "tenant-1", selfPn: tc.want},
				}
			}
			if got := rig.fleet.recorded(); !reflect.DeepEqual(got, want) {
				t.Fatalf("self_pn %q: llamadas a fleet = %+v, se esperaba %+v", tc.raw, got, want)
			}
			if got := rig.row(t, cc).SelfPn; got != tc.want {
				t.Errorf("self_pn %q: persistido %q, se esperaba %q", tc.raw, got, tc.want)
			}
			if tc.want == "" {
				rig.requireLog(t, "level=DEBUG", `msg="heartbeat: self_pn no normalizable; se descarta"`, "session_id=s-1", "edge_id=edge-1")
			} else if rig.log.String() != "" {
				t.Errorf("self_pn %q: un número guardado no deja rastro: %q", tc.raw, rig.log.String())
			}
			requireNoNumber(t, rig.log, strings.TrimSpace(tc.raw), tc.want)
		})
	}
}

// Un latido sin self_pn (sesión sin emparejar aún) no toca el número ya guardado: ni se llama
// a fleet ni se dice nada.
func TestPersistSelfPnEmptyKeepsThePreviousNumber(t *testing.T) {
	t.Parallel()
	rig := newHeartbeatRig()
	cc := phone("tenant-1", "edge-1", "s-1")
	rig.online(t, cc)
	rig.srv.persistSelfPn(context.Background(), cc, &cloudlinkv1.Heartbeat{SelfPn: "573001112233"})
	before := len(rig.fleet.recorded())

	rig.srv.persistSelfPn(context.Background(), cc, &cloudlinkv1.Heartbeat{})

	if got := rig.fleet.recorded(); len(got) != before {
		t.Errorf("el latido sin número llamó a fleet: %+v", got[before:])
	}
	if got := rig.row(t, cc).SelfPn; got != "573001112233" {
		t.Errorf("el número previo cambió a %q", got)
	}
}

// Si fleet falla al guardar el número se registra el error con IDs opacos —sin el número— y
// NO se pasa a contar: no hay número recién persistido sobre el que avisar.
func TestPersistSelfPnLogsAFleetFailureAndSkipsTheCount(t *testing.T) {
	t.Parallel()
	rig := newHeartbeatRig()
	rig.fleet.fail["SetSelfPn"] = errors.New("base caída")

	rig.srv.persistSelfPn(context.Background(), phone("tenant-1", "edge-1", "s-1"), &cloudlinkv1.Heartbeat{SelfPn: "+573001112233"})

	want := []fleetCall{{"SetSelfPn", "tenant-1", "edge-1", "s-1", "573001112233"}}
	if got := rig.fleet.recorded(); !reflect.DeepEqual(got, want) {
		t.Errorf("llamadas a fleet = %+v, se esperaba solo la escritura fallida", got)
	}
	rig.requireLog(t, "level=ERROR", `msg="fleet: persistir self_pn"`, "base caída", "edge_id=edge-1", "session_id=s-1")
	requireNoNumber(t, rig.log, "573001112233")
}

// pair deja `n` sesiones vivas del tenant emparejadas con ese número, cada una con su latido,
// y devuelve la última.
func (r *heartbeatRig) pair(t *testing.T, tenantID string, n int, prefix, selfPn string) connCtx {
	t.Helper()
	var last connCtx
	for i := range n {
		last = phone(tenantID, "edge-1", fmt.Sprintf("%s-%d", prefix, i+1))
		r.online(t, last)
		r.srv.persistSelfPn(context.Background(), last, &cloudlinkv1.Heartbeat{SelfPn: selfPn})
	}
	return last
}

const deviceLimitWarning = "un número supera el tope de dispositivos de WhatsApp"

// REQ-D4: se avisa cuando el número recién guardado tiene MÁS sesiones vivas que el tope de
// dispositivos de WhatsApp (4): la cuarta no avisa, la quinta sí. Es solo detección —la
// quinta se guarda igual— y el aviso lleva el conteo, el tope y los IDs opacos, nunca el número.
func TestDeviceLimitWarnsAboveFourLiveSessions(t *testing.T) {
	t.Parallel()
	rig := newHeartbeatRig()

	rig.pair(t, "tenant-1", 4, "s", "573001112233")
	if rig.log.contains(deviceLimitWarning) {
		t.Fatalf("con cuatro sesiones no se supera el tope: %q", rig.log.String())
	}

	fifth := phone("tenant-1", "edge-2", "s-5")
	rig.online(t, fifth)
	rig.srv.persistSelfPn(context.Background(), fifth, &cloudlinkv1.Heartbeat{SelfPn: "+57 300 111 2233"})

	rig.requireLog(t, "level=WARN", `msg="`+deviceLimitWarning+`"`, "session_id=s-5", "edge_id=edge-2", "sesiones_vivas=5", "tope=4")
	if got := strings.Count(rig.log.String(), deviceLimitWarning); got != 1 {
		t.Errorf("avisos = %d, se esperaba 1", got)
	}
	if got := rig.row(t, fifth).SelfPn; got != "573001112233" {
		t.Errorf("el aviso no bloquea: la quinta sesión debía guardar su número, tiene %q", got)
	}
	requireNoNumber(t, rig.log, "573001112233", "300 111")
}

// El conteo es por tenant: cinco sesiones del mismo número repartidas entre dos empresas no
// superan el tope de ninguna.
func TestDeviceLimitCountsPerTenant(t *testing.T) {
	t.Parallel()
	rig := newHeartbeatRig()
	rig.pair(t, "tenant-1", 3, "s", "573001112233")
	rig.pair(t, "tenant-2", 2, "t", "573001112233")
	if rig.log.contains(deviceLimitWarning) {
		t.Errorf("tres y dos sesiones en empresas distintas no superan el tope: %q", rig.log.String())
	}
}

// 🔴 Consecuencia del hallazgo 23, afirmada tal cual: una sesión que reporta el JID con su
// sufijo de dispositivo queda guardada como OTRO número, así que ni cuenta para el tope del
// número real ni hace saltar su aviso.
func TestDeviceLimitMissesASessionThatReportsItsJID(t *testing.T) {
	t.Parallel()
	rig := newHeartbeatRig()
	rig.pair(t, "tenant-1", 4, "s", "573001112233")

	fifth := phone("tenant-1", "edge-2", "s-5")
	rig.online(t, fifth)
	rig.srv.persistSelfPn(context.Background(), fifth, &cloudlinkv1.Heartbeat{SelfPn: "573001112233:5@s.whatsapp.net"})

	if rig.log.contains(deviceLimitWarning) {
		t.Errorf("la conducta copiada del viejo cambió: el JID con dispositivo contó como el número real: %q", rig.log.String())
	}
	if got := rig.row(t, fifth).SelfPn; got != "5730011122335" {
		t.Errorf("self_pn persistido = %q, se esperaba 5730011122335 (el dígito del dispositivo, concatenado)", got)
	}
}

// Un fallo del conteo se traga en debug: ni aviso, ni error, y el número ya quedó guardado.
func TestDeviceLimitSwallowsACountFailure(t *testing.T) {
	t.Parallel()
	rig := newHeartbeatRig()
	rig.fleet.fail["CountLiveBySelfPn"] = errors.New("base caída")

	last := rig.pair(t, "tenant-1", 5, "s", "573001112233")

	if rig.log.contains(deviceLimitWarning) || rig.log.contains("level=ERROR") {
		t.Errorf("un conteo fallido no avisa ni es un error: %q", rig.log.String())
	}
	rig.requireLog(t, "level=DEBUG", `msg="fleet: contar sesiones por self_pn para aviso de tope"`, "base caída", "session_id=s-5")
	if got := rig.row(t, last).SelfPn; got != "573001112233" {
		t.Errorf("el número debía quedar guardado aunque el conteo falle: %q", got)
	}
}
