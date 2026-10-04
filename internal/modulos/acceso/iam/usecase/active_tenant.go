// Porta internal/iam/usecase/active_tenant.go @ 9a77307

package usecase

// active_tenant.go — LA ELECCIÓN DE EMPRESA DE QUIEN PERTENECE A VARIAS (Plan 047 · Ola 5 ·
// T5.1, D-047.14).
//
// Este servicio tiene UNA regla: elegir una empresa no da acceso a ella. Comprueba la membresía
// al ESCRIBIR —guardar una preferencia hacia una empresa ajena no tendría sentido— y esa
// comprobación NO es la que protege: la que protege es la de la LECTURA, que corre en cada canje
// (ExchangeService). El listado (TenantsOfCaller) marca la empresa con LA MISMA regla que usa el
// canje, y por eso los dos no pueden discrepar (R-U18).

import (
	"context"
	"errors"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
)

// ActiveTenantService implementa in.ActiveTenantSelector (escribe la elección) e
// in.TenantLister (lee entre qué se puede elegir): comparten las tres dependencias y la regla.
type ActiveTenantService struct {
	caller  in.CallerResolver
	members out.MembershipRepo
	active  out.ActiveTenantRepo
}

// compile-time: ActiveTenantService satisface los DOS puertos de entrada de este plano.
//
// Son dos puertos y un solo servicio porque comparten exactamente las tres dependencias y la
// regla: quién llama, de qué es miembro, y qué eligió. Partirlo en dos servicios duplicaría el
// cableado para no separar nada.
var (
	_ in.ActiveTenantSelector = (*ActiveTenantService)(nil)
	_ in.TenantLister         = (*ActiveTenantService)(nil)
)

// NewActiveTenantService construye el servicio. R-U19: las tres dependencias son estructurales
// y un nil en cualquiera es error de cableado (fail-fast), con servicio nil y estos textos
// literales:
//
//   - caller nil ⇒ "iam: ActiveTenantService requiere un CallerResolver (quien elige sale del
//     contexto)";
//   - members nil ⇒ "iam: ActiveTenantService requiere un MembershipRepo (elegir empresa exige
//     ser miembro)";
//   - active nil ⇒ "iam: ActiveTenantService requiere un ActiveTenantRepo".
func NewActiveTenantService(caller in.CallerResolver, members out.MembershipRepo, active out.ActiveTenantRepo) (*ActiveTenantService, error) {
	// Igual que en NewRedeemService: sin resolver quién llama no se sabe a nombre de quién se
	// guarda, sin membresías no se puede comprobar nada, y sin repositorio no hay dónde guardar.
	if caller == nil {
		return nil, errors.New("iam: ActiveTenantService requiere un CallerResolver (quien elige sale del contexto)")
	}
	if members == nil {
		return nil, errors.New("iam: ActiveTenantService requiere un MembershipRepo (elegir empresa exige ser miembro)")
	}
	if active == nil {
		return nil, errors.New("iam: ActiveTenantService requiere un ActiveTenantRepo")
	}
	return &ActiveTenantService{caller: caller, members: members, active: active}, nil
}

// SelectActiveTenant implementa in.ActiveTenantSelector: guarda tenantID como empresa activa
// del Caller (out.ActiveTenantRepo.SetActiveTenant, que REEMPLAZA: un valor por usuario).
//
// Se exige el SUJETO del contexto y NO la empresa del token: quien llega aquí trae normalmente
// un Context Token SIN empresa (dos membresías y ninguna elegida).
//
// R-U15:
//   - nil ⇒ guardada: lo que se comprueba es lo que quedó escrito, no solo el error. Una segunda
//     elección reemplaza a la primera; no acumula.
//   - Contexto sin identidad o con UserID vacío ⇒ domain.ErrInvalidInput (envuelto con «el
//     contexto no acredita a nadie»); tenantID vacío ⇒ domain.ErrInvalidInput (envuelto con
//     «falta la empresa»). Es 400 y no 401: a este método solo se llega detrás de Authenticate.
//   - Quien NO es miembro de tenantID ⇒ domain.ErrNotFound SIN envolver, y no se escribe nada.
//     Una empresa ajena que existe y una que no existe dan EXACTAMENTE el mismo error
//     (anti-oráculo: si no, cualquiera con un token podría levantar el censo de empresas).
//   - Un fallo leyendo las membresías o escribiendo la elección se propaga.
//
// R-U16: con UNA sola membresía también se puede fijar (desenlace normal, no error).
func (s *ActiveTenantService) SelectActiveTenant(ctx context.Context, tenantID string) error {
	// 🔴 SE EXIGE EL SUJETO Y NO LA EMPRESA DEL TOKEN, exactamente como en
	// RedeemService.RedeemInvitation y por la misma familia de razones: con dos membresías y
	// ninguna elegida el token sale sin tenant y sin grants, así que exigir la empresa del
	// token rechazaría con 403 a todos los que necesitan este endpoint, siempre.
	c, ok := s.caller.Caller(ctx)
	if !ok || c.UserID == "" {
		// Es 400 y no 401: a este método solo se llega DETRÁS de Authenticate, así que un
		// contexto sin identidad es un error de cableado del servidor, no una credencial que
		// falte. Mismo criterio que RedeemService.
		return fmt.Errorf("%w: el contexto no acredita a nadie", domain.ErrInvalidInput)
	}
	if tenantID == "" {
		return fmt.Errorf("%w: falta la empresa", domain.ErrInvalidInput)
	}

	tenants, err := s.members.TenantsOfUser(ctx, c.UserID)
	if err != nil {
		return err
	}
	// isMember es la MISMA función con la que el canje contrasta la empresa guardada
	// (effectiveTenant, en exchange.go): la escritura y la lectura dan el mismo veredicto.
	if !isMember(tenants, tenantID) {
		// 🔴 EL 404 DE QUIEN NO ES MIEMBRO NO ES UN ERROR DE CORTESÍA: es anti-oráculo. Si «no
		// eres miembro de esa empresa» y «esa empresa no existe» tuvieran respuestas
		// distintas, cualquiera con un token válido podría sondear UUIDs y levantar el censo de
		// empresas de la plataforma. El transporte lo traduce al MISMO cuerpo genérico
		// («recurso no encontrado») con el que el resto del módulo contesta al recurso ajeno.
		//
		// Sin `fmt.Errorf` con contexto a propósito: lo que se envuelva aquí acaba en el log, y
		// el log de este proceso no necesita una línea por cada UUID que alguien pruebe.
		return domain.ErrNotFound
	}
	return s.active.SetActiveTenant(ctx, c.UserID, tenantID)
}

// TenantsOfCaller implementa in.TenantLister: las empresas del Caller con su nombre legible
// (out.MembershipRepo.UserTenants) y cuál llevará su PRÓXIMO Context Token.
//
// R-U17:
//   - Devuelve SOLO las empresas de las que es miembro, con ID y DisplayName.
//   - Cero empresas ⇒ lista VACÍA y NO nil (se serializa como [] y no como null), activeID
//     vacío y err nil: es el estado de quien acaba de registrarse (D-056.12), no un 404.
//   - activeID NO es la fila guardada: es lo que el canje resolvería AHORA, con la misma regla
//     —cero ⇒ ""; UNA ⇒ esa, aunque la guardada apunte a otra parte (y sin consultarla); VARIAS
//     ⇒ la guardada solo si sigue siendo suya, si no ""—.
//   - Contexto sin identidad o con UserID vacío ⇒ domain.ErrInvalidInput (400, no 401).
//   - Un fallo leyendo las membresías o la empresa activa se propaga (no degrada a «ninguna»).
//
// R-U18: para toda forma de estar (cero, una, una con guardada ajena, varias sin elegir,
// varias con elegida, varias con elegida que ya no es suya), el activeID de este método y el
// tenant del Context Token que emite ExchangeService.Exchange son EL MISMO valor.
func (s *ActiveTenantService) TenantsOfCaller(ctx context.Context) ([]domain.UserTenant, string, error) {
	c, ok := s.caller.Caller(ctx)
	if !ok || c.UserID == "" {
		return nil, "", fmt.Errorf("%w: el contexto no acredita a nadie", domain.ErrInvalidInput)
	}

	tenants, err := s.members.UserTenants(ctx, c.UserID)
	if err != nil {
		return nil, "", err
	}
	// Cero empresas ⇒ lista VACÍA y no nil. El contrato lo promete aquí y no lo deja al
	// adaptador: es el estado de quien acaba de registrarse (D-056.12), el que la consola
	// necesita distinguir de «dos empresas y ninguna elegida» —el Context Token de los dos es
	// el MISMO—, y un `null` en el cable lo rompería. (El viejo devolvía lo que diera el
	// repositorio; sus dos adaptadores ya devuelven vacía, así que la conducta no cambia.)
	if tenants == nil {
		tenants = []domain.UserTenant{}
	}

	ids := make([]string, 0, len(tenants))
	for _, t := range tenants {
		ids = append(ids, t.ID)
	}
	// 🔴 EL activeID SE CALCULA CON LA MISMA FUNCIÓN QUE USA EL CANJE (effectiveTenant, era
	// tenantEfectivo), y no leyendo la fila guardada. Es la mitad que impide que el selector y
	// el token discrepen (R-U18): con UNA sola membresía manda la membresía y la fila guardada
	// ni se mira, así que devolver la fila cruda marcaría la casilla equivocada sobre un token
	// que sí va acotado a la otra. Un selector que miente sobre con qué empresa estás operando
	// es peor que no tener selector.
	active, err := effectiveTenant(ids, func() (string, bool, error) {
		return s.active.ActiveTenantOf(ctx, c.UserID)
	})
	if err != nil {
		return nil, "", err
	}
	return tenants, active, nil
}
