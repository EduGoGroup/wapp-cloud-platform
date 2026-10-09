package arranque

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	legacyllm "github.com/EduGoGroup/wapp-cloud-platform/internal/llmvia"
	edgegrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/grpc"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/llmvia"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/llmvia/local"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm"
)

// Los tests de este fichero se derivan del comentario de turneroBridge (una aserción por
// promesa). Sin BD y sin Edge: turneroBridge se prueba sobre un *llmvia.Selector NUEVO de verdad,
// armado con dobles de su almacén y de su frame. Los cuatro de llmConfigBridge murieron con él en
// F7 (conmutar(captacion)).

// fakeLLMStore es el doble del almacén nuevo de tenant_llm: sirve de llmvia.Store al selector.
// Captura lo que recibe Get y devuelve lo que se le dice.
type fakeLLMStore struct {
	gotCtx      context.Context
	gotTenantID string
	calls       int

	cfg   tenantllm.Config
	found bool
	err   error
}

var _ llmvia.Store = (*fakeLLMStore)(nil)

func (f *fakeLLMStore) Get(ctx context.Context, tenantID string) (tenantllm.Config, bool, error) {
	f.calls++
	f.gotCtx, f.gotTenantID = ctx, tenantID
	if f.err != nil {
		return tenantllm.Config{}, false, f.err
	}
	return f.cfg, f.found, nil
}

// APIKey no se usa en estos tests: ni el turno acotado ni el re-análisis piden la credencial.
func (f *fakeLLMStore) APIKey(context.Context, string) (string, error) {
	return "", tenantllm.ErrNotConfigured
}

// fakeFrame es el doble del local.Frame nuevo (en producción, el *edgegrpc.Server).
type fakeFrame struct {
	gotCtx      context.Context
	gotTenantID string
	gotReq      edgegrpc.InferRequest
	calls       int

	out string
	err error
}

var _ local.Frame = (*fakeFrame)(nil)

func (f *fakeFrame) Infer(ctx context.Context, tenantID string, req edgegrpc.InferRequest) (string, error) {
	f.calls++
	f.gotCtx, f.gotTenantID, f.gotReq = ctx, tenantID, req
	if f.err != nil {
		return "", f.err
	}
	return f.out, nil
}

const (
	bridgeOriginSessionID = "origin-session"
	bridgeTurnPrompt      = "prompt ya compuesto"
	bridgeTurnSchema      = `{"type":"object"}`
)

// newTurneroBridge monta el adaptador sobre un selector nuevo real. frame nil ⇒ selector sin
// transporte.
func newTurneroBridge(t *testing.T, store *fakeLLMStore, frame *fakeFrame) *turneroBridge {
	t.Helper()
	opts := []llmvia.SelectorOption{}
	if frame != nil {
		opts = append(opts, llmvia.WithFrame(frame))
	}
	sel, err := llmvia.NewSelector(store, quietLogger(), opts...)
	if err != nil {
		t.Fatalf("llmvia.NewSelector: %v", err)
	}
	return &turneroBridge{sel: sel}
}

func legacyTurn() legacyllm.TurnoRequest {
	return legacyllm.TurnoRequest{Prompt: bridgeTurnPrompt, Formato: bridgeTurnSchema}
}

// assertSameFields afirma por reflexión que from y to (dos structs de tipos distintos) tienen los
// mismos campos y que cada uno de from, que no puede estar a cero, llegó igual a to. El día que
// uno de los dos tipos gane un campo, falla en vez de dejarlo viajar a cero.
func assertSameFields(t *testing.T, from, to any) {
	t.Helper()
	fromV, toV := reflect.ValueOf(from), reflect.ValueOf(to)
	if fromV.NumField() != toV.NumField() {
		t.Fatalf("%s tiene %d campos y %s tiene %d: el adaptador dejaría alguno sin copiar",
			fromV.Type(), fromV.NumField(), toV.Type(), toV.NumField())
	}
	for i := range fromV.NumField() {
		name := fromV.Type().Field(i).Name
		if fromV.Field(i).IsZero() {
			t.Fatalf("el campo %s del caso de prueba está a cero: no probaría que se copia", name)
		}
		got := toV.FieldByName(name)
		if !got.IsValid() {
			t.Errorf("%s no tiene el campo %s", toV.Type(), name)
			continue
		}
		if !reflect.DeepEqual(got.Interface(), fromV.Field(i).Interface()) {
			t.Errorf("campo %s: se copió %v; quiere %v", name, got.Interface(), fromV.Field(i).Interface())
		}
	}
}

// Promesa: Turno copia el TurnoRequest viejo en uno nuevo campo a campo.
func TestTurneroBridge_TurnoRequest_CopiesEveryField(t *testing.T) {
	old := legacyTurn()
	assertSameFields(t, old, toNewTurnoRequest(old))
}

// Promesa: delega en el selector con el mismo ctx, tenantID y originSessionID, y devuelve el
// texto crudo tal cual. Se ve en el frame, que es donde el selector nuevo deja la petición.
func TestTurneroBridge_Turno_DelegatesToTheNewSelector(t *testing.T) {
	cases := []struct {
		name  string
		store *fakeLLMStore
	}{
		{"tenant without row is served", &fakeLLMStore{}},
		{"tenant with local row is served", &fakeLLMStore{found: true, cfg: tenantllm.Config{Via: tenantllm.ViaLocal}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			frame := &fakeFrame{out: `{"elige":2}`}
			b := newTurneroBridge(t, c.store, frame)

			raw, err := b.Turno(markedCtx(t), bridgeTenantID, bridgeOriginSessionID, legacyTurn())
			if err != nil {
				t.Fatalf("Turno = %q, %v; quiere el texto del selector sin error", raw, err)
			}
			if raw != frame.out {
				t.Errorf("Turno = %q; quiere el texto crudo tal cual, %q", raw, frame.out)
			}
			if frame.calls != 1 {
				t.Fatalf("el frame recibió %d peticiones; quiere 1", frame.calls)
			}
			if ctxMarker(frame.gotCtx) != "marker" || ctxMarker(c.store.gotCtx) != "marker" {
				t.Errorf("el selector no recibió el ctx que se pasó al adaptador")
			}
			if frame.gotTenantID != bridgeTenantID || c.store.gotTenantID != bridgeTenantID {
				t.Errorf("tenant en el frame %q y en el almacén %q; quiere %q en los dos",
					frame.gotTenantID, c.store.gotTenantID, bridgeTenantID)
			}
			if frame.gotReq.Prompt != bridgeTurnPrompt || frame.gotReq.Format != bridgeTurnSchema {
				t.Errorf("el frame recibió prompt %q y formato %q; quiere los del TurnoRequest viejo",
					frame.gotReq.Prompt, frame.gotReq.Format)
			}
			if frame.gotReq.OriginSessionID != bridgeOriginSessionID {
				t.Errorf("sesión de origen %q; quiere %q", frame.gotReq.OriginSessionID, bridgeOriginSessionID)
			}
		})
	}
}

// Promesa: el ErrViaSinTurnoAcotado nuevo sale con el mismo texto y casa con el centinela VIEJO,
// que es el que compara turnoacotado para degradar a «sin resolutor» en vez de fallar (R4.7.c).
func TestTurneroBridge_UnservedRoute_KeepsTextAndMatchesBothSentinels(t *testing.T) {
	store := &fakeLLMStore{found: true, cfg: tenantllm.Config{Via: tenantllm.ViaAPI}}
	frame := &fakeFrame{out: "no debería llegar"}
	b := newTurneroBridge(t, store, frame)

	raw, err := b.Turno(t.Context(), bridgeTenantID, bridgeOriginSessionID, legacyTurn())

	if err == nil {
		t.Fatalf("Turno = %q sin error; quiere el centinela de turno no servido", raw)
	}
	if raw != "" {
		t.Errorf("junto al error devolvió %q; quiere la cadena vacía", raw)
	}
	if frame.calls != 0 {
		t.Errorf("el frame recibió %d peticiones; quiere 0", frame.calls)
	}
	if !errors.Is(err, legacyllm.ErrViaSinTurnoAcotado) {
		t.Errorf("errors.Is(err, centinela viejo) = false: turnoacotado devolvería un error en vez de degradar")
	}
	if !errors.Is(err, llmvia.ErrViaSinTurnoAcotado) {
		t.Errorf("errors.Is(err, centinela nuevo) = false; quiere que conserve el original")
	}
	if err.Error() != legacyllm.ErrViaSinTurnoAcotado.Error() {
		t.Errorf("Error() = %q; quiere el texto exacto del centinela viejo, %q", err.Error(), legacyllm.ErrViaSinTurnoAcotado.Error())
	}
}

// Promesa (translateTurnErr): también traduce el centinela envuelto, sin tocar su texto; y lo que
// no es el centinela sale siendo EL MISMO error.
func TestTranslateTurnErr_SentinelAndForeignErrors(t *testing.T) {
	wrapped := fmt.Errorf("selector: %w", llmvia.ErrViaSinTurnoAcotado)
	got := translateTurnErr(wrapped)
	if !errors.Is(got, legacyllm.ErrViaSinTurnoAcotado) || !errors.Is(got, wrapped) {
		t.Errorf("el centinela envuelto no casa con el viejo y con el original: %v", got)
	}
	if got.Error() != wrapped.Error() {
		t.Errorf("Error() = %q; quiere el mismo texto que el original, %q", got.Error(), wrapped.Error())
	}

	foreign := []error{
		errors.New("pq: connection refused"),
		context.DeadlineExceeded,
		local.ErrSinTransporte,
		llmvia.ErrViaDesconocida,
		llmvia.ErrViaSinCalentamiento,
		fmt.Errorf("envuelto: %w", llmvia.ErrViaDesconocida),
	}
	for _, in := range foreign {
		out := translateTurnErr(in)
		if out != in { //nolint:errorlint // se afirma identidad: el error ajeno no se envuelve
			t.Errorf("translateTurnErr(%v) = %v; quiere el mismo error, sin envolver", in, out)
		}
		if errors.Is(out, legacyllm.ErrViaSinTurnoAcotado) {
			t.Errorf("translateTurnErr(%v) casa con el centinela viejo; no debe", in)
		}
	}
}

// Promesa: cualquier otro error del selector pasa INTACTO y no casa con el centinela viejo.
func TestTurneroBridge_ForeignErrors_PassThrough(t *testing.T) {
	frameErr := errors.New("edge: sesión offline")
	storeErr := errors.New("pq: connection refused")
	cases := []struct {
		name  string
		store *fakeLLMStore
		frame *fakeFrame
		want  error // el error que tiene que salir (o estar dentro del que sale)
		same  bool  // true ⇒ tiene que ser exactamente want, sin envolver
	}{
		{"frame error", &fakeLLMStore{}, &fakeFrame{err: frameErr}, frameErr, true},
		{"selector without transport", &fakeLLMStore{}, nil, local.ErrSinTransporte, true},
		{"store read error", &fakeLLMStore{err: storeErr}, &fakeFrame{}, storeErr, false},
		{"route out of vocabulary", &fakeLLMStore{found: true, cfg: tenantllm.Config{Via: "nube"}},
			&fakeFrame{}, llmvia.ErrViaDesconocida, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := newTurneroBridge(t, c.store, c.frame)

			raw, err := b.Turno(t.Context(), bridgeTenantID, bridgeOriginSessionID, legacyTurn())

			if err == nil {
				t.Fatalf("Turno = %q sin error; quiere %v", raw, c.want)
			}
			if raw != "" {
				t.Errorf("junto al error devolvió %q; quiere la cadena vacía", raw)
			}
			if !errors.Is(err, c.want) {
				t.Errorf("errors.Is(err, %v) = false: %v", c.want, err)
			}
			if c.same && err != c.want { //nolint:errorlint // se afirma identidad: pasa sin envolver
				t.Errorf("err = %#v; quiere exactamente %#v, sin envolver", err, c.want)
			}
			var wrapped *bridgeError
			if errors.As(err, &wrapped) {
				t.Errorf("el error ajeno salió envuelto en un bridgeError: %v", err)
			}
			if errors.Is(err, legacyllm.ErrViaSinTurnoAcotado) {
				t.Errorf("el error ajeno casa con el centinela viejo: turnoacotado lo tomaría por «sin resolutor»")
			}
		})
	}
}
