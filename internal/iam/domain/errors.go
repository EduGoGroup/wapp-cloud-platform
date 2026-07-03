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
)
