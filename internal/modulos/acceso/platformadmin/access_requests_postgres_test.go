package platformadmin

import (
	"context"
	"database/sql/driver"
	"errors"
	"slices"
	"strings"
	"testing"

	iamdomain "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
)

// Este test es INTERNO y SIN BASE: el SQL de verdad lo prueba platformadminhelpertest.Contrato
// contra Postgres (test/procesos/platformadmin_contrato_test.go). Aquí se prueba lo que el
// adaptador DECIDE alrededor de su SQL —validar antes de consultar, traducir «sin fila» y «0
// filas» a centinelas, y sobre todo el orden y el desenlace de la transacción de
// ExecuteApprovalTx (nivel complejo: mutantes del orden GrantTenantAccess → UPDATE, de 0 filas →
// ErrConflict y del ROLLBACK)— con un driver de database/sql en memoria (fakeSQL) que contesta
// lo que cada caso programa y registra cada sentencia, BEGIN, COMMIT y ROLLBACK.

// Repository implementa los dos puertos.
var _ AccessRequestStore = (*Repository)(nil)

// ── ExecuteApprovalTx: la transacción (nivel complejo) ───────────────────────────────────────

const (
	reqID    = "6f0f3c2e-8d1b-4a57-9c64-2b7a1d5e9f10"
	tenantID = "11111111-2222-4333-8444-555555555555"
	userID   = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	roleID   = "10000000-0000-0000-0000-000000000002"
	operator = "0b5c6f40-7d1e-4a3b-9c2d-1e2f3a4b5c6d"
)

// features de los tests: concede o no multi_empresa.
type multiCompany bool

func (m multiCompany) Has(context.Context, string, string) (bool, error) { return bool(m), nil }

func wantSteps(t *testing.T, f *fakeSQL, want ...string) {
	t.Helper()
	if got := f.Steps(); !slices.Equal(got, want) {
		t.Fatalf("pasos = %v\nquiero  %v", got, want)
	}
}

// El orden de la aprobación: BEGIN, el alta COMPLETA de GrantTenantAccess (cerrojo, conteo,
// membresía, rol) DENTRO de la transacción, el UPDATE de la solicitud y COMMIT. Nada de ROLLBACK.
func TestExecuteApprovalTx_GrantThenApproveThenCommit(t *testing.T) {
	f := newFakeSQL()
	if err := f.repo(t, nil).ExecuteApprovalTx(context.Background(), reqID, tenantID, userID, roleID, operator); err != nil {
		t.Fatalf("ExecuteApprovalTx: %v", err)
	}
	wantSteps(t, f, "BEGIN", "lock", "count", "insert-member", "insert-role", "approve", "COMMIT")
	if args := f.argsOf("insert-role"); len(args) != 3 || args[0] != userID || args[1] != roleID || args[2] != tenantID {
		t.Fatalf("el rol se asigna con %v, quiero (user, role, tenant)", args)
	}
	if args := f.argsOf("approve"); len(args) != 2 || args[0] != operator || args[1] != reqID {
		t.Fatalf("el UPDATE recibe %v, quiero (operador, solicitud)", args)
	}
}

// decided_by: un operador que no es UUID se guarda como NULL.
func TestExecuteApprovalTx_OperatorNotUUID_DecidedByNull(t *testing.T) {
	f := newFakeSQL()
	if err := f.repo(t, nil).ExecuteApprovalTx(context.Background(), reqID, tenantID, userID, roleID, "operator-test"); err != nil {
		t.Fatalf("ExecuteApprovalTx: %v", err)
	}
	if args := f.argsOf("approve"); len(args) != 2 || args[0] != nil {
		t.Fatalf("el UPDATE recibe %v, quiero decided_by NULL", args)
	}
}

// 0 filas en el UPDATE (la solicitud no existe o ya no está pendiente), o no poder contarlas ⇒
// ErrConflict y ROLLBACK: el alta que GrantTenantAccess ya escribió se deshace.
func TestExecuteApprovalTx_NoRowUpdated_ConflictAndRollback(t *testing.T) {
	for _, c := range []struct {
		name string
		rule fakeRule
	}{
		{"ZeroRows", fakeRule{match: "SET status = 'approved'", affected: 0}},
		{"RowsAffectedFails", fakeRule{match: "SET status = 'approved'", affected: 1, affectedErr: errors.New("sin cuenta")}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeSQL(c.rule)
			err := f.repo(t, nil).ExecuteApprovalTx(context.Background(), reqID, tenantID, userID, roleID, operator)
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("err = %v, quiero ErrConflict", err)
			}
			wantSteps(t, f, "BEGIN", "lock", "count", "insert-member", "insert-role", "approve", "ROLLBACK")
		})
	}
}

// La regla de una sola empresa: miembro de otra y sin multi_empresa ⇒ el ErrConflict de iam sale
// como ErrConflict de platformadmin, sin escribir nada (ni membresía ni UPDATE) y con ROLLBACK.
// Con multi_empresa en la de destino (el resolver del constructor llega a GrantTenantAccess), pasa.
func TestExecuteApprovalTx_OtherCompany(t *testing.T) {
	other := fakeRule{match: "SELECT count(*)", rows: [][]driver.Value{{int64(1)}}}
	t.Run("WithoutMultiCompany_Conflict", func(t *testing.T) {
		f := newFakeSQL(other)
		err := f.repo(t, multiCompany(false)).ExecuteApprovalTx(context.Background(), reqID, tenantID, userID, roleID, operator)
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("err = %v, quiero ErrConflict de platformadmin", err)
		}
		if errors.Is(err, iamdomain.ErrConflict) {
			t.Fatalf("err = %v: sale el centinela de PLATFORMADMIN, no el de iam", err)
		}
		wantSteps(t, f, "BEGIN", "lock", "count", "ROLLBACK")
	})
	t.Run("NilResolver_Conflict", func(t *testing.T) {
		f := newFakeSQL(other)
		if err := f.repo(t, nil).ExecuteApprovalTx(context.Background(), reqID, tenantID, userID, roleID, operator); !errors.Is(err, ErrConflict) {
			t.Fatalf("err = %v, quiero ErrConflict (resolver nil = «no»)", err)
		}
	})
	t.Run("WithMultiCompany_Writes", func(t *testing.T) {
		f := newFakeSQL(other)
		if err := f.repo(t, multiCompany(true)).ExecuteApprovalTx(context.Background(), reqID, tenantID, userID, roleID, operator); err != nil {
			t.Fatalf("err = %v, quiero nil: el resolver del constructor tiene que llegar a GrantTenantAccess", err)
		}
		wantSteps(t, f, "BEGIN", "lock", "count", "insert-member", "insert-role", "approve", "COMMIT")
	})
}

// R-A7 (sin base): si el alta del rol falla tras escribir la membresía, el error sale tal cual
// (no es un conflicto), NO se marca la solicitud y se hace ROLLBACK. Igual con un fallo del
// UPDATE, que sale envuelto.
func TestExecuteApprovalTx_FailureMidway_RollsBackEverything(t *testing.T) {
	boom := errors.New("violación de clave foránea")
	t.Run("RoleInsertFails", func(t *testing.T) {
		f := newFakeSQL(fakeRule{match: "INSERT INTO public.iam_user_roles", err: boom})
		err := f.repo(t, nil).ExecuteApprovalTx(context.Background(), reqID, tenantID, userID, roleID, operator)
		if !errors.Is(err, boom) || errors.Is(err, ErrConflict) {
			t.Fatalf("err = %v, quiero el fallo del alta, no ErrConflict", err)
		}
		wantSteps(t, f, "BEGIN", "lock", "count", "insert-member", "insert-role", "ROLLBACK")
	})
	t.Run("UpdateFails", func(t *testing.T) {
		f := newFakeSQL(fakeRule{match: "SET status = 'approved'", err: boom})
		err := f.repo(t, nil).ExecuteApprovalTx(context.Background(), reqID, tenantID, userID, roleID, operator)
		if !errors.Is(err, boom) || !strings.HasPrefix(err.Error(), "platformadmin: update access request status: ") {
			t.Fatalf("err = %v, quiero el fallo envuelto con «platformadmin: update access request status: »", err)
		}
		wantSteps(t, f, "BEGIN", "lock", "count", "insert-member", "insert-role", "approve", "ROLLBACK")
	})
}

// No poder abrir ni cerrar la transacción sale envuelto con su prefijo.
func TestExecuteApprovalTx_BeginAndCommitFailures(t *testing.T) {
	boom := errors.New("conexión perdida")
	t.Run("Begin", func(t *testing.T) {
		f := newFakeSQL()
		f.beginErr = boom
		err := f.repo(t, nil).ExecuteApprovalTx(context.Background(), reqID, tenantID, userID, roleID, operator)
		if !errors.Is(err, boom) || !strings.HasPrefix(err.Error(), "platformadmin: begin tx: ") {
			t.Fatalf("err = %v", err)
		}
		wantSteps(t, f)
	})
	t.Run("Commit", func(t *testing.T) {
		f := newFakeSQL()
		f.commitErr = boom
		err := f.repo(t, nil).ExecuteApprovalTx(context.Background(), reqID, tenantID, userID, roleID, operator)
		if !errors.Is(err, boom) || !strings.HasPrefix(err.Error(), "platformadmin: commit tx: ") {
			t.Fatalf("err = %v", err)
		}
	})
}

// ── el resto del puerto: validación previa y traducción de errores ───────────────────────────

// CreateAccessRequest y RejectAccessRequest validan ANTES de consultar: un argumento inválido no
// llega a la base.
func TestAccessRequestPostgres_ValidatesBeforeTheDB(t *testing.T) {
	f := newFakeSQL()
	r := f.repo(t, nil)
	ctx := context.Background()
	for _, c := range []struct{ user, email, origin string }{{"", "a@x.com", "bff"}, {userID, "", "bff"}, {userID, "a@x.com", "web"}} {
		if err := r.CreateAccessRequest(ctx, c.user, c.email, c.origin); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("CreateAccessRequest(%q, %q, %q) = %v, quiero ErrInvalidInput", c.user, c.email, c.origin, err)
		}
	}
	for _, c := range []struct{ id, reason string }{{"", "motivo"}, {reqID, ""}, {reqID, " \t "}} {
		if err := r.RejectAccessRequest(ctx, c.id, c.reason, operator); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("RejectAccessRequest(%q, %q) = %v, quiero ErrInvalidInput", c.id, c.reason, err)
		}
	}
	wantSteps(t, f)
}

// CreateAccessRequest escribe 'pending' con ON CONFLICT … DO NOTHING (la idempotencia la da el
// índice parcial) y envuelve el fallo.
func TestCreateAccessRequest_InsertAndError(t *testing.T) {
	f := newFakeSQL()
	if err := f.repo(t, nil).CreateAccessRequest(context.Background(), userID, "a@x.com", "edge"); err != nil {
		t.Fatalf("CreateAccessRequest: %v", err)
	}
	if args := f.argsOf("insert-request"); !slices.Equal(args, []driver.Value{userID, "a@x.com", "edge"}) {
		t.Fatalf("argumentos = %v", args)
	}
	boom := errors.New("bd caída")
	g := newFakeSQL(fakeRule{match: "INSERT INTO public.access_requests", err: boom})
	err := g.repo(t, nil).CreateAccessRequest(context.Background(), userID, "a@x.com", "edge")
	if !errors.Is(err, boom) || !strings.HasPrefix(err.Error(), "platformadmin: create access request: ") {
		t.Fatalf("err = %v", err)
	}
}

// RejectAccessRequest (M-06): 0 filas y la solicitud no existe ⇒ ErrNotFound; existe ⇒
// ErrConflict; no se pudo comprobar ⇒ el fallo envuelto, NUNCA ErrNotFound. Un fallo del UPDATE,
// envuelto. Con una fila, nil y sin segunda consulta.
func TestRejectAccessRequest_Outcomes(t *testing.T) {
	zero := fakeRule{match: "SET status = 'rejected'", affected: 0}
	boom := errors.New("bd caída")
	for _, c := range []struct {
		name   string
		rules  []fakeRule
		want   error
		prefix string
		steps  []string
	}{
		{"Updated", nil, nil, "", []string{"reject"}},
		{"Missing_NotFound", []fakeRule{zero, {match: "SELECT true FROM", rows: nil}}, ErrNotFound, "", []string{"reject", "exists-request"}},
		{"Decided_Conflict", []fakeRule{zero, {match: "SELECT true FROM", rows: [][]driver.Value{{true}}}}, ErrConflict, "", []string{"reject", "exists-request"}},
		{"ExistenceFails_Wrapped", []fakeRule{zero, {match: "SELECT true FROM", err: boom}}, boom,
			"platformadmin: check access request existence: ", []string{"reject", "exists-request"}},
		{"UpdateFails_Wrapped", []fakeRule{{match: "SET status = 'rejected'", err: boom}}, boom, "platformadmin: reject access request: ", []string{"reject"}},
		{"RowsAffectedFails_Wrapped", []fakeRule{{match: "SET status = 'rejected'", affectedErr: boom}}, boom, "platformadmin: rows affected: ", []string{"reject"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeSQL(c.rules...)
			err := f.repo(t, nil).RejectAccessRequest(context.Background(), reqID, "motivo", "no-uuid")
			if !errors.Is(err, c.want) || (c.want == nil && err != nil) {
				t.Fatalf("err = %v, quiero %v", err, c.want)
			}
			if c.prefix != "" && (!strings.HasPrefix(err.Error(), c.prefix) || errors.Is(err, ErrNotFound)) {
				t.Fatalf("err = %v, quiero el prefijo %q y no ErrNotFound", err, c.prefix)
			}
			wantSteps(t, f, c.steps...)
			if args := f.argsOf("reject"); len(args) != 3 || args[0] != "motivo" || args[1] != nil || args[2] != reqID {
				t.Fatalf("el UPDATE recibe %v, quiero (motivo, NULL, solicitud)", args)
			}
		})
	}
}

// LookupAccessRequestStatus y ResolveRoleID traducen «sin fila» a su centinela y envuelven el resto.
func TestLookupAndResolveRole_NoRowAndErrors(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("bd caída")

	f := newFakeSQL(fakeRule{match: "SELECT user_id::text, status", rows: [][]driver.Value{{userID, "approved"}}})
	if u, s, err := f.repo(t, nil).LookupAccessRequestStatus(ctx, reqID); err != nil || u != userID || s != "approved" {
		t.Fatalf("LookupAccessRequestStatus = (%q, %q, %v)", u, s, err)
	}
	if _, _, err := newFakeSQL().repo(t, nil).LookupAccessRequestStatus(ctx, reqID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("sin fila = %v, quiero ErrNotFound", err)
	}
	g := newFakeSQL(fakeRule{match: "SELECT user_id::text, status", err: boom})
	if _, _, err := g.repo(t, nil).LookupAccessRequestStatus(ctx, reqID); !errors.Is(err, boom) || errors.Is(err, ErrNotFound) ||
		!strings.HasPrefix(err.Error(), "platformadmin: read access request: ") {
		t.Fatalf("fallo = %v, quiero envuelto y no ErrNotFound", err)
	}

	h := newFakeSQL(fakeRule{match: "FROM public.iam_roles", rows: [][]driver.Value{{roleID}}})
	if id, err := h.repo(t, nil).ResolveRoleID(ctx, "operator"); err != nil || id != roleID {
		t.Fatalf("ResolveRoleID = (%q, %v)", id, err)
	}
	if args := h.argsOf("role-id"); !slices.Equal(args, []driver.Value{"operator"}) {
		t.Fatalf("argumentos = %v (un solo $1 para nombre e id)", args)
	}
	if _, err := newFakeSQL().repo(t, nil).ResolveRoleID(ctx, "nada"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("sin fila = %v, quiero ErrInvalidInput", err)
	}
	k := newFakeSQL(fakeRule{match: "FROM public.iam_roles", err: boom})
	if _, err := k.repo(t, nil).ResolveRoleID(ctx, "operator"); !errors.Is(err, boom) || errors.Is(err, ErrInvalidInput) ||
		!strings.HasPrefix(err.Error(), "platformadmin: resolve role: ") {
		t.Fatalf("fallo = %v, quiero envuelto y no ErrInvalidInput", err)
	}
}

// CheckRetryApproved: membresía primero (no ⇒ ErrConflict SIN preguntar por el rol), rol después
// (no ⇒ ErrRetryRoleMismatch); los fallos, envueltos.
func TestCheckRetryApproved_OrderAndOutcomes(t *testing.T) {
	member := func(v bool) fakeRule {
		return fakeRule{match: "FROM public.tenant_members WHERE user_id = $1 AND tenant_id = $2", rows: [][]driver.Value{{v}}}
	}
	role := func(v bool) fakeRule {
		return fakeRule{match: "FROM public.iam_user_roles WHERE", rows: [][]driver.Value{{v}}}
	}
	boom := errors.New("bd caída")
	for _, c := range []struct {
		name   string
		rules  []fakeRule
		want   error
		prefix string
		steps  []string
	}{
		{"Converges", []fakeRule{member(true), role(true)}, nil, "", []string{"retry-member", "retry-role"}},
		{"NotMember_Conflict", []fakeRule{member(false), role(true)}, ErrConflict, "", []string{"retry-member"}},
		{"OtherRole_Mismatch", []fakeRule{member(true), role(false)}, ErrRetryRoleMismatch, "", []string{"retry-member", "retry-role"}},
		{"MemberFails", []fakeRule{{match: "FROM public.tenant_members WHERE user_id = $1 AND tenant_id = $2", err: boom}}, boom,
			"platformadmin: check retry membership: ", []string{"retry-member"}},
		{"RoleFails", []fakeRule{member(true), {match: "FROM public.iam_user_roles WHERE", err: boom}}, boom,
			"platformadmin: check retry role: ", []string{"retry-member", "retry-role"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeSQL(c.rules...)
			err := f.repo(t, nil).CheckRetryApproved(context.Background(), userID, tenantID, roleID)
			if !errors.Is(err, c.want) || (c.want == nil && err != nil) {
				t.Fatalf("err = %v, quiero %v", err, c.want)
			}
			if c.prefix != "" && !strings.HasPrefix(err.Error(), c.prefix) {
				t.Fatalf("err = %v, quiero el prefijo %q", err, c.prefix)
			}
			wantSteps(t, f, c.steps...)
		})
	}
}

// wantListedRow exige la fila programada en TestListAccessRequests_StatusDefaultAndShape, con
// Systems [] y SystemsKnown false (C-05).
func wantListedRow(t *testing.T, it AccessRequestItem) {
	t.Helper()
	got := []string{it.ID, it.UserID, it.Email, it.Origin, it.Status}
	if !slices.Equal(got, []string{reqID, userID, "a@x.com", "bff", "rejected"}) || !it.CreatedAt.Equal(fixedNow) {
		t.Fatalf("fila = %+v", it)
	}
	if it.Systems == nil || len(it.Systems) != 0 || it.SystemsKnown {
		t.Fatalf("Systems = %#v, SystemsKnown = %v; quiero [] y false", it.Systems, it.SystemsKnown)
	}
}

// ListAccessRequests: "" se pregunta como 'pending'; sin filas, arreglo vacío; cada fila con
// Systems [] y SystemsKnown false; los fallos, envueltos.
func TestListAccessRequests_StatusDefaultAndShape(t *testing.T) {
	ctx := context.Background()
	f := newFakeSQL()
	items, err := f.repo(t, nil).ListAccessRequests(ctx, "")
	if err != nil || items == nil || len(items) != 0 {
		t.Fatalf("ListAccessRequests vacío = (%#v, %v), quiero [] sin error", items, err)
	}
	if args := f.argsOf("list"); !slices.Equal(args, []driver.Value{"pending"}) {
		t.Fatalf("status preguntado = %v, quiero pending", args)
	}
	g := newFakeSQL(fakeRule{match: "WHERE status = $1", rows: [][]driver.Value{{reqID, userID, "a@x.com", "bff", "rejected", fixedNow}}})
	items, err = g.repo(t, nil).ListAccessRequests(ctx, "rejected")
	if err != nil || len(items) != 1 {
		t.Fatalf("ListAccessRequests = (%+v, %v)", items, err)
	}
	wantListedRow(t, items[0])
	boom := errors.New("bd caída")
	h := newFakeSQL(fakeRule{match: "WHERE status = $1", err: boom})
	if _, err := h.repo(t, nil).ListAccessRequests(ctx, "pending"); !errors.Is(err, boom) ||
		!strings.HasPrefix(err.Error(), "platformadmin: list access requests: ") {
		t.Fatalf("fallo = %v", err)
	}
}
