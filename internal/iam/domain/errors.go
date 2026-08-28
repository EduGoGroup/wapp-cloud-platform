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

	// ErrNoTenant indica que quien llama no trae empresa en su contexto de
	// identidad. NO es lo mismo que "no autenticado": desde D-056.12 el canje
	// emite un Context Token válido y SIN tenant para quien todavía no tiene
	// membresía, y ese token no puede administrar nada. Los usecases acotados a
	// un tenant fallan con esto antes de tocar un repositorio, porque sin tenant
	// del CONTEXTO no hay dónde acotar (INV-8) y el único sustituto posible
	// sería un tenant elegido por el llamante.
	ErrNoTenant = errors.New("iam: el contexto de identidad no trae tenant")

	// ErrGlobalRoleImmutable indica un intento de MODIFICAR una plantilla global
	// (iam_roles con tenant_id NULL) desde la administración de un tenant. Las
	// plantillas son visibles para todos y asignables por todos, y justo por eso
	// no son editables por ninguno: cambiar sus grants cambiaría los permisos de
	// todos los tenants a la vez. Leerlas y asignarlas sigue permitido.
	ErrGlobalRoleImmutable = errors.New("iam: las plantillas de rol globales no se modifican desde un tenant")

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

	// ---------------------------------------------------------------------
	// Cliente M2M de identity (Plan 056 · T2.4)
	//
	// Aquí wApp no habla como persona sino como MÁQUINA: canjea su API key por
	// un Service Token y con él asegura personas en el padrón global y les abre
	// aplicaciones. Los errores de abajo son los que el llamante NECESITA
	// distinguir para decidir; todo lo demás cae en ErrIdentityUnavailable o
	// sube envuelto tal cual.
	// ---------------------------------------------------------------------

	// ErrMachineCredentialInvalid indica que identity rechazó la credencial M2M
	// de wApp: el canje devolvió 401 (key desconocida, revocada o vencida —un
	// solo código para las tres, identity no las distingue a propósito—) o una
	// ruta M2M devolvió 403 FORBIDDEN por scope insuficiente. NO es culpa de
	// quien pidió el alta: es la configuración de wApp
	// (WAPP_IDENTITY_API_KEY y los scopes de esa key) lo que hay que arreglar.
	ErrMachineCredentialInvalid = errors.New("iam: identity rechazó la credencial M2M de wApp")

	// ErrEmailTaken indica que el correo ya tiene dueño en identity y la clave
	// presentada no es la suya (409 de POST /auth/signup —identity ADR-0027,
	// caso D—), o que esa cuenta está bloqueada o inactiva. Los tres comparten
	// respuesta: identity NO los distingue en el cable, así que wApp tampoco
	// puede. ⚠️ Esta ruta no es anti-enumerante y eso es deliberado de identity
	// (docs/RESPUESTA-wapp-alta-de-usuarios.md §1): quien la exponga al público
	// hereda ese trato, no lo empeora.
	ErrEmailTaken = errors.New("iam: el correo ya está registrado en identity")

	// ErrPasswordPolicy indica que la contraseña no cumple la política de
	// identity: mínimo 12 CARACTERES, máximo 72 BYTES, normalización NFKC y
	// NINGUNA regla de composición (identity password_policy.go:13,18). El
	// motivo textual que devolvió identity viaja envuelto con %w, así que se
	// reconoce con errors.Is y se lee con Error().
	ErrPasswordPolicy = errors.New("iam: la contraseña no cumple la política de identity")

	// ErrRateLimited indica que identity aplicó su freno por IP (429). Su cuerpo
	// NO trae Retry-After, así que no hay cuándo reintentar: quien lo reciba
	// decide su propia espera.
	ErrRateLimited = errors.New("iam: identity aplicó su límite de peticiones")

	// ErrIdentityNotConfigured indica que ESTE despliegue no tiene cliente M2M
	// de identity (falta WAPP_IDENTITY_API_KEY o WAPP_IDENTITY_URL) y la
	// operación pedida NO se puede completar sin él.
	//
	// 🔴 No es un fallo de quien llamó ni una indisponibilidad de identity: es
	// configuración que falta AQUÍ, y por eso no se puede colapsar ni en el 400
	// ni en el 500 genérico. El alta de un miembro lo devuelve antes de escribir
	// nada: sin poder acreditar la aplicación en identity, la fila de
	// tenant_members solo produciría una persona que es miembro y no puede
	// entrar — el defecto exacto que la Ola B del Plan 047 cierra.
	ErrIdentityNotConfigured = errors.New("iam: el cliente M2M de identity no está configurado en este despliegue")

	// ErrSystemNotAllowed indica que identity rechazó (403 SYSTEM_ACCESS_DENIED)
	// el conjunto de aplicaciones de un PUT /users/{id}/systems porque alguna no
	// es del ecosistema de la credencial o no existe. Es ATÓMICO: no se escribió
	// NADA, ni siquiera las claves legítimas que iban en el mismo conjunto.
	ErrSystemNotAllowed = errors.New("iam: identity rechazó alguna aplicación del conjunto")
)
