package in

// active_tenant.go — EL PUERTO DE ENTRADA DE LA ELECCIÓN DE EMPRESA
// (Plan 047 · Ola 5 · T5.1, D-047.14).

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
)

// ActiveTenantSelector fija la empresa con la que opera quien llama, cuando
// pertenece a varias.
//
// 🔴 ES LA PUERTA APARTE QUE PERMITE QUE INV-8 SIGA INTACTO, y por eso existe
// como puerto propio en vez de como un campo más en ExchangeInput. El canje NO
// acepta el tenant del cliente y no lo va a aceptar: los tres consumidores web
// re-canjean SOLOS cada ~13 min (DefaultAccessTTL = 15 min, usecase/config.go),
// sin nadie delante, así que un tenant que viajara en el canje viajaría en cada
// refresco desatendido. Aquí, en cambio, la empresa viaja UNA vez, en una acción
// deliberada de una persona, y lo que el canje lee después está en el servidor.
//
// ⚠️ Y AQUÍ SÍ ENTRA UN TENANT DESDE EL CUERPO, que es lo contrario de lo que
// hace el resto de este paquete —donde ningún Input tiene campo TenantID porque
// el tenant sale del Caller (INV-04)—. No es una excepción a la regla, es su
// razón de ser: este endpoint existe EXACTAMENTE para elegir empresa, así que la
// empresa no puede salir del token (el token todavía no la tiene, o tiene la
// otra). Lo que sustituye a la regla es la COMPROBACIÓN: el usecase exige que
// quien llama sea miembro de la empresa que pide, y si no lo es responde como si
// no existiera.
//
// ⚠️ EL CALLER PUEDE NO TRAER EMPRESA, igual que en InvitationRedeemer y por una
// razón parecida: quien tiene dos membresías y ninguna elegida recibe hoy un
// Context Token SIN empresa y sin un solo grant. Un usecase que exigiera
// `Caller.TenantID != ""` —como hace MembershipService.tenantOf, y hace bien—
// rechazaría con 403 a exactamente todas las personas para las que existe este
// endpoint.
type ActiveTenantSelector interface {
	// SelectActiveTenant fija tenantID como empresa activa del Caller.
	//
	// nil ⇒ guardada; el SIGUIENTE canje la usará. Este método NO acuña un
	// token: el que quien llama tiene en la mano se emitió antes y sigue
	// diciendo lo que decía. La consola refresca su sesión por el camino que ya
	// existe (POST /api/v1/auth/exchange).
	//
	// Los desenlaces salen como errores tipados del dominio y el transporte los
	// traduce: ErrInvalidInput (400) si falta el tenant o el contexto no
	// acredita a nadie, y ErrNotFound (404) si quien llama NO es miembro de esa
	// empresa — el mismo 404 con el que el resto del módulo contesta al recurso
	// ajeno, para no confirmar qué empresas existen.
	SelectActiveTenant(ctx context.Context, tenantID string) error
}

// TenantLister devuelve las empresas ENTRE LAS QUE quien llama puede elegir.
//
// 🔴 ES LA PIEZA SIN LA CUAL EL SELECTOR NO SE PUEDE PINTAR, y el hueco que
// tapaba era peor que «falta un listado»: el Context Token de quien tiene CERO
// empresas y el de quien tiene DOS y no ha elegido son IDÉNTICOS —los dos sin
// tenant y sin un solo grant—, así que la consola no podía ni distinguir si tocaba
// pintar la pantalla de espera («tu acceso está en revisión») o el selector
// («¿con cuál entras?»). Con este puerto, la lista vacía y la lista de dos
// separan los dos casos sin que el token tenga que cambiar.
//
// Va SEPARADO de ActiveTenantSelector aunque hoy los satisfaga el mismo servicio:
// uno LEE y el otro ESCRIBE, y un consumidor que solo necesite pintar el selector
// no tiene por qué recibir de paso la capacidad de cambiar la empresa activa.
type TenantLister interface {
	// TenantsOfCaller devuelve las empresas del Caller y CUÁL de ellas llevará su
	// PRÓXIMO Context Token.
	//
	// `activeID` vacío significa «ninguna»: o no ha elegido, o lo que eligió ya
	// no es suyo. Es un valor legítimo y no un error — el selector nace sin nada
	// marcado, que es exactamente lo que hay que enseñarle.
	//
	// 🔴 `activeID` NO es «lo que hay guardado en user_active_tenant»: es lo que
	// el canje resolvería AHORA MISMO, calculado con la misma función
	// (usecase.tenantEfectivo). La diferencia se ve justo donde importa: con UNA
	// sola membresía manda la membresía y no la fila guardada, así que devolver
	// la fila cruda marcaría la opción equivocada —o ninguna— sobre un token que
	// sí va acotado. Un selector que discrepe del token es peor que no tenerlo.
	//
	// Cero empresas devuelve lista VACÍA y sin error (D-056.12), nunca
	// ErrNotFound: no pertenecer a ninguna empresa todavía es un estado del
	// producto, no un fallo.
	TenantsOfCaller(ctx context.Context) (tenants []domain.UserTenant, activeID string, err error)
}
