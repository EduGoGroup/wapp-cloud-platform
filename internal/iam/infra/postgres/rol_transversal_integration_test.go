package iampostgres_test

// rol_transversal_integration_test.go — UN ROL DE EMPRESA NO SE ASIGNA GLOBAL
// (Plan 047 · Ola 5 · T5.6).
//
// LO QUE ESTÁ EN JUEGO. Una fila de public.iam_user_roles con tenant_id NULL es
// un rol que vale en TODAS las empresas: así lo resuelve RoleRepo.RolesOfUser
// —`WHERE ur.tenant_id = $2 OR ur.tenant_id IS NULL`— y así tiene que seguir
// siendo para `platform_admin`, que por diseño actúa sobre empresas ajenas
// (ADR-0039). Para cualquier OTRO rol es un privilegio que nadie concedió: el
// día que su titular pertenezca a dos empresas —que es justo lo que la Ola 5
// abre— será administrador de las dos.
//
// 🔴 LOS DOS TESTS DE ESTE FICHERO SON HERMANOS Y NINGUNO VALE SOLO. El negativo
// lo pasaría también una guarda que rechazara TODO, incluido el transversal —la
// mutación al extremo trivial—, y con ella el plano de plataforma se quedaría
// sin poder asignarse. Por eso el positivo va al lado y con aserto sobre el
// dato, no metido como una fila más en la tabla del negativo.

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	iampostgres "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/infra/postgres"
)

// TestIntegration_AssignToUser_UnRolDeEmpresaNoSeAsignaGlobal — el NEGATIVO.
//
// Cubre las dos formas de decir «sin empresa» que llegan al repositorio: el
// puntero nil y el puntero a cadena vacía. La segunda no es una rareza de test:
// es lo que produce un `&c.TenantID` cuando el contexto no traía tenant, y sin
// ella la guarda se rodea pasando "" en vez de nil.
func TestIntegration_AssignToUser_UnRolDeEmpresaNoSeAsignaGlobal(t *testing.T) {
	t.Parallel()
	env := newITEnv(t)
	ctx := context.Background()
	roles := iampostgres.NewRoleRepo(env.db)
	userID := uuid.NewString()

	rol, err := roles.Create(ctx, domain.Role{TenantID: &env.tenantID, Name: "rol-de-empresa"})
	if err != nil {
		t.Fatalf("crear rol de empresa: %v", err)
	}
	vacio := ""
	for nombre, ambito := range map[string]*string{"nil": nil, "cadena vacía": &vacio} {
		t.Run(nombre, func(t *testing.T) {
			err := roles.AssignToUser(ctx, userID, rol.ID, ambito)
			if !errors.Is(err, domain.ErrRoleScopeInvalid) {
				t.Fatalf("err = %v, quiero domain.ErrRoleScopeInvalid", err)
			}
		})
	}

	// Y no escribió nada: el rechazo va antes del INSERT, no después.
	var filas int
	if err := env.db.QueryRowContext(ctx,
		`SELECT count(*) FROM public.iam_user_roles WHERE user_id = $1`, userID).Scan(&filas); err != nil {
		t.Fatalf("contar asignaciones: %v", err)
	}
	if filas != 0 {
		t.Fatalf("el rechazo dejó %d filas en iam_user_roles: tenía que cortar antes de escribir", filas)
	}

	// LA OTRA MITAD, y sin ella el test sería una guarda que rechaza todo: el
	// MISMO rol ACOTADO a su empresa se asigna sin problema.
	if err := roles.AssignToUser(ctx, userID, rol.ID, &env.tenantID); err != nil {
		t.Fatalf("el mismo rol acotado a su empresa tenía que asignarse: %v", err)
	}
	asignados, err := roles.RolesOfUser(ctx, userID, env.tenantID)
	if err != nil || len(asignados) != 1 || asignados[0].ID != rol.ID {
		t.Fatalf("RolesOfUser = %+v err=%v, quiero solo %s", asignados, err, rol.ID)
	}
}

// TestIntegration_AssignToUser_ElRolTransversalSiSeAsignaGlobal — el HERMANO
// POSITIVO, con aserto sobre el DATO y no solo sobre el error.
//
// `platform_admin` es la excepción por diseño, y su ámbito global tiene que
// seguir escribiéndose EXACTAMENTE igual que antes de T5.6: fila con tenant_id
// NULL, resuelta para cualquier empresa. El id que se usa aquí es el fijo que
// siembra 0059_platform_admin.sql, que es el mismo discriminador que usa la
// guarda — si algún día la migración lo cambiara, este test lo dice.
func TestIntegration_AssignToUser_ElRolTransversalSiSeAsignaGlobal(t *testing.T) {
	t.Parallel()
	env := newITEnv(t)
	ctx := context.Background()
	roles := iampostgres.NewRoleRepo(env.db)
	userID := uuid.NewString()

	if err := roles.AssignToUser(ctx, userID, domain.RolTransversalID, nil); err != nil {
		t.Fatalf("el rol transversal TIENE que poder asignarse global: %v", err)
	}

	var nulos int
	if err := env.db.QueryRowContext(ctx,
		`SELECT count(*) FROM public.iam_user_roles WHERE user_id = $1 AND tenant_id IS NULL`,
		userID).Scan(&nulos); err != nil {
		t.Fatalf("contar asignaciones globales: %v", err)
	}
	if nulos != 1 {
		t.Fatalf("asignaciones globales = %d, quiero 1 (la fila tiene que existir CON tenant_id NULL)", nulos)
	}

	// Y sigue valiendo en una empresa cualquiera, que es para lo que existe el
	// ámbito global: el token del operador sale idéntico.
	asignados, err := roles.RolesOfUser(ctx, userID, env.tenantID)
	if err != nil {
		t.Fatalf("RolesOfUser: %v", err)
	}
	var encontrado bool
	for _, r := range asignados {
		if r.ID == domain.RolTransversalID {
			encontrado = true
		}
	}
	if !encontrado {
		t.Fatalf("el rol transversal tenía que resolverse en cualquier empresa: %+v", asignados)
	}
}

// TestIntegration_GrantTenantAccess_NoAsignaRolesConAmbitoGlobal cierra la
// SEGUNDA vía de escritura de iam_user_roles. El criterio de T5.6 pide las dos:
// una guarda que solo viva en RoleRepo.AssignToUser deja abierta la puerta del
// alta de acceso, que inserta en la misma tabla con su propio SQL.
//
// El tenant vacío es la única forma de pedirle a esta función una fila global
// (aquí el tenant viaja por valor), y hoy además reventaría más abajo con un
// error de UUID: lo que este test fija es que se va con el error de ÁMBITO —el
// que nombra el problema— y no con un fallo de sintaxis de Postgres.
func TestIntegration_GrantTenantAccess_NoAsignaRolesConAmbitoGlobal(t *testing.T) {
	t.Parallel()
	env := newITEnv(t)
	ctx := context.Background()
	roles := iampostgres.NewRoleRepo(env.db)

	rol, err := roles.Create(ctx, domain.Role{TenantID: &env.tenantID, Name: "rol-de-empresa-alta"})
	if err != nil {
		t.Fatalf("crear rol: %v", err)
	}
	err = iampostgres.GrantTenantAccess(ctx, env.db, nil, uuid.NewString(), "", &rol.ID)
	if !errors.Is(err, domain.ErrRoleScopeInvalid) {
		t.Fatalf("err = %v, quiero domain.ErrRoleScopeInvalid", err)
	}
}
