package usecase

import (
	"context"
	"errors"
	"fmt"
	"slices"

	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
)

// MembershipService implementa in.MembershipAdmin: el alta y la baja de una
// persona en la empresa del llamante.
//
// 🔴 QUÉ COMPARTE CON LA VÍA DEL OPERADOR, Y QUÉ NO (REQ-17). Hasta el Plan 047
// la única alta de membresía la escribía el operador al aprobar un
// access-request (platformadmin.executeApprovalTx), dentro de una transacción
// que hacía CUATRO cosas. Solo tres son «dar acceso a una empresa» —la guarda de
// una sola empresa, la membresía y el rol—; la cuarta, marcar la solicitud como
// 'approved', es del flujo de esa bandeja.
//
// Así que las tres primeras SÍ se comparten: son iampostgres.GrantTenantAccess,
// y public.tenant_members tiene UN solo escritor en todo el código. Lo que no se
// pudo compartir es la capa: GrantTenantAccess recibe la transacción del
// llamante —el operador necesita que su cuarto paso sea atómico con los otros
// tres— y una transacción no cabe en out.MembershipRepo, que es un puerto PURO
// (context y tipos de dominio, ports/out/repos.go:1). Por eso el punto de
// reunión está en el adaptador Postgres y no aquí: este servicio aporta lo que
// el adaptador no puede saber, que es de quién es el tenant.
//
// Que no reaparezca un segundo INSERT lo vigila un candado estructural sobre el
// AST (iam/infra/postgres/membresia_unica_ast_test.go), no la memoria de quien
// lea esto.
type MembershipService struct {
	caller  in.CallerResolver
	members out.MembershipRepo
	// systems acredita la aplicación en identity. Puede ser nil, y es la ÚNICA
	// dependencia de este servicio que lo admite: ver NewMembershipService.
	systems out.UserSystemsClient
	// log separa en el RASTRO lo que la respuesta HTTP funde a propósito. Es
	// OPCIONAL (nil no loguea), igual que en DelegatedAuthService. Ver acreditar.
	log sharedlogger.Logger
}

// compile-time: MembershipService satisface el puerto de entrada.
var _ in.MembershipAdmin = (*MembershipService)(nil)

// NewMembershipService construye el servicio. Valida deps nil (fail-fast) SALVO
// systems, y esa asimetría es deliberada.
//
// caller y members son estructurales: sin ellos el servicio no puede ni resolver
// la empresa ni tocar la tabla, así que un nil es un error de cableado y se
// rechaza al arrancar. systems, en cambio, es nil en un despliegue LEGÍTIMO —el
// que no tiene WAPP_IDENTITY_API_KEY— y ahí la respuesta correcta no es negarse
// a arrancar: la LECTURA de miembros sigue sirviendo perfectamente y solo el
// alta queda sin poder completarse. Lo que no puede pasar es que el alta escriba
// a medias, y de eso se encarga acreditar() devolviendo
// domain.ErrIdentityNotConfigured antes de tocar nada (→ 503).
//
// Es el mismo trato que POST /api/v1/signup (internal/bootstrap/http.go): sin
// M2M la ruta existe y contesta 503, no desaparece.
func NewMembershipService(caller in.CallerResolver, members out.MembershipRepo, systems out.UserSystemsClient, log sharedlogger.Logger) (*MembershipService, error) {
	if caller == nil {
		return nil, errors.New("iam: MembershipService requiere un CallerResolver (INV-04: el tenant sale del contexto)")
	}
	if members == nil {
		return nil, errors.New("iam: MembershipService requiere un MembershipRepo")
	}
	return &MembershipService{caller: caller, members: members, systems: systems, log: log}, nil
}

// tenantOf resuelve la empresa del llamante. Mismo criterio que en RoleService:
// es el único origen posible del tenant_id (INV-04).
func (s *MembershipService) tenantOf(ctx context.Context) (in.Caller, error) {
	c, ok := s.caller.Caller(ctx)
	if !ok || c.TenantID == "" {
		return in.Caller{}, domain.ErrNoTenant
	}
	return c, nil
}

// ListMembers implementa in.MembershipAdmin: quién está en la empresa del
// CONTEXTO.
//
// El tenant sale de tenantOf y de ningún otro sitio (INV-04). Fíjate en que el
// método no tiene parámetros: no hay dónde colar una empresa ajena ni por
// descuido, que es la razón de que la firma sea así y no `ListMembers(tenantID)`
// con una comprobación dentro — una comprobación se puede olvidar, un parámetro
// que no existe no.
func (s *MembershipService) ListMembers(ctx context.Context) ([]domain.Membership, error) {
	c, err := s.tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	return s.members.MembersOf(ctx, c.TenantID)
}

// AddMember implementa in.MembershipAdmin. Da de alta a la persona en la empresa
// del CONTEXTO; el llamante no elige empresa.
//
// SON DOS ESCRITURAS Y EL ORDEN ES LA MITAD DEL ARREGLO (Plan 047 · Ola B).
// Hasta esta ola solo se escribía la fila de tenant_members y nadie acreditaba
// `wapp.bff` en identity, así que el alta podía terminar en 204 dejando a una
// persona que ES miembro y NO PUEDE ENTRAR: identity evalúa el System Gate en el
// login, ANTES de emitir token, y contesta 403. El administrador veía «añadido»
// y la persona se quedaba fuera, sin un error en ningún sitio.
//
// Se acredita PRIMERO y se escribe la membresía DESPUÉS. Con ese orden, un fallo
// de identity deja el estado ANTERIOR intacto —ni fila, ni acceso— y reintentar
// es idempotente por los dos lados. Al revés, cada fallo dejaría exactamente el
// estado roto que esto cierra, y ninguna de las dos escrituras puede
// deshacer a la otra (viven en dos sistemas y no hay transacción que las abarque).
//
// domain.ErrConflict si esa persona ya es miembro de otra empresa: la guarda la
// aplica el repositorio dentro de su transacción, no aquí, para que valga
// también contra el estado que otra vía haya escrito entre medias (MD-055.2).
// Ojo a lo que implica el orden en ese caso: se acredita antes de saber que la
// membresía se va a rechazar. No es un problema real —quien ya es miembro de
// otra empresa YA entra a wApp, luego ya tiene `wapp.bff` y acreditar no escribe
// nada (ver acreditar)— y arreglarlo con una comprobación previa duplicaría en
// este usecase la guarda que el repositorio aplica de forma atómica.
func (s *MembershipService) AddMember(ctx context.Context, input in.MembershipInput) error {
	c, err := s.tenantOf(ctx)
	if err != nil {
		return err
	}
	if input.UserID == "" {
		return fmt.Errorf("%w: user_id vacío", domain.ErrInvalidInput)
	}
	if err := s.acreditar(ctx, input.UserID); err != nil {
		return err
	}
	return s.members.Add(ctx, input.UserID, c.TenantID)
}

// acreditar deja abierta la aplicación web de wApp (`wapp.bff`) para esa persona
// en identity, y lo hace por UNIÓN sobre lo que ya tenía.
//
// 🔴 POR QUÉ LEER ANTES DE ESCRIBIR. ReplaceUserSystems es DECLARATIVO: lo que
// no viaja en el conjunto queda REVOCADO. Mandar `["wapp.bff"]` a secas le
// quitaría a esa persona cualquier otra aplicación de wApp que tuviera —empezando
// por `wapp.edge`, la del relé del Edge—, y el síntoma no aparecería aquí sino
// en su siguiente login contra la otra aplicación. Por eso se lee el conjunto
// vigente y se le AÑADE la clave; el conjunto que identity devuelve ya está
// acotado a nuestro ecosistema (ADR-0016), así que la unión no puede arrastrar
// ni pisar accesos de otro.
//
// Si la clave ya estaba, NO se escribe: el PUT no se llama ni una vez. Es la
// diferencia entre «no había nada que cambiar» y «se volvió a declarar lo mismo»,
// y a identity le cuesta una escritura y una línea de registro por cada alta
// repetida — que son la mayoría, porque el alta es idempotente y la consola la
// reintenta.
//
// ⚠️ TOCTOU DECLARADO Y ACEPTADO: entre el GET y el PUT cabe otra escritura, y
// entonces esta unión se calcularía sobre un conjunto ya viejo y podría revocar
// lo que la otra acababa de conceder. No se cierra, y no es descuido: identity no
// ofrece un alta ADITIVA de una sola clave (solo el PUT declarativo y un DELETE
// por clave), así que la única forma de cerrarlo sería un candado que wApp no
// puede tomar sobre el padrón del grupo. La ventana se acepta porque el ÚNICO
// otro escritor de los systems de esa persona dentro del ecosistema `wapp` es
// wApp misma —la aprobación del operador y el signup público—, y las tres vías
// son operaciones de administración que no corren en ráfaga sobre la misma
// persona. Si algún día identity publica el alta aditiva, esto se reduce a una
// llamada y el comentario sobra.
func (s *MembershipService) acreditar(ctx context.Context, userID string) error {
	if s.systems == nil {
		return domain.ErrIdentityNotConfigured
	}
	vigentes, err := s.systems.GetUserSystems(ctx, userID)
	if err != nil {
		return s.anotarFallo(userID, pasoLeer, err)
	}
	if slices.Contains(vigentes, SystemWappBFF) {
		return nil
	}
	// El orden es el que identity dio (alfabético) con la clave nueva al final:
	// es estable y reproducible, que es lo que permite afirmar sobre el conjunto
	// exacto que viaja en el cable. slices.Clone evita escribir en el arreglo que
	// devolvió el puerto, que no es nuestro.
	deseados := append(slices.Clone(vigentes), SystemWappBFF)
	if _, err = s.systems.ReplaceUserSystems(ctx, userID, deseados); err != nil {
		return s.anotarFallo(userID, pasoDeclarar, err)
	}
	return nil
}

// Pasos de la acreditación, tal y como salen en el rastro. Son dos literales y
// no una frase libre para que se puedan filtrar.
const (
	pasoLeer     = "leer_accesos"
	pasoDeclarar = "declarar_accesos"
)

// anotarFallo deja el rastro del fallo y devuelve el mismo error, sin envolver:
// quien decide el código HTTP lo hace por errors.Is y aquí no se le cambia la
// respuesta a nadie.
//
// 🔴 POR QUÉ HAY DOS RAMAS Y NO UNA. Al llamante se le contesta lo mismo —un 500
// genérico— cuando identity rechaza NUESTRA credencial: es un fallo del
// servidor, y decirle a un administrador que a wApp le falta un scope no le
// sirve de nada y cuenta de más. Pero cuando la respuesta funde dos causas a
// propósito, el LOG es el único sitio donde vive la diferencia; si ahí también
// se funden, quien diagnostica se queda ciego. Es la misma lección que costó una
// tarde el 2026-08-28 con el 401/403 del login de la consola: la causa hubo que
// deducirla por la AUSENCIA de una línea.
//
// La rama de la credencial nombra el scope EXACTO que hay que reemitir para que
// quien lea la línea llegue solo a la conclusión, sin abrir este fichero. La
// otra rama es todo lo demás —identity caído, la persona que no existe, el
// conjunto rechazado— y ahí el error sí describe el problema.
//
// NADA de material: por aquí no pasa ni la API key ni un correo. La key vive
// dentro del adaptador M2M (que no loguea) y los errores que devuelve nombran la
// operación y el código HTTP, nunca lo que viajó. El user_id sí va: es un id
// opaco de identity y es lo único que permite seguir un alta concreta.
func (s *MembershipService) anotarFallo(userID, paso string, err error) error {
	if s.log == nil {
		return err
	}
	if errors.Is(err, domain.ErrMachineCredentialInvalid) {
		s.log.Error("acreditacion_imposible_por_credencial: identity rechazó la credencial M2M de wApp; "+
			"reemite WAPP_IDENTITY_API_KEY con el scope identity.users.systems.read (el de escritura NO basta)",
			"user_id", userID, "paso", paso)
		return err
	}
	s.log.Warn("acreditacion_fallida: identity no acreditó la aplicación y la membresía NO se escribió",
		"user_id", userID, "paso", paso, "error", err)
	return err
}

// RemoveMember implementa in.MembershipAdmin. Solo puede dar de baja de SU
// empresa: pasar el UUID de alguien de otra no borra nada, porque el DELETE
// lleva el tenant del contexto.
//
// ⚠️ NO es simétrica con AddMember y no lo será por descuido: la baja retira la
// membresía y NO revoca `wapp.bff` en identity. Sin membresía el canje ya no le
// resuelve empresa —entra a la aplicación y no puede operar en ninguna—, así que
// la revocación no añade seguridad; y revocarla sí rompería a quien esté
// operando desde el Edge con la misma cuenta. Cambiar esto es una decisión de
// producto, no una simetría que falte.
func (s *MembershipService) RemoveMember(ctx context.Context, input in.MembershipInput) error {
	c, err := s.tenantOf(ctx)
	if err != nil {
		return err
	}
	if input.UserID == "" {
		return fmt.Errorf("%w: user_id vacío", domain.ErrInvalidInput)
	}
	return s.members.Remove(ctx, input.UserID, c.TenantID)
}
