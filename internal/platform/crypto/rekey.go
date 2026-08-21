package crypto

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
)

// DefaultRekeyBatch es el tamaño de batch por defecto de la rotación cuando el
// llamador no especifica uno (<= 0). Acota memoria y duración de la transacción.
const DefaultRekeyBatch = 500

// rekeyTarget describe UNA tabla con dato cifrado por envelope encryption (Plan
// 011/012): qué columnas identifican la fila, dónde vive la DEK envuelta y dónde
// el key_id de la KEK que la envolvió. La rotación no necesita saber nada más —
// en particular NO conoce la columna del dato cifrado (…_enc), que jamás toca:
// eso es precisamente lo que hace barata la rotación (§7).
//
// Requisitos de una tabla para entrar aquí: tener la pareja (dek, kek_id) y una
// columna updated_at (el UPDATE la refresca). Las cuatro tablas del censo la tienen.
type rekeyTarget struct {
	// table es el nombre CALIFICADO de la tabla ("public.contacts").
	table string
	// pkCols son las columnas de la clave primaria, en orden. Se leen con ::text
	// (hay UUID y TEXT mezclados) y se usan para localizar la fila en el UPDATE.
	// Son IDENTIFICADORES, nunca contenido: pueden aparecer en logs y errores
	// (value_bidx es el HMAC del índice ciego, no el valor).
	pkCols []string
	// dekCol es la columna con la DEK por-fila envuelta por la KEK.
	dekCol string
	// kekCol es la columna con el key_id de la KEK que envolvió dekCol. Es el
	// discriminador de la rotación: kekCol <> current ⇒ fila pendiente.
	kekCol string
}

// rekeyTargets es el CENSO de tablas con dato cifrado por la KEK. Añadir aquí una
// tabla nueva es lo único que hace falta para que entre en la rotación.
//
// ⚠️ Omitir una tabla es el fallo que este censo viene a impedir. Hasta el Plan
// 042 la rotación barría SOLO public.contacts: PendingByKeyID podía devolver
// "rotación completa" (mapa vacío) con dos tablas enteras todavía envueltas por la
// KEK vieja, y retirar esa KEK del keyring —el paso que ese mapa vacío autoriza
// (§10.F)— habría dejado esas filas ILEGIBLES.
//
// Los nombres de tabla y columna de aquí se interpolan en el SQL (no pueden ser
// parámetros): son constantes de este archivo, NUNCA entrada del usuario.
var rekeyTargets = []rekeyTarget{
	// PII de contactos (Plan 011, migraciones 0006/0007). PK compuesta con el
	// índice ciego.
	{
		table:  "public.contacts",
		pkCols: []string{"tenant_id", "kind", "value_bidx"},
		dekCol: "value_dek",
		kekCol: "value_kek_id",
	},
	// Datos mínimos del comprador (Plan 041, migración 0045). Las tres columnas
	// del envelope son NOT NULL: toda fila existente está cifrada.
	{
		table:  "public.intake_buyer_data",
		pkCols: []string{"intake_id"},
		dekCol: "data_dek",
		kekCol: "data_kek_id",
	},
	// Secreto de firma HMAC del puente CRM (Plan 042, migración 0047). Aquí el
	// trío del envelope es NULLable (un tenant puede tener integración 'local' sin
	// secreto): las filas sin secreto tienen secret_kek_id NULL y quedan fuera del
	// barrido solas, porque `NULL <> 'x'` no es TRUE en SQL.
	{
		table:  "public.tenant_integrations",
		pkCols: []string{"tenant_id"},
		dekCol: "secret_dek",
		kekCol: "secret_kek_id",
	},
	// Número propio de la sesión, self_pn (Plan 046 · T4.1, migración 0068). Mismo
	// caso que tenant_integrations: el trío del envelope es NULLable —una sesión sin
	// emparejar no tiene número— y esas filas quedan fuera del barrido solas, porque
	// `NULL <> 'x'` no es TRUE en SQL.
	//
	// 🔴 ENTRA AQUÍ EN EL MISMO COMMIT QUE EMPIEZA A CIFRAR, y no después. El censo
	// es lo que hace verdadero al mapa de PendingByKeyID, y ese mapa vacío es lo que
	// AUTORIZA retirar una KEK del keyring (§10.F). Una tabla cifrada que no esté en
	// el censo no rompe nada hoy: rompe el día de la rotación, dejando sus filas
	// envueltas por una KEK que ya nadie tiene — y con el self_pn eso significa una
	// flota entera sin número propio y sin forma de recuperarlo.
	//
	// 📌 Es además lo que permite que SetSelfPn NO re-escriba el sobre en cada latido
	// (ver la guarda de su WHERE): re-envolver tras una rotación es trabajo de aquí,
	// que lo hace SIN descifrar el número, y no del camino caliente del Heartbeat.
	{
		table:  "public.fleet_sessions",
		pkCols: []string{"tenant_id", "edge_id", "session_id"},
		dekCol: "self_pn_dek",
		kekCol: "self_pn_kek_id",
	},
}

// selectSQL arma el SELECT del batch: PK (como texto) + DEK + key_id de las filas
// fuera de la KEK current, bloqueadas con FOR UPDATE SKIP LOCKED.
func (t rekeyTarget) selectSQL() string {
	cols := make([]string, 0, len(t.pkCols)+2)
	for _, c := range t.pkCols {
		cols = append(cols, c+"::text")
	}
	cols = append(cols, t.dekCol, t.kekCol)
	return fmt.Sprintf(`
		SELECT %s
		FROM %s
		WHERE %s <> $1
		ORDER BY %s
		LIMIT $2
		FOR UPDATE SKIP LOCKED
	`, strings.Join(cols, ", "), t.table, t.kekCol, strings.Join(t.pkCols, ", "))
}

// updateSQL arma el UPDATE por PK: $1 = DEK re-envuelta, $2 = key_id nuevo, y
// desde $3 los valores de la PK en el orden de pkCols. NO toca la columna del dato
// cifrado (§7): re-envolver la DEK no re-cifra el valor.
func (t rekeyTarget) updateSQL() string {
	conds := make([]string, len(t.pkCols))
	for i, c := range t.pkCols {
		conds[i] = fmt.Sprintf("%s = $%d", c, i+3)
	}
	return fmt.Sprintf(`
		UPDATE %s
		SET %s = $1, %s = $2, updated_at = now()
		WHERE %s
	`, t.table, t.dekCol, t.kekCol, strings.Join(conds, " AND "))
}

// pendingSQL arma el conteo de filas pendientes agrupado por key_id viejo.
func (t rekeyTarget) pendingSQL() string {
	return fmt.Sprintf(`
		SELECT %s, COUNT(*)
		FROM %s
		WHERE %s <> $1
		GROUP BY %s
	`, t.kekCol, t.table, t.kekCol, t.kekCol)
}

// pkDesc describe la fila para logs y errores: "col=valor col=valor". Son
// IDENTIFICADORES (§10.H), nunca el valor en claro ni la DEK.
func (t rekeyTarget) pkDesc(pk []string) string {
	parts := make([]string, len(pk))
	for i, v := range pk {
		parts[i] = t.pkCols[i] + "=" + v
	}
	return strings.Join(parts, " ")
}

// Report resume una pasada de rotación (re-wrap incremental) para auditoría
// (§10.H). NO contiene contenido sensible (número/value/JID), solo contadores y
// key_ids del keyring.
type Report struct {
	// Processed es el total de filas re-envueltas a la KEK current en esta pasada,
	// SUMADO sobre todas las tablas del censo (rekeyTargets).
	Processed int
	// CurrentKeyID es el key_id de la KEK current hacia la que se re-envolvió.
	CurrentKeyID string
	// PendingByKeyID cuenta, al terminar la pasada, cuántas filas siguen envueltas
	// por cada key_id != current, AGREGADO sobre las tablas del censo (0 filas de un
	// key_id ⇒ esa KEK ya no se referencia en NINGUNA tabla y es retirable del
	// keyring, §10.F). Es un mapa vacío (no nil) si no hay pendientes: la rotación
	// quedó completa.
	PendingByKeyID map[string]int
}

// Rekey re-envuelve por batch todas las DEK que aún no están envueltas por la KEK
// current, SIN re-cifrar el dato (§7), recorriendo TODAS las tablas del censo
// rekeyTargets (contacts, intake_buyer_data, tenant_integrations, fleet_sessions).
// Para cada fila:
// UnwrapDEK(dek, kek_id) → WrapDEK con la current → UPDATE de dek + kek_id +
// updated_at, dejando el dato cifrado y la PK INTACTOS. El valor NUNCA se descifra
// (solo la DEK, en memoria).
//
// Es REANUDABLE e IDEMPOTENTE (§10.D): el criterio de selección es el propio
// estado (kek_id <> current), así que reejecutar tras una interrupción retoma
// donde quedó y reejecutar tras terminar es un no-op (0 filas). Cada batch es
// ATÓMICO (una transacción con FOR UPDATE SKIP LOCKED + UPDATE por PK): un fallo
// de ReWrap aborta el batch sin dejar filas a medias y sin corromper (fail-safe
// §10.J). SKIP LOCKED permite ejecutar la rotación de forma segura en paralelo con
// lecturas y con otra instancia de Rekey.
//
// Al terminar, Report.PendingByKeyID indica cuántas filas siguen en cada KEK vieja
// sumando las cuatro tablas (para decidir el retiro seguro a 0 referencias, §10.F).
// Los logs NO llevan contenido (§10.H).
func Rekey(ctx context.Context, db *sql.DB, cipher *FieldCipher, kp KeyProvider, batch int) (Report, error) {
	if batch <= 0 {
		batch = DefaultRekeyBatch
	}
	current := kp.CurrentKeyID()
	report := Report{CurrentKeyID: current, PendingByKeyID: map[string]int{}}

	for _, target := range rekeyTargets {
		perTable := 0
		for {
			n, err := rekeyBatch(ctx, db, cipher, target, current, batch)
			if err != nil {
				return report, err
			}
			if n == 0 {
				break
			}
			perTable += n
			report.Processed += n
			// Auditoría SIN contenido (§10.H): solo tabla, contadores y key_id current.
			log.Printf("[wapp][crypto][INFO] rekey batch: table=%s processed=%d total=%d current=%q",
				target.table, perTable, report.Processed, current)
		}
	}

	pending, err := PendingByKeyID(ctx, db, current)
	if err != nil {
		return report, err
	}
	report.PendingByKeyID = pending
	return report, nil
}

// pendingRow es una fila pendiente de re-wrap. pk lleva los valores de la clave
// primaria (como texto, en el orden de rekeyTarget.pkCols): identificadores, NO
// contenido (§10.H) — value_bidx es el HMAC del índice ciego, no el valor. Nunca
// se lee ni se toca la columna del dato cifrado: la rotación no descifra el valor.
type pendingRow struct {
	pk       []string
	dek      []byte
	oldKeyID string
}

// rekeyBatch procesa un batch de UNA tabla en UNA transacción: toma hasta batch
// filas con kek_id <> current (bloqueándolas con FOR UPDATE SKIP LOCKED),
// re-envuelve su DEK a la current y las actualiza por PK. Devuelve cuántas filas
// procesó (0 cuando ya no quedan pendientes o todas las restantes están bloqueadas
// por otro worker). Todo o nada: cualquier error hace rollback (defer) sin commit.
func rekeyBatch(ctx context.Context, db *sql.DB, cipher *FieldCipher, target rekeyTarget, current string, batch int) (n int, err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("rekey: iniciar transacción: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			if rerr := tx.Rollback(); rerr != nil {
				log.Printf("[wapp][crypto][WARN] rekey: rollback del batch de %s: %v", target.table, rerr)
			}
		}
	}()

	batchRows, err := selectPending(ctx, tx, target, current, batch)
	if err != nil {
		return 0, err
	}
	if len(batchRows) == 0 {
		return 0, nil
	}

	updateSQL := target.updateSQL()
	for _, r := range batchRows {
		newDEK, newID, rwErr := cipher.ReWrap(r.dek, r.oldKeyID)
		if rwErr != nil {
			// Fail-safe §10.J/§10.H: reporta tabla + key_id + PK, NUNCA el
			// value/número/JID ni la DEK. El rollback (defer) deja el batch intacto.
			return 0, fmt.Errorf("rekey: ReWrap de %s (%s) con key_id %q: %w",
				target.table, target.pkDesc(r.pk), r.oldKeyID, rwErr)
		}
		args := make([]any, 0, len(r.pk)+2)
		args = append(args, newDEK, newID)
		for _, v := range r.pk {
			args = append(args, v)
		}
		if _, uerr := tx.ExecContext(ctx, updateSQL, args...); uerr != nil {
			return 0, fmt.Errorf("rekey: actualizar fila de %s (%s): %w",
				target.table, target.pkDesc(r.pk), uerr)
		}
	}

	if cerr := tx.Commit(); cerr != nil {
		return 0, fmt.Errorf("rekey: confirmar batch de %s: %w", target.table, cerr)
	}
	committed = true
	return len(batchRows), nil
}

// selectPending toma y bloquea (FOR UPDATE SKIP LOCKED) hasta batch filas de
// target cuyo kek_id != current, dentro de la transacción tx. El cursor se cierra
// al volver: en una misma tx no puede haber un rows abierto mientras se emiten los
// UPDATE. Los locks se mantienen hasta el commit/rollback, no hasta el Close.
func selectPending(ctx context.Context, tx *sql.Tx, target rekeyTarget, current string, batch int) (rowsOut []pendingRow, err error) {
	rows, err := tx.QueryContext(ctx, target.selectSQL(), current, batch)
	if err != nil {
		return nil, fmt.Errorf("rekey: seleccionar batch de %s: %w", target.table, err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("rekey: cerrar filas de %s: %w", target.table, cerr)
			rowsOut = nil
		}
	}()

	for rows.Next() {
		// pk fresco por fila: el slice se guarda en pendingRow, no se puede reusar.
		pk := make([]string, len(target.pkCols))
		var r pendingRow
		dest := make([]any, 0, len(pk)+2)
		for i := range pk {
			dest = append(dest, &pk[i])
		}
		dest = append(dest, &r.dek, &r.oldKeyID)
		if serr := rows.Scan(dest...); serr != nil {
			return nil, fmt.Errorf("rekey: escanear fila de %s: %w", target.table, serr)
		}
		r.pk = pk
		rowsOut = append(rowsOut, r)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, fmt.Errorf("rekey: iterar filas de %s: %w", target.table, rerr)
	}
	return rowsOut, nil
}

// PendingByKeyID devuelve, por key_id != currentKeyID, cuántas filas siguen
// envueltas por esa KEK SUMANDO todas las tablas del censo (rekeyTargets). Un
// key_id con 0 filas (ausente del mapa) ya no se referencia en ninguna tabla y su
// KEK es retirable del keyring (§10.F); mientras tenga > 0 pendientes NO debe
// retirarse (retirarla haría fallar claro las lecturas de esas filas, §10.J —
// nunca corrupción, pero sí indisponibilidad). Devuelve un mapa vacío (no nil)
// cuando la rotación está completa.
func PendingByKeyID(ctx context.Context, db *sql.DB, currentKeyID string) (map[string]int, error) {
	pending := make(map[string]int)
	for _, target := range rekeyTargets {
		if err := pendingForTarget(ctx, db, target, currentKeyID, pending); err != nil {
			return nil, err
		}
	}
	return pending, nil
}

// pendingForTarget acumula en pending el conteo por key_id viejo de UNA tabla.
func pendingForTarget(ctx context.Context, db *sql.DB, target rekeyTarget, currentKeyID string, pending map[string]int) (err error) {
	rows, err := db.QueryContext(ctx, target.pendingSQL(), currentKeyID)
	if err != nil {
		return fmt.Errorf("rekey: consultar pendientes por key_id en %s: %w", target.table, err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("rekey: cerrar pendientes de %s: %w", target.table, cerr)
		}
	}()

	for rows.Next() {
		var (
			keyID string
			count int
		)
		if serr := rows.Scan(&keyID, &count); serr != nil {
			return fmt.Errorf("rekey: escanear pendientes de %s: %w", target.table, serr)
		}
		pending[keyID] += count
	}
	if rerr := rows.Err(); rerr != nil {
		return fmt.Errorf("rekey: iterar pendientes de %s: %w", target.table, rerr)
	}
	return nil
}
