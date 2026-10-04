// Porta internal/iam/ports/out/canje.go @ 9a77307

package out

// canje.go — EL PUERTO DE SALIDA DEL CANJE (Plan 047 · Ola A · T-A3/T-A4/T-A5).
// El fichero conserva el nombre del viejo (tareas F2 T2.6); el tipo ya estaba en inglés.

import "context"

// InvitationRedeemRepo canjea una invitación: convierte un digest y un usuario
// en una membresía, una invitación marcada y una solicitud huérfana cerrada.
//
// 🔴 POR QUÉ ES UN SOLO MÉTODO Y NO CUATRO, que es la pregunta que se hace quien
// venga a añadir aquí un `FindByTokenHash`. Los cuatro pasos del canje —validar
// la invitación, dar el acceso, marcarla usada y cerrar la solicitud que el
// invitado dejó en la bandeja del operador— tienen que ser ATÓMICOS entre sí, y
// una transacción no cabe en un puerto puro (este paquete es context + tipos de
// dominio, ver la cabecera de repos.go). Partirlo en cuatro métodos obligaría a
// que el usecase orquestara la transacción, y para eso tendría que conocer
// `*sql.Tx`: el módulo entero dejaría de tener frontera.
//
// Así que la transacción vive DENTRO del adaptador, y este puerto expresa la
// única unidad que el usecase necesita pedir. Es la MISMA lección que dejó
// escrita MembershipService sobre GrantTenantAccess, resuelta al otro lado: allí
// el punto de reunión tuvo que estar en el adaptador porque un paso (marcar el
// access-request del OPERADOR) era ajeno al alta; aquí los cuatro pasos son del
// canje, así que la unidad cabe entera detrás de una firma pura.
//
// 🔴 RECIBE EL DIGEST, NO EL TOKEN. El texto en claro que la persona pegó desde
// WhatsApp muere en el usecase, que es quien llama a domain.HashInvitationToken.
// Un puerto que aceptara el token lo haría viajar hasta la capa que compone SQL
// y escribe logs de consulta, que es exactamente donde no debe estar el único
// secreto de este flujo.
//
// La suite que toda implementación tiene que pasar es
// outhelpertest.ContratoInvitationRedeemRepo.
type InvitationRedeemRepo interface {
	// Redeem canjea la invitación cuyo token tiene ese digest a nombre de userID.
	//
	// Los cuatro pasos, todo o nada: (1) leer la invitación y juzgarla con
	// domain.EvaluateRedemption; (2) dar el acceso en la empresa DE LA INVITACIÓN
	// —nunca otra: el puerto no recibe ninguna— con la misma guarda que
	// MembershipRepo.Add (una empresa por persona salvo que la de destino tenga
	// multi_empresa; resolver caído ⇒ rechazo) y, si la invitación trae rol, ese
	// rol acotado a la misma empresa; (3) marcarla canjeada por userID; (4) pasar a
	// 'approved' la solicitud pendiente que userID tuviera en la bandeja del
	// operador (sin solicitud pendiente no es un fallo).
	//
	// Devuelve, y el llamante NO debe reordenar estos desenlaces:
	//   - nil                         → canjeada; la membresía existe.
	//   - domain.ErrNotFound          → no hay invitación con ese digest.
	//   - domain.ErrInvitationExpired → existía y ya había vencido.
	//   - domain.ErrConflict          → o la invitación ya era terminal (usada o
	//     revocada), o esa persona ya es miembro de OTRA empresa y la de la
	//     invitación no tiene multi_empresa (con el mismo texto que el 409 de
	//     MembershipRepo.Add). Los dos son conflicto de estado y los dos dejan la
	//     invitación INTACTA y usable.
	//
	// Ningún rechazo deja rastro escrito: ni membresía, ni rol, ni marca en la
	// invitación, ni la solicitud cerrada.
	//
	// ⚠️ Los dos primeros desenlaces tienen que costar lo mismo en tiempo: es el
	// requisito anti-oráculo de T-A3 y se sostiene en que la implementación hace
	// UNA sola consulta para ambos. Quien la reimplemente con una segunda
	// consulta en la rama de la ausencia habrá roto el requisito sin romper
	// ninguna firma — por eso hay un candado sobre el AST que lo cuenta (el de
	// infra/postgres).
	Redeem(ctx context.Context, tokenHash []byte, userID string) error
}
