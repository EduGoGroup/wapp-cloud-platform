package filtercfg_test

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/filtercfg"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
)

// fuenteFija es un filtercfg.Source de prueba: devuelve la foto que se le dio, o el
// error que se le dio.
type fuenteFija struct {
	tp      fleet.TenantProfiles
	err     error
	pedidos []string // tenants que se le consultaron, en orden
}

func (f *fuenteFija) ProfilesByTenant(_ context.Context, tenantID string) (fleet.TenantProfiles, error) {
	f.pedidos = append(f.pedidos, tenantID)
	if f.err != nil {
		return fleet.TenantProfiles{}, f.err
	}
	return f.tp, nil
}

// pushEspia captura el último PushConfig y puede fallar a voluntad.
type pushEspia struct {
	llamadas int
	tenant   string
	kind     string
	version  string
	payload  []byte
	err      error
}

func (p *pushEspia) PushConfig(_ context.Context, tenantID, kind, version string, payload []byte) error {
	p.llamadas++
	p.tenant, p.kind, p.version, p.payload = tenantID, kind, version, payload
	return p.err
}

// decodePayload deserializa el payload al MISMO shape que el Edge espera, declarado
// aquí a mano y NO reusando filtercfg.Payload: si un día alguien renombra la etiqueta
// JSON del struct de producción, reusarlo haría que el test renombrara con él y no
// dijera ni pío. El contrato se escribe dos veces a propósito.
func decodePayload(t *testing.T, raw []byte) struct {
	Version  int64 `json:"version"`
	Sessions map[string]struct {
		Profile string `json:"profile"`
	} `json:"sessions"`
} {
	t.Helper()
	var out struct {
		Version  int64 `json:"version"`
		Sessions map[string]struct {
			Profile string `json:"profile"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("payload no deserializa al contrato de D-046.2: %v — %s", err, raw)
	}
	return out
}

// TestKind_EsElLiteralAcordado. El Edge se escribió EN PARALELO contra este string.
// Un cambio aquí no rompe nada visible: el Edge ignora los kinds que no conoce con un
// log tolerante, así que el filtro simplemente dejaría de aplicarse. Por eso se ancla.
func TestKind_EsElLiteralAcordado(t *testing.T) {
	if filtercfg.Kind != "filters" {
		t.Fatalf("Kind = %q, quiero \"filters\" (D-046.2, contrato con el Edge)", filtercfg.Kind)
	}
}

// TestBuild_IncluyeTodasLasSesiones_TambienLasActivas es la regla que sostiene el
// fail-open del contrato: el Edge asume `active` para toda sesión AUSENTE del mapa, así
// que omitir las activas «porque se asumen» coincidiría hoy y mentiría el día que una
// sesión pase de pasiva a activa (se quedaría con el passive viejo).
func TestBuild_IncluyeTodasLasSesiones_TambienLasActivas(t *testing.T) {
	tp := fleet.TenantProfiles{
		Version: 1_700_000_000_123_456,
		Sessions: map[string]fleet.Profile{
			"sess-activa": fleet.ProfileActive,
			"sess-pasiva": fleet.ProfilePassive,
		},
	}
	version, raw, err := filtercfg.Build(tp)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	got := decodePayload(t, raw)
	if len(got.Sessions) != 2 {
		t.Fatalf("el mapa trae %d sesiones, quiero 2 (las activas TAMBIÉN viajan): %s", len(got.Sessions), raw)
	}
	if got.Sessions["sess-activa"].Profile != "active" || got.Sessions["sess-pasiva"].Profile != "passive" {
		t.Fatalf("perfiles mal proyectados: %s", raw)
	}
	// La version del frame y la del payload son EL MISMO entero (el Edge compara como
	// número; un hash o un uuid aquí romperían la monotonicidad).
	if version != strconv.FormatInt(tp.Version, 10) {
		t.Fatalf("version del frame = %q, quiero la decimal de %d", version, tp.Version)
	}
	if got.Version != tp.Version {
		t.Fatalf("version del payload = %d, quiero %d (misma que la del frame)", got.Version, tp.Version)
	}
}

// TestBuild_TenantSinSesiones_MapaVacioNoNull: un tenant sin filas produce
// `"sessions":{}`, NO `null`. Un `null` obligaría al Edge a distinguir dos formas de
// «vacío» y es exactamente el tipo de detalle que se descubre en campo.
func TestBuild_TenantSinSesiones_MapaVacioNoNull(t *testing.T) {
	_, raw, err := filtercfg.Build(fleet.TenantProfiles{Sessions: nil})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var crudo map[string]json.RawMessage
	if err := json.Unmarshal(raw, &crudo); err != nil {
		t.Fatalf("payload inválido: %v", err)
	}
	if string(crudo["sessions"]) != "{}" {
		t.Fatalf("sessions = %s, quiero {} (nunca null)", crudo["sessions"])
	}
	got := decodePayload(t, raw)
	if got.Version != 0 {
		t.Fatalf("version = %d, quiero 0 para un tenant sin filas", got.Version)
	}
}

// TestBuild_PerfilDesconocido_DegradaAPassive: la columna tiene CHECK, así que esto no
// debería pasar; si pasara, emitir el valor crudo haría que el validador del Edge
// tirara el payload ENTERO y se quedara con el last-known-good de todas las sesiones.
// Degradar solo esa sesión al valor seguro es estrictamente mejor.
func TestBuild_PerfilDesconocido_DegradaAPassive(t *testing.T) {
	_, raw, err := filtercfg.Build(fleet.TenantProfiles{
		Version:  7,
		Sessions: map[string]fleet.Profile{"sess-rara": fleet.Profile("supervisor")},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := decodePayload(t, raw); got.Sessions["sess-rara"].Profile != "passive" {
		t.Fatalf("perfil desconocido = %q, quiero \"passive\" (lectura segura)", got.Sessions["sess-rara"].Profile)
	}
}

// TestPusher_EmpujaLaFotoDelTenantEntero_NoLaSesionDisparadora. El argumento sessionID
// es el DISPARADOR, no el contenido: si el pusher armara el mapa con él, el Edge —que
// interpreta la ausencia como `active`— reactivaría en silencio TODAS las demás pasivas
// del tenant. Este test es el que detecta ese atajo.
func TestPusher_EmpujaLaFotoDelTenantEntero_NoLaSesionDisparadora(t *testing.T) {
	src := &fuenteFija{tp: fleet.TenantProfiles{
		Version: 42,
		Sessions: map[string]fleet.Profile{
			"sess-1": fleet.ProfilePassive,
			"sess-2": fleet.ProfilePassive,
			"sess-3": fleet.ProfileActive,
		},
	}}
	espia := &pushEspia{}
	p := filtercfg.NewPusher(src, espia)

	if err := p.PushProfile(context.Background(), "t-1", "sess-3", fleet.ProfileActive); err != nil {
		t.Fatalf("PushProfile: %v", err)
	}
	if espia.llamadas != 1 || espia.tenant != "t-1" || espia.kind != "filters" {
		t.Fatalf("push inesperado: llamadas=%d tenant=%q kind=%q", espia.llamadas, espia.tenant, espia.kind)
	}
	if espia.version != "42" {
		t.Fatalf("version del frame = %q, quiero \"42\"", espia.version)
	}
	got := decodePayload(t, espia.payload)
	if len(got.Sessions) != 3 {
		t.Fatalf("se empujaron %d sesiones, quiero las 3 del tenant: %s", len(got.Sessions), espia.payload)
	}
	if got.Sessions["sess-1"].Profile != "passive" || got.Sessions["sess-3"].Profile != "active" {
		t.Fatalf("foto del tenant mal armada: %s", espia.payload)
	}
}

// TestPusher_ErrorDeLectura_SePropaga: el error sube al handler, que lo LOGUEA y no
// cambia el código de respuesta (contrato best-effort de ProfilePusher). Lo que no
// puede pasar es que se empuje una foto a medias.
func TestPusher_ErrorDeLectura_SePropaga(t *testing.T) {
	boom := errors.New("bd caída")
	espia := &pushEspia{}
	p := filtercfg.NewPusher(&fuenteFija{err: boom}, espia)

	err := p.PushProfile(context.Background(), "t-1", "sess-1", fleet.ProfilePassive)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, quiero que envuelva %v", err, boom)
	}
	if espia.llamadas != 0 {
		t.Fatalf("se empujó %d veces con la lectura rota: no se empuja config a medias", espia.llamadas)
	}
}

// TestPusher_SinGateway_EsNoOp: un Pusher sin ConfigPusher no consulta ni empuja. Sirve
// para montar el hook en un test sin Gateway, y garantiza que el no-op es de verdad
// no-op (ni siquiera pega a la BD).
func TestPusher_SinGateway_EsNoOp(t *testing.T) {
	src := &fuenteFija{tp: fleet.TenantProfiles{Sessions: map[string]fleet.Profile{"s": fleet.ProfileActive}}}
	p := filtercfg.NewPusher(src, nil)
	if err := p.PushProfile(context.Background(), "t-1", "s", fleet.ProfileActive); err != nil {
		t.Fatalf("PushProfile sin gateway: %v", err)
	}
	if len(src.pedidos) != 0 {
		t.Fatalf("el no-op consultó la fuente %d veces", len(src.pedidos))
	}
}

// TestForTenant_NoConsultaEntitlements_YSiempreDevuelveConfig es la REGLA 1 de T2.1
// leída desde el comportamiento: la firma de Source no tiene por dónde preguntar por
// una feature, y un tenant SIN ninguna sesión pasiva recibe config igual (regla 2).
// Un provider que devolviera nil «porque no hay nada que filtrar» dejaría al Edge con
// el mapa anterior y una sesión reactivada seguiría muda.
func TestForTenant_NoConsultaEntitlements_YSiempreDevuelveConfig(t *testing.T) {
	src := &fuenteFija{tp: fleet.TenantProfiles{
		Version:  9,
		Sessions: map[string]fleet.Profile{"sess-1": fleet.ProfileActive, "sess-2": fleet.ProfileActive},
	}}
	version, raw, err := filtercfg.ForTenant(context.Background(), src, "t-sin-pasivas")
	if err != nil {
		t.Fatalf("ForTenant: %v", err)
	}
	if version == "" || len(raw) == 0 {
		t.Fatal("un tenant sin ni una pasiva DEBE recibir su config igual (regla 2 de T2.1)")
	}
	if got := decodePayload(t, raw); len(got.Sessions) != 2 {
		t.Fatalf("mapa con %d sesiones, quiero 2 todas active", len(got.Sessions))
	}
}
