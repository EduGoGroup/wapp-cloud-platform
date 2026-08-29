package iampostgres_test

// multi_empresa_integration_test.go — LA SEGUNDA EMPRESA, CONTRA POSTGRES REAL
// (Plan 047 · Ola 5 · T5.2, D-047.14).
//
// Aquí se mide lo que un unitario no puede: que la guarda del alta consulta el
// entitlement `multi_empresa` DENTRO de la transacción del alta, y que el
// cerrojo que cierra la ventana TOCTOU es un cerrojo de verdad y no un
// comentario. Las dos cosas viven en el SQL, así que el doble en memoria —que
// tiene su hermana de estos tests en usecase/memberships_test.go— no las
// alcanza.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	iampostgres "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/infra/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
)

// segundaEmpresa siembra un tenant aparte del de env, para poder dar de alta a
// la misma persona en dos sitios.
func segundaEmpresa(t *testing.T, env itEnv, motivo string) string {
	t.Helper()
	slug := fmt.Sprintf("iam-it-%s-%d", motivo, time.Now().UnixNano())
	tn, err := postgres.NewTenantRepository(env.db).Create(context.Background(), slug, "Segunda")
	if err != nil {
		t.Fatalf("sembrar la segunda empresa: %v", err)
	}
	return tn.ID
}

// TestIntegration_GrantTenantAccess_ConMultiEmpresaEscribeLaSegunda es la mitad
// PERMISIVA de T5.2 contra la base real: el tenant de destino tiene el derecho,
// así que el alta de quien ya es miembro de otra empresa escribe la fila y
// además asigna su rol — el mismo desenlace que un alta normal.
//
// 🔴 EL DERECHO SE ENCIENDE EN EL TENANT DE DESTINO Y NO EN EL DE ORIGEN. Si la
// implementación preguntara por el de origen, este test se pondría rojo: es el
// único sitio donde la diferencia entre las dos lecturas es observable.
func TestIntegration_GrantTenantAccess_ConMultiEmpresaEscribeLaSegunda(t *testing.T) {
	t.Parallel()
	env := newITEnv(t)
	ctx := context.Background()
	roles := iampostgres.NewRoleRepo(env.db)
	userID := uuid.NewString()

	destino := segundaEmpresa(t, env, "multi-si")
	feats := entitlements.NewFake()
	feats.Enable(destino, entitlements.FeatureMultiEmpresa)

	// La PRIMERA empresa no necesita derecho ninguno: es la de siempre.
	if err := iampostgres.GrantTenantAccess(ctx, env.db, feats, userID, env.tenantID, nil); err != nil {
		t.Fatalf("GrantTenantAccess (primera empresa): %v", err)
	}

	rol, err := roles.Create(ctx, domain.Role{TenantID: &destino, Name: "admin-destino"})
	if err != nil {
		t.Fatalf("crear rol de la segunda empresa: %v", err)
	}
	if err := iampostgres.GrantTenantAccess(ctx, env.db, feats, userID, destino, &rol.ID); err != nil {
		t.Fatalf("GrantTenantAccess (segunda empresa, CON multi_empresa): %v", err)
	}

	tenants, err := iampostgres.NewMembershipRepo(env.db, nil).TenantsOfUser(ctx, userID)
	if err != nil {
		t.Fatalf("TenantsOfUser: %v", err)
	}
	if len(tenants) != 2 {
		t.Fatalf("membresías = %v, quiero DOS", tenants)
	}
	asignados, err := roles.RolesOfUser(ctx, userID, destino)
	if err != nil {
		t.Fatalf("RolesOfUser: %v", err)
	}
	if len(asignados) != 1 || asignados[0].ID != rol.ID {
		t.Fatalf("el alta tenía que asignar el rol de la segunda empresa: %+v", asignados)
	}
}

// TestIntegration_GrantTenantAccess_SinLaFeatureElRechazoEsElDeSiempre fija la
// mitad que NO puede cambiar: sin `multi_empresa`, el 409 es idéntico al de
// antes de T5.2 — mismo sentinela y mismo cuerpo, palabra por palabra.
//
// El literal del mensaje se compara a propósito. Es lo que viaja al cliente por
// las dos bandejas que traducen este error, y un refactor que lo reescribiera
// «mejor» cambiaría una respuesta pública sin que nada más se enterara.
func TestIntegration_GrantTenantAccess_SinLaFeatureElRechazoEsElDeSiempre(t *testing.T) {
	t.Parallel()
	env := newITEnv(t)
	ctx := context.Background()
	userID := uuid.NewString()
	destino := segundaEmpresa(t, env, "multi-no")

	// El resolver conoce la feature… pero para OTRO tenant.
	feats := entitlements.NewFake()
	feats.Enable(env.tenantID, entitlements.FeatureMultiEmpresa)

	if err := iampostgres.GrantTenantAccess(ctx, env.db, feats, userID, env.tenantID, nil); err != nil {
		t.Fatalf("GrantTenantAccess (primera empresa): %v", err)
	}
	err := iampostgres.GrantTenantAccess(ctx, env.db, feats, userID, destino, nil)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("err = %v, quiero domain.ErrConflict", err)
	}
	if got, want := err.Error(), "iam: conflicto de unicidad: el usuario ya es miembro de otra empresa"; got != want {
		t.Fatalf("el cuerpo del 409 cambió.\n got: %s\nwant: %s", got, want)
	}
}

// TestIntegration_GrantTenantAccess_ElResolverCaidoMantieneElRechazo — el
// FAIL-CLOSED de T5.2 contra la base real, y el aserto que impide la variante
// «devuelve el error del resolver»: quien no puede acreditar su derecho se lleva
// el conflicto de siempre, no un 500.
func TestIntegration_GrantTenantAccess_ElResolverCaidoMantieneElRechazo(t *testing.T) {
	t.Parallel()
	env := newITEnv(t)
	ctx := context.Background()
	userID := uuid.NewString()
	destino := segundaEmpresa(t, env, "multi-caido")

	feats := entitlements.NewFake()
	feats.Enable(destino, entitlements.FeatureMultiEmpresa) // lo tiene…
	feats.Err = errors.New("la base de entitlements no contesta")

	if err := iampostgres.GrantTenantAccess(ctx, env.db, entitlements.NewFake(), userID, env.tenantID, nil); err != nil {
		t.Fatalf("GrantTenantAccess (primera empresa): %v", err)
	}
	err := iampostgres.GrantTenantAccess(ctx, env.db, feats, userID, destino, nil)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("err = %v, quiero domain.ErrConflict (fail-closed)", err)
	}
	tenants, terr := iampostgres.NewMembershipRepo(env.db, nil).TenantsOfUser(ctx, userID)
	if terr != nil || len(tenants) != 1 {
		t.Fatalf("no se podía escribir nada: %v err=%v", tenants, terr)
	}
}

// TestIntegration_GrantTenantAccess_DosAltasSimultaneasSoloUnaEscribe es la
// prueba de la ventana TOCTOU que T5.2 CIERRA (el paso (0) de
// GrantTenantAccess, pg_advisory_xact_lock sobre el user_id).
//
// 🔴 POR QUÉ ESTE TEST NO SE PUEDE ESCRIBIR DE OTRA FORMA. Lo que se mide es una
// carrera entre DOS transacciones, así que hace falta que existan las dos a la
// vez: con una sola conexión el conteo siempre ve el estado confirmado y la
// ventana no aparece nunca. Las dos goroutines abren su transacción, se esperan
// en una barrera y ENTONCES piden el alta de la MISMA persona en DOS empresas
// distintas, ninguna de las cuales tiene `multi_empresa`.
//
// Con el cerrojo: la segunda espera a que la primera confirme, cuenta 1 y se va
// con conflicto ⇒ una membresía. Sin él: las dos cuentan 0 y las dos escriben ⇒
// dos membresías, que es exactamente lo que la feature niega y lo que un tenant
// sin pagarla conseguiría lanzando dos altas a la vez.
//
// ⚠️ No es un test de flake: cuando el cerrojo está, el desenlace es
// determinista. Lo probabilístico es el otro lado —sin cerrojo la carrera puede
// no darse—, y por eso se repite varias veces.
func TestIntegration_GrantTenantAccess_DosAltasSimultaneasSoloUnaEscribe(t *testing.T) {
	t.Parallel()
	env := newITEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	members := iampostgres.NewMembershipRepo(env.db, nil)

	const vueltas = 5
	for vuelta := range vueltas {
		userID := uuid.NewString()
		destinos := [2]string{env.tenantID, segundaEmpresa(t, env, fmt.Sprintf("toctou-%d", vuelta))}

		var arranque sync.WaitGroup
		arranque.Add(2)
		var fin sync.WaitGroup
		fin.Add(2)
		resultados := make([]error, 2)

		for i := range 2 {
			go func() {
				defer fin.Done()
				tx, err := env.db.BeginTx(ctx, nil)
				if err != nil {
					resultados[i] = fmt.Errorf("BeginTx: %w", err)
					arranque.Done()
					return
				}
				// La barrera va DESPUÉS del BeginTx: lo que tiene que solaparse
				// son las transacciones, no las goroutines.
				arranque.Done()
				arranque.Wait()

				if gerr := iampostgres.GrantTenantAccess(ctx, tx, nil, userID, destinos[i], nil); gerr != nil {
					resultados[i] = gerr
					if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
						t.Errorf("rollback tras el rechazo: %v", rerr)
					}
					return
				}
				resultados[i] = tx.Commit()
			}()
		}
		fin.Wait()

		var escrituras, conflictos int
		for _, err := range resultados {
			switch {
			case err == nil:
				escrituras++
			case errors.Is(err, domain.ErrConflict):
				conflictos++
			default:
				t.Fatalf("vuelta %d: error inesperado: %v", vuelta, err)
			}
		}
		if escrituras != 1 || conflictos != 1 {
			t.Fatalf("vuelta %d: escrituras=%d conflictos=%d, quiero 1 y 1 (%v)",
				vuelta, escrituras, conflictos, resultados)
		}
		tenants, err := members.TenantsOfUser(ctx, userID)
		if err != nil {
			t.Fatalf("TenantsOfUser: %v", err)
		}
		if len(tenants) != 1 {
			t.Fatalf("vuelta %d: la carrera dejó %d membresías (%v): la ventana TOCTOU sigue abierta",
				vuelta, len(tenants), tenants)
		}
	}
}
