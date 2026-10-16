//go:build laboratorio

package common

import (
	"context"
	"fmt"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements/entitlementshelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake/intakehelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/pipeline"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events/eventshelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime/runtimehelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/llmvia"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/llmvia/local"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes/telemetria"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// Inbox es el montaje de la BANDEJA (E13 y E14): el pipeline real que deja un borrador y, detrás,
// el servicio real de solicitudes con su salida hacia el cliente.
//
//   - REAL: el compositor del literal, el cifrador de la PII (llaves efímeras), el worker y las
//     cinco etapas, intakes.Service con su Notifier (la voz de la dueña hacia WhatsApp) y su
//     telemetría (telemetria.New sobre el MISMO almacén de flujos que la etapa draft), y el
//     resolver de contactos en memoria (la vía custodiada que traduce el contact_id opaco).
//   - DOBLE: el hilo del evento, la máquina de intake_jobs, los almacenes, los derechos, el
//     reloj, el Sender (runtimehelpertest: apunta lo que el Edge habría mandado) y la inferencia.
//   - COSTURA: IntakeTable, que hace que el borrador escriba la cabecera en el doble de la bandeja.
//
// Lo que NO lleva, y por qué: ni el puente CRM (WithCRMPusher: es otra pieza, el webhook_outbox),
// ni los recordatorios perezosos de seña y plazo (WithDepositReminder / WithExpiryReminder: solo
// actúan sobre solicitudes en deposit_requested o vencidas, que estos escalones no fabrican).
type Inbox struct {
	Clock     *Clock
	Log       logger.Logger
	Cipher    *crypto.FieldCipher
	Thread    *eventshelpertest.Store
	Composer  *runtime.SourceTextComposer
	Machine   *intakehelpertest.MachineMemory
	Stages    *IntakeStages
	Selector  *llmvia.Selector
	Worker    *pipeline.Worker
	Rights    *entitlementshelpertest.Fake
	Contacts  *contact.MemoryResolver
	Sender    *runtimehelpertest.Sender
	Notifier  *intakes.Notifier
	Service   *intakes.Service
	Intakes   *intakes.MemoryStore
	Table     *IntakeTable
	frameName string
}

// NewInbox arma la bandeja sobre el frame dado (normalmente el de FrameFromFlags), con el reloj
// manual parado en AmberStart.
func NewInbox(frame local.Frame, verbose bool) (*Inbox, error) {
	b := &Inbox{Clock: NewClock(time.Time{}), Log: NewLog(verbose)}
	var err error
	if b.Cipher, _, err = NewCipher(); err != nil {
		return nil, err
	}
	b.Thread = eventshelpertest.NewStore()
	b.Thread.SetClock(b.Clock.Now)
	b.Composer = runtime.NewSourceTextComposer(b.Log, b.Thread, b.Cipher)
	b.Rights = entitlementshelpertest.NewFake()
	b.Rights.Enable(LabTenant, entitlements.FeatureLLMIntake)

	if b.Selector, err = NewSelector(frame, b.Log); err != nil {
		return nil, fmt.Errorf("common: selector: %w", err)
	}
	b.Machine = intakehelpertest.NewMachineMemory(b.Clock.Now)
	if b.Stages, err = NewIntakeStages(b.Log, b.Selector, b.Machine, b.Clock.Now, WithHeaderInIntakes()); err != nil {
		return nil, err
	}
	b.Intakes, b.Table = b.Stages.Intakes, b.Stages.Table
	b.Table.Events = b.Thread
	idx, err := AmberIndex()
	if err != nil {
		return nil, err
	}
	b.Worker, err = pipeline.NewWorker(b.Log, b.Machine,
		b.Stages.P2, b.Stages.P3, b.Stages.P4, b.Stages.Match, b.Stages.Draft,
		StaticCatalog{Index: idx}, b.Cipher, pipeline.Config{},
		pipeline.WithClock(b.Clock.Now), pipeline.WithShippingZones(b.Intakes))
	if err != nil {
		return nil, fmt.Errorf("common: worker: %w", err)
	}

	// La salida hacia el cliente, cableada como en internal/arranque/fase6_solicitudes.go: UN
	// notificador para las dos opciones (WithNotifier y WithQuoteSender).
	b.Contacts = contact.NewMemoryResolver(nil)
	b.Sender = runtimehelpertest.NewSender()
	b.Notifier = intakes.NewNotifier(b.Sender, b.Contacts, b.Intakes, b.Log)
	b.Service = intakes.NewService(b.Intakes,
		intakes.WithNotifier(b.Notifier),
		intakes.WithQuoteSender(b.Notifier),
		intakes.WithMetrics(telemetria.New(b.Stages.Flow), b.Log),
		intakes.WithMetricsClock(b.Clock.Now),
		intakes.WithClock(b.Clock.Now))
	return b, nil
}

// NewCustomer da de alta (o encuentra) al cliente por su teléfono en el resolver de contactos y
// devuelve su contact_id OPACO: lo único que guardan la ventana, el job y la solicitud.
func (b *Inbox) NewCustomer(ctx context.Context, phone, pushName string) (string, error) {
	ref, err := contact.NewRef(contact.KindPhoneE164, phone)
	if err != nil {
		return "", err
	}
	return b.Contacts.Resolve(ctx, LabTenant, []contact.Ref{ref}, pushName)
}

// Drafted es lo que deja DraftFromMessages.
type Drafted struct {
	EventID  string
	JobID    string
	IntakeID string
	Row      intakehelpertest.Row
}

// Round es una vuelta del worker vista desde fuera: el estado del job al terminarla.
type Round struct {
	N    int
	Row  intakehelpertest.Row
	Wall time.Duration
	Wait time.Duration // el backoff que se saltó el reloj antes de la vuelta siguiente
}

// DraftFromMessages abre un evento, escribe los mensajes en su hilo (15 s entre uno y otro), y
// SIN VENTANA —E11 ya la enseñó— le pide al compositor real que componga y selle el literal, como
// hace la puerta del re-análisis. Siembra el job `pending` con ese sobre y deja que el worker real
// lo lleve hasta done o failed. onRound, si no es nil, ve cada vuelta.
func (b *Inbox) DraftFromMessages(ctx context.Context, session, contactID string, messages []string, onRound func(Round)) (Drafted, error) {
	ev, err := b.Thread.CreateEvent(ctx, events.NewEvent{TenantID: LabTenant, SessionID: session, ContactID: contactID, Kind: "lab_intake"})
	if err != nil {
		return Drafted{}, fmt.Errorf("common: evento: %w", err)
	}
	first := b.Clock.Now()
	for i, text := range messages {
		if i > 0 {
			b.Clock.Advance(15 * time.Second)
		}
		if _, err := b.Thread.AppendMessage(ctx, ev.ID, events.RoleClient, text); err != nil {
			return Drafted{}, fmt.Errorf("common: hilo: %w", err)
		}
	}
	key := intake.WindowKey{TenantID: LabTenant, SessionID: session, ContactID: contactID, EventID: ev.ID}
	env, err := b.Composer.Compose(ctx, key)
	if err != nil {
		return Drafted{}, fmt.Errorf("common: componer el literal: %w", err)
	}
	now := b.Clock.Now()
	jobID := b.Machine.Seed(intakehelpertest.Row{
		Key: key, Status: intake.StatusPending, MessageTS: first, SourceText: env, CreatedAt: now, UpdatedAt: now,
	})
	row, err := b.RunJob(ctx, jobID, onRound)
	if err != nil {
		return Drafted{}, err
	}
	return Drafted{EventID: ev.ID, JobID: jobID, IntakeID: row.IntakeID, Row: row}, nil
}

// RunJob hace lo que Run hace cada 5 s —RunOnce— hasta que el job jobID termina (done o failed).
// Ante un reintento con backoff, adelanta el reloj manual hasta la marca.
func (b *Inbox) RunJob(ctx context.Context, jobID string, onRound func(Round)) (intakehelpertest.Row, error) {
	for n := 1; n <= pipeline.DefaultMaxQualityAttempts+1; n++ {
		wall := time.Now()
		found, err := b.Worker.RunOnce(ctx)
		if err != nil {
			return intakehelpertest.Row{}, fmt.Errorf("common: RunOnce: %w", err)
		}
		row, _ := b.Machine.View(jobID)
		r := Round{N: n, Row: row, Wall: time.Since(wall)}
		if !found {
			return row, fmt.Errorf("common: RunOnce no encontró nada que reclamar (job %s en %q)", jobID, row.Status)
		}
		if !intake.IsTerminal(row.Status) && row.NextAttemptAt.After(b.Clock.Now()) {
			r.Wait = row.NextAttemptAt.Sub(b.Clock.Now())
			b.Clock.Advance(r.Wait)
		}
		if onRound != nil {
			onRound(r)
		}
		if intake.IsTerminal(row.Status) {
			return row, nil
		}
	}
	row, _ := b.Machine.View(jobID)
	return row, fmt.Errorf("common: el job %s no terminó (estado %q)", jobID, row.Status)
}
