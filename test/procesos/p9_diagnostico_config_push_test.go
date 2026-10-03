//go:build integracion

package procesos

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"testing"
)

// P9, segunda mitad: la configuración empujada (ADR-0021). El servidor empuja al Edge un ConfigUpdate
// por kind —«jwks», «intents», «filters»— al conectar y cada vez que la administradora cambia el
// catálogo de intenciones o el perfil de una sesión. Sale de p9_diagnostico_test.go por tamaño.

const (
	// p9IntentRow da la versión del catálogo guardado de una empresa (intent_configs.tenant_id es TEXT).
	p9IntentRow = `SELECT version FROM public.intent_configs WHERE tenant_id = $1`
	// p9IntentCount cuenta los catálogos guardados, de todas las empresas.
	p9IntentCount = `SELECT count(*)::text FROM public.intent_configs`
	// p9ProfileOf da el perfil de una sesión en la flota.
	p9ProfileOf = `SELECT profile FROM public.fleet_sessions WHERE tenant_id = $1::uuid AND session_id = $2`
)

// p9HashVersion es la forma de la versión de entidad del catálogo: 12 hex del sha256 del JSON
// normalizado (entityVersion, internal/publicapi/intents.go). No es la `version` del cuerpo.
var p9HashVersion = regexp.MustCompile(`^[0-9a-f]{12}$`)

// p9FiltersPayload es el cuerpo del kind «filters» (D-046.2): la foto de TODAS las sesiones de la
// empresa con su perfil, y la versión como entero.
type p9FiltersPayload struct {
	Version  int64 `json:"version"`
	Sessions map[string]struct {
		Profile string `json:"profile"`
	} `json:"sessions"`
}

// p9Catalog arma un catálogo de intenciones con una intención del nombre dado y un ejemplo. Va como
// json.RawMessage para que el cuerpo que sale sea exactamente estos bytes.
func p9Catalog(t *testing.T, name, example string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"version": "v1",
		"intents": []map[string]any{{
			"name":        name,
			"descripcion": "pedir comida",
			"ejemplos":    []map[string]string{{"mensaje": example}},
		}},
	})
	if err != nil {
		t.Fatalf("p9Catalog: %v", err)
	}
	return raw
}

// putIntents publica un catálogo como la administradora de la empresa del proceso, exige el 200 y
// devuelve la versión de entidad que contestó el servidor.
func (w *p9World) putIntents(t *testing.T, body json.RawMessage) string {
	t.Helper()
	r := w.call(t, w.main, p9ActIntents, http.MethodPut, p9PathIntents, body)
	if r.Codigo != http.StatusOK {
		t.Fatalf("publicar el catálogo: HTTP %d, quería 200\ncuerpo: %s", r.Codigo, recortar(r.Cuerpo))
	}
	var res struct {
		Version string `json:"version"`
	}
	r.JSON(t, &res)
	if !p9HashVersion.MatchString(res.Version) {
		t.Fatalf("publicar el catálogo: version %q, quería 12 hex", res.Version)
	}
	return res.Version
}

// p9JSONEqual dice si dos documentos JSON son el mismo valor (claves en cualquier orden).
func p9JSONEqual(t *testing.T, a, b []byte) bool {
	t.Helper()
	var va, vb any
	if err := json.Unmarshal(a, &va); err != nil {
		t.Fatalf("no es JSON: %s", recortar(a))
	}
	if err := json.Unmarshal(b, &vb); err != nil {
		t.Fatalf("no es JSON: %s", recortar(b))
	}
	na, errA := json.Marshal(va)
	nb, errB := json.Marshal(vb)
	return errA == nil && errB == nil && bytes.Equal(na, nb)
}

// p9Filters decodifica el payload de un ConfigUpdate «filters» y comprueba lo que el contrato fija:
// la versión del frame es el MISMO entero que va dentro, en decimal.
func p9Filters(t *testing.T, cfg configRecibida) p9FiltersPayload {
	t.Helper()
	var p p9FiltersPayload
	if err := json.Unmarshal(cfg.Payload, &p); err != nil {
		t.Fatalf("el payload de filters no es el JSON esperado: %v\n%s", err, recortar(cfg.Payload))
	}
	if cfg.Version != strconv.FormatInt(p.Version, 10) {
		t.Errorf("filters: la versión del frame es %q y la del payload %d: tienen que ser el mismo entero", cfg.Version, p.Version)
	}
	return p
}

// p9Profiles resume el mapa de sesiones de un payload de filters como session_id → perfil.
func p9Profiles(p p9FiltersPayload) map[string]string {
	out := make(map[string]string, len(p.Sessions))
	for id, s := range p.Sessions {
		out[id] = s.Profile
	}
	return out
}

// initialConfig: lo que cada Edge recibió al conectar, sin catálogo publicado: el jwks (con el que
// valida a su operador) y el mapa de filtros, en ese orden, dirigidos a su sesión. Ningún «intents».
func (w *p9World) initialConfig(t *testing.T) {
	for name, e := range map[string]*edge{"e": w.e, "e2": w.e2, "eOth": w.eOth} {
		cfgs := p9WaitConfigs(t, e, 0, "", 2)
		if kinds := p9Kinds(e.Configs()); fmt.Sprint(kinds) != "[jwks filters]" {
			t.Errorf("%s: al conectar recibió %v, quería [jwks filters]", name, kinds)
			continue
		}
		var jwks struct {
			Keys []json.RawMessage `json:"keys"`
		}
		if err := json.Unmarshal(cfgs[0].Payload, &jwks); err != nil || len(jwks.Keys) == 0 || cfgs[0].Version == "" {
			t.Errorf("%s: el jwks trae versión %q y %d claves (err %v)", name, cfgs[0].Version, len(jwks.Keys), err)
		}
		for _, c := range cfgs {
			if c.Sesion != e.SessionID || c.ComandoID == "" {
				t.Errorf("%s: ConfigUpdate %s con sesión %q y command_id %q", name, c.Kind, c.Sesion, c.ComandoID)
			}
		}
		// El mapa de filtros de la conexión trae la propia sesión, que nace pasiva, y solo sesiones
		// de su empresa.
		profiles := p9Profiles(p9Filters(t, cfgs[1]))
		if profiles[e.SessionID] != "passive" {
			t.Errorf("%s: el mapa de filtros inicial es %v, quería su sesión como passive", name, profiles)
		}
		for id := range profiles {
			if got := p9Scalar(t, w.esc.DB, p9ProfileOf, e.TenantID, id); got == "" {
				t.Errorf("%s: el mapa de filtros trae la sesión %q, que no es de su empresa", name, id)
			}
		}
	}
}

// intentsPush: publicar el catálogo lo guarda (una fila por empresa, con la versión por hash) y lo
// empuja TAL CUAL a todas las sesiones vivas de la empresa y a ninguna de otra. El mismo contenido
// con otro orden de claves da la misma versión; otro catálogo, otra, y la fila se reemplaza. Sin la
// feature llm_intent, 403.
func (w *p9World) intentsPush(t *testing.T) {
	if r := w.call(t, w.main, "", http.MethodGet, p9PathIntents, nil); r.Codigo != http.StatusNotFound {
		t.Errorf("leer el catálogo antes de publicarlo: HTTP %d, quería 404", r.Codigo)
	}
	from, from2 := len(w.e.Configs()), len(w.e2.Configs())
	catA, verA := w.intentsFirstPublish(t, from, from2)

	// El mismo contenido con las claves en otro orden: misma versión, y se vuelve a empujar (el
	// cuerpo que viaja es el nuevo, tal cual).
	catA2 := json.RawMessage(`{"version":"v1","intents":[{"name":"pedir_pizza","ejemplos":[{"mensaje":"quiero una pizza"}],"descripcion":"pedir comida"}]}`)
	if bytes.Equal(catA, catA2) {
		t.Fatalf("los dos cuerpos del mismo catálogo son iguales byte a byte: el caso no prueba nada")
	}
	if ver := w.putIntents(t, catA2); ver != verA {
		t.Errorf("el mismo catálogo con otro orden de claves dio la versión %q, quería %q", ver, verA)
	}
	if cfgs := p9WaitConfigs(t, w.e, from, "intents", 2); cfgs[1].Version != verA || !bytes.Equal(cfgs[1].Payload, catA2) {
		t.Errorf("el segundo push = versión %q payload %s", cfgs[1].Version, recortar(cfgs[1].Payload))
	}

	w.intentsReplace(t, verA, from, from2)
	w.intentsWithoutFeature(t, catA)
}

// intentsFirstPublish publica el primer catálogo y mira la fila, el push a las dos sesiones de la
// empresa y la lectura por la API. Devuelve el cuerpo publicado y su versión.
func (w *p9World) intentsFirstPublish(t *testing.T, from, from2 int) (json.RawMessage, string) {
	db := w.esc.DB
	catA := p9Catalog(t, "pedir_pizza", "quiero una pizza")
	verA := w.putIntents(t, catA)
	if got := p9Scalar(t, db, p9IntentRow, w.esc.Tenant); got != verA {
		t.Errorf("intent_configs.version = %q, quería %q", got, verA)
	}
	if got := p9Scalar(t, db, `SELECT (config = $2::jsonb)::text FROM public.intent_configs WHERE tenant_id = $1`, w.esc.Tenant, string(catA)); got != "true" {
		t.Errorf("intent_configs.config no es el catálogo publicado (igualdad jsonb = %q)", got)
	}
	for name, c := range map[string]struct {
		e    *edge
		from int
	}{"e": {w.e, from}, "e2": {w.e2, from2}} {
		cfg := p9WaitConfigs(t, c.e, c.from, "intents", 1)[0]
		if cfg.Version != verA || !bytes.Equal(cfg.Payload, catA) || cfg.Sesion != c.e.SessionID || cfg.ComandoID == "" {
			t.Errorf("%s: ConfigUpdate intents = versión %q sesión %q payload %s; quería la versión %q y el cuerpo publicado tal cual",
				name, cfg.Version, cfg.Sesion, recortar(cfg.Payload), verA)
		}
	}
	read := w.call(t, w.main, "", http.MethodGet, p9PathIntents, nil)
	var stored struct {
		Version string          `json:"version"`
		Config  json.RawMessage `json:"config"`
	}
	read.JSON(t, &stored)
	if read.Codigo != http.StatusOK || stored.Version != verA || !p9JSONEqual(t, stored.Config, catA) {
		t.Errorf("leer el catálogo: HTTP %d versión %q config %s", read.Codigo, stored.Version, recortar(stored.Config))
	}
	return catA, verA
}

// intentsReplace publica OTRO catálogo: otra versión que verA, la fila se reemplaza y el tercer
// push llega a las dos sesiones de la empresa.
func (w *p9World) intentsReplace(t *testing.T, verA string, from, from2 int) {
	db := w.esc.DB
	// Otro catálogo: otra versión, y la fila se reemplaza (sigue habiendo una).
	catB := p9Catalog(t, "pedir_empanadas", "me das dos empanadas")
	verB := w.putIntents(t, catB)
	if verB == verA {
		t.Errorf("dos catálogos distintos dieron la misma versión %q", verB)
	}
	if got := p9Scalar(t, db, p9IntentRow, w.esc.Tenant); got != verB {
		t.Errorf("tras el segundo catálogo intent_configs.version = %q, quería %q", got, verB)
	}
	if got := p9Scalar(t, db, p9IntentCount); got != "1" {
		t.Errorf("intent_configs tiene %s filas, quería 1 (upsert por empresa)", got)
	}
	for name, c := range map[string]struct {
		e    *edge
		from int
	}{"e": {w.e, from}, "e2": {w.e2, from2}} {
		cfgs := p9WaitConfigs(t, c.e, c.from, "intents", 3)
		if len(cfgs) != 3 || cfgs[2].Version != verB || !bytes.Equal(cfgs[2].Payload, catB) {
			t.Errorf("%s: tras tres PUT tiene %d ConfigUpdate intents; el último, versión %q", name, len(cfgs), cfgs[len(cfgs)-1].Version)
		}
	}
}

// intentsWithoutFeature es la empresa sin llm_intent, y la llamada sin token: ni publican ni leen,
// no dejan fila y el Edge de la otra empresa no recibe ningún «intents».
func (w *p9World) intentsWithoutFeature(t *testing.T, catA json.RawMessage) {
	db := w.esc.DB
	// La empresa sin llm_intent: 403, sin fila, y su Edge no recibe ningún «intents» (ni el suyo ni
	// el de la empresa del proceso).
	r := w.call(t, w.other, p9ActIntents, http.MethodPut, p9PathIntents, catA)
	if !p9ErrorIs(r, http.StatusForbidden, "el plan del tenant no incluye la clasificación de intenciones") {
		t.Errorf("publicar el catálogo sin llm_intent: HTTP %d %s, quería 403", r.Codigo, recortar(r.Cuerpo))
	}
	if got := p9Scalar(t, db, p9IntentCount); got != "1" {
		t.Errorf("tras el 403 intent_configs tiene %s filas, quería 1", got)
	}
	if r := w.call(t, w.other, "", http.MethodGet, p9PathIntents, nil); r.Codigo != http.StatusNotFound {
		t.Errorf("leer el catálogo de la otra empresa: HTTP %d, quería 404", r.Codigo)
	}
	if r := w.call(t, p9Caller{client: w.esc.S.Publica("")}, "", http.MethodPut, p9PathIntents, catA); r.Codigo != http.StatusUnauthorized {
		t.Errorf("publicar el catálogo sin token: HTTP %d, quería 401", r.Codigo)
	}
	if cfgs := p9ConfigsSince(w.eOth, 0, "intents"); len(cfgs) != 0 {
		t.Errorf("el Edge de la otra empresa recibió %d ConfigUpdate intents", len(cfgs))
	}
}

// intentsAdversarial es la tabla de casos adversarios del catálogo: el nombre de una intención tiene
// que cumplir ^[a-z][a-z0-9_]{1,63}$ (wapp-shared/intents). Lo que no lo cumple se rechaza con 400,
// no toca la fila y no se empuja; lo que sí —un separador repetido lo cumple— entra.
func (w *p9World) intentsAdversarial(t *testing.T) {
	db := w.esc.DB
	const rowState = `SELECT version || '|' || updated_at::text FROM public.intent_configs WHERE tenant_id = $1`
	before := p9Scalar(t, db, rowState, w.esc.Tenant)
	from := len(w.e.Configs())

	for name, bad := range map[string]string{
		"separador repetido @@":   "a@@b",
		"dígitos árabe-índicos":   "١٢٣",
		"dígitos no ASCII tras":   "pedir١٢٣",
		"espacio U+00A0":          "pedir pizza",
		"espacio U+2003":          "pedir pizza",
		"espacio U+00A0 al final": "pedir_pizza ",
		"guion":                   "pedir-pizza",
		"mayúscula":               "Pedir_pizza",
		"una sola letra":          "p",
	} {
		r := w.call(t, w.main, p9ActIntents, http.MethodPut, p9PathIntents, p9Catalog(t, bad, "quiero una pizza"))
		if !p9ErrorIs(r, http.StatusBadRequest, "config de intents inválida") {
			t.Errorf("nombre adversario (%s) %q: HTTP %d %s, quería 400", name, bad, r.Codigo, recortar(r.Cuerpo))
		}
	}
	// Otros cuerpos que el contrato rechaza.
	for name, body := range map[string]json.RawMessage{
		"sin intents":         json.RawMessage(`{"version":"v1","intents":[]}`),
		"sin version":         json.RawMessage(`{"version":"","intents":[{"name":"pedir_pizza","descripcion":"d","ejemplos":[{"mensaje":"m"}]}]}`),
		"sin ejemplos":        json.RawMessage(`{"version":"v1","intents":[{"name":"pedir_pizza","descripcion":"d","ejemplos":[]}]}`),
		"nombre duplicado":    json.RawMessage(`{"version":"v1","intents":[{"name":"pedir_pizza","descripcion":"d","ejemplos":[{"mensaje":"m"}]},{"name":"pedir_pizza","descripcion":"d","ejemplos":[{"mensaje":"m"}]}]}`),
		"no es un objeto":     json.RawMessage(`"pedir_pizza"`),
		"ejemplo en blanco":   json.RawMessage(`{"version":"v1","intents":[{"name":"pedir_pizza","descripcion":"d","ejemplos":[{"mensaje":""}]}]}`),
		"sin descripción":     json.RawMessage(`{"version":"v1","intents":[{"name":"pedir_pizza","descripcion":"","ejemplos":[{"mensaje":"m"}]}]}`),
		"umbral fuera":        json.RawMessage(`{"version":"v1","umbral_confianza":2,"intents":[{"name":"pedir_pizza","descripcion":"d","ejemplos":[{"mensaje":"m"}]}]}`),
		"intents no es lista": json.RawMessage(`{"version":"v1","intents":"pedir_pizza"}`),
	} {
		if r := w.call(t, w.main, p9ActIntents, http.MethodPut, p9PathIntents, body); r.Codigo != http.StatusBadRequest {
			t.Errorf("catálogo inválido (%s): HTTP %d %s, quería 400", name, r.Codigo, recortar(r.Cuerpo))
		}
	}
	if got := p9Scalar(t, db, rowState, w.esc.Tenant); got != before {
		t.Errorf("los catálogos rechazados tocaron intent_configs: de %q a %q", before, got)
	}

	// El centinela: un catálogo válido cuyo nombre lleva el separador repetido y cuyo ejemplo —texto
	// libre— lleva los tres adversarios. Es el ÚNICO ConfigUpdate intents que el Edge recibe desde
	// que empezó la tabla: el stream es ordenado, así que ninguno de los rechazados se empujó.
	sentinel := p9Catalog(t, "pedir__pizza", "quiero una pizza a@@b ١٢٣ ya")
	ver := w.putIntents(t, sentinel)
	edgeEsperar(t, edgeTopeFila, "el ConfigUpdate del catálogo centinela", func() bool {
		cfgs := p9ConfigsSince(w.e, from, "intents")
		return len(cfgs) > 0 && cfgs[len(cfgs)-1].Version == ver
	})
	cfgs := p9ConfigsSince(w.e, from, "intents")
	if len(cfgs) != 1 || !bytes.Equal(cfgs[0].Payload, sentinel) {
		t.Errorf("desde la tabla de adversarios el Edge recibió %d ConfigUpdate intents, quería 1 (el centinela, tal cual)", len(cfgs))
	}
	if got := p9Scalar(t, db, p9IntentRow, w.esc.Tenant); got != ver {
		t.Errorf("tras el centinela intent_configs.version = %q, quería %q", got, ver)
	}
}

// setProfile cambia el perfil de la sesión del Edge target como la administradora, exige el 200 y
// espera en e y en e2 el ConfigUpdate «filters» que provoca. Devuelve el payload que recibió e.
func (w *p9World) setProfile(t *testing.T, target *edge, profile, wantStored string) p9FiltersPayload {
	t.Helper()
	from, from2 := len(w.e.Configs()), len(w.e2.Configs())
	r := w.call(t, w.main, p9ActProfile, http.MethodPost, p9ProfilePath(target.SessionID), map[string]string{"profile": profile})
	var res struct {
		SessionID string `json:"session_id"`
		Profile   string `json:"profile"`
	}
	if r.Codigo != http.StatusOK {
		t.Fatalf("fijar el perfil %q de %s: HTTP %d, quería 200\ncuerpo: %s", profile, target.SessionID, r.Codigo, recortar(r.Cuerpo))
	}
	r.JSON(t, &res)
	if res.SessionID != target.SessionID || res.Profile != wantStored {
		t.Errorf("fijar el perfil %q: respuesta %+v, quería %s/%s", profile, res, target.SessionID, wantStored)
	}
	if got := p9Scalar(t, w.esc.DB, p9ProfileOf, w.esc.Tenant, target.SessionID); got != wantStored {
		t.Errorf("fleet_sessions.profile = %q, quería %q", got, wantStored)
	}
	got := p9WaitConfigs(t, w.e, from, "filters", 1)
	got2 := p9WaitConfigs(t, w.e2, from2, "filters", 1)
	if !bytes.Equal(got[0].Payload, got2[0].Payload) || got[0].Version != got2[0].Version {
		t.Errorf("las dos sesiones de la empresa recibieron filtros distintos: %s y %s", recortar(got[0].Payload), recortar(got2[0].Payload))
	}
	if got[0].Sesion != w.e.SessionID || got2[0].Sesion != w.e2.SessionID {
		t.Errorf("los ConfigUpdate filters van a las sesiones %q y %q", got[0].Sesion, got2[0].Sesion)
	}
	return p9Filters(t, got[0])
}

// filtersPush: cambiar el perfil de UNA sesión empuja a todas las sesiones vivas de la empresa la
// foto ENTERA (todas las sesiones, activas incluidas), con una versión que crece con cada cambio, y a
// ninguna sesión de otra empresa. Un mapa todo-activo también se empuja.
func (w *p9World) filtersPush(t *testing.T) {
	fromOth := len(w.eOth.Configs())
	e, e2 := w.e.SessionID, w.e2.SessionID

	p1 := w.setProfile(t, w.e, "active", "active")
	if got, want := fmt.Sprint(p9Profiles(p1)), fmt.Sprint(map[string]string{e: "active", e2: "passive"}); got != want {
		t.Errorf("tras activar la primera sesión el mapa es %s, quería %s", got, want)
	}
	p2 := w.setProfile(t, w.e2, "active", "active")
	if got, want := fmt.Sprint(p9Profiles(p2)), fmt.Sprint(map[string]string{e: "active", e2: "active"}); got != want {
		t.Errorf("con las dos sesiones activas el mapa es %s, quería %s (un mapa todo-activo también viaja)", got, want)
	}
	p3 := w.setProfile(t, w.e, "passive", "passive")
	if got, want := fmt.Sprint(p9Profiles(p3)), fmt.Sprint(map[string]string{e: "passive", e2: "active"}); got != want {
		t.Errorf("tras volver a pasiva la primera sesión el mapa es %s, quería %s", got, want)
	}
	if p2.Version <= p1.Version || p3.Version <= p2.Version {
		t.Errorf("las versiones de filters no crecen: %d, %d, %d (estrictamente mayor, no «distinta»)", p1.Version, p2.Version, p3.Version)
	}
	if cfgs := p9ConfigsSince(w.eOth, fromOth, ""); len(cfgs) != 0 {
		t.Errorf("el Edge de la otra empresa recibió %v por los cambios de perfil de la empresa del proceso", p9Kinds(cfgs))
	}
}

// filtersAdversarial es la tabla de casos adversarios del perfil: el valor solo puede ser active o
// passive (se le quitan los espacios de los extremos, los Unicode también), y la sesión de la ruta
// se compara byte a byte y dentro de la empresa del token.
func (w *p9World) filtersAdversarial(t *testing.T) {
	db := w.esc.DB
	sid := w.e.SessionID
	const state = `SELECT string_agg(session_id || '=' || profile || '@' || profile_updated_at::text, ',' ORDER BY session_id) FROM public.fleet_sessions`
	before := p9Scalar(t, db, state)
	from := len(w.e.Configs())

	for name, bad := range map[string]string{
		"separador repetido":     "a@@b",
		"dígitos árabe-índicos":  "١٢٣",
		"espacio Unicode dentro": "act ive",
		"mayúsculas":             "ACTIVE",
		"vacío":                  "",
		"solo espacios Unicode":  "  ",
		"el rol viejo":           "primary",
	} {
		r := w.call(t, w.main, p9ActProfile, http.MethodPost, p9ProfilePath(sid), map[string]string{"profile": bad})
		if !p9ErrorIs(r, http.StatusBadRequest, "profile inválido (usar active|passive)") {
			t.Errorf("perfil adversario (%s) %q: HTTP %d %s, quería 400", name, bad, r.Codigo, recortar(r.Cuerpo))
		}
	}
	for name, bad := range map[string]string{
		"separador repetido":     "a@@b",
		"dígitos árabe-índicos":  p9ArabicDigits(sid),
		"espacio U+00A0 detrás":  sid + " ",
		"espacio U+2003 delante": " " + sid,
		"sesión de otra empresa": w.eOth.SessionID,
	} {
		r := w.call(t, w.main, p9ActProfile, http.MethodPost, p9ProfilePath(bad), map[string]string{"profile": "active"})
		if !p9ErrorIs(r, http.StatusNotFound, "sesión no encontrada") {
			t.Errorf("sesión adversaria (%s) %q: HTTP %d %s, quería 404", name, bad, r.Codigo, recortar(r.Cuerpo))
		}
	}
	if r := w.call(t, w.other, p9ActProfile, http.MethodPost, p9ProfilePath(sid), map[string]string{"profile": "active"}); r.Codigo != http.StatusNotFound {
		t.Errorf("la otra empresa cambia el perfil de una sesión ajena: HTTP %d, quería 404", r.Codigo)
	}
	if got := p9Scalar(t, db, state); got != before {
		t.Errorf("los cambios de perfil rechazados tocaron fleet_sessions:\nantes   %s\ndespués %s", before, got)
	}

	// El centinela: el perfil con espacios Unicode en los extremos SÍ entra (strings.TrimSpace) y se
	// guarda limpio. Es el único «filters» que el Edge recibe desde que empezó la tabla.
	p := w.setProfile(t, w.e, " active ", "active")
	if got := p9Profiles(p)[sid]; got != "active" {
		t.Errorf("el perfil con espacios Unicode en los extremos viajó como %q, quería active", got)
	}
	if cfgs := p9ConfigsSince(w.e, from, "filters"); len(cfgs) != 1 {
		t.Errorf("desde la tabla de adversarios el Edge recibió %d ConfigUpdate filters, quería 1 (el centinela)", len(cfgs))
	}
}

// reconnect: al reconectar, el Edge vuelve a recibir TODA su configuración vigente, un frame por
// kind: con llm_intent y catálogo publicado, jwks + intents + filters; sin la feature, jwks +
// filters (los filtros no se gatean por plan). Antes se espera a que los calentamientos pedidos por
// los PUT del catálogo hayan terminado, para no cortar ninguno a medias.
func (w *p9World) reconnect(t *testing.T) {
	db, s := w.esc.DB, w.esc.S
	edgeEsperar(t, edgeTopeFila, "que terminen los calentamientos del Edge", func() bool {
		n := len(w.e.Inferencias())
		done := len(edgeWarmupLogLines(s, w.e, edgeLogWarmupServed)) + len(edgeWarmupLogLines(s, w.e, edgeLogWarmupNotServed))
		return n > 0 && done >= n
	})
	if lines := edgeWarmupLogLines(s, w.e, edgeLogWarmupNotServed); len(lines) != 0 {
		t.Errorf("con lease vigente el servidor registró calentamientos no servidos: %v", lines)
	}
	version := p9Scalar(t, db, p9IntentRow, w.esc.Tenant)
	stored := p9Scalar(t, db, `SELECT config::text FROM public.intent_configs WHERE tenant_id = $1`, w.esc.Tenant)

	w.e.desconectar(t)
	edgeEsperarValor(t, db, "offline", "la sesión tras desconectar", edgeEstadoSesion, w.esc.Tenant, w.e.EdgeID, w.e.SessionID)
	from := len(w.e.Configs())
	w.reconnectEdge(t, w.e)
	cfgs := p9WaitConfigs(t, w.e, from, "", 3)
	if kinds := p9Kinds(p9ConfigsSince(w.e, from, "")); fmt.Sprint(kinds) != "[jwks intents filters]" {
		t.Fatalf("al reconectar el Edge recibió %v, quería [jwks intents filters]", kinds)
	}
	// El catálogo de la reconexión sale de la base: la misma versión y el mismo valor JSON (no los
	// mismos bytes: jsonb no guarda el texto).
	if cfgs[1].Version != version || !p9JSONEqual(t, cfgs[1].Payload, []byte(stored)) {
		t.Errorf("al reconectar, intents = versión %q payload %s; quería la versión %q y el catálogo guardado", cfgs[1].Version, recortar(cfgs[1].Payload), version)
	}
	want := fmt.Sprint(map[string]string{w.e.SessionID: "active", w.e2.SessionID: "active"})
	if got := fmt.Sprint(p9Profiles(p9Filters(t, cfgs[2]))); got != want {
		t.Errorf("al reconectar, filters = %s, quería %s", got, want)
	}
	for _, c := range cfgs {
		if c.Sesion != w.e.SessionID || c.ComandoID == "" {
			t.Errorf("al reconectar, ConfigUpdate %s con sesión %q y command_id %q", c.Kind, c.Sesion, c.ComandoID)
		}
	}

	// La empresa sin llm_intent: dos kinds, y filters es uno de ellos.
	w.eOth.desconectar(t)
	edgeEsperarValor(t, db, "offline", "la sesión de la otra empresa tras desconectar", edgeEstadoSesion, w.other.tenant, w.eOth.EdgeID, w.eOth.SessionID)
	fromOth := len(w.eOth.Configs())
	w.reconnectEdge(t, w.eOth)
	p9WaitConfigs(t, w.eOth, fromOth, "filters", 1)
	if kinds := p9Kinds(p9ConfigsSince(w.eOth, fromOth, "")); fmt.Sprint(kinds) != "[jwks filters]" {
		t.Errorf("al reconectar sin llm_intent el Edge recibió %v, quería [jwks filters]", kinds)
	}

	// El Edge reconectado sigue siendo diagnosticable y sigue recibiendo los cambios en caliente.
	back := w.requestDiag(t, w.main, w.e, map[string]string{"scope": "logs"}, "logs")
	w.e.bundle(t, back.CommandID, "tras reconectar")
	edgeEsperarValor(t, db, "ready|true|tras reconectar", "el bundle del Edge reconectado", p9DiagState, back.CommandID)
	// El calentamiento que el servidor pide al reconectar (ya hay catálogo) también tiene que acabar
	// antes del cierre.
	edgeEsperar(t, edgeTopeFila, "que termine el calentamiento de la reconexión", func() bool {
		n := len(w.e.Inferencias())
		done := len(edgeWarmupLogLines(s, w.e, edgeLogWarmupServed)) + len(edgeWarmupLogLines(s, w.e, edgeLogWarmupNotServed))
		return done >= n
	})
}
