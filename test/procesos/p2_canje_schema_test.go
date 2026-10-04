//go:build integracion

package procesos

import (
	"database/sql"
	"testing"
)

// La migración 0038 (retiro del IAM propio) vista desde P2 (T2.33, diseno.md §6 de F2). Porta la
// regla de internal/iam/infra/postgres/integration_test.go
// (TestIntegration_ElIAMPropioNoSobrevivioALaMigracion): que el DDL corra sin error no prueba nada,
// porque el fallo temido —un DROP … CASCADE que se lleva el RBAC de negocio— también corre sin
// error. Por eso se miran las DOS mitades, sobre la base con la que el servidor del proceso ya
// canjeó identidades y resolvió permisos.

// p2OwnIAMTables son las tablas del IAM propio de wApp, que la 0038 tenía que borrar: las personas
// y sus credenciales viven en identity.
var p2OwnIAMTables = []string{"iam_users", "iam_refresh_tokens", "iam_api_keys"}

// p2BusinessRBACTables son las del RBAC de negocio, que tenían que sobrevivir a ese borrado.
var p2BusinessRBACTables = []string{"iam_roles", "iam_role_grants", "iam_user_roles", "iam_user_grants"}

// p2ForeignKeysTo cuenta las claves foráneas del esquema public que apuntan a una tabla.
const p2ForeignKeysTo = `SELECT count(*)
	FROM information_schema.table_constraints tc
	JOIN information_schema.constraint_column_usage ccu
	  ON tc.constraint_name = ccu.constraint_name AND tc.table_schema = ccu.table_schema
	WHERE tc.constraint_type = 'FOREIGN KEY' AND tc.table_schema = 'public' AND ccu.table_name = $1`

// p2TableExists dice si la tabla existe en el esquema public.
func p2TableExists(t *testing.T, db *sql.DB, table string) bool {
	t.Helper()
	return p2Text(t, db, `SELECT (to_regclass('public.' || quote_ident($1)) IS NOT NULL)::text`, table) == "true"
}

// p2OwnIAMDidNotSurvive afirma el desenlace de la 0038: ninguna tabla del IAM propio existe, las
// cuatro del RBAC de negocio sí, ninguna clave foránea apunta ya a iam_users (por ahí se propagaría
// un CASCADE, y la siguiente réplica de las migraciones fallaría) y el RBAC conserva sus plantillas
// sembradas, que es lo que un CASCADE se habría llevado dejando las tablas en pie.
func p2OwnIAMDidNotSurvive(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, table := range p2OwnIAMTables {
		if p2TableExists(t, db, table) {
			t.Errorf("la tabla %s sigue existiendo: el IAM propio debía morir en la migración 0038", table)
		}
	}
	for _, table := range p2BusinessRBACTables {
		if !p2TableExists(t, db, table) {
			t.Errorf("la tabla %s no existe: es RBAC de negocio y tenía que sobrevivir a la migración 0038", table)
		}
	}
	if n := consultaEntero(t, db, p2ForeignKeysTo, "iam_users"); n != 0 {
		t.Errorf("quedan %d claves foráneas hacia iam_users: la 0038 debía soltarlas todas", n)
	}
	for _, role := range []string{edgeRolTenantAdmin, p2RoleOperator, p2RoleViewer} {
		if n := consultaEntero(t, db, `SELECT count(*) FROM public.iam_role_grants WHERE role_id = $1::uuid`, role); n == 0 {
			t.Errorf("la plantilla de rol %s no tiene grants: el RBAC de negocio no sobrevivió entero", role)
		}
	}
}
