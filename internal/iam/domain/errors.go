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

	// ErrConflict indica una violación de unicidad (email por tenant, client_id,
	// key_hash, …). Lo mapean los repos desde el unique_violation de Postgres.
	ErrConflict = errors.New("iam: conflicto de unicidad")

	// ErrInvalidInput indica un argumento de entrada inválido (vacío/mal formado)
	// detectado por un usecase antes de tocar el repositorio.
	ErrInvalidInput = errors.New("iam: entrada inválida")

	// ErrInvalidCredentials indica que el par (email, password) no autentica. Es
	// deliberadamente OPACO (no distingue "usuario inexistente" de "password
	// incorrecta") para no filtrar la existencia de cuentas.
	ErrInvalidCredentials = errors.New("iam: credenciales inválidas")

	// ErrUserInactive indica que el usuario existe pero está deshabilitado
	// (is_active=false) o dado de baja (deleted_at set).
	ErrUserInactive = errors.New("iam: usuario inactivo")

	// ErrRefreshInvalid indica que un refresh token no es utilizable: no existe,
	// está revocado o expiró. Opaco por diseño (no distingue el motivo).
	ErrRefreshInvalid = errors.New("iam: refresh token inválido")

	// ErrAPIKeyInvalid indica que una api-key M2M no autentica: no existe, está
	// inactiva, revocada o expirada.
	ErrAPIKeyInvalid = errors.New("iam: api-key inválida")

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

	// ErrUserNotMigrated indica que el `sub` del Identity Token no corresponde a
	// ningún usuario de wApp, o que ese usuario no tiene membresía de tenant. Los
	// UUID se preservaron en la migración EXACTAMENTE para que esto no pase: un
	// sujeto desconocido es un usuario sin migrar, no un usuario que crear al
	// vuelo.
	ErrUserNotMigrated = errors.New("iam: el sujeto del identity token no existe en wApp")

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
