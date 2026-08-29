package usecase

// invitations.go — LA EMISIÓN, EL LISTADO Y LA REVOCACIÓN DE INVITACIONES
// (Plan 047 · Ola A · T-A2 y T-A8, D-047.11).
//
// Resuelve el hueco que la Ola 1 dejó abierto: la dueña de una empresa no tiene
// bandeja y no ve a nadie, así que a quien se registra por su cuenta no lo puede
// buscar. Hasta hoy la única vía era que esa persona le DICTARA su UUID. Con
// esto la dueña emite un código opaco, se lo pasa por WhatsApp —que es el
// producto, no un canal prestado— y la persona lo canjea después de registrarse
// ella misma. Ni correo, ni SMTP, ni mailer: aquí no se teclea el correo de
// nadie.
//
// 🔴 ESTE FICHERO NO CANJEA. El canje es T-A3 y escribe la membresía a través de
// iampostgres.GrantTenantAccess, único escritor de public.tenant_members en todo
// el código (vigilado por membresia_unica_ast_test.go). Lo que se deja listo
// para él es el contrato: domain.HashInvitationToken, el estado derivado
// domain.Invitation.Status y el UPDATE atómico condicionado de la revocación,
// que es EXACTAMENTE la forma que el canje tiene que copiar.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
)

// El TTL de la invitación, decidido por Jhoan el 2026-08-28 y con UN SOLO dueño:
// este fichero. La migración 0085 renuncia a poner un DEFAULT sobre expires_at
// precisamente para que el número no viva en dos sitios — un default en el DDL
// congelaría un valor que el emisor sobreescribe en cada llamada, y haría creer
// que el clamp también lo aplica la base, que no lo aplica.
//
// Los tres números son el precedente LITERAL de los códigos de enrolamiento
// (platformadmin/handlers.go:249-260), que resuelven el mismo problema: un código
// de un solo uso que alguien reparte a mano y que tiene que caducar. Copiar esa
// forma —incluido el nombre `ttl` del campo en el cuerpo— es lo que hace que las
// dos emisiones de la casa se expliquen con la misma frase.
const (
	// ttlInvitacionPorDefecto son 24 horas: lo que dura si quien emite no pide otra cosa.
	ttlInvitacionPorDefecto = 86400
	// ttlInvitacionMinimo evita la invitación que nace muerta por una unidad mal
	// puesta (segundos escritos como si fueran minutos).
	ttlInvitacionMinimo = 60
	// ttlInvitacionMaximo son 30 días: el techo por encima del cual una
	// invitación deja de ser una invitación y es una puerta abierta.
	ttlInvitacionMaximo = 30 * 86400
)

// InvitationService implementa in.InvitationAdmin.
type InvitationService struct {
	caller      in.CallerResolver
	invitations out.InvitationRepo
	// roles solo se usa para UNA cosa: comprobar que el rol que se promete en la
	// invitación es visible para la empresa que la emite. Ver rolPrometido.
	roles out.RoleRepo
}

// compile-time: InvitationService satisface el puerto de entrada.
var _ in.InvitationAdmin = (*InvitationService)(nil)

// NewInvitationService construye el servicio y valida sus dependencias
// (fail-fast al arrancar). Aquí NINGUNA admite nil, a diferencia del `systems`
// de MembershipService: las tres son estructurales —sin caller no hay empresa a
// la que acotar, sin repositorio no hay tabla que tocar, y sin roles no se puede
// comprobar el rol prometido— y un nil sería un error de cableado, no un
// despliegue legítimo.
func NewInvitationService(caller in.CallerResolver, invitations out.InvitationRepo, roles out.RoleRepo) (*InvitationService, error) {
	if caller == nil {
		return nil, errors.New("iam: InvitationService requiere un CallerResolver (INV-04: el tenant sale del contexto)")
	}
	if invitations == nil {
		return nil, errors.New("iam: InvitationService requiere un InvitationRepo")
	}
	if roles == nil {
		return nil, errors.New("iam: InvitationService requiere un RoleRepo para validar el rol prometido")
	}
	return &InvitationService{caller: caller, invitations: invitations, roles: roles}, nil
}

// tenantOf resuelve la empresa del llamante. Mismo criterio que en RoleService y
// MembershipService: es el único origen posible del tenant_id (INV-04).
func (s *InvitationService) tenantOf(ctx context.Context) (in.Caller, error) {
	c, ok := s.caller.Caller(ctx)
	if !ok || c.TenantID == "" {
		return in.Caller{}, domain.ErrNoTenant
	}
	return c, nil
}

// vidaDe aplica el DEFAULT y el CLAMP del TTL, en ese orden, y es la copia
// exacta del patrón de IssueEnrollmentCodeHandler.
//
// Un TTL <= 0 significa AUSENTE, no "que caduque ya": el cuerpo de la petición
// es opcional y un entero de Go no distingue la clave que no vino del cero
// escrito a mano. La diferencia no es interesante aquí —nadie pide de verdad una
// invitación de cero segundos— y colapsarlas en el default es lo que hace que el
// cuerpo `{}` y el cuerpo ausente se comporten igual.
func vidaDe(ttlSegundos int) time.Duration {
	ttl := ttlInvitacionPorDefecto
	if ttlSegundos > 0 {
		ttl = ttlSegundos
	}
	if ttl < ttlInvitacionMinimo {
		ttl = ttlInvitacionMinimo
	} else if ttl > ttlInvitacionMaximo {
		ttl = ttlInvitacionMaximo
	}
	return time.Duration(ttl) * time.Second
}

// rolPedido normaliza el rol que llega en la entrada: nil si no se pidió
// ninguno, y también si vino como cadena vacía. Es traducción pura y no consulta
// nada, para que la validación de abajo tenga UNA sola pregunta que hacer.
func rolPedido(roleID *string) *string {
	if roleID == nil {
		return nil
	}
	if strings.TrimSpace(*roleID) == "" {
		return nil
	}
	return roleID
}

// exigirRolVisible comprueba que el rol que la invitación concederá al canjearse
// es visible para la empresa que la emite.
//
// 🔴 POR QUÉ SE VALIDA AQUÍ Y NO EN EL CANJE. La FK de la tabla apunta a
// iam_roles a secas y iam_roles contiene los roles de TODAS las empresas: sin
// esta comprobación, quien emite podría teclear el UUID de un rol de otra
// empresa y la base lo aceptaría encantada. El agujero no se vería al emitir
// —la respuesta sería un 201 perfecto— sino meses después, cuando el canje
// diera de alta a alguien con un rol ajeno. Es el mismo criterio de visibilidad
// que RoleService.visibleRole: suyo o plantilla global, y cualquier otro caso es
// ErrNotFound y no un "prohibido", que confirmaría que ese rol existe fuera.
func (s *InvitationService) exigirRolVisible(ctx context.Context, tenantID, roleID string) error {
	role, err := s.roles.GetByID(ctx, roleID)
	if err != nil {
		return err
	}
	if role.TenantID != nil && *role.TenantID != tenantID {
		return domain.ErrNotFound
	}
	return nil
}

// IssueInvitation implementa in.InvitationAdmin: emite una invitación para la
// empresa del CONTEXTO y devuelve el token en claro por única vez.
//
// EL ORDEN ES EL QUE ES: primero se resuelve la empresa (sin ella no hay nada
// que emitir), después se valida el rol (para no gastar un token en una petición
// que va a fallar) y solo entonces se genera el secreto. Con ese orden, una
// emisión rechazada no deja ni una fila ni un token vivo suelto.
func (s *InvitationService) IssueInvitation(ctx context.Context, input in.IssueInvitationInput) (in.IssuedInvitation, error) {
	c, err := s.tenantOf(ctx)
	if err != nil {
		return in.IssuedInvitation{}, err
	}
	rol := rolPedido(input.RoleID)
	if rol != nil {
		if err := s.exigirRolVisible(ctx, c.TenantID, *rol); err != nil {
			return in.IssuedInvitation{}, err
		}
	}
	token, err := domain.NewInvitationToken()
	if err != nil {
		return in.IssuedInvitation{}, fmt.Errorf("iam: emitir invitación: %w", err)
	}
	fila, err := s.invitations.Create(ctx, domain.Invitation{
		TenantID: c.TenantID,
		// Lo que se guarda es el DIGEST. El token en claro no vuelve a existir en
		// ninguna parte después de esta función salvo en la respuesta HTTP.
		TokenHash: domain.HashInvitationToken(token),
		RoleID:    rol,
		ExpiresAt: time.Now().UTC().Add(vidaDe(input.TTLSeconds)),
		CreatedBy: c.UserID,
	})
	if err != nil {
		return in.IssuedInvitation{}, err
	}
	return in.IssuedInvitation{Invitation: fila, Token: token}, nil
}

// ListInvitations implementa in.InvitationAdmin: las invitaciones de la empresa
// del CONTEXTO.
//
// Sin parámetros, por la misma razón que ListMembers: no hay dónde colar una
// empresa ajena ni por descuido. Una comprobación se puede olvidar; un parámetro
// que no existe, no.
func (s *InvitationService) ListInvitations(ctx context.Context) ([]domain.Invitation, error) {
	c, err := s.tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	return s.invitations.ListByTenant(ctx, c.TenantID)
}

// RevokeInvitation implementa in.InvitationAdmin (T-A8): anula una invitación
// viva de la empresa del CONTEXTO.
//
// El tenant viaja al repositorio y entra en el WHERE del UPDATE, no en un `if`
// posterior: revocar la invitación de otra empresa no puede depender de que
// alguien recuerde comprobarlo después de haber escrito.
func (s *InvitationService) RevokeInvitation(ctx context.Context, id string) error {
	c, err := s.tenantOf(ctx)
	if err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("%w: id vacío", domain.ErrInvalidInput)
	}
	// Un id que no parsea como UUID no puede estar en una columna `uuid`, así que
	// su destino es el MISMO que el de un id que no existe: 404. Se comprueba
	// aquí, en el usecase, y no en el adaptador Postgres, porque así las dos
	// implementaciones del puerto contestan lo mismo a la misma pregunta sin
	// tener que llevar cada una su guarda —el doble en memoria no daría nunca el
	// 22P02 de Postgres, y esa asimetría es la que convierte un 404 en un 500 en
	// producción y en verde en los tests—. Precedente: store.ListIntakeItems.
	if _, perr := uuid.Parse(id); perr != nil {
		return domain.ErrNotFound
	}
	return s.invitations.Revoke(ctx, id, c.TenantID)
}
