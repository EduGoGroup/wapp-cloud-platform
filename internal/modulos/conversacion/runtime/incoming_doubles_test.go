//go:build pendiente

package runtime_test

// incoming_doubles_test.go: los dobles PROPIOS de los tests del entrante que el arnés no trae
// (partidos de incoming_test.go e incoming_trigger_test.go por E-13): el almacén con fallos
// inyectables y el segundo Runtime que lo usa, el resolver de disparos espía y el
// OpeningBuilder de guion.

import (
	"context"
	"slices"
	"sync"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
)

// incomingFlakyStore es el almacén del guion con fallos inyectables en las tres lecturas y
// escrituras que el camino del entrante nombra en sus errores: cargar el estado, guardarlo y
// leer los ajustes del tenant. El resto lo hereda del gemelo en memoria.
type incomingFlakyStore struct {
	*store.MemoryRepository
	mu          sync.Mutex
	loadErr     error
	saveErr     error
	settingsErr error
	settings    int
}

func (s *incomingFlakyStore) fail(change func(s *incomingFlakyStore)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	change(s)
}

// Load implementa store.ConversationStore.
func (s *incomingFlakyStore) Load(ctx context.Context, key store.Key) (model.Conversation, bool, error) {
	s.mu.Lock()
	err := s.loadErr
	s.mu.Unlock()
	if err != nil {
		return model.Conversation{}, false, err
	}
	return s.MemoryRepository.Load(ctx, key)
}

// Save implementa store.ConversationStore.
func (s *incomingFlakyStore) Save(ctx context.Context, state model.Conversation) error {
	s.mu.Lock()
	err := s.saveErr
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.MemoryRepository.Save(ctx, state)
}

// GetTenantSettings implementa store.TenantSettingsReader y cuenta las lecturas.
func (s *incomingFlakyStore) GetTenantSettings(ctx context.Context, tenantID string) (store.TenantSettings, error) {
	s.mu.Lock()
	s.settings++
	err := s.settingsErr
	s.mu.Unlock()
	if err != nil {
		return store.TenantSettings{}, err
	}
	return s.MemoryRepository.GetTenantSettings(ctx, tenantID)
}

func (s *incomingFlakyStore) settingsReads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings
}

// incomingFlakyRuntime construye un SEGUNDO Runtime sobre los mismos dobles del guion, con el
// almacén envuelto en incomingFlakyStore y el Sender que se le pase (nil = el del guion). El
// Runtime del arnés (h.rt) no se usa en esos tests: es otro motor, con otro candado.
func incomingFlakyRuntime(h *harness, sender runtime.Sender) (*runtime.Runtime, *incomingFlakyStore) {
	flaky := &incomingFlakyStore{MemoryRepository: h.repo}
	if sender == nil {
		sender = h.sender
	}
	return runtime.New(flaky, h.engine, sender, h.tenants, h.contacts, h.log, h.runtimeOptions()...), flaky
}

// incomingSpyResolver es el resolver de disparos del guion (el ConfigResolver de verdad sobre
// las reglas sembradas) con las señales apuntadas y un fallo inyectable por método.
type incomingSpyResolver struct {
	trigger.Resolver
	mu         sync.Mutex
	signals    []trigger.Signal
	resolveErr error
	escapeErr  error
	liveErr    error
}

// Resolve implementa trigger.Resolver.
func (r *incomingSpyResolver) Resolve(ctx context.Context, tenantID, sessionID string, sig trigger.Signal) (trigger.Decision, error) {
	r.mu.Lock()
	r.signals = append(r.signals, sig)
	err := r.resolveErr
	r.mu.Unlock()
	if err != nil {
		return trigger.Decision{}, err
	}
	return r.Resolver.Resolve(ctx, tenantID, sessionID, sig)
}

// IsEscape implementa trigger.Resolver.
func (r *incomingSpyResolver) IsEscape(ctx context.Context, tenantID, sessionID, text string) (bool, string, error) {
	r.mu.Lock()
	err := r.escapeErr
	r.mu.Unlock()
	if err != nil {
		return false, "", err
	}
	return r.Resolver.IsEscape(ctx, tenantID, sessionID, text)
}

// ResolveLive implementa trigger.Resolver.
func (r *incomingSpyResolver) ResolveLive(ctx context.Context, tenantID, sessionID, text string) (trigger.Decision, error) {
	r.mu.Lock()
	err := r.liveErr
	r.mu.Unlock()
	if err != nil {
		return trigger.Decision{}, err
	}
	return r.Resolver.ResolveLive(ctx, tenantID, sessionID, text)
}

func (r *incomingSpyResolver) seen() []trigger.Signal {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.signals)
}

func (r *incomingSpyResolver) fail(change func(r *incomingSpyResolver)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	change(r)
}

// incomingWithSpyResolver cablea el resolver espía en lugar del del guion y lo deja en *out.
func incomingWithSpyResolver(out **incomingSpyResolver) harnessOption {
	return withOptions(func(h *harness) []runtime.Option {
		spy := &incomingSpyResolver{Resolver: trigger.NewConfigResolver(h.rules)}
		*out = spy
		return []runtime.Option{runtime.WithTriggerResolver(spy)}
	})
}

// incomingOpening es un runtime.OpeningBuilder de guion: contesta siempre la misma oferta y
// cuenta cuántas veces se le pidió. Sin oferta (el valor cero) contesta la vacía.
type incomingOpening struct {
	mu     sync.Mutex
	offer  events.Offering
	err    error
	builds int
}

// BuildOpening implementa runtime.OpeningBuilder.
func (o *incomingOpening) BuildOpening(context.Context, events.ConversationRef) (events.Offering, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.builds++
	return o.offer, o.err
}

// BuildRescue implementa runtime.OpeningBuilder: nada que retomar.
func (o *incomingOpening) BuildRescue(context.Context, events.ConversationRef) (events.Offering, error) {
	return events.Offering{}, nil
}

// BuildTagline implementa runtime.OpeningBuilder: sin coletilla.
func (o *incomingOpening) BuildTagline(context.Context, events.ConversationRef) (string, error) {
	return "", nil
}

func (o *incomingOpening) built() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.builds
}

const incomingOfferText = "Oferta de tipos-qzx"

// incomingOffer es una oferta NO vacía: un texto y un menú con una opción.
func incomingOffer() events.Offering {
	return events.Offering{
		Text: incomingOfferText,
		Menu: events.Menu{Options: []events.MenuOption{{Number: 1, Action: events.ActionStart, Kind: trigger.EventKindCart}}},
	}
}

// incomingWithOpening cablea ese OpeningBuilder en lugar del despachador del guion.
func incomingWithOpening(o *incomingOpening) harnessOption {
	return withOptions(func(*harness) []runtime.Option {
		return []runtime.Option{runtime.WithOpeningBuilder(o)}
	})
}
