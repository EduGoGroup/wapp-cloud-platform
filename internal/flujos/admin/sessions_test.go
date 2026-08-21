package admin_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/admin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// viasSesion son las DOS vías en las que el MISMO handler está colgado en producción
// (Plan 046 · T1.2): la API pública y la de administración. Los contract tests se
// recorren SOBRE LAS DOS a propósito — la de admin es la que se olvida, y una
// deprecación que solo avisa por una vía no avisa a quien usa la otra.
var viasSesion = map[string]string{
	"api":   "/api/v1/sessions",
	"admin": "/admin/sessions",
}

// pusherEspia es un ProfilePusher de prueba: cuenta las llamadas, guarda lo último
// que le pidieron y puede fallar a voluntad (para probar el contrato best-effort).
type pusherEspia struct {
	llamadas int
	tenant   string
	sesion   string
	perfil   string
	err      error
}

func (p *pusherEspia) PushProfile(_ context.Context, tenantID, sessionID string, profile fleet.Profile) error {
	p.llamadas++
	p.tenant, p.sesion, p.perfil = tenantID, sessionID, string(profile)
	return p.err
}

// doSessionRoleEn ejecuta el handler de rol sobre la vía indicada, con un mux con el
// patrón real (para que r.PathValue("id") funcione) y una Identity del tenant
// inyectada en el contexto como haría Authenticate. withID=false ejercita el 401.
func doSessionRoleEn(via string, store admin.SessionRoleStore, pusher admin.ProfilePusher,
	tenant, sessionID, body string, withID bool,
) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	mux.Handle("POST "+via+"/{id}/role", admin.SetSessionRoleHandler(store, pusher, nil))
	req := httptest.NewRequest(http.MethodPost, via+"/"+sessionID+"/role", strings.NewReader(body))
	if withID {
		req = req.WithContext(httpapi.WithIdentity(req.Context(), httpapi.Identity{TenantID: tenant, Subject: "user-1"}))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// doSessionRole conserva la firma que usaban los tests del Plan 020: la vía admin,
// sin pusher.
func doSessionRole(store admin.SessionRoleStore, tenant, sessionID, body string, withID bool) *httptest.ResponseRecorder {
	return doSessionRoleEn(viasSesion["admin"], store, nil, tenant, sessionID, body, withID)
}

// doSessionProfileEn ejecuta el handler de PERFIL sobre la vía indicada, con el mismo
// método que doSessionRoleEn.
func doSessionProfileEn(via string, store admin.SessionProfileStore, pusher admin.ProfilePusher,
	tenant, sessionID, body string, withID bool,
) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	mux.Handle("POST "+via+"/{id}/profile", admin.SetSessionProfileHandler(store, pusher, nil))
	req := httptest.NewRequest(http.MethodPost, via+"/"+sessionID+"/profile", strings.NewReader(body))
	if withID {
		req = req.WithContext(httpapi.WithIdentity(req.Context(), httpapi.Identity{TenantID: tenant, Subject: "user-1"}))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func seedSession(t *testing.T, repo *fleet.MemoryRepository, tenant, session string) {
	t.Helper()
	if err := repo.MarkOnline(context.Background(), tenant, "edge-1", session); err != nil {
		t.Fatalf("seed sesión: %v", err)
	}
}

// TestSetSessionRole_OK_Passive: el dueño fija el rol passive → 200 y persiste.
func TestSetSessionRole_OK_Passive(t *testing.T) {
	repo := fleet.NewMemoryRepository()
	seedSession(t, repo, ctxTenant, "sess-1")

	rec := doSessionRole(repo, ctxTenant, "sess-1", `{"role":"passive"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		SessionID string `json:"session_id"`
		Role      string `json:"role"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.SessionID != "sess-1" || out.Role != "passive" {
		t.Fatalf("respuesta inesperada: %+v", out)
	}
	s, _, err := repo.Get(context.Background(), ctxTenant, "edge-1", "sess-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if s.Role != fleet.RolePassive {
		t.Fatalf("no persistió passive: %q", s.Role)
	}
}

// TestSetSessionRole_401_NoIdentity: sin Identity en el contexto → 401.
func TestSetSessionRole_401_NoIdentity(t *testing.T) {
	repo := fleet.NewMemoryRepository()
	seedSession(t, repo, ctxTenant, "sess-1")
	rec := doSessionRole(repo, ctxTenant, "sess-1", `{"role":"passive"}`, false)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d, quiero 401", rec.Code)
	}
}

// TestSetSessionRole_400_InvalidBody: JSON roto o rol desconocido → 400.
func TestSetSessionRole_400_InvalidBody(t *testing.T) {
	repo := fleet.NewMemoryRepository()
	seedSession(t, repo, ctxTenant, "sess-1")
	for name, body := range map[string]string{
		"json roto":         `{`,
		"rol desconocido":   `{"role":"supervisor"}`,
		"rol vacío":         `{"role":""}`,
		"rol online (typo)": `{"role":"online"}`,
	} {
		rec := doSessionRole(repo, ctxTenant, "sess-1", body, true)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: code=%d, quiero 400; body=%s", name, rec.Code, rec.Body.String())
		}
	}
}

// TestSetSessionRole_404_CrossTenant: un tenant AJENO no puede tocar la sesión de
// otro (aislamiento multi-tenant, INV-8) → 404 opaco, y la sesión del dueño queda
// intacta (sigue bot).
func TestSetSessionRole_404_CrossTenant(t *testing.T) {
	repo := fleet.NewMemoryRepository()
	seedSession(t, repo, ctxTenant, "sess-1")

	rec := doSessionRole(repo, "otro-tenant", "sess-1", `{"role":"passive"}`, true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant code=%d, quiero 404; body=%s", rec.Code, rec.Body.String())
	}
	// La sesión del dueño NO cambió (nadie ajeno la mutó).
	s, _, err := repo.Get(context.Background(), ctxTenant, "edge-1", "sess-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if s.Role != fleet.RoleBot {
		t.Fatalf("aislamiento roto: la sesión del dueño cambió a %q", s.Role)
	}
}

// TestSetSessionRole_404_Unknown: una sesión inexistente para el tenant → 404.
func TestSetSessionRole_404_Unknown(t *testing.T) {
	repo := fleet.NewMemoryRepository()
	rec := doSessionRole(repo, ctxTenant, "no-existe", `{"role":"bot"}`, true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d, quiero 404; body=%s", rec.Code, rec.Body.String())
	}
}

// --- Plan 020 · T3: estatus de sesión (retiro de zombie) ---

// doSessionStatus ejecuta el handler de estatus vía un mux con el patrón real (para
// que r.PathValue("id") funcione), con una Identity del tenant inyectada como haría
// Authenticate. withID=false ejercita el 401.
func doSessionStatus(store admin.SessionStatusStore, tenant, sessionID, body string, withID bool) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	mux.Handle("POST /admin/sessions/{id}/status", admin.SetSessionStatusHandler(store))
	req := httptest.NewRequest(http.MethodPost, "/admin/sessions/"+sessionID+"/status", strings.NewReader(body))
	if withID {
		req = req.WithContext(httpapi.WithIdentity(req.Context(), httpapi.Identity{TenantID: tenant, Subject: "user-1"}))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestSetSessionStatus_OK_RetireZombie: el dueño retira un zombie (loggedout) → 200
// y persiste.
func TestSetSessionStatus_OK_RetireZombie(t *testing.T) {
	repo := fleet.NewMemoryRepository()
	seedSession(t, repo, ctxTenant, "sess-1")

	rec := doSessionStatus(repo, ctxTenant, "sess-1", `{"state":"loggedout"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		SessionID string `json:"session_id"`
		State     string `json:"state"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.SessionID != "sess-1" || out.State != "loggedout" {
		t.Fatalf("respuesta inesperada: %+v", out)
	}
	s, _, err := repo.Get(context.Background(), ctxTenant, "edge-1", "sess-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if s.State != fleet.StateLoggedOut {
		t.Fatalf("no persistió loggedout: %q", s.State)
	}
}

// TestSetSessionStatus_400_InvalidState: JSON roto o estado NO admin-admitido
// (online / arbitrario) → 400.
func TestSetSessionStatus_400_InvalidState(t *testing.T) {
	repo := fleet.NewMemoryRepository()
	seedSession(t, repo, ctxTenant, "sess-1")
	for name, body := range map[string]string{
		"json roto":         `{`,
		"online (derivado)": `{"state":"online"}`,
		"estado arbitrario": `{"state":"banana"}`,
		"estado vacío":      `{"state":""}`,
	} {
		rec := doSessionStatus(repo, ctxTenant, "sess-1", body, true)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: code=%d, quiero 400; body=%s", name, rec.Code, rec.Body.String())
		}
	}
}

// TestSetSessionStatus_401_NoIdentity: sin Identity en el contexto → 401.
func TestSetSessionStatus_401_NoIdentity(t *testing.T) {
	repo := fleet.NewMemoryRepository()
	seedSession(t, repo, ctxTenant, "sess-1")
	rec := doSessionStatus(repo, ctxTenant, "sess-1", `{"state":"offline"}`, false)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d, quiero 401", rec.Code)
	}
}

// TestSetSessionStatus_404_CrossTenant: un tenant AJENO no puede tocar la sesión de
// otro (aislamiento INV-8) → 404 opaco, y la sesión del dueño queda intacta (online).
func TestSetSessionStatus_404_CrossTenant(t *testing.T) {
	repo := fleet.NewMemoryRepository()
	seedSession(t, repo, ctxTenant, "sess-1")

	rec := doSessionStatus(repo, "otro-tenant", "sess-1", `{"state":"loggedout"}`, true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant code=%d, quiero 404; body=%s", rec.Code, rec.Body.String())
	}
	s, _, err := repo.Get(context.Background(), ctxTenant, "edge-1", "sess-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if s.State != fleet.StateOnline {
		t.Fatalf("aislamiento roto: la sesión del dueño cambió a %q", s.State)
	}
}

// --- Plan 046 · T1.2: perfil de sesión y deprecación de /role ---

// TestSetSessionProfile_OK_LasDosVias: el dueño fija el perfil por CADA una de las
// dos vías → 200 con {session_id, profile} y persiste — y persiste TAMBIÉN el alias
// legado `role` sincronizado (D-046.1), que es lo que impide que un lector viejo y
// uno nuevo se contradigan durante el ciclo de deprecación.
func TestSetSessionProfile_OK_LasDosVias(t *testing.T) {
	for nombre, via := range viasSesion {
		t.Run(nombre, func(t *testing.T) {
			repo := fleet.NewMemoryRepository()
			seedSession(t, repo, ctxTenant, "sess-1")

			rec := doSessionProfileEn(via, repo, nil, ctxTenant, "sess-1", `{"profile":"passive"}`, true)
			if rec.Code != http.StatusOK {
				t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
			}
			var out struct {
				SessionID string `json:"session_id"`
				Profile   string `json:"profile"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if out.SessionID != "sess-1" || out.Profile != "passive" {
				t.Fatalf("respuesta inesperada: %+v", out)
			}
			s, _, err := repo.Get(context.Background(), ctxTenant, "edge-1", "sess-1")
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if s.Profile != fleet.ProfilePassive {
				t.Fatalf("no persistió el perfil passive: %q", s.Profile)
			}
			if s.Role != fleet.RolePassive {
				t.Fatalf("el alias legado quedó desincronizado: role=%q con profile=%q", s.Role, s.Profile)
			}
		})
	}
}

// TestSetSessionProfile_OK_Active: active persiste como active y arrastra role=bot.
func TestSetSessionProfile_OK_Active(t *testing.T) {
	repo := fleet.NewMemoryRepository()
	seedSession(t, repo, ctxTenant, "sess-1")

	rec := doSessionProfileEn(viasSesion["api"], repo, nil, ctxTenant, "sess-1", `{"profile":"active"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	s, _, err := repo.Get(context.Background(), ctxTenant, "edge-1", "sess-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if s.Profile != fleet.ProfileActive || s.Role != fleet.RoleBot {
		t.Fatalf("esperaba profile=active/role=bot; tengo profile=%q role=%q", s.Profile, s.Role)
	}
}

// TestSetSessionProfile_400_Bot: `bot` es vocabulario de la ruta VIEJA. Sobre
// /profile es 400 y NO se traduce en silencio: si /profile aceptara `bot`, la
// deprecación no cortaría nunca y el vocabulario nuevo no significaría nada.
func TestSetSessionProfile_400_Bot(t *testing.T) {
	for nombre, via := range viasSesion {
		t.Run(nombre, func(t *testing.T) {
			repo := fleet.NewMemoryRepository()
			seedSession(t, repo, ctxTenant, "sess-1")
			rec := doSessionProfileEn(via, repo, nil, ctxTenant, "sess-1", `{"profile":"bot"}`, true)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("code=%d, quiero 400; body=%s", rec.Code, rec.Body.String())
			}
			// Y no tocó nada: la sesión sigue con el perfil con el que nació.
			// ⚠️ Ese perfil de nacimiento es PASSIVE, no active: defaultProfile
			// espeja el DEFAULT 'passive' de la 0063 (privacidad por defecto),
			// mientras defaultRole sigue dando 'bot' por la 0025. La divergencia
			// es deliberada (ver defaultProfile en fleet.go).
			s, _, err := repo.Get(context.Background(), ctxTenant, "edge-1", "sess-1")
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if s.Profile != fleet.ProfilePassive {
				t.Fatalf("un 400 mutó la sesión: profile=%q", s.Profile)
			}
		})
	}
}

// TestSetSessionProfile_400_OtrosCuerpos: JSON roto, perfil vacío o desconocido → 400.
func TestSetSessionProfile_400_OtrosCuerpos(t *testing.T) {
	repo := fleet.NewMemoryRepository()
	seedSession(t, repo, ctxTenant, "sess-1")
	for name, body := range map[string]string{
		"json roto":            `{`,
		"perfil vacío":         `{"profile":""}`,
		"perfil desconocido":   `{"profile":"supervisor"}`,
		"perfil online (typo)": `{"profile":"online"}`,
	} {
		rec := doSessionProfileEn(viasSesion["api"], repo, nil, ctxTenant, "sess-1", body, true)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: code=%d, quiero 400; body=%s", name, rec.Code, rec.Body.String())
		}
	}
}

// TestSetSessionProfile_401_SinIdentity: sin Identity en el contexto → 401, por las
// dos vías.
func TestSetSessionProfile_401_SinIdentity(t *testing.T) {
	for nombre, via := range viasSesion {
		t.Run(nombre, func(t *testing.T) {
			repo := fleet.NewMemoryRepository()
			seedSession(t, repo, ctxTenant, "sess-1")
			rec := doSessionProfileEn(via, repo, nil, ctxTenant, "sess-1", `{"profile":"passive"}`, false)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("code=%d, quiero 401", rec.Code)
			}
		})
	}
}

// TestSetSessionProfile_404_CrossTenant: un tenant AJENO no puede tocar la sesión de
// otro (aislamiento INV-8 del Plan 018) → 404 opaco y NUNCA 403: un 403 confirmaría que la
// sesión existe. La sesión del dueño queda intacta.
func TestSetSessionProfile_404_CrossTenant(t *testing.T) {
	for nombre, via := range viasSesion {
		t.Run(nombre, func(t *testing.T) {
			repo := fleet.NewMemoryRepository()
			seedSession(t, repo, ctxTenant, "sess-1")

			rec := doSessionProfileEn(via, repo, nil, "otro-tenant", "sess-1", `{"profile":"active"}`, true)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("cross-tenant code=%d, quiero 404; body=%s", rec.Code, rec.Body.String())
			}
			if rec.Code == http.StatusForbidden {
				t.Fatalf("403 filtra la existencia de la sesión ajena")
			}
			s, _, err := repo.Get(context.Background(), ctxTenant, "edge-1", "sess-1")
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			// Perfil de nacimiento = passive (DEFAULT de la 0063), no active.
			if s.Profile != fleet.ProfilePassive {
				t.Fatalf("aislamiento roto: la sesión del dueño cambió a %q", s.Profile)
			}
		})
	}
}

// TestSetSessionProfile_404_Desconocida: una sesión inexistente para el tenant → 404.
func TestSetSessionProfile_404_Desconocida(t *testing.T) {
	repo := fleet.NewMemoryRepository()
	rec := doSessionProfileEn(viasSesion["api"], repo, nil, ctxTenant, "no-existe", `{"profile":"active"}`, true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d, quiero 404; body=%s", rec.Code, rec.Body.String())
	}
}

// TestSetSessionRole_Deprecacion_LasDosVias es el corazón de T1.2: /role sigue
// devolviendo 200 con {session_id, role} —el contrato del cliente viejo NO cambia— y
// ADEMÁS avisa por partida doble. Las aserciones van sobre el NOMBRE EXACTO de las
// cabeceras (Deprecation, Link) y sobre el campo `deprecation` del cuerpo, no sobre
// «hay un aviso» en alguna parte.
func TestSetSessionRole_Deprecacion_LasDosVias(t *testing.T) {
	for nombre, via := range viasSesion {
		t.Run(nombre, func(t *testing.T) {
			repo := fleet.NewMemoryRepository()
			seedSession(t, repo, ctxTenant, "sess-1")

			rec := doSessionRoleEn(via, repo, nil, ctxTenant, "sess-1", `{"role":"passive"}`, true)
			if rec.Code != http.StatusOK {
				t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Deprecation"); got != "true" {
				t.Fatalf("cabecera Deprecation=%q, quiero \"true\"", got)
			}
			// El Link apunta al sucesor de ESTA MISMA vía: mandar al operador de
			// /admin a /api/v1 sería mandarlo a una ruta que puede no alcanzar.
			quiero := "<" + via + "/sess-1/profile>; rel=\"successor-version\""
			if got := rec.Header().Get("Link"); got != quiero {
				t.Fatalf("cabecera Link=%q, quiero %q", got, quiero)
			}
			var out struct {
				SessionID   string `json:"session_id"`
				Role        string `json:"role"`
				Deprecation string `json:"deprecation"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if out.SessionID != "sess-1" || out.Role != "passive" {
				t.Fatalf("el contrato viejo cambió: %+v", out)
			}
			if out.Deprecation == "" {
				t.Fatal("el cuerpo no trae el campo `deprecation`")
			}
			if !strings.Contains(out.Deprecation, via+"/sess-1/profile") {
				t.Fatalf("el aviso del cuerpo no nombra al sucesor de esta vía: %q", out.Deprecation)
			}
		})
	}
}

// TestSetSessionRole_Deprecacion_TambienEnLosErrores: el aviso viaja en las CINCO
// respuestas, no solo en el 200. Quien depure un 400 contra /role también tiene que
// enterarse de que la ruta se retira.
func TestSetSessionRole_Deprecacion_TambienEnLosErrores(t *testing.T) {
	repo := fleet.NewMemoryRepository()
	seedSession(t, repo, ctxTenant, "sess-1")
	casos := map[string]struct {
		tenant string
		body   string
		withID bool
		quiero int
	}{
		"400 rol inválido": {ctxTenant, `{"role":"supervisor"}`, true, http.StatusBadRequest},
		"401 sin identity": {ctxTenant, `{"role":"passive"}`, false, http.StatusUnauthorized},
		"404 otro tenant":  {"otro-tenant", `{"role":"passive"}`, true, http.StatusNotFound},
	}
	for name, c := range casos {
		rec := doSessionRoleEn(viasSesion["api"], repo, nil, c.tenant, "sess-1", c.body, c.withID)
		if rec.Code != c.quiero {
			t.Fatalf("%s: code=%d, quiero %d", name, rec.Code, c.quiero)
		}
		if got := rec.Header().Get("Deprecation"); got != "true" {
			t.Fatalf("%s: cabecera Deprecation=%q, quiero \"true\"", name, got)
		}
		if rec.Header().Get("Link") == "" {
			t.Fatalf("%s: falta la cabecera Link", name)
		}
	}
}

// TestSetSessionRole_TraduceAPerfil: /role sigue hablando de bot/passive de cara
// afuera, pero por dentro deja el PERFIL bien puesto (bot⇒active). Es lo que permite
// que las dos rutas convivan escribiendo el mismo dato.
func TestSetSessionRole_TraduceAPerfil(t *testing.T) {
	repo := fleet.NewMemoryRepository()
	seedSession(t, repo, ctxTenant, "sess-1")

	rec := doSessionRoleEn(viasSesion["admin"], repo, nil, ctxTenant, "sess-1", `{"role":"bot"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
	}
	s, _, err := repo.Get(context.Background(), ctxTenant, "edge-1", "sess-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if s.Profile != fleet.ProfileActive {
		t.Fatalf("/role no dejó el perfil traducido: profile=%q, quiero active", s.Profile)
	}
}

// TestProfilePusher_BestEffort_NoCambiaElCodigo es el contrato de ConfigPusher
// replicado: el push va DESPUÉS de persistir y su fallo se loguea sin tocar la
// respuesta. Un 500 aquí convertiría un problema de entrega en un problema de
// escritura, y el dueño reintentaría un cambio que YA está guardado.
func TestProfilePusher_BestEffort_NoCambiaElCodigo(t *testing.T) {
	rutas := map[string]func(*fleet.MemoryRepository, *pusherEspia) *httptest.ResponseRecorder{
		"/profile": func(repo *fleet.MemoryRepository, p *pusherEspia) *httptest.ResponseRecorder {
			return doSessionProfileEn(viasSesion["api"], repo, p, ctxTenant, "sess-1", `{"profile":"passive"}`, true)
		},
		"/role": func(repo *fleet.MemoryRepository, p *pusherEspia) *httptest.ResponseRecorder {
			return doSessionRoleEn(viasSesion["api"], repo, p, ctxTenant, "sess-1", `{"role":"passive"}`, true)
		},
	}
	for nombre, ejecutar := range rutas {
		t.Run(nombre, func(t *testing.T) {
			repo := fleet.NewMemoryRepository()
			seedSession(t, repo, ctxTenant, "sess-1")
			espia := &pusherEspia{err: errors.New("el Edge no está conectado")}

			rec := ejecutar(repo, espia)
			if rec.Code != http.StatusOK {
				t.Fatalf("un push fallido cambió el código: %d, quiero 200; body=%s", rec.Code, rec.Body.String())
			}
			if espia.llamadas != 1 {
				t.Fatalf("llamadas al pusher=%d, quiero 1", espia.llamadas)
			}
			if espia.tenant != ctxTenant || espia.sesion != "sess-1" || espia.perfil != "passive" {
				t.Fatalf("el pusher recibió %+v", *espia)
			}
			// Y lo persistido sigue ahí: el fallo del push no deshace nada.
			s, _, err := repo.Get(context.Background(), ctxTenant, "edge-1", "sess-1")
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if s.Profile != fleet.ProfilePassive {
				t.Fatalf("el push fallido se llevó por delante la escritura: %q", s.Profile)
			}
		})
	}
}

// TestProfilePusher_Nil_EsNoOp: el cableado de T1.2 pasa nil por las dos vías. Un
// nil no puede reventar el handler: es el estado NORMAL hasta que T2.1 lo enchufe.
func TestProfilePusher_Nil_EsNoOp(t *testing.T) {
	repo := fleet.NewMemoryRepository()
	seedSession(t, repo, ctxTenant, "sess-1")
	if rec := doSessionProfileEn(viasSesion["api"], repo, nil, ctxTenant, "sess-1",
		`{"profile":"passive"}`, true); rec.Code != http.StatusOK {
		t.Fatalf("/profile con pusher nil: code=%d, quiero 200", rec.Code)
	}
	if rec := doSessionRoleEn(viasSesion["api"], repo, nil, ctxTenant, "sess-1",
		`{"role":"bot"}`, true); rec.Code != http.StatusOK {
		t.Fatalf("/role con pusher nil: code=%d, quiero 200", rec.Code)
	}
}

// TestProfilePusher_NoSeLlamaSiNoSePersistio: un 404 (sesión de otro tenant) no
// empuja nada. Empujar un cambio que no ocurrió le diría al Edge que se reconfigure
// por una escritura que la base rechazó.
func TestProfilePusher_NoSeLlamaSiNoSePersistio(t *testing.T) {
	repo := fleet.NewMemoryRepository()
	seedSession(t, repo, ctxTenant, "sess-1")
	espia := &pusherEspia{}

	rec := doSessionProfileEn(viasSesion["api"], repo, espia, "otro-tenant", "sess-1", `{"profile":"passive"}`, true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d, quiero 404", rec.Code)
	}
	if espia.llamadas != 0 {
		t.Fatalf("se empujó un cambio que no se persistió: %d llamadas", espia.llamadas)
	}
}
