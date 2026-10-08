// Porta internal/publicapi/crmcallback.go @ ed60c24 (310 líneas) y su registro
// (internal/publicapi/publicapi.go @ ed60c24, registerCRMCallback, líneas 1035-1057).
//
// crmcallback.go — LA VUELTA DEL PUENTE CRM (Plan 042 · T4.2, D-042.5; mapa §2.7, G17):
// POST /api/v1/integrations/callback, el verbo `intake.status` del contrato wapp-crm-v1. El
// dominio vive en internal/modulos/solicitudes (integrations, integrations/sigv1 e intakes); aquí
// solo se abre la puerta HTTP.
//
// 🔴 NO PASA POR protect NI POR protectRead, y esa es la diferencia con todo lo demás de la cara
// (FX T-7): el callback no lleva JWT. Quien llama es el puente del cliente, un proceso que no
// tiene usuario ni sesión, y su credencial es la firma HMAC sobre el cuerpo crudo con el secreto
// de firma del puente (D-042.5). Meterlo bajo el middleware de sesión lo dejaría inalcanzable;
// darle un token lo convertiría en un usuario más, que es justo lo que el contrato evita.
//
// El secreto que aquí se lee es el de la firma del puente, del envelope de NEGOCIO de esta
// pieza: vive solo dentro de la verificación y no sale por ninguna puerta ni por ningún log.
//
// En el rojo solo existían los cuatro puertos, CRMCallbackDeps y MountCRMCallback; el handler y
// sus auxiliares nacieron con el verde (05 E-4, P6).

package apipublica

import (
	"context"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// CRMSecretReader entrega el secreto de firma del puente del tenant, para verificar el
// callback. Lo satisface *integrations.Postgres del módulo solicitudes NUEVO.
type CRMSecretReader interface {
	// GetTenantSecret devuelve el secreto de tenantID. found false ⇒ el tenant no tiene
	// integración, o la tiene sin secreto.
	GetTenantSecret(ctx context.Context, tenantID string) (secret string, found bool, err error)
}

// CRMBridgeGate responde si el tenant puede usar el puente CRM: la feature comercial Y la
// integración configurada, encendida y con destino (D-042.8, D-F6-11). Lo satisface
// *integrations.EntitlementsGate — el MISMO gate que decide si se encola la ida, de modo que un
// tenant no puede quedar en el absurdo de recibir la vuelta de algo que no se le mandó.
type CRMBridgeGate interface {
	// Enabled dice si el puente de tenantID está abierto. Un gate cerrado no es un error.
	Enabled(ctx context.Context, tenantID string) (bool, error)
}

// CRMReflector aplica el estado canónico del CRM sobre la solicitud del tenant. Lo satisface
// *intakes.Postgres del módulo solicitudes NUEVO.
type CRMReflector interface {
	// ReflectCRMStatus refleja status en la solicitud intakeID DE tenantID. Found false ⇒ no
	// existe o es de otro tenant, sin distinguir cuál.
	ReflectCRMStatus(ctx context.Context, tenantID, intakeID, status, externalRef string,
		syncedAt time.Time) (intakes.CRMReflection, error)
}

// CRMStatusNotifier avisa al cliente final de que su pedido cambió de estado en el CRM. Es
// OPCIONAL (nil ⇒ no se avisa): el reflejo es la operación y el aviso es su consecuencia.
//
// NO devuelve error, y no es un olvido: es la regla 1 del notificador del Plan 041, que este
// puerto respeta en vez de inventarse otra. Cuando esto corre, el reflejo YA está escrito; un
// error hacia arriba haría que el puente reintentara y volviera a escribir lo mismo para nada,
// y el aviso tampoco se recuperaría por ese camino. Todo fallo se registra donde ocurre y muere
// ahí. Lo satisface *intakes.Notifier.
type CRMStatusNotifier interface {
	// NotifyCRMStatus avisa al cliente de in de que su pedido pasó a crmStatus.
	NotifyCRMStatus(ctx context.Context, tenantID string, in intakes.Intake, crmStatus string)
}

// CRMCallbackDeps es lo que G17 necesita. En la cara vieja eran los campos CRMSecrets, CRMGate,
// CRMReflect y CRMNotify de publicapi.Deps; Now es nuevo (el viejo leía el reloj del proceso).
type CRMCallbackDeps struct {
	// CRMSecrets entrega el secreto con el que se verifica la firma. nil ⇒ G17 no se monta.
	CRMSecrets CRMSecretReader
	// CRMGate combina la feature comercial y la integración encendida. nil ⇒ G17 no se monta.
	CRMGate CRMBridgeGate
	// CRMReflect aplica el estado sobre la solicitud. nil ⇒ G17 no se monta.
	CRMReflect CRMReflector
	// CRMNotify avisa al cliente final del cambio reflejado. OPCIONAL: nil ⇒ G17 se monta y
	// refleja igual, sin avisar. ⚠️ Tiene que ser un nil de interfaz: un puntero nil metido en
	// la interfaz NO es nil, y el aviso se intentaría (el pánico se contiene, ver abajo).
	CRMNotify CRMStatusNotifier
	// Now es el reloj contra el que se mide la ventana anti-replay y con el que se sella el
	// reflejo. nil ⇒ time.Now. Lo inyectan los tests.
	Now func() time.Time
}

// MountCRMCallback registra en c "POST /api/v1/integrations/callback" (G17), solo si
// d.CRMSecrets, d.CRMGate y d.CRMReflect son los tres distintos de nil. Faltando cualquiera la
// ruta NO existe (404 de ruta inexistente): sin secreto no se puede autenticar, sin gate no se
// puede decidir y sin reflector no hay dónde escribir, y un 404 es mejor que un 500 a mitad de
// una puerta de autenticación. d.CRMNotify es opcional y no condiciona el montaje.
//
// 🔴 SIN Authenticate NI RequirePermission (FX T-7): la cadena es SOLO el access-log de k.Log
// (ver Common: una línea por petición, con "method", "path" y "status", y sin "tenant_id",
// porque aquí no hay identidad de token). Por eso este Mount NO exige k.MW: con MW nil monta y
// sirve igual, y un Authorization que venga —válido, inválido o ausente— ni se mira. NO deja
// registro de auditoría en ningún desenlace; k.Auditor no se usa. Con k.Log nil no hay
// access-log ni líneas de diagnóstico, y las respuestas son las mismas.
//
// La credencial ENTERA son tres headers y el cuerpo:
//
//   - "X-Wapp-Tenant": el tenant que dice ser quien llama (se recortan los espacios de los
//     bordes). Es el ÚNICO sitio del que sale el tenant: el cuerpo no lo lleva;
//   - "X-Wapp-Timestamp": segundos Unix en decimal (dígitos ASCII, con signo opcional; se
//     recortan los espacios de los bordes), que deben caer en la ventana de ±300 s —los dos
//     bordes incluidos— alrededor de d.Now();
//   - "X-Wapp-Signature": "v1=" + la firma sigv1 en hex del timestamp y del cuerpo CRUDO, byte
//     a byte, con el secreto del tenant. Se verifica en tiempo constante (sigv1.Verify). El
//     prefijo "v1=" se quita UNA vez si está; el hex a secas también verifica, y en mayúsculas
//     también.
//
// EL ORDEN DE LAS COMPROBACIONES ES LA MITAD DEL DISEÑO. La primera que falla responde:
//
//  1. sin "X-Wapp-Tenant" (o en blanco) ⇒ 401 {"error":"no autenticado"};
//  2. "X-Wapp-Timestamp" ausente, ilegible (vacío, con decimales, con espacios por dentro, con
//     dígitos que no son ASCII, fuera del rango de int64) o fuera de la ventana ⇒ el MISMO 401.
//     Hasta aquí no se ha leído el cuerpo ni consultado nada;
//  3. cuerpo de más de 64 KiB (65536 bytes justos sí entran) ⇒ 413 {"error":"el cuerpo del
//     callback excede el tamaño máximo de 65536 bytes","max_bytes":65536}, sin consultar el
//     secreto. Un fallo al leer el cuerpo responde ese mismo 413;
//  4. GetTenantSecret falla ⇒ 503 {"error":"no se pudo verificar la petición"} y un Warn
//     "callback CRM: no se pudo leer el secreto del tenant" con "tenant" y "error": un fallo de
//     infraestructura NO se disfraza de 401, o el puente acabaría rotando su secreto para nada;
//  5. tenant sin secreto, o firma que no verifica (de otro secreto, de otro timestamp, de otro
//     cuerpo, malformada, vacía, ausente) ⇒ el MISMO 401 {"error":"no autenticado"}. Un tenant
//     sin integración sale por 401 y no por 403: sin secreto es indistinguible de una firma
//     mala, y así no se delata qué tenants tienen puente. TODAS las formas de fallar la
//     autenticación responden lo mismo, sin detalle;
//  6. SOLO con la firma buena se consulta el gate: cerrado ⇒ 403 {"error":"el puente CRM no
//     está activo para este tenant"}. Si Enabled falla, es fail-closed: el mismo 403 y un Warn
//     "callback CRM: no se pudo resolver el puente del tenant". Va DESPUÉS de la firma para que
//     un desconocido no pueda averiguar quién tiene contratado el puente por la diferencia
//     entre 401 y 403;
//  7. el cuerpo no es un `intake.status` válido ⇒ 422 con el motivo (ver abajo); el reflector
//     no se llama;
//  8. ReflectCRMStatus falla ⇒ 500 {"error":"no se pudo aplicar el estado"} y un Error
//     "callback CRM: no se pudo reflejar el estado"; el error del puerto no viaja al cliente;
//  9. la solicitud no existe O es de otro tenant (Found false) ⇒ el MISMO 404
//     {"error":"solicitud no encontrada"}: el callback no sirve de oráculo de identificadores;
//  10. 200 {"changed":<bool>,"crm_status":"<status>","intake_id":"<intake_id>"}, con las claves
//     en ese orden (el alfabético, el del viejo) y los valores tal cual llegaron en el cuerpo.
//
// El 422 (el `additionalProperties:false` y los `required` del schema publicado,
// docs/contracts/wapp-crm-v1/intake.status.schema.json; reintentar el mismo cuerpo dará siempre
// 422). Los motivos, en el orden en que se comprueban:
//
//   - un campo que el contrato no tiene ⇒ `el cuerpo trae un campo que el contrato no admite:
//     "<campo>"` (se nombra el que sobra; "tenant" es el primero que se le ocurre mandar a un
//     autor de puentes);
//   - un campo con otro tipo ⇒ "el campo <campo> tiene un tipo que el contrato no admite"; un
//     cuerpo que es JSON pero no un objeto (una lista, una cadena) da ese mismo motivo con el
//     nombre VACÍO («el campo  tiene…», con sus dos espacios: es el texto del viejo);
//   - no es JSON, está vacío o está truncado ⇒ "el cuerpo no es un intake.status válido";
//   - un `null` o un `{}` no fallan al decodificar: caen en la primera regla de abajo;
//   - `contract_version debe ser "1"` · `verb debe ser "intake.status"` · "intake_id es
//     obligatorio" (vacío o solo espacios) · "status es obligatorio" · "status debe ser uno de:
//     paid, preparing, delivered, rejected" (el mensaje NOMBRA los cuatro: quien mapea mal su
//     CRM necesita saber contra qué mapear) · "occurred_at es obligatorio" · "occurred_at debe
//     ser una marca RFC3339".
//
// El reflejo: ReflectCRMStatus recibe el tenant AUTENTICADO del header, el intake_id y el status
// del cuerpo, external_ref recortado de espacios, y como syncedAt el instante de d.Now() en UTC
// (NO el occurred_at del cuerpo, que solo se valida).
//
// El aviso al cliente va DESPUÉS de escribir y NO puede cambiar la respuesta:
//
//   - se llama a d.CRMNotify.NotifyCRMStatus(tenant, la solicitud reflejada, status) SOLO si el
//     reflejo CAMBIÓ algo (Changed): un puente con reintentos manda el mismo estado muchas
//     veces y el cliente no puede recibir el mismo mensaje una vez por reintento;
//   - con d.CRMNotify nil no se avisa y la respuesta es la misma;
//   - un PÁNICO del notificador se contiene: la respuesta sigue siendo el 200 y queda un Error
//     "callback CRM: pánico avisando al cliente; el reflejo YA está aplicado" con "tenant",
//     "intake" y "panic".
//
// ⚠️ DIVERGENCIAS con la cara vieja, las dos aceptadas:
//
//   - D-F6-11: con el gate nuevo (integrations.EntitlementsGate exige endpoint y secreto), un
//     callback BIEN FIRMADO de un tenant con el puente encendido pero SIN endpoint_url pasa de
//     200 a 403. (Sin secreto ya era 401 en las dos: no hay con qué verificar la firma.) La
//     diferencia es del gate, no de esta puerta, que le pregunta lo mismo que antes;
//   - la ventana: el viejo aceptaba un timestamp a más de ~292 años en el FUTURO —el caso real
//     es un puente que manda MILISEGUNDOS en vez de segundos—, porque la resta de instantes
//     satura en el mínimo de time.Duration y su negación desborda y sigue siendo negativa
//     (internal/publicapi/crmcallback.go:289-293). Un mensaje así no caducaba nunca. Aquí es
//     401, que es lo que el contrato dice de todo lo que cae fuera de ±300 s.
//
// Rarezas del viejo que se conservan: solo se decodifica el PRIMER valor JSON del cuerpo (lo que
// venga detrás del objeto no se mira, aunque sí está cubierto por la firma); una clave repetida
// toma su última aparición; y, en dos cosas, la frontera es MÁS LAXA que el schema publicado:
// un campo a null cuenta como ausente (`"external_ref":null` pasa) y las claves casan sin
// distinguir mayúsculas (`"STATUS"` vale por `"status"`), que es lo que hace encoding/json.
func MountCRMCallback(c *Cara, k Common, d CRMCallbackDeps) {
	panic(pendiente.Implementar("apipublica.MountCRMCallback"))
}
