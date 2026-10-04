// Porta internal/iam/infra/postgres/memberships.go @ 9a77307

package iampostgres

import (
	"context"
	"database/sql"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// MembershipRepo implementa out.MembershipRepo sobre public.tenant_members (migración 0037). La
// tabla no lleva FK hacia el usuario: su identidad vive en identity, en otra base de datos.
type MembershipRepo struct{}

// NewMembershipRepo construye el repositorio sobre el pool dado. No toca el pool.
//
// `features` es OBLIGATORIO en la firma, y nil es un valor válido que NO desactiva el gate: lo
// deja contestando que no (fail-closed). Add da de alta, y desde el Plan 047 · Ola 5 · T5.2 el
// desenlace de un alta depende del entitlement multi_empresa del tenant: un constructor que lo
// dejara opcional convertiría «se me olvidó cablearlo» en «esta empresa no paga la
// multi-empresa». Los sitios que solo LEEN pueden pasar nil.
func NewMembershipRepo(db *sql.DB, features FeatureResolver) *MembershipRepo {
	panic(pendiente.Implementar("iampostgres.NewMembershipRepo"))
}

var _ out.MembershipRepo = (*MembershipRepo)(nil)

// TenantsOfUser implementa out.MembershipRepo: los tenants de userID en orden (created_at,
// tenant_id), estable entre llamadas. 🔴 EL ORDEN NO ELIGE NADA: con varias membresías y sin
// empresa activa el canje no toma «la primera». Fallo de la base → (nil, "iam: leer membresías:
// …").
func (r *MembershipRepo) TenantsOfUser(ctx context.Context, userID string) ([]string, error) {
	panic(pendiente.Implementar("iampostgres.MembershipRepo.TenantsOfUser"))
}

// UserTenants implementa out.MembershipRepo: las empresas de userID CON SU NOMBRE, en el MISMO
// orden que TenantsOfUser. Una empresa de la que no es miembro no puede aparecer (anti-oráculo:
// la consulta arranca en sus membresías). No filtra las empresas revocadas: el canje tampoco lo
// hace. Sin empresas, lista vacía NO nil (se serializa `[]`). Fallo de la base → (nil, "iam:
// listar las empresas del usuario: …").
func (r *MembershipRepo) UserTenants(ctx context.Context, userID string) ([]domain.UserTenant, error) {
	panic(pendiente.Implementar("iampostgres.MembershipRepo.UserTenants"))
}

// MembersOf implementa out.MembershipRepo (R-P4): los miembros de tenantID y solo de él, en orden
// (created_at, user_id) con desempate estable; las tres columnas de la tabla, CERO PII. Sin
// miembros, lista vacía no nil. Fallo de la base → (nil, "iam: listar miembros del tenant: …").
func (r *MembershipRepo) MembersOf(ctx context.Context, tenantID string) ([]domain.Membership, error) {
	panic(pendiente.Implementar("iampostgres.MembershipRepo.MembersOf"))
}

// Executor es el mínimo común de *sql.DB y *sql.Tx. Existe para que el alta de acceso a una
// empresa sea LITERALMENTE el mismo código en sus vías, aunque una escriba dentro de una
// transacción ajena —la del operador, que necesita que su UPDATE de access_requests sea atómico
// con esto— y las otras abran la suya.
type Executor interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// FeatureResolver es lo mínimo que el alta de acceso necesita del resolver de entitlements
// (interfaz local, ISP): una sola pregunta, «¿tiene este tenant este derecho?». La satisfacen el
// resolver Postgres de entitlements y el doble de entitlementshelpertest.
type FeatureResolver interface {
	Has(ctx context.Context, tenantID, feature string) (bool, error)
}

// GrantTenantAccess da acceso a tenantID a userID (R-P1): es el caso de uso compartido de las
// vías de alta (la bandeja del operador, el alta del administrador y el canje) y el ÚNICO sitio
// del código que inserta en public.tenant_members (candado single_membership_writer). En este
// orden, dentro de la transacción que trae exec —esta función no la abre ni la cierra nunca—:
//
//  1. Guarda de ámbito del rol (T5.6), sin tocar la base: un roleID que no sea el transversal
//     con tenantID "" → error que envuelve domain.ErrRoleScopeInvalid, y nada se escribe.
//  2. El cerrojo de la persona, pg_advisory_xact_lock sobre userID (R-P2): la PRIMERA operación
//     sobre exec. Si falla → "iam: tomar el cerrojo del alta de membresía: …" y no se cuenta
//     ni se escribe nada. Cierra la ventana TOCTOU de T5.2 y DEPENDE de que exec sea una
//     transacción: con un *sql.DB en autocommit el cerrojo se suelta en el acto.
//  3. La guarda de «una empresa por usuario»: cuenta las membresías de userID en OTROS tenants.
//     Si falla → "iam: contar membresías en otros tenants: …" y nada se escribe. Si hay alguna
//     y el tenant que RECIBE al miembro no tiene multi_empresa → error que envuelve
//     domain.ErrConflict con el MISMO cuerpo de siempre, «el usuario ya es miembro de otra
//     empresa» (R-P3), ANTES de escribir la membresía y el rol. Un resolver nil o que falla se
//     trata como «no la tiene»: fail-closed invertido, se MANTIENE el rechazo (T-11).
//  4. La membresía, idempotente (ON CONFLICT DO NOTHING). Fallo → "iam: alta de membresía: …".
//  5. Si roleID no es nil, ese rol acotado a tenantID, idempotente; con roleID nil
//     iam_user_roles no se toca. Fallo → "iam: asignar rol en el alta de acceso: …".
//
// 🔴 LO QUE multi_empresa NO GATEA: las rutas del plano de membresías. Gobierna el DESENLACE de un
// alta concreta, no el ACCESO a la puerta (entitlements.FeatureMultiCompany).
func GrantTenantAccess(ctx context.Context, exec Executor, features FeatureResolver, userID, tenantID string, roleID *string) error {
	panic(pendiente.Implementar("iampostgres.GrantTenantAccess"))
}

// Add implementa out.MembershipRepo sobre GrantTenantAccess, con roleID nil (darlo de alta y
// darle un rol son dos decisiones distintas) y con SU PROPIA transacción, que confirma solo si el
// alta pasa: la guarda y la escritura son atómicas entre sí, y el cerrojo vive en ella.
// Idempotente; segunda empresa sin multi_empresa → ErrConflict (R-U24). No abrir la transacción →
// "iam: abrir tx de alta de membresía: …"; no confirmarla → "iam: confirmar alta de membresía: …".
func (r *MembershipRepo) Add(ctx context.Context, userID, tenantID string) error {
	panic(pendiente.Implementar("iampostgres.MembershipRepo.Add"))
}

// Remove implementa out.MembershipRepo: borra la membresía de userID en tenantID y solo esa.
// No-op si no estaba: la baja de algo que ya no está es el estado que se pedía. Fallo de la base
// → "iam: baja de membresía: …".
func (r *MembershipRepo) Remove(ctx context.Context, userID, tenantID string) error {
	panic(pendiente.Implementar("iampostgres.MembershipRepo.Remove"))
}
