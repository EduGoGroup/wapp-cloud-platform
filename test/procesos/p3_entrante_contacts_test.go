//go:build integracion

package procesos

import (
	"bytes"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// P3 · pasos 6–8: las tres reglas de public.contacts que F1 difiere a este proceso (D-F1-11,
// diseno.md §4): R-27 el nombre tardío se sella, R-28 gana el primer nombre, R-29 la ráfaga con
// identidades parciales no pierde entrantes. Se afirman por SQL sobre contacts, sin calcular el
// índice ciego ni abrir ningún sobre: el contacto se identifica por el contact_id que APARECE en el
// tenant tras el primer entrante de un remitente nuevo.
//
// Cada test tiene su servidor y su base (p3_contacts_late, p3_contacts_first, p3_contacts_burst):
// así los tres son tests de primer nivel, localizables con -run, van en paralelo y la ráfaga no
// comparte estadísticas de Postgres con nadie.
//
// Cómo se sabe que un entrante terminó de procesarse, sin esperas fijas: la sesión está activa y
// tiene el menú de prueba, así que cada entrante acaba en un SendText (lo último que hace el
// servidor con él) o, si el tope de auto-respuestas lo corta, en su línea de log.

// p3ContactRow es una fila de public.contacts vista desde fuera: la clase de la ref, el contacto al
// que pertenece y las tres piezas del sobre del nombre de perfil, tal como están en la base.
type p3ContactRow struct {
	Kind      string
	ContactID string
	NameEnc   []byte
	NameDek   []byte
	NameKekID sql.NullString
}

// nameSealed dice si la fila tiene el sobre del nombre poblado. Las tres piezas van juntas («las
// tres o ninguna», R-26): una fila con solo alguna de ellas falla el test (t.Fatalf).
func (r p3ContactRow) nameSealed(t *testing.T) bool {
	t.Helper()
	enc, dek, kek := r.NameEnc != nil, r.NameDek != nil, r.NameKekID.Valid
	if enc != dek || dek != kek {
		t.Fatalf("la fila %s del contacto %s tiene el sobre del nombre a medias: enc=%v dek=%v kek_id=%v", r.Kind, r.ContactID, enc, dek, kek)
	}
	if enc && (len(r.NameEnc) == 0 || len(r.NameDek) == 0 || r.NameKekID.String == "") {
		t.Fatalf("la fila %s del contacto %s tiene una pieza del sobre vacía", r.Kind, r.ContactID)
	}
	return enc
}

// sameEnvelope dice si dos filas tienen el mismo sobre del nombre, byte a byte.
func (r p3ContactRow) sameEnvelope(o p3ContactRow) bool {
	return r.Kind == o.Kind && r.ContactID == o.ContactID && bytes.Equal(r.NameEnc, o.NameEnc) &&
		bytes.Equal(r.NameDek, o.NameDek) && r.NameKekID == o.NameKekID
}

// p3ContactIDs devuelve los contact_id distintos del tenant, ordenados.
func p3ContactIDs(t *testing.T, sc p3Scene) []string {
	t.Helper()
	rows, err := sc.DB.QueryContext(t.Context(), `SELECT DISTINCT contact_id::text FROM public.contacts WHERE tenant_id = $1::uuid ORDER BY 1`, sc.Tenant)
	if err != nil {
		t.Fatalf("leer los contactos del tenant: %v", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("leer los contactos del tenant: cerrar rows: %v", err)
		}
	}()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("leer los contactos del tenant: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("leer los contactos del tenant: %v", err)
	}
	return ids
}

// p3NewContactID devuelve el único contact_id del tenant que no está en known: el del remitente
// nuevo que acaba de escribir. Falla (t.Fatalf) si no hay exactamente uno.
func p3NewContactID(t *testing.T, sc p3Scene, known []string) string {
	t.Helper()
	var fresh []string
	for _, id := range p3ContactIDs(t, sc) {
		if !slices.Contains(known, id) {
			fresh = append(fresh, id)
		}
	}
	if len(fresh) != 1 {
		t.Fatalf("hay %d contactos nuevos en el tenant (%v), quería 1", len(fresh), fresh)
	}
	return fresh[0]
}

// p3ContactRows devuelve las filas de un contacto, ordenadas por clase (phone_e164 antes que wa_lid).
func p3ContactRows(t *testing.T, sc p3Scene, contactID string) []p3ContactRow {
	t.Helper()
	rows, err := sc.DB.QueryContext(t.Context(), `
		SELECT kind, contact_id::text, push_name_enc, push_name_dek, push_name_kek_id
		FROM public.contacts WHERE tenant_id = $1::uuid AND contact_id = $2::uuid ORDER BY kind`, sc.Tenant, contactID)
	if err != nil {
		t.Fatalf("leer las filas del contacto: %v", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("leer las filas del contacto: cerrar rows: %v", err)
		}
	}()
	var out []p3ContactRow
	for rows.Next() {
		var r p3ContactRow
		if err := rows.Scan(&r.Kind, &r.ContactID, &r.NameEnc, &r.NameDek, &r.NameKekID); err != nil {
			t.Fatalf("leer las filas del contacto: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("leer las filas del contacto: %v", err)
	}
	return out
}

// p3WaID compone un wa_message_id único dentro de un test: prefijo, número de caso y sufijo.
func p3WaID(prefix string, i int, suffix string) string {
	return fmt.Sprintf("%s-%03d-%s", prefix, i, suffix)
}

// p3LateName recorre el paso 6 con el número pn y devuelve el contact_id y su fila ya sellada: un
// entrante SIN nombre crea el contacto con las tres piezas del sobre a NULL; otro del mismo número
// con el nombre «Ana» deja el mismo contacto, sin una fila más, con las tres pobladas.
func p3LateName(t *testing.T, sc p3Scene, pn string) (string, p3ContactRow) {
	t.Helper()
	from := pn + "@s.whatsapp.net"
	sc.Edge.sendSealedIncoming(t, sealedIncoming{From: from, WaID: "P3-NAME-1", Text: p3Keyword, FromPn: pn})
	sc.expectText(t, pn, p3Welcome)
	sc.expectText(t, pn, p3MenuPrompt)
	id := p3NewContactID(t, sc, nil)
	rows := p3ContactRows(t, sc, id)
	if len(rows) != 1 || rows[0].Kind != "phone_e164" {
		t.Fatalf("el contacto nuevo tiene las filas %+v, quería una phone_e164", rows)
	}
	if rows[0].nameSealed(t) {
		t.Fatalf("el contacto creado sin nombre nace con el sobre del nombre poblado")
	}

	sc.Edge.sendSealedIncoming(t, sealedIncoming{From: from, WaID: "P3-NAME-2", Text: "1", FromPn: pn, PushName: "Ana"})
	sc.expectText(t, pn, p3SalesText)
	if ids := p3ContactIDs(t, sc); !slices.Equal(ids, []string{id}) {
		t.Fatalf("tras el nombre tardío los contactos del tenant son %v, quería solo %s", ids, id)
	}
	rows = p3ContactRows(t, sc, id)
	if len(rows) != 1 || rows[0].Kind != "phone_e164" {
		t.Fatalf("tras el nombre tardío el contacto tiene las filas %+v, quería la misma phone_e164", rows)
	}
	if !rows[0].nameSealed(t) {
		t.Fatalf("R-27: el nombre que llegó después no se selló: las tres piezas siguen a NULL")
	}
	return id, rows[0]
}

// TestP3_LatePushNameIsSealed es el paso 6 · R-27 (y R-26): el nombre que llega tarde a un contacto
// creado sin él se guarda entonces, en el mismo contacto, con las tres piezas del sobre.
func TestP3_LatePushNameIsSealed(t *testing.T) {
	t.Parallel()
	sc := p3ActiveMenuScene(t, "p3_contacts_late", "p3-contacts-late")
	p3LateName(t, sc, "573002220001")
	if errs := sc.Edge.Errores(); len(errs) != 0 {
		t.Errorf("errores del núcleo del Edge: %v", errs)
	}
	edgeSinErrores(t, sc.S, nil)
}

// TestP3_FirstPushNameWins es el paso 7 · R-28: con el sobre ya sellado por el paso 6, un tercer
// entrante del mismo número con OTRO nombre («Beto») no cambia un byte de push_name_enc, ni de las
// otras dos piezas. Es el precio aceptado del centinela `push_name_enc IS NULL`, no un defecto.
func TestP3_FirstPushNameWins(t *testing.T) {
	t.Parallel()
	sc := p3ActiveMenuScene(t, "p3_contacts_first", "p3-contacts-first")
	const pn = "573002220002"
	id, sealed := p3LateName(t, sc, pn)

	// El flujo terminó con la opción del paso 6: la palabra clave lo vuelve a arrancar, y el menú
	// que llega es la señal de que este entrante ya pasó por contacts.
	sc.Edge.sendSealedIncoming(t, sealedIncoming{From: pn + "@s.whatsapp.net", WaID: "P3-NAME-3", Text: p3Keyword, FromPn: pn, PushName: "Beto"})
	sc.expectText(t, pn, p3MenuPrompt)
	rows := p3ContactRows(t, sc, id)
	if len(rows) != 1 || !rows[0].sameEnvelope(sealed) {
		t.Errorf("R-28: el segundo nombre cambió el sobre o las filas del contacto:\nantes   %+v\ndespués %+v", sealed, rows)
	}
	if ids := p3ContactIDs(t, sc); !slices.Equal(ids, []string{id}) {
		t.Errorf("los contactos del tenant son %v, quería solo %s", ids, id)
	}
	if errs := sc.Edge.Errores(); len(errs) != 0 {
		t.Errorf("errores del núcleo del Edge: %v", errs)
	}
	edgeSinErrores(t, sc.S, nil)
}

// p3BurstSize es N, el número de entrantes de la ráfaga del paso 8: 64, el cupo del pool de
// entrantes del runtime (FlowConfig.MaxConcurrentIncoming por defecto, el semáforo de OnIncoming).
// Con N igual al cupo, los N entran a la vez en HandleIncoming —la máxima concurrencia que el
// servidor da a contacts.Resolve— y NINGUNO espera cupo, así que ninguno se puede descartar por
// saturación (que solo ocurre tras 30 s sin cupo). Más entrantes no añaden carrera: se encolarían
// tras el semáforo, y solo la primera oleada cruza los row-locks (en cuanto el sobre queda sellado,
// el UPDATE del centinela ya no toca filas). El test viejo usaba 16 goroutines × 60 llamadas
// directas a Resolve; desde fuera la concurrencia la pone el servidor, no el test.
const p3BurstSize = 64

// p3CrossWaitersQuery cuenta los backends de esta base que esperan un lock que tiene OTRO backend
// que a su vez espera uno suyo: los dos lados de un ciclo de row-locks aún sin romper.
const p3CrossWaitersQuery = `
	SELECT count(*) FROM pg_stat_activity a
	WHERE a.datname = current_database() AND a.wait_event_type = 'Lock' AND EXISTS (
		SELECT 1 FROM pg_stat_activity b
		WHERE b.pid = ANY(pg_blocking_pids(a.pid)) AND a.pid = ANY(pg_blocking_pids(b.pid)))`

// p3BurstTimeout es el tope para que el servidor termine la ráfaga. Holgado: medido, tarda menos
// de un segundo; un 40P01 reintentado añade como mucho el segundo de deadlock_timeout por ciclo.
const p3BurstTimeout = 60 * time.Second

// p3BurstTally cuenta cómo terminó cada entrante de la ráfaga: con respuesta, cortado por el tope
// de auto-respuestas, perdido con ERROR o descartado por saturación.
type p3BurstTally struct{ replies, rateLimited, failed, saturated int }

func (b p3BurstTally) total() int { return b.replies + b.rateLimited + b.failed + b.saturated }

// p3WaitBurst espera, sondeando, a que los want entrantes mandados a un contacto con el menú
// abierto hayan terminado TODOS, y devuelve cómo. Un entrante con un texto que no es opción del menú
// acaba, como último acto del servidor, en el SendText «opción no válida» (o el de ayuda, que lo
// sustituye cada pocos intentos fallidos) o —si el tope de
// auto-respuestas por conversación lo corta— en la línea de log de ese corte; si se pierde, en su
// línea ERROR; si no hubo cupo, en la de saturación. Cuando las cuatro cuentas suman want no queda
// trabajo pendiente. replies acumula los textos ya leídos del canal entre llamadas. Falla
// (t.Fatalf) si no suman want dentro del tope, o si llega un texto que no es el esperado.
func p3WaitBurst(t *testing.T, sc p3Scene, contactID, to string, want int, replies *int) p3BurstTally {
	t.Helper()
	var tally p3BurstTally
	edgeEsperar(t, p3BurstTimeout, fmt.Sprintf("que terminen los %d entrantes de la ráfaga", want), func() bool {
		for drained := false; !drained; {
			select {
			case got := <-sc.Edge.Textos():
				if got.A != to || (got.Texto != p3InvalidOption && got.Texto != p3HelpOption) {
					t.Fatalf("en la ráfaga llegó un SendText a %q con %q; quería a %q con la opción no válida o la ayuda", got.A, got.Texto, to)
				}
				*replies++
			default:
				drained = true
			}
		}
		tally = p3BurstTally{replies: *replies}
		for _, l := range sc.S.LineasLog() {
			msg, ok := l["msg"].(string)
			if !ok {
				continue // una línea sin msg de texto no es ninguna de las tres
			}
			switch {
			case msg == p3MsgRateLimited && l["contact_id"] == contactID:
				tally.rateLimited++
			case msg == p3MsgIncomingFailed && l["level"] == "ERROR":
				tally.failed++
			case strings.HasPrefix(msg, p3MsgSaturated):
				tally.saturated++
			}
		}
		return tally.total() >= want
	})
	if tally.total() != want {
		t.Fatalf("la ráfaga dejó %+v, que suma %d; quería %d", tally, tally.total(), want)
	}
	return tally
}

// TestP3_HistoryBurstWithoutDeadlock es el paso 8 · R-29: la ráfaga de identidades parciales y
// disjuntas no pierde ningún entrante.
//
// Siembra: UN entrante con from_pn y from_lid y SIN nombre → un contacto con dos filas (phone_e164
// y wa_lid). 🔴 La precondición —dos filas, el mismo contact_id, el sobre del nombre a NULL en las
// dos— es una aserción ANTES de la ráfaga: sembrado con nombre, el UPDATE del centinela no tomaría
// ni un row-lock, no habría ciclo que reproducir y el paso quedaría verde y hueco.
//
// Ráfaga: p3BurstSize entrantes seguidos, la mitad SOLO con from_pn y la mitad SOLO con from_lid,
// todos con el mismo nombre. Cada mitad bloquea primero SU fila y después, al sellar el nombre, la
// otra: órdenes cruzados, el ciclo de 40P01 que WithTx absorbe reintentando.
//
// 🔴 La PRIMERA OLEADA se lanza contra una compuerta. Mandados sin más, los entrantes llegan de uno
// en uno por el stream y el primero sella el nombre antes de que el segundo llegue a su fila:
// medido, cero deadlocks, y el paso quedaría verde y hueco por otro camino. Por eso una transacción
// del test retiene las dos filas mientras entran los dos primeros (uno de cada mitad), espera a que
// cada fila tenga al suyo en cola (pg_locks) y suelta: cada uno toma su fila y pide la otra, y el
// ciclo queda formado. El resto de la ráfaga entra con el ciclo vivo y se encola detrás. Es lo que
// el test viejo conseguía con su barrera de la primera oleada, hecho desde fuera.
//
// ⚠️ La compuerta retiene SOLO a la primera oleada, a propósito. Reteniendo a los N (medido en
// T9.15, 3 de 10 corridas contra el viejo) las colas de cada fila se llenan por delante de quien
// cruza, cada víctima abortada le cede la fila a otro que vuelve a cruzar, y los ciclos se
// encadenan a un segundo de deadlock_timeout cada uno: hasta 65 deadlocks, los 64 entrantes
// perdidos por «context deadline exceeded» a los 30 s. Es un hallazgo del viejo, no de este test,
// y queda en el informe de T9.15; aquí se afirma lo que el viejo sí cumple.
//
// Aserta: (a) ningún entrante perdido —cero ERROR «runtime: procesar entrante», cero descartes, y
// los N anotados en ingest_dedupe—; (b) el contacto sigue siendo uno, con sus dos filas; (c) las
// dos acaban con el sobre poblado, y un entrante más con OTRO nombre no cambia un byte de ninguna.
// Y (d) que la primera oleada cruzó los locks de verdad: Postgres contó al menos un deadlock.
//
// El reintento de WithTx, aislado y con la víctima elegida, lo ejecuta el paso 9
// (TestP3_TxRetryOnSerializationFailure); aquí se afirma que bajo ráfaga no se pierde nada.
func TestP3_HistoryBurstWithoutDeadlock(t *testing.T) {
	t.Parallel()
	sc := p3ActiveMenuScene(t, "p3_contacts_burst", "p3-contacts-burst")
	const pn, lid = "573002220003", "880022200003@lid"
	fromPn, fromLid := pn+"@s.whatsapp.net", lid

	id := p3SeedBurstContact(t, sc, pn, lid)
	deadlocks := consultaEntero(t, sc.DB, p3DeadlocksQuery)

	ids := make([]string, p3BurstSize)
	send := func(i int) {
		ids[i] = p3WaID("P3-BURST", i, "X")
		in := sealedIncoming{From: fromPn, WaID: ids[i], Text: "x", FromPn: pn, PushName: "Ana"}
		if i%2 == 1 {
			in = sealedIncoming{From: fromLid, WaID: ids[i], Text: "x", FromLid: lid, PushName: "Ana"}
		}
		sc.Edge.sendSealedIncoming(t, in)
	}

	live := p3BurstFirstWave(t, sc, id, send)
	for i := 2; i < p3BurstSize; i++ {
		send(i)
	}

	replies := 0
	tally := p3WaitBurst(t, sc, id, pn, p3BurstSize, &replies)
	crossed := consultaEntero(t, sc.DB, p3DeadlocksQuery) - deadlocks
	t.Logf("ráfaga de %d: %+v; el resto entró con el ciclo vivo: %v; deadlocks que Postgres lleva contados: %d", p3BurstSize, tally, live, crossed)

	// (a), (b) y (c): ninguno perdido, un contacto con dos filas y el sobre poblado en las dos…
	sealed := p3CheckBurstOutcome(t, sc, id, ids, tally)
	// … y sellado una sola vez.
	sc.Edge.sendSealedIncoming(t, sealedIncoming{From: fromPn, WaID: "P3-BURST-EXTRA", Text: "x", FromPn: pn, FromLid: lid, PushName: "Beto"})
	if extra := p3WaitBurst(t, sc, id, pn, p3BurstSize+1, &replies); extra.failed != 0 || extra.saturated != 0 {
		t.Errorf("el entrante de más se perdió: %+v", extra)
	}
	after := p3ContactRows(t, sc, id)
	if len(after) != 2 || !after[0].sameEnvelope(sealed[0]) || !after[1].sameEnvelope(sealed[1]) {
		t.Errorf("un entrante más con otro nombre cambió el sobre:\nantes   %+v\ndespués %+v", sealed, after)
	}
	// (d) la primera oleada cruzó los locks: el contador se vuelca con retraso, por eso se sondea.
	edgeEsperar(t, p3StatsTimeout, "que Postgres cuente al menos un deadlock de la ráfaga (si no, no cruzó los locks)", func() bool {
		return consultaEntero(t, sc.DB, p3DeadlocksQuery) > deadlocks
	})

	if errs := sc.Edge.Errores(); len(errs) != 0 {
		t.Errorf("errores del núcleo del Edge: %v", errs)
	}
	edgeSinErrores(t, sc.S, nil)
}

// p3SeedBurstContact es la siembra del paso 8 y su precondición: UN entrante con from_pn y from_lid
// y sin nombre deja un contacto con dos filas y el sobre del nombre a NULL en las dos. Devuelve el
// contact_id; falla (t.Fatalf) si la siembra no quedó así.
func p3SeedBurstContact(t *testing.T, sc p3Scene, pn, lid string) string {
	sc.Edge.sendSealedIncoming(t, sealedIncoming{From: pn + "@s.whatsapp.net", WaID: "P3-BURST-SEED", Text: p3Keyword, FromPn: pn, FromLid: lid})
	sc.expectText(t, pn, p3Welcome)
	sc.expectText(t, pn, p3MenuPrompt)
	id := p3NewContactID(t, sc, nil)
	seeded := p3ContactRows(t, sc, id)
	if len(seeded) != 2 || seeded[0].Kind != "phone_e164" || seeded[1].Kind != "wa_lid" {
		t.Fatalf("precondición: la siembra dejó las filas %+v; quería dos, phone_e164 y wa_lid, del mismo contacto", seeded)
	}
	for _, r := range seeded {
		if r.nameSealed(t) {
			t.Fatalf("precondición: la fila %s nació con el sobre del nombre poblado; la ráfaga no cruzaría ningún lock", r.Kind)
		}
	}
	return id
}

// p3BurstFirstWave lanza la primera oleada contra la compuerta: uno solo con from_pn (send(0)) y
// uno solo con from_lid (send(1)). Dice si el ciclo de row-locks seguía vivo al soltarla.
func p3BurstFirstWave(t *testing.T, sc p3Scene, id string, send func(int)) bool {
	gate := p3OpenGate(t, sc.DB, "compuerta")
	gate.exec(t, `SELECT 1 FROM public.contacts WHERE tenant_id = $1::uuid AND contact_id = $2::uuid FOR UPDATE`, sc.Tenant, id)
	send(0)
	send(1)
	// Quien espera el row-lock de una fila toma antes el lock «tuple» de esa fila: dos tuplas
	// distintas de contacts con ese lock son las dos filas del contacto, cada una con su entrante.
	edgeEsperarValor(t, sc.DB, "2", "que cada fila del contacto tenga en cola a su entrante", `
		SELECT count(DISTINCT (page, tuple))::text FROM pg_locks
		WHERE locktype = 'tuple' AND relation = 'public.contacts'::regclass AND pid <> $1`, gate.pid)
	gate.exec(t, `COMMIT`)
	// El resto entra con el ciclo vivo (dos backends esperándose el uno al otro). Si el sondeo
	// llegara tarde y Postgres ya lo hubiera roto, las dos filas estarían selladas: también vale.
	live := false
	edgeEsperar(t, edgeTopeFila, "que la primera oleada forme el ciclo de row-locks (o ya lo haya resuelto)", func() bool {
		live = consultaEntero(t, sc.DB, p3CrossWaitersQuery) == 2
		return live || consultaEntero(t, sc.DB, `SELECT count(*) FROM public.contacts
			WHERE tenant_id = $1::uuid AND contact_id = $2::uuid AND push_name_enc IS NOT NULL`, sc.Tenant, id) == 2
	})
	return live
}

// p3CheckBurstOutcome son las aserciones (a), (b) y (c) de la ráfaga, y devuelve las dos filas ya
// selladas. Falla (t.Fatalf) si el contacto no conserva sus dos filas.
func p3CheckBurstOutcome(t *testing.T, sc p3Scene, id string, ids []string, tally p3BurstTally) []p3ContactRow {
	// (a) ninguno perdido.
	if tally.failed != 0 || tally.saturated != 0 {
		t.Errorf("R-29: la ráfaga perdió entrantes: %+v\n%v", tally, p3IncomingErrors(sc.S, ""))
	}
	if n := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.ingest_dedupe WHERE session_id = $1 AND wa_message_id = ANY($2)`, sc.Edge.SessionID, ids); n != p3BurstSize {
		t.Errorf("ingest_dedupe tiene %d de los %d entrantes de la ráfaga", n, p3BurstSize)
	}
	// (b) un contacto, dos filas.
	if got := p3ContactIDs(t, sc); !slices.Equal(got, []string{id}) {
		t.Errorf("tras la ráfaga los contactos del tenant son %v, quería solo %s", got, id)
	}
	sealed := p3ContactRows(t, sc, id)
	if len(sealed) != 2 || sealed[0].Kind != "phone_e164" || sealed[1].Kind != "wa_lid" {
		t.Fatalf("tras la ráfaga el contacto tiene las filas %+v, quería las dos de la siembra", sealed)
	}
	// (c) el sobre poblado en las dos.
	for _, r := range sealed {
		if !r.nameSealed(t) {
			t.Errorf("la fila %s sigue con el sobre del nombre a NULL: la ráfaga no escribió y no reproduce nada", r.Kind)
		}
	}
	return sealed
}
