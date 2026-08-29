package memory

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
)

// membresia es una fila de tenant_members en el doble. Guarda el instante de
// alta (columna created_at) y un ORDINAL monótono.
//
// El ordinal no es redundante con el instante: dos altas seguidas pueden caer en
// el mismo `time.Now()` según la resolución del reloj del sistema, y entonces
// ordenar solo por tiempo dejaría el orden a merced del recorrido del mapa —que
// en Go es aleatorio a propósito—. El doble se volvería no determinista justo en
// lo que el puerto promete: orden estable. El adaptador Postgres resuelve el
// mismo empate con el user_id como segundo criterio; aquí basta el ordinal.
type membresia struct {
	tenantID string
	at       time.Time
	orden    uint64
}

// FeatureResolver es lo mínimo que la guarda del alta necesita del resolver de
// derechos comerciales, con la MISMA forma que iampostgres.FeatureResolver: una
// pregunta y nada más. Se declara aquí y no se importa de allí para que el doble
// no dependa del adaptador que dobla.
type FeatureResolver interface {
	Has(ctx context.Context, tenantID, feature string) (bool, error)
}

// MembershipStore implementa out.MembershipRepo en memoria (tabla
// tenant_members). Conserva el orden de alta, como el ORDER BY created_at de la
// implementación Postgres.
type MembershipStore struct {
	mu       sync.RWMutex
	byUserID map[string][]membresia
	// features dobla al resolver de entitlements. Ausente (nil) ⇒ NADIE tiene
	// multi_empresa, que es el mismo extremo fail-closed del adaptador real y el
	// comportamiento que este doble tenía antes de T5.2.
	features FeatureResolver
	// nombres es el trozo de public.tenants que este doble necesita conocer: el
	// display_name por tenant. NO es una tabla de tenants en miniatura — solo
	// existe porque UserTenants hace un JOIN contra ella en el adaptador real, y
	// un doble que devolviera el id como nombre daría por buenos tests que la
	// base no respalda.
	nombres map[string]string
	// siguiente es el ordinal del próximo alta. Empieza en 1 para que el cero
	// valor de membresia nunca se confunda con una fila real.
	siguiente uint64
}

// NewMembershipStore crea el store vacío.
func NewMembershipStore() *MembershipStore {
	return &MembershipStore{
		byUserID:  make(map[string][]membresia),
		nombres:   make(map[string]string),
		siguiente: 1,
	}
}

// ConFeatures ata al doble un resolver de derechos comerciales, para poder
// fabricar el caso de un tenant CON multi_empresa (Plan 047 · Ola 5 · T5.2).
// Devuelve el propio store para poder encadenarlo en la construcción.
//
// Es un ajuste APARTE del constructor y no un parámetro suyo, por la misma razón
// que SeedTenantName: la inmensa mayoría de los tests no tienen nada que decir
// sobre entitlements y no deberían tener que decirlo. Quien no lo llame se queda
// con la guarda cerrada, que es el caso de siempre.
func (s *MembershipStore) ConFeatures(f FeatureResolver) *MembershipStore {
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
	s.nombres[tenantID] = displayName
}

var _ out.MembershipRepo = (*MembershipStore)(nil)

// anotar escribe la membresía si no estaba. Devuelve false si ya existía (el
// alta es idempotente, como el ON CONFLICT DO NOTHING de la tabla real).
// El llamante ya tiene el candado.
func (s *MembershipStore) anotar(userID, tenantID string) bool {
	for _, existente := range s.byUserID[userID] {
		if existente.tenantID == tenantID {
			return false
		}
	}
	s.byUserID[userID] = append(s.byUserID[userID], membresia{
		tenantID: tenantID,
		at:       time.Now(),
		orden:    s.siguiente,
	})
	s.siguiente++
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
	s.anotar(userID, tenantID)
}

// Add implementa out.MembershipRepo con la MISMA guarda que el adaptador
// Postgres (iampostgres.GrantTenantAccess): membresía en otro tenant →
// domain.ErrConflict, SALVO que el tenant de destino tenga el entitlement
// `multi_empresa`. Si el doble no la tuviera, los tests unitarios darían por
// buena una segunda empresa que la base rechaza.
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
func (s *MembershipStore) Add(ctx context.Context, userID, tenantID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existente := range s.byUserID[userID] {
		if existente.tenantID != tenantID && !s.multiEmpresaConcedida(ctx, tenantID) {
			return fmt.Errorf("%w: el usuario ya es miembro de otra empresa", domain.ErrConflict)
		}
	}
	s.anotar(userID, tenantID) // idempotente: false si ya estaba
	return nil
}

// multiEmpresaConcedida dobla a iampostgres.multiEmpresaConcedida, incluida su
// firma: un bool, sin error, porque «no la tiene» y «no se pudo averiguar»
// tienen que acabar en el mismo 409. El llamante ya tiene el candado.
func (s *MembershipStore) multiEmpresaConcedida(ctx context.Context, tenantID string) bool {
	if s.features == nil {
		return false
	}
	concedida, err := s.features.Has(ctx, tenantID, entitlements.FeatureMultiEmpresa)
	return err == nil && concedida
}

// Remove implementa out.MembershipRepo. No-op si no estaba.
func (s *MembershipStore) Remove(_ context.Context, userID, tenantID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := make([]membresia, 0, len(s.byUserID[userID]))
	for _, existente := range s.byUserID[userID] {
		if existente.tenantID != tenantID {
			kept = append(kept, existente)
		}
	}
	s.byUserID[userID] = kept
	return nil
}

// TenantsOfUser implementa out.MembershipRepo.
func (s *MembershipStore) TenantsOfUser(_ context.Context, userID string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	filas := s.byUserID[userID]
	if len(filas) == 0 {
		return nil, nil
	}
	tenants := make([]string, 0, len(filas))
	for _, f := range filas {
		tenants = append(tenants, f.tenantID)
	}
	return tenants, nil
}

// UserTenants implementa out.MembershipRepo: las empresas del usuario CON su
// nombre, en el MISMO orden que TenantsOfUser (el ordinal de alta).
//
// Recorre SOLO las membresías de ese usuario, igual que el INNER JOIN del
// adaptador real: no hay forma de que aparezca aquí una empresa de la que no sea
// miembro, ni siquiera una que exista en `nombres` sin membresía detrás. Esa
// simetría con el SQL es lo que hace que un test unitario verde signifique algo.
func (s *MembershipStore) UserTenants(_ context.Context, userID string) ([]domain.UserTenant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	filas := append([]membresia(nil), s.byUserID[userID]...)
	sort.Slice(filas, func(i, j int) bool { return filas[i].orden < filas[j].orden })

	// No nula: cero empresas se serializa como `[]`, nunca como `null`.
	tenants := make([]domain.UserTenant, 0, len(filas))
	for _, f := range filas {
		tenants = append(tenants, domain.UserTenant{ID: f.tenantID, DisplayName: s.nombres[f.tenantID]})
	}
	return tenants, nil
}

// MembersOf implementa out.MembershipRepo: la lectura INVERSA, los miembros de
// un tenant. Recorre el mapa entero —el doble está indexado por usuario, como la
// PK de la tabla— y ORDENA por el ordinal de alta, que es lo que hace
// determinista el resultado pese al recorrido aleatorio del mapa de Go.
func (s *MembershipStore) MembersOf(_ context.Context, tenantID string) ([]domain.Membership, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	type fila struct {
		m     domain.Membership
		orden uint64
	}
	filas := make([]fila, 0)
	for userID, membresias := range s.byUserID {
		for _, m := range membresias {
			if m.tenantID != tenantID {
				continue
			}
			filas = append(filas, fila{
				m:     domain.Membership{UserID: userID, TenantID: tenantID, CreatedAt: m.at},
				orden: m.orden,
			})
		}
	}
	sort.Slice(filas, func(i, j int) bool { return filas[i].orden < filas[j].orden })

	miembros := make([]domain.Membership, 0, len(filas))
	for _, f := range filas {
		miembros = append(miembros, f.m)
	}
	return miembros, nil
}
