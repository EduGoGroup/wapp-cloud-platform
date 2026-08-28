package usecase

import (
	"context"
	"errors"
	"fmt"

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
}

// compile-time: MembershipService satisface el puerto de entrada.
var _ in.MembershipAdmin = (*MembershipService)(nil)

// NewMembershipService construye el servicio. Valida deps nil (fail-fast).
func NewMembershipService(caller in.CallerResolver, members out.MembershipRepo) (*MembershipService, error) {
	if caller == nil {
		return nil, errors.New("iam: MembershipService requiere un CallerResolver (INV-04: el tenant sale del contexto)")
	}
	if members == nil {
		return nil, errors.New("iam: MembershipService requiere un MembershipRepo")
	}
	return &MembershipService{caller: caller, members: members}, nil
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
// domain.ErrConflict si esa persona ya es miembro de otra: la guarda la aplica
// el repositorio dentro de su transacción, no aquí, para que valga también
// contra el estado que otra vía haya escrito entre medias.
func (s *MembershipService) AddMember(ctx context.Context, input in.MembershipInput) error {
	c, err := s.tenantOf(ctx)
	if err != nil {
		return err
	}
	if input.UserID == "" {
		return fmt.Errorf("%w: user_id vacío", domain.ErrInvalidInput)
	}
	return s.members.Add(ctx, input.UserID, c.TenantID)
}

// RemoveMember implementa in.MembershipAdmin. Solo puede dar de baja de SU
// empresa: pasar el UUID de alguien de otra no borra nada, porque el DELETE
// lleva el tenant del contexto.
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
