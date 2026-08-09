package entitlements

// Tests del gate PLURAL (RequireAnyFeature, Plan 043 · T3.9b): el de una capacidad
// que no pertenece a una feature sino a varias, y basta tener una.
//
// Reusan el harness de middleware_test.go (nextOK, requestConTenant): el gate
// plural es el mismo contrato con otra pregunta, y probarlo con otro andamiaje
// habría sido la primera grieta por donde los dos empezaran a divergir.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// lasCuatro son claves reales del catálogo (las de los tipos de fábrica), no
// inventadas: un gate probado con features que no existen no dice nada sobre el
// que corre en producción.
var lasCuatro = []string{FeatureCartBasic, FeatureMedia, FeatureMenu, FeatureSurvey}

// ejecutaAny corre el gate plural sobre nextOK.
func ejecutaAny(t *testing.T, resolver Resolver, req *http.Request, features ...string) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	var llamado bool
	rec := httptest.NewRecorder()
	RequireAnyFeature(resolver, features...)(nextOK(&llamado)).ServeHTTP(rec, req)
	return rec, llamado
}

// exigeDenegadoAny comprueba el corte del plural: 403, cuerpo con la lista de las
// que habrían valido y el handler protegido SIN ejecutar.
func exigeDenegadoAny(t *testing.T, rec *httptest.ResponseRecorder, llamado bool, features []string) {
	t.Helper()
	if llamado {
		t.Fatal("el handler protegido NO debió ejecutarse")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code=%d, quería 403; body=%s", rec.Code, rec.Body.String())
	}
	want := `{"error":"feature_not_enabled"`
	if len(features) > 0 {
		want += `,"features":["` + features[0] + `"`
		for _, f := range features[1:] {
			want += `,"` + f + `"`
		}
		want += `]`
	}
	want += `}`
	if got := rec.Body.String(); got != want {
		t.Fatalf("body=%s, quería %s", got, want)
	}
}

// TestRequireAnyFeature_UnaBasta: con UNA sola de las cuatro encendida, pasa. Es la
// razón de existir del gate — un tenant de solo encuestas entra a la bandeja de
// eventos conversacionales sin tener el carrito.
func TestRequireAnyFeature_UnaBasta(t *testing.T) {
	fake := NewFake()
	fake.Enable("t1", FeatureSurvey) // la ÚLTIMA de la lista: no vale con mirar la primera

	rec, llamado := ejecutaAny(t, fake, requestConTenant("t1"), lasCuatro...)
	if !llamado || rec.Code != http.StatusOK {
		t.Fatalf("code=%d llamado=%v; con survey encendida el gate tiene que dejar pasar; body=%s",
			rec.Code, llamado, rec.Body.String())
	}
}

// TestRequireAnyFeature_NingunaCorta: sin ninguna de las cuatro, 403 con la lista
// de las que habrían valido.
func TestRequireAnyFeature_NingunaCorta(t *testing.T) {
	rec, llamado := ejecutaAny(t, NewFake(), requestConTenant("t1"), lasCuatro...)
	exigeDenegadoAny(t, rec, llamado, lasCuatro)
}

// TestRequireAnyFeature_OverrideApagado: una feature apagada EXPLÍCITAMENTE
// (override enabled=false, ADR-0022) no cuenta como tenerla, ni siquiera para
// «alguna».
func TestRequireAnyFeature_OverrideApagado(t *testing.T) {
	fake := NewFake()
	for _, f := range lasCuatro {
		fake.Disable("t1", f)
	}
	rec, llamado := ejecutaAny(t, fake, requestConTenant("t1"), lasCuatro...)
	exigeDenegadoAny(t, rec, llamado, lasCuatro)
}

// TestRequireAnyFeature_ResolverCaido_FailClosed: un fallo de infraestructura corta
// con 403, NO con 500 y NO dejando pasar.
//
// Y corta EN EL ACTO, sin seguir preguntando por las demás: si el error se tragara
// para probar la siguiente clave, un resolver caído se leería como «no tiene
// ninguna» unas veces y como «tiene» otras, según qué clave respondiera antes de
// caerse. Un gate cuyo resultado depende del orden de los fallos no es un gate.
func TestRequireAnyFeature_ResolverCaido_FailClosed(t *testing.T) {
	roto := NewFake()
	roto.Enable("t1", FeatureSurvey) // la tendría, pero el resolver no puede decirlo
	roto.Err = errors.New("la BD de entitlements se cayó")

	rec, llamado := ejecutaAny(t, roto, requestConTenant("t1"), lasCuatro...)
	exigeDenegadoAny(t, rec, llamado, lasCuatro)
}

// TestRequireAnyFeature_SinIdentidad_FailClosed: sin Identity (o sin tenant en
// ella) no hay a quién preguntarle, así que no se abre.
func TestRequireAnyFeature_SinIdentidad_FailClosed(t *testing.T) {
	fake := NewFake()
	fake.Enable("t1", FeatureSurvey)

	rec, llamado := ejecutaAny(t, fake, requestConTenant(""), lasCuatro...)
	exigeDenegadoAny(t, rec, llamado, lasCuatro)
}

// TestRequireAnyFeature_ResolverNil_FailClosed: un cableado a medias tampoco abre.
func TestRequireAnyFeature_ResolverNil_FailClosed(t *testing.T) {
	rec, llamado := ejecutaAny(t, nil, requestConTenant("t1"), lasCuatro...)
	exigeDenegadoAny(t, rec, llamado, lasCuatro)
}

// TestRequireAnyFeature_SinClavesNoAbre: una lista VACÍA es «ninguna basta», no
// «todas valen».
//
// Es el descuido con peor pinta de todos —`RequireAnyFeature(resolver)` compila
// perfectamente— y el único de esta familia que dejaría una ruta de pago abierta a
// cualquiera en vez de cerrada de más.
func TestRequireAnyFeature_SinClavesNoAbre(t *testing.T) {
	fake := NewFake()
	fake.Enable("t1", FeatureSurvey)

	rec, llamado := ejecutaAny(t, fake, requestConTenant("t1"))
	exigeDenegadoAny(t, rec, llamado, nil)
}

// resolverQueFallaEn responde bien salvo para UNA clave, que revienta.
type resolverQueFallaEn struct {
	*Fake
	rota string
}

func (r *resolverQueFallaEn) Has(ctx context.Context, tenantID, feature string) (bool, error) {
	if feature == r.rota {
		return false, errors.New("esta clave no se pudo resolver")
	}
	return r.Fake.Has(ctx, tenantID, feature)
}

// TestRequireAnyFeature_UnFalloNoSeTRAGA es el test que fija por qué el bucle corta
// en el error y no sigue probando: el tenant SÍ tiene la segunda clave, pero la
// primera no se pudo resolver.
//
// Con el corte, 403. Con un `continue` —que parece inofensivo y hasta generoso—,
// 200: y entonces el mismo tenant, con la misma BD medio caída, entraría o no según
// qué clave fallara primero. La política tiene que ser una, no un sorteo.
func TestRequireAnyFeature_UnFalloNoSeTRAGA(t *testing.T) {
	fake := NewFake()
	fake.Enable("t1", FeatureMedia) // la SEGUNDA de lasCuatro: la buena viene después
	feats := &resolverQueFallaEn{Fake: fake, rota: FeatureCartBasic}

	rec, llamado := ejecutaAny(t, feats, requestConTenant("t1"), lasCuatro...)
	exigeDenegadoAny(t, rec, llamado, lasCuatro)
}
