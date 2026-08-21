package runtime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// ErrSelfNumbersSinKeyProvider lo devuelve IsSelfNumber si el componente se
// construyó sin KeyProvider: sin la indexKey no hay índice ciego que calcular y la
// pregunta NO se puede responder. Se devuelve como ERROR (no como "false" mudo)
// para que el llamante lo distinga de un "no es número propio" legítimo y lo
// loguee; la decisión conservadora hacia procesar la toma isSelfLoop, que es quien
// conoce la política de no-regresión. Se inspecciona con errors.Is.
var ErrSelfNumbersSinKeyProvider = errors.New("self_numbers: sin KeyProvider no se puede calcular el índice ciego")

// PostgresSelfNumbers implementa SelfNumberChecker resolviendo EN SQL, y por
// ÍNDICE CIEGO, si un número es propio del tenant (Plan 020 · T2; migrado al bidx
// por el Plan 046 · T4.1). Aislamiento estricto por tenant (INV-8): la consulta
// filtra por tenant_id, así los números de un tenant nunca cruzan a otro (y el
// propio bidx ya va salado con el tenant_id, ver crypto.KeyProvider.BlindIndex).
//
// 🔒 POR QUÉ ÍNDICE CIEGO Y POR QUÉ PREDICADO EN VEZ DE LISTA (Plan 046 · T4.1).
// Hasta la T4.1 esto era un LISTER: traía a memoria TODOS los self_pn en claro del
// tenant y el llamante los recorría comparando strings. Eso tenía dos costes que el
// plan viene a eliminar:
//   - la columna en claro self_pn deja de existir como fuente (queda VACÍA tras la
//     migración de la ola): el número vive cifrado en self_pn_enc/self_pn_dek/
//     self_pn_kek_id, y lo BUSCABLE es self_pn_bidx. Con la lista habría que
//     DESCIFRAR N números por cada entrante solo para tirarlos; con el predicado no
//     se descifra NADA — el HMAC del remitente se compara contra el HMAC guardado.
//   - la lista de teléfonos del tenant DEJA DE VIAJAR A RAM. Eso no es un efecto
//     colateral agradable: es el espíritu del Plan 046 (menos PII en memoria, menos
//     superficie en un core dump, en un log accidental o en un panic con stack).
//     Lo único que cruza el proceso ahora es UN booleano.
//
// Consciente del PERFIL (Plan 020 · T1+T2, migrado al eje profile por el Plan 046 ·
// T1.1): las sesiones PASIVAS NO bloquean — una pasiva nunca auto-responde
// (reactiveBlocked lo corta antes del motor), así que un mensaje que llega DESDE ese
// número no puede realimentar un bucle; bloquearlo solo impediría que una sesión
// activa atienda al número personal del mismo tenant. El predicado es
// profile <> 'passive' (y no profile = 'active') a propósito: si mañana existe un
// tercer perfil, sus números SIGUEN bloqueando por defecto (no sabemos si
// auto-responde ⇒ conservador hacia bloquear, misma convención que "perfil
// desconocido ⇒ bloquea"). El rate-limit por conversación (T0) queda como red de
// contención adicional.
//
// 🔴 No "normalizar" el <> a un =: invertir el predicado cambia hacia dónde falla la
// guarda ante un valor desconocido, que es justo lo que este párrafo protege. La
// columna legada role ya no se consulta aquí (D-046.1).
//
// ⚠️ Y NO unificar este predicado con el de PostgresTenantResolver, que agrega con
// bool_or(profile <> 'active'). Se PARECEN y son cosas distintas: allí la pregunta
// es "¿el perfil EFECTIVO de ESTA sesión repartida entre edges es activo?" y falla
// hacia PASIVA (no auto-responder); aquí es "¿este NÚMERO puede cerrar un bucle?" y
// falla hacia BLOQUEAR. Fusionarlos rompería una de las dos.
//
// La decisión es POR NÚMERO, no por fila. Un mismo número puede aparecer en varias
// filas —otro edge_id, o la fila que dejó un emparejamiento anterior— y con el
// filtro por fila bastaba UNA fila activa para bloquear el número aunque la sesión
// VIVA fuese pasiva: marcarla pasiva desde la consola no surtía efecto y el bot no
// podía atender al teléfono personal. Un número bloquea solo si ALGUNA de sus
// sesiones NO retiradas es no-pasiva.
//
// Coste: se invoca UNA vez por entrante (dentro de la guarda anti-self-loop). Es una
// query trivial e indexada por (tenant_id, self_pn_bidx); para el MVP se acepta SIN
// caché (correcto siempre, sin invalidación que mantener). 🔴 Y ahora hay una razón
// EXTRA para no cachear que antes no existía: una caché de números propios volvería
// a materializar en RAM justo lo que la T4.1 acaba de sacar de ahí. Si el volumen lo
// exigiera, lo cacheable sería el VEREDICTO por bidx (no el número), con TTL corto.
type PostgresSelfNumbers struct {
	db *sql.DB
	// kp aporta la indexKey con la que se calcula el índice ciego. Es la MISMA que
	// usó el escritor al persistir self_pn_bidx; si no lo fuera, ningún número
	// casaría jamás y la guarda quedaría muda (por eso la indexKey es estable de por
	// vida y no rota con la KEK, §10.C).
	kp crypto.KeyProvider
}

// NewPostgresSelfNumbers construye el checker sobre el pool y el KeyProvider dados.
// El KeyProvider es OBLIGATORIO en producción: sin él no hay bidx que comparar (ver
// ErrSelfNumbersSinKeyProvider). Se pide por parámetro —y no se deriva de un
// singleton— por la misma razón que en contact.NewPostgresResolver: el keyring es
// una dependencia explícita, no un ambiente.
func NewPostgresSelfNumbers(db *sql.DB, kp crypto.KeyProvider) *PostgresSelfNumbers {
	return &PostgresSelfNumbers{db: db, kp: kp}
}

// IsSelfNumber responde si el número YA NORMALIZADO pertenece a alguna sesión del
// tenant que NO está retirada y NO es pasiva.
//
// ⚠️ El parámetro DEBE venir normalizado por contact.Normalize(KindPhoneE164, …)
// —solo dígitos, sin '+', espacios, guiones ni paréntesis—. crypto.KeyProvider.
// BlindIndex NO normaliza: es un HMAC sobre los bytes que recibe, así que "+57 300"
// y "57300" darían HMACs distintos y el mismo teléfono dejaría de casar consigo
// mismo. Normalizar es responsabilidad del llamante (lo hace isSelfLoop) porque la
// forma canónica es la del dominio contact, no la de la criptografía. El escritor
// del bidx normaliza con la MISMA función: esa simetría es todo el contrato.
//
// ⚠️ El filtro de vida es state <> 'loggedout', NO state = 'online'. Son cosas
// distintas (0029_fleet_sessions_state_loggedout.sql): 'offline' es el stream
// CloudLink caído y RECUPERABLE —el socket de WhatsApp sigue vivo y la sesión
// auto-responde en cuanto reconecta, drenando el outbox del Plan 027—, mientras que
// 'loggedout' es terminal (WhatsApp cerró el device; no vuelve sin re-QR).
// Estrechar esto a 'online' dejaría de bloquear números que SÍ auto-responden y
// reabriría justo el bucle sesión↔sesión que esta guarda existe para cerrar.
//
// 🧮 EQUIVALENCIA CON EL `GROUP BY … HAVING` QUE SUSTITUYE. La forma anterior
// agrupaba TODOS los números del tenant y devolvía aquellos cuyo grupo cumplía
// bool_or(profile <> 'passive'); el llamante preguntaba luego "¿está el mío en la
// lista?". Aquí el WHERE self_pn_bidx = $2 SELECCIONA exactamente ese único grupo
// —el conjunto de filas que comparten número, que es lo que el GROUP BY formaba— y
// se agrega sobre él sin GROUP BY. Mismo conjunto de filas, misma función de
// agregación ⇒ mismo booleano. El caso borde es el grupo VACÍO: un agregado sin
// GROUP BY sobre cero filas devuelve UNA fila con NULL (no cero filas), que aquí se
// lee como bool_or → NULL → false. Y false es justo lo que la forma vieja producía:
// sin grupo, el número no salía en la lista, el bucle no casaba, y el mensaje se
// procesaba. Los tres casos —sin filas, todas pasivas, alguna no pasiva— coinciden
// uno a uno.
//
// El filtro por self_pn no nulo y no vacío de la forma vieja ya no hace falta: existía
// que el "número vacío" no formara un grupo propio, y aquí $2 es siempre un HMAC en
// hex no vacío: no puede casar ni un NULL (NULL = x es NULL, nunca true) ni un vacío.
func (r *PostgresSelfNumbers) IsSelfNumber(ctx context.Context, tenantID, numeroNormalizado string) (bool, error) {
	if numeroNormalizado == "" {
		// Sin número no hay pregunta que hacerle a la BD. No es un error: es el
		// entrante sin from_pn, que el llamante ya filtra; aquí solo se blinda.
		return false, nil
	}
	if r.kp == nil {
		return false, ErrSelfNumbersSinKeyProvider
	}
	bidx := r.kp.BlindIndex(tenantID, numeroNormalizado)

	var bloquea sql.NullBool
	err := r.db.QueryRowContext(ctx, `
		SELECT bool_or(profile <> 'passive')
		FROM public.fleet_sessions
		WHERE tenant_id = $1
		  AND self_pn_bidx = $2
		  AND state <> 'loggedout'
	`, tenantID, bidx).Scan(&bloquea)
	if err != nil {
		// sql.ErrNoRows es teóricamente inalcanzable (un agregado sin GROUP BY
		// SIEMPRE devuelve una fila), pero se contempla explícitamente y se traduce
		// al mismo "no es número propio" que el grupo vacío: si algún día el SQL
		// cambia a una forma que sí pueda no devolver filas, el comportamiento no
		// se convierte en un error espurio que apague la guarda entera.
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		// El mensaje NO embebe el número ni el bidx (higiene PII, design.md §8/§10.I):
		// el bidx no es reversible, pero sí es un identificador estable de una persona
		// y no tiene por qué acabar en un log de errores.
		return false, fmt.Errorf("self_numbers: consulta fleet_sessions: %w", err)
	}
	// NULL (grupo vacío) ⇒ false: el número no pertenece a ninguna sesión viva del
	// tenant. Valid==false y Bool==false llevan al mismo sitio; se escribe con
	// NullBool para no confundir "no hay filas" con "las hay y todas son pasivas".
	return bloquea.Valid && bloquea.Bool, nil
}
