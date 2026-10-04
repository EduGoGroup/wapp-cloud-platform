// Porta internal/iam/infra/postgres/memberships.go @ 9a77307

package iampostgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
)

// MembershipRepo implementa out.MembershipRepo sobre public.tenant_members (migración 0037). La
// tabla no lleva FK hacia el usuario: su identidad vive en identity, en otra base de datos.
type MembershipRepo struct {
	db *sql.DB
	// features resuelve los derechos comerciales del tenant. Lo usa UNA sola cosa —la guarda del
	// alta, que pregunta por multi_empresa— y por eso viaja como la interfaz de una pregunta y no
	// como el Resolver entero.
	features FeatureResolver
}

// NewMembershipRepo construye el repositorio sobre el pool dado. No toca el pool.
//
// `features` es OBLIGATORIO en la firma, y nil es un valor válido que NO desactiva el gate: lo
// deja contestando que no (fail-closed). Add da de alta, y desde el Plan 047 · Ola 5 · T5.2 el
// desenlace de un alta depende del entitlement multi_empresa del tenant: un constructor que lo
// dejara opcional convertiría «se me olvidó cablearlo» en «esta empresa no paga la
// multi-empresa». Los sitios que solo LEEN pueden pasar nil.
func NewMembershipRepo(db *sql.DB, features FeatureResolver) *MembershipRepo {
	return &MembershipRepo{db: db, features: features}
}

var _ out.MembershipRepo = (*MembershipRepo)(nil)

// TenantsOfUser implementa out.MembershipRepo: los tenants de userID en orden (created_at,
// tenant_id), estable entre llamadas. 🔴 EL ORDEN NO ELIGE NADA: con varias membresías y sin
// empresa activa el canje no toma «la primera». Fallo de la base → (nil, "iam: leer membresías:
// …").
//
// Hasta el Plan 047 · Ola 5 · T5.1 la frase era «con varios tenants el exchange falla», y ya no
// falla: resuelve por la EMPRESA ACTIVA (D-047.14). Lo que sigue siendo cierto —y es lo que este
// orden NO puede convertirse en— es que con varias membresías y sin empresa activa el canje
// emite un token sin empresa. Si alguien lee este ORDER BY como «la preferida es la más antigua»,
// habrá construido justo la elección silenciosa que está prohibida.
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

// UserTenants implementa out.MembershipRepo: las empresas de userID CON SU NOMBRE, en el MISMO
// orden que TenantsOfUser. Una empresa de la que no es miembro no puede aparecer (anti-oráculo:
// la consulta arranca en sus membresías). No filtra las empresas revocadas: el canje tampoco lo
// hace. Sin empresas, lista vacía NO nil (se serializa `[]`). Fallo de la base → (nil, "iam:
// listar las empresas del usuario: …").
//
// 🔴 EL `INNER JOIN` ES LA GARANTÍA ANTI-ORÁCULO, no un detalle de rendimiento. La consulta
// arranca en `tenant_members` filtrando por `user_id` y solo desde ahí alcanza `tenants`: una
// empresa de la que este usuario no sea miembro no tiene por dónde entrar en el resultado. Un
// `LEFT JOIN`, o invertir el orden para arrancar en `public.tenants`, rompería esa propiedad sin
// romper ninguna firma.
//
// El ORDER BY es EL MISMO que el de TenantsOfUser —(created_at, tenant_id)— y tiene que seguir
// siéndolo: los dos métodos describen la misma lista. El `tm.` delante de las columnas no es
// adorno: sin él, `created_at` es ambiguo (las DOS tablas la tienen) y Postgres lo rechaza.
//
// Aquí NO se filtra por `tenants.revoked_at` (el kill-switch comercial de D-055.2), y se dice en
// vez de callarlo: el canje tampoco lo mira, así que filtrar aquí escondería del selector una
// empresa a la que el Context Token SIGUE pudiendo acotarse — el selector mentiría sobre lo que
// el sistema hace. Si algún día una empresa revocada debe desaparecer de la vista, el sitio donde
// empezar es la resolución del tenant en el canje, no este SELECT.
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

	// Se inicializa NO NULA a propósito: cero empresas es un estado legítimo (D-056.12) y tiene
	// que serializarse como `[]`, no como `null`. Un `null` en el JSON obligaría a cada cliente a
	// distinguir dos formas del mismo hecho.
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

// MembersOf implementa out.MembershipRepo (R-P4): los miembros de tenantID y solo de él, en orden
// (created_at, user_id) con desempate estable; las tres columnas de la tabla, CERO PII. Sin
// miembros, lista vacía no nil. Fallo de la base → (nil, "iam: listar miembros del tenant: …").
//
// Es la consulta que estrena idx_tenant_members_tenant, el índice que la migración 0037 dejó
// creado «para el acceso de administración por tenant». El ORDER BY es (created_at, user_id) y no
// solo created_at: dos altas del mismo instante (la aprobación del operador escribe membresía y
// rol en la MISMA transacción, así que comparten `now()`) dejarían el orden a merced del plan de
// ejecución, y un listado que cambia de orden entre dos recargas es un listado roto para quien lo
// pagine. Aquí no se sale a identity a por el nombre (INV-02).
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

// membershipWriteLock (era bloqueoAltaDeMembresia) es el espacio de nombres del advisory lock que
// serializa las altas de UNA MISMA persona (el paso del cerrojo de GrantTenantAccess). Es un
// entero arbitrario pero FIJO —047·05·2, el plan, la ola y la tarea que lo introdujeron— y su
// único requisito es no coincidir con el de otro lock de la aplicación: hoy el único otro uso de
// advisory locks es el runner de migraciones, que usa la forma de UN argumento (bigint) y por
// tanto no comparte espacio con esta, que usa la de DOS (int, int). 🔴 Es el MISMO valor que el
// del GrantTenantAccess viejo: mientras convivan los dos árboles, las altas de una persona se
// serializan entre los dos.
const membershipWriteLock = 47052

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
//
// ════════════════════════════════════════════════════════════════════════════
// POR QUÉ ESTA FORMA (comentario-ADR del viejo)
// ════════════════════════════════════════════════════════════════════════════
// ES EL CASO DE USO COMPARTIDO QUE PIDE REQ-17, y su forma sale de mirar qué hacía de verdad la
// aprobación del operador (platformadmin.executeApprovalTx): cuatro pasos dentro de una tx, de
// los cuales SOLO LOS TRES PRIMEROS son «dar acceso» —la guarda, la membresía y el rol—. El
// cuarto, marcar la solicitud como 'approved', es del flujo de esa bandeja y se queda allí. Recibe
// el Executor en vez de abrir su propia transacción porque ese paso tiene que ser atómico con los
// de aquí: si esta función commiteara por su cuenta, una aprobación podría dar el acceso y NO
// marcar la solicitud.
//
// 🔓 LA GUARDA DEJA DE SER INCONDICIONAL (Plan 047 · Ola 5 · T5.2, D-047.14). Hasta el 2026-08-29
// esta función rechazaba SIEMPRE la segunda membresía. Hoy pregunta por el entitlement
// `multi_empresa` del tenant que RECIBE al miembro: quien compra la capacidad de incorporar a
// alguien que ya está en otra parte es la empresa que lo incorpora. Preguntar por el otro dejaría
// el permiso en manos de un tercero que no participa en esta alta.
//
// 🔴 FAIL-CLOSED, Y AQUÍ EL SENTIDO SE INVIERTE. La política de entitlements dice «si el derecho
// no se puede resolver, no se concede»: en un gate normal eso significa CORTAR, y aquí significa
// MANTENER EL RECHAZO. Un resolver caído no puede abrir una capacidad de pago ni por un instante,
// y por eso multiCompanyGranted devuelve un bool y no (bool, error).
//
// 🔒 LA VENTANA TOCTOU: CERRADA (T5.2), no aceptada. Contar y escribir en la misma transacción NO
// es exclusión mutua: bajo READ COMMITTED dos altas simultáneas de la MISMA persona en DOS
// empresas distintas leían cero las dos y escribían las dos. Con T5.2 la segunda membresía deja
// de estar prohibida, y la carrera pasaría a ser el modo de SALTARSE el gate. El cerrojo la
// cierra DENTRO de la transacción que ya existía: NO es un cambio de esquema ni una restricción
// nueva en la tabla.
//
// ⚠️ DEPENDE DE QUE HAYA TRANSACCIÓN. Las vías vivas pasan un *sql.Tx (MembershipRepo.Add,
// InvitationRedeemRepo.Redeem y la aprobación del operador). Quien añada otra vía con *sql.DB
// tendrá la guarda, pero no la exclusión.
//
// ⚠️ No hay riesgo de interbloqueo entre las vías: el cerrojo se toma como PRIMERA operación de
// escritura de la transacción y ninguna de ellas retiene antes un lock de fila (el canje lee su
// invitación con un SELECT liso, sin FOR UPDATE). Dos transacciones de la misma persona se
// ordenan; las de personas distintas no se ven —salvo colisión de hashtext, que solo cuesta una
// espera.
func GrantTenantAccess(ctx context.Context, exec Executor, features FeatureResolver, userID, tenantID string, roleID *string) error {
	// GUARDA DE ÁMBITO DEL ROL (T5.6), y va LA PRIMERA porque es lo único de esta función que se
	// decide mirando los argumentos: no toca la base, no depende del estado y no puede cambiar de
	// veredicto más tarde. Tomar un cerrojo y contar filas para acabar rechazando por una
	// asignación mal formada sería trabajo tirado, y —peor— dejaría la rama sin alcanzar: con el
	// tenant vacío, el conteo de más abajo revienta antes con un error de UUID inválido, que nombra
	// el síntoma y no el problema. Las DOS vías que escriben en iam_user_roles rechazan el par (rol
	// de empresa, ámbito global): ésta y RoleRepo.AssignToUser.
	if roleID != nil {
		if err := validateAssignmentScope(*roleID, &tenantID); err != nil {
			return err
		}
	}

	// (0) EL CERROJO DE LA PERSONA, antes de contar nada: sin él, el conteo de abajo y la
	// escritura de más abajo no son atómicos ENTRE TRANSACCIONES. hashtext() reduce el uuid a un
	// int4 —dos usuarios distintos pueden colisionar y esperarse, lo cual es correcto aunque
	// innecesario; lo que no puede pasar es que la MISMA persona no colisione consigo misma.
	if _, err := exec.ExecContext(ctx,
		`SELECT pg_advisory_xact_lock($1, hashtext($2))`, membershipWriteLock, userID); err != nil {
		return fmt.Errorf("iam: tomar el cerrojo del alta de membresía: %w", err)
	}

	others, err := countOtherMemberships(ctx, exec, userID, tenantID)
	if err != nil {
		return err
	}
	if others > 0 && !multiCompanyGranted(ctx, features, tenantID) {
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
	// El ON CONFLICT va SIN target a propósito, igual que en RoleRepo.AssignToUser: desde la 0060
	// iam_user_roles tiene un índice PARCIAL que la inferencia por columnas a secas no cubre, y la
	// forma sin target es la única que vale para los dos índices a la vez.
	if _, err := exec.ExecContext(ctx, `
		INSERT INTO public.iam_user_roles (user_id, role_id, tenant_id)
		VALUES ($1, $2, $3)
		ON CONFLICT DO NOTHING
	`, userID, *roleID, tenantID); err != nil {
		return fmt.Errorf("iam: asignar rol en el alta de acceso: %w", err)
	}
	return nil
}

// countOtherMemberships cuenta las membresías de userID en tenants DISTINTOS de tenantID. Es
// CONTABLE a propósito (M-04): no lee una fila arbitraria de una PK compuesta con N filas
// posibles por usuario —sin ORDER BY eso dejaba pasar a alguien con 2+ membresías si la fila
// leída al azar coincidía con el tenant pedido—. Un count basta: no importa CUÁL es la otra
// empresa, solo que la hay. Conserva su nombre: lo busca el candado single_membership_writer.
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

// multiCompanyGranted (era multiEmpresaConcedida) responde si el tenant que RECIBE al miembro
// tiene derecho a incorporar a alguien que ya pertenece a otra empresa
// (entitlements.FeatureMultiCompany, Plan 047 · Ola 5 · T5.2).
//
// 🔴 DEVUELVE UN BOOL Y SE TRAGA EL ERROR A PROPÓSITO, y no es descuido: la política del paquete
// entitlements es fail-closed, y aquí «cerrado» es MANTENER EL RECHAZO. Un error de
// infraestructura y un «no la tiene» tienen que producir exactamente el mismo desenlace —el 409
// de siempre—, así que distinguirlos en la firma solo invitaría a que algún llamante los tratara
// distinto y abriera una capacidad de pago por un fallo transitorio de red.
//
// El resolver nil es el mismo caso llevado al extremo: un adaptador construido sin resolver no
// puede acreditar ningún derecho, así que no concede ninguno. No es un modo «sin gate»: es el
// gate contestando que no.
func multiCompanyGranted(ctx context.Context, features FeatureResolver, tenantID string) bool {
	if features == nil {
		return false
	}
	granted, err := features.Has(ctx, tenantID, entitlements.FeatureMultiCompany)
	return err == nil && granted
}

// Add implementa out.MembershipRepo sobre GrantTenantAccess, con roleID nil (darlo de alta y
// darle un rol son dos decisiones distintas) y con SU PROPIA transacción, que confirma solo si el
// alta pasa: la guarda y la escritura son atómicas entre sí, y el cerrojo vive en ella.
// Idempotente; segunda empresa sin multi_empresa → ErrConflict (R-U24). No abrir la transacción →
// "iam: abrir tx de alta de membresía: …"; no confirmarla → "iam: confirmar alta de membresía: …".
//
// Por esta vía el alta NO asigna rol: la segunda decisión del administrador tiene su propia
// puerta (in.RoleAdmin.AssignRole).
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

	if err := GrantTenantAccess(ctx, tx, r.features, userID, tenantID, nil); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("iam: confirmar alta de membresía: %w", err)
	}
	return nil
}

// Remove implementa out.MembershipRepo: borra la membresía de userID en tenantID y solo esa.
// No-op si no estaba: la baja de algo que ya no está es el estado que se pedía. Fallo de la base
// → "iam: baja de membresía: …".
func (r *MembershipRepo) Remove(ctx context.Context, userID, tenantID string) error {
	if _, err := r.db.ExecContext(ctx, `
		DELETE FROM public.tenant_members
		WHERE user_id = $1 AND tenant_id = $2
	`, userID, tenantID); err != nil {
		return fmt.Errorf("iam: baja de membresía: %w", err)
	}
	return nil
}
