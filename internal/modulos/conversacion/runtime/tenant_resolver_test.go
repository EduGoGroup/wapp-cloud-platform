package runtime

import (
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
)

// Aserción de compilación: el adaptador ES el puerto que consume el runtime.
var _ TenantResolver = (*PostgresTenantResolver)(nil)

// Este fichero prueba, con un driver de mentira, la FORMA del adaptador: cuántas sentencias
// salen y con qué argumentos, y cómo se mapean las filas y los errores. Que su SQL haga en un
// Postgres de verdad lo que el contrato promete —el perfil agregado entre edges, el perfil fuera
// de dominio, el DEFAULT de la columna, la ambigüedad entre tenants— lo prueba
// runtimehelpertest.ContratoTenantResolver con el arnés de test/procesos
// (runtime_tenant_resolver_contrato_test.go), y contra el gemelo en memoria en unitario.

const (
	trTenant  = "5b7e0f0c-9d0e-4a51-8a55-0d4f6a3c1e10"
	trOther   = "0c1d2e3f-4a5b-4c6d-8e7f-9a0b1c2d3e4f"
	trSession = "sess-tenant-resolver"
)

// trHarness es un resolver sobre el driver de mentira, con el registro de lo que le llegó.
type trHarness struct {
	fake     *pgFake
	resolver *PostgresTenantResolver
}

// newTRHarness construye el resolver sobre un driver de mentira con ese guion.
func newTRHarness(t *testing.T, replies ...pgReply) trHarness {
	t.Helper()
	fake := &pgFake{replies: replies}
	db := fake.open(t)
	return trHarness{resolver: NewPostgresTenantResolver(db), fake: fake}
}

// TestNewPostgresTenantResolver_DoesNotTouchTheDatabase: construir no manda ni una sentencia, y
// con un db nil tampoco falla (el pool no se valida aquí).
func TestNewPostgresTenantResolver_DoesNotTouchTheDatabase(t *testing.T) {
	h := newTRHarness(t)
	if h.resolver == nil {
		t.Fatal("NewPostgresTenantResolver devolvió nil")
	}
	if NewPostgresTenantResolver(nil) == nil {
		t.Fatal("NewPostgresTenantResolver(nil) devolvió nil")
	}
	requirePgStatements(t, h.fake, 0)
}

// TestResolveTenant_OneTenant_MapsTheAggregateToTheProfile: con una fila (un tenant), UNA sola
// sentencia cuyo único argumento es el session_id, y el agregado «alguna no activa» se traduce al
// perfil del motor: false → "active", true → "passive".
func TestResolveTenant_OneTenant_MapsTheAggregateToTheProfile(t *testing.T) {
	cases := []struct {
		name       string
		anyPassive bool
		want       string
	}{
		{"every row is active", false, "active"},
		{"some row is not active", true, "passive"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newTRHarness(t, pgReply{rows: [][]driver.Value{{trTenant, c.anyPassive}}})

			tenantID, profile, err := h.resolver.ResolveTenant(t.Context(), trSession)
			if err != nil {
				t.Fatalf("ResolveTenant: %v", err)
			}
			if tenantID != trTenant || profile != c.want {
				t.Errorf("ResolveTenant = (%q, %q), quería (%q, %q)", tenantID, profile, trTenant, c.want)
			}
			sent := requirePgStatements(t, h.fake, 1)
			if len(sent[0].args) != 1 || sent[0].args[0] != trSession {
				t.Errorf("argumentos = %v, quería solo el session_id %q", sent[0].args, trSession)
			}
		})
	}
}

// TestResolveTenant_NoRows_ErrTenantNotResolved: sin filas, valores vacíos y un error que casa con
// el centinela y cuyo texto es el del viejo, byte a byte.
func TestResolveTenant_NoRows_ErrTenantNotResolved(t *testing.T) {
	h := newTRHarness(t)

	tenantID, profile, err := h.resolver.ResolveTenant(t.Context(), trSession)
	if !errors.Is(err, ErrTenantNotResolved) {
		t.Fatalf("error = %v, quería ErrTenantNotResolved", err)
	}
	const want = "no se pudo resolver tenant para la sesión: session_id=" + trSession + " (0 filas en fleet_sessions)"
	if err.Error() != want {
		t.Errorf("texto del error = %q, quería %q", err.Error(), want)
	}
	if tenantID != "" || profile != "" {
		t.Errorf("con error devolvió (%q, %q), quería los dos vacíos", tenantID, profile)
	}
	requirePgStatements(t, h.fake, 1)
}

// TestResolveTenant_SeveralTenants_Ambiguous: con filas bajo dos tenants distintos no elige
// ninguno: valores vacíos y el centinela, con el número de tenants en el texto.
func TestResolveTenant_SeveralTenants_Ambiguous(t *testing.T) {
	h := newTRHarness(t, pgReply{rows: [][]driver.Value{{trTenant, false}, {trOther, true}}})

	tenantID, profile, err := h.resolver.ResolveTenant(t.Context(), trSession)
	if !errors.Is(err, ErrTenantNotResolved) {
		t.Fatalf("error = %v, quería ErrTenantNotResolved", err)
	}
	const want = "no se pudo resolver tenant para la sesión: session_id=" + trSession + " ambiguo (2 tenants)"
	if err.Error() != want {
		t.Errorf("texto del error = %q, quería %q", err.Error(), want)
	}
	if tenantID != "" || profile != "" {
		t.Errorf("con error devolvió (%q, %q), quería los dos vacíos", tenantID, profile)
	}
}

// TestResolveTenant_DatabaseFailures_WrappedAndNotTheSentinel: un fallo de la base —al consultar,
// al leer una fila o al recorrerlas— vuelve envuelto con %w tras su prefijo, con los valores
// vacíos, y NO casa con ErrTenantNotResolved (no es «la sesión no existe»).
func TestResolveTenant_DatabaseFailures_WrappedAndNotTheSentinel(t *testing.T) {
	boom := errors.New("la base se cayó")
	cases := []struct {
		name   string
		reply  pgReply
		prefix string
		cause  error
	}{
		{"the query fails", pgReply{err: boom}, "resolver tenant: consulta fleet_sessions: ", boom},
		// Una fila cuyo agregado no es un booleano: Scan no puede leerla.
		{"a row cannot be scanned", pgReply{rows: [][]driver.Value{{trTenant, "no es un booleano"}}}, "resolver tenant: scan: ", nil},
		{"iterating the rows fails", pgReply{rows: [][]driver.Value{{trTenant, false}}, iterErr: boom}, "resolver tenant: iterar filas: ", boom},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newTRHarness(t, c.reply)

			tenantID, profile, err := h.resolver.ResolveTenant(t.Context(), trSession)
			if err == nil {
				t.Fatal("ResolveTenant no devolvió error")
			}
			if !strings.HasPrefix(err.Error(), c.prefix) {
				t.Errorf("texto del error = %q, quería el prefijo %q", err.Error(), c.prefix)
			}
			if c.cause != nil && !errors.Is(err, c.cause) {
				t.Errorf("el error %v no envuelve la causa %v", err, c.cause)
			}
			if errors.Is(err, ErrTenantNotResolved) {
				t.Errorf("un fallo de la base casa con ErrTenantNotResolved: %v", err)
			}
			if tenantID != "" || profile != "" {
				t.Errorf("con error devolvió (%q, %q), quería los dos vacíos", tenantID, profile)
			}
		})
	}
}

// TestErrTenantNotResolved_Literal: el texto del centinela es el del viejo, byte a byte.
func TestErrTenantNotResolved_Literal(t *testing.T) {
	const want = "no se pudo resolver tenant para la sesión"
	if got := ErrTenantNotResolved.Error(); got != want {
		t.Errorf("ErrTenantNotResolved = %q, quería %q", got, want)
	}
}
