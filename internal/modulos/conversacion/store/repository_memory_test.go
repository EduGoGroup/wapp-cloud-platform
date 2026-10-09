package store_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store/storehelpertest"
)

// Los tests de MemoryRepository son EXTERNOS (package store_test): storehelpertest importa store,
// así que un test interno que importara la suite daría un ciclo de imports. Este fichero trae el
// reloj de test y los auxiliares que comparten los repository_memory_*_test.go; el Montaje de la
// suite está en repository_memory_contrato_test.go.
//
// La suite común (storehelpertest.Contrato) afirma lo que el gemelo comparte con Postgres. Los
// demás tests de estos ficheros afirman lo que es SOLO del gemelo: sus mutadores y miradores, el
// reloj inyectado, y lo que la base no deja sembrar (filas sin evento padre, ids que no son UUID).

// Aserciones de compilación de lo que promete repository_memory.go.
var (
	_ func() *store.MemoryRepository                                                                       = store.NewMemoryRepository
	_ func(*store.MemoryRepository, func() time.Time)                                                      = (*store.MemoryRepository).SetClock
	_ func(*store.MemoryRepository, context.Context, store.Key) (bool, error)                              = (*store.MemoryRepository).Exists
	_ func(*store.MemoryRepository, context.Context, store.Key) (model.Conversation, bool, error)          = (*store.MemoryRepository).Load
	_ func(*store.MemoryRepository, context.Context, model.Conversation) error                             = (*store.MemoryRepository).Save
	_ func(*store.MemoryRepository, context.Context, store.Key) error                                      = (*store.MemoryRepository).Delete
	_ func(*store.MemoryRepository, context.Context, string, string, string) error                         = (*store.MemoryRepository).MigrateContactID
	_ func(*store.MemoryRepository, context.Context, string, string) (model.Flow, error)                   = (*store.MemoryRepository).LatestDefinition
	_ func(*store.MemoryRepository, context.Context, string, string, int) (model.Flow, error)              = (*store.MemoryRepository).GetDefinition
	_ func(*store.MemoryRepository, context.Context, string, model.Flow) (int, error)                      = (*store.MemoryRepository).InsertDefinition
	_ func(*store.MemoryRepository, context.Context, string) ([]store.FlowSummary, error)                  = (*store.MemoryRepository).ListDefinitions
	_ func(*store.MemoryRepository, context.Context, []store.SurveyResult) error                           = (*store.MemoryRepository).InsertResults
	_ func(*store.MemoryRepository, context.Context, string, string, string) ([]store.SurveyResult, error) = (*store.MemoryRepository).ListResults
	_ func(*store.MemoryRepository) []store.SurveyResult                                                   = (*store.MemoryRepository).SurveyResults
	_ func(*store.MemoryRepository, context.Context, store.FlowEvent) error                                = (*store.MemoryRepository).InsertFlowEvent
	_ func(*store.MemoryRepository) []store.FlowEvent                                                      = (*store.MemoryRepository).FlowEvents
	_ storehelpertest.Port                                                                                 = (*store.MemoryRepository)(nil)
)

var ctx = context.Background()

// testClock es un reloj que solo avanza cuando el test lo mueve. Arranca después de los instantes
// que la suite pone a mano, que son de agosto de 2026.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock {
	return &testClock{now: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// newMemoryRepository devuelve un repositorio vacío con un reloj de test inyectado.
func newMemoryRepository() (*store.MemoryRepository, *testClock) {
	clock := newTestClock()
	repo := store.NewMemoryRepository()
	repo.SetClock(clock.Now)
	return repo, clock
}

// conversation es un estado mínimo de esa clave en ese nodo.
func conversation(k store.Key, node string) model.Conversation {
	return model.Conversation{TenantID: k.TenantID, SessionID: k.SessionID, ContactID: k.ContactID,
		FlowID: "menu", FlowVersion: 1, CurrentNode: node}
}

// mustSave guarda el estado o falla el test.
func mustSave(t *testing.T, repo *store.MemoryRepository, c model.Conversation) {
	t.Helper()
	if err := repo.Save(ctx, c); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

// nodeAt devuelve el nodo de la conversación de la clave, o "" si no hay conversación.
func nodeAt(t *testing.T, repo *store.MemoryRepository, k store.Key) string {
	t.Helper()
	got, found, err := repo.Load(ctx, k)
	if err != nil {
		t.Fatalf("Load(%s): %v", k, err)
	}
	if !found {
		return ""
	}
	return got.CurrentNode
}

// TestNewMemoryRepository_EmptyAndUsesTheProcessClock: un repositorio nuevo no tiene nada y, sin
// SetClock, fecha con el reloj del proceso (un instante que no es cero).
func TestNewMemoryRepository_EmptyAndUsesTheProcessClock(t *testing.T) {
	repo := store.NewMemoryRepository()
	if n := len(repo.FlowEvents()) + len(repo.SurveyResults()); n != 0 {
		t.Errorf("un repositorio nuevo trae %d filas, quería ninguna", n)
	}
	k := store.Key{TenantID: "t1", SessionID: "s1", ContactID: "573001112233"}
	if found, err := repo.Exists(ctx, k); err != nil || found {
		t.Errorf("Exists en un repositorio nuevo = (%v, %v), quería (false, nil)", found, err)
	}
	// Acepta ids que no son UUID: no tiene tipos de columna.
	mustSave(t, repo, conversation(k, "root"))
	got, found, err := repo.Load(ctx, k)
	if err != nil || !found || got.UpdatedAt.IsZero() {
		t.Errorf("Load = (%+v, %v, %v), quería la conversación fechada por el reloj del proceso", got, found, err)
	}
}

// TestMemoryRepository_SetClock: desde SetClock todo lo que el repositorio fecha sale de ese
// reloj; no remarca lo ya guardado; un reloj nil se ignora.
func TestMemoryRepository_SetClock(t *testing.T) {
	repo, clock := newMemoryRepository()
	first := store.Key{TenantID: "t1", SessionID: "s1", ContactID: "c1"}
	second := store.Key{TenantID: "t1", SessionID: "s1", ContactID: "c2"}
	mustSave(t, repo, conversation(first, "root"))
	t0 := clock.Now()

	clock.Advance(3 * time.Hour)
	repo.SetClock(nil)
	mustSave(t, repo, conversation(second, "root"))

	for k, want := range map[store.Key]time.Time{first: t0, second: t0.Add(3 * time.Hour)} {
		got, _, err := repo.Load(ctx, k)
		if err != nil || !got.UpdatedAt.Equal(want) {
			t.Errorf("UpdatedAt de %s = %v (%v), quería %v", k, got.UpdatedAt, err, want)
		}
	}

	fixed := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	repo.SetClock(func() time.Time { return fixed })
	if err := repo.InsertResults(ctx, []store.SurveyResult{{TenantID: "t1", ContactID: "c1", FlowID: "menu", QuestionID: "q1"}}); err != nil {
		t.Fatalf("InsertResults: %v", err)
	}
	if got := repo.SurveyResults(); len(got) != 1 || !got[0].CreatedAt.Equal(fixed) {
		t.Errorf("la respuesta se fechó con %+v, quería el reloj nuevo (%v)", got, fixed)
	}
}

// TestMemoryRepository_MigrateContactID: pasa el estado del huérfano al canónico en TODAS las
// sesiones del tenant; si el canónico ya tenía estado en una sesión se conserva el suyo y se
// descarta el del huérfano; no toca otros tenants ni otros contactos; sin estado es un no-op.
func TestMemoryRepository_MigrateContactID(t *testing.T) {
	repo, _ := newMemoryRepository()
	key := func(tenant, session, contact string) store.Key {
		return store.Key{TenantID: tenant, SessionID: session, ContactID: contact}
	}
	mustSave(t, repo, conversation(key("t1", "s1", "orphan"), "del huérfano en s1"))
	mustSave(t, repo, conversation(key("t1", "s2", "orphan"), "del huérfano en s2"))
	mustSave(t, repo, conversation(key("t1", "s2", "canonical"), "del canónico en s2"))
	mustSave(t, repo, conversation(key("t1", "s1", "bystander"), "de otro contacto"))
	mustSave(t, repo, conversation(key("t2", "s1", "orphan"), "de otro tenant"))

	if err := repo.MigrateContactID(ctx, "t1", "orphan", "canonical"); err != nil {
		t.Fatalf("MigrateContactID: %v", err)
	}
	for k, want := range map[store.Key]string{
		key("t1", "s1", "orphan"):    "",
		key("t1", "s2", "orphan"):    "",
		key("t1", "s1", "canonical"): "del huérfano en s1",
		key("t1", "s2", "canonical"): "del canónico en s2",
		key("t1", "s1", "bystander"): "de otro contacto",
		key("t2", "s1", "orphan"):    "de otro tenant",
	} {
		if got := nodeAt(t, repo, k); got != want {
			t.Errorf("tras la migración, %s está en %q, quería %q", k, got, want)
		}
	}
	moved, _, err := repo.Load(ctx, key("t1", "s1", "canonical"))
	if err != nil || moved.ContactID != "canonical" || moved.TenantID != "t1" || moved.SessionID != "s1" {
		t.Errorf("el estado migrado lleva la clave (%q, %q, %q), quería la del canónico (%v)", moved.TenantID, moved.SessionID, moved.ContactID, err)
	}

	if err := repo.MigrateContactID(ctx, "t1", "nobody", "canonical"); err != nil {
		t.Errorf("MigrateContactID sin estado que mover = %v, quería nil", err)
	}
	if got := nodeAt(t, repo, key("t1", "s1", "canonical")); got != "del huérfano en s1" {
		t.Errorf("una migración sin estado que mover cambió al canónico: %q", got)
	}
}

// TestMemoryRepository_FlowEventsMirror: el mirador del outbox devuelve los efectos de TODOS los
// tenants, en orden de escritura, y una COPIA. Lo que solo el gemelo hace: conserva el payload nil
// tal cual (Postgres lo materializa como `{}`).
func TestMemoryRepository_FlowEventsMirror(t *testing.T) {
	repo, _ := newMemoryRepository()
	for _, tenant := range []string{"t1", "t2"} {
		if err := repo.InsertFlowEvent(ctx, store.FlowEvent{TenantID: tenant, Name: "de " + tenant}); err != nil {
			t.Fatalf("InsertFlowEvent: %v", err)
		}
	}
	events := repo.FlowEvents()
	requireEqual(t, "efectos guardados", len(events), 2)
	requireEqual(t, "primer efecto", events[0].Name, "de t1")
	requireEqual(t, "segundo efecto", events[1].Name, "de t2")
	requireEqual(t, "el payload nil se conserva nil", events[0].Payload == nil, true)

	events[0].Name = "mutado"
	requireEqual(t, "lo guardado tras mutar lo que devuelve FlowEvents", repo.FlowEvents()[0].Name, "de t1")
}

// TestMemoryRepository_SurveyResultsMirror: el mirador de las respuestas devuelve las de TODOS los
// tenants, en orden de escritura, y una COPIA. Lo que solo el gemelo hace: acepta una respuesta sin
// evento y respeta el CreatedAt que trae una fila (fecha con el reloj la que no lo trae). Y
// ListResults devuelve el EventID.
func TestMemoryRepository_SurveyResultsMirror(t *testing.T) {
	repo, clock := newMemoryRepository()
	own := time.Date(2020, 5, 5, 0, 0, 0, 0, time.UTC)
	rows := []store.SurveyResult{
		{TenantID: "t1", ContactID: "c1", FlowID: "menu", QuestionID: "q1", AnswerCode: "sin evento"},
		{TenantID: "t2", ContactID: "c1", FlowID: "menu", QuestionID: "q1", AnswerCode: "con fecha", EventID: "e-1", CreatedAt: own},
	}
	if err := repo.InsertResults(ctx, rows); err != nil {
		t.Fatalf("InsertResults: %v", err)
	}
	rows[0].AnswerCode = "mutado tras guardar"

	results := repo.SurveyResults()
	requireEqual(t, "respuestas guardadas", len(results), 2)
	requireEqual(t, "la primera, tras mutar la del llamante", results[0].AnswerCode, "sin evento")
	requireEqual(t, "la primera se fecha con el reloj", results[0].CreatedAt, clock.Now())
	requireEqual(t, "la segunda conserva su fecha", results[1].CreatedAt, own)

	results[1].AnswerCode = "mutado"
	listed, err := repo.ListResults(ctx, "t2", "c1", "menu")
	if err != nil {
		t.Fatalf("ListResults: %v", err)
	}
	requireEqual(t, "respuestas de t2", len(listed), 1)
	requireEqual(t, "la de t2, tras mutar lo que devuelve SurveyResults", listed[0].AnswerCode, "con fecha")
	requireEqual(t, "ListResults devuelve el EventID", listed[0].EventID, "e-1")
}

// TestMemoryRepository_ConcurrentUse: cada método es atómico; usarlo desde varias goroutines no
// pierde escrituras (y con -race, no hay carrera de datos).
func TestMemoryRepository_ConcurrentUse(t *testing.T) {
	repo, _ := newMemoryRepository()
	const writers = 16
	var done sync.WaitGroup
	for i := range writers {
		done.Add(1)
		go func() {
			defer done.Done()
			k := store.Key{TenantID: "t1", SessionID: "s1", ContactID: uuid.NewString()}
			if err := repo.Save(ctx, conversation(k, "root")); err != nil {
				t.Errorf("Save %d: %v", i, err)
			}
			if _, err := repo.InsertDefinition(ctx, "t1", model.Flow{FlowID: "menu"}); err != nil {
				t.Errorf("InsertDefinition %d: %v", i, err)
			}
			if err := repo.InsertFlowEvent(ctx, store.FlowEvent{TenantID: "t1", Payload: map[string]any{"i": i}}); err != nil {
				t.Errorf("InsertFlowEvent %d: %v", i, err)
			}
			if _, _, err := repo.Load(ctx, k); err != nil {
				t.Errorf("Load %d: %v", i, err)
			}
		}()
	}
	done.Wait()
	latest, err := repo.LatestDefinition(ctx, "t1", "menu")
	if err != nil || latest.Version != writers {
		t.Errorf("LatestDefinition = (v%d, %v), quería la versión %d: una por escritor", latest.Version, err, writers)
	}
	if got := len(repo.FlowEvents()); got != writers {
		t.Errorf("%d efectos guardados, quería %d", got, writers)
	}
}
