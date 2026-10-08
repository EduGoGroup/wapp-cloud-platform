package intakes

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// Los recordatorios PEREZOSOS de las lecturas (R-05). Los dobles y la siembra están en
// service_test.go.

// svcTouchOptions cablea los colaboradores pedidos y devuelve sus espías (nil el no cableado).
func svcTouchOptions(deposit, expiry bool) ([]Option, *svcDepositSpy, *svcExpirySpy) {
	var (
		opts []Option
		d    *svcDepositSpy
		e    *svcExpirySpy
	)
	if deposit {
		d = &svcDepositSpy{}
		opts = append(opts, WithDepositReminder(d))
	}
	if expiry {
		e = &svcExpirySpy{}
		opts = append(opts, WithExpiryReminder(e))
	}
	return opts, d, e
}

// TestService_Touch_RemindersAreIndependent es R-05: List y Get preguntan por CADA recordatorio
// por separado. Cablear solo el del plazo tiene que funcionar — una guarda que saliera al faltar
// el de la seña lo dejaría mudo y en verde.
func TestService_Touch_RemindersAreIndependent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name            string
		deposit, expiry bool
	}{
		{name: "none wired"},
		{name: "only the deposit reminder", deposit: true},
		{name: "only the expiry reminder", expiry: true},
		{name: "both wired", deposit: true, expiry: true},
	}
	reads := []struct {
		name string
		read func(*Service) error
	}{
		{name: "List", read: func(s *Service) error {
			_, err := s.List(context.Background(), svcTenantA, Filter{})
			return err
		}},
		{name: "Get", read: func(s *Service) error {
			_, err := s.Get(context.Background(), svcTenantA, svcIntakeID)
			return err
		}},
	}
	for _, c := range cases {
		for _, r := range reads {
			t.Run(c.name+"/"+r.name, func(t *testing.T) {
				t.Parallel()
				opts, deposit, expiry := svcTouchOptions(c.deposit, c.expiry)
				svc := NewService(svcSeedStore(t, StatusDepositRequested), opts...)

				if err := r.read(svc); err != nil {
					t.Fatalf("%s devolvió el error %v", r.name, err)
				}
				if deposit != nil {
					svcWantOneTouch(t, "seña", deposit.calls)
				}
				if expiry != nil {
					svcWantOneTouch(t, "plazo", expiry.calls)
				}
			})
		}
	}
}

// svcWantOneTouch exige UN toque, del tenant A y con la solicitud sembrada y solo ella.
func svcWantOneTouch(t *testing.T, which string, calls []svcTouch) {
	t.Helper()
	if len(calls) != 1 {
		t.Fatalf("el recordatorio de %s recibió %d toques, quería exactamente 1", which, len(calls))
	}
	got := calls[0]
	if got.tenantID != svcTenantA || len(got.touched) != 1 || got.touched[0].ID != svcIntakeID {
		t.Errorf("el recordatorio de %s recibió (%q, %d solicitudes), quería el tenant A con la sembrada", which, got.tenantID, len(got.touched))
	}
}

// TestService_Touch_OnlyWhatThePageRead: el toque va DESPUÉS de leer y lleva exactamente las
// solicitudes de la página, no todas las del filtro.
func TestService_Touch_OnlyWhatThePageRead(t *testing.T) {
	t.Parallel()
	st := svcSeedStore(t, StatusOpen)
	st.Add(svcTenantA, svcIntake(svcOtherID, StatusOpen))
	opts, deposit, expiry := svcTouchOptions(true, true)
	svc := NewService(st, opts...)

	page, err := svc.List(context.Background(), svcTenantA, Filter{Page: 2, PageSize: 1})
	if err != nil {
		t.Fatalf("List devolvió el error %v", err)
	}
	if page.Total != 2 || len(page.Intakes) != 1 {
		t.Fatalf("la página trae %d de %d, quería 1 de 2", len(page.Intakes), page.Total)
	}
	for which, calls := range map[string][]svcTouch{"seña": deposit.calls, "plazo": expiry.calls} {
		if len(calls) != 1 || !reflect.DeepEqual(calls[0].touched, page.Intakes) {
			t.Errorf("el recordatorio de %s recibió %+v, quería exactamente la página leída", which, calls)
		}
	}
}

// TestService_Touch_NothingReadTouchesNobody: una página vacía, una solicitud ajena o una
// lectura que falla no evalúan ningún recordatorio.
func TestService_Touch_NothingReadTouchesNobody(t *testing.T) {
	t.Parallel()
	errDown := errors.New("base caída")
	list := func(tenantID string) func(*Service) error {
		return func(s *Service) error {
			_, err := s.List(context.Background(), tenantID, Filter{})
			return err
		}
	}
	get := func(tenantID string) func(*Service) error {
		return func(s *Service) error {
			_, err := s.Get(context.Background(), tenantID, svcIntakeID)
			return err
		}
	}
	cases := []struct {
		name  string
		store func(t *testing.T) Store
		read  func(*Service) error
		// wantErr: el error con que acaba la lectura (nil si va bien).
		wantErr error
	}{
		{
			name:  "empty page",
			store: func(t *testing.T) Store { return svcSeedStore(t, StatusOpen) },
			read:  list(svcTenantB),
		},
		{
			name:  "intake of another tenant",
			store: func(t *testing.T) Store { return svcSeedStore(t, StatusOpen) },
			read:  get(svcTenantB), wantErr: ErrNotFound,
		},
		{
			name:  "failed list",
			store: func(*testing.T) Store { return svcFailingStore{err: errDown} },
			read:  list(svcTenantA), wantErr: errDown,
		},
		{
			name:  "failed get",
			store: func(*testing.T) Store { return svcFailingStore{err: errDown} },
			read:  get(svcTenantA), wantErr: errDown,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			opts, deposit, expiry := svcTouchOptions(true, true)
			if err := c.read(NewService(c.store(t), opts...)); !errors.Is(err, c.wantErr) {
				t.Fatalf("la lectura acabó con el error %v, quería %v", err, c.wantErr)
			}
			if len(deposit.calls) != 0 || len(expiry.calls) != 0 {
				t.Errorf("toques = (seña %d, plazo %d), quería (0, 0): sin filas leídas no hay nada que evaluar", len(deposit.calls), len(expiry.calls))
			}
		})
	}
}

// TestService_Touch_ExportAndSummaryDoNotTouch: descargar una hoja de cálculo no le manda
// WhatsApps a los clientes del dueño.
func TestService_Touch_ExportAndSummaryDoNotTouch(t *testing.T) {
	t.Parallel()
	opts, deposit, expiry := svcTouchOptions(true, true)
	svc := NewService(svcSeedStore(t, StatusDepositRequested), opts...)

	details, err := svc.ListDetails(context.Background(), svcTenantA, Filter{})
	if err != nil || len(details) != 1 {
		t.Fatalf("ListDetails devolvió (%d, %v), quería (1, nil)", len(details), err)
	}
	if _, err := svc.Summary(context.Background(), svcTenantA, Filter{}); err != nil {
		t.Fatalf("Summary devolvió el error %v", err)
	}
	if len(deposit.calls) != 0 || len(expiry.calls) != 0 {
		t.Errorf("toques = (seña %d, plazo %d), quería (0, 0): el export y el resumen no son un toque", len(deposit.calls), len(expiry.calls))
	}
}
