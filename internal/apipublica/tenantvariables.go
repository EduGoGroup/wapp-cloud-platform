// Porta internal/publicapi/tenantvariables.go @ ed60c24 (197 líneas) y su registro
// (internal/publicapi/publicapi.go @ ed60c24, registerTenantVariables, líneas 855-885).
//
// tenantvariables.go — LAS VARIABLES DE EMPRESA DEL TENANT (Plan 041, D-041.1; mapa §2.7, G11 y
// G12): GET y PUT de /api/v1/tenant-variables. El dominio vive en
// internal/modulos/solicitudes/tenantvars; aquí solo se abre la puerta HTTP.
//
// 🔴 wApp NO INTERPRETA NI CLAVES NI VALORES (D-041.1): no se valida que `moneda` sea una moneda,
// no se tipa el valor y no hay lista blanca. Lo único que se comprueba es la FORMA del
// transporte: que sea JSON, que la clave no venga vacía y los techos de tamaño.
//
// SIN gate de feature, a diferencia de la bandeja: esto es CAPA TÉCNICA (ADR-0035), no una
// capacidad que se venda. Ponerle RequireFeature dejaría a un tenant del plan más simple sin
// poder decir cómo se llama su moneda.
//
// En el rojo solo existían el puerto, TenantVariablesDeps y MountTenantVariables; los handlers y
// sus auxiliares nacieron con el verde (05 E-4, P6).

package apipublica

import (
	"context"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/tenantvars"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// TenantVariableStore es el puerto MÍNIMO de las variables de empresa que la cara consume. Lo
// satisfacen *tenantvars.Postgres y *tenantvars.MemoryStore del módulo solicitudes NUEVO (es la
// misma forma que tenantvars.Store).
//
// TODAS las operaciones van acotadas al tenant (INV-8): el tenant es un ARGUMENTO que sale del
// token, nunca del cuerpo ni de un parámetro.
type TenantVariableStore interface {
	// List devuelve las variables de tenantID (vacío y sin error si no tiene ninguna).
	List(ctx context.Context, tenantID string) ([]tenantvars.Variable, error)
	// Replace deja el conjunto de tenantID EXACTAMENTE igual a vars: lo que no venga, se borra.
	Replace(ctx context.Context, tenantID string, vars map[string]string) error
}

// TenantVariablesDeps es lo que G11 y G12 necesitan. En la cara vieja eran los campos
// TenantVariables y DBTimeout de publicapi.Deps.
type TenantVariablesDeps struct {
	// TenantVariables guarda y lee las variables. nil ⇒ G11 y G12 no se montan.
	TenantVariables TenantVariableStore
	// DBTimeout es el plazo de la LECTURA de G11, cableado desde config.PublicAPIDBTimeout.
	// <= 0 ⇒ 1,5 s. G12 no lo usa.
	DBTimeout time.Duration
}

// MountTenantVariables registra en c las DOS rutas de /api/v1/tenant-variables, juntas o
// ninguna (T-2: una familia partida daría un 405 falso), solo si d.TenantVariables no es nil.
// Sin almacén las rutas no existen (404 de ruta inexistente, mejor que un 500 a medio camino).
//
//   - G11 "GET /api/v1/tenant-variables": cadena R con permiso "content.read";
//   - G12 "PUT /api/v1/tenant-variables": cadena W con permiso "content.write" y recurso de
//     auditoría "tenant_variables". Se audita la ACCIÓN, jamás claves ni valores.
//
// (Ver Common: 401 sin token; 403 {"error":"permiso denegado"} sin el permiso o con un token sin
// empresa; G11 no deja registro de auditoría y G12 deja exactamente uno.) NINGUNA de las dos
// lleva gate de feature: un tenant sin una sola feature las usa.
//
// El tenant es SIEMPRE el del token (INV-8): no hay parámetro ni campo del cuerpo que lo cambie.
//
// G11 — leer:
//
//   - 200 {"variables":{clave:valor,…},"updated_at":"…"}: las variables VERBATIM y el instante
//     del cambio MÁS reciente del conjunto (UTC, RFC 3339 con segundos);
//   - un tenant sin variables responde 200 {"variables":{}} —el mapa vacío y NO null, sin
//     "updated_at"—, no 404: «no tengo ninguna» es una respuesta, no un fallo;
//   - 🔴 es la ÚNICA ruta de solicitudes con plazo de BD: List va acotada por d.DBTimeout
//     (<= 0 ⇒ 1,5 s) y, si vence, responde 504 {"error":"la lectura de las variables no
//     respondió a tiempo, reintenta"} y deja en k.Log un Warn "lectura a BD vencida: se responde
//     504" con "op"="tenant_variables.list" y "tenant_id";
//   - cualquier otro fallo de List ⇒ 500 {"error":"no se pudieron leer las variables"}, que NO
//     repite el error del puerto.
//
// G12 — reemplazar el conjunto ENTERO (no hace merge por clave): el cuerpo es la foto completa
// y lo que no venga se borra. Es la ÚNICA forma de borrar una variable (no hay DELETE), es lo
// que PUT significa sobre una colección, y el consumidor es una pantalla que tiene el conjunto
// entero a la vista. El precio: dos editores simultáneos se pisan y gana el último, entero.
//
//   - el cuerpo es {"variables":{clave:valor,…}} con valores de CADENA; {"variables":{}} es la
//     intención explícita de dejar al tenant sin variables;
//   - 200 con el conjunto RESULTANTE releído del puerto, en la misma forma que G11;
//   - 413 {"error":"el cuerpo excede el tamaño máximo de 262144 bytes","max_bytes":262144} si
//     el cuerpo pasa de 256 KiB (262144 bytes justos sí entran);
//   - 400 {"error":"el cuerpo debe ser un JSON {\"variables\":{clave:valor}} de cadenas"} si no
//     es JSON, si trae algo detrás del objeto o si un valor no es una cadena;
//   - 400 {"error":"falta el objeto variables (usa {} para dejar el tenant sin variables)"} si
//     no trae `variables` o viene null: se niega a adivinar si el cliente quiso vaciar el
//     conjunto o se equivocó de forma;
//   - 400 {"error":"demasiadas variables"} con más de 500 claves (500 justas sí entran);
//   - 400 {"error":"hay una clave vacía"} y 400 {"error":"hay una clave demasiado larga"} (más
//     de 200 BYTES, no runas: 200 justos sí entran);
//   - 400 {"error":"no se pudo leer el cuerpo"} si la lectura del cuerpo falla;
//   - un cuerpo rechazado NO toca lo guardado: Replace no se llama;
//   - Replace falla ⇒ 500 {"error":"no se pudieron guardar las variables"}; guardó pero la
//     relectura falla ⇒ 500 {"error":"variables guardadas, pero no se pudieron releer"};
//   - 🔴 SIN plazo de BD propio, a propósito: reemplaza el conjunto entero en una transacción,
//     y 1,5 s está calibrado para una lectura. Ni Replace ni la relectura llevan d.DBTimeout.
//
// Lo que G12 NO hace, y es deliberado (comportamiento del viejo, conservado):
//
//   - no normaliza ni recorta claves ni valores: una clave Unicode, con espacios o con
//     mayúsculas se guarda tal cual, y "Moneda" y "moneda" son dos variables;
//   - una clave REPETIDA en el JSON no es un error: gana la última aparición (lo que hace
//     encoding/json con un mapa);
//   - un valor null se guarda como cadena vacía, y un campo de más junto a `variables` se
//     ignora.
//
// Defensa que los tokens de sharedjwt no alcanzan (RequirePermission corta antes): una
// identidad sin empresa que llegara a un handler recibiría 401 {"error":"autenticación
// requerida"}.
//
// Fallo de cableado: k.MW nil con el almacén presente hace panic AL MONTAR (ver Common).
func MountTenantVariables(c *Cara, k Common, d TenantVariablesDeps) {
	panic(pendiente.Implementar("apipublica.MountTenantVariables"))
}
