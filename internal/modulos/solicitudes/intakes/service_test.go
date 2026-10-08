package intakes

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"
)

// Las lecturas del Service, su reloj (D-F6-5) y su cableado. La siembra y los dobles que
// comparten todos los tests del Service están en service_doubles_test.go.

// Los dobles de service_doubles_test.go satisfacen los puertos que declara service.go (y el de
// metricas.go): si una firma del contrato cambia, esto deja de compilar.
var (
	_ StatusNotifier   = (*svcNotifierSpy)(nil)
	_ DepositTouch     = (*svcDepositSpy)(nil)
	_ ExpiryTouch      = (*svcExpirySpy)(nil)
	_ CRMPusher        = (*svcCRMSpy)(nil)
	_ QuoteSender      = (*svcQuoteSpy)(nil)
	_ MetricsPublisher = (*svcMetricsSpy)(nil)
)

// TestService_List_NormalizesPagination: la cota superior no es cosmética — sin ella un solo
// GET puede pedir la tabla entera. La Page devuelve los valores YA saneados.
func TestService_List_NormalizesPagination(t *testing.T) {
	t.Parallel()
	svc := NewService(svcSeedStore(t, StatusOpen))

	page, err := svc.List(context.Background(), svcTenantA, Filter{Page: 0, PageSize: 100000})
	if err != nil {
		t.Fatalf("List devolvió el error %v", err)
	}
	if page.Page != 1 || page.PageSize != MaxPageSize {
		t.Errorf("página (%d, %d), quería (1, %d): la paginación se sanea antes de consultar", page.Page, page.PageSize, MaxPageSize)
	}
	if page.Total != 1 || len(page.Intakes) != 1 || page.Intakes[0].ID != svcIntakeID {
		t.Errorf("List devolvió total=%d y %d filas, quería la solicitud sembrada", page.Total, len(page.Intakes))
	}
}

// TestService_List_EmptyIsNotNil: sin coincidencias la lista es VACÍA, no nil, y la paginación
// sale con sus valores por defecto.
func TestService_List_EmptyIsNotNil(t *testing.T) {
	t.Parallel()
	svc := NewService(svcSeedStore(t, StatusOpen))

	page, err := svc.List(context.Background(), svcTenantB, Filter{})
	if err != nil {
		t.Fatalf("List devolvió el error %v", err)
	}
	if page.Intakes == nil || len(page.Intakes) != 0 {
		t.Errorf("Intakes = %#v, quería una lista vacía y no nil", page.Intakes)
	}
	if page.Page != 1 || page.PageSize != DefaultPageSize || page.Total != 0 {
		t.Errorf("página (%d, %d, total %d), quería (1, %d, total 0)", page.Page, page.PageSize, page.Total, DefaultPageSize)
	}

	// Tampoco con un store que devuelve nil para «ninguna fila» (svcFailingStore sin error).
	page, err = NewService(svcFailingStore{}).List(context.Background(), svcTenantA, Filter{})
	if err != nil || page.Intakes == nil || len(page.Intakes) != 0 {
		t.Errorf("con un store que devuelve nil, List devolvió (%#v, %v); quería una lista vacía y no nil", page.Intakes, err)
	}
}

// TestService_Reads_ReturnTheStoreError: un error del store sale tal cual y con el resultado en cero.
func TestService_Reads_ReturnTheStoreError(t *testing.T) {
	t.Parallel()
	down := errors.New("base caída")
	svc := NewService(svcFailingStore{err: down})

	page, err := svc.List(context.Background(), svcTenantA, Filter{})
	if !errors.Is(err, down) {
		t.Errorf("List devolvió el error %v, quería el del store", err)
	}
	if !reflect.DeepEqual(page, Page{}) {
		t.Errorf("List devolvió %+v junto al error, quería la Page en cero", page)
	}
	if _, err := svc.Get(context.Background(), svcTenantA, svcIntakeID); !errors.Is(err, down) {
		t.Errorf("Get devolvió el error %v, quería el del store", err)
	}
}

// TestService_ListDetails_ExportBound: MaxExportIntakes caben justas; una más es ErrTooLarge. Al
// store se le pide UNA más que la cota, que es lo que permite distinguir los dos casos.
func TestService_ListDetails_ExportBound(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		n       int
		wantErr error
	}{
		{name: "nothing matches", n: 0},
		{name: "exactly the bound fits", n: MaxExportIntakes},
		{name: "one over the bound is too large", n: MaxExportIntakes + 1, wantErr: ErrTooLarge},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			store := &svcBulkStore{n: c.n}
			got, err := NewService(store).ListDetails(context.Background(), svcTenantA, Filter{})
			if store.gotLimit != MaxExportIntakes+1 {
				t.Errorf("al store se le pidió la cota %d, quería %d (una más que el máximo)", store.gotLimit, MaxExportIntakes+1)
			}
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) || got != nil {
					t.Fatalf("ListDetails devolvió (%d filas, %v), quería (nil, %v)", len(got), err, c.wantErr)
				}
				return
			}
			if err != nil || len(got) != c.n {
				t.Fatalf("ListDetails devolvió (%d filas, %v), quería (%d, nil)", len(got), err, c.n)
			}
		})
	}
}

// TestService_ListDetails_ReturnsTheStoreError: el fallo de lectura no se disfraza de ErrTooLarge.
func TestService_ListDetails_ReturnsTheStoreError(t *testing.T) {
	t.Parallel()
	down := errors.New("base caída")
	got, err := NewService(&svcBulkStore{err: down}).ListDetails(context.Background(), svcTenantA, Filter{})
	if !errors.Is(err, down) || got != nil {
		t.Errorf("ListDetails devolvió (%v, %v), quería (nil, el error del store)", got, err)
	}
}

// TestService_Summary_UsesTheInjectedClock es D-F6-5: GeneratedAt es el instante del reloj
// inyectado, exacto, y no un rango alrededor del reloj del sistema. La aritmética es la de
// BuildSummary sobre lo mismo que leería el export.
func TestService_Summary_UsesTheInjectedClock(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 15, 8, 30, 0, 0, time.UTC)
	svc := NewService(svcSeedStore(t, StatusPendingApproval), WithClock(func() time.Time { return now }))

	got, err := svc.Summary(context.Background(), svcTenantA, Filter{})
	if err != nil {
		t.Fatalf("Summary devolvió el error %v", err)
	}
	if !got.GeneratedAt.Equal(now) {
		t.Errorf("GeneratedAt = %v, quería %v: el instante sale del reloj inyectado", got.GeneratedAt, now)
	}
	if got.Intakes != 1 || got.Revenue != 21500 || len(got.Details) != 1 {
		t.Errorf("Summary = (%d solicitudes, %v, %d detalles), quería (1, 21500, 1)", got.Intakes, got.Revenue, len(got.Details))
	}
	if want := map[string]int{StatusPendingApproval: 1}; !reflect.DeepEqual(got.ByStatus, want) {
		t.Errorf("ByStatus = %v, quería %v", got.ByStatus, want)
	}
	wantTop := []TopItem{
		{SKU: "torta-v1", Label: "Torta 10-12 porciones", QtyTotal: 1, Revenue: 18000},
		{SKU: ShippingSKU, Label: "Envío", QtyTotal: 1, Revenue: 3500},
	}
	if !reflect.DeepEqual(got.TopItems, wantTop) {
		t.Errorf("TopItems = %+v, quería %+v", got.TopItems, wantTop)
	}
}

// TestService_WithClock_NilKeepsTheClock: pasar nil no deja al servicio sin reloj; se queda el
// que ya había.
func TestService_WithClock_NilKeepsTheClock(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 15, 8, 30, 0, 0, time.UTC)
	svc := NewService(svcSeedStore(t, StatusOpen), WithClock(func() time.Time { return now }), WithClock(nil))

	got, err := svc.Summary(context.Background(), svcTenantA, Filter{})
	if err != nil {
		t.Fatalf("Summary devolvió el error %v", err)
	}
	if !got.GeneratedAt.Equal(now) {
		t.Errorf("GeneratedAt = %v, quería %v: WithClock(nil) no debe cambiar el reloj", got.GeneratedAt, now)
	}
}

// TestService_Summary_DefaultClockIsSet: sin WithClock hay reloj (time.Now) y Summary no revienta.
func TestService_Summary_DefaultClockIsSet(t *testing.T) {
	t.Parallel()
	got, err := NewService(svcSeedStore(t, StatusOpen)).Summary(context.Background(), svcTenantA, Filter{})
	if err != nil {
		t.Fatalf("Summary devolvió el error %v", err)
	}
	if got.GeneratedAt.IsZero() {
		t.Error("GeneratedAt es el instante cero: el servicio nació sin reloj")
	}
}

// TestService_Summary_SharesTheExportBound: lee por el mismo camino que el export, cota incluida.
func TestService_Summary_SharesTheExportBound(t *testing.T) {
	t.Parallel()
	store := &svcBulkStore{n: MaxExportIntakes + 1}
	_, err := NewService(store).Summary(context.Background(), svcTenantA, Filter{})
	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("Summary devolvió el error %v, quería ErrTooLarge", err)
	}
	if store.gotLimit != MaxExportIntakes+1 {
		t.Errorf("Summary pidió la cota %d, quería %d", store.gotLimit, MaxExportIntakes+1)
	}
}

// TestService_Get: la solicitud con sus líneas; la de otro tenant no existe (INV-8).
func TestService_Get(t *testing.T) {
	t.Parallel()
	svc := NewService(svcSeedStore(t, StatusOpen))

	got, err := svc.Get(context.Background(), svcTenantA, svcIntakeID)
	if err != nil {
		t.Fatalf("Get devolvió el error %v", err)
	}
	if got.ID != svcIntakeID || got.Status != StatusOpen || len(got.Items) != 2 {
		t.Errorf("Get = (%q, %q, %d líneas), quería la solicitud sembrada con sus 2 líneas", got.ID, got.Status, len(got.Items))
	}
	if _, err := svc.Get(context.Background(), svcTenantB, svcIntakeID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get desde otro tenant devolvió %v, quería ErrNotFound", err)
	}
}

// TestNewService_LastOptionWins: las opciones se aplican en el orden en que se pasan; si dos
// tocan el mismo colaborador, gana la última. Recorre los cinco puertos del Service.
func TestNewService_LastOptionWins(t *testing.T) {
	t.Parallel()
	var (
		firstNotifier, lastNotifier = &svcNotifierSpy{}, &svcNotifierSpy{}
		firstDeposit, lastDeposit   = &svcDepositSpy{}, &svcDepositSpy{}
		firstExpiry, lastExpiry     = &svcExpirySpy{}, &svcExpirySpy{}
		firstCRM, lastCRM           = &svcCRMSpy{}, &svcCRMSpy{}
		firstQuotes, lastQuotes     = &svcQuoteSpy{}, &svcQuoteSpy{}
	)
	opts := []Option{
		WithNotifier(firstNotifier), WithDepositReminder(firstDeposit), WithExpiryReminder(firstExpiry),
		WithCRMPusher(firstCRM), WithQuoteSender(firstQuotes),
		WithNotifier(lastNotifier), WithDepositReminder(lastDeposit), WithExpiryReminder(lastExpiry),
		WithCRMPusher(lastCRM), WithQuoteSender(lastQuotes),
	}
	svc := NewService(svcSeedStore(t, StatusPendingApproval), opts...)
	ctx := context.Background()

	// Una acción por colaborador: leer (los dos recordatorios), empujar, preguntar y transicionar.
	actions := []func(*Service) error{
		func(s *Service) error { _, err := s.Get(ctx, svcTenantA, svcIntakeID); return err },
		func(s *Service) error {
			s.PushRevisionToCRM(ctx, svcTenantA, Detail{Intake: svcIntake(svcIntakeID, StatusPendingApproval)}, 1)
			return nil
		},
		func(s *Service) error {
			_, err := s.RequestInfo(ctx, svcTenantA, svcIntakeID, "¿cuántas?")
			return err
		},
		func(s *Service) error {
			_, err := s.SetStatus(ctx, svcTenantA, svcIntakeID, StatusPendingApproval, NoticeToClient)
			return err
		},
	}
	for i, action := range actions {
		if err := action(svc); err != nil {
			t.Fatalf("la acción %d devolvió el error %v", i, err)
		}
	}

	first := len(firstNotifier.calls) + len(firstDeposit.calls) + len(firstExpiry.calls) + len(firstCRM.calls) + len(firstQuotes.questions)
	if first != 0 {
		t.Errorf("los colaboradores sustituidos recibieron %d llamadas, quería 0", first)
	}
	last := []int{len(lastNotifier.calls), len(lastDeposit.calls), len(lastExpiry.calls), len(lastCRM.calls), len(lastQuotes.questions)}
	if !slices.Equal(last, []int{1, 1, 1, 1, 1}) {
		t.Errorf("los últimos colaboradores (aviso, seña, plazo, CRM, voz) recibieron %v llamadas, quería una cada uno", last)
	}
}

// svcShippingStore recuerda cómo se le pidió la línea de envío y contesta `err`.
type svcShippingStore struct {
	Store
	err   error
	calls []string
	got   []ShippingPolicy
}

func (s *svcShippingStore) EnsureShippingLine(_ context.Context, tenantID, intakeID string, policy ShippingPolicy) error {
	s.calls = append(s.calls, tenantID+"|"+intakeID)
	s.got = append(s.got, policy)
	return s.err
}

func (s *svcShippingStore) AbandonByEvent(_ context.Context, tenantID, eventID string) error {
	s.calls = append(s.calls, tenantID+"|"+eventID)
	return s.err
}

// TestService_DelegatesToTheStore: AbandonByEvent y EnsureShippingLine pasan sus argumentos al
// store —la política incluida— y devuelven su error tal cual.
func TestService_DelegatesToTheStore(t *testing.T) {
	t.Parallel()
	down := errors.New("base caída")
	for _, storeErr := range []error{nil, down} {
		st := &svcShippingStore{err: storeErr}
		svc := NewService(st)

		if err := svc.AbandonByEvent(context.Background(), svcTenantA, "evento-1"); !errors.Is(err, storeErr) || (storeErr == nil && err != nil) {
			t.Errorf("AbandonByEvent devolvió %v, quería %v", err, storeErr)
		}
		for _, policy := range []ShippingPolicy{ShippingAlways, ShippingOnlyIfZones} {
			if err := svc.EnsureShippingLine(context.Background(), svcTenantA, svcIntakeID, policy); !errors.Is(err, storeErr) || (storeErr == nil && err != nil) {
				t.Errorf("EnsureShippingLine devolvió %v, quería %v", err, storeErr)
			}
		}
		wantCalls := []string{svcTenantA + "|evento-1", svcTenantA + "|" + svcIntakeID, svcTenantA + "|" + svcIntakeID}
		if !slices.Equal(st.calls, wantCalls) {
			t.Errorf("el store recibió %v, quería %v", st.calls, wantCalls)
		}
		if !slices.Equal(st.got, []ShippingPolicy{ShippingAlways, ShippingOnlyIfZones}) {
			t.Errorf("el store recibió las políticas %v, quería las dos pedidas y en orden", st.got)
		}
	}
}
