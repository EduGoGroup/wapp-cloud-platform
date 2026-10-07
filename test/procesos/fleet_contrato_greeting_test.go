//go:build integracion

package procesos

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Casos propios del aviso de sesión pasiva y del sobre incompleto de fleet.PostgresRepository
// (hallazgo 76 de F3): lo que la suite de contrato deja fuera a propósito —PendingGreeting y
// MarkGreeted no están en fleet.Repository, son de su consumidor— y lo que solo existe contra
// Postgres: que las tres condiciones de PendingGreeting y el centinela de MarkGreeted son estado de
// la FILA, que dos marcas a la vez las arbitra el lock de fila y qué sirve cada lector cuando el
// sobre del self_pn está a medias. Parte de fleet_contrato_test.go (05 E-13) y no importa nada de
// internal/ (R9.4.d, regla 1): el adaptador le llega por el fleetContractRig.
//
// Lo que estos casos NO afirman: los textos de los fallos del driver (los fija el test de fichero
// con su base de datos falsa) y que un sobre que no abre por faltar la KEK es un error en
// PendingGreeting (ídem; aquí solo se afirma el sobre INCOMPLETO).

// fleetContractGreeter son los dos métodos del adaptador que no están en el puerto. Es una interfaz
// local, y no un alias de la suite como los del hallazgo 75, porque aquí no hay un tipo del puerto
// que nombrar: lo que haría falta nombrar es el adaptador mismo (fleet.PostgresRepository), que no
// es de la suite, y de él solo se usan estos dos métodos.
type fleetContractGreeter interface {
	PendingGreeting(ctx context.Context, tenantID, edgeID, sessionID string) (string, bool, error)
	MarkGreeted(ctx context.Context, tenantID, edgeID, sessionID string) (bool, error)
}

const (
	// fleetContractFreezeGreetedAt es la tercera sentencia de freezeFleetContractRow: escribe el
	// centinela en greeted_at y no toca ninguna otra columna.
	fleetContractFreezeGreetedAt = `UPDATE public.fleet_sessions SET greeted_at = $4 WHERE tenant_id = $1::uuid AND edge_id = $2 AND session_id = $3`

	// Las cinco formas de romper por SQL el sobre de una fila que SetSelfPn escribió entero. Las
	// cuatro primeras lo dejan a medias; la última lo quita y deja el índice ciego.
	fleetContractDropEnc      = `UPDATE public.fleet_sessions SET self_pn_enc = NULL WHERE tenant_id = $1::uuid AND edge_id = $2 AND session_id = $3`
	fleetContractDropDek      = `UPDATE public.fleet_sessions SET self_pn_dek = NULL WHERE tenant_id = $1::uuid AND edge_id = $2 AND session_id = $3`
	fleetContractDropKekID    = `UPDATE public.fleet_sessions SET self_pn_kek_id = NULL WHERE tenant_id = $1::uuid AND edge_id = $2 AND session_id = $3`
	fleetContractEmptyEnc     = `UPDATE public.fleet_sessions SET self_pn_enc = ''::bytea WHERE tenant_id = $1::uuid AND edge_id = $2 AND session_id = $3`
	fleetContractDropEnvelope = `UPDATE public.fleet_sessions SET self_pn_enc = NULL, self_pn_dek = NULL, self_pn_kek_id = NULL WHERE tenant_id = $1::uuid AND edge_id = $2 AND session_id = $3`

	// Los dos textos literales con que PendingGreeting rechaza una fila sin destino.
	fleetContractIncompleteEnvelope = "fleet: sobre de self_pn incompleto (enc/dek/kek_id no viajan juntos)"
	fleetContractIndexWithoutEnv    = "fleet: la fila tiene índice ciego de self_pn pero no sobre; no hay destino para el saludo"

	// fleetContractMarkRounds y fleetContractMarkers son las repeticiones de la carrera de
	// MarkGreeted y las marcas simultáneas de cada una.
	fleetContractMarkRounds = 12
	fleetContractMarkers    = 16
)

// fleetContractGreeting son los tres relojes de la fila que tocan (o no deben tocar) los casos del
// aviso: la marca del saludo, el reloj de la fila y el reloj del eje profile.
type fleetContractGreeting struct {
	greetedAt        sql.NullTime
	updatedAt        time.Time
	profileUpdatedAt time.Time
}

// readFleetContractGreeting lee por SQL los tres relojes de la sesión. Falla el test si no existe.
func readFleetContractGreeting(t *testing.T, db *sql.DB, tenantID, edgeID, sessionID string) fleetContractGreeting {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), fleetContractTimeout)
	defer cancel()
	var g fleetContractGreeting
	err := db.QueryRowContext(ctx, `
		SELECT greeted_at, updated_at, profile_updated_at
		FROM public.fleet_sessions
		WHERE tenant_id = $1::uuid AND edge_id = $2 AND session_id = $3`,
		tenantID, edgeID, sessionID,
	).Scan(&g.greetedAt, &g.updatedAt, &g.profileUpdatedAt)
	if err != nil {
		t.Fatalf("leer los relojes de la sesión (%s, %s, %s): %v", tenantID, edgeID, sessionID, err)
	}
	return g
}

// execFleetContractRow ejecuta sobre la fila de la sesión una sentencia de tres argumentos (la
// identidad) y falla el test si no toca exactamente una fila.
func execFleetContractRow(t *testing.T, db *sql.DB, statement, tenantID, edgeID, sessionID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), fleetContractTimeout)
	defer cancel()
	res, err := db.ExecContext(ctx, statement, tenantID, edgeID, sessionID)
	if err != nil {
		t.Fatalf("sembrar la sesión (%s, %s, %s) con %q: %v", tenantID, edgeID, sessionID, statement, err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		t.Fatalf("sembrar la sesión (%s, %s, %s) con %q: tocó %d filas (err %v); quería 1", tenantID, edgeID, sessionID, statement, n, err)
	}
}

// requireFleetContractPending afirma lo que PendingGreeting contesta para la sesión: con want vacío,
// ("", false, nil) —no hay a quién avisar, y no es un error—; con número, (want, true, nil).
func requireFleetContractPending(t *testing.T, rig fleetContractRig, tenantID, edgeID, sessionID, want, when string) {
	t.Helper()
	to, pending, err := rig.greeter.PendingGreeting(t.Context(), tenantID, edgeID, sessionID)
	if err != nil || to != want || pending != (want != "") {
		t.Errorf("PendingGreeting %s = (%q, %v, %v); quería (%q, %v, nil)", when, to, pending, err, want, want != "")
	}
}

// requireFleetContractProfileSet afirma el resultado de un SetProfile por el puerto: encontró la
// sesión y no dio error. El perfil lo escribe quien llama como literal ("active", "passive"): el
// tipo fleet.Profile no se nombra.
func requireFleetContractProfileSet(t *testing.T, profile string, found bool, err error) {
	t.Helper()
	if err != nil || !found {
		t.Fatalf("SetProfile(%s) = (found=%v, err=%v); quería la sesión sin error", profile, found, err)
	}
}

// TestFleetContract_PendingGreeting_OnlyAPassiveUngreetedSessionWithNumber: PendingGreeting solo
// devuelve pendiente —con el número CANÓNICO descifrado— para una fila que existe, tiene número, no
// se ha saludado y está en perfil pasivo. Todo lo demás es ("", false, nil), que es la respuesta
// normal y no un error:
//
//   - una sesión que no existe, y la misma identidad pedida desde otro tenant;
//   - una sesión recién registrada, pasiva pero sin número;
//   - con número: pendiente, y preguntar NO es un claim (la segunda pregunta contesta igual y la
//     fila sigue sin marca);
//   - pasada a ACTIVA deja de estar pendiente, y de vuelta a pasiva lo está otra vez: su greeted_at
//     siguió a NULL.
func TestFleetContract_PendingGreeting_OnlyAPassiveUngreetedSessionWithNumber(t *testing.T) {
	keyring := fleetContractKeyID + ":" + clavesSecretoB64(t)
	rig := newFleetContractRig(t, newFleetContractDB(t), keyring, fleetContractKeyID, clavesSecretoB64(t))

	requireFleetContractPending(t, rig, rig.m.SeedTenant(t), "edge-"+uuid.NewString(), "session-"+uuid.NewString(), "", "de una sesión que no existe")

	tenant, edge, session := newFleetContractSession(t, rig)
	requireFleetContractPending(t, rig, tenant, edge, session, "", "de una sesión sin número")

	setFleetContractSelfPn(t, rig, tenant, edge, session, fleetContractSelfPnSpelled)
	for range 2 {
		requireFleetContractPending(t, rig, tenant, edge, session, fleetContractSelfPn, "de una sesión pasiva con número")
	}
	if asked := readFleetContractGreeting(t, rig.db, tenant, edge, session); asked.greetedAt.Valid {
		t.Errorf("preguntar por el saludo marcó la fila: greeted_at = %v; quería NULL", asked.greetedAt.Time)
	}
	requireFleetContractPending(t, rig, rig.m.SeedTenant(t), edge, session, "", "de la misma sesión pedida desde otro tenant")

	found, err := rig.m.Repository.SetProfile(t.Context(), tenant, session, "active")
	requireFleetContractProfileSet(t, "active", found, err)
	requireFleetContractPending(t, rig, tenant, edge, session, "", "de una sesión ACTIVA")
	found, err = rig.m.Repository.SetProfile(t.Context(), tenant, session, "passive")
	requireFleetContractProfileSet(t, "passive", found, err)
	requireFleetContractPending(t, rig, tenant, edge, session, fleetContractSelfPn, "de una sesión que volvió a pasiva sin saludar")
}

// TestFleetContract_MarkGreeted_MarksOnceAndOnlyItsRow: MarkGreeted es un compare-and-set sobre
// greeted_at, y lo que deja en la fila es:
//
//   - de una sesión que no existe, (false, nil): cero filas no es un error;
//   - la primera marca devuelve true, fija greeted_at con el now() de la base, mueve updated_at (el
//     reloj de la FILA) y NO mueve profile_updated_at (el reloj del eje: solo lo mueve SetProfile);
//   - tras ella PendingGreeting deja de dar pendiente;
//   - la segunda devuelve (false, nil) y no reescribe nada: con greeted_at y updated_at congelados
//     por SQL, los dos siguen byte a byte en el centinela;
//   - la marca es de UNA fila: la misma sesión bajo otro Edge sigue sin marca y pendiente;
//   - y el centinela es su única condición: una sesión sin número también se marca.
func TestFleetContract_MarkGreeted_MarksOnceAndOnlyItsRow(t *testing.T) {
	keyring := fleetContractKeyID + ":" + clavesSecretoB64(t)
	rig := newFleetContractRig(t, newFleetContractDB(t), keyring, fleetContractKeyID, clavesSecretoB64(t))
	mark := func(tenantID, edgeID, sessionID string, want bool, when string) {
		t.Helper()
		marked, err := rig.greeter.MarkGreeted(t.Context(), tenantID, edgeID, sessionID)
		if err != nil || marked != want {
			t.Errorf("MarkGreeted %s = (%v, %v); quería (%v, nil)", when, marked, err, want)
		}
	}

	mark(rig.m.SeedTenant(t), "edge-"+uuid.NewString(), "session-"+uuid.NewString(), false, "de una sesión que no existe")

	tenant, edge, session := newFleetContractSession(t, rig)
	otherEdge := "edge-" + uuid.NewString()
	if err := rig.m.Repository.MarkOnline(t.Context(), tenant, otherEdge, session); err != nil {
		t.Fatalf("MarkOnline de la misma sesión bajo otro Edge: error inesperado %v", err)
	}
	setFleetContractSelfPn(t, rig, tenant, edge, session, fleetContractSelfPn)
	setFleetContractSelfPn(t, rig, tenant, otherEdge, session, fleetContractSelfPn)
	freezeFleetContractRow(t, rig.db, fleetContractFreezeUpdatedAt, tenant, edge, session)
	before := readFleetContractGreeting(t, rig.db, tenant, edge, session)
	if before.greetedAt.Valid {
		t.Fatalf("una sesión sin saludar tiene greeted_at = %v; quería NULL", before.greetedAt.Time)
	}

	mark(tenant, edge, session, true, "la primera vez")
	first := readFleetContractGreeting(t, rig.db, tenant, edge, session)
	if !first.greetedAt.Valid || first.greetedAt.Time.Before(before.profileUpdatedAt) {
		t.Errorf("greeted_at tras la primera marca = %v (válida %v); quería el now() de la base, no anterior al alta (%v)",
			first.greetedAt.Time, first.greetedAt.Valid, before.profileUpdatedAt)
	}
	if first.updatedAt.Equal(fleetContractFrozenAt) {
		t.Error("la primera marca no movió updated_at, que es el reloj de la fila")
	}
	if !first.profileUpdatedAt.Equal(before.profileUpdatedAt) {
		t.Errorf("la marca movió profile_updated_at: %v → %v; solo lo mueve SetProfile", before.profileUpdatedAt, first.profileUpdatedAt)
	}
	requireFleetContractPending(t, rig, tenant, edge, session, "", "de una sesión ya saludada")

	freezeFleetContractRow(t, rig.db, fleetContractFreezeGreetedAt, tenant, edge, session)
	freezeFleetContractRow(t, rig.db, fleetContractFreezeUpdatedAt, tenant, edge, session)
	mark(tenant, edge, session, false, "la segunda vez")
	second := readFleetContractGreeting(t, rig.db, tenant, edge, session)
	if !second.greetedAt.Time.Equal(fleetContractFrozenAt) || !second.updatedAt.Equal(fleetContractFrozenAt) {
		t.Errorf("la segunda marca reescribió la fila: greeted_at %v, updated_at %v; quería los dos en %v",
			second.greetedAt.Time, second.updatedAt, fleetContractFrozenAt)
	}

	if other := readFleetContractGreeting(t, rig.db, tenant, otherEdge, session); other.greetedAt.Valid {
		t.Errorf("marcar la fila de un Edge marcó la del otro: greeted_at = %v; quería NULL", other.greetedAt.Time)
	}
	requireFleetContractPending(t, rig, tenant, otherEdge, session, fleetContractSelfPn, "de la misma sesión bajo el otro Edge")

	bare, bareEdge, bareSession := newFleetContractSession(t, rig)
	mark(bare, bareEdge, bareSession, true, "de una sesión sin número")
}

// TestFleetContract_MarkGreeted_ConcurrentMarks_ExactlyOneWins es la carrera que el centinela
// `greeted_at IS NULL` existe para cerrar: dos latidos de la misma sesión leyeron pendiente y los
// dos llegan a marcar. De fleetContractMarkers marcas simultáneas sobre la misma fila, exactamente
// UNA devuelve true; las demás, (false, nil) —perder no es un error—, y la fila queda marcada. Lo
// arbitra el lock de fila de Postgres, que es lo que el test de fichero no puede ver. Las marcas
// salen juntas por una barrera, y se repite con fleetContractMarkRounds sesiones.
func TestFleetContract_MarkGreeted_ConcurrentMarks_ExactlyOneWins(t *testing.T) {
	keyring := fleetContractKeyID + ":" + clavesSecretoB64(t)
	rig := newFleetContractRig(t, newFleetContractDB(t), keyring, fleetContractKeyID, clavesSecretoB64(t))

	type result struct {
		marked bool
		err    error
	}
	for round := range fleetContractMarkRounds {
		tenant, edge, session := newFleetContractSession(t, rig)
		start := make(chan struct{})
		results := make(chan result, fleetContractMarkers)
		var wg sync.WaitGroup
		for range fleetContractMarkers {
			wg.Go(func() {
				<-start
				marked, err := rig.greeter.MarkGreeted(t.Context(), tenant, edge, session)
				results <- result{marked: marked, err: err}
			})
		}
		close(start)
		wg.Wait()
		close(results)

		wins := 0
		for r := range results {
			switch {
			case r.err != nil:
				t.Errorf("ronda %d: una marca devolvió el error %v; perder la carrera no es un error", round, r.err)
			case r.marked:
				wins++
			}
		}
		if wins != 1 {
			t.Errorf("ronda %d: de %d marcas a la vez ganaron %d; quería exactamente 1", round, fleetContractMarkers, wins)
		}
		if after := readFleetContractGreeting(t, rig.db, tenant, edge, session); !after.greetedAt.Valid {
			t.Errorf("ronda %d: tras la carrera la fila sigue sin greeted_at", round)
		}
	}
}

// TestFleetContract_BrokenEnvelope_ReadsServeNoNumberAndTheGreetingFails: qué sirve cada lector
// cuando el sobre del self_pn de una fila no está entero. SetSelfPn lo escribe entero y aquí se
// rompe por SQL, de cinco maneras; la fila conserva su índice ciego en todas.
//
// Get y List son el fallo BLANDO: sirven la sesión con el número vacío y SIN error (un campo
// ilegible no tumba el listado), y no reescriben la fila. CountLiveBySelfPn la sigue contando: va
// por el índice ciego, no por el sobre. PendingGreeting es el fallo DURO: ahí el número es el
// destino del mensaje, así que devuelve ("", false, error) con su texto literal —el del sobre a
// medias, o el de la fila con índice y sin sobre— y no «no está pendiente».
func TestFleetContract_BrokenEnvelope_ReadsServeNoNumberAndTheGreetingFails(t *testing.T) {
	cases := []struct {
		name      string
		statement string
		want      string
	}{
		{"enc missing", fleetContractDropEnc, fleetContractIncompleteEnvelope},
		{"dek missing", fleetContractDropDek, fleetContractIncompleteEnvelope},
		{"kek_id missing", fleetContractDropKekID, fleetContractIncompleteEnvelope},
		{"empty enc", fleetContractEmptyEnc, fleetContractIncompleteEnvelope},
		{"blind index without envelope", fleetContractDropEnvelope, fleetContractIndexWithoutEnv},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			keyring := fleetContractKeyID + ":" + clavesSecretoB64(t)
			rig := newFleetContractRig(t, newFleetContractDB(t), keyring, fleetContractKeyID, clavesSecretoB64(t))
			tenant, edge, session := newFleetContractSession(t, rig)
			setFleetContractSelfPn(t, rig, tenant, edge, session, fleetContractSelfPn)
			requireFleetContractPending(t, rig, tenant, edge, session, fleetContractSelfPn, "con el sobre entero")

			execFleetContractRow(t, rig.db, c.statement, tenant, edge, session)
			freezeFleetContractRow(t, rig.db, fleetContractFreezeUpdatedAt, tenant, edge, session)
			if broken := readFleetContractRow(t, rig.db, tenant, edge, session); !broken.bidx.Valid {
				t.Fatalf("la siembra se llevó el índice ciego: %s", broken.json)
			}

			requireFleetContractSelfPn(t, rig, tenant, edge, session, "")
			listed, err := rig.m.Repository.List(t.Context(), tenant)
			if err != nil || len(listed) != 1 || listed[0].SessionID != session || listed[0].SelfPn != "" {
				t.Errorf("List = (%d sesiones, err %v); quería la sesión, con el número vacío y sin error", len(listed), err)
			}
			requireFleetContractCount(t, rig, tenant, fleetContractSelfPn, 1)

			to, pending, err := rig.greeter.PendingGreeting(t.Context(), tenant, edge, session)
			if to != "" || pending || err == nil || err.Error() != c.want {
				t.Errorf("PendingGreeting = (%q, %v, %v); quería (\"\", false, %q)", to, pending, err, c.want)
			}

			after := readFleetContractGreeting(t, rig.db, tenant, edge, session)
			if !after.updatedAt.Equal(fleetContractFrozenAt) || after.greetedAt.Valid {
				t.Errorf("leer una fila con el sobre roto la reescribió: updated_at %v (quería %v), greeted_at válida %v",
					after.updatedAt, fleetContractFrozenAt, after.greetedAt.Valid)
			}
		})
	}
}
