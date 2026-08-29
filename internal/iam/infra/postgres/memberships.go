package iampostgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
)

// MembershipRepo implementa out.MembershipRepo sobre public.tenant_members
// (migración 0037). La tabla no lleva FK hacia el usuario: su identidad vive en
// identity, en otra base de datos.
type MembershipRepo struct {
	db *sql.DB
}

// NewMembershipRepo construye el repositorio sobre el pool dado.
func NewMembershipRepo(db *sql.DB) *MembershipRepo { return &MembershipRepo{db: db} }

var _ out.MembershipRepo = (*MembershipRepo)(nil)

// TenantsOfUser implementa out.MembershipRepo. Ordena por created_at para que
// dos llamadas devuelvan siempre lo mismo.
//
// 🔴 EL ORDEN NO ELIGE NADA, y desde el Plan 047 · Ola 5 · T5.1 hay que decirlo
// distinto que antes. Hasta entonces la frase era «con varios tenants el exchange
// falla», y ya no falla: resuelve por la EMPRESA ACTIVA (D-047.14). Lo que sigue
// siendo cierto —y es lo que este orden NO puede convertirse en— es que con
// varias membresías y sin empresa activa el canje NO elige la primera: emite un
// token sin empresa. Si alguien lee este ORDER BY como «la preferida es la más
// antigua», habrá construido justo la elección silenciosa que está prohibida.
func (r *MembershipRepo) TenantsOfUser(ctx context.Context, userID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT tenant_id::text
		FROM public.tenant_members
		WHERE user_id = $1
		ORDER BY created_at, tenant_id
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("iam: leer membresías: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			_ = cerr
		}
	}()

	var tenants []string
	for rows.Next() {
		var tenantID string
		if scanErr := rows.Scan(&tenantID); scanErr != nil {
			return nil, fmt.Errorf("iam: leer membresías: %w", scanErr)
		}
		tenants = append(tenants, tenantID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iam: leer membresías: %w", err)
	}
	return tenants, nil
}

// UserTenants implementa out.MembershipRepo: las empresas del usuario CON SU
// NOMBRE, para que el selector no tenga que pintar UUIDs.
//
// 🔴 EL `INNER JOIN` ES LA GARANTÍA ANTI-ORÁCULO, no un detalle de rendimiento.
// La consulta arranca en `tenant_members` filtrando por `user_id` y solo desde
// ahí alcanza `tenants`: una empresa de la que este usuario no sea miembro no
// tiene por dónde entrar en el resultado. No es que se filtre después — es que no
// se llega a leer. Un `LEFT JOIN`, o invertir el orden para arrancar en
// `public.tenants`, rompería esa propiedad sin romper ninguna firma.
//
// El ORDER BY es EL MISMO que el de TenantsOfUser —(created_at, tenant_id)— y
// tiene que seguir siéndolo: los dos métodos describen la misma lista, y que el
// selector la pinte en un orden y el canje razone sobre otro sería confuso el día
// que alguien los compare. El `tm.` delante de las columnas no es adorno: sin él,
// `created_at` es ambiguo (las DOS tablas la tienen) y Postgres lo rechaza.
//
// Aquí NO se filtra por `tenants.revoked_at` (el kill-switch comercial de
// D-055.2), y se dice en vez de callarlo: el canje tampoco lo mira, así que
// filtrar aquí escondería del selector una empresa a la que el Context Token
// SIGUE pudiendo acotarse — el selector mentiría sobre lo que el sistema hace.
// Si algún día una empresa revocada debe desaparecer de la vista, el sitio donde
// empezar es resolveTenant, no este SELECT.
func (r *MembershipRepo) UserTenants(ctx context.Context, userID string) ([]domain.UserTenant, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT t.id::text, t.display_name
		FROM public.tenant_members tm
		INNER JOIN public.tenants t ON t.id = tm.tenant_id
		WHERE tm.user_id = $1
		ORDER BY tm.created_at, tm.tenant_id
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("iam: listar las empresas del usuario: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			_ = cerr
		}
	}()

	// Se inicializa NO NULA a propósito: cero empresas es un estado legítimo
	// (D-056.12) y tiene que serializarse como `[]`, no como `null`. Un `null` en
	// el JSON obligaría a cada cliente a distinguir dos formas del mismo hecho.
	tenants := make([]domain.UserTenant, 0)
	for rows.Next() {
		var t domain.UserTenant
		if scanErr := rows.Scan(&t.ID, &t.DisplayName); scanErr != nil {
			return nil, fmt.Errorf("iam: listar las empresas del usuario: %w", scanErr)
		}
		tenants = append(tenants, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iam: listar las empresas del usuario: %w", err)
	}
	return tenants, nil
}

// MembersOf implementa out.MembershipRepo: los miembros de UN tenant.
//
// Es la consulta que estrena idx_tenant_members_tenant, el índice que la
// migración 0037 dejó creado y sin consumidor «para el acceso de administración
// por tenant» — este es. El ORDER BY es (created_at, user_id) y no solo
// created_at: dos altas del mismo instante (la aprobación del operador escribe
// membresía y rol en la MISMA transacción, así que comparten `now()`) dejarían
// el orden a merced del plan de ejecución, y un listado que cambia de orden
// entre dos recargas es un listado roto para quien lo pagine.
//
// Devuelve las TRES columnas de la tabla y ninguna más: aquí no se sale a
// identity a por el nombre (INV-02). CERO PII.
func (r *MembershipRepo) MembersOf(ctx context.Context, tenantID string) ([]domain.Membership, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT user_id::text, tenant_id::text, created_at
		FROM public.tenant_members
		WHERE tenant_id = $1
		ORDER BY created_at, user_id
	`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("iam: listar miembros del tenant: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			_ = cerr
		}
	}()

	members := make([]domain.Membership, 0)
	for rows.Next() {
		var m domain.Membership
		if scanErr := rows.Scan(&m.UserID, &m.TenantID, &m.CreatedAt); scanErr != nil {
			return nil, fmt.Errorf("iam: listar miembros del tenant: %w", scanErr)
		}
		members = append(members, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iam: listar miembros del tenant: %w", err)
	}
	return members, nil
}

// Executor es el mínimo común de *sql.DB y *sql.Tx. Existe para que el alta de
// acceso a una empresa sea LITERALMENTE el mismo código en sus dos vías, aunque
// una escriba dentro de una transacción ajena —la del operador, que necesita que
// su UPDATE de access_requests sea atómico con esto— y la otra abra la suya.
type Executor interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// GrantTenantAccess da acceso a una empresa: comprueba la guarda de «una sola
// empresa por usuario», escribe la membresía y, si roleID no es nil, le asigna
// ese rol acotado al mismo tenant. Es el ÚNICO sitio del código que inserta en
// public.tenant_members.
//
// 🔴 ES EL CASO DE USO COMPARTIDO QUE PIDE REQ-17, y su forma sale de mirar qué
// hacía de verdad la aprobación del operador (platformadmin.executeApprovalTx):
// cuatro pasos dentro de una tx, de los cuales SOLO LOS TRES PRIMEROS son «dar
// acceso» —la guarda, la membresía y el rol—. El cuarto, marcar la solicitud
// como 'approved', es del flujo de esa bandeja y se queda allí.
//
// Recibe el Executor en vez de abrir su propia transacción porque el paso que se
// queda fuera tiene que ser atómico con los tres de aquí: si esta función
// commiteara por su cuenta, una aprobación podría dar el acceso y NO marcar la
// solicitud. Quien llama decide la transacción; esta función no la abre ni la
// cierra nunca.
//
// La guarda limita a UNA la empresa de cada persona y devuelve
// domain.ErrConflict cuando ya hay otra.
//
// 🔧 SU JUSTIFICACIÓN CAMBIÓ EL 2026-08-29 Y LA GUARDA SE QUEDA (Plan 047 · Ola
// 5 · T5.1, D-047.14). Hasta hoy se defendía diciendo que una segunda membresía
// «le rompe el login», porque el canje fallaba con dos filas. ESO YA NO ES
// CIERTO: el canje resuelve por la empresa ACTIVA y, sin elección válida, emite
// un token sin empresa. Lo que T5.1 abrió es el lado de la LECTURA; el de la
// ESCRITURA —qué significa dar de alta a alguien en una segunda empresa, y quién
// puede— sigue sin decidirse, así que la guarda se mantiene tal cual y su
// levantamiento sigue siendo MD-055.2. Lo que ya no se puede decir es que
// protege un canje que se rompería.
//
// ⚠️ La atomicidad NO es exclusión mutua: bajo READ COMMITTED dos altas
// simultáneas del mismo usuario en dos empresas distintas pueden contar cero las
// dos y escribir las dos. La ventana ya existía en la vía del operador y NO se
// cierra aquí a propósito —hacerlo pide un lock (pg_advisory_xact_lock sobre el
// user_id) o una restricción en la tabla, que es cambio de esquema y de
// comportamiento del camino que hoy corre en campo—. Queda dicho para que quien
// levante MD-055.2 lo encuentre escrito y no lo redescubra en producción.
func GrantTenantAccess(ctx context.Context, exec Executor, userID, tenantID string, roleID *string) error {
	others, err := countOtherMemberships(ctx, exec, userID, tenantID)
	if err != nil {
		return err
	}
	if others > 0 {
		return fmt.Errorf("%w: el usuario ya es miembro de otra empresa", domain.ErrConflict)
	}

	if _, err := exec.ExecContext(ctx, `
		INSERT INTO public.tenant_members (user_id, tenant_id)
		VALUES ($1, $2)
		ON CONFLICT DO NOTHING
	`, userID, tenantID); err != nil {
		return fmt.Errorf("iam: alta de membresía: %w", err)
	}

	if roleID == nil {
		return nil
	}
	// El ON CONFLICT va SIN target a propósito, igual que en
	// RoleRepo.AssignToUser: desde la 0060 iam_user_roles tiene un índice
	// PARCIAL que la inferencia por columnas a secas no cubre, y la forma sin
	// target es la única que vale para los dos índices a la vez.
	if _, err := exec.ExecContext(ctx, `
		INSERT INTO public.iam_user_roles (user_id, role_id, tenant_id)
		VALUES ($1, $2, $3)
		ON CONFLICT DO NOTHING
	`, userID, *roleID, tenantID); err != nil {
		return fmt.Errorf("iam: asignar rol en el alta de acceso: %w", err)
	}
	return nil
}

// countOtherMemberships cuenta las membresías de userID en tenants DISTINTOS de
// tenantID. Es CONTABLE a propósito (M-04): no lee una fila arbitraria de una PK
// compuesta con N filas posibles por usuario —sin ORDER BY eso dejaba pasar a
// alguien con 2+ membresías si la fila leída al azar coincidía con el tenant
// pedido—. Un count basta: no importa CUÁL es la otra empresa, solo que la hay.
func countOtherMemberships(ctx context.Context, q Executor, userID, tenantID string) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `
		SELECT count(*)
		FROM public.tenant_members
		WHERE user_id = $1 AND tenant_id <> $2
	`, userID, tenantID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("iam: contar membresías en otros tenants: %w", err)
	}
	return n, nil
}

// Add implementa out.MembershipRepo sobre el caso de uso compartido, con su
// propia transacción: aquí no hay un cuarto paso que deba ser atómico con el
// alta, pero la guarda y la escritura sí tienen que serlo entre sí.
//
// roleID va nil: por esta vía el alta NO asigna rol. Darlo de alta y darle un
// rol son dos decisiones distintas del administrador, y la segunda tiene su
// propia puerta (in.RoleAdmin.AssignRole).
func (r *MembershipRepo) Add(ctx context.Context, userID, tenantID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("iam: abrir tx de alta de membresía: %w", err)
	}
	defer func() {
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			_ = rerr
		}
	}()

	if err := GrantTenantAccess(ctx, tx, userID, tenantID, nil); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("iam: confirmar alta de membresía: %w", err)
	}
	return nil
}

// Remove implementa out.MembershipRepo. No-op si la membresía no estaba: la baja
// de algo que ya no está es el estado que se pedía.
func (r *MembershipRepo) Remove(ctx context.Context, userID, tenantID string) error {
	if _, err := r.db.ExecContext(ctx, `
		DELETE FROM public.tenant_members
		WHERE user_id = $1 AND tenant_id = $2
	`, userID, tenantID); err != nil {
		return fmt.Errorf("iam: baja de membresía: %w", err)
	}
	return nil
}
