//go:build laboratorio

package main

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake/intakehelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/reanalisis"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/stages"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm/tenantllmhelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"

	"github.com/EduGoGroup/wapp-cloud-platform/laboratorio/common"
)

// reanalysis.go — el re-análisis a petición de la dueña (reanalisis.Service + el worker).

// pasted es lo que la dueña pega al pedir el re-análisis: una llamada que el hilo de WhatsApp no
// tiene. Va al hilo del evento como mensaje «pegado por el dueño» y entra en el literal.
const pasted = "La clienta llamó por teléfono: la torta de chocolate que sea de 12 porciones."

func (l *lab) reanalyze(id string) error {
	l.n.Step("Re-analizar: reanalisis.Service abre un job NUEVO sobre el mismo evento")
	svc, err := reanalisis.NewService(l.box.Log, l.box.Table, l.box.Thread, l.box.Machine,
		l.box.Composer, l.box.Rights, tenantllmhelpertest.NewMemoria(), runtime.DefaultThreadLimit)
	if err != nil {
		l.n.Fail(err, "reanalisis.NewService")
		return err
	}
	l.n.Note("Cableado como el arranque: compositor = el MISMO de la ventana; jobs = la máquina de intake_jobs;")
	l.n.Note("  tenant_llm vacío ⇒ vía local (la del Edge); derechos con llm_intake; límite del hilo %d.", runtime.DefaultThreadLimit)
	l.n.Note("  Intakes = common.IntakeTable: intakes.MemoryStore NO implementa ReanalysisTargetOf (solo *intakes.Postgres).")

	l.box.Rights.Disable(common.LabTenant, entitlements.FeatureLLMIntake)
	_, err = svc.Reanalyze(l.ctx, reanalisis.Request{TenantID: common.LabTenant, IntakeID: id})
	l.n.Fail(err, "sin la feature llm_intake (%T)", err)
	l.box.Rights.Enable(common.LabTenant, entitlements.FeatureLLMIntake)

	l.box.Clock.Advance(15 * time.Minute)
	res, err := svc.Reanalyze(l.ctx, reanalisis.Request{TenantID: common.LabTenant, IntakeID: id, Text: pasted})
	if err != nil {
		l.n.Fail(err, "Reanalyze")
		return err
	}
	l.n.OK("Reanalyze → job %s · vía %q · estado %q · la revisión que nacerá: %d.", res.JobID, res.Via, res.Status, res.RevisionNo)
	entries := l.box.Thread.Entries(l.anaEvent)
	l.n.Note("El hilo del evento tiene ahora %d entrada(s): la transcripción pegada se AÑADIÓ (sellada) al final.", len(entries))
	row, _ := l.box.Machine.View(res.JobID)
	l.n.Note("Job %s en %q · contexto: requested_by %q · via %q · source %q · from %d · sobre del literal %d B cifrados.",
		row.ID, row.Status, row.Reanalysis.RequestedBy, row.Reanalysis.Via, row.Reanalysis.Source, row.Reanalysis.From, len(row.SourceText.Enc))

	_, err = svc.Reanalyze(l.ctx, reanalisis.Request{TenantID: common.LabTenant, IntakeID: id})
	var busy reanalisis.InProgressError
	if errors.As(err, &busy) {
		l.n.Fail(err, "un segundo Reanalyze antes de que el worker termine (InProgressError, 409)")
	} else {
		l.n.Fail(err, "se esperaba InProgressError")
	}

	l.n.Note("El worker lo reclama como cualquier job (RunOnce):")
	evs := len(l.box.Stages.Flow.FlowEvents())
	row, err = l.box.RunJob(l.ctx, res.JobID, func(r common.Round) {
		l.n.Note("RunOnce %d · %s · %q · etapa %q · %s de pared", r.N, r.Row.ID, r.Row.Status, r.Row.Stage, r.Wall.Round(time.Millisecond))
	})
	if err != nil {
		l.n.Fail(err, "worker")
		return err
	}
	if row.IntakeID == "" {
		l.n.Fail(nil, "el job del re-análisis murió %q: %s", row.Status, row.Error)
		l.n.Note("La solicitud se queda como estaba: el re-análisis fallido no borra nada.")
		return nil
	}
	l.n.OK("done · intake_id %s: la MISMA solicitud (UUID v5 del evento), no una nueva.", row.IntakeID[:8])
	l.showIsolated(row)
	return l.afterReanalysis(id, evs)
}

// afterReanalysis enseña lo que dejó el re-análisis y lo que eso le hace a la bandeja.
func (l *lab) afterReanalysis(id string, evs int) error {
	det, err := l.box.Service.Get(l.ctx, common.LabTenant, id)
	if err != nil {
		l.n.Fail(err, "Get")
		return err
	}
	l.n.Note("Solicitud %s · estado %q (NO se toca: draft cuelga la revisión de la fila que ya existía) · %d línea(s) en intake_items.",
		id[:8], det.Status, len(det.Items))
	for _, rev := range det.Revisions {
		l.n.Note("  revisión %d · %-11s · created_by %s", rev.RevisionNo, rev.Kind, rev.CreatedBy)
	}
	if last, ok := intakes.LastRevision(det.Revisions); ok {
		var p struct {
			Analysis json.RawMessage `json:"analysis"`
			Lines    []struct {
				Label     string   `json:"label"`
				UnitPrice *float64 `json:"unit_price"`
			} `json:"lines"`
		}
		if json.Unmarshal(last.Payload, &p) == nil {
			l.n.Show("analysis de la revisión nueva", p.Analysis)
			for i, ln := range p.Lines {
				price := "null"
				if ln.UnitPrice != nil {
					price = "precio"
				}
				l.n.Note("  línea %d · %q · unit_price %s", i, ln.Label, price)
			}
		}
	}
	for _, ev := range l.box.Stages.Flow.FlowEvents()[evs:] {
		l.n.Note("📊 flow_events: %s %q · %v", ev.Kind, ev.Name, ev.Payload)
	}

	l.n.Note("¿Y la bandeja? La revisión VIGENTE vuelve a ser interpreted, con líneas sin precio:")
	svc, err := l.newQuoteService(styleSeed)
	if err != nil {
		return err
	}
	_, err = svc.Suggest(l.ctx, common.LabTenant, id)
	if p, ok := pendingAfter(err); ok {
		l.n.Fail(p, "Suggest tras el re-análisis")
	} else {
		l.n.Fail(err, "Suggest tras el re-análisis (se esperaba PendingPriceError)")
	}
	_, err = l.box.Service.Approve(l.ctx, common.LabTenant, id, "Te confirmo.")
	l.n.Fail(err, "Approve tras el re-análisis")
	l.n.Note("🔑 Las líneas corregidas siguen en intake_items, pero lo que se mira es la ÚLTIMA revisión: la dueña tiene")
	l.n.Note("  que volver a guardar líneas (otra corrected) para poder sugerir o aprobar. Re-analizar deshace la corrección.")
	return nil
}

// showIsolated enseña los ítems que P3 aisló en un job: quedan en artifacts.p3 y en ningún otro
// sitio.
func (l *lab) showIsolated(row intakehelpertest.Row) {
	var p3 stages.P3Artifact
	raw, ok := row.Artifacts[intake.StageP3]
	if !ok || json.Unmarshal(raw, &p3) != nil || len(p3.Isolated) == 0 {
		return
	}
	for _, it := range p3.Isolated {
		l.n.Fail(nil, "P3 AISLÓ la idea nº %d (%s): no llega al match ni al borrador", it.IdeaPos, it.Reason)
	}
	l.n.Note("  🔴 Solo queda en artifacts.p3 del job: ni la revisión ni la bandeja dicen que falta un ítem que el cliente pidió.")
}
