//go:build integracion

package procesos

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"maps"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// La rotación de KEK del envelope de PII de negocio (crypto.Rekey y crypto.PendingByKeyID) sobre
// filas reales, con dos juegos de llaves. Es lo que P10 no puede hacer por la puerta: el arnés no
// arranca un segundo servidor con otro keyring sobre la base en la que el primero cifró (hallazgo
// 57 de F9). Aquí no hay servidor: se llama a la lógica directamente sobre una base clonada, y cada
// ring (rekey_fixtures_contrato_test.go) es el servidor antes o después de cambiar el keyring.
// Re-expresa los tres Test* de internal/platform/crypto/rekey_integration_test.go, que corre contra
// un Postgres vivo y que F10 borra.
//
// 🔴 NO ES UNA SUITE DE CONTRATO AL USO, Y SE DICE: rekey.go no se reconstruye, así que no hay
// puerto ni …helpertest. El fichero se llama *_contrato_test.go porque es la única forma de que el
// candado ProcessImports (R9.4.d, regla 3c) le deje importar internal/platform/crypto, que es lo
// único que importa de internal/. De ese paquete usa los constructores y, además, Rekey y
// PendingByKeyID: lo que se prueba.
//
// 🔴 OCHO SOBRES, NO SIETE. El censo de la rotación tiene ocho entradas sobre siete tablas: la fila
// de contacts lleva dos sobres (número y nombre) y la octava, intake_jobs.source_text, entró
// después de escribirse el test viejo, que no la sembraba (hallazgo 58 de F9). Processed y el mapa
// de pendientes cuentan SOBRES: aquí son 8 con 7 filas.
//
// 🔴 HOMÓNIMO: la «DEK» y la KEK de este fichero son las del envelope de PII de negocio, NO la DEK
// del ADR-0007 que custodia el cliente. Se generan en el test y mueren con él.

// rekeyUntouchedMark es la huella de todo lo que la rotación NO puede cambiar: las filas ENTERAS de
// las siete tablas del censo, quitando solo lo que sí escribe (la DEK envuelta y el key_id de cada
// sobre, y updated_at). Dentro quedan el dato cifrado, el índice ciego, las claves primarias y el
// resto de columnas, también las de las filas sin sobre.
const rekeyUntouchedMark = `SELECT md5(
	coalesce((SELECT string_agg((to_jsonb(c) - 'value_dek' - 'value_kek_id' - 'push_name_dek' - 'push_name_kek_id' - 'updated_at')::text, '|'
		ORDER BY c.tenant_id, c.kind, c.value_bidx) FROM public.contacts c), '') || '#' ||
	coalesce((SELECT string_agg((to_jsonb(b) - 'data_dek' - 'data_kek_id' - 'updated_at')::text, '|' ORDER BY b.intake_id) FROM public.intake_buyer_data b), '') || '#' ||
	coalesce((SELECT string_agg((to_jsonb(i) - 'secret_dek' - 'secret_kek_id' - 'updated_at')::text, '|' ORDER BY i.tenant_id) FROM public.tenant_integrations i), '') || '#' ||
	coalesce((SELECT string_agg((to_jsonb(f) - 'self_pn_dek' - 'self_pn_kek_id' - 'updated_at')::text, '|'
		ORDER BY f.tenant_id, f.edge_id, f.session_id) FROM public.fleet_sessions f), '') || '#' ||
	coalesce((SELECT string_agg((to_jsonb(l) - 'api_key_dek' - 'api_key_kek_id' - 'updated_at')::text, '|' ORDER BY l.tenant_id) FROM public.tenant_llm l), '') || '#' ||
	coalesce((SELECT string_agg((to_jsonb(j) - 'source_text_dek' - 'source_text_kek_id' - 'updated_at')::text, '|' ORDER BY j.id) FROM public.intake_jobs j), '') || '#' ||
	coalesce((SELECT string_agg((to_jsonb(r) - 'literal_dek' - 'literal_kek_id')::text, '|' ORDER BY r.intake_id, r.revision_no) FROM public.intake_revisions r), ''))`

// rekeyPass corre una pasada de la rotación con el ring dado, acotada por rekeyTimeout.
func rekeyPass(t *testing.T, db *sql.DB, r rekeyRing, batch int) (crypto.Report, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), rekeyTimeout)
	defer cancel()
	return crypto.Rekey(ctx, db, r.cipher, r.kp, batch)
}

// wantRekeyPending exige que el mapa de pendientes visto con current como vigente sea want, clave a
// clave y con sus valores exactos. Un `> 0` seguiría verde con una entrada fuera del censo.
func wantRekeyPending(t *testing.T, db *sql.DB, when, current string, want map[string]int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), rekeyTimeout)
	defer cancel()
	got, err := crypto.PendingByKeyID(ctx, db, current)
	if err != nil {
		t.Fatalf("%s: PendingByKeyID(current %q): %v", when, current, err)
	}
	wantRekeyMap(t, fmt.Sprintf("%s: pendientes con current %q", when, current), got, want)
}

// wantRekeyMap exige que got sea want y que NO sea nil: el mapa vacío es la respuesta «no queda
// nada bajo otra KEK» —la que autoriza a retirarla—, y nil es la ausencia de respuesta.
func wantRekeyMap(t *testing.T, what string, got, want map[string]int) {
	t.Helper()
	if got == nil || !maps.Equal(got, want) {
		t.Fatalf("%s = %v (nil %v), quería %v. El mapa cuenta SOBRES, no filas: la fila de contacts aporta dos",
			what, got, got == nil, want)
	}
}

// wantRekeyRotated comprueba, sobre a sobre, que la rotación hizo su trabajo y SOLO su trabajo: el
// key_id es el de la KEK current, la DEK envuelta es otra, el dato cifrado está intacto byte a byte
// (rotar es re-envolver la DEK, no re-cifrar el valor) y el claro sale con el ring dado.
func wantRekeyRotated(t *testing.T, db *sql.DB, r rekeyRing, envelopes []rekeyEnvelope) {
	t.Helper()
	for _, e := range envelopes {
		s := e.stored(t, db)
		if s.keyID != r.kp.CurrentKeyID() {
			t.Errorf("%s: key_id %q tras rotar, quería %q (¿el sobre está en el censo? una entrada por tabla no basta: "+
				"contacts necesita dos)", e.name, s.keyID, r.kp.CurrentKeyID())
		}
		if bytes.Equal(s.dek, e.dek) {
			t.Errorf("%s: la DEK envuelta es la misma que antes de rotar", e.name)
		}
		if !bytes.Equal(s.enc, e.enc) {
			t.Errorf("%s: el dato cifrado cambió al rotar: la rotación no re-cifra", e.name)
		}
	}
	wantRekeyReadable(t, db, "tras rotar", r, envelopes)
}

// wantRekeyReadable abre cada sobre con el ring dado, usando el key_id que la fila dice, y exige el
// claro sembrado. Con un ring que ya no tiene la KEK vieja es el retiro seguro ejecutado de verdad,
// no supuesto a partir de un mapa de pendientes vacío.
func wantRekeyReadable(t *testing.T, db *sql.DB, when string, r rekeyRing, envelopes []rekeyEnvelope) {
	t.Helper()
	for _, e := range envelopes {
		s := e.stored(t, db)
		plain, err := r.cipher.Decrypt(s.enc, s.dek, s.keyID)
		if err != nil {
			t.Errorf("%s, %s: Decrypt bajo el key_id %q: %v", when, e.name, s.keyID, err)
			continue
		}
		if plain != e.plain {
			t.Errorf("%s, %s: el claro no es el sembrado", when, e.name)
		}
	}
}

// TestRekeyContract_EightEnvelopes_RotateToCurrent es la rotación entera: ocho sobres sembrados bajo
// la KEK A, una pasada con el keyring {A,B} y B como current, y la KEK A retirada después.
//
// Afirma: el mapa de pendientes antes (los ocho bajo A) y después (vacío, no nil); Processed = 8
// sobres con 7 filas, de uno en uno; el key_id de cada sobre pasa a B y su DEK envuelta cambia; el
// dato cifrado, el índice ciego del número propio y todo lo demás de las filas quedan como estaban;
// las filas sin sobre siguen sin él; la segunda pasada no hace nada; y con el keyring solo {B} los
// ocho claros siguen saliendo. Rotar y perder el dato es peor que no rotar.
func TestRekeyContract_EightEnvelopes_RotateToCurrent(t *testing.T) {
	db := newRekeyDB(t)
	keys := newRekeyKeys(t)
	before := keys.ring(t, rekeyOldKeyID, rekeyOldKeyID)
	// La base clonada no trae ningún sobre: los conteos de abajo son exactos sin limpiar nada.
	wantRekeyPending(t, db, "antes de sembrar", rekeyNewKeyID, map[string]int{})

	tenant := seedRekeyTenant(t, db)
	envelopes := seedRekeyCensus(t, db, before, tenant)
	bare := seedRekeyRowsWithoutEnvelope(t, db)
	if len(envelopes) != 8 {
		t.Fatalf("sembrados %d sobres, quería los 8 del censo", len(envelopes))
	}
	const selfPnIndex = `SELECT self_pn_bidx FROM public.fleet_sessions WHERE tenant_id::text = $1`
	// El índice ciego se lee crudo y ANTES: recalcularlo daría el mismo valor aunque la rotación
	// hubiera reescrito la columna.
	index := p2Text(t, db, selfPnIndex, tenant)
	if index == "" {
		t.Fatal("la sesión sembrada no tiene self_pn_bidx")
	}
	mark := consultaTexto(t, db, rekeyUntouchedMark)

	wantRekeyPending(t, db, "sembrado bajo A", rekeyNewKeyID, map[string]int{rekeyOldKeyID: len(envelopes)})
	wantRekeyPending(t, db, "sembrado bajo A", rekeyOldKeyID, map[string]int{})

	rotated := keys.ring(t, rekeyNewKeyID, rekeyOldKeyID, rekeyNewKeyID)
	// De uno en uno: cada entrada del censo da varias vueltas.
	rep, err := rekeyPass(t, db, rotated, 1)
	if err != nil {
		t.Fatalf("Rekey: %v", err)
	}
	if rep.Processed != len(envelopes) || rep.CurrentKeyID != rekeyNewKeyID {
		t.Fatalf("Rekey: processed %d y current %q, quería %d SOBRES (no filas: contacts aporta dos) y current %q. "+
			"Un sobre de menos es una entrada del censo que no barrió", rep.Processed, rep.CurrentKeyID, len(envelopes), rekeyNewKeyID)
	}
	wantRekeyMap(t, "pendientes del informe de la pasada", rep.PendingByKeyID, map[string]int{})

	wantRekeyRotated(t, db, rotated, envelopes)
	wantRekeyPending(t, db, "tras rotar", rekeyNewKeyID, map[string]int{})
	// Visto desde la KEK vieja, los ocho sobres están ahora bajo B.
	wantRekeyPending(t, db, "tras rotar", rekeyOldKeyID, map[string]int{rekeyNewKeyID: len(envelopes)})
	if got := p2Text(t, db, selfPnIndex, tenant); got != index {
		t.Errorf("self_pn_bidx cambió al rotar la KEK (%q → %q): el índice ciego lo calcula la clave del índice, que no rota", index, got)
	}
	if got := consultaTexto(t, db, rekeyUntouchedMark); got != mark {
		t.Errorf("la rotación tocó algo más que la DEK envuelta, el key_id y updated_at (huella %s → %s)", mark, got)
	}
	if n := consultaEntero(t, db, rekeyEmptyTrios, bare); n != 0 {
		t.Errorf("tras rotar, las filas sin sobre tienen %d piezas de sobre pobladas, quería 0", n)
	}

	// La segunda pasada no encuentra nada: reejecutar tras terminar es un no-op.
	again, err := rekeyPass(t, db, rotated, 1)
	if err != nil {
		t.Fatalf("Rekey, segunda pasada: %v", err)
	}
	if again.Processed != 0 {
		t.Errorf("segunda pasada: processed %d, quería 0", again.Processed)
	}
	wantRekeyMap(t, "pendientes de la segunda pasada", again.PendingByKeyID, map[string]int{})

	// El retiro: el keyring ya no tiene la KEK A.
	wantRekeyReadable(t, db, "con la KEK vieja retirada", keys.ring(t, rekeyNewKeyID, rekeyNewKeyID), envelopes)
}

// TestRekeyContract_PendingByKeyID_SumsTheCensus es el mapa que decide si una KEK vieja se puede
// retirar: tiene que sumar las ocho entradas del censo, por key_id, y solo quedar vacío cuando no
// queda nada. Con dos KEK viejas a la vez (los ocho sobres bajo A y tres contactos bajo C) se ve
// que agrupa por key_id y no por tabla, y que el vigente nunca cuenta.
func TestRekeyContract_PendingByKeyID_SumsTheCensus(t *testing.T) {
	db := newRekeyDB(t)
	keys := newRekeyKeys(t)
	tenant := seedRekeyTenant(t, db)
	envelopes := seedRekeyCensus(t, db, keys.ring(t, rekeyOldKeyID, rekeyOldKeyID), tenant)
	seedRekeyRowsWithoutEnvelope(t, db)
	other := keys.ring(t, rekeyOtherKeyID, rekeyOtherKeyID)
	for i := range 3 {
		envelopes = append(envelopes, seedRekeyNamelessContact(t, db, other, tenant, fmt.Sprintf("57300222000%d", i)))
	}

	wantRekeyPending(t, db, "antes de rotar", rekeyNewKeyID, map[string]int{rekeyOldKeyID: 8, rekeyOtherKeyID: 3})
	wantRekeyPending(t, db, "antes de rotar", rekeyOldKeyID, map[string]int{rekeyOtherKeyID: 3})
	wantRekeyPending(t, db, "antes de rotar", rekeyOtherKeyID, map[string]int{rekeyOldKeyID: 8})

	rotated := keys.ring(t, rekeyNewKeyID, rekeyOldKeyID, rekeyNewKeyID, rekeyOtherKeyID)
	rep, err := rekeyPass(t, db, rotated, 2)
	if err != nil {
		t.Fatalf("Rekey: %v", err)
	}
	if rep.Processed != 11 {
		t.Errorf("Rekey: processed %d, quería 11 sobres (8 bajo A y 3 bajo C)", rep.Processed)
	}
	wantRekeyMap(t, "pendientes del informe de la pasada", rep.PendingByKeyID, map[string]int{})
	wantRekeyPending(t, db, "tras rotar", rekeyNewKeyID, map[string]int{})
	wantRekeyPending(t, db, "tras rotar", rekeyOtherKeyID, map[string]int{rekeyNewKeyID: 11})
	wantRekeyRotated(t, db, rotated, envelopes)
}

// TestRekeyContract_NamelessContact protege «las tres o ninguna» (0069) por el lado de la rotación:
// un contacto sin nombre tiene las tres columnas del sobre del nombre a NULL, y eso no es un sobre
// a medias sino la ausencia de sobre. Cuenta UNA vez como pendiente (por su número), la rotación no
// le inventa un sobre —un key_id que apuntara a una DEK inexistente abortaría la rotación
// siguiente— y su número sí rota y sigue legible con la KEK vieja retirada.
func TestRekeyContract_NamelessContact(t *testing.T) {
	db := newRekeyDB(t)
	keys := newRekeyKeys(t)
	tenant := seedRekeyTenant(t, db)
	value := seedRekeyNamelessContact(t, db, keys.ring(t, rekeyOldKeyID, rekeyOldKeyID), tenant, "573001110002")

	const namePieces = `SELECT num_nonnulls(push_name_enc, push_name_dek, push_name_kek_id) FROM public.contacts WHERE tenant_id::text = $1`
	if n := consultaEntero(t, db, namePieces, tenant); n != 0 {
		t.Fatalf("el contacto sin nombre nace con %d de las 3 piezas del sobre pobladas, quería 0", n)
	}
	// Una sola vez, no dos: un 2 sería contar también el sobre del nombre, que no existe.
	wantRekeyPending(t, db, "antes de rotar", rekeyNewKeyID, map[string]int{rekeyOldKeyID: 1})

	rotated := keys.ring(t, rekeyNewKeyID, rekeyOldKeyID, rekeyNewKeyID)
	rep, err := rekeyPass(t, db, rotated, 10)
	if err != nil {
		t.Fatalf("Rekey: %v", err)
	}
	if rep.Processed != 1 {
		t.Errorf("Rekey: processed %d, quería 1 sobre (el del número): un 2 sería un sobre de nombre inventado", rep.Processed)
	}
	wantRekeyMap(t, "pendientes del informe de la pasada", rep.PendingByKeyID, map[string]int{})
	if n := consultaEntero(t, db, namePieces, tenant); n != 0 {
		t.Errorf("tras rotar, el contacto sin nombre tiene %d de las 3 piezas del sobre pobladas, quería 0", n)
	}
	wantRekeyRotated(t, db, rotated, []rekeyEnvelope{value})
	wantRekeyReadable(t, db, "con la KEK vieja retirada", keys.ring(t, rekeyNewKeyID, rekeyNewKeyID), []rekeyEnvelope{value})
}

// TestRekeyContract_ProcessedCountsEveryBatch fija Processed con lotes de más de una fila, que es
// lo que el «de uno en uno» de los otros tests no distingue: contar un sobre por lote da lo mismo
// que contar las filas del lote cuando el lote es de uno. Cinco contactos por tanda, con el tamaño
// por defecto (0 y negativo), con un lote que no divide exacto y con uno mayor que las filas.
func TestRekeyContract_ProcessedCountsEveryBatch(t *testing.T) {
	db := newRekeyDB(t)
	keys := newRekeyKeys(t)
	before := keys.ring(t, rekeyOldKeyID, rekeyOldKeyID)
	rotated := keys.ring(t, rekeyNewKeyID, rekeyOldKeyID, rekeyNewKeyID)
	tenant := seedRekeyTenant(t, db)
	const perRound = 5
	for round, batch := range []int{0, -3, 2, perRound, 500} {
		envelopes := make([]rekeyEnvelope, 0, perRound)
		for i := range perRound {
			envelopes = append(envelopes, seedRekeyNamelessContact(t, db, before, tenant, fmt.Sprintf("5730033%02d%03d", round, i)))
		}
		what := fmt.Sprintf("tanda %d, batch %d", round, batch)
		wantRekeyPending(t, db, what, rekeyNewKeyID, map[string]int{rekeyOldKeyID: perRound})
		rep, err := rekeyPass(t, db, rotated, batch)
		if err != nil {
			t.Fatalf("%s: Rekey: %v", what, err)
		}
		// Los de las tandas anteriores ya están bajo B: solo cuentan los cinco nuevos.
		if rep.Processed != perRound {
			t.Errorf("%s: processed %d, quería %d", what, rep.Processed, perRound)
		}
		wantRekeyMap(t, what+": pendientes del informe", rep.PendingByKeyID, map[string]int{})
		wantRekeyRotated(t, db, rotated, envelopes)
	}
}

// TestRekeyContract_MissingKEK_AbortsThenResumes es la rotación que se queda a medias y se retoma.
// Con un sobre bajo una KEK que el keyring no tiene, la pasada devuelve error, ese sobre queda como
// estaba y el informe trae el mapa de pendientes a NIL —vacío sería afirmar «no queda nada» justo
// cuando no se llegó a contar—. El error nombra la fila por sus identificadores, nunca por el
// claro. Devuelta la KEK al keyring, otra pasada termina lo que faltaba: entre las dos procesan
// cada sobre exactamente una vez.
func TestRekeyContract_MissingKEK_AbortsThenResumes(t *testing.T) {
	db := newRekeyDB(t)
	keys := newRekeyKeys(t)
	tenant := seedRekeyTenant(t, db)
	envelopes := seedRekeyCensus(t, db, keys.ring(t, rekeyOldKeyID, rekeyOldKeyID), tenant)
	const strayPhone = "573004440001"
	stray := seedRekeyNamelessContact(t, db, keys.ring(t, rekeyOtherKeyID, rekeyOtherKeyID), tenant, strayPhone)

	failed, err := rekeyPass(t, db, keys.ring(t, rekeyNewKeyID, rekeyOldKeyID, rekeyNewKeyID), 1)
	if err == nil {
		t.Fatalf("Rekey sin la KEK %q en el keyring: sin error (processed %d)", rekeyOtherKeyID, failed.Processed)
	}
	if strings.Contains(err.Error(), strayPhone) || strings.Contains(err.Error(), rekeyPhone) {
		t.Errorf("el error de la rotación lleva un número en claro")
	}
	if failed.PendingByKeyID != nil {
		t.Errorf("la pasada con error trae pendientes %v, quería nil: no se llegó a contar", failed.PendingByKeyID)
	}
	if s := stray.stored(t, db); s.keyID != rekeyOtherKeyID || !bytes.Equal(s.dek, stray.dek) || !bytes.Equal(s.enc, stray.enc) {
		t.Errorf("el sobre bajo la KEK ausente cambió en la pasada abortada (key_id %q)", s.keyID)
	}
	wantRekeyPending(t, db, "tras la pasada abortada", rekeyNewKeyID,
		map[string]int{rekeyOldKeyID: len(envelopes) - failed.Processed, rekeyOtherKeyID: 1})

	all := keys.ring(t, rekeyNewKeyID, rekeyOldKeyID, rekeyNewKeyID, rekeyOtherKeyID)
	resumed, err := rekeyPass(t, db, all, 1)
	if err != nil {
		t.Fatalf("Rekey con las tres KEK: %v", err)
	}
	if total := failed.Processed + resumed.Processed; total != len(envelopes)+1 {
		t.Errorf("entre la pasada abortada (%d) y la reanudada (%d) procesaron %d sobres, quería %d",
			failed.Processed, resumed.Processed, total, len(envelopes)+1)
	}
	wantRekeyMap(t, "pendientes de la pasada reanudada", resumed.PendingByKeyID, map[string]int{})
	wantRekeyRotated(t, db, all, append(envelopes, stray))
}
