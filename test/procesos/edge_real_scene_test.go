//go:build integracion && edgereal

package procesos

// La escena de un caso con Edge real: el servidor, la empresa, la persona que la administra y opera
// el Edge, el Edge real enrolado y su sesión de WhatsApp falso emparejada. Y las dos direcciones de
// la conversación: lo que entra se inyecta por la puerta del ADR-0041 y lo que el Edge «envía» se
// lee de su registro de capturados (ADR-0049).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

const (
	// edgeRealDegraded es el código con el que el plano de control rechaza una escritura mientras
	// el Edge no tiene el JWKS de la nube: pasa nada más arrancar y se va solo.
	edgeRealDegraded = "degraded_read_only"
	// edgeRealRelayOffline es el código del login cuando el stream de CloudLink aún no está vivo.
	edgeRealRelayOffline = "relay_offline"
	// edgeRealSyntheticPrefix es la marca de lo sintético (ADR-0041): la llevan el wa_message_id de
	// cada entrante inyectado, que sube a la nube, y el id de cada mensaje «enviado».
	edgeRealSyntheticPrefix = "SINTETICO-"
	// edgeRealSelfPrefix es el arranque del número propio de una sesión de WhatsApp falso.
	edgeRealSelfPrefix = "0000"

	edgeRealLinkTimeout = 60 * time.Second
)

// edgeRealCaptured es un envío capturado por el WhatsApp falso, tal como lo devuelve
// GET /v1/diag/outbound/captured.
type edgeRealCaptured struct {
	SessionID string `json:"session_id"`
	CommandID string `json:"command_id"`
	MessageID string `json:"message_id"`
	To        string `json:"to"`
	Text      string `json:"text"`
}

// edgeRealScene es la escena. read es cuántos capturados lleva leídos el caso: el ORDEN de los
// textos es parte de lo que se afirma, igual que con el Edge falso.
type edgeRealScene struct {
	edgeEscenario
	Edge      *edgeReal
	Pub       *clienteHTTP
	Operator  string // el token de operador del plano de control del Edge
	SessionID string
	SelfPn    string // el número propio de la sesión falsa

	read *int
}

// edgeRealNewScene monta la escena: servidor con login de operador, empresa, una administradora que
// además opera el Edge, el Edge real enrolado y conectado, y una sesión emparejada que el servidor
// ya conoce (nace pasiva). Falla (t.Fatalf) si algo no sale.
func edgeRealNewScene(t *testing.T, proceso, slug string) edgeRealScene {
	t.Helper()
	s := arrancar(t, opcionesServidor{Proceso: proceso, IdentityLogin: true})
	db := s.Base.Abrir(t)
	staff := uuidAleatorio(t)
	altaStaffPlataforma(t, db, staff)
	tokenStaff := canjear(t, s, s.Identidad.TokenDe(staff, "wapp.bff"))
	esc := edgeEscenario{S: s, DB: db, TokenStaff: tokenStaff, Tenant: crearTenant(t, s, tokenStaff, slug)}

	admin := uuidAleatorio(t)
	edgeAltaAdminDelTenant(t, db, admin, esc.Tenant)
	esc.TokenAdmin = canjear(t, s, s.Identidad.TokenDe(admin, "wapp.bff"))
	email, password := "op-"+edgeAleatorioHex(t, 4)+"@procesos.test", "pw-"+edgeAleatorioHex(t, 16)
	s.Identidad.registerOperator(t, email, password, admin)

	sc := edgeRealScene{edgeEscenario: esc, Pub: s.Publica(esc.TokenAdmin), read: new(int)}
	sc.Edge = edgeRealStart(t, s, edgeEmitirCodigo(t, s, tokenStaff, esc.Tenant))
	sc.Operator = sc.login(t, email, password)
	sc.SessionID, sc.SelfPn = sc.pair(t)
	edgeEsperarValor(t, sc.DB, "passive", "el perfil de la sesión recién emparejada", p3ProfileQuery, sc.Tenant, sc.Edge.EdgeID, sc.SessionID)
	// El servidor empuja el mapa de perfiles al registrar la sesión. Hasta que el Edge lo aplica
	// filtra sin mapa (versión 0, todas activas): setProfile compara contra esta versión.
	edgeEsperar(t, edgeTopeFila, "que el Edge real aplique el mapa de perfiles con la sesión pasiva", func() bool {
		version, _ := sc.health(t)
		return version > 0
	})
	return sc
}

// edgeRealError lee el código del sobre de error del plano de control ({"error":{"code":…}}).
// Devuelve «» si el cuerpo no es ese sobre.
func edgeRealError(body []byte) string {
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return ""
	}
	return env.Error.Code
}

// login entra como operador por el plano de control del Edge, que relaya las credenciales a la nube
// por el stream. Reintenta mientras el stream no esté vivo. Devuelve el token de acceso.
func (sc edgeRealScene) login(t *testing.T, email, password string) string {
	t.Helper()
	var token string
	edgeEsperar(t, edgeRealLinkTimeout, "que el Edge real relaye el login del operador", func() bool {
		code, body, err := sc.Edge.call(t.Context(), "POST", "/v1/auth/login", "", map[string]string{"email": email, "password": password})
		if err != nil {
			t.Fatalf("login de operador: %v", err)
		}
		if code == http.StatusServiceUnavailable && edgeRealError(body) == edgeRealRelayOffline {
			return false
		}
		var res struct {
			AccessToken string `json:"access_token"`
		}
		if code != http.StatusOK || json.Unmarshal(body, &res) != nil || res.AccessToken == "" {
			t.Fatalf("login de operador: HTTP %d, quería 200 con un token\ncuerpo: %s", code, recortar(body))
		}
		token = res.AccessToken
		return true
	})
	return token
}

// pair empareja una sesión por el plano de control: el emparejamiento de siempre, que con el
// WhatsApp falso contesta «conectado» sin QR. Devuelve el session_id y el número propio. Reintenta
// mientras el Edge no tenga el JWKS de la nube (hasta entonces solo deja leer).
func (sc edgeRealScene) pair(t *testing.T) (sessionID, selfPn string) {
	t.Helper()
	var pairing struct {
		ID string `json:"id"`
	}
	edgeEsperar(t, edgeRealLinkTimeout, "que el Edge real acepte el emparejamiento", func() bool {
		code, body, err := sc.Edge.call(t.Context(), "POST", "/v1/sessions/pair", sc.Operator, nil)
		if err != nil {
			t.Fatalf("emparejar: %v", err)
		}
		if code == http.StatusForbidden && edgeRealError(body) == edgeRealDegraded {
			return false
		}
		if code != http.StatusOK || json.Unmarshal(body, &pairing) != nil || pairing.ID == "" {
			t.Fatalf("emparejar: HTTP %d, quería 200 con el id del emparejamiento\ncuerpo: %s", code, recortar(body))
		}
		return true
	})
	var done struct {
		Status    string `json:"status"`
		SessionID string `json:"session_id"`
		JID       string `json:"jid"`
		Error     string `json:"error"`
	}
	edgeEsperar(t, edgeRealLinkTimeout, "que el emparejamiento termine", func() bool {
		code, body, err := sc.Edge.call(t.Context(), "GET", "/v1/sessions/"+pairing.ID+"/pair", sc.Operator, nil)
		if err != nil || code != http.StatusOK || json.Unmarshal(body, &done) != nil {
			t.Fatalf("leer el emparejamiento: HTTP %d, %v\ncuerpo: %s", code, err, recortar(body))
		}
		if done.Status == "error" {
			t.Fatalf("el emparejamiento falló: %s", done.Error)
		}
		return done.Status == "success"
	})
	pn, server, ok := strings.Cut(done.JID, "@")
	if done.SessionID == "" || !ok || server != "s.whatsapp.net" || !strings.HasPrefix(pn, edgeRealSelfPrefix) {
		t.Fatalf("el emparejamiento terminó con la sesión %q y el JID %q; quería un session_id y un número falso (%s…)", done.SessionID, done.JID, edgeRealSelfPrefix)
	}
	return done.SessionID, pn
}

// health lee la vista de salud de la sesión en el Edge.
func (sc edgeRealScene) health(t *testing.T) (filtersVersion int64, droppedPassive uint64) {
	t.Helper()
	code, body, err := sc.Edge.call(t.Context(), "GET", "/v1/health", "", nil)
	var res struct {
		Sessions map[string]struct {
			FiltersVersion int64  `json:"filters_version"`
			DroppedPassive uint64 `json:"dropped_passive"`
		} `json:"sessions"`
	}
	if err != nil || code != http.StatusOK || json.Unmarshal(body, &res) != nil {
		t.Fatalf("leer la salud del Edge real: HTTP %d, %v\ncuerpo: %s", code, err, recortar(body))
	}
	return res.Sessions[sc.SessionID].FiltersVersion, res.Sessions[sc.SessionID].DroppedPassive
}

// setProfile cambia el perfil de la sesión por la API pública y espera las dos caras que se ven
// desde fuera: la fila de fleet_sessions y que el Edge esté filtrando con un mapa MÁS NUEVO que el
// que tenía (la versión del mapa de perfiles de su salud).
func (sc edgeRealScene) setProfile(t *testing.T, profile string) {
	t.Helper()
	before, _ := sc.health(t)
	r := sc.Pub.Post(t, "/api/v1/sessions/"+sc.SessionID+"/profile", map[string]string{"profile": profile})
	if r.Codigo != http.StatusOK {
		t.Fatalf("perfil %s: HTTP %d, quería 200\ncuerpo: %s", profile, r.Codigo, recortar(r.Cuerpo))
	}
	edgeEsperarValor(t, sc.DB, profile, "el perfil en fleet_sessions", p3ProfileQuery, sc.Tenant, sc.Edge.EdgeID, sc.SessionID)
	edgeEsperar(t, edgeTopeFila, "que el Edge real filtre con el mapa de perfiles nuevo", func() bool {
		now, _ := sc.health(t)
		return now > before
	})
}

// waID es el wa_message_id con el que sube a la nube el entrante del lote dado: lo fabrica el
// inyector del Edge, con la marca de lo sintético delante.
func edgeRealWaID(batch string) string { return edgeRealSyntheticPrefix + batch + "-0000" }

// say hace entrar un mensaje de from con ese texto por la puerta del inyector, y exige que el Edge
// lo haya aceptado y acusado. batch tiene que ser único en la escena: el Edge descarta en silencio
// un wa_message_id repetido. Reintenta el 409 de «sesión sin escucha publicada», que es transitorio.
func (sc edgeRealScene) say(t *testing.T, from, batch, text string) {
	t.Helper()
	req := map[string]any{"session_id": sc.SessionID, "texto": text, "chat_jid": from, "lote": batch}
	edgeEsperar(t, edgeTopeFila, "que el Edge real acepte el entrante "+batch, func() bool {
		code, body, err := sc.Edge.call(t.Context(), "POST", "/v1/diag/inbound/inject", sc.Operator, req)
		if err != nil {
			t.Fatalf("inyectar %s: %v", batch, err)
		}
		if code == http.StatusConflict {
			return false
		}
		var res struct {
			Inyectados int    `json:"inyectados"`
			Acusados   int    `json:"acusados"`
			Errores    int    `json:"errores"`
			Primer     string `json:"primer_error"`
		}
		if code != http.StatusOK || json.Unmarshal(body, &res) != nil || res.Inyectados != 1 || res.Acusados != 1 || res.Errores != 0 {
			t.Fatalf("inyectar %s: HTTP %d %+v, quería 200 con un entrante inyectado y acusado\ncuerpo: %s", batch, code, res, recortar(body))
		}
		return true
	})
}

// captured devuelve todo lo que el WhatsApp falso lleva capturado de la sesión, en orden.
func (sc edgeRealScene) captured(ctx context.Context) ([]edgeRealCaptured, error) {
	code, body, err := sc.Edge.call(ctx, "GET", "/v1/diag/outbound/captured?session_id="+sc.SessionID, sc.Operator, nil)
	if err != nil {
		return nil, err
	}
	var res struct {
		Captured  []edgeRealCaptured `json:"capturados"`
		Discarded int                `json:"descartados"`
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("leer los capturados: HTTP %d\ncuerpo: %s", code, recortar(body))
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("leer los capturados: %w\ncuerpo: %s", err, recortar(body))
	}
	if res.Discarded != 0 {
		return nil, fmt.Errorf("el registro de capturados descartó %d envíos: el caso ya no puede afirmar el orden", res.Discarded)
	}
	return res.Captured, nil
}

// expectText espera el siguiente envío capturado y exige que vaya a to con ese texto exacto, por
// esta sesión, con su command_id y con la marca de lo sintético en el id del mensaje. Falla
// (t.Fatalf) si no llega en p3TextTimeout o si es otro.
func (sc edgeRealScene) expectText(t *testing.T, to, text string) {
	t.Helper()
	var got edgeRealCaptured
	edgeEsperar(t, p3TextTimeout, fmt.Sprintf("que el Edge real capture un envío a %s", to), func() bool {
		all, err := sc.captured(t.Context())
		if err != nil {
			t.Fatalf("%v", err)
		}
		if len(all) <= *sc.read {
			return false
		}
		got = all[*sc.read]
		return true
	})
	*sc.read++
	if got.To != to || got.Text != text {
		t.Fatalf("el Edge real capturó un envío a %q con %q; quería a %q con %q", got.To, got.Text, to, text)
	}
	if got.SessionID != sc.SessionID || got.CommandID == "" || !strings.HasPrefix(got.MessageID, edgeRealSyntheticPrefix) {
		t.Errorf("el envío capturado no trae la sesión, el command_id o la marca de lo sintético: %+v", got)
	}
}

// expectNoPendingText comprueba que el Edge no capturó ningún envío que el caso no haya leído. No
// espera: quien la llama ya comprobó, sondeando un efecto del servidor, que el entrante terminó de
// procesarse. Falla con t.Errorf.
func (sc edgeRealScene) expectNoPendingText(t *testing.T, when string) {
	t.Helper()
	all, err := sc.captured(t.Context())
	if err != nil {
		t.Fatalf("%v", err)
	}
	for _, c := range all[min(*sc.read, len(all)):] {
		t.Errorf("%s: el Edge real capturó un envío que no debía salir: a %q, %q", when, c.To, c.Text)
	}
}
