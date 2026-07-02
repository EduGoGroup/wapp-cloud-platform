package contact

import (
	"context"
	"errors"
	"sync"
	"testing"
)

const rtTenant = "tenant-1"

// fakeMigrator registra las migraciones de flow_state que dispara la fusión y
// mantiene un mapa "session\x00contactID" -> marca, para verificar el re-clave.
type fakeMigrator struct {
	mu    sync.Mutex
	calls []migration
	// state simula flow_state: contactID -> conjunto de sesiones con estado.
	state map[string]map[string]bool
}

type migration struct{ from, to string }

func newFakeMigrator() *fakeMigrator {
	return &fakeMigrator{state: make(map[string]map[string]bool)}
}

func (f *fakeMigrator) seed(contactID, sessionID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state[contactID] == nil {
		f.state[contactID] = make(map[string]bool)
	}
	f.state[contactID][sessionID] = true
}

func (f *fakeMigrator) MigrateContactID(_ context.Context, _ /*tenantID*/, from, to string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, migration{from: from, to: to})
	// Aplica la MISMA política que el store real: conservar el estado del
	// canónico (to) en caso de colisión de sesión; migrar el resto.
	if to != "" && f.state[to] == nil {
		f.state[to] = make(map[string]bool)
	}
	for sess := range f.state[from] {
		if !f.state[to][sess] {
			f.state[to][sess] = true
		}
	}
	delete(f.state, from)
	return nil
}

func mustRef(t *testing.T, kind, value string) Ref {
	t.Helper()
	ref, err := NewRef(kind, value)
	if err != nil {
		t.Fatalf("NewRef(%s, %q): %v", kind, value, err)
	}
	return ref
}

func TestResolve_NuevaRef_CreaContactID(t *testing.T) {
	r := NewMemoryResolver(nil)
	ctx := context.Background()

	id, err := r.Resolve(ctx, rtTenant, []Ref{mustRef(t, KindPhoneE164, "573001112233")}, "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if id == "" {
		t.Fatal("contact_id vacío para ref nueva")
	}
}

func TestResolve_RefExistente_Reusa(t *testing.T) {
	r := NewMemoryResolver(nil)
	ctx := context.Background()
	ref := mustRef(t, KindPhoneE164, "573001112233")

	id1, err := r.Resolve(ctx, rtTenant, []Ref{ref}, "")
	if err != nil {
		t.Fatalf("Resolve 1: %v", err)
	}
	id2, err := r.Resolve(ctx, rtTenant, []Ref{ref}, "")
	if err != nil {
		t.Fatalf("Resolve 2: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("misma ref debe reusar contact_id: %q vs %q", id1, id2)
	}
}

func TestResolve_DosRefsMismoContacto_MismoID(t *testing.T) {
	r := NewMemoryResolver(nil)
	ctx := context.Background()
	phone := mustRef(t, KindPhoneE164, "573001112233")
	lid := mustRef(t, KindWALID, "88887777")

	// Las dos juntas de una → un solo contact_id.
	id, err := r.Resolve(ctx, rtTenant, []Ref{phone, lid}, "")
	if err != nil {
		t.Fatalf("Resolve juntas: %v", err)
	}
	// Cada ref por separado resuelve al MISMO contact_id.
	if got := mustResolve(t, r, phone); got != id {
		t.Fatalf("phone → %q, quiero %q", got, id)
	}
	if got := mustResolve(t, r, lid); got != id {
		t.Fatalf("lid → %q, quiero %q", got, id)
	}
}

// mustResolve resuelve una sola ref y falla el test ante error (helper de asserts).
func mustResolve(t *testing.T, r *MemoryResolver, ref Ref) string {
	t.Helper()
	id, err := r.Resolve(context.Background(), rtTenant, []Ref{ref}, "")
	if err != nil {
		t.Fatalf("Resolve(%+v): %v", ref, err)
	}
	return id
}

func TestResolve_RefNuevaSeAtaAContactoExistente(t *testing.T) {
	r := NewMemoryResolver(nil)
	ctx := context.Background()
	phone := mustRef(t, KindPhoneE164, "573001112233")
	lid := mustRef(t, KindWALID, "88887777")

	id, err := r.Resolve(ctx, rtTenant, []Ref{phone}, "")
	if err != nil {
		t.Fatalf("Resolve phone: %v", err)
	}
	// Llega el phone (existente) + lid (nueva): reusa el id y ata el lid.
	id2, err := r.Resolve(ctx, rtTenant, []Ref{phone, lid}, "")
	if err != nil {
		t.Fatalf("Resolve phone+lid: %v", err)
	}
	if id2 != id {
		t.Fatalf("ref nueva junto a existente debe reusar el id: %q vs %q", id2, id)
	}
	if got := mustResolve(t, r, lid); got != id {
		t.Fatalf("el lid quedó atado a otro id: %q vs %q", got, id)
	}
}

// TestResolve_FusionReconciliacionTardia cubre el borde fino (§5, §10.D): dos
// contact_id distintos creados en momentos diferentes (uno por número, otro por
// LID) se FUNDEN cuando llega un mensaje con ambas refs. El canónico es el más
// antiguo y el flow_state del huérfano se MIGRA al canónico.
func TestResolve_FusionReconciliacionTardia_MigraFlowState(t *testing.T) {
	mig := newFakeMigrator()
	r := NewMemoryResolver(mig)
	ctx := context.Background()
	phone := mustRef(t, KindPhoneE164, "573001112233")
	lid := mustRef(t, KindWALID, "88887777")

	// 1. Se crea C1 por número (más antiguo → canónico).
	c1, err := r.Resolve(ctx, rtTenant, []Ref{phone}, "")
	if err != nil {
		t.Fatalf("Resolve phone: %v", err)
	}
	// 2. Más tarde se crea C2 por LID (sin número conocido aún).
	c2, err := r.Resolve(ctx, rtTenant, []Ref{lid}, "")
	if err != nil {
		t.Fatalf("Resolve lid: %v", err)
	}
	if c1 == c2 {
		t.Fatal("precondición: C1 y C2 deben ser distintos antes de la fusión")
	}
	// El huérfano C2 tiene flow_state en la sesión "s1".
	mig.seed(c2, "s1")

	// 3. Llega un mensaje con AMBAS refs → fusión.
	canonical, err := r.Resolve(ctx, rtTenant, []Ref{phone, lid}, "")
	if err != nil {
		t.Fatalf("Resolve fusión: %v", err)
	}
	if canonical != c1 {
		t.Fatalf("canónico debe ser el más antiguo (C1=%q), fue %q", c1, canonical)
	}
	// El flow_state del huérfano se migró al canónico.
	if len(mig.calls) != 1 || mig.calls[0] != (migration{from: c2, to: c1}) {
		t.Fatalf("migración esperada {from:C2 to:C1}, hubo %+v", mig.calls)
	}
	if mig.state[c1] == nil || !mig.state[c1]["s1"] {
		t.Fatalf("el flow_state no quedó bajo el canónico: %+v", mig.state)
	}
	if _, ok := mig.state[c2]; ok {
		t.Fatalf("el huérfano C2 no debió conservar flow_state: %+v", mig.state)
	}
	// Ambas refs resuelven ahora al canónico.
	if got := mustResolve(t, r, lid); got != c1 {
		t.Fatalf("lid tras fusión → %q, quiero %q", got, c1)
	}
}

func TestResolve_SinRefs_Error(t *testing.T) {
	r := NewMemoryResolver(nil)
	if _, err := r.Resolve(context.Background(), rtTenant, nil, ""); !errors.Is(err, ErrNoRefs) {
		t.Fatalf("Resolve sin refs debe dar ErrNoRefs, dio: %v", err)
	}
}

func TestDestino_Preferencia(t *testing.T) {
	r := NewMemoryResolver(nil)
	ctx := context.Background()
	phone := mustRef(t, KindPhoneE164, "573001112233")
	lid := mustRef(t, KindWALID, "88887777")

	id, err := r.Resolve(ctx, rtTenant, []Ref{lid, phone}, "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	dst, err := r.Destino(ctx, rtTenant, id)
	if err != nil {
		t.Fatalf("Destino: %v", err)
	}
	// phone_e164 gana a wa_lid (§10.E).
	if dst.Kind != KindPhoneE164 || dst.Value != "573001112233" {
		t.Fatalf("destino = %+v, quiero phone_e164 573001112233", dst)
	}
}

func TestDestino_SoloLID_UsaLID(t *testing.T) {
	r := NewMemoryResolver(nil)
	ctx := context.Background()
	id, err := r.Resolve(ctx, rtTenant, []Ref{mustRef(t, KindWALID, "88887777")}, "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	dst, err := r.Destino(ctx, rtTenant, id)
	if err != nil {
		t.Fatalf("Destino: %v", err)
	}
	if dst.Kind != KindWALID {
		t.Fatalf("destino = %+v, quiero wa_lid", dst)
	}
	to, err := dst.Sendable()
	if err != nil {
		t.Fatalf("Sendable lid: %v", err)
	}
	if to != "88887777@lid" {
		t.Fatalf("sendable lid = %q, quiero 88887777@lid", to)
	}
}

func TestDestino_SoloUsername_SinEnviable(t *testing.T) {
	r := NewMemoryResolver(nil)
	ctx := context.Background()
	// Un contacto con SOLO username (aún no direccionable) → ErrNoDestino.
	id, err := r.Resolve(ctx, rtTenant, []Ref{mustRef(t, KindWAUsername, "juanito")}, "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, err := r.Destino(ctx, rtTenant, id); !errors.Is(err, ErrNoDestino) {
		t.Fatalf("Destino sin enviable debe dar ErrNoDestino, dio: %v", err)
	}
}

func TestDestino_ContactoInexistente_Error(t *testing.T) {
	r := NewMemoryResolver(nil)
	if _, err := r.Destino(context.Background(), rtTenant, "no-existe"); !errors.Is(err, ErrContactNotFound) {
		t.Fatalf("Destino de id inexistente debe dar ErrContactNotFound, dio: %v", err)
	}
}

func TestRefsFrom(t *testing.T) {
	// pn + lid poblados.
	refs := RefsFrom("573001112233", "88887777", "88887777@lid")
	if len(refs) != 2 {
		t.Fatalf("RefsFrom(pn,lid) = %d refs, quiero 2: %+v", len(refs), refs)
	}
	// Solo el JID crudo en formato lid → infiere wa_lid.
	refs = RefsFrom("", "", "88887777@lid")
	if len(refs) != 1 || refs[0].Kind != KindWALID {
		t.Fatalf("RefsFrom(lid JID) = %+v, quiero [wa_lid]", refs)
	}
	// Solo el JID crudo numérico → infiere phone_e164.
	refs = RefsFrom("", "", "573001112233@s.whatsapp.net")
	if len(refs) != 1 || refs[0].Kind != KindPhoneE164 {
		t.Fatalf("RefsFrom(phone JID) = %+v, quiero [phone_e164]", refs)
	}
	// Nada utilizable.
	if got := RefsFrom("", "", ""); len(got) != 0 {
		t.Fatalf("RefsFrom vacío = %+v, quiero []", got)
	}
}
