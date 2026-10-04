// Porta internal/iam/infra/memory/membership_store.go @ 9a77307

package memory

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
)

// membershipRow es una fila de tenant_members en el doble (era membresia): el
// tenant y el instante de alta (columna created_at).
//
// 🔧 El doble viejo guardaba además un ORDINAL monótono para desempatar dos altas
// del mismo `time.Now()`. Ahora el instante sale de un reloj inyectable y el
// empate se resuelve como en el adaptador Postgres —(created_at, tenant_id) y
// (created_at, user_id)—, que es lo que el puerto promete; el ordinal sobraba.
type membershipRow struct {
	tenantID string
	at       time.Time
}

// FeatureResolver es lo mínimo que la guarda del alta necesita del resolver de
// derechos comerciales, con la MISMA forma que el FeatureResolver de
// infra/postgres: una pregunta y nada más. Se declara aquí y no se importa de
// allí para que el doble no dependa del adaptador que dobla. Lo satisfacen
// entitlements.Resolver y entitlementshelpertest.Fake.
type FeatureResolver interface {
	Has(ctx context.Context, tenantID, feature string) (bool, error)
}

// MembershipStore implementa out.MembershipRepo en memoria (tabla
// tenant_members). Ordena como el ORDER BY de la implementación Postgres y pasa
// outhelpertest.ContratoMembershipRepo, la misma suite.
type MembershipStore struct {
	mu       sync.RWMutex
	byUserID map[string][]membershipRow
	// features dobla al resolver de entitlements. Ausente (nil) ⇒ NADIE tiene
	// multi_empresa, que es el mismo extremo fail-closed del adaptador real y el
	// comportamiento que este doble tenía antes de T5.2.
	features FeatureResolver
	// names es el trozo de public.tenants que este doble necesita conocer: el
	// display_name por tenant (era nombres). NO es una tabla de tenants en
	// miniatura — solo existe porque UserTenants hace un JOIN contra ella en el
	// adaptador real, y un doble que devolviera el id como nombre daría por buenos
	// tests que la base no respalda.
	names map[string]string
	// now es el reloj de created_at. Por defecto time.Now; WithClock lo sustituye.
	now func() time.Time
}

// NewMembershipStore crea el store vacío, sin resolver de derechos (nadie tiene
// multi_empresa) y con el reloj real.
func NewMembershipStore() *MembershipStore {
	return &MembershipStore{
		byUserID: make(map[string][]membershipRow),
		names:    make(map[string]string),
		now:      time.Now,
	}
}

// WithClock sustituye el reloj del store y lo devuelve, para encadenarlo tras el constructor
// (antes de usarlo: no es seguro cambiar el reloj con el store en uso). Un now nil deja el que
// tenía.
func (s *MembershipStore) WithClock(now func() time.Time) *MembershipStore {
	if now != nil {
		s.now = now
	}
	return s
}

// WithFeatures (era ConFeatures) ata al doble un resolver de derechos
// comerciales, para poder fabricar el caso de un tenant CON multi_empresa (Plan
// 047 · Ola 5 · T5.2). Devuelve el propio store para poder encadenarlo en la
// construcción.
//
// Es un ajuste APARTE del constructor y no un parámetro suyo, por la misma razón
// que SeedTenantName: la inmensa mayoría de los tests no tienen nada que decir
// sobre entitlements y no deberían tener que decirlo. Quien no lo llame se queda
// con la guarda cerrada, que es el caso de siempre.
func (s *MembershipStore) WithFeatures(f FeatureResolver) *MembershipStore {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.features = f
	return s
}

// SeedTenantName registra el nombre legible de una empresa (helper de tests).
//
// ⚠️ Es una siembra APARTE de Seed y no un parámetro suyo, y eso es fiel a la
// realidad: el nombre vive en OTRA tabla (public.tenants) que existe antes que
// cualquier membresía. Una empresa sin nombre sembrado devuelve DisplayName
// vacío en vez de inventarse uno — así, un test que compruebe nombres tiene que
// sembrarlos, y no puede pasar por casualidad.
func (s *MembershipStore) SeedTenantName(tenantID, displayName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.names[tenantID] = displayName
}

var _ out.MembershipRepo = (*MembershipStore)(nil)

// insertLocked escribe la membresía si no estaba (era anotar). Devuelve false si
// ya existía (el alta es idempotente, como el ON CONFLICT DO NOTHING de la tabla
// real, y no reescribe created_at). El llamante ya tiene el candado.
func (s *MembershipStore) insertLocked(userID, tenantID string) bool {
	for _, existing := range s.byUserID[userID] {
		if existing.tenantID == tenantID {
			return false
		}
	}
	s.byUserID[userID] = append(s.byUserID[userID], membershipRow{tenantID: tenantID, at: s.now()})
	return true
}

// Seed da de alta una membresía (helper de tests). Repetir la misma pareja no
// la duplica, igual que la PK compuesta de la tabla real.
//
// A diferencia de Add, NO aplica la guarda de una sola empresa por usuario: es
// para FABRICAR el estado de partida, incluido el que la guarda ya no dejaría
// escribir (dos empresas para la misma persona, que es la deuda MD-055.2 y
// existe en bases reales anteriores a la guarda).
func (s *MembershipStore) Seed(userID, tenantID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.insertLocked(userID, tenantID)
}

// Add implementa out.MembershipRepo con la MISMA guarda que el adaptador
// Postgres (GrantTenantAccess): membresía en otro tenant → domain.ErrConflict,
// SALVO que el tenant de destino tenga el entitlement `multi_empresa`. Si el
// doble no la tuviera, los tests unitarios darían por buena una segunda empresa
// que la base rechaza.
//
// 🔓 LA EXCEPCIÓN ES DE T5.2 (Plan 047 · Ola 5) y sin ella el caso PERMISIVO no
// se podría ni escribir en un test unitario: el doble contestaría 409 a un alta
// que la base acepta, y la única forma de probar la mitad buena de la guarda
// sería bajar a Postgres.
//
// 🔴 FAIL-CLOSED CON EL MISMO SENTIDO INVERTIDO QUE EL ORIGINAL: sin resolver, o
// con un resolver que falla, se MANTIENE el rechazo. El doble no puede ser más
// permisivo que lo que dobla; si lo fuera, un test verde aquí sería un 409 en
// campo.
//
// El candado del store hace aquí el papel del pg_advisory_xact_lock de la base:
// dos altas simultáneas de la misma persona no pueden contar cero las dos.
func (s *MembershipStore) Add(ctx context.Context, userID, tenantID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.grantLocked(ctx, userID, tenantID)
}

// grantLocked es la guarda y la escritura del alta, la parte de GrantTenantAccess
// que toca tenant_members: si la persona ya es miembro de OTRO tenant y el de
// destino no tiene multi_empresa, el rechazo de siempre sin escribir nada; si no,
// la membresía (idempotente). La comparten Add y RedeemStore.Redeem, como las dos
// vías comparten GrantTenantAccess en Postgres. El llamante tiene el candado.
func (s *MembershipStore) grantLocked(ctx context.Context, userID, tenantID string) error {
	for _, existing := range s.byUserID[userID] {
		if existing.tenantID != tenantID && !s.multiCompanyGranted(ctx, tenantID) {
			return fmt.Errorf("%w: el usuario ya es miembro de otra empresa", domain.ErrConflict)
		}
	}
	s.insertLocked(userID, tenantID) // idempotente: false si ya estaba
	return nil
}

// multiCompanyGranted (era multiEmpresaConcedida) dobla a la función homónima de
// infra/postgres, incluida su firma: un bool, sin error, porque «no la tiene» y
// «no se pudo averiguar» tienen que acabar en el mismo 409. Pregunta por el
// tenant de DESTINO. El llamante ya tiene el candado.
func (s *MembershipStore) multiCompanyGranted(ctx context.Context, tenantID string) bool {
	if s.features == nil {
		return false
	}
	granted, err := s.features.Has(ctx, tenantID, entitlements.FeatureMultiCompany)
	return err == nil && granted
}

// Remove implementa out.MembershipRepo: baja de esa pareja y solo de esa. No-op
// si no estaba.
func (s *MembershipStore) Remove(_ context.Context, userID, tenantID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := make([]membershipRow, 0, len(s.byUserID[userID]))
	for _, existing := range s.byUserID[userID] {
		if existing.tenantID != tenantID {
			kept = append(kept, existing)
		}
	}
	s.byUserID[userID] = kept
	return nil
}

// rowsOfLocked devuelve una copia de las membresías del usuario en el orden del
// adaptador Postgres: (created_at, tenant_id). El llamante tiene el candado.
func (s *MembershipStore) rowsOfLocked(userID string) []membershipRow {
	rows := slices.Clone(s.byUserID[userID])
	slices.SortFunc(rows, func(a, b membershipRow) int {
		if c := a.at.Compare(b.at); c != 0 {
			return c
		}
		return strings.Compare(a.tenantID, b.tenantID)
	})
	return rows
}

// TenantsOfUser implementa out.MembershipRepo. Sin membresías devuelve nil, como
// el doble viejo y el adaptador Postgres.
func (s *MembershipStore) TenantsOfUser(_ context.Context, userID string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows := s.rowsOfLocked(userID)
	if len(rows) == 0 {
		return nil, nil
	}
	tenants := make([]string, 0, len(rows))
	for _, r := range rows {
		tenants = append(tenants, r.tenantID)
	}
	return tenants, nil
}

// UserTenants implementa out.MembershipRepo: las empresas del usuario CON su
// nombre, en el MISMO orden que TenantsOfUser.
//
// Recorre SOLO las membresías de ese usuario, igual que el INNER JOIN del
// adaptador real: no hay forma de que aparezca aquí una empresa de la que no sea
// miembro, ni siquiera una que exista en `names` sin membresía detrás. Esa
// simetría con el SQL es lo que hace que un test unitario verde signifique algo.
func (s *MembershipStore) UserTenants(_ context.Context, userID string) ([]domain.UserTenant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows := s.rowsOfLocked(userID)
	// No nula: cero empresas se serializa como `[]`, nunca como `null`.
	tenants := make([]domain.UserTenant, 0, len(rows))
	for _, r := range rows {
		tenants = append(tenants, domain.UserTenant{ID: r.tenantID, DisplayName: s.names[r.tenantID]})
	}
	return tenants, nil
}

// MembersOf implementa out.MembershipRepo: la lectura INVERSA, los miembros de
// un tenant. Recorre el mapa entero —el doble está indexado por usuario, como la
// PK de la tabla— y ORDENA por (created_at, user_id), el ORDER BY del adaptador,
// que es lo que hace determinista el resultado pese al recorrido aleatorio del
// mapa de Go. Vacía, no nil.
func (s *MembershipStore) MembersOf(_ context.Context, tenantID string) ([]domain.Membership, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	members := make([]domain.Membership, 0)
	for userID, rows := range s.byUserID {
		for _, r := range rows {
			if r.tenantID == tenantID {
				members = append(members, domain.Membership{UserID: userID, TenantID: tenantID, CreatedAt: r.at})
			}
		}
	}
	slices.SortFunc(members, func(a, b domain.Membership) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.UserID, b.UserID)
	})
	return members, nil
}
