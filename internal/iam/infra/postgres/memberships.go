package iampostgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
)

// MembershipRepo implementa out.MembershipRepo sobre public.tenant_members
// (migración 0037). La tabla no lleva FK hacia el usuario: su identidad vive en
// identity, en otra base de datos.
type MembershipRepo struct {
	db *sql.DB
	// features resuelve los derechos comerciales del tenant. Lo usa UNA sola
	// cosa —la guarda del alta, que pregunta por multi_empresa— y por eso viaja
	// como la interfaz de una pregunta y no como el Resolver entero.
	features FeatureResolver
}

// NewMembershipRepo construye el repositorio sobre el pool dado.
//
// `features` es OBLIGATORIO en la firma aunque sus lecturas no lo usen, y es
// deliberado: Add da de alta, y desde el Plan 047 · Ola 5 · T5.2 el desenlace de
// un alta depende del entitlement multi_empresa del tenant. Un constructor que
// lo dejara opcional convertiría «se me olvidó cablearlo» en «esta empresa no
// paga la multi-empresa», que es un 409 que nadie sabría explicar. Los sitios
// que solo LEEN (el canje, el selector de empresa) pueden pasar nil: es el
// extremo fail-closed —sin resolver no hay derecho— y no un atajo.
func NewMembershipRepo(db *sql.DB, features FeatureResolver) *MembershipRepo {
	return &MembershipRepo{db: db, features: features}
}

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

// FeatureResolver es lo mínimo que el alta de acceso necesita del resolver de
// entitlements (interfaz local, ISP): una sola pregunta, «¿tiene este tenant
// este derecho?». La satisfacen *entitlements.Postgres (el cacheado que ya gatea
// el resto de la plataforma) y *entitlements.Fake. Declararla AQUÍ, en el
// paquete que la consume, es el mismo patrón —y la misma forma exacta— que
// integrations.FeatureResolver, que existe por esta misma razón.
type FeatureResolver interface {
	Has(ctx context.Context, tenantID, feature string) (bool, error)
}

// bloqueoAltaDeMembresia es el espacio de nombres del advisory lock que
// serializa las altas de UNA MISMA persona (ver el paso (0) de
// GrantTenantAccess). Es un entero arbitrario pero FIJO —047·05·2, el plan, la
// ola y la tarea que lo introdujeron— y su único requisito es no coincidir con
// el de otro lock de la aplicación: hoy el único otro uso de advisory locks es
// el runner de migraciones, que usa la forma de UN argumento (bigint) y por
// tanto no comparte espacio con esta, que usa la de DOS (int, int).
const bloqueoAltaDeMembresia = 47052

// GrantTenantAccess da acceso a una empresa: toma el cerrojo de la persona,
// comprueba la guarda de «una empresa por usuario, salvo que el tenant pague la
// multi-empresa», escribe la membresía y, si roleID no es nil, le asigna ese rol
// acotado al mismo tenant. Es el ÚNICO sitio del código que inserta en
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
// ════════════════════════════════════════════════════════════════════════════
// 🔓 LA GUARDA DEJA DE SER INCONDICIONAL (Plan 047 · Ola 5 · T5.2, D-047.14)
// ════════════════════════════════════════════════════════════════════════════
// Hasta el 2026-08-29 esta función rechazaba SIEMPRE la segunda membresía. Hoy
// pregunta por el entitlement `multi_empresa` del tenant que RECIBE al miembro:
//
//   - sin la feature ⇒ domain.ErrConflict, con el MISMO cuerpo de siempre
//     («el usuario ya es miembro de otra empresa») y el mismo sentinela, que es
//     el 409 que las dos bandejas traducen;
//   - con la feature ⇒ escribe, y el alta es un 2xx normal.
//
// 🔴 SE PREGUNTA POR EL TENANT DE DESTINO, no por el de origen: quien compra la
// capacidad de incorporar a alguien que ya está en otra parte es la empresa que
// lo incorpora. Preguntar por el otro dejaría el permiso en manos de un tercero
// que no participa en esta alta.
//
// 🔴 FAIL-CLOSED, Y AQUÍ EL SENTIDO SE INVIERTE. La política de entitlements dice
// «si el derecho no se puede resolver, no se concede» (ver la cabecera del
// paquete entitlements): en un gate normal eso significa CORTAR, y aquí
// significa MANTENER EL RECHAZO, que es lo mismo dicho desde el otro lado. Un
// resolver caído no puede abrir una capacidad de pago ni por un instante, así
// que su error se trata exactamente igual que un «no la tiene» — y por eso
// multiEmpresaConcedida devuelve un bool y no (bool, error): no hay un tercer
// desenlace que el llamante pudiera tratar distinto.
//
// 🔴 LO QUE ESTA FEATURE **NO** GATEA: las rutas del plano de membresías
// (D-047.10, que vence hoy y se resuelve así). Administrar miembros sigue siendo
// CAPACIDAD BASE de cualquier empresa; lo que la feature gobierna es el
// DESENLACE de un alta concreta, no el ACCESO a la puerta. Ver el 🔴 de
// entitlements.FeatureMultiEmpresa, donde está escrito el porqué.
//
// ════════════════════════════════════════════════════════════════════════════
// 🔒 LA VENTANA TOCTOU: CERRADA (T5.2), no aceptada
// ════════════════════════════════════════════════════════════════════════════
// Contar y escribir en la misma transacción NO es exclusión mutua: bajo READ
// COMMITTED dos altas simultáneas de la MISMA persona en DOS empresas distintas
// leían cero las dos y escribían las dos. Esa ventana se declaró y se aceptó
// mientras la segunda membresía estaba prohibida de todas formas —el peor
// desenlace de la carrera era llegar a un estado que otra vía ya podía alcanzar
// por deuda histórica—. Con T5.2 deja de estar prohibida, y entonces la carrera
// pasa a ser el modo de SALTARSE el gate: dos altas a la vez sobre un tenant SIN
// `multi_empresa` conseguirían lo que la feature niega.
//
// El paso (0) la cierra con el remedio que el propio comentario anterior ya
// nombraba: pg_advisory_xact_lock sobre el user_id, DENTRO de la transacción que
// ya existía. Es un cerrojo, NO un cambio de esquema y NO una restricción nueva
// en la tabla: nada de lo que hoy corre en campo cambia de forma.
//
// ⚠️ DEPENDE DE QUE HAYA TRANSACCIÓN. Un advisory lock «xact» se suelta al
// terminar la transacción, y fuera de una hay una implícita por sentencia: con
// un *sql.DB en autocommit el cerrojo se toma y se suelta en el acto, o sea que
// no serializa nada. Las TRES vías vivas pasan un *sql.Tx (MembershipRepo.Add,
// InvitationRedeemRepo.Redeem y platformadmin.executeApprovalTx), y que el canje
// pase `tx` y no `r.db` ya lo vigila un candado sobre el AST
// (canje_orden_ast_test.go). Quien añada una cuarta vía con *sql.DB tendrá la
// guarda, pero no la exclusión.
//
// ⚠️ No hay riesgo de interbloqueo entre las tres vías: el cerrojo se toma como
// PRIMERA operación de escritura de la transacción y ninguna de ellas retiene
// antes un lock de fila (el canje lee su invitación con un SELECT liso, sin FOR
// UPDATE). Dos transacciones de la misma persona se ordenan; las de personas
// distintas no se ven —salvo colisión de hashtext, que solo cuesta una espera.
func GrantTenantAccess(ctx context.Context, exec Executor, features FeatureResolver, userID, tenantID string, roleID *string) error {
	// GUARDA DE ÁMBITO DEL ROL (Plan 047 · Ola 5 · T5.6), y va LA PRIMERA porque
	// es lo único de esta función que se decide mirando los argumentos: no toca la
	// base, no depende del estado y no puede cambiar de veredicto más tarde. Tomar
	// un cerrojo y contar filas para acabar rechazando por una asignación mal
	// formada sería trabajo tirado, y —peor— dejaría la rama sin alcanzar: con el
	// tenant vacío, el conteo de más abajo revienta antes con un error de UUID
	// inválido, que nombra el síntoma y no el problema.
	//
	// El criterio de T5.6 pide que las DOS vías que escriben en iam_user_roles
	// rechacen el par (rol de empresa, ámbito global). Ésta es la segunda; la
	// primera es RoleRepo.AssignToUser. Una guarda que solo viva en una es media
	// guarda.
	if roleID != nil {
		if err := validarAmbitoDeAsignacion(*roleID, &tenantID); err != nil {
			return err
		}
	}

	// (0) EL CERROJO DE LA PERSONA, antes de contar nada: sin él, el conteo de
	// abajo y la escritura de más abajo no son atómicos ENTRE TRANSACCIONES.
	// hashtext() reduce el uuid a un int4 —dos usuarios distintos pueden colisionar
	// y esperarse, lo cual es correcto aunque innecesario; lo que no puede pasar es
	// que la MISMA persona no colisione consigo misma.
	if _, err := exec.ExecContext(ctx,
		`SELECT pg_advisory_xact_lock($1, hashtext($2))`, bloqueoAltaDeMembresia, userID); err != nil {
		return fmt.Errorf("iam: tomar el cerrojo del alta de membresía: %w", err)
	}

	others, err := countOtherMemberships(ctx, exec, userID, tenantID)
	if err != nil {
		return err
	}
	if others > 0 && !multiEmpresaConcedida(ctx, features, tenantID) {
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

// multiEmpresaConcedida responde si el tenant que RECIBE al miembro tiene
// derecho a incorporar a alguien que ya pertenece a otra empresa
// (entitlements.FeatureMultiEmpresa, Plan 047 · Ola 5 · T5.2).
//
// 🔴 DEVUELVE UN BOOL Y SE TRAGA EL ERROR A PROPÓSITO, y no es descuido: la
// política del paquete entitlements es fail-closed, y aquí «cerrado» es
// MANTENER EL RECHAZO. Un error de infraestructura y un «no la tiene» tienen que
// producir exactamente el mismo desenlace —el 409 de siempre—, así que
// distinguirlos en la firma solo invitaría a que algún llamante los tratara
// distinto y abriera una capacidad de pago por un fallo transitorio de red.
//
// El resolver nil es el mismo caso llevado al extremo: un adaptador construido
// sin resolver no puede acreditar ningún derecho, así que no concede ninguno.
// No es un modo «sin gate»: es el gate contestando que no.
func multiEmpresaConcedida(ctx context.Context, features FeatureResolver, tenantID string) bool {
	if features == nil {
		return false
	}
	concedida, err := features.Has(ctx, tenantID, entitlements.FeatureMultiEmpresa)
	return err == nil && concedida
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

	if err := GrantTenantAccess(ctx, tx, r.features, userID, tenantID, nil); err != nil {
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
