package runtime

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// ComposeAtFlush: el compositor cableado (leer el hilo, componer, cifrar y guardar el sobre). La
// función pura que reparte el hilo se prueba en source_composer_test.go.

// composerRead es una lectura del hilo tal como llegó al doble.
type composerRead struct {
	marked  bool
	eventID string
	limit   int
}

// composerThread es el ThreadReader: devuelve siempre las mismas entradas (o falla).
type composerThread struct {
	entries []events.ThreadEntry
	err     error
	reads   []composerRead
}

type composerCtxKey struct{}

func composerCtx() context.Context {
	return context.WithValue(context.Background(), composerCtxKey{}, "marked")
}

func (th *composerThread) ListThread(ctx context.Context, eventID string, limit int) ([]events.ThreadEntry, error) {
	th.reads = append(th.reads, composerRead{marked: ctx.Value(composerCtxKey{}) == "marked", eventID: eventID, limit: limit})
	if th.err != nil {
		return nil, th.err
	}
	return th.entries, nil
}

// composerBrokenKeys es un KeyProvider que no sabe envolver: hace fallar el cifrado.
type composerBrokenKeys struct{ err error }

func (k composerBrokenKeys) WrapDEK([]byte) ([]byte, string, error)   { return nil, "", k.err }
func (k composerBrokenKeys) UnwrapDEK([]byte, string) ([]byte, error) { return nil, k.err }
func (composerBrokenKeys) BlindIndex(string, string) string           { return "" }
func (composerBrokenKeys) CurrentKeyID() string                       { return "broken" }

// Aserciones de compilación: el compositor llena el hueco del agregador, y sus dos puertos los
// cumplen el almacén de jobs y el doble del hilo.
var (
	_ SourceComposer     = (*SourceTextComposer)(nil)
	_ ThreadReader       = (*composerThread)(nil)
	_ SourceTextWriter   = (*intake.MemoryStore)(nil)
	_ SourceTextWriter   = intake.JobStore(nil)
	_ crypto.KeyProvider = composerBrokenKeys{}
)

// composerClientText es el literal del cliente: no puede aparecer en ningún log ni en ningún error.
const composerClientText = "dos tortas de chocolate zzq"

// composerRig es un compositor con sus cuatro dependencias y una ventana recién cerrada.
type composerRig struct {
	log    *sinkLogRecorder
	thread *composerThread
	jobs   *intake.MemoryStore
	cipher *crypto.FieldCipher
	key    intake.WindowKey
}

func newComposerRig(t *testing.T, entries ...events.ThreadEntry) *composerRig {
	t.Helper()
	kek := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x33}, 32))
	index := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x44}, 32))
	keys, err := crypto.NewEnvKeyProvider(crypto.KeyringConfig{KeyringB64: "K1:" + kek, CurrentID: "K1", IndexB64: index})
	if err != nil {
		t.Fatalf("no se pudo montar el keyring del test: %v", err)
	}
	at := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)
	rig := &composerRig{
		log:    newSinkLogRecorder(),
		thread: &composerThread{entries: entries},
		jobs:   intake.NewMemoryStore(func() time.Time { return at }),
		cipher: crypto.NewFieldCipher(keys),
		key:    intake.WindowKey{TenantID: "tenant-1", SessionID: "session-9", ContactID: "contact-opaque", EventID: "event-7"},
	}
	rig.jobs.Seed(intake.Job{Key: rig.key, Status: intake.StatusPending})
	rig.jobs.ResetCounters()
	return rig
}

func (r *composerRig) composer(opts ...SourceTextComposerOption) *SourceTextComposer {
	return NewSourceTextComposer(r.log, r.thread, r.jobs, r.cipher, opts...)
}

// envelope devuelve el sobre de la única fila del almacén.
func (r *composerRig) envelope(t *testing.T) intake.SourceText {
	t.Helper()
	jobs := r.jobs.Jobs()
	if len(jobs) != 1 {
		t.Fatalf("el almacén tiene %d filas, quería 1", len(jobs))
	}
	return jobs[0].SourceText
}

// requireNoContent falla si el literal del cliente aparece en el log o en el error.
func (r *composerRig) requireNoContent(t *testing.T, err error) {
	t.Helper()
	if dump := r.log.dump(); strings.Contains(dump, "zzq") {
		t.Errorf("el log lleva contenido del hilo (REQ-10c):\n%s", dump)
	}
	if err != nil && strings.Contains(err.Error(), "zzq") {
		t.Errorf("el error cita contenido del hilo (REQ-10c): %v", err)
	}
}

// composerThreadFixture es un hilo con las dos clases de entrada que cuentan.
func composerThreadFixture() []events.ThreadEntry {
	return []events.ThreadEntry{
		composerEntry(events.KindSummary, events.RoleSystem, "tenías un pan zzq"),
		composerClient(composerClientText), composerBusiness("anotado zzq"),
	}
}

// TestSourceTextComposer_ComposeAtFlush_WritesTheSealedEnvelope: lee el hilo una vez con el evento
// de la clave y el límite por defecto, y guarda UN sobre de tres piezas que se abre con el mismo
// keyring y da exactamente el Text compuesto (contexto y mensajes).
func TestSourceTextComposer_ComposeAtFlush_WritesTheSealedEnvelope(t *testing.T) {
	rig := newComposerRig(t, composerThreadFixture()...)

	if err := rig.composer().ComposeAtFlush(composerCtx(), rig.key); err != nil {
		t.Fatalf("ComposeAtFlush devolvió %v, quería nil", err)
	}

	wantRead := composerRead{marked: true, eventID: "event-7", limit: 200}
	if len(rig.thread.reads) != 1 || rig.thread.reads[0] != wantRead {
		t.Errorf("lecturas del hilo = %+v, quería una: %+v", rig.thread.reads, wantRead)
	}
	if DefaultThreadLimit != 200 {
		t.Errorf("DefaultThreadLimit = %d, quería 200", DefaultThreadLimit)
	}
	if got, want := rig.jobs.Counters(), (intake.Counters{PutSourceText: 1}); got != want {
		t.Errorf("presupuesto = %+v, quería %+v: una sola escritura del sobre y nada más", got, want)
	}
	env := rig.envelope(t)
	if !env.Complete() || env.KEKID != "K1" {
		t.Fatalf("sobre = (enc %d bytes, dek %d bytes, kek %q), quería las tres piezas con la KEK current", len(env.Enc), len(env.DEK), env.KEKID)
	}
	if bytes.Contains(env.Enc, []byte("zzq")) {
		t.Error("el sobre guarda el literal en claro")
	}
	plain, err := rig.cipher.Decrypt(env.Enc, env.DEK, env.KEKID)
	if err != nil {
		t.Fatalf("el sobre no se abre con el keyring: %v", err)
	}
	if want := ComposeSourceText(composerThreadFixture()).Text; plain != want {
		t.Errorf("el sobre abre a %q, quería %q", plain, want)
	}
}

// TestSourceTextComposer_ComposeAtFlush_LogsNumbersNeverContent: la composición se cuenta en UNA
// línea Debug con identificadores y números (REQ-10c); el contenido del hilo no aparece.
func TestSourceTextComposer_ComposeAtFlush_LogsNumbersNeverContent(t *testing.T) {
	rig := newComposerRig(t, composerThreadFixture()...)

	if err := rig.composer().ComposeAtFlush(composerCtx(), rig.key); err != nil {
		t.Fatalf("ComposeAtFlush devolvió %v, quería nil", err)
	}

	lines := rig.log.all()
	if len(lines) != 1 || lines[0].level != "debug" || lines[0].msg != "compositor: literal compuesto y cifrado" {
		t.Fatalf("log = %+v, quería una única línea Debug del literal compuesto", lines)
	}
	want := map[string]any{
		"tenant_id": "tenant-1", "session_id": "session-9", "event_id": "event-7",
		"mensajes": 2, "contexto": 1, "bytes": len(ComposeSourceText(composerThreadFixture()).Text),
	}
	if !reflect.DeepEqual(lines[0].fields, want) {
		t.Errorf("claves del log = %v, quería exactamente %v", lines[0].fields, want)
	}
	rig.requireNoContent(t, nil)
}

// TestWithThreadLimit: el límite inyectado es el que se pide al lector; <= 0 se ignora.
func TestWithThreadLimit(t *testing.T) {
	cases := []struct {
		name string
		opts []SourceTextComposerOption
		want int
	}{
		{"positive", []SourceTextComposerOption{WithThreadLimit(3)}, 3},
		{"zero is ignored", []SourceTextComposerOption{WithThreadLimit(0)}, DefaultThreadLimit},
		{"negative is ignored", []SourceTextComposerOption{WithThreadLimit(-5)}, DefaultThreadLimit},
		{"ignored value keeps the previous one", []SourceTextComposerOption{WithThreadLimit(7), WithThreadLimit(0)}, 7},
		{"last positive wins", []SourceTextComposerOption{WithThreadLimit(7), WithThreadLimit(9)}, 9},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rig := newComposerRig(t, composerClient("hola"))
			if err := rig.composer(c.opts...).ComposeAtFlush(composerCtx(), rig.key); err != nil {
				t.Fatalf("ComposeAtFlush devolvió %v, quería nil", err)
			}
			if len(rig.thread.reads) != 1 || rig.thread.reads[0].limit != c.want {
				t.Errorf("lecturas = %+v, quería una con límite %d", rig.thread.reads, c.want)
			}
		})
	}
}

// TestSourceTextComposer_ComposeAtFlush_NoOpWithoutADependency: con cualquier dependencia a nil, o
// sobre un receptor nil, devuelve nil sin leer, escribir ni loguear.
func TestSourceTextComposer_ComposeAtFlush_NoOpWithoutADependency(t *testing.T) {
	rig := newComposerRig(t, composerClient(composerClientText))
	var nilComposer *SourceTextComposer
	cases := map[string]*SourceTextComposer{
		"nil receiver": nilComposer,
		"nil logger":   NewSourceTextComposer(nil, rig.thread, rig.jobs, rig.cipher),
		"nil thread":   NewSourceTextComposer(rig.log, nil, rig.jobs, rig.cipher),
		"nil jobs":     NewSourceTextComposer(rig.log, rig.thread, nil, rig.cipher),
		"nil cipher":   NewSourceTextComposer(rig.log, rig.thread, rig.jobs, nil),
	}
	for name, composer := range cases {
		if name != "nil receiver" && composer == nil {
			t.Errorf("%s: NewSourceTextComposer devolvió nil", name)
		}
		if err := composer.ComposeAtFlush(composerCtx(), rig.key); err != nil {
			t.Errorf("%s: ComposeAtFlush devolvió %v, quería nil", name, err)
		}
	}
	if len(rig.thread.reads) != 0 || rig.jobs.Counters().PutSourceText != 0 || len(rig.log.all()) != 0 {
		t.Errorf("un compositor sin dependencias tocó algo: lecturas=%d, escrituras=%d, log=%d",
			len(rig.thread.reads), rig.jobs.Counters().PutSourceText, len(rig.log.all()))
	}
}

// TestSourceTextComposer_ComposeAtFlush_IncompleteKey: una clave incompleta es un error, y se
// detecta antes de leer el hilo.
func TestSourceTextComposer_ComposeAtFlush_IncompleteKey(t *testing.T) {
	rig := newComposerRig(t, composerClient(composerClientText))
	key := rig.key
	key.EventID = ""

	err := rig.composer().ComposeAtFlush(composerCtx(), key)

	if err == nil || err.Error() != "compositor: clave de ventana incompleta" {
		t.Fatalf("error = %v, quería «compositor: clave de ventana incompleta»", err)
	}
	if len(rig.thread.reads) != 0 || rig.jobs.Counters().PutSourceText != 0 {
		t.Error("con la clave incompleta se leyó el hilo o se escribió el sobre")
	}
}

// TestSourceTextComposer_ComposeAtFlush_NoMessagesWritesNothing: con cero mensajes —el hilo vacío,
// o un hilo SOLO de contexto— no se escribe el sobre y queda un aviso con el número de entradas de
// contexto. No es un error.
func TestSourceTextComposer_ComposeAtFlush_NoMessagesWritesNothing(t *testing.T) {
	cases := []struct {
		name    string
		entries []events.ThreadEntry
		wantCtx int
	}{
		{"empty thread", nil, 0},
		{"context only", []events.ThreadEntry{
			composerEntry(events.KindSummary, events.RoleSystem, "tenías dos tortas zzq"),
			composerEntry(events.KindMessageOutOfTurn, events.RoleBusiness, "¿sigues ahí? zzq"),
		}, 2},
		{"only decisions and empty messages", []events.ThreadEntry{
			composerEntry(events.KindDecision, events.RoleClient, "zzq"), composerClient(""),
		}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rig := newComposerRig(t, c.entries...)

			if err := rig.composer().ComposeAtFlush(composerCtx(), rig.key); err != nil {
				t.Fatalf("ComposeAtFlush devolvió %v, quería nil", err)
			}
			if rig.jobs.Counters().PutSourceText != 0 || rig.envelope(t).Complete() {
				t.Error("se escribió un sobre sin una sola línea del cliente")
			}
			lines := rig.log.all()
			if len(lines) != 1 || lines[0].level != "warn" ||
				lines[0].msg != "compositor: la ventana cerró sin una sola línea del hilo; el sobre se queda vacío" {
				t.Fatalf("log = %+v, quería un único aviso en Warn", lines)
			}
			want := map[string]any{"tenant_id": "tenant-1", "session_id": "session-9", "event_id": "event-7", "entradas_de_contexto": c.wantCtx}
			if !reflect.DeepEqual(lines[0].fields, want) {
				t.Errorf("claves del aviso = %v, quería exactamente %v", lines[0].fields, want)
			}
			rig.requireNoContent(t, nil)
		})
	}
}

// TestSourceTextComposer_ComposeAtFlush_Failures: la composición fallida en sus tres sitios —leer
// el hilo, cifrar, guardar—. El error envuelve el de origen con su texto literal, no se escribe
// ningún sobre y ni el error ni el log citan el contenido.
func TestSourceTextComposer_ComposeAtFlush_Failures(t *testing.T) {
	boom := errors.New("fallo de infraestructura")
	cases := []struct {
		name       string
		build      func(rig *composerRig) *SourceTextComposer
		wantPrefix string
		wantPuts   int
	}{
		{
			name: "reading the thread",
			build: func(rig *composerRig) *SourceTextComposer {
				rig.thread.err = boom
				return rig.composer()
			},
			wantPrefix: "compositor: leer el hilo del evento event-7: ",
		},
		{
			name: "encrypting",
			build: func(rig *composerRig) *SourceTextComposer {
				broken := crypto.NewFieldCipher(composerBrokenKeys{err: boom})
				return NewSourceTextComposer(rig.log, rig.thread, rig.jobs, broken)
			},
			wantPrefix: "compositor: cifrar el literal de la ventana del evento event-7: ",
		},
		{
			name: "storing the envelope",
			build: func(rig *composerRig) *SourceTextComposer {
				rig.jobs.FailPutWith(boom)
				return rig.composer()
			},
			wantPrefix: "compositor: guardar el literal de la ventana del evento event-7: ",
			wantPuts:   1,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rig := newComposerRig(t, composerClient(composerClientText))
			composer := c.build(rig)

			err := composer.ComposeAtFlush(composerCtx(), rig.key)

			if !errors.Is(err, boom) {
				t.Fatalf("error = %v, quería uno que envuelva el de origen", err)
			}
			if !strings.HasPrefix(err.Error(), c.wantPrefix) {
				t.Errorf("texto = %q, quería que empezara por %q", err.Error(), c.wantPrefix)
			}
			if got := rig.jobs.Counters().PutSourceText; got != c.wantPuts {
				t.Errorf("escrituras del sobre intentadas = %d, quería %d", got, c.wantPuts)
			}
			if rig.envelope(t).Complete() {
				t.Error("quedó un sobre escrito tras una composición fallida")
			}
			rig.requireNoContent(t, err)
		})
	}
}

// TestSourceTextComposer_ComposeAtFlush_DoesNotOverwrite: si la fila ya tenía sobre, o la ventana
// no está en `pending`, no es un error: devuelve nil, no toca la fila y lo dice en Debug.
func TestSourceTextComposer_ComposeAtFlush_DoesNotOverwrite(t *testing.T) {
	rig := newComposerRig(t, composerClient(composerClientText))
	composer := rig.composer()
	if err := composer.ComposeAtFlush(composerCtx(), rig.key); err != nil {
		t.Fatalf("la primera composición devolvió %v", err)
	}
	first := rig.envelope(t)

	if err := composer.ComposeAtFlush(composerCtx(), rig.key); err != nil {
		t.Fatalf("la segunda composición devolvió %v, quería nil (idempotencia)", err)
	}

	second := rig.envelope(t)
	if !bytes.Equal(first.Enc, second.Enc) || !bytes.Equal(first.DEK, second.DEK) || first.KEKID != second.KEKID {
		t.Error("la segunda composición sobrescribió el sobre")
	}
	debug := rig.log.at("debug")
	if len(debug) != 2 || debug[1].msg != "compositor: la ventana ya tenía literal; no se sobrescribe" {
		t.Fatalf("debug = %+v, quería la línea de «ya tenía literal» en segundo lugar", debug)
	}
	if got := debug[1].fields; len(got) != 2 || got["tenant_id"] != "tenant-1" || got["event_id"] != "event-7" {
		t.Errorf("claves = %v, quería tenant_id y event_id", got)
	}

	t.Run("window not pending", func(t *testing.T) {
		other := newComposerRig(t, composerClient(composerClientText))
		key := other.key
		key.EventID = "event-without-pending-row"
		if err := other.composer().ComposeAtFlush(composerCtx(), key); err != nil {
			t.Fatalf("ComposeAtFlush devolvió %v, quería nil", err)
		}
		if other.envelope(t).Complete() {
			t.Error("se escribió el sobre de otra tupla")
		}
		if len(other.log.at("error"))+len(other.log.at("warn")) != 0 {
			t.Errorf("«no había dónde escribir» no es una avería: %s", other.log.dump())
		}
	})
}
