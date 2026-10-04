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

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// ActiveTenantService implementa in.ActiveTenantSelector (escribe la elección) e
// in.TenantLister (lee entre qué se puede elegir): comparten las tres dependencias y la regla.
type ActiveTenantService struct{}

// compile-time: ActiveTenantService satisface los DOS puertos de entrada de este plano.
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
	panic(pendiente.Implementar("usecase.NewActiveTenantService"))
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
	panic(pendiente.Implementar("usecase.ActiveTenantService.SelectActiveTenant"))
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
	panic(pendiente.Implementar("usecase.ActiveTenantService.TenantsOfCaller"))
}
