// Porta internal/iam/usecase/memberships.go @ 9a77307

package usecase

import (
	"context"

	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/in"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/iam/ports/out"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
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
type MembershipService struct{}

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
	panic(pendiente.Implementar("usecase.NewMembershipService"))
}

// ListMembers implementa in.MembershipAdmin: los miembros de la empresa del CONTEXTO
// (out.MembershipRepo.MembersOf con ese tenant y ningún otro). No recibe tenant: no hay dónde
// colar una empresa ajena. No sale a identity. Una lista vacía no es error.
func (s *MembershipService) ListMembers(ctx context.Context) ([]domain.Membership, error) {
	panic(pendiente.Implementar("usecase.MembershipService.ListMembers"))
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
	panic(pendiente.Implementar("usecase.MembershipService.AddMember"))
}

// RemoveMember implementa in.MembershipAdmin: da de baja a la persona SOLO de la empresa del
// CONTEXTO (el DELETE lleva ese tenant): pasar el UUID de alguien de otra empresa es un no-op sin
// error. Idempotente. NO revoca SystemWappBFF en identity (decisión de producto, no simetría
// olvidada).
func (s *MembershipService) RemoveMember(ctx context.Context, input in.MembershipInput) error {
	panic(pendiente.Implementar("usecase.MembershipService.RemoveMember"))
}
