package filtercfg_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/filtercfg"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet"
)

// fixedSource es un filtercfg.Source de prueba: devuelve la foto que se le dio, o el
// error que se le dio, y apunta los tenants por los que se le preguntó, en orden.
type fixedSource struct {
	profiles fleet.TenantProfiles
	err      error
	asked    []string
}

var _ filtercfg.Source = (*fixedSource)(nil)

func (s *fixedSource) ProfilesByTenant(_ context.Context, tenantID string) (fleet.TenantProfiles, error) {
	s.asked = append(s.asked, tenantID)
	if s.err != nil {
		return fleet.TenantProfiles{}, s.err
	}
	return s.profiles, nil
}

// pushCall son los argumentos de un PushConfig.
type pushCall struct {
	ctx      context.Context
	tenantID string
	kind     string
	version  string
	payload  []byte
}

// spyPusher es un filtercfg.ConfigPusher de prueba: apunta cada PushConfig y devuelve
// el error que se le dio.
type spyPusher struct {
	calls []pushCall
	err   error
}

var _ filtercfg.ConfigPusher = (*spyPusher)(nil)

func (p *spyPusher) PushConfig(ctx context.Context, tenantID, kind, version string, payload []byte) error {
	p.calls = append(p.calls, pushCall{ctx: ctx, tenantID: tenantID, kind: kind, version: version, payload: payload})
	return p.err
}

// wirePayload es el shape de D-046.2 tal como lo lee el Edge, declarado aquí A MANO y
// no reusando filtercfg.Payload: si un día alguien renombra una etiqueta JSON del
// struct de producción, reusarlo haría que el test renombrara con él y no dijera nada.
// El contrato se escribe dos veces a propósito.
type wirePayload struct {
	Version  *int64 `json:"version"`
	Sessions map[string]struct {
		Profile *string `json:"profile"`
	} `json:"sessions"`
}

// decodeWire deserializa el payload al shape del cable. Rechaza las claves que el
// contrato no conoce, y exige que "version" y cada "profile" estén presentes.
func decodeWire(t *testing.T, raw []byte) (version int64, sessions map[string]string) {
	t.Helper()
	var out wirePayload
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		t.Fatalf("el payload no deserializa al contrato de D-046.2: %v — %s", err, raw)
	}
	if out.Version == nil {
		t.Fatalf("el payload no trae la clave \"version\": %s", raw)
	}
	sessions = make(map[string]string, len(out.Sessions))
	for id, f := range out.Sessions {
		if f.Profile == nil {
			t.Fatalf("la sesión %q no trae la clave \"profile\": %s", id, raw)
		}
		sessions[id] = *f.Profile
	}
	return *out.Version, sessions
}

// ctxKey es la clave del valor con el que se reconoce un contexto.
type ctxKey struct{}

// markedContext devuelve un contexto reconocible, para comprobar que es ESE el que
// llega al puerto de salida.
func markedContext() context.Context {
	return context.WithValue(context.Background(), ctxKey{}, "marked")
}

// R-C1. El Edge se escribió en paralelo contra este string: un cambio aquí no rompe
// nada visible, el filtro simplemente dejaría de aplicarse.
func TestKind_IsTheAgreedLiteral(t *testing.T) {
	if filtercfg.Kind != "filters" {
		t.Fatalf("Kind = %q, quería \"filters\" (D-046.2, contrato con el Edge)", filtercfg.Kind)
	}
}

// Las etiquetas JSON de Payload y SessionFilter son el contrato del cable: se
// comprueban byte a byte sobre un valor construido a mano.
func TestPayload_JSONTagsAreTheWireContract(t *testing.T) {
	raw, err := json.Marshal(filtercfg.Payload{
		Version:  7,
		Sessions: map[string]filtercfg.SessionFilter{"s-1": {Profile: "passive"}},
	})
	if err != nil {
		t.Fatalf("json.Marshal: error inesperado %v", err)
	}
	const want = `{"version":7,"sessions":{"s-1":{"profile":"passive"}}}`
	if string(raw) != want {
		t.Fatalf("JSON = %s, quería %s", raw, want)
	}
}

// R-C2. Las activas TAMBIÉN viajan: el Edge asume `active` para toda sesión ausente,
// así que omitirlas dejaría con el `passive` viejo a la que pase de pasiva a activa.
func TestBuild_IncludesEverySession_ActiveOnesToo(t *testing.T) {
	tp := fleet.TenantProfiles{
		Version: 1_700_000_000_123_456,
		Sessions: map[string]fleet.Profile{
			"s-active-1": fleet.ProfileActive,
			"s-active-2": fleet.ProfileActive,
			"s-passive":  fleet.ProfilePassive,
		},
	}
	_, raw, err := filtercfg.Build(tp)
	if err != nil {
		t.Fatalf("Build: error inesperado %v", err)
	}
	_, got := decodeWire(t, raw)
	want := map[string]string{"s-active-1": "active", "s-active-2": "active", "s-passive": "passive"}
	if len(got) != len(want) {
		t.Fatalf("el mapa trae %d sesiones, quería %d (las activas también viajan): %s", len(got), len(want), raw)
	}
	for id, profile := range want {
		if got[id] != profile {
			t.Errorf("sesión %q = %q, quería %q: %s", id, got[id], profile, raw)
		}
	}
}

// R-C2. El perfil viaja como OBJETO {"profile": …}, no como un string suelto: es lo
// que deja añadir un campo mañana sin romper al Edge.
func TestBuild_ProfileTravelsAsAnObject(t *testing.T) {
	_, raw, err := filtercfg.Build(fleet.TenantProfiles{
		Version:  3,
		Sessions: map[string]fleet.Profile{"s-1": fleet.ProfileActive},
	})
	if err != nil {
		t.Fatalf("Build: error inesperado %v", err)
	}
	const want = `{"version":3,"sessions":{"s-1":{"profile":"active"}}}`
	if string(raw) != want {
		t.Fatalf("payload = %s, quería %s", raw, want)
	}
}

// R-C2. Sin sesiones el JSON trae "sessions":{} y nunca null: un null obligaría al
// Edge a distinguir dos formas de «vacío». Se miran los BYTES, porque decodificado a
// un mapa un null y un {} se parecen demasiado.
func TestBuild_WithoutSessions_EmptyObjectNotNull(t *testing.T) {
	cases := []struct {
		name string
		in   fleet.TenantProfiles
		want string
	}{
		{"zero value", fleet.TenantProfiles{}, `{"version":0,"sessions":{}}`},
		{"nil sessions", fleet.TenantProfiles{Version: 5, Sessions: nil}, `{"version":5,"sessions":{}}`},
		{"empty map", fleet.TenantProfiles{Version: 5, Sessions: map[string]fleet.Profile{}}, `{"version":5,"sessions":{}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			version, raw, err := filtercfg.Build(c.in)
			if err != nil {
				t.Fatalf("Build: error inesperado %v", err)
			}
			if string(raw) != c.want {
				t.Errorf("payload = %s, quería %s", raw, c.want)
			}
			if bytes.Contains(raw, []byte("null")) {
				t.Errorf("el payload trae un null: %s", raw)
			}
			if want := strconv.FormatInt(c.in.Version, 10); version != want {
				t.Errorf("version = %q, quería %q", version, want)
			}
		})
	}
}

// R-C2. Un perfil que no es active|passive —incluido el vacío— degrada a passive: el
// valor crudo haría que el Edge rechazara el payload ENTERO. Solo degrada esa sesión.
func TestBuild_InvalidProfile_DegradesToPassive(t *testing.T) {
	cases := []struct {
		name    string
		profile fleet.Profile
	}{
		{"unknown", fleet.Profile("supervisor")},
		{"empty", fleet.Profile("")},
		{"other case", fleet.Profile("ACTIVE")},
		{"retired axis", fleet.Profile("bot")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if fleet.ValidProfile(c.profile) {
				t.Fatalf("el caso no vale: %q es un perfil válido", c.profile)
			}
			_, raw, err := filtercfg.Build(fleet.TenantProfiles{
				Version: 7,
				Sessions: map[string]fleet.Profile{
					"s-odd":    c.profile,
					"s-active": fleet.ProfileActive,
				},
			})
			if err != nil {
				t.Fatalf("Build: error inesperado %v", err)
			}
			_, got := decodeWire(t, raw)
			if got["s-odd"] != "passive" {
				t.Errorf("perfil %q proyectado como %q, quería \"passive\": %s", c.profile, got["s-odd"], raw)
			}
			if got["s-active"] != "active" {
				t.Errorf("la sesión vecina salió %q, quería \"active\" (solo degrada la inválida): %s", got["s-active"], raw)
			}
		})
	}
}

// R-C2. La versión del frame y la del payload son EL MISMO entero: el Edge la compara
// como número, y un hash o un UUID romperían la monotonicidad.
func TestBuild_VersionIsTheSameIntegerInFrameAndPayload(t *testing.T) {
	cases := []struct {
		name    string
		version int64
	}{
		{"zero", 0},
		{"small", 42},
		{"unix micros", 1_700_000_000_123_456},
		{"negative", -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			version, raw, err := filtercfg.Build(fleet.TenantProfiles{
				Version:  c.version,
				Sessions: map[string]fleet.Profile{"s-1": fleet.ProfilePassive},
			})
			if err != nil {
				t.Fatalf("Build: error inesperado %v", err)
			}
			if want := strconv.FormatInt(c.version, 10); version != want {
				t.Errorf("version del frame = %q, quería %q", version, want)
			}
			inPayload, _ := decodeWire(t, raw)
			if inPayload != c.version {
				t.Errorf("version del payload = %d, quería %d (la misma que la del frame)", inPayload, c.version)
			}
		})
	}
}

// R-C4. Una sola lectura, con el tenant dado, y el resultado es el Build de esa foto.
// Source es el único puerto que recibe: no tiene por dónde preguntar por una feature,
// y un tenant sin ni una pasiva recibe config igual.
func TestForTenant_ReadsTheSourceOnce_AndReturnsItsBuild(t *testing.T) {
	tp := fleet.TenantProfiles{
		Version:  9,
		Sessions: map[string]fleet.Profile{"s-1": fleet.ProfileActive, "s-2": fleet.ProfileActive},
	}
	src := &fixedSource{profiles: tp}
	version, raw, err := filtercfg.ForTenant(context.Background(), src, "tenant-a")
	if err != nil {
		t.Fatalf("ForTenant: error inesperado %v", err)
	}
	if len(src.asked) != 1 || src.asked[0] != "tenant-a" {
		t.Errorf("lecturas de la fuente = %q, quería una sola con \"tenant-a\"", src.asked)
	}
	wantVersion, wantRaw, err := filtercfg.Build(tp)
	if err != nil {
		t.Fatalf("Build: error inesperado %v", err)
	}
	if version != wantVersion || version != "9" {
		t.Errorf("version = %q, quería %q", version, "9")
	}
	if !bytes.Equal(raw, wantRaw) {
		t.Errorf("payload = %s, quería el de Build: %s", raw, wantRaw)
	}
	_, got := decodeWire(t, raw)
	if len(got) != 2 || got["s-1"] != "active" || got["s-2"] != "active" {
		t.Errorf("un tenant todo-activo debe recibir su mapa entero: %s", raw)
	}
}

// R-C4. Un tenant sin sesiones no es un fallo: versión "0" y mapa vacío, que se
// empujan igual (regla 2).
func TestForTenant_TenantWithoutSessions_StillReturnsConfig(t *testing.T) {
	src := &fixedSource{profiles: fleet.TenantProfiles{}}
	version, raw, err := filtercfg.ForTenant(context.Background(), src, "tenant-empty")
	if err != nil {
		t.Fatalf("ForTenant: error inesperado %v", err)
	}
	if version != "0" {
		t.Errorf("version = %q, quería \"0\"", version)
	}
	if want := `{"version":0,"sessions":{}}`; string(raw) != want {
		t.Errorf("payload = %s, quería %s", raw, want)
	}
}

// R-C4. El fallo de lectura se propaga envuelto y sin config a medias.
func TestForTenant_ReadError_IsWrappedAndReturnsNothing(t *testing.T) {
	cause := errors.New("db down")
	src := &fixedSource{err: cause}
	version, raw, err := filtercfg.ForTenant(context.Background(), src, "tenant-a")
	if !errors.Is(err, cause) {
		t.Fatalf("err = %v, quería que envolviera %v", err, cause)
	}
	if want := "filtercfg: leer perfiles del tenant: db down"; err.Error() != want {
		t.Errorf("texto del error = %q, quería %q", err.Error(), want)
	}
	if version != "" || raw != nil {
		t.Errorf("con error devolvió (%q, %s), quería (\"\", nil)", version, raw)
	}
	if len(src.asked) != 1 {
		t.Errorf("lecturas de la fuente = %d, quería 1", len(src.asked))
	}
}

// R-C3. sessionID y profile son el DISPARADOR, no el contenido: lo que se empuja es la
// foto del tenant entero, releída de la fuente. Por eso el disparador CONTRADICE aquí
// a la fuente (dice active, la fuente dice passive): si el Pusher armara el mapa con
// el argumento, el Edge reactivaría en silencio todas las demás pasivas.
func TestPusher_PushProfile_PushesTheWholeTenantSnapshot(t *testing.T) {
	src := &fixedSource{profiles: fleet.TenantProfiles{
		Version: 42,
		Sessions: map[string]fleet.Profile{
			"s-1": fleet.ProfilePassive,
			"s-2": fleet.ProfilePassive,
			"s-3": fleet.ProfilePassive,
		},
	}}
	spy := &spyPusher{}
	p := filtercfg.NewPusher(src, spy)
	if len(src.asked) != 0 || len(spy.calls) != 0 {
		t.Fatalf("NewPusher consultó o empujó al construir: lecturas=%d, pushes=%d", len(src.asked), len(spy.calls))
	}

	ctx := markedContext()
	if err := p.PushProfile(ctx, "tenant-a", "s-3", fleet.ProfileActive); err != nil {
		t.Fatalf("PushProfile: error inesperado %v", err)
	}
	if len(src.asked) != 1 || src.asked[0] != "tenant-a" {
		t.Errorf("lecturas de la fuente = %q, quería una sola con \"tenant-a\"", src.asked)
	}
	if len(spy.calls) != 1 {
		t.Fatalf("PushConfig se llamó %d veces, quería exactamente 1", len(spy.calls))
	}
	call := spy.calls[0]
	if call.ctx != ctx {
		t.Errorf("PushConfig no recibió el contexto del llamante")
	}
	if call.tenantID != "tenant-a" {
		t.Errorf("tenant del push = %q, quería \"tenant-a\"", call.tenantID)
	}
	if call.kind != "filters" {
		t.Errorf("kind del push = %q, quería \"filters\"", call.kind)
	}
	if call.version != "42" {
		t.Errorf("version del push = %q, quería \"42\"", call.version)
	}
	const want = `{"version":42,"sessions":{"s-1":{"profile":"passive"},"s-2":{"profile":"passive"},"s-3":{"profile":"passive"}}}`
	if string(call.payload) != want {
		t.Errorf("payload del push = %s, quería la foto de la fuente: %s", call.payload, want)
	}
}

// R-C3. Con la lectura rota no se empuja nada, y el error sube envuelto con el
// disparador en el texto: el handler lo loguea y no cambia el código de respuesta.
func TestPusher_PushProfile_ReadError_IsWrappedAndPushesNothing(t *testing.T) {
	cause := errors.New("db down")
	spy := &spyPusher{}
	p := filtercfg.NewPusher(&fixedSource{err: cause}, spy)

	err := p.PushProfile(context.Background(), "tenant-a", "s-1", fleet.ProfilePassive)
	if !errors.Is(err, cause) {
		t.Fatalf("err = %v, quería que envolviera %v", err, cause)
	}
	const want = "filtercfg: armar filtros del tenant (disparado por s-1=passive): " +
		"filtercfg: leer perfiles del tenant: db down"
	if err.Error() != want {
		t.Errorf("texto del error = %q, quería %q", err.Error(), want)
	}
	if len(spy.calls) != 0 {
		t.Errorf("PushConfig se llamó %d veces con la lectura rota: no se empuja config a medias", len(spy.calls))
	}
}

// R-C3. El error del fan-out vuelve TAL CUAL: el mismo valor, sin envolver.
func TestPusher_PushProfile_PushError_ComesBackUnwrapped(t *testing.T) {
	cause := errors.New("stream closed")
	spy := &spyPusher{err: cause}
	src := &fixedSource{profiles: fleet.TenantProfiles{
		Version:  1,
		Sessions: map[string]fleet.Profile{"s-1": fleet.ProfileActive},
	}}
	p := filtercfg.NewPusher(src, spy)

	err := p.PushProfile(context.Background(), "tenant-a", "s-1", fleet.ProfileActive)
	if err != cause { //nolint:errorlint // la promesa es la IDENTIDAD: un error envuelto también pasaría errors.Is.
		t.Fatalf("err = %v, quería exactamente %v, sin envolver", err, cause)
	}
	if len(spy.calls) != 1 {
		t.Errorf("PushConfig se llamó %d veces, quería 1", len(spy.calls))
	}
}

// R-C3. Sin gateway el hook es un no-op de verdad: ni empuja ni consulta la fuente.
// Vale para el receptor nil, para el Pusher sin ConfigPusher y para el valor cero.
func TestPusher_PushProfile_WithoutPusher_IsNoOp(t *testing.T) {
	newSource := func() *fixedSource {
		return &fixedSource{profiles: fleet.TenantProfiles{
			Version:  1,
			Sessions: map[string]fleet.Profile{"s-1": fleet.ProfileActive},
		}}
	}
	cases := []struct {
		name string
		make func(src *fixedSource) *filtercfg.Pusher
	}{
		{"nil receiver", func(*fixedSource) *filtercfg.Pusher { return nil }},
		{"nil config pusher", func(src *fixedSource) *filtercfg.Pusher { return filtercfg.NewPusher(src, nil) }},
		{"zero value", func(*fixedSource) *filtercfg.Pusher { return &filtercfg.Pusher{} }},
		// Con la fuente rota tampoco falla: no llega a leerla.
		{"nil config pusher and broken source", func(src *fixedSource) *filtercfg.Pusher {
			src.err = errors.New("db down")
			return filtercfg.NewPusher(src, nil)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := newSource()
			p := c.make(src)
			if err := p.PushProfile(context.Background(), "tenant-a", "s-1", fleet.ProfileActive); err != nil {
				t.Fatalf("PushProfile: error inesperado %v", err)
			}
			if len(src.asked) != 0 {
				t.Errorf("el no-op consultó la fuente %d veces, quería 0", len(src.asked))
			}
		})
	}
}
