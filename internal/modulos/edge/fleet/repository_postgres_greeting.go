// Porta internal/gateway/fleet/repository_postgres.go @ 809345b

package fleet

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// PendingGreeting responde a la ÚNICA pregunta del emisor del saludo de T3.2 (b):
// «¿a esta sesión hay que avisarla, y a qué número?». Devuelve pending=true —con el
// self_pn ya normalizado que persistió SetSelfPn— solo si la fila existe, tiene
// número conocido, `greeted_at IS NULL` y —🔴 la tercera, ver abajo— está en perfil
// PASIVO.
//
// Las tres condiciones van en el SQL y no en el llamante a propósito: son estado de la
// FILA, y evaluarlas en Go exigiría traerse la sesión entera para mirar tres campos.
// Cero filas es la respuesta NORMAL, no un error: la da toda sesión ya saludada, toda
// sesión ACTIVA, toda sesión sin emparejar y el canal de control (que no tiene fila en
// esta tabla).
//
// 🔴 POR QUÉ FILTRA POR PERFIL, Y POR QUÉ SIN ESTO EL AVISO MENTÍA (decisión de Jhoan
// del 2026-08-21, durante el barrido de la Ola 3). El texto AVISO_SESION_PASIVA_V1
// afirma tres cosas —«nació en perfil PASIVA», «wApp todavía no responde solo» y
// «cámbiala a ACTIVA»— y las tres son FALSAS dichas a una sesión que ya está activa.
// Sin este predicado las activas también salían pendientes: se verificó ejecutando
// esta misma consulta contra Postgres con dos filas sembradas, y la activa aparecía.
// No era hipotético: al aplicar la 0066, la ÚNICA sesión de UAT (profile='active',
// self_pn poblado, greeted_at NULL) habría recibido ese mensaje en su siguiente
// latido, en un teléfono real.
//
// ⚠️ `= 'passive'` Y NO `<> 'active'`, aunque hoy sean equivalentes (la columna es NOT
// NULL y su CHECK la acota a los dos valores, 0063:104). La equivalencia solo dura
// mientras el dominio tenga DOS valores: el día que aparezca un tercer perfil,
// `<> 'active'` le mandaría un aviso que lo describe mal, y `= 'passive'` no le manda
// nada. El aviso habla de UN perfil concreto, así que la pregunta correcta es «¿es
// pasiva?», no «¿no es activa?». Fail-closed ante lo que todavía no existe.
//
// ⚠️ CONSECUENCIA ACEPTADA: quien active su sesión dentro de los ~30 s que van del
// emparejamiento al latido que consigue entregar, NO recibe el aviso. Es correcto —ya
// no le aplica—, y su `greeted_at` se queda NULL, así que si algún día vuelve a
// pasiva sí lo recibirá. Eso también es correcto: en ese momento el texto vuelve a
// ser cierto.
//
// ⚠️ NO es un claim: no reserva nada. Entre este SELECT y el MarkGreeted posterior
// hay un envío a WhatsApp de por medio, y esa es justo la ventana que el centinela de
// MarkGreeted cierra.
//
// NUNCA loguea: devuelve el número, que es PII, y quien lo recibe se encarga.
//
// 🔴 ESTE LECTOR SE INCORPORÓ A T4.1 SOBRE LA MARCHA, Y NO ESTÁ EN EL CENSO DEL
// PLAN. El censo de lectores de `self_pn` se escribió ANTES que este método:
// PendingGreeting nació con la Ola 3 (el aviso de sesión pasiva, T3.2 (b)), o sea
// DESPUÉS. Si el cifrado hubiera seguido el censo al pie de la letra, este SELECT
// habría quedado leyendo una columna que a partir de la 0068 está VACÍA: el
// predicado que exigía la columna no vacía no casaría NUNCA, PendingGreeting
// devolvería pending=false siempre, y el saludo se quedaría sin destino EN
// SILENCIO —sin error, sin log, sin nada que delatara que dejó de emitirse—.
// De ahí las dos correcciones de abajo, y de ahí esta nota: el que venga detrás
// tiene que poder ver que este lector se añadió a mano y por qué.
//
//   - El predicado pasa a `self_pn_bidx IS NOT NULL`. El índice ciego es la
//     señal de «esta fila tiene número»: se escribe con el sobre y en el mismo
//     UPDATE, así que su presencia es exactamente lo que antes decía ese filtro.
//   - El SELECT trae el SOBRE y el número se descifra EN GO, en memoria, justo
//     para pasárselo a SendText. No hay forma de hacerlo en SQL —ni debe haberla:
//     la KEK no vive en la base—.
//
// Un sobre que no abre devuelve ERROR (no pending=false): sin número no hay a
// quién avisar, y decir «no está pendiente» convertiría un dato corrupto en un
// saludo que no se manda nunca y que nadie echa de menos. El emisor lo registra
// en Warn con IDs opacos y reintenta al siguiente latido (el emisor del saludo,
// en el paquete grpc del gateway).
//
// Promesas que su test fija: la sentencia exacta y sus tres argumentos; sin fila,
// ("", false, nil); un fallo del driver, envuelto como "fleet: consultar saludo
// pendiente: …"; un sobre incompleto o que no abre, el error de decryptSelfPn tal
// cual; y una fila con índice ciego pero sin sobre, su error propio.
func (r *PostgresRepository) PendingGreeting(ctx context.Context, tenantID, edgeID, sessionID string) (string, bool, error) {
	var (
		enc, dek []byte
		kekID    sql.NullString
	)
	err := r.db.QueryRowContext(ctx, `
		SELECT self_pn_enc, self_pn_dek, self_pn_kek_id
		FROM public.fleet_sessions
		WHERE tenant_id = $1 AND edge_id = $2 AND session_id = $3
		  AND greeted_at IS NULL
		  AND self_pn_bidx IS NOT NULL
		  AND profile = 'passive'
	`, tenantID, edgeID, sessionID).Scan(&enc, &dek, &kekID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("fleet: consultar saludo pendiente: %w", err)
	}
	// A partir de aquí el número vive en memoria y no se loguea (§8 del Plan 011).
	selfPn, err := r.decryptSelfPn(enc, dek, kekID)
	if err != nil {
		return "", false, err
	}
	if selfPn == "" {
		// El bidx estaba pero el sobre no: la fila casó el WHERE y aun así no hay
		// destino. No puede pasar si el sobre se escribe entero (SetSelfPn y el
		// backfill lo hacen), pero si pasara, «pendiente sin número» sería mentira.
		return "", false, errors.New("fleet: la fila tiene índice ciego de self_pn pero no sobre; no hay destino para el saludo")
	}
	return selfPn, true, nil
}

// MarkGreeted deja constancia de que a esta sesión YA se le entregó el aviso
// AVISO_SESION_PASIVA_V1 (Plan 046 · T3.2 (b), migración 0066). Devuelve marked=true
// solo si ESTA llamada fue la que puso la marca.
//
// 🔴 EL CENTINELA `greeted_at IS NULL` ES LA IDEMPOTENCIA, y no es decorativo: si dos
// latidos de la misma sesión llegan a la vez —dos Edges reportando la misma sesión,
// dos streams durante una reconexión—, los dos pueden haber leído `pending=true` y
// los dos llegar aquí. Con el centinela, el UPDATE del segundo casa CERO filas y
// devuelve marked=false, así que el llamante sabe que su envío fue el duplicado y
// puede decirlo en el log en vez de creerse el primero. Sin él, la marca se
// re-escribiría en cada latido y `greeted_at` dejaría de ser «cuándo se avisó» para
// pasar a ser «el último latido», que es otra columna que ya existe (updated_at).
//
// ⚠️ Solo se llama con el Ack del Edge en la mano y ok=true. Un envío que muere en la
// ventana del lease (el Validator del Edge nace cerrado y tarda 0,5-1,1 s en abrirse,
// medido en campo) NO puede marcar: el reintento es el latido siguiente y nada más.
//
// NO toca `profile_updated_at`, y eso es obligatorio: la regla de la 0065 dice que
// SOLO SetProfile mueve el reloj del eje. Mover `updated_at` sí es correcto —es el
// reloj de la FILA y esta escritura la cambia—.
//
// Promesas que su test fija: la sentencia exacta; 1 fila ⇒ true, 0 filas ⇒
// (false, nil); un fallo del driver, envuelto como "fleet: marcar saludo
// entregado: …", y el de leer las filas afectadas, como "fleet: filas afectadas al
// marcar el saludo: …".
func (r *PostgresRepository) MarkGreeted(ctx context.Context, tenantID, edgeID, sessionID string) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE public.fleet_sessions
		SET greeted_at = now(), updated_at = now()
		WHERE tenant_id = $1 AND edge_id = $2 AND session_id = $3
		  AND greeted_at IS NULL
	`, tenantID, edgeID, sessionID)
	if err != nil {
		return false, fmt.Errorf("fleet: marcar saludo entregado: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("fleet: filas afectadas al marcar el saludo: %w", err)
	}
	return n > 0, nil
}
