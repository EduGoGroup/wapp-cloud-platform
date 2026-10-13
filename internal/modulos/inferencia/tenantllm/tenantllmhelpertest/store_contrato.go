package tenantllmhelpertest

// Los casos de Get, APIKey y Delete, el aislamiento por tenant (INV-7) y la concurrencia.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm"
)

// caseGetWithoutRow: un tenant sin fila no es un error ni un «desconocido»: found=false y
// Config cero. Esa ausencia ES una respuesta: el tenant está en la vía local por defecto.
func caseGetWithoutRow(t *testing.T, m Montaje) {
	tenant := seedTenant(t, m)
	cfg, found, err := m.Store.Get(context.Background(), tenant)
	if err != nil || found || cfg != (tenantllm.Config{}) {
		t.Errorf("Get(%s) = (%+v, found=%v, err=%v), quería (Config cero, false, nil)", tenant, cfg, found, err)
	}
	if row, found := m.Row(t, tenant); found {
		t.Errorf("un tenant recién sembrado tiene fila (%+v): el Montaje no vino limpio", row)
	}
}

// caseAPIKeyWithoutRow (R4.4.c): sin fila, ErrNotConfigured —el centinela, no nil con una clave
// vacía— para que quien llama no acabe llamando al proveedor sin credencial. Y pedirla no crea
// la fila.
func caseAPIKeyWithoutRow(t *testing.T, m Montaje) {
	tenant := seedTenant(t, m)
	requireNotConfigured(t, m, tenant)
	requireNoRow(t, m, tenant)
}

// caseDeleteRevokes: borrar se lleva la fila entera —credencial y consentimiento de una vez—,
// en las dos vías, y repetirlo no es un error.
func caseDeleteRevokes(t *testing.T, m Montaje) {
	for _, prior := range priorStates()[1:] { // «api row» y «local row»
		tenant := seedTenant(t, m)
		prior.setup(t, m, tenant)

		if err := m.Store.Delete(context.Background(), tenant); err != nil {
			t.Fatalf("Delete sobre «%s»: error inesperado %v", prior.name, err)
		}
		requireNoRow(t, m, tenant)

		if err := m.Store.Delete(context.Background(), tenant); err != nil {
			t.Errorf("segundo Delete sobre «%s»: error %v, quería nil (idempotente)", prior.name, err)
		}
		requireNoRow(t, m, tenant)
	}
}

// caseDeleteWithoutRow: borrar lo que no hay no es un error, y no crea nada.
func caseDeleteWithoutRow(t *testing.T, m Montaje) {
	tenant := seedTenant(t, m)
	if err := m.Store.Delete(context.Background(), tenant); err != nil {
		t.Errorf("Delete de un tenant sin fila: error %v, quería nil", err)
	}
	requireNoRow(t, m, tenant)
}

// caseRowsIsolatedByTenant (INV-7): cada tenant solo ve y solo toca lo suyo. Ni Get ni APIKey
// del ajeno ven la fila del primero; ni el Upsert ni el Delete del ajeno la cambian.
func caseRowsIsolatedByTenant(t *testing.T, m Montaje) {
	owner, other := seedTenant(t, m), seedTenant(t, m)
	if owner == other {
		t.Fatalf("Montaje.SeedTenant devolvió dos veces el mismo tenant, %s", owner)
	}
	upsert(t, m, apiConfig(owner), keyFirst, consentFirst)
	ownerState := captureState(t, m, owner)

	// El ajeno no ve nada: ni la configuración ni, sobre todo, la clave.
	requireNoRow(t, m, other)

	// El ajeno escribe lo suyo, con otra clave y otro modelo: el primero no se entera.
	second := tenantllm.Config{TenantID: other, Via: tenantllm.ViaAPI, Provider: tenantllm.ProviderGemini, Model: modelSecond}
	upsert(t, m, second, keyRotated, consentSecond)
	requireSameState(t, m, owner, ownerState)
	requireAPIKey(t, m, other, keyRotated)

	// El ajeno cambia de vía y luego borra: el primero sigue con su credencial.
	upsert(t, m, localConfig(other), "", time.Time{})
	requireSameState(t, m, owner, ownerState)
	if err := m.Store.Delete(context.Background(), other); err != nil {
		t.Fatalf("Delete(%s): error inesperado %v", other, err)
	}
	requireSameState(t, m, owner, ownerState)
	requireNoRow(t, m, other)
}

// caseConcurrentUpserts: escritores que alternan las dos vías sobre el MISMO tenant, con lectores
// en medio. Cada upsert es la foto entera, así que ni durante la carrera ni al final se ve una
// fila mezclada: o la vía api con su eje completo, y la clave y el modelo de UNA misma escritura,
// o la vía local sin nada.
func caseConcurrentUpserts(t *testing.T, m Montaje) {
	const writers, rounds = 8, 5
	tenant := seedTenant(t, m)
	ctx := context.Background()
	keyOf := func(w int) string { return fmt.Sprintf("%s-writer-%d", keyFirst, w) }
	modelOf := func(w int) string { return fmt.Sprintf("model-%d", w) }

	failures := make(chan error, 2*writers*rounds)
	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(2)
		go func() { // escritor: pares escriben la vía api, impares la local
			defer wg.Done()
			for range rounds {
				cfg, key, consent := localConfig(tenant), "", time.Time{}
				if w%2 == 0 {
					cfg, key, consent = apiConfig(tenant), keyOf(w), consentFirst
					cfg.Model = modelOf(w)
				}
				if err := m.Store.Upsert(ctx, cfg, key, consent); err != nil {
					failures <- fmt.Errorf("Upsert del escritor %d: %w", w, err)
				}
			}
		}()
		go func() { // lector: nunca ve una configuración a medias
			defer wg.Done()
			for range rounds {
				cfg, found, err := m.Store.Get(ctx, tenant)
				if err != nil {
					failures <- fmt.Errorf("Get en carrera: %w", err)
					continue
				}
				if found && !coherent(cfg) {
					failures <- fmt.Errorf("Get en carrera vio una configuración a medias: %+v", cfg)
				}
				if _, err := m.Store.APIKey(ctx, tenant); err != nil && !errors.Is(err, tenantllm.ErrNotConfigured) {
					failures <- fmt.Errorf("APIKey en carrera: %w", err)
				}
			}
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}

	cfg := mustGet(t, m, tenant)
	if !cfg.HasAPIKey { // ganó un escritor de la vía local
		requireLocalRow(t, m, tenant)
		return
	}
	requireRow(t, m, tenant, rowAPI)
	for w := 0; w < writers; w += 2 {
		if cfg.Model == modelOf(w) {
			requireAPIKey(t, m, tenant, keyOf(w)) // la clave y el modelo son de la misma escritura
			return
		}
	}
	t.Errorf("la fila final tiene el modelo %q, que no escribió ningún escritor", cfg.Model)
}

// coherent dice si un Config es una de las dos formas legítimas: api con su eje, o local sin él.
// Se pasa a la forma de fila que Get deja ver (del sobre solo ve si hay clave) y se compara con
// las dos formas enteras.
func coherent(cfg tenantllm.Config) bool {
	shape := Row{
		Via:         cfg.Via,
		HasProvider: cfg.Provider != "",
		HasModel:    cfg.Model != "",
		HasKeyEnc:   cfg.HasAPIKey,
		HasKeyDEK:   cfg.HasAPIKey,
		HasKEKID:    cfg.HasAPIKey,
		HasConsent:  !cfg.ConsentedAt.IsZero(),
	}
	return shape == rowAPI || shape == rowLocal
}
