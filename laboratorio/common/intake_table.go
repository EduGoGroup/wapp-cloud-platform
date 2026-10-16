//go:build laboratorio

package common

import (
	"context"
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/stages"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// IntakeTable es la COSTURA del laboratorio para la tabla `intakes`: deja que el borrador del
// pipeline (stages.IntakeStore) y la bandeja (intakes.Service, sobre intakes.MemoryStore) vean LA
// MISMA fila, como en producción, donde las dos caras son `public.intakes` en Postgres.
//
// Por qué hace falta: el borrador escribe la cabecera por el puerto del almacén de flujos
// (`conversacion/store`, store.Intake) y la bandeja la lee por el suyo (`solicitudes/intakes`,
// intakes.Intake). En Postgres es la misma tabla; en memoria hay dos dobles que no se hablan, y
// ninguno cumple los dos puertos. Este adaptador escribe la cabecera DIRECTAMENTE en el doble de
// la bandeja (MemoryStore.Add + BindEvent) y responde la pregunta del borrador leyendo de él.
//
// Cumple además el puerto del re-análisis (reanalisis.Intakes): intakes.MemoryStore NO implementa
// ReanalysisTargetOf —solo lo hace *intakes.Postgres—, y aquí se reproduce su consulta en memoria.
// Es lo ÚNICO de E13 y E14 que no existe en producción.
type IntakeTable struct {
	store *intakes.MemoryStore
	now   func() time.Time
	// Events, si no es nil, da el estado del evento padre al nacer la solicitud (la tabla
	// conversation_events reducida que imita MemoryStore.SetEvent).
	Events interface {
		GetEventForTenant(ctx context.Context, tenantID, eventID string) (events.Event, error)
	}

	mu      sync.Mutex
	byEvent map[string]string // tenant\x00evento → intake_id
	eventOf map[string]string // intake_id → evento
}

var _ stages.IntakeStore = (*IntakeTable)(nil)

// NewIntakeTable crea la costura sobre el doble de la bandeja; now fecha las cabeceras nuevas.
func NewIntakeTable(st *intakes.MemoryStore, now func() time.Time) *IntakeTable {
	if now == nil {
		now = time.Now
	}
	return &IntakeTable{store: st, now: now, byEvent: map[string]string{}, eventOf: map[string]string{}}
}

func eventKey(tenantID, eventID string) string { return tenantID + "\x00" + eventID }

// GetIntakeByEvent implementa stages.IntakeStore: la solicitud del evento, SIN filtrar por estado
// (D-044.46), leída del doble de la bandeja.
func (t *IntakeTable) GetIntakeByEvent(ctx context.Context, tenantID, eventID string) (store.Intake, bool, error) {
	t.mu.Lock()
	id, ok := t.byEvent[eventKey(tenantID, eventID)]
	t.mu.Unlock()
	if !ok {
		return store.Intake{}, false, nil
	}
	d, err := t.store.Get(ctx, tenantID, id)
	if err != nil {
		return store.Intake{}, false, err
	}
	return store.Intake{
		ID: d.ID, TenantID: tenantID, ContactID: d.ContactID, SessionID: d.SessionID,
		Status: d.Status, Total: d.Total, EventID: eventID,
		CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt, CustomerNote: d.CustomerNote,
	}, true, nil
}

// UpsertIntake implementa stages.IntakeStore. El borrador solo lo llama cuando el evento NO tenía
// solicitud, así que aquí es siempre un NACIMIENTO: la fila entra en el doble de la bandeja con su
// estado tal cual (pending_approval), ligada a su evento.
func (t *IntakeTable) UpsertIntake(ctx context.Context, o store.Intake) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, exists := t.eventOf[o.ID]; exists {
		return nil
	}
	now := t.now()
	t.store.Add(o.TenantID, intakes.Intake{
		ID: o.ID, ContactID: o.ContactID, SessionID: o.SessionID, Status: o.Status,
		Total: o.Total, CreatedAt: now, UpdatedAt: now,
	})
	t.store.BindEvent(o.ID, o.EventID)
	status := string(events.StatusOpen)
	if t.Events != nil {
		if ev, err := t.Events.GetEventForTenant(ctx, o.TenantID, o.EventID); err == nil {
			status = string(ev.Status)
		}
	}
	t.store.SetEvent(o.EventID, status)
	t.byEvent[eventKey(o.TenantID, o.EventID)] = o.ID
	t.eventOf[o.ID] = o.EventID
	return nil
}

// EventOf es el evento padre de una solicitud («» si no nació por aquí).
func (t *IntakeTable) EventOf(intakeID string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.eventOf[intakeID]
}

// ReanalysisTargetOf reproduce en memoria la consulta de *intakes.Postgres: sesión, contacto,
// evento, estado normalizado y el número de la última revisión (0 si no tiene). ErrNotFound si la
// solicitud no es del tenant.
func (t *IntakeTable) ReanalysisTargetOf(ctx context.Context, tenantID, intakeID string) (intakes.ReanalysisTarget, error) {
	d, err := t.store.Get(ctx, tenantID, intakeID)
	if err != nil {
		return intakes.ReanalysisTarget{}, err
	}
	last := 0
	if rev, ok := intakes.LastRevision(d.Revisions); ok {
		last = rev.RevisionNo
	}
	return intakes.ReanalysisTarget{
		SessionID: d.SessionID, ContactID: d.ContactID, EventID: t.EventOf(intakeID),
		Status: intakes.NormalizeStatus(d.Status), LastRevisionNo: last,
	}, nil
}
