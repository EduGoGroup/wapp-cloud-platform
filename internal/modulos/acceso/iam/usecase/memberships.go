// Porta internal/iam/usecase/memberships.go @ 9a77307

package usecase

import (
	"context"
	"errors"
	"fmt"
	"slices"

	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
)

// MembershipService implementa in.MembershipAdmin: el alta y la baja de una persona en la
// empresa del Caller.
//
// Lo que aporta es lo que el adaptador no puede saber: de quién es el tenant (INV-04, siempre
// del contexto). La guarda de «una sola empresa» y la escritura de public.tenant_members viven
// en el repositorio (su único escritor, vigilado por el candado AST de iam/infra/postgres).
//
// Regla común: sin identidad en el contexto o con identidad sin empresa ⇒ domain.ErrNoTenant
// y no se ejecuta nada. Un user_id vacío ⇒ domain.ErrInvalidInput (envuelto con «user_id
// vacío»).
//
// 🔴 QUÉ COMPARTE CON LA VÍA DEL OPERADOR, Y QUÉ NO (REQ-17). Hasta el Plan 047 la única alta de
// membresía la escribía el operador al aprobar un access-request (platformadmin.executeApprovalTx),
// dentro de una transacción que hacía CUATRO cosas. Solo tres son «dar acceso a una empresa» —la
// guarda de una sola empresa, la membresía y el rol—; la cuarta, marcar la solicitud como
// 'approved', es del flujo de esa bandeja.
//
// Así que las tres primeras SÍ se comparten: son GrantTenantAccess del adaptador Postgres, y
// public.tenant_members tiene UN solo escritor. Lo que no se pudo compartir es la capa:
// GrantTenantAccess recibe la transacción del llamante —el operador necesita que su cuarto paso
// sea atómico con los otros tres— y una transacción no cabe en out.MembershipRepo, que es un
// puerto PURO (context y tipos de dominio). Por eso el punto de reunión está en el adaptador
// Postgres y no aquí. Que no reaparezca un segundo INSERT lo vigila un candado estructural sobre
// el AST, no la memoria de quien lea esto.
type MembershipService struct {
	caller  in.CallerResolver
	members out.MembershipRepo
	// systems acredita la aplicación en identity. Puede ser nil, y es la ÚNICA dependencia de
	// este servicio que lo admite: ver NewMembershipService.
	systems out.UserSystemsClient
	// log separa en el RASTRO lo que la respuesta HTTP funde a propósito. Es OPCIONAL (nil no
	// loguea), igual que en DelegatedAuthService. Ver recordFailure.
	log sharedlogger.Logger
}

// compile-time: MembershipService satisface el puerto de entrada.
var _ in.MembershipAdmin = (*MembershipService)(nil)

// NewMembershipService construye el servicio. caller y members son estructurales y un nil se
// rechaza al arrancar (servicio nil y texto literal):
//
//   - caller nil ⇒ "iam: MembershipService requiere un CallerResolver (INV-04: el tenant sale
//     del contexto)";
//   - members nil ⇒ "iam: MembershipService requiere un MembershipRepo".
//
// systems SÍ admite nil, y la asimetría es deliberada: es el despliegue LEGÍTIMO sin
// WAPP_IDENTITY_API_KEY, donde la lectura de miembros sigue sirviendo y solo el alta no puede
// completarse (ver AddMember). log también es opcional (nil = sin rastro).
func NewMembershipService(caller in.CallerResolver, members out.MembershipRepo, systems out.UserSystemsClient, log sharedlogger.Logger) (*MembershipService, error) {
	// systems nil no se rechaza: lo que no puede pasar es que el alta escriba a medias, y de eso
	// se encarga accredit devolviendo domain.ErrIdentityNotConfigured antes de tocar nada
	// (→ 503). Es el mismo trato que POST /api/v1/signup: sin M2M la ruta existe y contesta
	// 503, no desaparece.
	if caller == nil {
		return nil, errors.New("iam: MembershipService requiere un CallerResolver (INV-04: el tenant sale del contexto)")
	}
	if members == nil {
		return nil, errors.New("iam: MembershipService requiere un MembershipRepo")
	}
	return &MembershipService{caller: caller, members: members, systems: systems, log: log}, nil
}

// tenantOf resuelve la empresa del llamante. Mismo criterio que RoleService.tenantOf (cada
// servicio tiene el suyo, como método, igual que en el viejo): es el único origen posible del
// tenant_id (INV-04).
func (s *MembershipService) tenantOf(ctx context.Context) (in.Caller, error) {
	c, ok := s.caller.Caller(ctx)
	if !ok || c.TenantID == "" {
		return in.Caller{}, domain.ErrNoTenant
	}
	return c, nil
}

// ListMembers implementa in.MembershipAdmin: los miembros de la empresa del CONTEXTO
// (out.MembershipRepo.MembersOf con ese tenant y ningún otro). No recibe tenant: no hay dónde
// colar una empresa ajena. No sale a identity. Una lista vacía no es error.
func (s *MembershipService) ListMembers(ctx context.Context) ([]domain.Membership, error) {
	// El método no tiene parámetros a propósito: no hay dónde colar una empresa ajena ni por
	// descuido. Una comprobación se puede olvidar; un parámetro que no existe, no.
	c, err := s.tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	return s.members.MembersOf(ctx, c.TenantID)
}

// AddMember implementa in.MembershipAdmin: da de alta a la persona en la empresa del CONTEXTO.
//
// R-U24 (lo decide el repositorio, el servicio lo propaga): idempotente —repetir el alta deja
// una sola membresía—; una segunda empresa sin el derecho multi_empresa del tenant destino ⇒
// domain.ErrConflict y la membresía original intacta; con multi_empresa se escribe; si el
// resolver de derechos está caído se MANTIENE el rechazo (fail-closed invertido, T-11: «si no se
// puede resolver, no se concede»).
//
// R-U25 · se ACREDITA PRIMERO en identity y se escribe la membresía DESPUÉS:
//   - sin cliente M2M (systems nil) ⇒ domain.ErrIdentityNotConfigured y CERO escrituras (ni en
//     identity ni en tenant_members);
//   - se lee el conjunto vigente (GetUserSystems); si ya contiene SystemWappBFF NO se escribe en
//     identity (cero PUT), y se sigue con la membresía;
//   - si no, se declara (ReplaceUserSystems) la UNIÓN: el conjunto vigente, en el orden en que
//     identity lo dio, con SystemWappBFF al final. Nunca la clave sola: el PUT es declarativo y
//     lo que no viaja queda revocado;
//   - si la lectura o la escritura en identity fallan, ese error se devuelve SIN envolver
//     (errors.Is) y members.Add NO se llama.
//
// R-U26 · rastro de un fallo de acreditación (solo con logger; sin logger no revienta y
// devuelve el mismo error): si es domain.ErrMachineCredentialInvalid, Error con
// "acreditacion_imposible_por_credencial: identity rechazó la credencial M2M de wApp; reemite
// WAPP_IDENTITY_API_KEY con el scope identity.users.systems.read (el de escritura NO basta)";
// cualquier otro, Warn con "acreditacion_fallida: identity no acreditó la aplicación y la
// membresía NO se escribió". Las dos líneas llevan "user_id" y "paso" ("leer_accesos" o
// "declarar_accesos"); nunca una API key ni un correo. Al llamante se le devuelve lo mismo en
// los dos casos: el error tal cual.
func (s *MembershipService) AddMember(ctx context.Context, input in.MembershipInput) error {
	c, err := s.tenantOf(ctx)
	if err != nil {
		return err
	}
	if input.UserID == "" {
		return fmt.Errorf("%w: user_id vacío", domain.ErrInvalidInput)
	}
	// SON DOS ESCRITURAS Y EL ORDEN ES LA MITAD DEL ARREGLO (Plan 047 · Ola B). Hasta esa ola
	// solo se escribía la fila de tenant_members y nadie acreditaba `wapp.bff` en identity, así
	// que el alta podía terminar en 204 dejando a una persona que ES miembro y NO PUEDE ENTRAR:
	// identity evalúa el System Gate en el login, ANTES de emitir token, y contesta 403.
	//
	// Se acredita PRIMERO y se escribe la membresía DESPUÉS. Con ese orden, un fallo de identity
	// deja el estado ANTERIOR intacto —ni fila, ni acceso— y reintentar es idempotente por los
	// dos lados. Al revés, cada fallo dejaría exactamente el estado roto que esto cierra, y
	// ninguna de las dos escrituras puede deshacer a la otra (viven en dos sistemas y no hay
	// transacción que las abarque).
	//
	// domain.ErrConflict (otra empresa sin multi_empresa, o resolver caído: T-11) lo aplica el
	// repositorio dentro de su transacción, no aquí, para que valga también contra el estado que
	// otra vía haya escrito entre medias (MD-055.2). Implica que se acredita antes de saber que
	// la membresía se va a rechazar. No es un problema real —quien ya es miembro de otra
	// empresa YA entra a wApp, luego ya tiene `wapp.bff` y accredit no escribe nada— y
	// arreglarlo con una comprobación previa duplicaría aquí la guarda que el repositorio
	// aplica de forma atómica.
	if err := s.accredit(ctx, input.UserID); err != nil {
		return err
	}
	return s.members.Add(ctx, input.UserID, c.TenantID)
}

// accredit (era acreditar) deja abierta la aplicación web de wApp (SystemWappBFF) para esa
// persona en identity, y lo hace por UNIÓN sobre lo que ya tenía.
//
// 🔴 POR QUÉ LEER ANTES DE ESCRIBIR. ReplaceUserSystems es DECLARATIVO: lo que no viaja en el
// conjunto queda REVOCADO. Mandar `["wapp.bff"]` a secas le quitaría a esa persona cualquier
// otra aplicación de wApp que tuviera —empezando por `wapp.edge`, la del relé del Edge—, y el
// síntoma no aparecería aquí sino en su siguiente login contra la otra aplicación. Por eso se
// lee el conjunto vigente y se le AÑADE la clave; el conjunto que identity devuelve ya está
// acotado a nuestro ecosistema (ADR-0016), así que la unión no puede arrastrar ni pisar accesos
// de otro.
//
// Si la clave ya estaba, NO se escribe: el PUT no se llama ni una vez. A identity le cuesta una
// escritura y una línea de registro por cada alta repetida — que son la mayoría, porque el alta
// es idempotente y la consola la reintenta.
//
// ⚠️ TOCTOU DECLARADO Y ACEPTADO: entre el GET y el PUT cabe otra escritura, y entonces esta
// unión se calcularía sobre un conjunto ya viejo y podría revocar lo que la otra acababa de
// conceder. No se cierra, y no es descuido: identity no ofrece un alta ADITIVA de una sola clave
// (solo el PUT declarativo y un DELETE por clave), así que la única forma de cerrarlo sería un
// candado que wApp no puede tomar sobre el padrón del grupo. La ventana se acepta porque el
// ÚNICO otro escritor de los systems de esa persona dentro del ecosistema `wapp` es wApp misma
// —la aprobación del operador y el signup público—, y las tres vías son operaciones de
// administración que no corren en ráfaga sobre la misma persona. Si algún día identity publica
// el alta aditiva, esto se reduce a una llamada y el comentario sobra.
func (s *MembershipService) accredit(ctx context.Context, userID string) error {
	if s.systems == nil {
		return domain.ErrIdentityNotConfigured
	}
	current, err := s.systems.GetUserSystems(ctx, userID)
	if err != nil {
		return s.recordFailure(userID, stepRead, err)
	}
	if slices.Contains(current, SystemWappBFF) {
		return nil
	}
	// El orden es el que identity dio (alfabético) con la clave nueva al final: es estable y
	// reproducible, que es lo que permite afirmar sobre el conjunto exacto que viaja en el
	// cable. slices.Clone evita escribir en el arreglo que devolvió el puerto, que no es
	// nuestro.
	desired := append(slices.Clone(current), SystemWappBFF)
	if _, err = s.systems.ReplaceUserSystems(ctx, userID, desired); err != nil {
		return s.recordFailure(userID, stepDeclare, err)
	}
	return nil
}

// Pasos de la acreditación, tal y como salen en el rastro (eran pasoLeer y pasoDeclarar). Son
// dos literales y no una frase libre para que se puedan filtrar; sus VALORES son observables y
// no cambian.
const (
	stepRead    = "leer_accesos"
	stepDeclare = "declarar_accesos"
)

// recordFailure (era anotarFallo) deja el rastro del fallo y devuelve el mismo error, sin
// envolver: quien decide el código HTTP lo hace por errors.Is y aquí no se le cambia la
// respuesta a nadie.
//
// 🔴 POR QUÉ HAY DOS RAMAS Y NO UNA. Al llamante se le contesta lo mismo —un 500 genérico—
// cuando identity rechaza NUESTRA credencial: es un fallo del servidor, y decirle a un
// administrador que a wApp le falta un scope no le sirve de nada y cuenta de más. Pero cuando la
// respuesta funde dos causas a propósito, el LOG es el único sitio donde vive la diferencia; si
// ahí también se funden, quien diagnostica se queda ciego. Es la misma lección que costó una
// tarde el 2026-08-28 con el 401/403 del login de la consola: la causa hubo que deducirla por
// la AUSENCIA de una línea.
//
// La rama de la credencial nombra el scope EXACTO que hay que reemitir para que quien lea la
// línea llegue solo a la conclusión, sin abrir este fichero. La otra rama es todo lo demás
// —identity caído, la persona que no existe, el conjunto rechazado— y ahí el error sí describe
// el problema.
//
// NADA de material: por aquí no pasa ni la API key ni un correo. La key vive dentro del
// adaptador M2M (que no loguea) y los errores que devuelve nombran la operación y el código
// HTTP, nunca lo que viajó. El user_id sí va: es un id opaco de identity y es lo único que
// permite seguir un alta concreta.
func (s *MembershipService) recordFailure(userID, step string, err error) error {
	if s.log == nil {
		return err
	}
	if errors.Is(err, domain.ErrMachineCredentialInvalid) {
		s.log.Error("acreditacion_imposible_por_credencial: identity rechazó la credencial M2M de wApp; "+
			"reemite WAPP_IDENTITY_API_KEY con el scope identity.users.systems.read (el de escritura NO basta)",
			"user_id", userID, "paso", step)
		return err
	}
	s.log.Warn("acreditacion_fallida: identity no acreditó la aplicación y la membresía NO se escribió",
		"user_id", userID, "paso", step, "error", err)
	return err
}

// RemoveMember implementa in.MembershipAdmin: da de baja a la persona SOLO de la empresa del
// CONTEXTO (el DELETE lleva ese tenant): pasar el UUID de alguien de otra empresa es un no-op sin
// error. Idempotente. NO revoca SystemWappBFF en identity (decisión de producto, no simetría
// olvidada).
func (s *MembershipService) RemoveMember(ctx context.Context, input in.MembershipInput) error {
	// ⚠️ NO es simétrica con AddMember y no lo será por descuido: la baja retira la membresía y
	// NO revoca `wapp.bff` en identity. Sin membresía el canje ya no le resuelve empresa —entra
	// a la aplicación y no puede operar en ninguna—, así que la revocación no añade seguridad;
	// y revocarla sí rompería a quien esté operando desde el Edge con la misma cuenta. Cambiar
	// esto es una decisión de producto, no una simetría que falte.
	c, err := s.tenantOf(ctx)
	if err != nil {
		return err
	}
	if input.UserID == "" {
		return fmt.Errorf("%w: user_id vacío", domain.ErrInvalidInput)
	}
	return s.members.Remove(ctx, input.UserID, c.TenantID)
}
