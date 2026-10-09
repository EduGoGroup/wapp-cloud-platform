package arranque

import (
	"context"
	"errors"
	"reflect"
	"testing"

	flowruntime "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	intakeviejo "github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
)

// Los tests de este fichero se derivan de los comentarios de bridge_captacion.go (una aserción por
// promesa). Sin BD, sin selector y sin goroutines: los tres adaptadores se prueban sobre dobles de
// sus puertos mínimos. Que en el arranque real envuelvan el pool, el compositor y el agregador del
// contenedor —y no otros— lo afirma captacion_cableado_test.go.

// captureWindowKey es una clave de ventana con los cuatro campos distintos entre sí y ninguno a
// cero: un campo cruzado o perdido en la conversión no puede pasar por bueno.
var captureWindowKey = intake.WindowKey{
	TenantID:  bridgeTenantID,
	SessionID: "session-7",
	ContactID: "contact-42",
	EventID:   "5c1d9a36-7e2b-4f0a-8b6d-2a9e4c7f1b03",
}

// fakeAheadPool es el doble de aheadRequests (en producción, el *intakeahead.Pool nuevo).
type fakeAheadPool struct {
	gotKey  intake.WindowKey
	gotText string
	calls   int
}

func (f *fakeAheadPool) Request(key intake.WindowKey, text string) {
	f.calls++
	f.gotKey, f.gotText = key, text
}

// fakeFlushComposer es el doble de legacyFlushComposer (en producción, el compositor viejo).
type fakeFlushComposer struct {
	gotCtx context.Context
	gotKey intakeviejo.WindowKey
	calls  int

	err error
}

func (f *fakeFlushComposer) ComposeAtFlush(ctx context.Context, key intakeviejo.WindowKey) error {
	f.calls++
	f.gotCtx, f.gotKey = ctx, key
	return f.err
}

// fakeClassifiedSink es el doble de legacyClassifiedSink (en producción, el agregador viejo).
type fakeClassifiedSink struct {
	gotKey        intakeviejo.WindowKey
	gotIntent     string
	gotConfidence float64
	calls         int
}

func (f *fakeClassifiedSink) OnClassified(key intakeviejo.WindowKey, intent string, confidence float64) {
	f.calls++
	f.gotKey, f.gotIntent, f.gotConfidence = key, intent, confidence
}

// Promesa (T-3): la clave de ventana viaja entre el intake viejo y el nuevo campo a campo, en los
// dos sentidos, y la ida y vuelta devuelve la misma clave.
func TestCaptureBridge_WindowKey_RoundTripsEveryField(t *testing.T) {
	legacy := toLegacyWindowKey(captureWindowKey)
	assertSameFields(t, captureWindowKey, legacy)
	// Y en el otro sentido: un campo que solo existiera en la clave vieja se quedaría a cero.
	assertSameFields(t, legacy, toNewWindowKey(legacy))

	if back := toNewWindowKey(legacy); back != captureWindowKey {
		t.Errorf("nueva → vieja → nueva = %+v; quiere la clave de partida, %+v", back, captureWindowKey)
	}
	if back := toLegacyWindowKey(toNewWindowKey(legacy)); back != legacy {
		t.Errorf("vieja → nueva → vieja = %+v; quiere la clave de partida, %+v", back, legacy)
	}
}

// Promesa: Request convierte la clave vieja en la nueva y delega en el pool con esa clave y el
// mismo texto, una sola vez.
func TestAheadBridge_Request_DelegatesWithTheConvertedKey(t *testing.T) {
	pool := &fakeAheadPool{}
	b := &aheadBridge{pool: pool}

	b.Request(toLegacyWindowKey(captureWindowKey), "quiero dos tortas para el sábado")

	if pool.calls != 1 {
		t.Fatalf("el pool recibió %d peticiones; quiere 1", pool.calls)
	}
	if pool.gotKey != captureWindowKey {
		t.Errorf("el pool recibió la clave %+v; quiere %+v", pool.gotKey, captureWindowKey)
	}
	if pool.gotText != "quiero dos tortas para el sábado" {
		t.Errorf("el pool recibió el texto %q; quiere el mismo que el adaptador", pool.gotText)
	}
}

// Promesa: Request no valida la clave: una incompleta llega al pool tal cual (es el pool quien la
// descarta), igual que cuando el agregador llamaba al pool directamente.
func TestAheadBridge_Request_DoesNotValidateTheKey(t *testing.T) {
	pool := &fakeAheadPool{}
	b := &aheadBridge{pool: pool}

	b.Request(intakeviejo.WindowKey{TenantID: bridgeTenantID}, "")

	if pool.calls != 1 || pool.gotKey != (intake.WindowKey{TenantID: bridgeTenantID}) || pool.gotText != "" {
		t.Errorf("con una clave incompleta el pool recibió %d peticiones (clave %+v, texto %q); quiere 1, tal cual",
			pool.calls, pool.gotKey, pool.gotText)
	}
}

// Promesa: ComposeAtFlush convierte la clave nueva en la vieja y delega con el mismo ctx; sin
// error del compositor, devuelve nil.
func TestComposerBridge_ComposeAtFlush_DelegatesWithTheConvertedKey(t *testing.T) {
	composer := &fakeFlushComposer{}
	b := &composerBridge{composer: composer}

	if err := b.ComposeAtFlush(markedCtx(t), captureWindowKey); err != nil {
		t.Fatalf("ComposeAtFlush: %v; quiere nil, lo que devolvió el compositor", err)
	}

	if composer.calls != 1 {
		t.Fatalf("el compositor recibió %d llamadas; quiere 1", composer.calls)
	}
	if composer.gotKey != toLegacyWindowKey(captureWindowKey) {
		t.Errorf("el compositor recibió la clave %+v; quiere %+v", composer.gotKey, captureWindowKey)
	}
	if ctxMarker(composer.gotCtx) != "marker" {
		t.Error("el compositor no recibió el mismo ctx que el adaptador")
	}
}

// Promesa: el error del compositor pasa TAL CUAL, sin envolver ni traducir.
func TestComposerBridge_ComposeAtFlush_ErrorPassesThrough(t *testing.T) {
	composerErr := errors.New("pq: connection refused")
	b := &composerBridge{composer: &fakeFlushComposer{err: composerErr}}

	err := b.ComposeAtFlush(t.Context(), captureWindowKey)

	if err != composerErr { //nolint:errorlint // se afirma identidad: pasa sin envolver
		t.Errorf("err = %v; quiere exactamente el error del compositor, sin envolver", err)
	}
}

// Promesa: la clausura del sink NO resuelve su destino al construirse, sino en cada clasificación:
// un destino que todavía no existe cuando nace el pool se resuelve cuando ya existe (el nudo de
// construcción de la fase 7).
func TestClassifiedSink_ResolvesTheTargetAtCallTime(t *testing.T) {
	var target *fakeClassifiedSink
	resolved := 0
	sink := newClassifiedSink(func() legacyClassifiedSink {
		resolved++
		return target
	})
	if resolved != 0 {
		t.Fatalf("el destino se resolvió %d veces al construir la clausura; quiere 0: en el arranque todavía es nil", resolved)
	}

	// El destino nace DESPUÉS que la clausura, como el agregador después que el pool.
	target = &fakeClassifiedSink{}
	sink.OnClassified(captureWindowKey, "pedido", 0.91)
	sink.OnClassified(captureWindowKey, "pedido", 0.91)

	if resolved != 2 {
		t.Errorf("el destino se resolvió %d veces en dos clasificaciones; quiere 2, una por cada una", resolved)
	}
	if target.calls != 2 {
		t.Errorf("el destino recibió %d clasificaciones; quiere 2", target.calls)
	}
}

// Promesa: cada clasificación llega al destino con la clave convertida y los mismos intent y
// confidence.
func TestClassifiedSink_DelegatesWithTheConvertedKey(t *testing.T) {
	target := &fakeClassifiedSink{}
	sink := newClassifiedSink(func() legacyClassifiedSink { return target })

	sink.OnClassified(captureWindowKey, "consulta_precio", 0.625)

	if target.calls != 1 {
		t.Fatalf("el destino recibió %d clasificaciones; quiere 1", target.calls)
	}
	if target.gotKey != toLegacyWindowKey(captureWindowKey) {
		t.Errorf("el destino recibió la clave %+v; quiere %+v", target.gotKey, captureWindowKey)
	}
	if target.gotIntent != "consulta_precio" || target.gotConfidence != 0.625 {
		t.Errorf("el destino recibió (%q, %v); quiere (\"consulta_precio\", 0.625), sin tocar",
			target.gotIntent, target.gotConfidence)
	}
}

// Promesa: classifiedSink lee c.intakeAggregator AL LLAMAR. Se construye con el campo a nil —como
// en la fase 7— sin tocarlo; lo que resuelve después es el campo del contenedor, no una copia.
func TestClassifiedSink_ReadsTheContainerFieldLazily(t *testing.T) {
	c := &contenedor{}
	sink := classifiedSink(c)
	if sink == nil {
		t.Fatal("classifiedSink devolvió nil con el agregador todavía sin construir; quiere la clausura")
	}
	// No se llama: con el agregador a nil la clausura no lleva guarda (ver su comentario), y con
	// uno real haría falta la cola. Que el destino sea el agregador del contenedor lo afirma, sobre
	// el arranque real, TestCableado_TheCaptureBridgesWrapTheBootObjects.
}

// Promesa: el límite del hilo que recibe el re-análisis es el del compositor viejo, derivado, y
// es positivo (con ≤ 0 reanalisis.NewService no construye).
func TestCaptureBridge_ThreadLimitIsTheLegacyComposerLimit(t *testing.T) {
	if legacyThreadLimit != flowruntime.DefaultThreadLimit {
		t.Errorf("legacyThreadLimit = %d; quiere flowruntime.DefaultThreadLimit, %d", legacyThreadLimit, flowruntime.DefaultThreadLimit)
	}
	if legacyThreadLimit <= 0 {
		t.Errorf("legacyThreadLimit = %d; quiere un límite positivo: el re-análisis no arrancaría", legacyThreadLimit)
	}
}

// Promesa: newLegacyIntakeJobs devuelve la cola VIEJA (el alias es el tipo del intake viejo), y
// satisface los dos puertos de sus únicos consumidores: el agregador y el compositor.
func TestNewLegacyIntakeJobs_IsTheOldQueueForItsTwoConsumers(t *testing.T) {
	jobs := newLegacyIntakeJobs(nil)
	if jobs == nil {
		t.Fatal("newLegacyIntakeJobs devolvió nil")
	}
	if got, want := reflect.TypeOf(jobs), reflect.TypeFor[*intakeviejo.Postgres](); got != want {
		t.Errorf("newLegacyIntakeJobs devuelve un %s; quiere %s, la cola del intake viejo", got, want)
	}
	var (
		_ intakeviejo.JobStore         = jobs
		_ flowruntime.SourceTextWriter = jobs
	)
}
