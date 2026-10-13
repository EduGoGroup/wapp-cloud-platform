package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/infra/memory"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
)

// listSpy envuelve un AuditRepo y anota los argumentos con los que se le llamó a List.
type listSpy struct {
	out.AuditRepo
	limit, offset int
	calls         int
}

func (s *listSpy) List(ctx context.Context, tenantID string, limit, offset int) ([]domain.AuditEvent, error) {
	s.calls++
	s.limit, s.offset = limit, offset
	return s.AuditRepo.List(ctx, tenantID, limit, offset)
}

// failingAuditRepo falla en las dos operaciones.
type failingAuditRepo struct{ err error }

func (f failingAuditRepo) Record(context.Context, domain.AuditEvent) error { return f.err }
func (f failingAuditRepo) List(context.Context, string, int, int) ([]domain.AuditEvent, error) {
	return nil, f.err
}

func newAuditService(t *testing.T, repo out.AuditRepo) *AuditService {
	t.Helper()
	svc, err := NewAuditService(repo)
	if err != nil {
		t.Fatalf("NewAuditService: %v", err)
	}
	return svc
}

// Fail-fast: sin repositorio no se construye, con el texto literal.
func TestNewAuditService_RequiresRepo(t *testing.T) {
	svc, err := NewAuditService(nil)
	if err == nil || svc != nil {
		t.Fatalf("NewAuditService(nil) = %v, %v; quiere nil y error", svc, err)
	}
	if got, want := err.Error(), "iam: AuditService requiere el repositorio de auditoría"; got != want {
		t.Errorf("err = %q; quiere el literal %q", got, want)
	}
}

// Record copia los campos; un tenant informado viaja como puntero a ese valor.
func TestAuditService_Record_CopiesFields(t *testing.T) {
	store := memory.NewAuditStore()
	svc := newAuditService(t, store)
	meta := map[string]any{"endpoint": "/api/v1/roles"}

	err := svc.Record(context.Background(), in.AuditInput{
		TenantID: testTenant, Actor: "actor-1", Action: "roles.create", Resource: "role-9", Result: "ok", Meta: meta,
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	events := store.Events()
	if len(events) != 1 {
		t.Fatalf("eventos = %d; quiere 1", len(events))
	}
	e := events[0]
	if e.TenantID == nil || *e.TenantID != testTenant {
		t.Errorf("TenantID = %v; quiere %q", e.TenantID, testTenant)
	}
	if e.Actor != "actor-1" || e.Action != "roles.create" || e.Resource != "role-9" || e.Result != "ok" {
		t.Errorf("evento = %+v; los campos no llegaron tal cual", e)
	}
	if e.Meta["endpoint"] != "/api/v1/roles" {
		t.Errorf("Meta = %v; quiere el de la entrada", e.Meta)
	}
}

// Un tenant vacío es un evento pre-auth: se persiste como NULL.
func TestAuditService_Record_EmptyTenantIsNull(t *testing.T) {
	store := memory.NewAuditStore()
	svc := newAuditService(t, store)
	if err := svc.Record(context.Background(), in.AuditInput{Actor: "unknown", Action: "auth.exchange"}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	events := store.Events()
	if len(events) != 1 || events[0].TenantID != nil {
		t.Fatalf("eventos = %+v; quiere uno con TenantID nil (NULL)", events)
	}
}

// Sin acción es entrada inválida y no se escribe nada.
func TestAuditService_Record_EmptyActionIsInvalidAndWritesNothing(t *testing.T) {
	store := memory.NewAuditStore()
	svc := newAuditService(t, store)
	err := svc.Record(context.Background(), in.AuditInput{TenantID: testTenant, Actor: "a"})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("err = %v; quiere ErrInvalidInput", err)
	}
	if n := len(store.Events()); n != 0 {
		t.Errorf("se escribieron %d eventos; quiere 0", n)
	}
}

// Un error del repositorio sube tal cual, en las dos operaciones.
func TestAuditService_RepoErrorPropagates(t *testing.T) {
	boom := errors.New("la bitácora no contesta")
	svc := newAuditService(t, failingAuditRepo{err: boom})
	if err := svc.Record(context.Background(), in.AuditInput{Action: "x"}); !errors.Is(err, boom) {
		t.Errorf("Record: err = %v; quiere el del repositorio", err)
	}
	if _, err := svc.ListAudit(context.Background(), testTenant, 10, 0); !errors.Is(err, boom) {
		t.Errorf("ListAudit: err = %v; quiere el del repositorio", err)
	}
}

// R-U30: límite por defecto 100 con limit<=0; offset negativo a 0; el resto viaja tal cual.
func TestAuditService_ListAudit_DefaultLimitAndOffset(t *testing.T) {
	cases := []struct {
		name                  string
		limit, offset         int
		wantLimit, wantOffset int
	}{
		{"zero_limit_takes_default", 0, 0, 100, 0},
		{"negative_limit_takes_default", -5, 3, 100, 3},
		{"negative_offset_is_zero", 7, -1, 7, 0},
		{"explicit_values_travel", 250, 40, 250, 40},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spy := &listSpy{AuditRepo: memory.NewAuditStore()}
			svc := newAuditService(t, spy)
			if _, err := svc.ListAudit(context.Background(), testTenant, c.limit, c.offset); err != nil {
				t.Fatalf("ListAudit: %v", err)
			}
			if spy.calls != 1 || spy.limit != c.wantLimit || spy.offset != c.wantOffset {
				t.Fatalf("List(limit=%d, offset=%d) en %d llamadas; quiere limit=%d offset=%d",
					spy.limit, spy.offset, spy.calls, c.wantLimit, c.wantOffset)
			}
		})
	}
}

// R-U30 por su efecto: con 101 eventos y sin límite salen 100, del tenant pedido.
func TestAuditService_ListAudit_DefaultLimitCapsTheList(t *testing.T) {
	store := memory.NewAuditStore()
	svc := newAuditService(t, store)
	ctx := context.Background()
	for range defaultAuditLimit + 1 {
		if err := svc.Record(ctx, in.AuditInput{TenantID: testTenant, Action: "a"}); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	if err := svc.Record(ctx, in.AuditInput{TenantID: "otra-empresa", Action: "a"}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	got, err := svc.ListAudit(ctx, testTenant, 0, 0)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(got) != 100 {
		t.Fatalf("ListAudit sin límite devolvió %d; quiere 100", len(got))
	}
	for _, e := range got {
		if e.TenantID == nil || *e.TenantID != testTenant {
			t.Fatalf("se coló un evento de otra empresa: %+v", e)
		}
	}
}
