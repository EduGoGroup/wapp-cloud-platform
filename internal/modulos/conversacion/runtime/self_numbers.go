// Porta internal/flujos/runtime/self_numbers.go @ e0159171

package runtime

import (
	"context"
	"database/sql"
	"errors"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// ErrSelfNumbersNoKeyProvider lo devuelve IsSelfNumber si el componente se construyó sin
// KeyProvider: sin la clave del índice no hay índice ciego que calcular y la pregunta NO se puede
// responder. Se devuelve como ERROR (no como un false mudo) para que el llamante lo distinga de
// un «no es número propio» legítimo y lo loguee; la decisión conservadora hacia procesar la toma
// la guarda anti-self-loop del runtime, que es quien conoce la política de no-regresión. Se
// inspecciona con errors.Is.
//
// En el viejo se llamaba ErrSelfNumbersSinKeyProvider (05 E-11); el texto no cambia.
var ErrSelfNumbersNoKeyProvider = errors.New("self_numbers: sin KeyProvider no se puede calcular el índice ciego")

// PostgresSelfNumbers implementa SelfNumberChecker resolviendo EN SQL, y por ÍNDICE CIEGO, si un
// número es propio del tenant (Plan 020 · T2; migrado al índice ciego por el Plan 046 · T4.1).
// Aislamiento estricto por tenant (INV-8): la consulta filtra por tenant_id, así los números de
// un tenant nunca cruzan a otro (y el propio índice ya va salado con el tenant_id, ver
// crypto.KeyProvider.BlindIndex).
//
// 🔒 POR QUÉ ÍNDICE CIEGO Y POR QUÉ PREDICADO EN VEZ DE LISTA (Plan 046 · T4.1). Hasta la T4.1
// esto era un listador: traía a memoria TODOS los números en claro del tenant y el llamante los
// recorría comparando strings. Eso tenía dos costes que el plan vino a eliminar:
//
//   - la columna en claro self_pn dejó de existir (la 0070 la borró): el número vive cifrado en
//     self_pn_enc/self_pn_dek/self_pn_kek_id, y lo BUSCABLE es self_pn_bidx. Con la lista habría
//     que DESCIFRAR N números por cada entrante solo para tirarlos; con el predicado no se
//     descifra NADA: el HMAC del remitente se compara contra el HMAC guardado;
//   - la lista de teléfonos del tenant DEJA DE VIAJAR A RAM. No es un efecto colateral agradable:
//     es el espíritu del Plan 046 (menos PII en memoria, menos superficie en un volcado, en un log
//     accidental o en un pánico con traza). Lo único que cruza el proceso es UN booleano.
//
// 🔴 EL KeyProvider TIENE QUE SER EL MISMO QUE ESCRIBE fleet_sessions (trampa T-3 de la fase). El
// índice que aquí se calcula solo casa con el guardado si los dos salen de la MISMA clave de
// índice. Con otro KeyProvider ningún número casa jamás y la guarda queda MUDA: deja de bloquear
// sin un solo error. Por eso la clave del índice es estable de por vida y no rota con la KEK.
//
// Consciente del PERFIL (Plan 020 · T1+T2, migrado al eje profile por el Plan 046 · T1.1): las
// sesiones PASIVAS NO bloquean. Una pasiva nunca auto-responde (el runtime la corta antes del
// motor), así que un mensaje que llega DESDE ese número no puede realimentar un bucle; bloquearlo
// solo impediría que una sesión activa atienda al número personal del mismo tenant. El predicado
// es profile <> 'passive' (y no profile = 'active') a propósito: si mañana existe un tercer
// perfil, sus números SIGUEN bloqueando por defecto (no sabemos si auto-responde ⇒ conservador
// hacia bloquear). El límite de auto-respuestas por conversación queda como red adicional.
//
// 🔴 No se «normaliza» el <> a un =: invertir el predicado cambia hacia dónde falla la guarda ante
// un valor desconocido, que es justo lo que este párrafo protege.
//
// ⚠️ Y NO se unifica este predicado con el de PostgresTenantResolver, que agrega con
// bool_or(profile <> 'active'). Se PARECEN y son cosas distintas: allí la pregunta es «¿el perfil
// EFECTIVO de ESTA sesión repartida entre edges es activo?» y falla hacia PASIVA (no
// auto-responder); aquí es «¿este NÚMERO puede cerrar un bucle?» y falla hacia BLOQUEAR.
// Fusionarlos rompería una de las dos.
//
// La decisión es POR NÚMERO, no por fila. Un mismo número puede aparecer en varias filas —otro
// edge_id, o la fila que dejó un emparejamiento anterior— y con un filtro por fila bastaba UNA
// fila activa para bloquear el número aunque la sesión VIVA fuese pasiva: marcarla pasiva desde
// la consola no surtía efecto y el bot no podía atender al teléfono personal. Un número bloquea
// solo si ALGUNA de sus sesiones NO retiradas es no-pasiva.
//
// Coste: se invoca UNA vez por entrante. Es una consulta trivial e indexada por
// (tenant_id, self_pn_bidx), y se acepta SIN caché (correcto siempre, sin invalidación que
// mantener). 🔴 Una caché de números propios volvería a materializar en RAM justo lo que la T4.1
// sacó de ahí; si el volumen lo exigiera, lo cacheable sería el VEREDICTO por índice (no el
// número), con TTL corto.
//
// Solo lee: no escribe ni una fila.
//
// En el rojo no lleva campos. El verde le pone dos: el *sql.DB y el crypto.KeyProvider.
type PostgresSelfNumbers struct{}

// NewPostgresSelfNumbers construye el predicado sobre el pool y el KeyProvider dados. No toca la
// base ni valida sus argumentos.
//
// El KeyProvider es OBLIGATORIO en producción —y tiene que ser el mismo con que la flota escribe
// self_pn_bidx (T-3)—: sin él no hay índice que comparar (ver ErrSelfNumbersNoKeyProvider). Se
// pide por parámetro, y no se deriva de un singleton, porque el llavero es una dependencia
// explícita, no un ambiente. Un kp nil no falla aquí: falla cada IsSelfNumber.
func NewPostgresSelfNumbers(db *sql.DB, kp crypto.KeyProvider) *PostgresSelfNumbers {
	panic(pendiente.Implementar("runtime.NewPostgresSelfNumbers"))
}

// IsSelfNumber responde si el número YA NORMALIZADO pertenece a alguna sesión del tenant que NO
// está retirada y NO es pasiva.
//
// En este orden:
//
//  1. normalizedNumber vacío → (false, nil) sin ir a la base ni mirar el KeyProvider. No es un
//     error: es el entrante sin número de remitente, que el llamante ya filtra; aquí solo se blinda;
//  2. sin KeyProvider → (false, ErrSelfNumbersNoKeyProvider) sin ir a la base;
//  3. UNA consulta con dos argumentos, tenantID y el índice ciego kp.BlindIndex(tenantID,
//     normalizedNumber). El número en claro NO viaja a la base.
//
// Y de la consulta:
//
//   - alguna sesión del tenant con ese índice, no retirada y de perfil distinto de 'passive' →
//     (true, nil);
//   - ninguna fila casa, o todas las que casan son pasivas o están retiradas → (false, nil). El
//     agregado sobre cero filas da NULL, que se lee como false; y si la consulta no devolviera
//     fila alguna (inalcanzable con un agregado sin GROUP BY, pero contemplado) también es
//     (false, nil), no un error espurio que apague la guarda entera;
//   - fallo de la base (o contexto cancelado) → (false, error) envuelto con %w tras el prefijo
//     literal «self_numbers: consulta fleet_sessions: ». El mensaje NO embebe el número ni el
//     índice (higiene de PII): el índice no es reversible, pero sí es un identificador estable de
//     una persona y no tiene por qué acabar en un log de errores.
//
// ⚠️ El parámetro DEBE venir normalizado —la forma E.164 canónica del dominio de contactos: solo
// dígitos, sin «+», espacios, guiones ni paréntesis— y aquí NO se vuelve a normalizar (RT-7).
// crypto.KeyProvider.BlindIndex tampoco normaliza: es un HMAC sobre los bytes que recibe, así que
// «+57 300» y «57300» dan índices distintos y el mismo teléfono dejaría de casar consigo mismo.
// Normalizar es responsabilidad del llamante porque la forma canónica es la del dominio de
// contactos, no la de la criptografía. Quien escribe el índice normaliza con la MISMA función: esa
// simetría es todo el contrato.
//
// ⚠️ El filtro de vida es state <> 'loggedout', NO state = 'online'. Son cosas distintas (0029):
// 'offline' es el stream CloudLink caído y RECUPERABLE —el socket de WhatsApp sigue vivo y la
// sesión auto-responde en cuanto reconecta, drenando el outbox del Plan 027—, mientras que
// 'loggedout' es terminal (WhatsApp cerró el dispositivo; no vuelve sin re-emparejar). Estrechar
// esto a 'online' dejaría de bloquear números que SÍ auto-responden y reabriría justo el bucle
// sesión↔sesión que esta guarda existe para cerrar.
//
// Una sesión sin número conocido (self_pn_bidx NULL) no casa con ningún número: el argumento es
// siempre un HMAC en hexadecimal no vacío, y NULL = x nunca es verdadero.
//
// El SQL lo prueba runtimehelpertest.ContratoSelfNumbers contra Postgres (test/procesos).
func (r *PostgresSelfNumbers) IsSelfNumber(ctx context.Context, tenantID, normalizedNumber string) (bool, error) {
	panic(pendiente.Implementar("runtime.PostgresSelfNumbers.IsSelfNumber"))
}
