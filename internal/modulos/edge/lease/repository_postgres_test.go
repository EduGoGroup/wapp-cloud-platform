package lease_test

// Los tests de fichero de lease.PostgresRepository, con un driver de database/sql de mentira
// (fakedb_test.go): el texto EXACTO de cada sentencia, sus argumentos y el mapeo de filas y
// errores. Que ese SQL haga en un Postgres de verdad lo que dice lo prueba la suite
// leasehelpertest.ContratoRepository en los procesos de F9 (sesión F3-05).

import (
	"context"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/lease"
)

// Las seis sentencias, byte a byte, escritas aquí a mano y NO copiadas de una constante de
// producción: si alguien toca el SQL del adaptador, este fichero lo dice. Los espacios en blanco
// (el salto inicial, las dos tabulaciones de sangría, la tabulación final) son los del literal
// del paquete viejo, internal/gateway/lease/repository_postgres.go @ 8896f13.
const (
	// Upsert: el INSERT escribe `false` literal en revoked y el SET del ON CONFLICT no nombra
	// esa columna. Es la guarda «Upsert no resucita» (R-L2, T-8).
	sqlUpsert = "\n" +
		"\t\tINSERT INTO public.leases (tenant_id, edge_id, counter, expires_at, revoked, issued_at, updated_at)\n" +
		"\t\tVALUES ($1, $2, $3, $4, false, now(), now())\n" +
		"\t\tON CONFLICT (tenant_id, edge_id) DO UPDATE\n" +
		"\t\tSET counter = EXCLUDED.counter,\n" +
		"\t\t    expires_at = EXCLUDED.expires_at,\n" +
		"\t\t    updated_at = now()\n" +
		"\t"
	// MarkRevoked: nace con counter 0 y revoked true; sobre una fila existente no toca counter.
	sqlMarkRevoked = "\n" +
		"\t\tINSERT INTO public.leases (tenant_id, edge_id, counter, expires_at, revoked, issued_at, updated_at)\n" +
		"\t\tVALUES ($1, $2, 0, $3, true, now(), now())\n" +
		"\t\tON CONFLICT (tenant_id, edge_id) DO UPDATE\n" +
		"\t\tSET revoked = true,\n" +
		"\t\t    expires_at = EXCLUDED.expires_at,\n" +
		"\t\t    updated_at = now()\n" +
		"\t"
	sqlGet = "\n" +
		"\t\tSELECT tenant_id::text, edge_id, counter, expires_at, revoked, issued_at, updated_at\n" +
		"\t\tFROM public.leases\n" +
		"\t\tWHERE tenant_id = $1 AND edge_id = $2\n" +
		"\t"
	// Las tres del corte por tenant leen y escriben public.tenants, que es de platform (D-9).
	sqlTenantRevoked = "\n" +
		"\t\tSELECT revoked_at FROM public.tenants WHERE id = $1\n" +
		"\t"
	sqlMarkTenantRevoked = "\n" +
		"\t\tUPDATE public.tenants SET revoked_at = now(), updated_at = now() WHERE id = $1\n" +
		"\t"
	sqlRestoreTenant = "\n" +
		"\t\tUPDATE public.tenants SET revoked_at = NULL, updated_at = now() WHERE id = $1\n" +
		"\t"
)

const (
	pgTenant = "5e0b3a52-6a52-4a44-8b1c-2f0d6a3f9c11"
	pgEdge   = "edge-pg-1"
)

var pgExpiry = time.Date(2031, 3, 4, 5, 6, 7, 0, time.UTC)

// newPostgres monta el adaptador sobre el driver de mentira.
func newPostgres(t *testing.T) (*lease.PostgresRepository, *fakeDB) {
	t.Helper()
	fake, db := openFakeDB(t)
	return lease.NewPostgresRepository(db), fake
}

// requireOnlyStatement afirma que al driver llegó UNA sentencia, con ese texto exacto y esos
// argumentos exactos (en número, orden, tipo y valor).
func requireOnlyStatement(t *testing.T, fake *fakeDB, wantQuery string, wantArgs ...driver.Value) {
	t.Helper()
	stmts := fake.statements()
	if len(stmts) != 1 {
		t.Fatalf("llegaron %d sentencias al driver, quería 1: %+v", len(stmts), stmts)
	}
	if stmts[0].query != wantQuery {
		t.Errorf("SQL emitido:\n%q\nquería, byte a byte:\n%q", stmts[0].query, wantQuery)
	}
	if !reflect.DeepEqual(stmts[0].args, wantArgs) {
		t.Errorf("argumentos = %#v, quería %#v", stmts[0].args, wantArgs)
	}
}

// requireWrapped afirma que err envuelve la causa y empieza por el prefijo del adaptador.
func requireWrapped(t *testing.T, err, cause error, prefix string) {
	t.Helper()
	if !errors.Is(err, cause) {
		t.Fatalf("error = %v, quería uno que envuelva %v", err, cause)
	}
	if !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("error = %q, quería el prefijo %q", err, prefix)
	}
}

// TestNewPostgresRepository_DoesNotTouchTheDatabase: construir no abre ni consulta nada.
func TestNewPostgresRepository_DoesNotTouchTheDatabase(t *testing.T) {
	repo, fake := newPostgres(t)
	if repo == nil {
		t.Fatal("NewPostgresRepository devolvió nil")
	}
	if n := len(fake.statements()); n != 0 {
		t.Errorf("construir el adaptador emitió %d sentencias, quería 0", n)
	}
}

// TestPostgresUpsert_NeverWritesRevoked: la sentencia exacta, y s.Revoked NO viaja: con true o
// con false el driver recibe lo mismo. Es la mitad «almacén» de la revocación pegajosa.
func TestPostgresUpsert_NeverWritesRevoked(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		repo, fake := newPostgres(t)
		err := repo.Upsert(context.Background(), lease.State{
			TenantID: pgTenant, EdgeID: pgEdge, Counter: 7, ExpiresAt: pgExpiry, Revoked: revoked,
			IssuedAt: pgExpiry.Add(time.Hour), UpdatedAt: pgExpiry.Add(2 * time.Hour), // se ignoran: los pone now()
		})
		if err != nil {
			t.Fatalf("Upsert(Revoked=%v): error inesperado %v", revoked, err)
		}
		requireOnlyStatement(t, fake, sqlUpsert, pgTenant, pgEdge, int64(7), pgExpiry)
	}

	// La guarda, dicha sobre el texto: el INSERT fija false y el SET no nombra revoked.
	insert, set, found := strings.Cut(sqlUpsert, "DO UPDATE")
	if !found || !strings.Contains(insert, "VALUES ($1, $2, $3, $4, false, now(), now())") {
		t.Errorf("el INSERT del Upsert no fija revoked a false literal:\n%s", insert)
	}
	if strings.Contains(set, "revoked") {
		t.Errorf("el SET del ON CONFLICT nombra revoked: Upsert podría resucitar un revocado:\n%s", set)
	}
}

// TestPostgresMarkRevoked: la sentencia exacta y sus tres argumentos.
func TestPostgresMarkRevoked(t *testing.T) {
	repo, fake := newPostgres(t)
	if err := repo.MarkRevoked(context.Background(), pgTenant, pgEdge, pgExpiry); err != nil {
		t.Fatalf("MarkRevoked: error inesperado %v", err)
	}
	requireOnlyStatement(t, fake, sqlMarkRevoked, pgTenant, pgEdge, pgExpiry)
}

// TestPostgresGet_MapsTheRow: la sentencia exacta y cada columna a su campo.
func TestPostgresGet_MapsTheRow(t *testing.T) {
	issued, updated := pgExpiry.Add(-time.Hour), pgExpiry.Add(-time.Minute)
	for _, revoked := range []bool{false, true} {
		repo, fake := newPostgres(t)
		fake.answer([]string{"tenant_id", "edge_id", "counter", "expires_at", "revoked", "issued_at", "updated_at"},
			[]driver.Value{pgTenant, pgEdge, int64(42), pgExpiry, revoked, issued, updated})
		st, found, err := repo.Get(context.Background(), pgTenant, pgEdge)
		if err != nil || !found {
			t.Fatalf("Get = (found=%v, err=%v), quería la fila", found, err)
		}
		want := lease.State{
			TenantID: pgTenant, EdgeID: pgEdge, Counter: 42, ExpiresAt: pgExpiry,
			Revoked: revoked, IssuedAt: issued, UpdatedAt: updated,
		}
		if st != want {
			t.Errorf("Get = %+v, quería %+v", st, want)
		}
		requireOnlyStatement(t, fake, sqlGet, pgTenant, pgEdge)
	}
}

// TestPostgresGet_NoRow_NotFound: sin fila no es un error: State cero, found=false, nil.
func TestPostgresGet_NoRow_NotFound(t *testing.T) {
	repo, fake := newPostgres(t)
	st, found, err := repo.Get(context.Background(), pgTenant, pgEdge)
	if err != nil || found || st != (lease.State{}) {
		t.Errorf("Get sin fila = (%+v, found=%v, err=%v), quería (State cero, false, nil)", st, found, err)
	}
	requireOnlyStatement(t, fake, sqlGet, pgTenant, pgEdge)
}

// TestPostgresGet_UnreadableRow_IsAnError: una fila que no se puede escanear es un error, no un
// «no encontrado» ni un State a medias.
func TestPostgresGet_UnreadableRow_IsAnError(t *testing.T) {
	repo, fake := newPostgres(t)
	fake.answer([]string{"tenant_id", "edge_id", "counter", "expires_at", "revoked", "issued_at", "updated_at"},
		[]driver.Value{pgTenant, pgEdge, "no-es-un-numero", pgExpiry, false, pgExpiry, pgExpiry})
	st, found, err := repo.Get(context.Background(), pgTenant, pgEdge)
	if err == nil || found || st != (lease.State{}) {
		t.Fatalf("Get de una fila ilegible = (%+v, found=%v, err=%v), quería (State cero, false, error)", st, found, err)
	}
	if !strings.HasPrefix(err.Error(), "lease: leer lease: ") {
		t.Errorf("error = %q, quería el prefijo %q", err, "lease: leer lease: ")
	}
}

// TestPostgresTenantRevoked: revoked_at NULL es activo; un instante, revocado; sin fila
// (sql.ErrNoRows), (false, nil): la ausencia de estado no es un «sí».
func TestPostgresTenantRevoked(t *testing.T) {
	cases := []struct {
		name string
		rows [][]driver.Value
		want bool
	}{
		{"null revoked_at is active", [][]driver.Value{{nil}}, false},
		{"revoked_at set is revoked", [][]driver.Value{{pgExpiry}}, true},
		{"no row is not revoked", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo, fake := newPostgres(t)
			fake.answer([]string{"revoked_at"}, c.rows...)
			got, err := repo.TenantRevoked(context.Background(), pgTenant)
			if err != nil || got != c.want {
				t.Errorf("TenantRevoked = (%v, %v), quería (%v, nil)", got, err, c.want)
			}
			requireOnlyStatement(t, fake, sqlTenantRevoked, pgTenant)
		})
	}
}

// TestPostgresMarkAndRestoreTenant: las dos sentencias exactas sobre public.tenants (D-9), con
// el tenant como único argumento.
func TestPostgresMarkAndRestoreTenant(t *testing.T) {
	repo, fake := newPostgres(t)
	if err := repo.MarkTenantRevoked(context.Background(), pgTenant); err != nil {
		t.Fatalf("MarkTenantRevoked: error inesperado %v", err)
	}
	requireOnlyStatement(t, fake, sqlMarkTenantRevoked, pgTenant)

	repo, fake = newPostgres(t)
	if err := repo.RestoreTenant(context.Background(), pgTenant); err != nil {
		t.Fatalf("RestoreTenant: error inesperado %v", err)
	}
	requireOnlyStatement(t, fake, sqlRestoreTenant, pgTenant)
}

// TestPostgres_DriverFailure_IsWrapped: un fallo del driver vuelve envuelto, con el texto de
// cada operación, y las lecturas no inventan estado: Get dice found=false y TenantRevoked,
// false (el fail-closed lo decide lease.Manager, que mira el error antes que el booleano).
func TestPostgres_DriverFailure_IsWrapped(t *testing.T) {
	cause := errors.New("conexión rota")
	ctx := context.Background()
	broken := func() *lease.PostgresRepository {
		repo, fake := newPostgres(t)
		fake.fail(cause)
		return repo
	}

	requireWrapped(t, broken().Upsert(ctx, lease.State{TenantID: pgTenant, EdgeID: pgEdge}), cause, "lease: upsert lease: ")
	requireWrapped(t, broken().MarkRevoked(ctx, pgTenant, pgEdge, pgExpiry), cause, "lease: marcar revocado: ")
	requireWrapped(t, broken().MarkTenantRevoked(ctx, pgTenant), cause, "lease: marcar tenant revocado: ")
	requireWrapped(t, broken().RestoreTenant(ctx, pgTenant), cause, "lease: restaurar tenant: ")

	st, found, err := broken().Get(ctx, pgTenant, pgEdge)
	requireWrapped(t, err, cause, "lease: leer lease: ")
	if found || st != (lease.State{}) {
		t.Errorf("Get con el driver caído = (%+v, found=%v), quería (State cero, false)", st, found)
	}
	revoked, err := broken().TenantRevoked(ctx, pgTenant)
	requireWrapped(t, err, cause, "lease: consultar revocación del tenant: ")
	if revoked {
		t.Error("TenantRevoked con el driver caído devolvió true")
	}
}
