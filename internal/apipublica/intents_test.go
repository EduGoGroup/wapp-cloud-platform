package apipublica_test

// intents_test.go — cubre el contrato de intents.go (IntentConfigStore, ConfigPusher,
// IntentsDeps, MountIntents): los dobles, el montaje, la cadena y E1. E2 va en
// intents_put_test.go (05 E-13).

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/apipublicahelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements/entitlementshelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intentcfg"
)

const (
	intentsTarget     = "/api/v1/intents"
	intentsPatternGet = "GET /api/v1/intents"
	intentsPatternPut = "PUT /api/v1/intents"
	intentsReadPerm   = "intents.read"
	intentsWritePerm  = "intents.write"
	intentsResource   = "intents"

	intentsMsgTimeout   = "la lectura de la config de intents no respondió a tiempo, reintenta"
	intentsMsgNotFound  = "el tenant no tiene config de intents"
	intentsMsgReadFail  = "no se pudo leer la config de intents"
	intentsMsgGateFail  = "no se pudo verificar el entitlement"
	intentsMsgNoFeature = "el plan del tenant no incluye la clasificación de intenciones"
	intentsMsgBodyRead  = "no se pudo leer el cuerpo"
	intentsMsgInvalid   = "config de intents inválida: "
	intentsMsgSaveFail  = "no se pudo persistir la config de intents"
	intentsMsgPushWarn  = "intents: push de config best-effort falló (persistida; reconcilia al conectar)"

	// intentsValidBody es un contrato de intenciones válido para wapp-shared/intents.
	intentsValidBody = `{"version":"v1","umbral_confianza":0.7,"intents":[{"name":"pedir_pizza","descripcion":"pedir comida","params":["cantidad"],"ejemplos":[{"mensaje":"quiero una pizza"}]}]}`
	// intentsBodyWithEventKind es el MISMO contrato más un `event_kind` por intent (Plan 043).
	intentsBodyWithEventKind = `{"version":"v1","umbral_confianza":0.7,"intents":[{"name":"pedir_pizza","descripcion":"pedir comida","params":["cantidad"],"ejemplos":[{"mensaje":"quiero una pizza"}],"event_kind":"cart"}]}`
)

// Los puertos de E1–E2 los cumplen las piezas REALES del módulo captación nuevo.
var (
	_ apipublica.IntentConfigStore = (*intentcfg.PostgresStore)(nil)
	_ apipublica.IntentConfigStore = (*intentcfg.MemoryStore)(nil)
	_ apipublica.IntentConfigStore = intentcfg.Store(nil)
	_ apipublica.ConfigPusher      = (*intentsPusherSpy)(nil)
)

// intentsStoreSpy es IntentConfigStore sobre el doble del módulo: delega en él y apunta cuántas
// veces se llamó cada método, con qué tenant y con qué plazo (-1 = sin plazo). Puede fallar a
// la carta (getErr, upsertErr) o no contestar (block: espera a que el contexto muera, sin
// time.Sleep).
type intentsStoreSpy struct {
	*intentcfg.MemoryStore
	getErr    error
	upsertErr error
	block     bool

	gets, upserts   int
	tenant          string
	getRemaining    time.Duration
	upsertRemaining time.Duration
}

var _ apipublica.IntentConfigStore = (*intentsStoreSpy)(nil)

func newIntentsStore() *intentsStoreSpy {
	return &intentsStoreSpy{MemoryStore: intentcfg.NewMemoryStore()}
}

func (s *intentsStoreSpy) Get(ctx context.Context, tenantID string) (intentcfg.Config, error) {
	s.gets++
	s.tenant, s.getRemaining = tenantID, tenantVarRemaining(ctx)
	if s.block {
		<-ctx.Done()
		return intentcfg.Config{}, ctx.Err()
	}
	if s.getErr != nil {
		return intentcfg.Config{}, s.getErr
	}
	return s.MemoryStore.Get(ctx, tenantID)
}

func (s *intentsStoreSpy) Upsert(ctx context.Context, tenantID, version string, blob []byte) error {
	s.upserts++
	s.tenant, s.upsertRemaining = tenantID, tenantVarRemaining(ctx)
	if s.upsertErr != nil {
		return s.upsertErr
	}
	return s.MemoryStore.Upsert(ctx, tenantID, version, blob)
}

// intentsPusherSpy es ConfigPusher: apunta la llamada (y su contexto) y falla si se le pide.
type intentsPusherSpy struct {
	err error

	calls                 int
	tenant, kind, version string
	payload               []byte
	ctx                   context.Context //nolint:containedctx // el test mira el contexto que recibió el push
}

func (p *intentsPusherSpy) PushConfig(ctx context.Context, tenantID, kind, version string, payload []byte) error {
	p.calls++
	p.ctx = ctx
	p.tenant, p.kind, p.version = tenantID, kind, version
	p.payload = append([]byte(nil), payload...)
	return p.err
}

// intentsCara monta E1–E2 con k y d.
func intentsCara(k apipublica.Common, d apipublica.IntentsDeps) *apipublica.Cara {
	c := apipublica.Nueva()
	apipublica.MountIntents(c, k, d)
	return c
}

// intentsRig es el montaje habitual: arnés, almacén espía, pusher espía y la cara, con
// `llm_intent` encendida para tenantA y tenantB.
type intentsRig struct {
	h      *apipublicahelpertest.Harness
	store  *intentsStoreSpy
	pusher *intentsPusherSpy
	cara   *apipublica.Cara
}

// newIntentsRig monta E1–E2 con el resolver dado (nil ⇒ el de `llm_intent` encendida).
func newIntentsRig(t *testing.T, resolver entitlements.Resolver) intentsRig {
	t.Helper()
	if resolver == nil {
		resolver = withFeatures(entitlements.FeatureLLMIntent)
	}
	rig := intentsRig{h: apipublicahelpertest.New(t), store: newIntentsStore(), pusher: &intentsPusherSpy{}}
	rig.cara = intentsCara(rig.h.Common(), apipublica.IntentsDeps{
		Intents: rig.store, Entitlements: resolver, ConfigPush: rig.pusher,
	})
	return rig
}

// get hace el GET de E1 como tenantA con el permiso de lectura.
func (rig intentsRig) get() *httptest.ResponseRecorder {
	return rig.h.Call(rig.cara, rig.h.With(tenantA, intentsReadPerm), http.MethodGet, intentsTarget, "")
}

// put hace el PUT de E2 como tenantA con el permiso de escritura.
func (rig intentsRig) put(body string) *httptest.ResponseRecorder {
	return rig.h.Call(rig.cara, rig.h.With(tenantA, intentsWritePerm), http.MethodPut, intentsTarget, body)
}

// intentsSeed siembra la config de un tenant por el doble, sin pasar por la cara.
func intentsSeed(t *testing.T, store *intentsStoreSpy, tenant, version, blob string) {
	t.Helper()
	if err := store.MemoryStore.Upsert(context.Background(), tenant, version, []byte(blob)); err != nil {
		t.Fatalf("sembrando la config de intents de %s: %v", tenant, err)
	}
}

// intentsStored lee lo guardado de un tenant por el doble; ok=false si no tiene config.
func intentsStored(t *testing.T, store *intentsStoreSpy, tenant string) (intentcfg.Config, bool) {
	t.Helper()
	cfg, err := store.MemoryStore.Get(context.Background(), tenant)
	if errors.Is(err, intentcfg.ErrNotFound) {
		return intentcfg.Config{}, false
	}
	if err != nil {
		t.Fatalf("leyendo la config de intents de %s: %v", tenant, err)
	}
	return cfg, true
}

func TestMountIntents_Chain(t *testing.T) {
	rig := newIntentsRig(t, nil)
	intentsSeed(t, rig.store, tenantA, "v-seed", intentsValidBody)
	wantPatterns(t, "E1–E2", rig.cara, []string{intentsPatternGet, intentsPatternPut})
	checkChain(t, rig.h, rig.cara, routeCase{id: "E1", method: http.MethodGet, target: intentsTarget,
		perm: intentsReadPerm, want: http.StatusOK})
	checkChain(t, rig.h, rig.cara, routeCase{id: "E2", method: http.MethodPut, target: intentsTarget, body: intentsValidBody,
		perm: intentsWritePerm, resource: intentsResource, want: http.StatusOK})
}

// TestMountIntents_BothRoutesOrNone: sin el almacén o sin el resolver no existe NINGUNA de las
// dos rutas (404 y no 405, y no 500); el pusher no cuenta para el montaje.
func TestMountIntents_BothRoutesOrNone(t *testing.T) {
	resolver := withFeatures(entitlements.FeatureLLMIntent)
	for name, d := range map[string]apipublica.IntentsDeps{
		"without_store":    {Entitlements: resolver, ConfigPush: &intentsPusherSpy{}},
		"without_resolver": {Intents: newIntentsStore(), ConfigPush: &intentsPusherSpy{}},
		"without_both":     {ConfigPush: &intentsPusherSpy{}, DBTimeout: time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			cara := intentsCara(h.Common(), d)
			wantPatterns(t, name, cara, nil)
			for _, method := range []string{http.MethodGet, http.MethodPut} {
				rec := h.Call(cara, h.With(tenantA, "intents.*"), method, intentsTarget, intentsValidBody)
				wantCode(t, method+" "+name, rec, http.StatusNotFound)
			}
		})
	}
	t.Run("pusher_does_not_condition_the_mount", func(t *testing.T) {
		h := apipublicahelpertest.New(t)
		cara := intentsCara(h.Common(), apipublica.IntentsDeps{Intents: newIntentsStore(), Entitlements: resolver})
		wantPatterns(t, "sin pusher", cara, []string{intentsPatternGet, intentsPatternPut})
	})
}

func TestMountIntents_NilMWPanicsAtMount(t *testing.T) {
	v := recuperar(func() {
		apipublica.MountIntents(apipublica.Nueva(), apipublica.Common{}, apipublica.IntentsDeps{
			Intents: newIntentsStore(), Entitlements: withFeatures(entitlements.FeatureLLMIntent),
		})
	})
	if v == nil || esPendiente(v) || !strings.Contains(fmt.Sprint(v), "MountIntents") {
		t.Errorf("MountIntents con MW nil: panic = %v; quiero un panic de cableado que nombre MountIntents", v)
	}
	// Sin las dependencias no hay ruta que encadenar: MW nil no es un fallo.
	if v := recuperar(func() {
		apipublica.MountIntents(apipublica.Nueva(), apipublica.Common{}, apipublica.IntentsDeps{})
	}); v != nil {
		t.Errorf("MountIntents sin dependencias y con MW nil: panic = %v; quiero que no monte nada y no falle", v)
	}
}

// TestMountIntents_GetBody: la version guardada y el blob como JSON crudo (solo compactado).
func TestMountIntents_GetBody(t *testing.T) {
	rig := newIntentsRig(t, nil)
	intentsSeed(t, rig.store, tenantA, "v-seed", `{ "version": "v1", "intents": [ ], "z": 1, "a": null }`)
	rec := rig.get()
	wantCode(t, "E1", rec, http.StatusOK)
	wantExactBody(t, "E1", rec, `{"version":"v-seed","config":{"version":"v1","intents":[],"z":1,"a":null}}`)
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("E1: Content-Type %q, quiero application/json", ct)
	}
	if rig.pusher.calls != 0 || rig.store.upserts != 0 {
		t.Errorf("E1 escribió o empujó (upserts %d, pushes %d); una lectura no hace ninguna de las dos", rig.store.upserts, rig.pusher.calls)
	}
}

// TestMountIntents_GetHasNoFeatureGate: el gate `llm_intent` es solo de E2; E1 lee sin la
// feature y también con el resolver caído.
func TestMountIntents_GetHasNoFeatureGate(t *testing.T) {
	for name, resolver := range map[string]entitlements.Resolver{
		"tenant_without_feature": entitlementshelpertest.NewFake(),
		"resolver_that_fails":    &entitlementshelpertest.Fake{Err: errors.New("resolver caído")},
	} {
		t.Run(name, func(t *testing.T) {
			rig := newIntentsRig(t, resolver)
			intentsSeed(t, rig.store, tenantA, "v-seed", intentsValidBody)
			wantCode(t, name, rig.get(), http.StatusOK)
		})
	}
}

// TestMountIntents_GetNotFound: sin config, 404; también cuando el puerto envuelve el centinela
// (lo que hace el Postgres, que le añade el tenant).
func TestMountIntents_GetNotFound(t *testing.T) {
	t.Run("bare_sentinel", func(t *testing.T) {
		rig := newIntentsRig(t, nil)
		rec := rig.get()
		wantCode(t, "sin config", rec, http.StatusNotFound)
		wantErrorBody(t, "sin config", rec, intentsMsgNotFound)
	})
	t.Run("wrapped_sentinel", func(t *testing.T) {
		rig := newIntentsRig(t, nil)
		rig.store.getErr = fmt.Errorf("%w: tenant=%s", intentcfg.ErrNotFound, tenantA)
		rec := rig.get()
		wantCode(t, "centinela envuelto", rec, http.StatusNotFound)
		wantErrorBody(t, "centinela envuelto", rec, intentsMsgNotFound)
	})
}

// TestMountIntents_GetStoreErrorIs500: el 500 no repite el error del puerto.
func TestMountIntents_GetStoreErrorIs500(t *testing.T) {
	rig := newIntentsRig(t, nil)
	rig.store.getErr = errors.New("postgres://usuario:ficticio@host/bd: conexión rechazada")
	rec := rig.get()
	wantCode(t, "E1 con el puerto caído", rec, http.StatusInternalServerError)
	wantErrorBody(t, "E1 con el puerto caído", rec, intentsMsgReadFail)
}

// TestMountIntents_GetDBTimeout: la lectura de E1 va acotada, y el plazo vencido es un 504.
func TestMountIntents_GetDBTimeout(t *testing.T) {
	resolver := withFeatures(entitlements.FeatureLLMIntent)
	for name, tc := range map[string]struct{ wired, floor, ceil time.Duration }{
		"zero_falls_to_1500ms":     {0, time.Second, 1500 * time.Millisecond},
		"negative_falls_to_1500ms": {-time.Second, time.Second, 1500 * time.Millisecond},
		"wired_timeout":            {5 * time.Second, 4 * time.Second, 5 * time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			h := apipublicahelpertest.New(t)
			store := newIntentsStore()
			intentsSeed(t, store, tenantA, "v-seed", intentsValidBody)
			cara := intentsCara(h.Common(), apipublica.IntentsDeps{Intents: store, Entitlements: resolver, DBTimeout: tc.wired})
			wantCode(t, name, h.Call(cara, h.With(tenantA, intentsReadPerm), http.MethodGet, intentsTarget, ""), http.StatusOK)
			if store.getRemaining <= tc.floor || store.getRemaining > tc.ceil {
				t.Errorf("al contexto de Get le quedaban %s, quiero entre %s y %s (-1 = sin plazo)", store.getRemaining, tc.floor, tc.ceil)
			}
		})
	}
	t.Run("store_that_does_not_answer_is_504", func(t *testing.T) {
		h := apipublicahelpertest.New(t)
		store := newIntentsStore()
		store.block = true
		cara := intentsCara(h.Common(), apipublica.IntentsDeps{Intents: store, Entitlements: resolver, DBTimeout: 20 * time.Millisecond})
		rec := h.Call(cara, h.With(tenantA, intentsReadPerm), http.MethodGet, intentsTarget, "")
		wantCode(t, "plazo vencido", rec, http.StatusGatewayTimeout)
		wantErrorBody(t, "plazo vencido", rec, intentsMsgTimeout)
		var warned bool
		for _, e := range h.Log().Entries() {
			if e.Level == "warn" && e.Msg == "lectura a BD vencida: se responde 504" {
				warned = e.Fields["op"] == "intents.get" && e.Fields["tenant_id"] == tenantA
			}
		}
		if !warned {
			t.Errorf("el 504 no dejó el Warn con op=intents.get y tenant_id: %+v", h.Log().Entries())
		}
	})
	t.Run("nil_log_still_answers_504", func(t *testing.T) {
		h := apipublicahelpertest.New(t)
		store := newIntentsStore()
		store.block = true
		cara := intentsCara(apipublica.Common{MW: h.MW()}, apipublica.IntentsDeps{Intents: store, Entitlements: resolver, DBTimeout: 20 * time.Millisecond})
		wantCode(t, "plazo vencido sin logger", h.Call(cara, h.With(tenantA, intentsReadPerm), http.MethodGet, intentsTarget, ""),
			http.StatusGatewayTimeout)
	})
	t.Run("deadline_is_checked_before_not_found", func(t *testing.T) {
		rig := newIntentsRig(t, nil)
		rig.store.getErr = fmt.Errorf("%w: %w", intentcfg.ErrNotFound, context.DeadlineExceeded)
		wantCode(t, "plazo y centinela a la vez", rig.get(), http.StatusGatewayTimeout)
	})
}

// TestMountIntents_TenantComesFromTheToken (INV-8): cada tenant lee y escribe SOLO lo suyo; ni
// la query ni un campo del cuerpo cambian de quién es la operación.
func TestMountIntents_TenantComesFromTheToken(t *testing.T) {
	rig := newIntentsRig(t, nil)
	intentsSeed(t, rig.store, tenantA, "v-de-a", `{"de":"a"}`)
	intentsSeed(t, rig.store, tenantB, "v-de-b", `{"de":"b"}`)

	rec := rig.h.Call(rig.cara, rig.h.With(tenantA, intentsReadPerm), http.MethodGet, intentsTarget+"?tenant_id="+tenantB, "")
	wantCode(t, "E1 de tenantA", rec, http.StatusOK)
	wantExactBody(t, "E1 de tenantA", rec, `{"version":"v-de-a","config":{"de":"a"}}`)

	body := strings.Replace(intentsValidBody, `{"version"`, `{"tenant_id":"`+tenantB+`","version"`, 1)
	rec = rig.h.Call(rig.cara, rig.h.With(tenantA, intentsWritePerm), http.MethodPut, intentsTarget+"?tenant_id="+tenantB, body)
	wantCode(t, "E2 de tenantA", rec, http.StatusOK)
	if rig.store.tenant != tenantA || rig.pusher.tenant != tenantA {
		t.Errorf("el puerto recibió el tenant %q y el pusher %q; quiero el del token %q en los dos", rig.store.tenant, rig.pusher.tenant, tenantA)
	}
	if got, _ := intentsStored(t, rig.store, tenantB); got.Version != "v-de-b" || string(got.Blob) != `{"de":"b"}` {
		t.Errorf("el PUT de tenantA tocó a tenantB: %+v", got)
	}
	if got, _ := intentsStored(t, rig.store, tenantA); string(got.Blob) != body {
		t.Errorf("tenantA quedó con %s, quiero el cuerpo que mandó", got.Blob)
	}
}
