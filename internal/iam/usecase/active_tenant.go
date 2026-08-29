package usecase

// active_tenant.go — LA ELECCIÓN DE EMPRESA DE QUIEN PERTENECE A VARIAS
// (Plan 047 · Ola 5 · T5.1, D-047.14).
//
// Este servicio tiene UNA regla y conviene decirla antes de leer el código:
// elegir una empresa no da acceso a ella. Comprueba la membresía al ESCRIBIR
// —porque guardar una preferencia hacia una empresa ajena no tendría sentido— y
// esa comprobación NO es la que protege: la que protege es la de la LECTURA, que
// vive en ExchangeService.resolveTenant y corre en cada canje. Ver el porqué
// completo en la cabecera de la migración 0086 y en out.ActiveTenantRepo.

import (
	"context"
	"errors"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/out"
)

// ActiveTenantService implementa in.ActiveTenantSelector.
type ActiveTenantService struct {
	caller  in.CallerResolver
	members out.MembershipRepo
	active  out.ActiveTenantRepo
}

// compile-time: ActiveTenantService satisface los DOS puertos de entrada de este
// plano — el que ESCRIBE la elección y el que LEE entre qué se puede elegir.
//
// Son dos puertos y un solo servicio porque comparten exactamente las tres
// dependencias y la regla: quién llama, de qué es miembro, y qué eligió. Partirlo
// en dos servicios duplicaría el cableado para no separar nada.
var (
	_ in.ActiveTenantSelector = (*ActiveTenantService)(nil)
	_ in.TenantLister         = (*ActiveTenantService)(nil)
)

// NewActiveTenantService construye el servicio. Las tres dependencias son
// estructurales y un nil en cualquiera es error de cableado (fail-fast al
// arrancar), igual que en NewRedeemService: sin resolver quién llama no se sabe a
// nombre de quién se guarda, sin membresías no se puede comprobar nada, y sin
// repositorio no hay dónde guardar.
func NewActiveTenantService(caller in.CallerResolver, members out.MembershipRepo, active out.ActiveTenantRepo) (*ActiveTenantService, error) {
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

// SelectActiveTenant implementa in.ActiveTenantSelector.
//
// 🔴 SE EXIGE EL SUJETO Y NO SE EXIGE LA EMPRESA DEL TOKEN, exactamente como en
// RedeemService.RedeemInvitation y por la misma familia de razones: quien llega
// aquí puede traer un Context Token SIN empresa (dos membresías y ninguna
// elegida ⇒ token sin tenant y sin grants), así que `tenantOf` lo rechazaría con
// 403 — a todos los que necesitan este endpoint, siempre.
//
// 🔴 EL 404 DE QUIEN NO ES MIEMBRO NO ES UN ERROR DE CORTESÍA: es anti-oráculo.
// Si «no eres miembro de esa empresa» y «esa empresa no existe» tuvieran
// respuestas distintas, cualquiera con un token válido podría sondear UUIDs y
// levantar el censo de empresas de la plataforma. Sale por domain.ErrNotFound, y
// el transporte lo traduce con writeDomainError al MISMO cuerpo genérico
// («recurso no encontrado») con el que el resto del módulo contesta al recurso
// ajeno — que es la razón por la que ese mapeo existe (ver su comentario en
// transport/http/http.go: «incluye el recurso de OTRA empresa […] aquí no se
// puede convertir en 403 sin confirmar que ese rol o esa persona existen fuera»).
func (s *ActiveTenantService) SelectActiveTenant(ctx context.Context, tenantID string) error {
	c, ok := s.caller.Caller(ctx)
	if !ok || c.UserID == "" {
		// Es 400 y no 401: a este método solo se llega DETRÁS de Authenticate,
		// así que un contexto sin identidad es un error de cableado del
		// servidor, no una credencial que falte. Mismo criterio que RedeemService.
		return fmt.Errorf("%w: el contexto no acredita a nadie", domain.ErrInvalidInput)
	}
	if tenantID == "" {
		return fmt.Errorf("%w: falta la empresa", domain.ErrInvalidInput)
	}

	tenants, err := s.members.TenantsOfUser(ctx, c.UserID)
	if err != nil {
		return err
	}
	if !esMiembro(tenants, tenantID) {
		// Sin `fmt.Errorf` con contexto a propósito: lo que se envuelva aquí
		// acaba en el log, y el log de este proceso no necesita una línea por
		// cada UUID que alguien pruebe.
		return domain.ErrNotFound
	}
	return s.active.SetActiveTenant(ctx, c.UserID, tenantID)
}

// TenantsOfCaller implementa in.TenantLister.
//
// 🔴 EL `activeID` SE CALCULA CON LA MISMA FUNCIÓN QUE USA EL CANJE
// (tenantEfectivo), y no leyendo la fila guardada. Es la mitad que impide que el
// selector y el token discrepen: con UNA sola membresía manda la membresía y la
// fila guardada ni se mira, así que devolver la fila cruda marcaría la casilla
// equivocada sobre un token que sí va acotado a la otra. Un selector que miente
// sobre con qué empresa estás operando es peor que no tener selector.
//
// Cero empresas ⇒ lista VACÍA, activeID vacío y err nil. No es un 404: es el
// estado de quien acaba de registrarse (D-056.12), y es precisamente el caso que
// la consola necesita poder distinguir de «dos empresas y ninguna elegida» —
// porque el Context Token de los dos es el MISMO.
func (s *ActiveTenantService) TenantsOfCaller(ctx context.Context) ([]domain.UserTenant, string, error) {
	c, ok := s.caller.Caller(ctx)
	if !ok || c.UserID == "" {
		return nil, "", fmt.Errorf("%w: el contexto no acredita a nadie", domain.ErrInvalidInput)
	}

	tenants, err := s.members.UserTenants(ctx, c.UserID)
	if err != nil {
		return nil, "", err
	}

	ids := make([]string, 0, len(tenants))
	for _, t := range tenants {
		ids = append(ids, t.ID)
	}
	activo, err := tenantEfectivo(ids, func() (string, bool, error) {
		return s.active.ActiveTenantOf(ctx, c.UserID)
	})
	if err != nil {
		return nil, "", err
	}
	return tenants, activo, nil
}

// tenantEfectivo decide QUÉ empresa llevará el próximo Context Token de alguien,
// dadas sus membresías. Es LA regla del multi-empresa, y vive aquí sola porque
// tiene DOS consumidores que no pueden discrepar: el canje, que emite el token
// (ExchangeService.resolveTenant), y el listado, que marca la opción del selector
// (TenantsOfCaller). Con dos copias, un día marcarían cosas distintas y el
// síntoma sería «la consola dice que estoy en A y no me deja hacer nada en A».
//
// 🔴 `activa` ES UNA FUNCIÓN Y NO UN VALOR, Y ESA FIRMA ES EL PUNTO. Solo la rama
// de «varias» la invoca, así que los casos de CERO y UNA membresía no pagan la
// consulta — ni pueden pagarla por descuido, porque quien edite esto no tiene el
// dato a mano, tiene que ir a buscarlo. Es la regresión de rango cero convertida
// en una firma: el camino que hoy corre en producción para todo el mundo (una
// membresía) no toca la tabla nueva.
//
// Las TRES ramas, y por qué cada una:
//
//   - CERO — empresa vacía y sin error (D-056.12). El token sale sin tenant y sin
//     un solo grant: es el estado «en espera», se entra y no se puede hacer nada.
//   - UNA — esa, sin consultar nada. Manda la membresía, no la preferencia: una
//     fila guardada que apunte a otra parte es basura de una baja anterior, y
//     obedecerla dejaría a esa persona sin empresa teniendo una.
//   - VARIAS — la ACTIVA, y solo si sigue siendo suya. Si no hay o ya no es suya,
//     vacía: elegir la primera en silencio está PROHIBIDO.
//
// 🔴 Un error de `activa` SUBE. No se degrada a «no ha elegido»: quien pertenece
// a dos empresas no puede acabar operando sin ninguna —sin poder hacer nada, y
// pareciendo que nunca eligió— porque la base tuvo un mal segundo.
func tenantEfectivo(tenants []string, activa func() (string, bool, error)) (string, error) {
	switch len(tenants) {
	case 0:
		return "", nil
	case 1:
		return tenants[0], nil
	default:
		activo, hay, err := activa()
		if err != nil {
			return "", err
		}
		if !hay || !esMiembro(tenants, activo) {
			return "", nil
		}
		return activo, nil
	}
}

// esMiembro reporta si tenantID está entre las membresías dadas.
//
// 🔴 ES LA MISMA FUNCIÓN QUE USA EL CANJE, Y ESO ES EL PUNTO. La comprobación de
// la ESCRITURA (aquí) y la de la LECTURA (ExchangeService.resolveTenant) tienen
// que dar el mismo veredicto sobre la misma lista; con dos implementaciones, un
// día darían dos. Recibe la lista YA LEÍDA en vez de un repositorio: así el canje
// la aplica sobre los tenants que acaba de traer, sin pagar una segunda consulta,
// y esta función no puede introducir una.
func esMiembro(tenants []string, tenantID string) bool {
	for _, t := range tenants {
		if t == tenantID {
			return true
		}
	}
	return false
}
