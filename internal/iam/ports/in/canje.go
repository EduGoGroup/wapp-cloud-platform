package in

// canje.go — EL PUERTO DE ENTRADA DEL CANJE (Plan 047 · Ola A · T-A3).

import "context"

// InvitationRedeemer canjea la invitación que trae quien llama, y lo hace a
// nombre de quien llama.
//
// 🔴 SU FIRMA ES EL INVARIANTE INV-04, ESCRITO DE LA ÚNICA FORMA QUE NO SE PUEDE
// OLVIDAR. No recibe `tenantID` ni `userID`: la empresa sale de la FILA de la
// invitación (es la que eligió quien la emitió, y quien canjea no la elige) y la
// persona sale del CONTEXTO de identidad. No hay parámetro por el que colar una
// empresa ajena ni una persona ajena, y un parámetro que no existe no se puede
// dejar de comprobar. Mismo criterio que MembershipAdmin.ListMembers, que
// tampoco recibe tenant y por la misma razón.
//
// Lo único que viaja es el token, porque es lo único que quien canjea aporta.
//
// ⚠️ Y AQUÍ EL CALLER NO TRAE EMPRESA, que es la diferencia con todo lo demás de
// este paquete. Quien canjea acaba de registrarse y todavía no es miembro de
// nada, así que su Context Token va SIN tenant (resolveTenant con cero
// membresías devuelve "" y sin error, D-056.12). Un usecase que exigiera
// `Caller.TenantID != ""` —como hace MembershipService.tenantOf, y hace bien—
// rechazaría con 403 a exactamente todas las personas para las que existe este
// endpoint.
type InvitationRedeemer interface {
	// RedeemInvitation canjea el token dado a nombre del Caller del contexto.
	//
	// nil ⇒ ya es miembro de la empresa de la invitación. Los desenlaces de
	// negocio salen como errores tipados del dominio y el transporte los traduce:
	// ErrNotFound (404), ErrInvitationExpired (410), ErrConflict (409) y
	// ErrInvalidInput (400) para un token vacío.
	RedeemInvitation(ctx context.Context, token string) error
}
