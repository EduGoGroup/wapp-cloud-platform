package out

// active_tenant.go — EL PUERTO DE LA EMPRESA ACTIVA (Plan 047 · Ola 5 · T5.1,
// D-047.14). Tabla public.user_active_tenant (migración 0086).

import "context"

// ActiveTenantRepo guarda y lee la empresa ACTIVA de una persona: la que el
// canje usa cuando esa persona pertenece a VARIAS y hay que elegir una.
//
// 🔴 ESTE PUERTO NO AUTORIZA NADA, Y LA FIRMA NO PUEDE DECIRLO SOLA. Lo que
// devuelve `ActiveTenantOf` es una PREFERENCIA guardada, no un permiso: quien la
// lea tiene que contrastarla contra las membresías VIVAS antes de acotar un token
// a esa empresa. Si a alguien se le retira la membresía, esta fila sobrevive y
// deja de valer sola — nadie la borra, y ese es el diseño: una revocación que
// dependiera de acordarse de limpiar aquí se olvidaría el día que naciera una
// segunda vía de baja. Ver usecase.ExchangeService.resolveTenant, que hace la
// comprobación con los tenants que YA tiene en la mano (cuesta cero consultas
// extra) y el comentario de la 0086.
//
// ⚠️ NO tiene método de BORRADO, y es deliberado. No hace falta para revocar
// —eso lo hace la comprobación de lectura— y tenerlo invitaría a construir la
// revocación sobre él, que es justo la que se olvida. Lo único que la vida de una
// fila necesita está cubierto: se reemplaza al elegir otra empresa, y desaparece
// con su empresa (ON DELETE CASCADE).
type ActiveTenantRepo interface {
	// ActiveTenantOf devuelve la empresa activa guardada para userID.
	//
	// ok=false significa «no hay ninguna guardada», y NO es un error: es el
	// estado de toda persona que todavía no ha elegido. Un error de verdad
	// (la base caída) sale por err y NO se confunde con la ausencia: quien
	// canjea no debe pasar a «token sin empresa» porque Postgres no contestó.
	ActiveTenantOf(ctx context.Context, userID string) (tenantID string, ok bool, err error)
	// SetActiveTenant guarda la empresa activa de userID, REEMPLAZANDO la
	// anterior si la había. Es idempotente: repetir la misma elección no
	// duplica ni falla (la tabla tiene PK por user_id).
	//
	// 🔴 NO comprueba la membresía: eso es una decisión de negocio y vive en el
	// usecase, que es quien tiene el CallerResolver y el MembershipRepo. Un
	// adaptador que la comprobara por su cuenta dejaría la regla escrita en dos
	// sitios, y el día que discreparan ganaría el que nadie está mirando.
	SetActiveTenant(ctx context.Context, userID, tenantID string) error
}
