package runtime

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// Compose: leer el hilo, componer y cifrar, SIN escribir (D-F7-9, D-F8-13). Es lo que el agregador
// llama antes de cerrar la ventana. El montaje (composerRig) es el de source_composer_flush_test.go:
// su almacén tiene UNA fila `pending` sin sobre, que aquí tiene que quedar intacta.

// requireUntouchedStore falla si Compose escribió algo en intake_jobs.
func (r *composerRig) requireUntouchedStore(t *testing.T) {
	t.Helper()
	if got := r.jobs.Counters(); got != (intake.Counters{}) {
		t.Errorf("presupuesto de intake_jobs = %+v, quería cero: Compose no escribe", got)
	}
	if env := r.envelope(t); !env.Empty() {
		t.Errorf("la fila del almacén ganó un sobre (%d bytes): Compose no escribe", len(env.Enc))
	}
}

// TestSourceTextComposer_Compose_ReturnsTheSealedEnvelopeWithoutWriting: lee el hilo una vez con el
// evento de la clave y el límite por defecto, y DEVUELVE un sobre de tres piezas que se abre con el
// mismo keyring y da exactamente el Text compuesto. En intake_jobs no toca nada.
//
// Mata: «Compose escribe con PutSourceText».
func TestSourceTextComposer_Compose_ReturnsTheSealedEnvelopeWithoutWriting(t *testing.T) {
	rig := newComposerRig(t, composerThreadFixture()...)

	env, err := rig.composer().Compose(composerCtx(), rig.key)
	if err != nil {
		t.Fatalf("Compose devolvió %v, quería nil", err)
	}

	wantRead := composerRead{marked: true, eventID: "event-7", limit: 200}
	if len(rig.thread.reads) != 1 || rig.thread.reads[0] != wantRead {
		t.Errorf("lecturas del hilo = %+v, quería una: %+v", rig.thread.reads, wantRead)
	}
	if !env.Complete() || env.KEKID != "K1" {
		t.Fatalf("sobre = (enc %d bytes, dek %d bytes, kek %q), quería las tres piezas con la KEK current", len(env.Enc), len(env.DEK), env.KEKID)
	}
	if bytes.Contains(env.Enc, []byte("zzq")) {
		t.Error("el sobre lleva el literal en claro")
	}
	plain, err := rig.cipher.Decrypt(env.Enc, env.DEK, env.KEKID)
	if err != nil {
		t.Fatalf("el sobre no se abre con el keyring: %v", err)
	}
	if want := ComposeSourceText(composerThreadFixture()).Text; plain != want {
		t.Errorf("el sobre abre a %q, quería %q", plain, want)
	}
	rig.requireUntouchedStore(t)
}

// TestSourceTextComposer_Compose_LogsNumbersNeverContent: la composición se cuenta en UNA línea
// Debug con identificadores y números (REQ-10c); el contenido del hilo no aparece.
func TestSourceTextComposer_Compose_LogsNumbersNeverContent(t *testing.T) {
	rig := newComposerRig(t, composerThreadFixture()...)

	if _, err := rig.composer().Compose(composerCtx(), rig.key); err != nil {
		t.Fatalf("Compose devolvió %v, quería nil", err)
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

// TestSourceTextComposer_Compose_WorksWithoutAJobStore: Compose no escribe y por tanto no necesita
// `jobs`. Con jobs a nil sigue devolviendo el sobre (ComposeAtFlush, en cambio, es un no-op).
//
// Mata: «la guarda de nil de Compose exige jobs» (el agregador cerraría toda ventana sin sobre).
func TestSourceTextComposer_Compose_WorksWithoutAJobStore(t *testing.T) {
	rig := newComposerRig(t, composerClient(composerClientText))
	composer := NewSourceTextComposer(rig.log, rig.thread, nil, rig.cipher)

	env, err := composer.Compose(composerCtx(), rig.key)

	if err != nil || !env.Complete() {
		t.Fatalf("Compose sin jobs = (sobre completo %v, %v), quería un sobre completo y nil", env.Complete(), err)
	}
	if plain, derr := rig.cipher.Decrypt(env.Enc, env.DEK, env.KEKID); derr != nil || !strings.Contains(plain, composerClientText) {
		t.Errorf("el sobre no abre al hilo (error %v)", derr)
	}
}

// TestSourceTextComposer_Compose_NoOpWithoutADependency: sobre un receptor nil, o sin log, sin
// lector del hilo o sin cipher, devuelve el sobre vacío y nil sin leer, escribir ni loguear.
func TestSourceTextComposer_Compose_NoOpWithoutADependency(t *testing.T) {
	rig := newComposerRig(t, composerClient(composerClientText))
	var nilComposer *SourceTextComposer
	cases := map[string]*SourceTextComposer{
		"nil receiver": nilComposer,
		"nil logger":   NewSourceTextComposer(nil, rig.thread, rig.jobs, rig.cipher),
		"nil thread":   NewSourceTextComposer(rig.log, nil, rig.jobs, rig.cipher),
		"nil cipher":   NewSourceTextComposer(rig.log, rig.thread, rig.jobs, nil),
	}
	for name, composer := range cases {
		env, err := composer.Compose(composerCtx(), rig.key)
		if err != nil || !env.Empty() {
			t.Errorf("%s: Compose = (sobre vacío %v, %v), quería el sobre vacío y nil", name, env.Empty(), err)
		}
	}
	if len(rig.thread.reads) != 0 || len(rig.log.all()) != 0 {
		t.Errorf("un compositor sin dependencias tocó algo: lecturas=%d, log=%d", len(rig.thread.reads), len(rig.log.all()))
	}
	rig.requireUntouchedStore(t)
}

// TestSourceTextComposer_Compose_IncompleteKey: una clave incompleta es un error, y se detecta antes
// de leer el hilo.
func TestSourceTextComposer_Compose_IncompleteKey(t *testing.T) {
	rig := newComposerRig(t, composerClient(composerClientText))
	key := rig.key
	key.EventID = ""

	env, err := rig.composer().Compose(composerCtx(), key)

	if err == nil || err.Error() != "compositor: clave de ventana incompleta" {
		t.Fatalf("error = %v, quería «compositor: clave de ventana incompleta»", err)
	}
	if !env.Empty() || len(rig.thread.reads) != 0 {
		t.Error("con la clave incompleta se leyó el hilo o se devolvió un sobre")
	}
	rig.requireUntouchedStore(t)
}

// TestSourceTextComposer_Compose_NoMessagesIsAnEmptyEnvelope: con cero mensajes —el hilo vacío, o un
// hilo SOLO de contexto— devuelve el sobre VACÍO y nil, y deja el aviso con el número de entradas
// de contexto. No es un error: el agregador cierra la ventana con el sobre a NULL.
//
// Mata: «Compose con el hilo vacío devuelve error» (el agregador lo trataría como una avería).
func TestSourceTextComposer_Compose_NoMessagesIsAnEmptyEnvelope(t *testing.T) {
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
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rig := newComposerRig(t, c.entries...)

			env, err := rig.composer().Compose(composerCtx(), rig.key)

			if err != nil {
				t.Fatalf("Compose devolvió %v, quería nil", err)
			}
			if !env.Empty() {
				t.Errorf("sobre = (enc %d bytes, kek %q), quería el vacío: no hay una sola línea del cliente", len(env.Enc), env.KEKID)
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
			rig.requireUntouchedStore(t)
		})
	}
}

// TestSourceTextComposer_Compose_Failures: la composición fallida en sus dos sitios —leer el hilo y
// cifrar—. El error envuelve el de origen con su texto literal, el sobre devuelto es el vacío y ni
// el error ni el log citan el contenido.
func TestSourceTextComposer_Compose_Failures(t *testing.T) {
	boom := errors.New("fallo de infraestructura")
	cases := []struct {
		name       string
		build      func(rig *composerRig) *SourceTextComposer
		wantPrefix string
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
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rig := newComposerRig(t, composerClient(composerClientText))
			composer := c.build(rig)

			env, err := composer.Compose(composerCtx(), rig.key)

			if !errors.Is(err, boom) {
				t.Fatalf("error = %v, quería uno que envuelva el de origen", err)
			}
			if !strings.HasPrefix(err.Error(), c.wantPrefix) {
				t.Errorf("texto = %q, quería que empezara por %q", err.Error(), c.wantPrefix)
			}
			if !env.Empty() {
				t.Error("una composición fallida devolvió un sobre")
			}
			if got := len(rig.log.all()); got != 0 {
				t.Errorf("una composición fallida logueó %d líneas: el fallo lo registra quien llama", got)
			}
			rig.requireNoContent(t, err)
			rig.requireUntouchedStore(t)
		})
	}
}
