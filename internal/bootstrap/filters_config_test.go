package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
	gatewaygrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/grpc"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intentcfg"
)

// providerFijo devuelve las configs que se le dan (o un error).
type providerFijo struct {
	cfgs []gatewaygrpc.ConfigPayload
	err  error
}

func (p providerFijo) ConfigsForConnect(context.Context, string) ([]gatewaygrpc.ConfigPayload, error) {
	return p.cfgs, p.err
}

// fuentePerfiles es un filtercfg.Source de prueba. Devuelve la MISMA foto para
// cualquier tenant a propósito: así, si en el test de la cadena real un tenant se
// queda sin `filters`, no puede ser porque la fuente le diera menos — solo porque
// alguien lo gateó.
type fuentePerfiles struct {
	tp  fleet.TenantProfiles
	err error
}

func (f fuentePerfiles) ProfilesByTenant(context.Context, string) (fleet.TenantProfiles, error) {
	return f.tp, f.err
}

// kindsDe extrae los kinds en orden, que es lo que se afirma en casi todos los tests
// de aquí (el contenido de cada payload lo cubren los tests de su propio paquete).
func kindsDe(cfgs []gatewaygrpc.ConfigPayload) []string {
	out := make([]string, 0, len(cfgs))
	for _, c := range cfgs {
		out = append(out, c.Kind)
	}
	return out
}

func mismosKinds(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// La CADENA REAL — criterio (d) del Plan 046 · T2.1
// ---------------------------------------------------------------------------

// TestBuildConfigProvider_TresKindsConLlmIntent_YDosSinElla ejerce la cadena que Run()
// cablea DE VERDAD —buildConfigProvider, o sea jwks{next: chain{intents, filters}}—
// contra dos tenants que se diferencian en UNA sola cosa: tener o no la feature
// llm_intent. Los dos tienen config de intents persistida y los dos ven la MISMA foto
// de perfiles, así que cualquier diferencia que no sea el kind "intents" es un bug.
//
// 🔴 POR QUÉ ESTE TEST EXISTE, Y POR QUÉ NO BASTABA EL QUE HABÍA. El test previo del
// criterio (d) vivía en internal/gateway/grpc y le daba al Gateway un fakeProvider con
// la lista de 3 o de 2 configs YA ESCRITA A MANO. Ese test prueba que el Gateway
// entrega lo que le den —que está bien y sigue vivo— pero NO prueba nada sobre quién
// aporta qué: gatear `filters` por entitlement, que es justo la regla 1 de T2.1 y la
// que el plan más protege, lo habría dejado en VERDE.
//
// La MUTACIÓN que este test detecta: añadirle a filtersConfigProvider un campo `ents`
// y un `if has, _ := p.ents.Has(...); !has { return nil, nil }`. El tenant sin la
// feature perdería su mapa de filtros y subiría a la nube el tráfico de sus sesiones
// pasivas — el fallo exacto que el Plan 046 viene a cerrar.
func TestBuildConfigProvider_TresKindsConLlmIntent_YDosSinElla(t *testing.T) {
	ctx := context.Background()
	const (
		tenantConFeature = "t-con-llm-intent"
		tenantSinFeature = "t-sin-llm-intent"
	)

	jwks := gatewaygrpc.ConfigPayload{
		Kind: "jwks", Version: "kid-1", Payload: []byte(`{"keys":[]}`),
	}

	// LOS DOS tenants tienen config de intents persistida: si uno no la tuviera, el
	// test pasaría por el motivo equivocado (intentsConfigProvider corta también por
	// «sin config», no solo por «sin feature»).
	intents := intentcfg.NewMemoryStore()
	for _, tid := range []string{tenantConFeature, tenantSinFeature} {
		if err := intents.Upsert(ctx, tid, "v-intents-1", []byte(`{"version":"v1"}`)); err != nil {
			t.Fatalf("sembrar intents de %s: %v", tid, err)
		}
	}

	// La ÚNICA diferencia entre los dos tenants.
	ents := entitlements.NewFake()
	ents.Enable(tenantConFeature, entitlements.FeatureLLMIntent)

	profiles := fuentePerfiles{tp: fleet.TenantProfiles{
		Version:  1_700_000_000_123_456,
		Sessions: map[string]fleet.Profile{"sess-1": fleet.ProfileActive, "sess-2": fleet.ProfilePassive},
	}}

	provider := buildConfigProvider(jwks, intents, ents, profiles, nil)

	casos := []struct {
		nombre string
		tenant string
		kinds  []string
	}{
		{"con llm_intent ⇒ jwks + intents + filters", tenantConFeature, []string{"jwks", "intents", "filters"}},
		{"sin llm_intent ⇒ jwks + filters (filters NO se gatea)", tenantSinFeature, []string{"jwks", "filters"}},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			got, err := provider.ConfigsForConnect(ctx, c.tenant)
			if err != nil {
				t.Fatalf("ConfigsForConnect: %v", err)
			}
			if !mismosKinds(kindsDe(got), c.kinds) {
				t.Fatalf("kinds = %v, quiero %v (en ESTE orden)", kindsDe(got), c.kinds)
			}
			// El payload de filters tiene que ser el de verdad, no un hueco: el mapa
			// completo del tenant con su versión. Un eslabón que entregara el kind
			// vacío pasaría la aserción de arriba y no serviría de nada.
			var payload struct {
				Version  int64 `json:"version"`
				Sessions map[string]struct {
					Profile string `json:"profile"`
				} `json:"sessions"`
			}
			for _, cfg := range got {
				if cfg.Kind != "filters" {
					continue
				}
				if err := json.Unmarshal(cfg.Payload, &payload); err != nil {
					t.Fatalf("payload de filters no deserializa al contrato D-046.2: %v", err)
				}
			}
			if payload.Version != profiles.tp.Version || len(payload.Sessions) != 2 {
				t.Fatalf("filters llegó vacío o recortado: %+v", payload)
			}
			if payload.Sessions["sess-2"].Profile != "passive" {
				t.Fatalf("el perfil pasivo no viajó: %+v", payload.Sessions)
			}
		})
	}
}

// TestBuildConfigProvider_FiltersSobreviveAlFalloDeIntents_YViceversa es el
// best-effort por eslabón visto desde la cadena REAL: un tenant cuyo resolver de
// entitlements está roto (Neon con hipo) pierde `intents` —el eslabón que consulta la
// feature— y CONSERVA jwks y filters.
//
// El fallo que evita: hasta la corrección del 2026-08-21 el error del primer eslabón
// abortaba la cadena entera y el Edge se quedaba SIN LOS TRES kinds, incluido el jwks,
// que ni siquiera hace I/O. Con T2.1 hay una query más a Neon en cada connect, así que
// la probabilidad de ese hipo sube justo ahora.
func TestBuildConfigProvider_FiltersSobreviveAlFalloDeIntents_YViceversa(t *testing.T) {
	jwks := gatewaygrpc.ConfigPayload{Kind: "jwks", Version: "kid-1", Payload: []byte(`{"keys":[]}`)}
	boom := errors.New("neon con hipo")

	t.Run("cae intents", func(t *testing.T) {
		ents := entitlements.NewFake()
		ents.Err = boom // Has() falla ⇒ intentsConfigProvider devuelve error.
		provider := buildConfigProvider(jwks, intentcfg.NewMemoryStore(), ents,
			fuentePerfiles{tp: fleet.TenantProfiles{Version: 7, Sessions: map[string]fleet.Profile{}}}, nil)

		got, err := provider.ConfigsForConnect(context.Background(), "t1")
		if err != nil {
			t.Fatalf("la cadena NO puede propagar el fallo de un eslabón: %v", err)
		}
		if !mismosKinds(kindsDe(got), []string{"jwks", "filters"}) {
			t.Fatalf("kinds = %v, quiero [jwks filters]: el fallo de intents no puede "+
				"llevarse por delante los otros dos", kindsDe(got))
		}
	})

	t.Run("cae filters", func(t *testing.T) {
		ents := entitlements.NewFake()
		ents.Enable("t1", entitlements.FeatureLLMIntent)
		intents := intentcfg.NewMemoryStore()
		if err := intents.Upsert(context.Background(), "t1", "v1", []byte(`{"version":"v1"}`)); err != nil {
			t.Fatalf("sembrar intents: %v", err)
		}
		provider := buildConfigProvider(jwks, intents, ents, fuentePerfiles{err: boom}, nil)

		got, err := provider.ConfigsForConnect(context.Background(), "t1")
		if err != nil {
			t.Fatalf("la cadena NO puede propagar el fallo de un eslabón: %v", err)
		}
		if !mismosKinds(kindsDe(got), []string{"jwks", "intents"}) {
			t.Fatalf("kinds = %v, quiero [jwks intents]", kindsDe(got))
		}
	})
}

// ---------------------------------------------------------------------------
// chainConfigProvider — la mecánica, con dobles
// ---------------------------------------------------------------------------

// TestChainConfigProvider_NoSeComeElEslabonSiguiente es la razón de existir de
// chainConfigProvider, y el bug que evita: intentsConfigProvider corta con
// `return nil, nil` cuando el tenant no tiene llm_intent. Colgarle el provider de
// filters por su campo `next` habría dejado SIN filtros a todo tenant sin la feature
// —en silencio, y con los tests de cada provider por separado en verde—.
func TestChainConfigProvider_NoSeComeElEslabonSiguiente(t *testing.T) {
	c := chainConfigProvider{links: []chainLink{
		{kind: "intents", provider: providerFijo{cfgs: nil}}, // el que "no aplica"
		{kind: "filters", provider: providerFijo{cfgs: []gatewaygrpc.ConfigPayload{{Kind: "filters", Version: "7"}}}},
	}}
	got, err := c.ConfigsForConnect(context.Background(), "t1")
	if err != nil {
		t.Fatalf("ConfigsForConnect: %v", err)
	}
	if len(got) != 1 || got[0].Kind != "filters" {
		t.Fatalf("configs = %+v, quiero solo filters: un eslabón que devuelve nil no puede "+
			"cortar la cadena", got)
	}
}

// TestChainConfigProvider_ConservaElOrdenYSaltaLosNil.
func TestChainConfigProvider_ConservaElOrdenYSaltaLosNil(t *testing.T) {
	c := chainConfigProvider{links: []chainLink{
		{kind: "intents", provider: providerFijo{cfgs: []gatewaygrpc.ConfigPayload{{Kind: "intents"}}}},
		{kind: "fantasma", provider: nil},
		{kind: "filters", provider: providerFijo{cfgs: []gatewaygrpc.ConfigPayload{{Kind: "filters"}}}},
	}}
	got, err := c.ConfigsForConnect(context.Background(), "t1")
	if err != nil {
		t.Fatalf("ConfigsForConnect: %v", err)
	}
	if !mismosKinds(kindsDe(got), []string{"intents", "filters"}) {
		t.Fatalf("kinds = %v, quiero [intents filters]", kindsDe(got))
	}
}

// TestChainConfigProvider_UnEslabonRotoNoSeLlevaALosDemas: BEST-EFFORT POR ESLABÓN.
//
// ⚠️ Este test AFIRMA LO CONTRARIO de lo que afirmaba su antecesor
// (TestChainConfigProvider_ErrorAbortaLaCadena), y el cambio es deliberado. Aquel
// defendía «media config es peor que ninguna: el Edge conserva su last-known-good
// COMPLETO». Es falso en los dos extremos: los kinds son INDEPENDIENTES en el Edge
// (cada uno con su versión y su persistencia), así que no hay un «completo» que
// conservar; y el modo de fallo real era el inverso — un hipo de Neon dejaba al Edge
// sin jwks, sin intents y sin filters de golpe, con un solo Error en el log.
func TestChainConfigProvider_UnEslabonRotoNoSeLlevaALosDemas(t *testing.T) {
	c := chainConfigProvider{links: []chainLink{
		{kind: "intents", provider: providerFijo{cfgs: []gatewaygrpc.ConfigPayload{{Kind: "intents"}}}},
		{kind: "roto", provider: providerFijo{err: errors.New("bd caída")}},
		{kind: "filters", provider: providerFijo{cfgs: []gatewaygrpc.ConfigPayload{{Kind: "filters"}}}},
	}}
	got, err := c.ConfigsForConnect(context.Background(), "t1")
	if err != nil {
		t.Fatalf("err = %v, quiero nil: un eslabón roto se LOGUEA, no se propaga", err)
	}
	if !mismosKinds(kindsDe(got), []string{"intents", "filters"}) {
		t.Fatalf("kinds = %v, quiero [intents filters]: el eslabón de en medio falló, "+
			"los otros dos tienen que llegar igual", kindsDe(got))
	}
}

// TestJwksConfigProvider_ConservaSuJwksAunqueFalleElResto es el otro medio del mismo
// arreglo, y el más caro de los dos: el jwks se calcula AL ARRANCAR y no hace I/O, o
// sea que NO PUEDE fallar. Propagarlo hacia arriba por el fallo de otro era regalar la
// única config que estaba garantizada — y sin jwks el Edge no verifica offline los
// access tokens del operador (ADR-0025).
func TestJwksConfigProvider_ConservaSuJwksAunqueFalleElResto(t *testing.T) {
	jwks := gatewaygrpc.ConfigPayload{Kind: "jwks", Version: "kid-1", Payload: []byte(`{"keys":[]}`)}
	p := jwksConfigProvider{jwks: jwks, next: providerFijo{err: errors.New("bd caída")}}

	got, err := p.ConfigsForConnect(context.Background(), "t1")
	if err != nil {
		t.Fatalf("err = %v, quiero nil", err)
	}
	if len(got) != 1 || got[0].Kind != "jwks" || got[0].Version != "kid-1" {
		t.Fatalf("configs = %+v, quiero el jwks intacto", got)
	}
}

// ---------------------------------------------------------------------------
// filtersConfigProvider — el eslabón, aislado
// ---------------------------------------------------------------------------

// TestFiltersConfigProvider_SiempreEntregaConfig_SinGateDeEntitlement es la regla 1 +
// la regla 2 de T2.1 vistas desde el eslabón real que se cablea en Run():
//
//   - un tenant SIN ninguna sesión pasiva recibe igualmente su frame (el mapa
//     todo-active es lo que hace converger al Edge cuando una sesión se reactiva);
//   - un tenant sin NINGUNA sesión también (mapa vacío, version 0);
//   - y en ningún caso se consulta una feature: el struct no tiene ni campo para ello.
func TestFiltersConfigProvider_SiempreEntregaConfig_SinGateDeEntitlement(t *testing.T) {
	casos := map[string]fleet.TenantProfiles{
		"sin ninguna pasiva": {Version: 99, Sessions: map[string]fleet.Profile{
			"sess-1": fleet.ProfileActive, "sess-2": fleet.ProfileActive,
		}},
		"sin ninguna sesión": {Version: 0, Sessions: map[string]fleet.Profile{}},
	}
	for nombre, tp := range casos {
		t.Run(nombre, func(t *testing.T) {
			p := filtersConfigProvider{src: fuentePerfiles{tp: tp}}
			got, err := p.ConfigsForConnect(context.Background(), "t1")
			if err != nil {
				t.Fatalf("ConfigsForConnect: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("%d configs, quiero 1 SIEMPRE (devolver nil dejaría al Edge con el "+
					"mapa anterior y una sesión reactivada seguiría muda)", len(got))
			}
			if got[0].Kind != "filters" {
				t.Fatalf("kind = %q, quiero \"filters\"", got[0].Kind)
			}
			var payload struct {
				Version  int64 `json:"version"`
				Sessions map[string]struct {
					Profile string `json:"profile"`
				} `json:"sessions"`
			}
			if err := json.Unmarshal(got[0].Payload, &payload); err != nil {
				t.Fatalf("payload no deserializa al contrato D-046.2: %v", err)
			}
			if len(payload.Sessions) != len(tp.Sessions) {
				t.Fatalf("mapa con %d sesiones, quiero %d", len(payload.Sessions), len(tp.Sessions))
			}
			if payload.Version != tp.Version {
				t.Fatalf("version del payload = %d, quiero %d", payload.Version, tp.Version)
			}
		})
	}
}

// TestFiltersConfigProvider_ErrorSePropagaAlLlamante: un fallo de lectura no se
// traduce en «no hay filtros» (que el Edge leería como «todas activas»: fail-open). El
// eslabón lo devuelve, y quien decide qué hacer con él es la cadena, que lo loguea con
// su kind y sigue. El Edge conserva el mapa de filtros que ya tenía.
func TestFiltersConfigProvider_ErrorSePropagaAlLlamante(t *testing.T) {
	boom := errors.New("bd caída")
	p := filtersConfigProvider{src: fuentePerfiles{err: boom}}
	got, err := p.ConfigsForConnect(context.Background(), "t1")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, quiero que envuelva %v", err, boom)
	}
	if got != nil {
		t.Fatalf("configs = %+v con la lectura rota, quiero nil", got)
	}
}
