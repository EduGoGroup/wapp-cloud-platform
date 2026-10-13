package entitlements_test

// Parte de postgres_test.go, partido por tamaño (F2-03, a petición de Jhoan; solo se movieron
// declaraciones): cachés separadas, el cerrojo fuera de la consulta y el uso concurrente.

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
)

// --- Las dos cachés, el candado y la concurrencia ----------------------------------------------

// TestPostgres_CachesAreSeparate: lo que cachea Has no sirve a ListEffective, ni al revés.
func TestPostgres_CachesAreSeparate(t *testing.T) {
	p, fdb, _ := newPostgres(t)
	fdb.set(func(f *fakeDB) { f.plans[tenantA], f.plans[tenantB] = "pro", "pro" })

	mustHas(t, p, tenantA, featureF)
	mustList(t, p, tenantA)
	if n := fdb.count(queryTenantPlan); n != 1 {
		t.Errorf("ListEffective(A) tras Has(A, f): %d consultas de plan, quería 1 (la caché de Has no le sirve)", n)
	}

	mustList(t, p, tenantB)
	mustHas(t, p, tenantB, featureF)
	if n := fdb.count(queryOverride); n != 2 {
		t.Errorf("Has(B, f) tras ListEffective(B): %d consultas de override, quería 2 (la caché por tenant no le sirve)", n)
	}
}

// TestPostgres_QueryDoesNotHoldTheLock: mientras una consulta de un tenant está colgada en la BD,
// los demás llamantes —los que aciertan en la caché y los que tienen que consultar— terminan.
func TestPostgres_QueryDoesNotHoldTheLock(t *testing.T) {
	cases := []struct {
		name string
		slow func(p *entitlements.Postgres) error
	}{
		{"slowHas", func(p *entitlements.Postgres) error {
			_, err := p.Has(context.Background(), tenantSlow, featureF)
			return err
		}},
		{"slowListEffective", func(p *entitlements.Postgres) error {
			_, _, err := p.ListEffective(context.Background(), tenantSlow)
			return err
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, fdb, _ := newPostgres(t)
			fdb.set(func(f *fakeDB) { f.plans[tenantA], f.plans[tenantSlow] = "pro", "pro" })
			// Con la BD sana se llenan las entradas de A, que luego se servirán de la caché.
			mustHas(t, p, tenantA, featureF)
			mustList(t, p, tenantA)

			started := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			fdb.set(func(f *fakeDB) {
				f.gate = func(_ queryKind, tenant string) {
					if tenant == tenantSlow {
						once.Do(func() { close(started) })
						<-release
					}
				}
			})
			slowDone := make(chan error, 1)
			go func() { slowDone <- c.slow(p) }()
			<-started

			othersDone := make(chan error, 1)
			go func() {
				ctx := context.Background()
				_, errHit := p.Has(ctx, tenantA, featureF)         // acierto
				_, _, errListHit := p.ListEffective(ctx, tenantA)  // acierto
				_, errMiss := p.Has(ctx, tenantB, featureG)        // fallo: consulta
				_, _, errListMiss := p.ListEffective(ctx, tenantB) // fallo: consulta
				othersDone <- errors.Join(errHit, errListHit, errMiss, errListMiss)
			}()
			var othersErr error
			finished := false
			select {
			case othersErr = <-othersDone:
				finished = true
			case <-time.After(watchdog):
				t.Errorf("con una consulta de %q colgada, los demás llamantes no terminaron en %v: el candado se sostiene durante la consulta", tenantSlow, watchdog)
			}
			close(release)
			if err := <-slowDone; err != nil {
				t.Errorf("la consulta lenta terminó con error %v", err)
			}
			if !finished {
				othersErr = <-othersDone
			}
			if othersErr != nil {
				t.Errorf("los demás llamantes terminaron con error: %v", othersErr)
			}
		})
	}
}

// TestPostgres_ConcurrentUse: Has y ListEffective en paralelo, con el reloj avanzando para que se
// mezclen aciertos y fallos de caché. Bajo -race, toda escritura sin proteger se ve aquí; y cada
// respuesta es la sembrada.
func TestPostgres_ConcurrentUse(t *testing.T) {
	p, fdb, clock := newPostgres(t)
	tenants := []string{tenantA, tenantB, tenantSlow}
	fdb.set(func(f *fakeDB) {
		for _, tenant := range tenants {
			f.plans[tenant] = "pro"
			f.features[tenant] = []driver.Value{"menu", "cart_basic"}
			f.overrides[pair{tenant, featureF}] = true
		}
	})
	const workers = 16
	var wg sync.WaitGroup
	errs := make(chan string, workers*len(tenants)*2)
	for i := range workers {
		wg.Go(func() {
			ctx := context.Background()
			for _, tenant := range tenants {
				if has, err := p.Has(ctx, tenant, featureF); err != nil || !has {
					errs <- fmt.Sprintf("Has(%q) en paralelo = (%v, %v); quería (true, nil)", tenant, has, err)
				}
				plan, features, err := p.ListEffective(ctx, tenant)
				if err != nil || plan != "pro" || !slices.Equal(features, []string{"cart_basic", "menu"}) {
					errs <- fmt.Sprintf("ListEffective(%q) en paralelo = (%q, %v, %v)", tenant, plan, features, err)
				}
				if len(features) > 0 {
					features[0] = "poisoned" // cada llamante recibe su copia
				}
			}
			if i%4 == 0 {
				clock.Advance(testTTL)
			}
		})
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Error(msg)
	}
}
