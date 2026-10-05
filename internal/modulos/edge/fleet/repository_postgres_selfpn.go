// Porta internal/gateway/fleet/repository_postgres.go @ 809345b

package fleet

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// CountLiveBySelfPn cuenta las sesiones vivas (state != 'loggedout') del tenant con
// el self_pn dado (REQ-D4, aviso del tope de dispositivos). selfPn vacío ⇒ 0 sin
// tocar la BD.
//
// 🔒 COMPARA POR ÍNDICE CIEGO (Plan 046 · T4.1), no por el número: la columna en
// claro ya no tiene el dato. El bidx es determinista —mismo tenant + mismo número
// normalizado ⇒ mismo hex— así que la igualdad que antes hacía Postgres sobre el
// texto la sigue haciendo, ahora sobre el HMAC, y el índice parcial
// (tenant_id, self_pn_bidx) de la 0068 la sirve igual de barata.
//
// Un número que NO normaliza devuelve error (no 0): «no puedo contar» y «hay
// cero» son respuestas distintas, y devolver 0 aquí apagaría el aviso del tope
// justo en el caso raro. El llamador (warnDeviceLimit) ya lo traga en Debug.
//
// Promesas que su test fija: la sentencia exacta, con el tenant y el índice ciego
// de la forma CANÓNICA como únicos argumentos (el número no viaja); el error de
// normalizar sale con el prefijo "fleet: normalizar self_pn para contar: " y sin
// emitir sentencia; un fallo del driver, con "fleet: contar sesiones vivas por
// self_pn: ".
func (r *PostgresRepository) CountLiveBySelfPn(ctx context.Context, tenantID, selfPn string) (int, error) {
	panic(pendiente.Implementar("fleet.PostgresRepository.CountLiveBySelfPn"))
}

// SetSelfPn persiste el self_pn reportado en el Heartbeat (Plan 020 · T2). UPDATE
// acotado por (tenant_id, edge_id, session_id). selfPn vacío es un no-op: NO
// sobrescribe un valor previo bueno (protege el dato). Un UPDATE de 0 filas
// (sesión aún sin registrar) es válido: el próximo Heartbeat lo fijará.
//
// 🔒 DESDE T4.1 ESCRIBE EL SOBRE, Y DESDE T5.4 ESO ES TODO LO QUE HAY QUE ESCRIBIR.
// Hasta la migración 0070 este UPDATE llevaba además `self_pn = NULL`, atómico con
// el sobre, para que la fila no quedara ni un instante con el número en claro Y el
// sobre a la vez. Esa columna ya no existe: la 0070 la retiró, así que la limpieza
// dejó de tener objeto y se fue con ella. La garantía que daba aquel `= NULL` la da
// ahora el esquema — no hay dónde escribir un teléfono en claro.
//
// ⚠️ EL NÚMERO NO SE PASA COMO PARÁMETRO SQL EN NINGÚN SITIO: solo viajan el
// envelope (bytes opacos), la DEK envuelta, el key_id y el HMAC. Un log de
// sentencias lentas de Postgres ya no puede filtrar un teléfono.
//
// 🔴 LA GUARDA DEL WHERE EVITA REESCRIBIR LA FILA EN CADA LATIDO, y no es un
// adorno de rendimiento. Encrypt genera una DEK FRESCA por llamada, así que el
// sobre sale distinto cada vez aunque el número sea el mismo: sin guarda, cada
// sesión reescribiría cuatro columnas cada 30 s, para siempre, generando WAL y
// bloat por un dato que no cambió. Con ella el UPDATE solo entra en DOS casos:
//
//	(1) el número CAMBIÓ — bidx distinto. El bidx sí es determinista, por eso es
//	    el comparando correcto y el sobre no lo sería. Mismo criterio que el
//	    `IS DISTINCT FROM` de contact.resolveExisting.
//	(2) el sobre está envuelto por una KEK que YA NO ES LA CURRENT —
//	    `self_pn_kek_id IS DISTINCT FROM $6`. Ver abajo: es una vía de
//	    RECUPERACIÓN, no una optimización.
//
// 🔧 ERAN TRES HASTA T5.4. El que se fue era «queda plano que limpiar»
// (`self_pn IS NOT NULL`), la condición que atrapaba la primera pasada tras la 0068
// y el rollback a un binario viejo que volviera a escribir la columna en claro. La
// 0070 borró esa columna: ese caso ya no puede darse, y una guarda que vigila un
// estado imposible es deuda con buena letra.
//
// 🔧 CORRECCIÓN DEL 2026-08-21 (revisión de T4.1). Este docstring afirmaba que
// «un sobre ya escrito no se re-envuelve solo tras una rotación de KEK, y es
// correcto porque re-envolver es trabajo de crypto.Rekey». LA SEGUNDA MITAD ERA
// FALSA, y con ella la primera dejaba de ser aceptable:
//
//	Rekey re-envuelve haciendo UnwrapDEK(dek, kek_id) → WrapDEK(current)
//	(FieldCipher.ReWrap, que llama crypto/rekey.go). Necesita la KEK VIEJA. Si esa KEK desapareció del keyring
//	—un despliegue con WAPP_KEK_KEYRING incompleto, la rotación mal cerrada de
//	§10.F— Rekey NO PUEDE con esa fila: falla al desenvolver y la deja igual.
//
// Sin el caso (2), esa fila quedaba ATRAPADA PARA SIEMPRE: el bidx casa (es
// determinista, no depende de la KEK), así que la guarda decía «nada que hacer»
// aunque el Edge estuviera reportando el mismo número cada 30 s; la consola
// servía "" y scanSession dejaba un Warn por fila y por poll, indefinidamente y
// SIN vía de recuperación automática. Y es el único caso del sistema en que el
// dato en claro SÍ está disponible —llega en cada latido— y aun así no se
// restauraba: cifrar de nuevo desde el latido no necesita la KEK vieja para nada.
//
// Con (2), el latido siguiente re-cifra la fila con la KEK current y la fila se
// AUTO-SANA. Converge en una sola escritura: tras ella `self_pn_kek_id = $6` y
// la guarda vuelve a bloquear. El precio, dicho entero: tras una rotación de KEK,
// cada sesión viva paga UN reescritura extra en su siguiente latido (y de paso le
// ahorra esa fila a Rekey). No reintroduce la reescritura perpetua que la guarda
// existe para impedir, porque el comparando de (2) también es estable.
//
// ⚠️ EL COMPARANDO DE (2) ES $6, NO UN PARÁMETRO NUEVO. $6 es el kekID que
// devuelve selfPnEnvelope, y ese ES por construcción el key_id de la KEK current:
// FieldCipher.Encrypt envuelve la DEK con kp.WrapDEK, que documenta «envuelve con
// la KEK current» (keyprovider.go:88-90). Pasar además kp.CurrentKeyID() sería un
// segundo testigo del mismo hecho, y dos testigos que pueden divergir son peores
// que uno.
//
// Promesas que su test fija: la sentencia exacta y sus siete argumentos ($1-$3 la
// identidad, $4/$5 el sobre —que abierto da el número CANÓNICO—, $6 el key_id de
// la KEK current y $7 el índice ciego del canónico); un número que no normaliza
// devuelve "fleet: normalizar self_pn: …" y uno que no se puede cifrar "fleet:
// cifrar self_pn: …", los dos SIN emitir sentencia; un fallo del driver vuelve
// envuelto como "fleet: fijar self_pn: …".
func (r *PostgresRepository) SetSelfPn(ctx context.Context, tenantID, edgeID, sessionID, selfPn string) error {
	panic(pendiente.Implementar("fleet.PostgresRepository.SetSelfPn"))
}
