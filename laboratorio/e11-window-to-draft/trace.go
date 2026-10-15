//go:build laboratorio

package main

import (
	"context"
	"errors"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake/intakehelpertest"

	"github.com/EduGoGroup/wapp-cloud-platform/laboratorio/common"
)

var errUnexpected = errors.New("e11: el montaje no se comportó como se esperaba")

// tracingStore es la máquina de intake_jobs en memoria con un NARRADOR delante: cada transición que
// el worker pide (reclamar, guardar etapa, reencolar, terminar, matar, soltar) sale por pantalla con
// el estado de antes y el de después. No cambia nada de lo que hace MachineMemory.
type tracingStore struct {
	*intakehelpertest.MachineMemory
	n      *common.Narrator
	clock  *common.Clock
	start  time.Time
	lastID string
}

var _ intake.PipelineStore = (*tracingStore)(nil)

func (t *tracingStore) rel() string { return "t+" + t.clock.Now().Sub(t.start).String() }

// ClaimNext: pending → processing.
func (t *tracingStore) ClaimNext(ctx context.Context) (intake.ClaimedJob, bool, error) {
	job, found, err := t.MachineMemory.ClaimNext(ctx)
	if found {
		t.lastID = job.ID
		resumed := ""
		if len(job.Artifacts) > 0 {
			resumed = " · trae artefactos de un intento anterior: esas etapas se SALTAN"
		}
		t.n.Note("%s · %s  pending → processing (ClaimNext, intento %d)%s", t.rel(), job.ID, job.Attempts+1, resumed)
	}
	return job, found, err
}

// SaveStage: el artefacto de una etapa queda escrito y `stage` avanza.
func (t *tracingStore) SaveStage(ctx context.Context, jobID string, a intake.Artifact) (bool, error) {
	ok, err := t.MachineMemory.SaveStage(ctx, jobID, a)
	t.n.Note("%s · %s  processing · etapa %-5s guardada (%d bytes)", t.rel(), jobID, a.Stage, len(a.Payload))
	return ok, err
}

// Retry: processing → pending con la marca del backoff.
func (t *tracingStore) Retry(ctx context.Context, jobID string, next time.Time) (bool, error) {
	ok, err := t.MachineMemory.Retry(ctx, jobID, next)
	t.n.Fail(nil, "%s · %s  processing → pending (Retry): la etapa falló, vuelve a la cola hasta %s", t.rel(), jobID, "t+"+next.Sub(t.start).String())
	return ok, err
}

// Finish: processing → done, con el intake_id del borrador.
func (t *tracingStore) Finish(ctx context.Context, jobID, intakeID string) (bool, error) {
	ok, err := t.MachineMemory.Finish(ctx, jobID, intakeID)
	t.n.OK("%s · %s  processing → done (Finish) · intake_id %s", t.rel(), jobID, intakeID)
	return ok, err
}

// Fail: processing → failed, con la causa.
func (t *tracingStore) Fail(ctx context.Context, jobID, reason string) (bool, error) {
	ok, err := t.MachineMemory.Fail(ctx, jobID, reason)
	t.n.Fail(nil, "%s · %s  processing → failed (Fail): %s", t.rel(), jobID, reason)
	return ok, err
}

// Release: processing → pending SIN castigo (apagado ordenado).
func (t *tracingStore) Release(ctx context.Context, jobID string) (bool, error) {
	ok, err := t.MachineMemory.Release(ctx, jobID)
	t.n.Note("%s · %s  processing → pending (Release, sin castigo)", t.rel(), jobID)
	return ok, err
}
