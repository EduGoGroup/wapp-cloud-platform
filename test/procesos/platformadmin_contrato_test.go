//go:build integracion

package procesos

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/platformadmin"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/platformadmin/platformadminhelpertest"
)

// Este fichero corre la suite de contrato de los dos puertos de platformadmin
// (platformadminhelpertest.Contrato: TenantStore y AccessRequestStore) contra su adaptador
// Postgres, platformadmin.Repository, sobre una base clonada de la plantilla migrada (T2.15,
// T2.33, P4, D-F2-3). No es un proceso de caja negra: es la excepción de R9.4.d que decidió
// D-F1-8 —solo importa la suite y, del paquete del puerto, su constructor (NewRepository)—, y
// la misma suite pasa contra el doble en memoria (platformadminhelpertest.Fake).
//
// No nombra ningún tipo ajeno: el FeatureResolver que pide NewRepository se satisface con un
// tipo local (pgNoMultiCompany, un Has), y los roles del Montaje son las plantillas globales que
// siembra la migración 0015 (operator y viewer), leídas aquí por SQL para no fiarse de un id.

const (
	// paTimeout acota cada sentencia de siembra u observación: la base es local al contenedor.
	paTimeout = 15 * time.Second
)

// paCases cuenta los Montajes pedidos en esta corrida: cada caso necesita una base con nombre
// propio mientras la anterior siga viva.
var paCases atomic.Int64

// paSeq numera las empresas sembradas para que sus slugs no choquen dentro de una base.
var paSeq atomic.Int64

// TestPlatformadminContrato_Postgres corre los casos de platformadminhelpertest.Contrato contra
// platformadmin.Repository. Cada caso recibe su base, sus dos empresas (filas reales de
// public.tenants: las FK de tenant_members, fleet_sessions y leases las exigen) y un repositorio
// cuyo resolver de derechos contesta que no a toda feature (el Montaje promete empresas SIN
// multi_empresa).
func TestPlatformadminContrato_Postgres(t *testing.T) {
	platformadminhelpertest.Contrato(t, newPlatformadminMontaje)
}

// pgNoMultiCompany es el FeatureResolver del repositorio: ninguna empresa tiene ningún derecho.
// Es lo que hace que la regla de una sola empresa rechace la segunda.
type pgNoMultiCompany struct{}

// Has implementa el FeatureResolver de NewRepository: siempre «no», sin error.
func (pgNoMultiCompany) Has(context.Context, string, string) (bool, error) { return false, nil }

func newPlatformadminMontaje(t *testing.T) platformadminhelpertest.Montaje {
	t.Helper()
	db := nuevaBase(t, fmt.Sprintf("platformadmin_contrato_%03d", paCases.Add(1))).Abrir(t)
	repo := platformadmin.NewRepository(db, pgNoMultiCompany{})
	st := &paState{db: db}
	return platformadminhelpertest.Montaje{
		Tenants:       repo,
		Requests:      repo,
		TenantA:       st.seedTenant(t, "Empresa A del contrato", nil),
		TenantB:       st.seedTenant(t, "Empresa B del contrato", nil),
		MissingTenant: "7d3c2b1a-0f9e-4d8c-b7a6-958473625140",
		RoleA:         st.globalRole(t, "operator"),
		RoleB:         st.globalRole(t, "viewer"),
		State:         st,
	}
}

// paCtx es el contexto acotado de una sentencia de siembra u observación.
func paCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), paTimeout)
	t.Cleanup(cancel)
	return ctx
}

// paState implementa platformadminhelpertest.State sobre public.tenants, public.fleet_sessions,
// public.leases, public.access_requests, public.tenant_members y public.iam_user_roles de la
// base del caso.
type paState struct {
	db *sql.DB
}

var _ platformadminhelpertest.State = (*paState)(nil)

// seedTenant inserta una empresa con un slug único y, si at no es nil, con ese created_at (y el
// mismo updated_at). Devuelve su id.
func (s *paState) seedTenant(t *testing.T, name string, at *time.Time) string {
	t.Helper()
	slug := fmt.Sprintf("pa-contrato-%05d", paSeq.Add(1))
	var id string
	var err error
	if at == nil {
		err = s.db.QueryRowContext(paCtx(t),
			`INSERT INTO public.tenants (slug, display_name) VALUES ($1, $2) RETURNING id::text`, slug, name,
		).Scan(&id)
	} else {
		err = s.db.QueryRowContext(paCtx(t),
			`INSERT INTO public.tenants (slug, display_name, created_at, updated_at) VALUES ($1, $2, $3, $3) RETURNING id::text`,
			slug, name, *at,
		).Scan(&id)
	}
	if err != nil {
		t.Fatalf("sembrar la empresa %q: %v", slug, err)
	}
	return id
}

// globalRole lee la plantilla global (tenant_id NULL) con ese nombre, sembrada por la migración
// 0015. Falla el test si no está.
func (s *paState) globalRole(t *testing.T, name string) platformadminhelpertest.Role {
	t.Helper()
	var id string
	if err := s.db.QueryRowContext(paCtx(t),
		`SELECT id::text FROM public.iam_roles WHERE name = $1 AND tenant_id IS NULL`, name,
	).Scan(&id); err != nil {
		t.Fatalf("leer el rol global %q (migración 0015): %v", name, err)
	}
	return platformadminhelpertest.Role{ID: id, Name: name}
}

// SeedTenantsCreatedAt implementa platformadminhelpertest.State.
func (s *paState) SeedTenantsCreatedAt(t *testing.T, n int, at time.Time) []string {
	t.Helper()
	ids := make([]string, 0, n)
	for range n {
		ids = append(ids, s.seedTenant(t, "Sembrada", &at))
	}
	return ids
}

// SeedFleetSession implementa platformadminhelpertest.State: repetir la sesión reemplaza su
// última señal.
func (s *paState) SeedFleetSession(t *testing.T, tenantID, edgeID, sessionID string, lastSeenAt *time.Time) {
	t.Helper()
	var seen any
	if lastSeenAt != nil {
		seen = *lastSeenAt
	}
	if _, err := s.db.ExecContext(paCtx(t), `
		INSERT INTO public.fleet_sessions (tenant_id, edge_id, session_id, last_seen_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (tenant_id, edge_id, session_id) DO UPDATE SET last_seen_at = EXCLUDED.last_seen_at
	`, tenantID, edgeID, sessionID, seen); err != nil {
		t.Fatalf("sembrar la sesión %s/%s/%s: %v", tenantID, edgeID, sessionID, err)
	}
}

// SeedLease implementa platformadminhelpertest.State.
func (s *paState) SeedLease(t *testing.T, tenantID, edgeID string, revoked bool) {
	t.Helper()
	if _, err := s.db.ExecContext(paCtx(t), `
		INSERT INTO public.leases (tenant_id, edge_id, expires_at, revoked)
		VALUES ($1, $2, now() + interval '5 minutes', $3)
		ON CONFLICT (tenant_id, edge_id) DO UPDATE SET revoked = EXCLUDED.revoked
	`, tenantID, edgeID, revoked); err != nil {
		t.Fatalf("sembrar el lease %s/%s: %v", tenantID, edgeID, err)
	}
}

// SeedMembership implementa platformadminhelpertest.State: escribe la membresía y los roles en
// esa empresa directamente, sin la regla de una sola empresa (es una siembra).
func (s *paState) SeedMembership(t *testing.T, userID, tenantID string, roleIDs ...string) {
	t.Helper()
	if _, err := s.db.ExecContext(paCtx(t),
		`INSERT INTO public.tenant_members (user_id, tenant_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, userID, tenantID,
	); err != nil {
		t.Fatalf("sembrar la membresía de %s en %s: %v", userID, tenantID, err)
	}
	for _, roleID := range roleIDs {
		if _, err := s.db.ExecContext(paCtx(t),
			`INSERT INTO public.iam_user_roles (user_id, role_id, tenant_id) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
			userID, roleID, tenantID,
		); err != nil {
			t.Fatalf("sembrar el rol %s de %s en %s: %v", roleID, userID, tenantID, err)
		}
	}
}

// SetRequestCreatedAt implementa platformadminhelpertest.State.
func (s *paState) SetRequestCreatedAt(t *testing.T, requestID string, at time.Time) {
	t.Helper()
	res, err := s.db.ExecContext(paCtx(t), `UPDATE public.access_requests SET created_at = $2 WHERE id = $1`, requestID, at)
	if err != nil {
		t.Fatalf("cambiar el created_at de la solicitud %s: %v", requestID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		t.Fatalf("contar las filas de la solicitud %s: %v", requestID, err)
	}
	if n != 1 {
		t.Fatalf("la solicitud %s no existe", requestID)
	}
}

// Request implementa platformadminhelpertest.State: la fila entera de public.access_requests.
func (s *paState) Request(t *testing.T, requestID string) platformadminhelpertest.RequestRow {
	t.Helper()
	var (
		row       platformadminhelpertest.RequestRow
		reason    sql.NullString
		decidedBy sql.NullString
		decidedAt sql.NullTime
	)
	err := s.db.QueryRowContext(paCtx(t), `
		SELECT user_id::text, email, origin, status, reason, decided_by::text, decided_at, created_at
		FROM public.access_requests
		WHERE id = $1
	`, requestID).Scan(&row.UserID, &row.Email, &row.Origin, &row.Status, &reason, &decidedBy, &decidedAt, &row.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("la solicitud %s no existe", requestID)
	}
	if err != nil {
		t.Fatalf("leer la solicitud %s: %v", requestID, err)
	}
	if reason.Valid {
		row.Reason = &reason.String
	}
	if decidedBy.Valid {
		row.DecidedBy = &decidedBy.String
	}
	if decidedAt.Valid {
		row.DecidedAt = &decidedAt.Time
	}
	return row
}

// Access implementa platformadminhelpertest.State: la membresía en public.tenant_members y los
// roles de public.iam_user_roles EN esa empresa, ordenados.
func (s *paState) Access(t *testing.T, userID, tenantID string) (bool, []string) {
	t.Helper()
	var member bool
	if err := s.db.QueryRowContext(paCtx(t),
		`SELECT EXISTS (SELECT 1 FROM public.tenant_members WHERE user_id = $1 AND tenant_id = $2)`, userID, tenantID,
	).Scan(&member); err != nil {
		t.Fatalf("leer la membresía de %s en %s: %v", userID, tenantID, err)
	}
	rows, err := s.db.QueryContext(paCtx(t),
		`SELECT role_id::text FROM public.iam_user_roles WHERE user_id = $1 AND tenant_id = $2`, userID, tenantID)
	if err != nil {
		t.Fatalf("leer los roles de %s en %s: %v", userID, tenantID, err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			t.Errorf("cerrar los roles: %v", cerr)
		}
	}()
	roles := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("escanear un rol: %v", err)
		}
		roles = append(roles, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterar los roles: %v", err)
	}
	sort.Strings(roles)
	return member, roles
}
