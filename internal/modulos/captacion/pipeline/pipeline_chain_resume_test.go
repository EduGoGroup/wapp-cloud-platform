package pipeline_test

// pipeline_chain_resume_test.go — el sobre del literal, la reanudación por estado y las
// cero ideas, tal como los promete RunOnce. Trozo de pipeline_chain_test.go (E-13).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/pipeline"
)

// TestEnvelope_IsOpenedWithItsThreePieces: el descifrador recibe las tres piezas del sobre
// de la fila, una vez por job.
func TestEnvelope_IsOpenedWithItsThreePieces(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	id := r.seed("")
	r.run(t, id, intake.StatusDone)

	opened := r.decrypter.envelopes()
	if len(opened) != 1 {
		t.Fatalf("el sobre se abrió %d veces, se esperaba 1", len(opened))
	}
	if string(opened[0].enc) != "cifrado" || string(opened[0].dek) != "dek" || opened[0].kek != "kek-1" {
		t.Errorf("el descifrador recibió %+v; se esperaban las tres piezas del sobre del job", opened[0])
	}
}

// TestEnvelope_Incomplete_FailsTheJobWithoutRetry (D-F7-9): el job cuyo sobre no está
// entero muere en el acto, sin reintento, sin descifrar y sin llamar al modelo, con el
// texto del worker. 🔴 Es también lo que le pasa al job reclamado ENTRE el cierre de la
// ventana y la escritura del sobre: esa carrera se porta TAL CUAL y la arregla F8. Este
// test FIJA la conducta; no la celebra.
func TestEnvelope_Incomplete_FailsTheJobWithoutRetry(t *testing.T) {
	cases := map[string]intake.SourceText{
		"no envelope at all": {},
		"no ciphertext":      {DEK: []byte("dek"), KEKID: "kek-1"},
		"no wrapped key":     {Enc: []byte("cifrado"), KEKID: "kek-1"},
		"no key id":          {Enc: []byte("cifrado"), DEK: []byte("dek")},
	}
	const want = "causa=job_invalido stage=ninguna: stages: el job no trae literal que analizar " +
		"(el compositor del flush no llegó a escribir el sobre)"
	for name, envelope := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, pipeline.Config{})
			row := r.healthyRow("")
			row.SourceText = envelope
			id := r.mem.Seed(row)
			r.run(t, id, intake.StatusFailed)

			got := r.row(t, id)
			if got.Error != want {
				t.Errorf("motivo de muerte = %q\n               se esperaba %q", got.Error, want)
			}
			if got.Attempts != 0 {
				t.Errorf("un job inválido no se reintenta ni una vez; attempts=%d", got.Attempts)
			}
			if len(r.decrypter.envelopes()) != 0 {
				t.Error("se intentó descifrar un sobre incompleto")
			}
			if calls := r.trace.list(); len(calls) != 0 {
				t.Errorf("se llamó a %v; sin literal NO se llama a ninguna etapa", calls)
			}
			if retries := r.store.closesOf(opRetry); len(retries) != 0 {
				t.Errorf("hubo %d reencolados; el sobre que no llegó no se espera", len(retries))
			}
			// No vuelve aunque pase el tiempo.
			r.clock.Advance(2 * pipeline.DefaultBackoffCap)
			if n := r.drain(context.Background()); n != 0 {
				t.Errorf("el job muerto volvió a reclamarse (%d)", n)
			}
		})
	}
}

// TestEnvelope_ThatDoesNotDecrypt_IsInfrastructure: el otro modo de «sin literal». El
// sobre está ENTERO pero la KEK no lo desenvuelve: puede ser un KMS caído, así que el job
// vuelve a la cola con el intento cobrado, y tampoco se llama al modelo.
func TestEnvelope_ThatDoesNotDecrypt_IsInfrastructure(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	r.decrypter.text, r.decrypter.err = "", errors.New("la KEK kek-9 no está en el keyring")
	id := r.seed("")
	r.run(t, id, intake.StatusPending)

	if got := r.row(t, id).Attempts; got != 1 {
		t.Errorf("el intento debe cobrarse; attempts=%d", got)
	}
	if calls := r.trace.list(); len(calls) != 0 {
		t.Errorf("se llamó a %v; sin literal NO se llama a ninguna etapa", calls)
	}
	line := r.log.one(t, "WARN", "la etapa falló; el job vuelve a la cola con backoff")
	if line.fields["causa"] != pipeline.CauseInfra || line.fields["stage"] != "" {
		t.Errorf("el tropiezo lleva %v; se esperaba causa=infra y la etapa vacía", line.fields)
	}
	if got := line.fields["error"]; got != "descifrar el literal del job: la KEK kek-9 no está en el keyring" {
		t.Errorf("error = %v", got)
	}
}

// TestEnvelope_ThatDecryptsToNothing_FailsTheJobWithoutRetry: un sobre que abre a la cadena
// vacía tampoco tiene texto que analizar, y no mejora reintentando.
func TestEnvelope_ThatDecryptsToNothing_FailsTheJobWithoutRetry(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	r.decrypter.text = ""
	id := r.seed("")
	r.run(t, id, intake.StatusFailed)

	const want = "causa=job_invalido stage=ninguna: stages: el job no trae literal que analizar " +
		"(el sobre descifró a cadena vacía)"
	row := r.row(t, id)
	if row.Error != want || row.Attempts != 0 {
		t.Errorf("el job quedó (error=%q, attempts=%d)\n       se esperaba error=%q sin reintentos", row.Error, row.Attempts, want)
	}
	if calls := r.trace.list(); len(calls) != 0 {
		t.Errorf("se llamó a %v; sin literal NO se llama a ninguna etapa", calls)
	}
}

// TestResume_SkipsTheStagesWhoseArtifactIsPersisted: un job que vuelve a la cola con `p2`
// y `p3` ya escritos NO los repite. Los artefactos no se fabrican a mano: los deja la
// PRIMERA corrida, que cae en P4.
func TestResume_SkipsTheStagesWhoseArtifactIsPersisted(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	r.p4.script = []step{{err: errors.New("el Edge no contesta")}, {}}
	id := r.seed("")

	r.run(t, id, intake.StatusPending) // 1.ª corrida: P2 y P3 salen, P4 cae
	if _, ok := r.row(t, id).Artifacts[intake.StageP3]; !ok {
		t.Fatal("la 1.ª corrida debía dejar el artefacto de P3 persistido: el test no mira nada")
	}
	r.clock.Advance(2 * pipeline.DefaultBackoffCap)
	r.run(t, id, intake.StatusDone) // 2.ª corrida: de P4 en adelante

	want := []string{intake.StageP2, intake.StageP3, intake.StageP4, intake.StageP4, intake.StageMatch, intake.StageDraft}
	if got := r.trace.list(); !reflect.DeepEqual(got, want) {
		t.Fatalf("las etapas se llamaron %v, se esperaba %v", got, want)
	}
	// Lo que se saltó llega igual a quien lo consume: P4 recibe los ítems del P3 PERSISTIDO.
	if !reflect.DeepEqual(r.p4.items, r.p3.items) {
		t.Errorf("en la reanudación P4 recibió %v, se esperaban los ítems persistidos %v", r.p4.items, r.p3.items)
	}
	if got := len(r.log.find("etapa saltada, su artefacto ya estaba persistido")); got != 2 {
		t.Errorf("hay %d líneas de etapa saltada, se esperaban 2 (p2 y p3)", got)
	}
	if got := r.row(t, id).IntakeID; got != draftIntakeID {
		t.Errorf("el job reanudado termina con su intake_id; quedó %q", got)
	}
}

// TestResume_AFailedDraftRunsAgain_ButNotTheMatchNorTheCatalogRead: la marca
// `artifacts.draft` es lo ÚLTIMO que la etapa escribe; si cayó antes, se repite. Lo de
// antes no: ni el match ni su lectura del catálogo.
func TestResume_AFailedDraftRunsAgain_ButNotTheMatchNorTheCatalogRead(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	r.draft.script = []step{{err: errors.New("la base no contesta")}, {}}
	id := r.seed("")

	r.run(t, id, intake.StatusPending)
	r.clock.Advance(2 * pipeline.DefaultBackoffCap)
	r.run(t, id, intake.StatusDone)

	if r.match.count() != 1 || r.draft.count() != 2 {
		t.Errorf("match se llamó %d veces y draft %d; se esperaba 1 y 2", r.match.count(), r.draft.count())
	}
	if got := r.catalogs.Reads(); got != 1 {
		t.Errorf("el catálogo se leyó %d veces; con el match saltado no se vuelve a leer", got)
	}
	// El borrador de la reanudación recibe el artefacto del match PERSISTIDO.
	if in, _, _ := r.draft.seen(); in.Match == nil || len(in.Match.Lines) != 2 {
		t.Errorf("el draft de la reanudación recibió %+v; se esperaba el match persistido (2 líneas)", in.Match)
	}
}

// TestResume_APersistedDraftIsNeverRunAgain: `draft` NO es idempotente en la revisión —
// repetirla le colgaría OTRA revisión al mismo borrador— y lo que lo evita es que el
// worker se la salta. Aquí la primera corrida escribe los CINCO artefactos y lo que falla
// es el cierre; devuelto el job a la cola, la segunda no llama a NINGUNA etapa y cierra con
// el `intake_id` del artefacto persistido.
func TestResume_APersistedDraftIsNeverRunAgain(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	id := r.seed("")
	r.store.breakOp(opFinish, errors.New("la base no contesta"))
	r.run(t, id, intake.StatusProcessing)
	r.log.one(t, "ERROR", "no se pudo terminar el job; queda en processing").requireKeys(t, "job_id", "error")

	// El rescate (hoy manual: `intake_jobs` no tiene `claimed_at`) lo devuelve a la cola.
	r.store.breakOp(opFinish, nil)
	if ok, err := r.mem.Release(context.Background(), id); !ok || err != nil {
		t.Fatalf("devolver el job a la cola: (%v, %v)", ok, err)
	}
	r.run(t, id, intake.StatusDone)

	if got := r.trace.list(); !reflect.DeepEqual(got, allStages) {
		t.Fatalf("las etapas se llamaron %v; la segunda corrida no debía llamar a ninguna", got)
	}
	if got := r.catalogs.Reads(); got != 1 {
		t.Errorf("el catálogo se leyó %d veces, se esperaba 1", got)
	}
	if got := r.row(t, id).IntakeID; got != draftIntakeID {
		t.Errorf("el job cierra con el intake_id del artefacto persistido; quedó %q", got)
	}
	if got := len(r.log.find("etapa saltada, su artefacto ya estaba persistido")); got != 5 {
		t.Errorf("hay %d líneas de etapa saltada, se esperaban las 5", got)
	}
}

// TestResume_AnUnreadableArtifact_RedoesTheStageInsteadOfKillingTheJob: un artefacto que
// este código no sabe decodificar NO mata el pedido: se rehace la etapa y se avisa, sin
// citar el artefacto (lleva frases del cliente). Uno vacío es como si no estuviera.
func TestResume_AnUnreadableArtifact_RedoesTheStageInsteadOfKillingTheJob(t *testing.T) {
	cases := []struct {
		name   string
		raw    json.RawMessage
		warned bool
	}{
		{"wrong shape", json.RawMessage(`{"version":1,"wants":"el cliente pidió una torta"}`), true},
		{"empty", json.RawMessage(nil), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, pipeline.Config{})
			row := r.healthyRow("")
			row.Stage = intake.StageP2
			row.Artifacts = map[string]json.RawMessage{intake.StageP2: c.raw}
			id := r.mem.Seed(row)
			r.run(t, id, intake.StatusDone)

			if got := r.p2.count(); got != 1 {
				t.Fatalf("la etapa debía REHACERSE: P2 se llamó %d veces", got)
			}
			warnings := r.log.find("el artefacto persistido no se pudo decodificar; la etapa se rehace")
			if (len(warnings) == 1) != c.warned {
				t.Fatalf("avisos de artefacto ilegible = %d, se esperaba aviso = %v", len(warnings), c.warned)
			}
			if !c.warned {
				return
			}
			if warnings[0].level != "WARN" || warnings[0].fields["job_id"] != id || warnings[0].fields["stage"] != intake.StageP2 {
				t.Errorf("el aviso salió %+v; se esperaba un WARN con su job y su etapa", warnings[0])
			}
			if strings.Contains(r.log.dump(), "el cliente pidió") {
				t.Error("el log cita el artefacto persistido, que lleva frases del cliente")
			}
		})
	}
}

// TestZeroIdeas_TheChainGoesOnAndSaysSo: cero ideas vivas tras P2 NO es fatal —un «hola»
// abre ventana, y con cero ideas P3 y P4 no llaman al modelo—, pero queda DICHO, nombrando
// los dos orígenes posibles sin afirmar ninguno y mandando a mirar donde sí está la causa.
func TestZeroIdeas_TheChainGoesOnAndSaysSo(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	r.p2.wants = nil
	id := r.seed("")
	r.run(t, id, intake.StatusDone)

	if got := r.trace.list(); !reflect.DeepEqual(got, allStages) {
		t.Fatalf("con cero ideas se llamaron %v; la cadena sigue entera", got)
	}
	line := r.log.one(t, "WARN", "P2 no dejó ni una idea viva")
	line.requireKeys(t, "job_id", "stage", "causa", "donde_mirar")
	if line.fields["causa"] != "indeterminada_desde_aqui" || line.fields["stage"] != intake.StageP2 {
		t.Errorf("el aviso lleva %v; no puede afirmar una causa que desde aquí no se ve", line.fields)
	}
	if where := fmt.Sprint(line.fields["donde_mirar"]); !strings.Contains(where, "ideas_descartadas") {
		t.Errorf("el aviso debe mandar al log de p2; dice %q", where)
	}
}

// TestZeroIdeas_OnAResumedJobIsSaidAgain: el aviso sale de las ideas que la cadena TIENE,
// vengan de P2 o de su artefacto persistido; y con ideas, no sale.
func TestZeroIdeas_OnAResumedJobIsSaidAgain(t *testing.T) {
	r := newRig(t, pipeline.Config{})
	r.p2.wants = nil
	r.p3.script = []step{{err: errors.New("el Edge no contesta")}, {}}
	id := r.seed("")
	r.run(t, id, intake.StatusPending)
	r.clock.Advance(2 * pipeline.DefaultBackoffCap)
	r.run(t, id, intake.StatusDone)
	if got := len(r.log.find("P2 no dejó ni una idea viva")); got != 2 {
		t.Errorf("el aviso salió %d veces en dos corridas, se esperaban 2", got)
	}

	withIdeas := newRig(t, pipeline.Config{})
	other := withIdeas.seed("")
	withIdeas.run(t, other, intake.StatusDone)
	if got := withIdeas.log.find("P2 no dejó ni una idea viva"); len(got) != 0 {
		t.Errorf("con ideas vivas no hay aviso:\n%s", withIdeas.log.dump())
	}
}
