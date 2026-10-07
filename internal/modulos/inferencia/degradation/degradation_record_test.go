package degradation_test

// Las promesas de Notifier.Record (R4.5.b, R4.5.c): los rechazos previos al store, lo que llega
// al store y el colapso por ventana. Es la parte de los tests de degradation.go que no cabe en
// degradation_test.go (E-13); las constantes compartidas (testTenant, window, base, wantReasons)
// están allí.

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/degradation"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/degradation/degradationhelpertest"
)

// spyStore es un Store que apunta lo que Record le pasa, tal cual, y contesta lo sembrado.
type spyStore struct {
	notices []degradation.Notice
	ctxs    []context.Context
	created bool
	err     error
}

func (s *spyStore) Save(ctx context.Context, n degradation.Notice) (bool, error) {
	s.notices = append(s.notices, n)
	s.ctxs = append(s.ctxs, ctx)
	return s.created, s.err
}

func (s *spyStore) List(context.Context, string, degradation.ListFilter) ([]degradation.Notice, error) {
	return []degradation.Notice{}, nil
}

// requireSavedOnce afirma que al store llegó UN aviso y que es want: los campos de texto y el
// contador como un valor, los instantes como instantes, y los tres que Record pone, en UTC.
func requireSavedOnce(t *testing.T, spy *spyStore, want degradation.Notice) {
	t.Helper()
	if len(spy.notices) != 1 {
		t.Fatalf("al store llegaron %d avisos, quería 1: %+v", len(spy.notices), spy.notices)
	}
	got := spy.notices[0]
	plain := func(n degradation.Notice) degradation.Notice {
		n.WindowStart, n.WindowEnd, n.ReadAt, n.CreatedAt, n.LastSeenAt = time.Time{}, time.Time{}, time.Time{}, time.Time{}, time.Time{}
		return n
	}
	if plain(got) != plain(want) {
		t.Errorf("aviso = %+v, quería %+v", plain(got), plain(want))
	}
	if !got.WindowStart.Equal(want.WindowStart) || !got.WindowEnd.Equal(want.WindowEnd) || !got.LastSeenAt.Equal(want.LastSeenAt) {
		t.Errorf("ventana [%s, %s) y último visto %s; quería [%s, %s) y %s",
			got.WindowStart, got.WindowEnd, got.LastSeenAt, want.WindowStart, want.WindowEnd, want.LastSeenAt)
	}
	if !got.ReadAt.IsZero() || !got.CreatedAt.IsZero() {
		t.Errorf("Record puso ReadAt (%s) o CreatedAt (%s): esos los decide el store", got.ReadAt, got.CreatedAt)
	}
	for _, instant := range []time.Time{got.WindowStart, got.WindowEnd, got.LastSeenAt} {
		if instant.Location() != time.UTC {
			t.Errorf("Record mandó al store un instante en %s, quería UTC: %s", instant.Location(), instant)
		}
	}
}

// TestRecord_RejectsBeforeTheStore (R4.5.b) es EL test del escritor: un motivo sano, uno
// inventado, una vía fuera del vocabulario o un tenant vacío devuelven su centinela, con su
// texto exacto y en su orden (tenant, motivo, vía), y el store NO SE LLEGA A LLAMAR
// (`saves == 0`), que es una afirmación más fuerte que «la base lo rechazó».
func TestRecord_RejectsBeforeTheStore(t *testing.T) {
	const (
		reasonText = "degradation: motivo fuera del vocabulario cerrado de la degradación"
		viaText    = "degradation: vía fuera del vocabulario cerrado (local|api)"
		tenantText = "degradation: tenant vacío: un aviso sin dueño no es un aviso"
	)
	unknownReason := func(reason string) string {
		return reasonText + ": " + strconv.Quote(reason) + " (los válidos son [" + strings.Join(wantReasons, " ") + "])"
	}
	unknownVia := func(via string) string { return viaText + ": " + strconv.Quote(via) }
	type rejection struct {
		name, tenant, reason, via string
		want                      error
		wantText                  string
	}
	cases := make([]rejection, 0, 32)
	cases = append(cases, []rejection{
		{"empty tenant", "", "timeout", "local", degradation.ErrTenantVacio, tenantText},
		{"empty tenant wins over reason and via", "", "fastlane", "edge", degradation.ErrTenantVacio, tenantText},
		{"unknown reason wins over unknown via", testTenant, "fastlane", "edge", degradation.ErrMotivoDesconocido, unknownReason("fastlane")},
	}...)
	for _, reason := range []string{
		"atajo_determinista", "fastlane", "sin_texto", "umbral_no_alcanzado", "me_lo_acabo_de_inventar",
		"", "TIMEOUT", "timeout\u00a0", "timeout\u0663", `time"out`,
	} {
		cases = append(cases, rejection{"reason " + strconv.Quote(reason), testTenant, reason, "local", degradation.ErrMotivoDesconocido, unknownReason(reason)})
	}
	for _, via := range []string{"edge", "", "API", "api\u00a0", "local\u0661", "ａｐｉ", "anthropic", `a"b`} {
		cases = append(cases, rejection{"via " + strconv.Quote(via), testTenant, "timeout", via, degradation.ErrViaDesconocida, unknownVia(via)})
	}
	sentinels := []error{degradation.ErrTenantVacio, degradation.ErrMotivoDesconocido, degradation.ErrViaDesconocida}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := degradationhelpertest.NewMemoria()
			notifier := degradation.NewNotifier(store, window)
			created, err := notifier.Record(context.Background(), c.tenant, degradation.Reason(c.reason), c.via, base)
			if err == nil || err.Error() != c.wantText {
				t.Errorf("error =\n%v\nquería, byte a byte:\n%s", err, c.wantText)
			}
			for _, sentinel := range sentinels {
				if is := errors.Is(err, sentinel); is != errors.Is(c.want, sentinel) {
					t.Errorf("errors.Is(err, %q) = %v: el rechazo es de otro centinela", sentinel, is)
				}
			}
			if created {
				t.Error("un Record rechazado dijo haber creado un aviso")
			}
			if n := store.Saves(); n != 0 {
				t.Errorf("el store se llamó %d veces: la guarda tiene que estar ANTES", n)
			}
			if rows := store.Rows(c.tenant); len(rows) != 0 {
				t.Errorf("el rechazo dejó %d filas, quería cero", len(rows))
			}
		})
	}
}

// TestRecord_SavesTheWindowedNotice: lo que llega al store es el tenant, el motivo, la vía, la
// ventana de VentanaDe y LastSeenAt = at.UTC() —nada más—, con el ctx del llamante; y lo que el
// store contesta vuelve tal cual, error incluido.
func TestRecord_SavesTheWindowedNotice(t *testing.T) {
	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "el del llamante")
	at := base.Add(7*time.Minute + 3*time.Second).In(time.FixedZone("UTC-5", -5*3600))
	cause := errors.New("la base no está")
	for _, answer := range []struct {
		name    string
		created bool
		err     error
	}{{"created", true, nil}, {"collapsed", false, nil}, {"store failure", false, cause}} {
		t.Run(answer.name, func(t *testing.T) {
			spy := &spyStore{created: answer.created, err: answer.err}
			created, err := degradation.NewNotifier(spy, window).Record(ctx, testTenant, degradation.ReasonAPIError, degradation.ViaAPI, at)
			if created != answer.created || !errors.Is(err, answer.err) || (err != nil && err.Error() != answer.err.Error()) {
				t.Errorf("Record = (%v, %v), quería lo que contestó el store, sin envolver: (%v, %v)", created, err, answer.created, answer.err)
			}
			requireSavedOnce(t, spy, degradation.Notice{
				TenantID: testTenant, Reason: degradation.ReasonAPIError, Via: degradation.ViaAPI,
				WindowStart: base, WindowEnd: base.Add(window), LastSeenAt: at,
			})
			if spy.ctxs[0].Value(ctxKey{}) == nil {
				t.Error("Record no pasó al store el ctx del llamante")
			}
		})
	}
}

// TestRecord_CollapsesTheWindow (R4.5.c, la mitad en memoria): diez fallos repartidos por la
// ventana reciben la MISMA clave —una fila, diez escrituras: el colapso lo hace la clave, no el
// escritor— y el de la ventana siguiente abre aviso nuevo. Otro motivo, otra vía u otro tenant
// en el mismo minuto son avisos distintos.
func TestRecord_CollapsesTheWindow(t *testing.T) {
	store := degradationhelpertest.NewMemoria()
	notifier := degradation.NewNotifier(store, window)
	ctx := context.Background()
	record := func(tenant string, reason degradation.Reason, via string, at time.Time) bool {
		t.Helper()
		created, err := notifier.Record(ctx, tenant, reason, via, at)
		if err != nil {
			t.Fatalf("Record(%s/%s/%s, %s): error inesperado %v", tenant, reason, via, at, err)
		}
		return created
	}
	born := 0
	for i := range 10 { // 0, 89, 178 … segundos: los diez caen dentro de los 15 minutos
		if record(testTenant, degradation.ReasonOllamaDown, degradation.ViaLocal, base.Add(time.Duration(i)*89*time.Second)) {
			born++
		}
	}
	rows := store.Rows(testTenant)
	if born != 1 || len(rows) != 1 || store.Saves() != 10 {
		t.Fatalf("diez fallos sostenidos: %d nacidos, %d filas, %d escrituras; quería 1, 1 y 10 (REQ-38)", born, len(rows), store.Saves())
	}
	if rows[0].Occurrences != 10 || !rows[0].CreatedAt.Equal(base) || !rows[0].LastSeenAt.Equal(base.Add(9*89*time.Second)) {
		t.Errorf("aviso colapsado = %+v, quería 10 fallos, nacido en %s y visto por última vez en %s", rows[0], base, base.Add(9*89*time.Second))
	}
	if !record(testTenant, degradation.ReasonOllamaDown, degradation.ViaLocal, base.Add(20*time.Minute)) {
		t.Error("la ventana siguiente no abrió aviso nuevo: el aviso es por ventana, no eterno")
	}
	for _, other := range []struct {
		tenant string
		reason degradation.Reason
		via    string
	}{
		{testTenant, degradation.ReasonBreakerOpen, degradation.ViaLocal},
		{testTenant, degradation.ReasonOllamaDown, degradation.ViaAPI},
		{"otro-tenant", degradation.ReasonOllamaDown, degradation.ViaLocal},
	} {
		if !record(other.tenant, other.reason, other.via, base.Add(time.Minute)) {
			t.Errorf("Record(%s/%s/%s) se colapsó sobre otro aviso: la clave está incompleta", other.tenant, other.reason, other.via)
		}
	}
	if n := len(store.Rows(testTenant)); n != 4 {
		t.Errorf("el tenant tiene %d avisos, quería 4 (dos ventanas, otro motivo y otra vía)", n)
	}
}
