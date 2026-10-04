//go:build integracion

package procesos

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// Lo que las migraciones dejan en el catálogo y en los datos, comprobado antes y después de cada
// réplica completa (TestP10_MigrationsReplay). Cada bloque lleva la regla del test viejo del que
// sale. Todo es SQL sobre la base clonada: el catálogo de Postgres ES lo observable de cmd/migrate.

// Los SQLSTATE con los que el esquema rechaza una fila.
const (
	p10UniqueViolation = "23505"
	p10CheckViolation  = "23514"
)

// p10ReplaySeed es lo sembrado antes de la primera réplica, y la huella del esquema de entonces.
type p10ReplaySeed struct {
	tenant         string
	user           string // user_active_tenant.user_id: sin FK, la persona vive en identity
	intake         string
	sessions       map[string]string // session_id → perfil explícito con el que se sembró
	activeTenantFP string            // huella de las columnas de user_active_tenant
	grants         int               // filas de iam_role_grants
}

// p10SeedReplayData deja datos de negocio en la base: una empresa, su empresa activa para una
// persona (0086), dos sesiones con perfiles OPUESTOS y explícitos (0063) y una solicitud con su
// línea y su revisión colgando de su evento (lo que tocó la 0045; desde la 0054 la solicitud declara
// a su padre). No hay servidor en este test: el SQL es la única forma de dejarlos.
func p10SeedReplayData(t *testing.T, db *sql.DB) p10ReplaySeed {
	t.Helper()
	seed := p10ReplaySeed{
		user:     uuidAleatorio(t),
		intake:   uuidAleatorio(t),
		sessions: map[string]string{"sesion-activa-p10": "active", "sesion-pasiva-p10": "passive"},
	}
	seed.tenant = p2Text(t, db, `INSERT INTO public.tenants (slug, display_name) VALUES ('p10-replica', 'Réplica de P10') RETURNING id::text`)
	p10Exec(t, db, `INSERT INTO public.user_active_tenant (user_id, tenant_id) VALUES ($1::uuid, $2::uuid)`, seed.user, seed.tenant)
	const session = `INSERT INTO public.fleet_sessions (tenant_id, edge_id, session_id, state, profile, last_seen_at, updated_at)
		VALUES ($1::uuid, 'edge-p10', $2, 'online', $3, now(), now())`
	for id, profile := range seed.sessions {
		p10Exec(t, db, session, seed.tenant, id, profile)
	}

	event := p2Text(t, db, `
		INSERT INTO public.conversation_events
			(tenant_id, session_id, contact_id, kind, history_id, status, flow_id, flow_version, closed_at)
		VALUES ($1::uuid, 'sesion-1', $2::uuid, 'cart', 'cart-p10-0002', 'closed', 'flujo-replica', 1, now())
		RETURNING id::text`, seed.tenant, uuidAleatorio(t))
	p10Exec(t, db, `INSERT INTO public.intakes (id, tenant_id, contact_id, session_id, status, total, event_id)
		VALUES ($1::uuid, $2, 'contacto-opaco', 'sesion-1', 'closed', 5000, $3::uuid)`, seed.intake, seed.tenant, event)
	p10Exec(t, db, `INSERT INTO public.intake_items (intake_id, sku, label, qty, unit_price)
		VALUES ($1::uuid, 'emp-pino', 'Empanada de pino', 2, 2500)`, seed.intake)
	p10Exec(t, db, `INSERT INTO public.intake_revisions (intake_id, revision_no, kind, payload, created_by)
		VALUES ($1::uuid, 1, 'cart', '{"version":1,"total":5000,"items":[]}'::jsonb, 'system')`, seed.intake)

	seed.activeTenantFP = p10TableFingerprint(t, db, "user_active_tenant")
	seed.grants = consultaEntero(t, db, `SELECT count(*) FROM public.iam_role_grants`)
	return seed
}

// p10TableFingerprint devuelve una huella de las columnas de la tabla (nombre, tipo, nulabilidad y
// default, en orden). Comparar la huella entera es lo que nota un cambio que nadie previó. Falla
// (t.Fatalf) si la tabla no tiene columnas: dos huellas vacías serían «iguales».
func p10TableFingerprint(t *testing.T, db *sql.DB, table string) string {
	t.Helper()
	fp := p2Text(t, db, `SELECT coalesce(string_agg(column_name || ' ' || data_type || ' null=' || is_nullable ||
			' def=' || coalesce(column_default, '-'), ' | ' ORDER BY ordinal_position), '')
		FROM information_schema.columns WHERE table_schema = 'public' AND table_name = $1`, table)
	if fp == "" {
		t.Fatalf("la tabla %s no tiene columnas: la comparación no probaría nada", table)
	}
	return fp
}

// p10Columns cuenta las columnas public.<table>.<column> del catálogo: 1 si existe, 0 si no.
func p10Columns(t *testing.T, db *sql.DB, table, column string) int {
	t.Helper()
	return consultaEntero(t, db, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2`, table, column)
}

// p10WantRejected exige que el esquema rechace la sentencia con ese SQLSTATE.
func p10WantRejected(t *testing.T, db *sql.DB, what, sqlstate, query string, args ...any) {
	t.Helper()
	err := p10ExecErr(t, db, query, args...)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != sqlstate {
		t.Errorf("%s: el esquema contestó %v, quería un rechazo con SQLSTATE %s", what, err, sqlstate)
	}
}

// p10CheckSchema comprueba el estado al que converge el directorio de migraciones. tag distingue
// las filas que cada llamada inserta para probar un DEFAULT o una clave.
func p10CheckSchema(t *testing.T, db *sql.DB, seed p10ReplaySeed, tag string) {
	t.Helper()
	p10CheckContactsSchema(t, db, seed, tag)
	p10CheckClearPIIGone(t, db)
	p10CheckSessionProfile(t, db, seed, tag)
	p10CheckActiveTenant(t, db, seed)
	p10CheckDataKept(t, db, seed)
	p10CheckGrantSeeds(t, db)
	if n := consultaEntero(t, db, `SELECT count(*) FROM public.iam_role_grants`); n != seed.grants {
		t.Errorf("iam_role_grants pasó de %d a %d filas: el seed no es idempotente", seed.grants, n)
	}
}

// p10CheckContactsSchema · contacts_integration_test.go: contacts existe y está CIFRADA (sin la
// columna value en claro), flow_state quedó re-claveada a contact_id (uuid) y no conserva contact,
// y la clave (tenant, kind, value_bidx) rechaza la misma ref dos veces. Y de integration_test.go,
// que tenants existe tras migrar.
func p10CheckContactsSchema(t *testing.T, db *sql.DB, seed p10ReplaySeed, tag string) {
	t.Helper()
	for _, table := range []string{"tenants", "contacts"} {
		if n := consultaEntero(t, db, `SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1`, table); n != 1 {
			t.Errorf("la tabla public.%s no existe tras migrar", table)
		}
	}
	if kind := p2Text(t, db, `SELECT data_type FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'flow_state' AND column_name = 'contact_id'`); kind != "uuid" {
		t.Errorf("flow_state.contact_id es de tipo %q, quería uuid", kind)
	}
	for _, gone := range [][2]string{{"flow_state", "contact"}, {"contacts", "value"}} {
		if p10Columns(t, db, gone[0], gone[1]) != 0 {
			t.Errorf("public.%s.%s sigue existiendo", gone[0], gone[1])
		}
	}
	const insert = `INSERT INTO public.contacts (tenant_id, kind, value_bidx, value_enc, value_dek)
		VALUES ($1::uuid, 'phone_e164', $2, '\x00'::bytea, '\x00'::bytea)`
	if n := p10Exec(t, db, insert, seed.tenant, "bidx-p10-"+tag); n != 1 {
		t.Errorf("el primer insert de la ref tocó %d filas, quería 1", n)
	}
	p10WantRejected(t, db, "la misma ref (tenant, kind, value_bidx) otra vez", p10UniqueViolation, insert, seed.tenant, "bidx-p10-"+tag)
}

// p10CheckClearPIIGone · drop_pii_claro_integration_test.go: las dos columnas en claro que retiró
// la 0070 no existen —y una réplica no las devuelve, aunque la 0005, la 0006 y la 0028 las vuelvan
// a declarar: es una invariante de ORDEN—, y las siete del sobre cifrado siguen ahí.
func p10CheckClearPIIGone(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, gone := range [][2]string{{"fleet_sessions", "self_pn"}, {"contacts", "push_name"}} {
		if p10Columns(t, db, gone[0], gone[1]) != 0 {
			t.Errorf("public.%s.%s existe: la 0070 no se aplicó o la réplica la recreó después de ella", gone[0], gone[1])
		}
	}
	alive := map[string][]string{
		"fleet_sessions": {"self_pn_enc", "self_pn_dek", "self_pn_kek_id", "self_pn_bidx"},
		"contacts":       {"push_name_enc", "push_name_dek", "push_name_kek_id"},
	}
	for table, columns := range alive {
		for _, column := range columns {
			if p10Columns(t, db, table, column) != 1 {
				t.Errorf("public.%s.%s NO existe: el DROP de la 0070 se llevó una columna del sobre cifrado", table, column)
			}
		}
	}
}

// p10CheckSessionProfile · profile_replay_integration_test.go: las dos sesiones conservan su perfil
// (el backfill de la 0063 solo toca las filas sin perfil), una sesión nueva nace pasiva, la columna
// es NOT NULL, el CHECK con NOMBRE sigue puesto y rechaza un perfil de fuera, y la columna role que
// retiró la 0064 no está (la 0025 la recrea en cada réplica).
func p10CheckSessionProfile(t *testing.T, db *sql.DB, seed p10ReplaySeed, tag string) {
	t.Helper()
	const profile = `SELECT profile FROM public.fleet_sessions WHERE tenant_id = $1::uuid AND session_id = $2`
	for session, want := range seed.sessions {
		if got := p2Text(t, db, profile, seed.tenant, session); got != want {
			t.Errorf("el perfil de la sesión %s es %q, quería %q: la réplica lo pisó", session, got, want)
		}
	}
	fresh := "sesion-nueva-p10-" + tag
	p10Exec(t, db, `INSERT INTO public.fleet_sessions (tenant_id, edge_id, session_id, state) VALUES ($1::uuid, 'edge-nuevo', $2, 'online')`, seed.tenant, fresh)
	if got := p2Text(t, db, profile, seed.tenant, fresh); got != "passive" {
		t.Errorf("una sesión nueva nace con el perfil %q, quería passive", got)
	}
	if nullable := p2Text(t, db, `SELECT is_nullable FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'fleet_sessions' AND column_name = 'profile'`); nullable != "NO" {
		t.Errorf("fleet_sessions.profile tiene is_nullable=%q, quería NO", nullable)
	}
	if n := consultaEntero(t, db, `SELECT count(*) FROM pg_constraint
		WHERE conrelid = 'public.fleet_sessions'::regclass AND contype = 'c' AND conname = 'fleet_sessions_profile_chk'`); n != 1 {
		t.Errorf("hay %d constraints fleet_sessions_profile_chk, quería 1", n)
	}
	p10WantRejected(t, db, "un perfil fuera del dominio", p10CheckViolation,
		`INSERT INTO public.fleet_sessions (tenant_id, edge_id, session_id, state, profile) VALUES ($1::uuid, 'edge-nuevo', $2, 'online', 'supervisor')`,
		seed.tenant, "sesion-chk-p10-"+tag)
	if p10Columns(t, db, "fleet_sessions", "role") != 0 {
		t.Errorf("fleet_sessions.role existe: la 0064 no se aplicó")
	}
}

// p10CheckActiveTenant · migrations/replay_integration_test.go: la empresa activa de la persona
// sigue ahí y con su valor, el esquema de la tabla no se movió, su clave primaria es user_id A
// SECAS (con una compuesta, una persona tendría dos empresas activas) y no tiene ni una columna de
// texto donde quepa un correo.
func p10CheckActiveTenant(t *testing.T, db *sql.DB, seed p10ReplaySeed) {
	t.Helper()
	if got := p2Text(t, db, `SELECT tenant_id::text FROM public.user_active_tenant WHERE user_id = $1::uuid`, seed.user); got != seed.tenant {
		t.Errorf("la empresa activa de la persona es %q, quería %q", got, seed.tenant)
	}
	if fp := p10TableFingerprint(t, db, "user_active_tenant"); fp != seed.activeTenantFP {
		t.Errorf("el esquema de user_active_tenant cambió.\nantes:   %s\ndespués: %s", seed.activeTenantFP, fp)
	}
	if pk := p2Text(t, db, `SELECT string_agg(a.attname, ',' ORDER BY a.attnum) FROM pg_constraint c
		JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = ANY (c.conkey)
		WHERE c.conrelid = 'public.user_active_tenant'::regclass AND c.contype = 'p'`); pk != "user_id" {
		t.Errorf("la clave primaria de user_active_tenant es %q, quería user_id a secas", pk)
	}
	if n := consultaEntero(t, db, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'user_active_tenant'
		  AND data_type IN ('text', 'character varying', 'character')`); n != 0 {
		t.Errorf("user_active_tenant tiene %d columnas de texto, quería 0", n)
	}
}

// p10CheckDataKept · replay_integration_test.go: la réplica no es destructiva. La solicitud
// conserva su línea y su revisión.
func p10CheckDataKept(t *testing.T, db *sql.DB, seed p10ReplaySeed) {
	t.Helper()
	for _, table := range []string{"intake_items", "intake_revisions"} {
		if n := consultaEntero(t, db, `SELECT count(*) FROM public.`+table+` WHERE intake_id = $1::uuid`, seed.intake); n != 1 {
			t.Errorf("%s tiene %d filas de la solicitud, quería 1: la réplica perdió datos", table, n)
		}
	}
}
