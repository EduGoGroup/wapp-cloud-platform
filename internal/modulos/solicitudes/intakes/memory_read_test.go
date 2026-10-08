package intakes_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// Aserciones de compilación de lo que promete memory_read.go.
var (
	_ func(*intakes.MemoryStore, context.Context, string, intakes.Filter) ([]intakes.Intake, int, error) = (*intakes.MemoryStore).List
	_ func(*intakes.MemoryStore, context.Context, string, intakes.Filter, int) ([]intakes.Detail, error) = (*intakes.MemoryStore).ListDetails
	_ func(*intakes.MemoryStore, context.Context, string, string) (intakes.Detail, error)                = (*intakes.MemoryStore).Get
	_ func(*intakes.MemoryStore, string) []intakes.Revision                                              = (*intakes.MemoryStore).Revisions
	_ func(*intakes.MemoryStore, string) []intakes.Revision                                              = (*intakes.MemoryStore).PersistedRevisions
	_ func(*intakes.MemoryStore, context.Context, string) ([]intakes.ShippingZone, error)                = (*intakes.MemoryStore).ShippingZones
	_ func(*intakes.MemoryStore, context.Context, string) (intakes.NotifySettings, error)                = (*intakes.MemoryStore).NotifySettings
	_ func(*intakes.MemoryStore, context.Context, string, int) ([]string, error)                         = (*intakes.MemoryStore).ApprovedRenderedTexts
	_ func(*intakes.MemoryStore, string, string) string                                                  = (*intakes.MemoryStore).StoredStatus
	_ func(*intakes.MemoryStore, string) intakes.BuyerData                                               = (*intakes.MemoryStore).BuyerDataOf
)

// logEntry es una emisión capturada: nivel, mensaje y sus pares clave/valor.
type logEntry struct {
	level, msg string
	args       []any
}

// recLogger es un logger.Logger que guarda lo que se emite, para OBSERVAR la poda.
type recLogger struct {
	mu      sync.Mutex
	entries []logEntry
}

func (l *recLogger) add(level, msg string, args []any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, logEntry{level: level, msg: msg, args: args})
}

func (l *recLogger) Debug(msg string, args ...any) { l.add("debug", msg, args) }
func (l *recLogger) Info(msg string, args ...any)  { l.add("info", msg, args) }
func (l *recLogger) Warn(msg string, args ...any)  { l.add("warn", msg, args) }
func (l *recLogger) Error(msg string, args ...any) { l.add("error", msg, args) }
func (l *recLogger) With(...any) logger.Logger     { return l }
func (l *recLogger) snapshot() []logEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.entries)
}
func (e logEntry) String() string { return fmt.Sprint(e.level, " ", e.msg, " ", e.args) }
func (e logEntry) value(key string) (v any, ok bool) {
	for i := 0; i+1 < len(e.args); i += 2 {
		if e.args[i] == key {
			return e.args[i+1], true
		}
	}
	return nil, false
}

// El literal del cliente de los tests de retención, y el payload con y sin él.
const (
	sourceText  = "quiero dos panes sin sal para la Sra. Marta"
	withLiteral = `{"version":1,"source_text":"` + sourceText + `","lines":[{"sku":"pan","qty":2,"evidence":"dos panes"}],"total":4}`
	noLiteral   = `{"version":1,"lines":[{"sku":"pan","qty":2}],"total":4}`
)

// equalJSON compara dos payloads por su contenido.
func equalJSON(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// retentionStore es un store con el logger de retención capturado y una solicitud "i-1" con dos
// revisiones escritas en el instante inicial del reloj: la 1 del carrito (sin literal) y la 2
// interpretada (con literal).
func retentionStore(t *testing.T) (*intakes.MemoryStore, *testClock, *recLogger) {
	t.Helper()
	store, clock := newMemoryStore()
	log := &recLogger{}
	store.SetRetentionLog(log)
	store.SetRetentionLog(nil) // se ignora: sigue mandando `log`
	store.Add(tenant1, seeded("i-1", intakes.StatusPendingApproval, 1))
	for _, rev := range []intakes.Revision{
		{IntakeID: "i-1", Kind: intakes.RevisionKindCart, Payload: []byte(`{"version":1,"total":4}`)},
		{IntakeID: "i-1", Kind: intakes.RevisionKindInterpreted, Payload: []byte(withLiteral)},
	} {
		if _, err := store.InsertRevision(ctx, rev); err != nil {
			t.Fatalf("sembrando la revisión %s: %v", rev.Kind, err)
		}
	}
	return store, clock, log
}

// TestMemoryStore_Revisions_ReturnsTheLiteralWhileItIsRetained: dentro del plazo, las dos lecturas
// —el mirador y el detalle— traen el literal en su sitio, sin sello y sin anunciar nada; lo
// GUARDADO no lo lleva; y leer dos veces no acumula.
func TestMemoryStore_Revisions_ReturnsTheLiteralWhileItIsRetained(t *testing.T) {
	store, clock, log := retentionStore(t)
	clock.Advance(intakes.DefaultLiteralTTL - time.Hour)
	for i := range 2 {
		for name, revs := range map[string][]intakes.Revision{
			"Revisions": store.Revisions("i-1"),
			"Get":       mustGet(t, store, tenant1, "i-1").Revisions,
		} {
			if len(revs) != 2 || revs[0].RevisionNo != 1 || revs[1].RevisionNo != 2 {
				t.Fatalf("%s (lectura %d) = %+v, quería las revisiones 1 y 2 en orden", name, i, revs)
			}
			if !equalJSON(revs[1].Payload, []byte(withLiteral)) {
				t.Errorf("%s (lectura %d): payload = %s, quería el literal en su sitio", name, i, revs[1].Payload)
			}
			if !revs[1].LiteralPrunedAt.IsZero() {
				t.Errorf("%s (lectura %d): sello de poda %v dentro del plazo", name, i, revs[1].LiteralPrunedAt)
			}
		}
	}
	persisted := store.PersistedRevisions("i-1")
	if len(persisted) != 2 || !equalJSON(persisted[1].Payload, []byte(noLiteral)) {
		t.Errorf("lo guardado = %+v, quería el payload SIN literal", persisted)
	}
	if strings.Contains(string(persisted[1].Payload), "Marta") {
		t.Errorf("lo guardado lleva el literal del cliente: %s", persisted[1].Payload)
	}
	if entries := log.snapshot(); len(entries) != 0 {
		t.Errorf("dentro del plazo se anunció algo: %v", entries)
	}
}

// TestMemoryStore_Revisions_PrunesTheLiteralPastTheTTL: vencido el plazo, la lectura que poda
// destruye el literal, deja intacta la interpretación, publica YA el sello con el instante del
// reloj y lo anuncia UNA vez, sin contenido. La hermana sin literal ni se sella ni se anuncia.
func TestMemoryStore_Revisions_PrunesTheLiteralPastTheTTL(t *testing.T) {
	store, clock, log := retentionStore(t)
	written := clock.Now()
	clock.Advance(intakes.DefaultLiteralTTL + time.Hour)
	prunedAt := clock.Now()

	revs := mustGet(t, store, tenant1, "i-1").Revisions
	if len(revs) != 2 {
		t.Fatalf("el detalle trae %d revisiones, quería 2", len(revs))
	}
	if !equalJSON(revs[1].Payload, []byte(noLiteral)) {
		t.Errorf("payload tras la poda = %s, quería la interpretación sin literal: %s", revs[1].Payload, noLiteral)
	}
	if !revs[1].LiteralPrunedAt.Equal(prunedAt) {
		t.Errorf("la lectura que poda publica el sello %v, quería el instante del reloj, %v", revs[1].LiteralPrunedAt, prunedAt)
	}
	if !revs[0].LiteralPrunedAt.IsZero() {
		t.Errorf("la revisión sin literal salió sellada: %v", revs[0].LiteralPrunedAt)
	}

	entries := log.snapshot()
	if len(entries) != 1 {
		t.Fatalf("la poda se anunció %d veces, quería 1: %v", len(entries), entries)
	}
	e := entries[0]
	if e.level != "info" || e.msg != "retención: literal de la revisión podado por TTL vencido" {
		t.Errorf("anuncio de la poda = %s", e)
	}
	wantAge := int64(prunedAt.Sub(written).Seconds())
	for key, want := range map[string]any{
		"intake_id": "i-1", "revision_no": 2, "edad_segundos": wantAge,
		"ttl_segundos": int64(intakes.DefaultLiteralTTL.Seconds()),
	} {
		if got, ok := e.value(key); !ok || got != want {
			t.Errorf("anuncio de la poda: %s = %v (%T), quería %v (%T)", key, got, got, want, want)
		}
	}
	if len(e.args) != 8 {
		t.Errorf("el anuncio lleva %d argumentos, quería las cuatro claves y nada más: %v", len(e.args), e.args)
	}
	if s := e.String(); strings.Contains(s, "Marta") || strings.Contains(s, "dos panes") {
		t.Errorf("el anuncio de la poda lleva contenido del cliente: %s", s)
	}

	// Releer no repite el anuncio ni mueve la fecha: es «cuándo se destruyó», no «cuándo se miró».
	clock.Advance(72 * time.Hour)
	again := store.Revisions("i-1")
	if !again[1].LiteralPrunedAt.Equal(prunedAt) || !equalJSON(again[1].Payload, []byte(noLiteral)) {
		t.Errorf("la segunda lectura dejó el sello en %v y el payload en %s", again[1].LiteralPrunedAt, again[1].Payload)
	}
	if n := len(log.snapshot()); n != 1 {
		t.Errorf("tras releer hay %d anuncios, quería seguir en 1", n)
	}
}

// TestMemoryStore_SetLiteralTTL_ZeroNeverPrunes_ShorterPrunesSooner: 0 es RETENCIÓN INDEFINIDA; un
// TTL más corto poda antes; y se sella UNA revisión, no la solicitud.
func TestMemoryStore_SetLiteralTTL_ZeroNeverPrunes_ShorterPrunesSooner(t *testing.T) {
	store, clock, log := retentionStore(t)
	store.SetLiteralTTL(0)
	clock.Advance(10 * intakes.DefaultLiteralTTL)
	if revs := store.Revisions("i-1"); !equalJSON(revs[1].Payload, []byte(withLiteral)) || !revs[1].LiteralPrunedAt.IsZero() {
		t.Errorf("con TTL 0 se podó: payload %s, sello %v", revs[1].Payload, revs[1].LiteralPrunedAt)
	}
	if n := len(log.snapshot()); n != 0 {
		t.Errorf("con TTL 0 se anunciaron %d podas", n)
	}

	// Con un TTL de una hora, la revisión de hace años vence en la siguiente lectura; una hermana
	// con literal escrita ahora mismo, no.
	store.SetLiteralTTL(time.Hour)
	if revs := store.Revisions("i-1"); revs[1].LiteralPrunedAt.IsZero() {
		t.Fatalf("con un TTL de una hora, la revisión de hace años no se podó")
	}
	fresh, err := store.InsertRevision(ctx, intakes.Revision{IntakeID: "i-1", Kind: intakes.RevisionKindInterpreted, Payload: []byte(withLiteral)})
	if err != nil {
		t.Fatalf("InsertRevision: error inesperado %v", err)
	}
	revs := store.Revisions("i-1")
	if got := revs[fresh.RevisionNo-1]; !got.LiteralPrunedAt.IsZero() || !equalJSON(got.Payload, []byte(withLiteral)) {
		t.Errorf("la revisión recién escrita salió podada: la poda es por revisión, no por solicitud (%+v)", got)
	}
	if n := len(log.snapshot()); n != 1 {
		t.Errorf("hay %d anuncios de poda, quería 1 (solo la revisión vencida)", n)
	}
}

// TestMemoryStore_PersistedRevisions_IsRawAndHasNoEffects: enseña lo guardado sin podar, sin
// fundir y sin sellar, aunque el plazo haya vencido; y devuelve una copia.
func TestMemoryStore_PersistedRevisions_IsRawAndHasNoEffects(t *testing.T) {
	store, clock, log := retentionStore(t)
	clock.Advance(intakes.DefaultLiteralTTL + time.Hour)
	persisted := store.PersistedRevisions("i-1")
	if len(persisted) != 2 || !persisted[1].LiteralPrunedAt.IsZero() || !equalJSON(persisted[1].Payload, []byte(noLiteral)) {
		t.Errorf("lo guardado = %+v, quería dos revisiones sin sello y sin literal", persisted)
	}
	if n := len(log.snapshot()); n != 0 {
		t.Errorf("mirar lo guardado anunció %d podas", n)
	}
	persisted[1].Kind = "pisada"
	if again := store.PersistedRevisions("i-1"); again[1].Kind != intakes.RevisionKindInterpreted {
		t.Errorf("pisar la copia cambió lo guardado: kind = %q", again[1].Kind)
	}
	if got := store.PersistedRevisions("i-desconocida"); len(got) != 0 {
		t.Errorf("una solicitud sin revisiones da %+v, quería nada", got)
	}
}

// TestMemoryStore_Reads_ReturnTheCallersCopies: lo que devuelven las lecturas es del llamante;
// pisarlo no cambia lo guardado.
func TestMemoryStore_Reads_ReturnTheCallersCopies(t *testing.T) {
	store, _, _ := retentionStore(t)
	store.Add(tenant1, seeded("i-2", intakes.StatusOpen, 2), twoLines()...)

	got := mustGet(t, store, tenant1, "i-2")
	got.Items[0].Label = "pisada"
	details, err := store.ListDetails(ctx, tenant1, intakes.Filter{}, 10)
	if err != nil || len(details) != 2 {
		t.Fatalf("ListDetails = (%d, %v), quería 2 solicitudes", len(details), err)
	}
	details[0].Items[1].Label = "pisada"
	revs := store.Revisions("i-1")
	revs[0].Kind = "pisada"
	// El payload es un slice: pisar los BYTES de lo leído, de lo guardado que enseña el mirador o
	// de lo que devolvió la escritura tampoco puede pisar lo guardado.
	revs[0].Payload[0] = 'X'
	store.PersistedRevisions("i-1")[0].Payload[0] = 'X'
	written, err := store.InsertRevision(ctx, intakes.Revision{IntakeID: "i-1", Kind: intakes.RevisionKindCart, Payload: []byte(`{"version":1,"total":9}`)})
	if err != nil {
		t.Fatalf("InsertRevision: error inesperado %v", err)
	}
	written.Payload[0] = 'X'
	if again := store.PersistedRevisions("i-1"); !equalJSON(again[0].Payload, []byte(`{"version":1,"total":4}`)) ||
		!equalJSON(again[2].Payload, []byte(`{"version":1,"total":9}`)) {
		t.Errorf("pisar los bytes de un payload devuelto cambió lo guardado: %s y %s", again[0].Payload, again[2].Payload)
	}
	if again := mustGet(t, store, tenant1, "i-2"); again.Items[0].Label != "Pan" || again.Items[1].Label != "Queso" {
		t.Errorf("pisar lo leído cambió las líneas guardadas: %+v", again.Items)
	}
	if again := store.Revisions("i-1"); again[0].Kind != intakes.RevisionKindCart {
		t.Errorf("pisar lo leído cambió la revisión guardada: kind = %q", again[0].Kind)
	}
}

// TestMemoryStore_List_ListDetails_EmptyShapes: una página más allá del final y una solicitud sin
// líneas son slices vacíos NO nil; y Get de otro tenant es ErrNotFound.
func TestMemoryStore_List_ListDetails_EmptyShapes(t *testing.T) {
	store, _ := newMemoryStore()
	store.Add(tenant1, seeded("i-1", intakes.StatusOpen, 1))
	page, total, err := store.List(ctx, tenant1, intakes.Filter{Page: 5, PageSize: 10})
	if err != nil || total != 1 || page == nil || len(page) != 0 {
		t.Errorf("página más allá del final = (%#v, %d, %v), quería vacío no nil y total 1", page, total, err)
	}
	details, err := store.ListDetails(ctx, tenant1, intakes.Filter{}, 10)
	if err != nil || len(details) != 1 || details[0].Items == nil || len(details[0].Items) != 0 {
		t.Errorf("ListDetails de una solicitud sin líneas = (%#v, %v), quería Items vacío no nil", details, err)
	}
	if none, err := store.ListDetails(ctx, tenant1, intakes.Filter{}, 0); err != nil || none == nil || len(none) != 0 {
		t.Errorf("ListDetails con límite 0 = (%#v, %v), quería vacío no nil", none, err)
	}
	if stored := store.StoredStatus(tenant2, "i-1"); stored != "" {
		t.Errorf("StoredStatus desde otro tenant = %q, quería vacío", stored)
	}
}

// TestMemoryStore_ApprovedRenderedTexts_NonPositiveLimitIsNil: pedir cero ejemplos es válido y
// devuelve nil, sin error; sin candidatas, un slice vacío.
func TestMemoryStore_ApprovedRenderedTexts_NonPositiveLimitIsNil(t *testing.T) {
	store, _ := newMemoryStore()
	store.Add(tenant1, seeded("i-1", intakes.StatusConfirmed, 1))
	for _, limit := range []int{0, -3} {
		if got, err := store.ApprovedRenderedTexts(ctx, tenant1, limit); got != nil || err != nil {
			t.Errorf("ApprovedRenderedTexts(%d) = (%#v, %v), quería nil y nil", limit, got, err)
		}
	}
	if got, err := store.ApprovedRenderedTexts(ctx, tenant1, intakes.MaxApprovedTexts); err != nil || got == nil || len(got) != 0 {
		t.Errorf("sin candidatas = (%#v, %v), quería un slice vacío", got, err)
	}
}

// TestMemoryStore_BuyerDataOf_ReturnsACopy: el mirador devuelve una copia (vacía, no nil, si no
// hay nada), y NotifySettings de un tenant sin sembrar es la configuración de arranque.
func TestMemoryStore_BuyerDataOf_ReturnsACopy(t *testing.T) {
	store, _ := newMemoryStore()
	if got := store.BuyerDataOf("i-1"); got == nil || len(got) != 0 {
		t.Errorf("BuyerDataOf sin datos = %#v, quería un mapa vacío no nil", got)
	}
	if err := store.PutBuyerField(ctx, "i-1", "rut", "11.111.111-1"); err != nil {
		t.Fatalf("PutBuyerField: error inesperado %v", err)
	}
	got := store.BuyerDataOf("i-1")
	got["rut"] = "pisado"
	got["extra"] = "x"
	if again := store.BuyerDataOf("i-1"); len(again) != 1 || again["rut"] != "11.111.111-1" {
		t.Errorf("pisar la copia cambió lo guardado: %v", again)
	}
	if cfg, err := store.NotifySettings(ctx, tenant1); err != nil || cfg != (intakes.NotifySettings{DepositDueDays: intakes.DefaultDepositDueDays}) {
		t.Errorf("NotifySettings sin sembrar = (%+v, %v), quería la configuración de arranque", cfg, err)
	}
}
