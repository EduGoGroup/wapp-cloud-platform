package domain

import "errors"

// Errores tipados del dominio IAM. Se inspeccionan con errors.Is. Los
// adaptadores (infra/postgres) mapean sus errores nativos a estos sentinels
// (sql.ErrNoRows → ErrNotFound, unique_violation 23505 → ErrConflict); los
// usecases razonan sobre ellos sin conocer el almacenamiento.
var (
	// ErrNotFound indica que un recurso solicitado no existe (o no es visible
	// para el tenant del contexto). Lo devuelven los repos GetByID/GetByEmail/…
	ErrNotFound = errors.New("iam: recurso no encontrado")

	// ErrConflict indica una violación de unicidad (nombre de rol por tenant, …).
	// Lo mapean los repos desde el unique_violation de Postgres.
	ErrConflict = errors.New("iam: conflicto de unicidad")

	// ErrInvalidInput indica un argumento de entrada inválido (vacío/mal formado)
	// detectado por un usecase antes de tocar el repositorio.
	ErrInvalidInput = errors.New("iam: entrada inválida")

	// ErrInvalidCredentials indica que el par (email, password) no autentica. Lo
	// devuelve identity-core, que es quien las valida desde la Ola 3; wApp solo
	// lo traduce. Es deliberadamente OPACO (no distingue "usuario inexistente" de
	// "password incorrecta") para no filtrar la existencia de cuentas.
	ErrInvalidCredentials = errors.New("iam: credenciales inválidas")

	// ErrUserInactive indica que identity acreditó a la persona pero le negó
	// ESTA aplicación: usuario deshabilitado o System Gate cerrado. NO nace de
	// una bandera local — desde la Ola 5 wApp no guarda ninguna (design.md Ola 5
	// §2: dos banderas de "activo" en dos bases dan dos sitios donde desactivar).
	ErrUserInactive = errors.New("iam: usuario inactivo")

	// ErrRefreshInvalid indica que un refresh token no es utilizable: no existe,
	// está revocado o expiró. Lo dictamina identity, dueño de la sesión. Opaco
	// por diseño (no distingue el motivo).
	ErrRefreshInvalid = errors.New("iam: refresh token inválido")

	// ---------------------------------------------------------------------
	// Canje de Identity Token por Context Token (identity Plan 003 · T3.1)
	// ---------------------------------------------------------------------

	// ErrIdentityTokenInvalid indica que el Identity Token presentado al canje no
	// se acepta: firma/emisor/`kid` que no cuadran, `token_use` distinto de
	// "identity", o emitido para una aplicación que no es de wApp. Opaco por
	// diseño (no distingue el motivo hacia fuera).
	ErrIdentityTokenInvalid = errors.New("iam: identity token inválido")

	// ErrIdentityTokenExpiring indica que el Identity Token es válido pero le
	// queda menos vida que el mínimo emitible de un Context Token. No se emite
	// uno más largo que su origen (identity ADR-0003, «pasaporte > visa»): el
	// cliente tiene que refrescar contra identity antes de volver a canjear.
	ErrIdentityTokenExpiring = errors.New("iam: al identity token le queda muy poca vida para canjearlo")

	// ErrUserNotMigrated indica que el `sub` del Identity Token no tiene
	// membresía de tenant en wApp (tabla tenant_members). Desde la Ola 5 esa es
	// la ÚNICA forma de pertenecer a wApp: el padrón local murió con
	// `iam_users`, así que "ser de wApp" es tener membresía, no tener fila. Los
	// UUID se preservaron en la migración EXACTAMENTE para que esto no pase: un
	// sujeto sin membresía es un usuario sin migrar, no uno que crear al vuelo.
	ErrUserNotMigrated = errors.New("iam: el sujeto del identity token no es miembro de ningún tenant de wApp")

	// ErrMultipleTenants indica que el usuario es miembro de más de un tenant y
	// el canje no puede decidir cuál va en el Context Token. Falla explícitamente
	// en vez de elegir el primero en silencio: la resolución (¿tenant en el
	// request? ¿selector?) es materia del Plan 005, que remodela la tenencia.
	ErrMultipleTenants = errors.New("iam: el usuario pertenece a más de un tenant")

	// ErrIdentityUnavailable indica que no se pudo decidir sobre el Identity
	// Token porque identity no está alcanzable (JWKS sin claves frescas). Es
	// indisponibilidad de una dependencia, NO un rechazo de la credencial: se
	// distingue para no contestar "no autorizado" a quien traía un token bueno.
	ErrIdentityUnavailable = errors.New("iam: identity no está disponible")
)
