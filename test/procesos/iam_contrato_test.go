//go:build integracion

package procesos

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	iampostgres "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/infra/postgres"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out/outhelpertest"
)

// Este fichero corre las siete suites de contrato de los puertos persistentes de iam
// (outhelpertest.Contrato) contra los adaptadores de iam/infra/postgres sobre una base clonada de
// la plantilla migrada (T2.33, P4, hallazgo 16 de F2). No es un proceso de caja negra: es la
// excepción de R9.4.d que decidió D-F1-8, con el par de D-F2-9 (la suite cuelga de
// iam/ports/out y los adaptadores viven en iam/infra/postgres): solo importa la suite y, del
// paquete de los adaptadores, sus constructores.
//
// 🔴 NO IMPORTA iam/domain, y dos montajes nombran sus entidades: InvitationTables.Seed recibe y
// devuelve una invitación, y RedeemState lleva una invitación y membresías. Las nombra por los
// alias de la suite (outhelpertest.Invitation y outhelpertest.Membership, decisión de Jhoan del
// 2026-10-04 en la sesión F2-03, junto a D-F2-9).

const (
	// iamTimeout acota cada sentencia de los montajes: la base es local al contenedor.
	iamTimeout = 15 * time.Second
	// iamMultiCompany es el valor de entitlements.FeatureMultiCompany, el único derecho que las
	// suites mueven. Se escribe literal para no importar entitlements (R9.4.d).
	iamMultiCompany = "multi_empresa"
	// iamRequestEmail es el correo de las solicitudes de acceso sembradas: la columna es NOT NULL
	// y el canje no la mira.
	iamRequestEmail = "contrato@wapp.test"
)

// iamCases cuenta los Montajes pedidos en esta corrida: cada caso de cada suite necesita una base
// con nombre propio mientras la anterior siga viva.
var iamCases atomic.Int64

// errIAMResolverDown es el fallo con que BreakResolver deja el resolver caído.
var errIAMResolverDown = errors.New("resolver de entitlements caído (contrato iam)")

// TestIAMContrato_Postgres corre las siete suites de los puertos persistentes de iam —las mismas
// que pasan contra los dobles de iam/infra/memory— contra los adaptadores Postgres. Cada caso
// recibe su base, sus dos tenants (filas reales de public.tenants: las FK los exigen) y su
// resolver de entitlements, todo nuevo.
func TestIAMContrato_Postgres(t *testing.T) {
	outhelpertest.Contrato(t, outhelpertest.Montajes{
		MembershipRepo:       newIAMMembershipMontaje,
		RoleRepo:             newIAMRoleMontaje,
		GrantRepo:            newIAMGrantMontaje,
		AuditRepo:            newIAMAuditMontaje,
		InvitationRepo:       newIAMInvitationMontaje,
		ActiveTenantRepo:     newIAMActiveTenantMontaje,
		InvitationRedeemRepo: newIAMRedeemMontaje,
	})
}

// iamBase es lo común a todos los montajes: la base del caso y sus dos tenants con su nombre.
type iamBase struct {
	db               *sql.DB
	tenantA, tenantB string
	nameA, nameB     string
}

// newIAMBase clona una base para el caso (nuevaBase la borra en el Cleanup del subtest), la abre
// y siembra los dos tenants por SQL.
func newIAMBase(t *testing.T) iamBase {
	t.Helper()
	db := nuevaBase(t, fmt.Sprintf("iam_contrato_%03d", iamCases.Add(1))).Abrir(t)
	b := iamBase{db: db, nameA: "Empresa A del contrato", nameB: "Empresa B del contrato"}
	b.tenantA = iamSeedTenant(t, db, "iam-contrato-a", b.nameA)
	b.tenantB = iamSeedTenant(t, db, "iam-contrato-b", b.nameB)
	return b
}

// iamCtx es el contexto acotado de una sentencia de montaje.
func iamCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), iamTimeout)
	t.Cleanup(cancel)
	return ctx
}

// iamSeedTenant inserta un tenant en public.tenants y devuelve su id. Falla el test si no puede.
func iamSeedTenant(t *testing.T, db *sql.DB, slug, name string) string {
	t.Helper()
	var id string
	if err := db.QueryRowContext(iamCtx(t),
		`INSERT INTO public.tenants (slug, display_name) VALUES ($1, $2) RETURNING id::text`, slug, name,
	).Scan(&id); err != nil {
		t.Fatalf("sembrar el tenant %q: %v", slug, err)
	}
	return id
}

// iamSeedRole crea un rol de tenantID en public.iam_roles y devuelve su id.
func iamSeedRole(t *testing.T, db *sql.DB, tenantID, name string) string {
	t.Helper()
	var id string
	if err := db.QueryRowContext(iamCtx(t),
		`INSERT INTO public.iam_roles (tenant_id, name) VALUES ($1, $2) RETURNING id::text`, tenantID, name,
	).Scan(&id); err != nil {
		t.Fatalf("sembrar el rol %q de %s: %v", name, tenantID, err)
	}
	return id
}

// iamFeatures es el resolver de entitlements de los montajes de alta y su interruptor
// (outhelpertest.FeatureSwitch): un registro en memoria de los tenants con multi_empresa, y no
// filas de public.tenant_features, porque el adaptador solo lo ve como su FeatureResolver (Has).
// Nace sin ningún tenant con multi_empresa y funcionando. Es seguro para concurrencia: la suite
// de membresías lanza altas simultáneas.
type iamFeatures struct {
	mu      sync.Mutex
	granted map[string]bool
	broken  bool
}

var _ outhelpertest.FeatureSwitch = (*iamFeatures)(nil)

func newIAMFeatures() *iamFeatures { return &iamFeatures{granted: map[string]bool{}} }

// Has contesta si tenantID tiene feature: con el resolver caído, error; si no, solo
// multi_empresa y solo en los tenants concedidos.
func (f *iamFeatures) Has(_ context.Context, tenantID, feature string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.broken {
		return false, errIAMResolverDown
	}
	return feature == iamMultiCompany && f.granted[tenantID], nil
}

// GrantMultiCompany implementa outhelpertest.FeatureSwitch.
func (f *iamFeatures) GrantMultiCompany(_ *testing.T, tenantID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.granted[tenantID] = true
}

// BreakResolver implementa outhelpertest.FeatureSwitch.
func (f *iamFeatures) BreakResolver(*testing.T) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.broken = true
}

func newIAMMembershipMontaje(t *testing.T) outhelpertest.MontajeMembershipRepo {
	t.Helper()
	b := newIAMBase(t)
	features := newIAMFeatures()
	return outhelpertest.MontajeMembershipRepo{
		Repo:     iampostgres.NewMembershipRepo(b.db, features),
		TenantA:  b.tenantA,
		TenantB:  b.tenantB,
		NameA:    b.nameA,
		NameB:    b.nameB,
		Features: features,
	}
}

// newIAMRoleMontaje: el rol transversal (plantilla global) lo siembra la migración 0059.
func newIAMRoleMontaje(t *testing.T) outhelpertest.MontajeRoleRepo {
	t.Helper()
	b := newIAMBase(t)
	return outhelpertest.MontajeRoleRepo{Repo: iampostgres.NewRoleRepo(b.db), TenantA: b.tenantA, TenantB: b.tenantB}
}

func newIAMGrantMontaje(t *testing.T) outhelpertest.MontajeGrantRepo {
	t.Helper()
	return outhelpertest.MontajeGrantRepo{Repo: iampostgres.NewGrantRepo(newIAMBase(t).db)}
}

func newIAMAuditMontaje(t *testing.T) outhelpertest.MontajeAuditRepo {
	t.Helper()
	b := newIAMBase(t)
	return outhelpertest.MontajeAuditRepo{Repo: iampostgres.NewAuditRepo(b.db), TenantA: b.tenantA, TenantB: b.tenantB}
}

func newIAMActiveTenantMontaje(t *testing.T) outhelpertest.MontajeActiveTenantRepo {
	t.Helper()
	b := newIAMBase(t)
	return outhelpertest.MontajeActiveTenantRepo{Repo: iampostgres.NewActiveTenantRepo(b.db), TenantA: b.tenantA, TenantB: b.tenantB}
}

func newIAMInvitationMontaje(t *testing.T) outhelpertest.MontajeInvitationRepo {
	t.Helper()
	b := newIAMBase(t)
	return outhelpertest.MontajeInvitationRepo{
		Repo:    iampostgres.NewInvitationRepo(b.db),
		TenantA: b.tenantA,
		TenantB: b.tenantB,
		RoleA:   iamSeedRole(t, b.db, b.tenantA, "operador"),
		Tables:  &iamInvitationTables{db: b.db},
	}
}

func newIAMRedeemMontaje(t *testing.T) outhelpertest.MontajeInvitationRedeemRepo {
	t.Helper()
	b := newIAMBase(t)
	features := newIAMFeatures()
	return outhelpertest.MontajeInvitationRedeemRepo{
		Repo:     iampostgres.NewInvitationRedeemRepo(b.db, features),
		TenantA:  b.tenantA,
		TenantB:  b.tenantB,
		RoleA:    iamSeedRole(t, b.db, b.tenantA, "operador"),
		Features: features,
		Tables:   &iamRedeemTables{db: b.db},
	}
}

// iamInvitationCols es la proyección de public.tenant_invitations en el orden de los campos de
// outhelpertest.Invitation.
const iamInvitationCols = `id::text, tenant_id::text, token_hash, role_id::text, expires_at,
	created_by::text, redeemed_by::text, redeemed_at, revoked_at, created_at`

// scanIAMInvitation lee una fila de iamInvitationCols.
func scanIAMInvitation(row interface{ Scan(...any) error }) (outhelpertest.Invitation, error) {
	var (
		f                     outhelpertest.Invitation
		roleID, redeemedBy    sql.NullString
		redeemedAt, revokedAt sql.NullTime
	)
	err := row.Scan(&f.ID, &f.TenantID, &f.TokenHash, &roleID, &f.ExpiresAt,
		&f.CreatedBy, &redeemedBy, &redeemedAt, &revokedAt, &f.CreatedAt)
	if err != nil {
		return outhelpertest.Invitation{}, err
	}
	if roleID.Valid {
		f.RoleID = &roleID.String
	}
	if redeemedBy.Valid {
		f.RedeemedBy = &redeemedBy.String
	}
	if redeemedAt.Valid {
		f.RedeemedAt = &redeemedAt.Time
	}
	if revokedAt.Valid {
		f.RevokedAt = &revokedAt.Time
	}
	return f, nil
}

// iamInvitationTables es outhelpertest.InvitationTables sobre SQL directo.
type iamInvitationTables struct {
	db *sql.DB
}

// Seed inserta la invitación TAL CUAL; id y created_at vacíos los pone la base.
func (it *iamInvitationTables) Seed(t *testing.T, inv outhelpertest.Invitation) outhelpertest.Invitation {
	t.Helper()
	f := inv
	var id, createdAt any
	if f.ID != "" {
		id = f.ID
	}
	if !f.CreatedAt.IsZero() {
		createdAt = f.CreatedAt
	}
	row := it.db.QueryRowContext(iamCtx(t), `
		INSERT INTO public.tenant_invitations
			(id, tenant_id, token_hash, role_id, expires_at, created_by, redeemed_by, redeemed_at, revoked_at, created_at)
		VALUES (COALESCE($1::uuid, gen_random_uuid()), $2, $3, $4, $5, $6, $7, $8, $9, COALESCE($10::timestamptz, now()))
		RETURNING `+iamInvitationCols,
		id, f.TenantID, f.TokenHash, f.RoleID, f.ExpiresAt, f.CreatedBy, f.RedeemedBy, f.RedeemedAt, f.RevokedAt, createdAt)
	written, err := scanIAMInvitation(row)
	if err != nil {
		t.Fatalf("InvitationTables.Seed: %v", err)
	}
	return written
}

// DeleteRole borra el rol; la FK de tenant_invitations.role_id (ON DELETE SET NULL) hace el resto.
func (it *iamInvitationTables) DeleteRole(t *testing.T, roleID string) {
	t.Helper()
	if _, err := it.db.ExecContext(iamCtx(t), `DELETE FROM public.iam_roles WHERE id = $1`, roleID); err != nil {
		t.Fatalf("InvitationTables.DeleteRole(%s): %v", roleID, err)
	}
}

// iamRedeemTables es outhelpertest.RedeemTables sobre SQL directo en las cuatro tablas del canje.
type iamRedeemTables struct {
	db *sql.DB
}

var _ outhelpertest.RedeemTables = (*iamRedeemTables)(nil)

// SeedInvitation siembra la invitación con expires_at relativo al now() de la base, el mismo
// reloj que luego la juzga; canjeada y revocada, en ese mismo instante.
func (rt *iamRedeemTables) SeedInvitation(t *testing.T, seed outhelpertest.InvitationSeed) {
	t.Helper()
	var redeemedBy any
	if seed.RedeemedBy != "" {
		redeemedBy = seed.RedeemedBy
	}
	if _, err := rt.db.ExecContext(iamCtx(t), `
		INSERT INTO public.tenant_invitations
			(tenant_id, token_hash, role_id, expires_at, created_by, redeemed_by, redeemed_at, revoked_at)
		VALUES ($1, $2, $3, now() + ($4::bigint * interval '1 microsecond'), gen_random_uuid(),
			$5::uuid, CASE WHEN $5::uuid IS NULL THEN NULL ELSE now() END, CASE WHEN $6 THEN now() END)`,
		seed.TenantID, seed.TokenHash, seed.RoleID, seed.ExpiresIn.Microseconds(), redeemedBy, seed.Revoked,
	); err != nil {
		t.Fatalf("RedeemTables.SeedInvitation: %v", err)
	}
}

// SeedMembership escribe la membresía por SQL, SIN pasar por la guarda.
func (rt *iamRedeemTables) SeedMembership(t *testing.T, userID, tenantID string) {
	t.Helper()
	if _, err := rt.db.ExecContext(iamCtx(t), `
		INSERT INTO public.tenant_members (user_id, tenant_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		userID, tenantID); err != nil {
		t.Fatalf("RedeemTables.SeedMembership: %v", err)
	}
}

// SeedPendingAccessRequest deja la solicitud 'pending' que deja el signup público.
func (rt *iamRedeemTables) SeedPendingAccessRequest(t *testing.T, userID string) {
	t.Helper()
	if _, err := rt.db.ExecContext(iamCtx(t), `
		INSERT INTO public.access_requests (user_id, email, origin) VALUES ($1, $2, 'bff')`,
		userID, iamRequestEmail); err != nil {
		t.Fatalf("RedeemTables.SeedPendingAccessRequest: %v", err)
	}
}

// Snapshot fotografía la invitación del digest, entera, y las membresías, asignaciones de rol y
// solicitud de acceso de userID.
func (rt *iamRedeemTables) Snapshot(t *testing.T, tokenHash []byte, userID string) outhelpertest.RedeemState {
	t.Helper()
	var state outhelpertest.RedeemState

	inv, err := scanIAMInvitation(rt.db.QueryRowContext(iamCtx(t),
		`SELECT `+iamInvitationCols+` FROM public.tenant_invitations WHERE token_hash = $1`, tokenHash))
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		t.Fatalf("Snapshot: leer la invitación: %v", err)
	default:
		state.Invitation = &inv
	}

	rt.snapshotMemberships(t, userID, &state)
	rt.snapshotRoles(t, userID, &state)
	rt.snapshotAccessRequest(t, userID, &state)
	return state
}

func (rt *iamRedeemTables) snapshotMemberships(t *testing.T, userID string, state *outhelpertest.RedeemState) {
	t.Helper()
	rows, err := rt.db.QueryContext(iamCtx(t), `
		SELECT user_id::text, tenant_id::text, created_at FROM public.tenant_members
		WHERE user_id = $1 ORDER BY created_at, tenant_id`, userID)
	if err != nil {
		t.Fatalf("Snapshot: leer membresías: %v", err)
	}
	defer closeIAMRows(t, rows)
	for rows.Next() {
		var m outhelpertest.Membership
		if err := rows.Scan(&m.UserID, &m.TenantID, &m.CreatedAt); err != nil {
			t.Fatalf("Snapshot: escanear membresía: %v", err)
		}
		state.Memberships = append(state.Memberships, m)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("Snapshot: iterar membresías: %v", err)
	}
}

func (rt *iamRedeemTables) snapshotRoles(t *testing.T, userID string, state *outhelpertest.RedeemState) {
	t.Helper()
	rows, err := rt.db.QueryContext(iamCtx(t), `
		SELECT role_id::text, COALESCE(tenant_id::text, '') FROM public.iam_user_roles WHERE user_id = $1`, userID)
	if err != nil {
		t.Fatalf("Snapshot: leer asignaciones: %v", err)
	}
	defer closeIAMRows(t, rows)
	for rows.Next() {
		var a outhelpertest.RoleAssignment
		if err := rows.Scan(&a.RoleID, &a.TenantID); err != nil {
			t.Fatalf("Snapshot: escanear asignación: %v", err)
		}
		state.Roles = append(state.Roles, a)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("Snapshot: iterar asignaciones: %v", err)
	}
}

func (rt *iamRedeemTables) snapshotAccessRequest(t *testing.T, userID string, state *outhelpertest.RedeemState) {
	t.Helper()
	rows, err := rt.db.QueryContext(iamCtx(t), `
		SELECT status, decided_at, decided_by::text FROM public.access_requests WHERE user_id = $1`, userID)
	if err != nil {
		t.Fatalf("Snapshot: leer la solicitud: %v", err)
	}
	defer closeIAMRows(t, rows)
	for rows.Next() {
		if state.AccessRequest != nil {
			t.Fatalf("Snapshot: %s tiene más de una solicitud de acceso; la suite siembra una como mucho", userID)
		}
		var (
			r         outhelpertest.AccessRequestRow
			decidedAt sql.NullTime
			decidedBy sql.NullString
		)
		if err := rows.Scan(&r.Status, &decidedAt, &decidedBy); err != nil {
			t.Fatalf("Snapshot: escanear la solicitud: %v", err)
		}
		if decidedAt.Valid {
			r.DecidedAt = &decidedAt.Time
		}
		if decidedBy.Valid {
			r.DecidedBy = &decidedBy.String
		}
		state.AccessRequest = &r
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("Snapshot: iterar la solicitud: %v", err)
	}
}

// closeIAMRows cierra rows y falla el test si no puede.
func closeIAMRows(t *testing.T, rows *sql.Rows) {
	t.Helper()
	if err := rows.Close(); err != nil {
		t.Errorf("cerrar rows: %v", err)
	}
}
