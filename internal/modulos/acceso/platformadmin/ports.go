// Nuevo (D-F2-3, Jhoan): los puertos de platformadmin. En el viejo no existían: los handlers
// recibían el *Repository de Postgres y la lógica de la aprobación vivía mezclada con su SQL
// (internal/platformadmin/postgres.go:99-277 y access_requests.go:136-485 @ 9a77307). Separarlos
// es lo que deja probar esa lógica sin base (E-6). Solo interfaces: su suite de contrato es
// platformadminhelpertest.Contrato (diseño F2 §2), y la corren el doble en memoria
// (platformadminhelpertest.Fake) y el adaptador Postgres (Repository) en los procesos de F9.

package platformadmin

import "context"

// TenantStore es el almacén cross-tenant de empresas y sus instalaciones. Lo implementa
// *Repository (postgres.go). Ninguna operación está acotada por el tenant del llamante: quien lo
// usa ya pasó por httpapi.EnforcePlatformCaller.
//
// Precondición de todo id de empresa: un UUID bien formado (los handlers devuelven 404 a uno que
// no lo es antes de llegar aquí; Postgres devolvería un error de codificación, no ErrNotFound).
type TenantStore interface {
	// ListTenants devuelve una página de empresas ordenada por created_at DESC y, a igualdad,
	// por id DESC: el desempate hace que dos páginas consecutivas no repitan ni salten una fila
	// aunque varias compartan created_at (el seed de varios tenants en una transacción comparte
	// now()). limit ≤ 0 se toma como 50 y limit > 500 como 500; offset < 0, como 0. Nunca
	// devuelve nil: una página vacía es un arreglo vacío.
	ListTenants(ctx context.Context, limit, offset int) ([]TenantListItem, error)

	// GetTenant devuelve el detalle de la empresa id: su fila; InstallationsCount, el número de
	// edges DISTINTOS con alguna sesión; y Features, sus features efectivas (las del plan
	// —'basic' si el plan es NULL— más los overrides encendidos, menos los apagados), ordenadas,
	// sin repetir y nunca nil. ErrNotFound si no existe.
	GetTenant(ctx context.Context, id string) (TenantDetail, error)

	// ExistsTenant informa si la empresa id existe: (false, nil) si no. Es la consulta ligera de
	// quien solo necesita decidir un 404, sin pagar el detalle de GetTenant.
	ExistsTenant(ctx context.Context, id string) (bool, error)

	// CreateTenant da de alta una empresa con id nuevo y devuelve ese id y el slug. slug o
	// displayName vacíos ⇒ un error que envuelve ErrInvalidInput, sin escribir nada. planID nil
	// o "" deja el plan en NULL. Un slug que ya existe ⇒ un error que envuelve ErrConflict, sin
	// escribir nada.
	CreateTenant(ctx context.Context, slug, displayName string, planID *string) (CreatedTenant, error)

	// ListInstallations devuelve las instalaciones de la empresa tenantID, una por edge con
	// alguna sesión, ordenadas por edge_id ascendente: Sessions es su número de sesiones;
	// LastSeenAt, la última señal de cualquiera de ellas (nil si ninguna dio señal); y
	// LeaseRevoked, si su lease está revocado (false si no tiene lease). Solo las de ESA empresa.
	// Nunca devuelve nil; una empresa sin instalaciones (o que no existe) da un arreglo vacío.
	ListInstallations(ctx context.Context, tenantID string) ([]InstallationItem, error)
}

// AccessRequestStore es el almacén de la bandeja de solicitudes de acceso y los pasos con base de
// datos de su aprobación. Lo implementa *Repository (access_requests_postgres.go). La
// ORQUESTACIÓN de la aprobación (validación, cerrojo de wapp.platform, rama pending/approved,
// unión de systems en identity) NO vive aquí: es ApproveAccessRequest (access_requests.go), que
// llama a estos pasos en orden.
//
// Precondición de todo id (solicitud, persona, operador salvo donde se dice): un UUID bien formado.
//
// Estados de una solicitud: 'pending' → 'approved' | 'rejected'. Una persona tiene a lo sumo una
// solicitud 'pending'; las resueltas se conservan.
type AccessRequestStore interface {
	// ListAccessRequests devuelve las solicitudes con ese status ("" se toma como "pending"), por
	// created_at ascendente. Systems es siempre un arreglo vacío y SystemsKnown false: la bandeja
	// no lee los systems de identity al listar (C-05). Nunca devuelve nil.
	ListAccessRequests(ctx context.Context, status string) ([]AccessRequestItem, error)

	// CreateAccessRequest siembra una solicitud 'pending' de userID con su correo y su origin.
	// userID o email vacíos, u origin distinto de "bff" y "edge" ⇒ ErrInvalidInput (sin
	// envolver), sin escribir nada. Si la persona ya tiene una 'pending', no hace nada y no es
	// error: la que había se queda como estaba (idempotente). Si solo tiene resueltas, nace otra.
	CreateAccessRequest(ctx context.Context, userID, email, origin string) error

	// RejectAccessRequest pasa la solicitud requestID de 'pending' a 'rejected' guardando el
	// motivo tal cual, el operador (decided_by: operatorID si es un UUID; si no, NULL) y el
	// instante (decided_at). El motivo es OBLIGATORIO (M-02): requestID vacío o reason en blanco
	// ⇒ ErrInvalidInput sin tocar nada. ErrNotFound si la solicitud no existe; ErrConflict si
	// existe pero ya no está 'pending' (y no se toca).
	RejectAccessRequest(ctx context.Context, requestID, reason, operatorID string) error

	// LookupAccessRequestStatus devuelve la persona y el status actual de la solicitud requestID
	// (el viejo lookupAccessRequestStatus). ErrNotFound si no existe. Solo lee.
	LookupAccessRequestStatus(ctx context.Context, requestID string) (userID, status string, err error)

	// ResolveRoleID devuelve el id del rol cuyo NOMBRE o id es role (el viejo resolveRoleID).
	// ErrInvalidInput si no hay ninguno. Solo lee.
	ResolveRoleID(ctx context.Context, role string) (string, error)

	// CheckRetryApproved decide si una solicitud YA 'approved' de userID puede reintentarse hacia
	// tenantID con el rol roleID (ya resuelto): converger es reproducir el MISMO estado (el viejo
	// checkRetryApproved, Tanda 6 · 1.2). ErrConflict si la persona no es miembro de tenantID (la
	// aprobación apuntaba a otra empresa, o lo local nunca se escribió); ErrRetryRoleMismatch si
	// es miembro pero no tiene roleID en esa empresa; nil si las dos cosas casan. Solo lee.
	CheckRetryApproved(ctx context.Context, userID, tenantID, roleID string) error

	// ExecuteApprovalTx escribe, ATÓMICAMENTE, la mitad local de la aprobación (el viejo
	// executeApprovalTx): da a userID acceso a tenantID con el rol roleID (membresía y rol en esa
	// empresa, idempotentes) y pasa la solicitud requestID de 'pending' a 'approved' con el
	// operador (decided_by: operatorID si es un UUID; si no, NULL) y el instante (decided_at).
	// O todo, o nada:
	//   - si la persona ya es miembro de OTRA empresa y la de destino no tiene multi_empresa
	//     ⇒ ErrConflict (la regla de «una sola empresa» del alta de membresía);
	//   - si la solicitud no existe o ya no está 'pending' ⇒ ErrConflict;
	// y en los dos casos no queda escrito nada (ni membresía, ni rol, ni status).
	// Precondiciones: tenantID existe (ApproveAccessRequest lo comprueba antes), roleID viene de
	// ResolveRoleID y userID es el de la solicitud.
	ExecuteApprovalTx(ctx context.Context, requestID, tenantID, userID, roleID, operatorID string) error
}
