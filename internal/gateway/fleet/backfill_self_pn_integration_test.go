package fleet_test

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// Integración de BackfillSelfPn (Plan 046 · T4.1, criterio (a)): el paso de arranque
// que cifra las filas anteriores a la migración 0068 y VACÍA la columna en claro.
// Mismo gate WAPP_TEST_DB_DSN que el resto del paquete.
//
// 🔴 POR QUÉ ESTO NO PODÍA SEGUIR SIN TESTS. BackfillSelfPn son ~340 líneas con
// transacción por lote, cursor keyset y compare-and-swap, y corre EN EL ARRANQUE del
// servidor, antes de aceptar tráfico. Sus tres modos de fallo son mudos o mortales:
// un cursor mal llevado cuelga el arranque en un bucle infinito (el supervisor
// reinicia, vuelve a colgarse); un centinela mal elegido re-cifra la tabla entera en
// CADA boot sin que nada lo delate (el bidx es determinista, así que todo lo demás
// sigue funcionando); y una normalización distinta a la de SetSelfPn parte el índice
// ciego en dos poblaciones que ya nadie puede reconciliar, porque el valor en claro
// con el que compararlas es justo lo que este backfill borra.
//
// ── HIGIENE DEL TABLERO ───────────────────────────────────────────────────────────
// El barrido es GLOBAL (no filtra por tenant: en producción tiene que alcanzar toda
// la flota), así que sus contadores solo son deterministas con el tablero limpio. Es
// el mismo problema —y la misma solución— que wipeCifradas en la suite de rotación de
// crypto, y por eso se limpia NULIFICANDO la columna en claro en vez de borrar filas:
// borrarlas sería tirar el estado de flota que otros casos dejaron sembrado.

// TestIntegration_BackfillSelfPn_CifraVaciaElClaroYEsIdempotente cubre el camino
// feliz completo del criterio (a): filas EN CLARO ⇒ cifradas, con `self_pn` a NULL, en
// VARIOS lotes, y el segundo arranque como no-op PERFECTO.
//
// 💥 MUTACIONES QUE LO PONEN ROJO, una por aserción:
//   - quitar `self_pn = NULL` del UPDATE (backfill_self_pn.go:122) ⇒ las filas quedan
//     cifradas Y en claro a la vez, y la fase 1 falla. Sin esa aserción el fallo sería
//     PERMANENTE y mudo: el centinela del backfill es el BIDX, así que la fila ya no
//     vuelve a entrar en ninguna pasada y el teléfono se queda en claro para siempre
//     —el backfill habría "corrido bien", con su contador a cero pendientes—.
//   - cambiar el centinela de `self_pn_bidx IS NULL` a `self_pn_enc IS NULL`
//     (backfill_self_pn.go:92) ⇒ la 2ª pasada re-cifra (nonce fresco) y la fase 2 caza
//     que `self_pn_enc` cambió byte a byte, que es la ÚNICA huella que deja esa
//     escritura muda.
//   - no avanzar el cursor entre lotes (o quedarse con el primer lote) ⇒ con 6 filas y
//     batch=2 el conteo no llega a 6.
//   - quitar normalizeSelfPn de selfPnEnvelope ⇒ la fila sembrada con grafía adornada
//     deja de casar con el lector en la fase 3.
func TestIntegration_BackfillSelfPn_CifraVaciaElClaroYEsIdempotente(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, db)
	repo, kp := repoDePrueba(t, db)
	limpiarPendientesDeBackfill(ctx, t, db)

	const edgeID = "edge-backfill"
	const adornada = "s-backfill-adornada"
	sesiones := make([]string, 0, 6)
	for i := 0; i < 5; i++ {
		sessionID := fmt.Sprintf("s-backfill-%d", i)
		sesiones = append(sesiones, sessionID)
		sembrarFilaEnClaro(ctx, t, db, tenantID, edgeID, sessionID, fmt.Sprintf("5698446700%d", i))
	}
	// La sexta va con GRAFÍA ADORNADA, como la escribiría un Edge viejo: el backfill
	// tiene que normalizarla igual que SetSelfPn o su bidx queda huérfano.
	sesiones = append(sesiones, adornada)
	sembrarFilaEnClaro(ctx, t, db, tenantID, edgeID, adornada, grafiasDelMismoNumero[2].escrito)

	// batch=2 con SEIS filas ⇒ TRES lotes y una cuarta vuelta vacía. Un backfill que
	// solo procesara el primer lote pasaría con batch por defecto (500) y fallaría aquí.
	rep, err := repo.BackfillSelfPn(ctx, 2)
	if err != nil {
		t.Fatalf("BackfillSelfPn (1ª pasada): %v", err)
	}
	if rep.Encrypted != len(sesiones) {
		t.Fatalf("1ª pasada: Encrypted = %d, want %d (seis filas en claro, batch=2 ⇒ tres lotes; "+
			"si sale 2, el cursor no avanza entre lotes)", rep.Encrypted, len(sesiones))
	}
	if rep.Skipped != 0 {
		t.Fatalf("1ª pasada: Skipped = %d, want 0 (los seis números normalizan)", rep.Skipped)
	}

	faseBackfillFilasCifradasYSinClaro(ctx, t, db, tenantID, edgeID, sesiones)
	faseBackfillSegundaPasadaEsNoOp(ctx, t, db, repo, tenantID, edgeID, adornada)
	faseBackfillElLectorEncuentraElNumero(ctx, t, db, repo, kp, tenantID, edgeID, adornada)
}

// TestIntegration_BackfillSelfPn_OmiteLoQueNoNormalizaYTermina es el modo de fallo
// DECIDIDO del backfill: una fila cuyo `self_pn` no es un teléfono se OMITE, se CUENTA
// y NO tumba el arranque —abortar aquí sería un bucle de arranque: muere, el supervisor
// levanta, vuelve a leer la misma fila, vuelve a morir—. Y, sobre todo, EL BARRIDO
// TERMINA: la fila omitida sigue casando el centinela después de procesarla.
//
// ⏱️ El ctx lleva DEADLINE a propósito. Si el barrido perdiera el cursor (un `LIMIT`
// pelado, que es lo que uno escribe primero), la fila omitida se releería para
// siempre: sin deadline el test no falla, se CUELGA, y una suite colgada se diagnostica
// mucho peor que una roja. Con él, el bucle infinito se manifiesta como un error de
// contexto en la aserción de abajo.
//
// 💥 MUTACIÓN QUE LO PONE ROJO: sustituir el cursor keyset de selfPnCursor por un
// `LIMIT` sin `(tenant_id, edge_id, session_id) > ($1,$2,$3)` ⇒ BackfillSelfPn no
// vuelve nunca y el ctx revienta. Y convertir el `continue` de la rama
// contact.ErrInvalidRef en un `return err` (abortar) ⇒ el error sube y el arranque
// queda bloqueado por un dato basura de UNA sesión.
func TestIntegration_BackfillSelfPn_OmiteLoQueNoNormalizaYTermina(t *testing.T) {
	db := openTestDB(t)
	tenantID := seedTenant(t, db)
	repo, _ := repoDePrueba(t, db)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	limpiarPendientesDeBackfill(ctx, t, db)

	const edgeID = "edge-basura"
	const sesionBasura = "s-no-normaliza"
	// Sin un solo dígito ⇒ contact.Normalize devuelve ErrInvalidRef. El orden del
	// barrido es (tenant_id, edge_id, session_id), así que "s-buena" se procesa ANTES
	// que "s-no-normaliza": la fila omitida queda la ÚLTIMA, que es el caso que de
	// verdad puede colgar el bucle.
	sembrarFilaEnClaro(ctx, t, db, tenantID, edgeID, "s-buena", numeroPropioNormalizado)
	sembrarFilaEnClaro(ctx, t, db, tenantID, edgeID, sesionBasura, "no-es-un-telefono")
	// La fila omitida CONSERVA su valor en claro por diseño, así que es un residuo
	// permanente en la base compartida: si no se retira, los contadores de la próxima
	// corrida de esta suite (o de otra) contarían un Skipped que no es suyo.
	t.Cleanup(func() { limpiarPendientesDeBackfill(context.Background(), t, db) })

	// batch=1: cada fila ocupa un lote entero, que es el peor caso para el cursor.
	rep, err := repo.BackfillSelfPn(ctx, 1)
	if err != nil {
		t.Fatalf("BackfillSelfPn con una fila que no normaliza: %v "+
			"(si es un deadline de contexto, el barrido NO termina: el cursor no avanza sobre la fila omitida "+
			"y el arranque del servidor se quedaría colgado en bucle)", err)
	}
	if rep.Encrypted != 1 {
		t.Fatalf("Encrypted = %d, want 1 (la fila buena se cifra aunque su vecina sea basura)", rep.Encrypted)
	}
	if rep.Skipped != 1 {
		t.Fatalf("Skipped = %d, want 1: el contador es lo ÚNICO que le dice al operador que el criterio (a) "+
			"NO se cumple todavía (esa fila conserva su teléfono en claro)", rep.Skipped)
	}

	basura := leerSobreCrudo(ctx, t, db, tenantID, edgeID, sesionBasura)
	if !basura.enClaro.Valid {
		t.Fatal("la fila omitida NO debe perder su valor: vaciarla sin cifrarla borraría el dato para siempre")
	}
	if basura.bidx.Valid {
		t.Fatal("la fila omitida no puede acabar con índice ciego: no se pudo normalizar, así que no hay nada que indexar")
	}
}

// faseBackfillFilasCifradasYSinClaro comprueba, fila a fila y por SQL directo, las dos
// mitades del criterio (a): ya no hay claro Y sí hay sobre. La segunda mitad no es
// redundante — vaciar sin cifrar también daría cero en la primera, y sería una pérdida
// de datos silenciosa (es la misma pareja de consultas V3 de la migración 0068).
//
// Extraída y NOMBRADA por gocyclo (umbral 15, que aplica también a los tests).
func faseBackfillFilasCifradasYSinClaro(
	ctx context.Context, t *testing.T, db *sql.DB, tenantID, edgeID string, sesiones []string,
) {
	t.Helper()
	for _, sessionID := range sesiones {
		s := leerSobreCrudo(ctx, t, db, tenantID, edgeID, sessionID)
		if s.enClaro.Valid {
			t.Fatalf("%s: self_pn sigue en claro tras el backfill (criterio (a) incumplido)", sessionID)
		}
		if !s.bidx.Valid || !s.kekID.Valid || len(s.enc) == 0 || len(s.dek) == 0 {
			t.Fatalf("%s: el backfill vació el claro pero no dejó el sobre ENTERO "+
				"(bidx=%v kek=%v enc=%d dek=%d): eso es perder el dato, no protegerlo",
				sessionID, s.bidx.Valid, s.kekID.Valid, len(s.enc), len(s.dek))
		}
	}
}

// faseBackfillSegundaPasadaEsNoOp: el arranque siguiente no toca NADA. Se afirma por
// las dos vías, y la segunda es la que importa — un contador a 0 lo daría igual un
// backfill que reescribiera las filas y no supiera contarlas; el envelope INTACTO byte
// a byte no lo da nada más que no haber escrito.
func faseBackfillSegundaPasadaEsNoOp(
	ctx context.Context, t *testing.T, db *sql.DB,
	repo *fleet.PostgresRepository, tenantID, edgeID, sessionID string,
) {
	t.Helper()
	antes := leerSobreCrudo(ctx, t, db, tenantID, edgeID, sessionID)
	rep, err := repo.BackfillSelfPn(ctx, 2)
	if err != nil {
		t.Fatalf("BackfillSelfPn (2ª pasada): %v", err)
	}
	if rep.Encrypted != 0 || rep.Skipped != 0 {
		t.Fatalf("2ª pasada: Encrypted=%d Skipped=%d, want 0 y 0 (reejecutar tiene que ser un no-op)",
			rep.Encrypted, rep.Skipped)
	}
	despues := leerSobreCrudo(ctx, t, db, tenantID, edgeID, sessionID)
	if !bytes.Equal(antes.enc, despues.enc) {
		t.Fatal("el envelope cambió en la 2ª pasada: el backfill re-cifró una fila ya cifrada. " +
			"Como el nonce es fresco por escritura y el bidx es determinista, TODO lo demás seguiría " +
			"funcionando y la tabla se reescribiría entera en cada boot sin una sola línea que lo delate")
	}
}

// faseBackfillElLectorEncuentraElNumero cierra el círculo del backfill: lo que escribió
// NO es solo «algo cifrado», es el MISMO índice ciego que interroga el anti-self-loop.
// Es la simetría de self_pn_cifrado_integration_test.go aplicada al TERCER escritor del
// bidx (SetSelfPn, el backfill y —desde la 0068— nadie más), y el que más fácil se
// desincroniza: corre una vez, en el arranque, y nadie lo mira.
func faseBackfillElLectorEncuentraElNumero(
	ctx context.Context, t *testing.T, db *sql.DB, repo *fleet.PostgresRepository,
	kp crypto.KeyProvider, tenantID, edgeID, sessionID string,
) {
	t.Helper()
	s, found, err := repo.Get(ctx, tenantID, edgeID, sessionID)
	if err != nil || !found {
		t.Fatalf("Get tras el backfill: found=%v err=%v", found, err)
	}
	if s.SelfPn != numeroPropioNormalizado {
		t.Fatalf("el backfill tiene que dejar el número NORMALIZADO: got %q, want %q "+
			"(la fila se sembró con grafía adornada)", s.SelfPn, numeroPropioNormalizado)
	}
	propio, err := runtime.NewPostgresSelfNumbers(db, kp).IsSelfNumber(ctx, tenantID, numeroPropioNormalizado)
	if err != nil {
		t.Fatalf("IsSelfNumber tras el backfill: %v", err)
	}
	if !propio {
		t.Fatal("el anti-self-loop NO reconoce un número que acaba de cifrar el backfill: " +
			"el backfill y el lector normalizan distinto y la guarda quedó muda para toda fila migrada")
	}
}

// limpiarPendientesDeBackfill deja el tablero SIN filas «en claro y sin cifrar». El
// barrido es global, así que sin esto los contadores del Report no son deterministas:
// una fila omitida que dejó otro caso (o una corrida anterior contra la misma base)
// sumaría a Skipped. Se NULIFICA la columna en vez de borrar la fila, por lo mismo que
// wipeCifradas en la suite de rotación: la fila es estado de flota de otro test, lo que
// contamina es su valor en claro.
func limpiarPendientesDeBackfill(ctx context.Context, t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		UPDATE public.fleet_sessions SET self_pn = NULL
		WHERE self_pn IS NOT NULL AND self_pn <> '' AND self_pn_bidx IS NULL
	`); err != nil {
		t.Fatalf("limpiar filas pendientes de backfill: %v", err)
	}
}

// sembrarFilaEnClaro inserta una fila TAL Y COMO la dejó el mundo anterior a la 0068:
// el teléfono en la columna `self_pn`, sin ninguna de las cuatro columnas del sobre.
// No se puede sembrar con SetSelfPn —que es justamente el escritor que ya cifra—, así
// que va por SQL directo: es el único sitio del árbol de tests que escribe un teléfono
// en claro en esta tabla, y existe para poder probar que el backfill lo retira.
//
// El ON CONFLICT resetea el sobre además del claro: sin eso, una segunda corrida sobre
// la misma base encontraría la fila ya cifrada y el caso probaría cero.
//
// El perfil se fija ACTIVE explícito (el DEFAULT es 'passive' y una pasiva no bloquea:
// la fase del lector quedaría probando otra cosa).
func sembrarFilaEnClaro(ctx context.Context, t *testing.T, db *sql.DB, tenantID, edgeID, sessionID, enClaro string) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.fleet_sessions
			(tenant_id, edge_id, session_id, state, profile, self_pn, last_connected_at, last_seen_at, updated_at)
		VALUES ($1, $2, $3, 'online', 'active', $4, now(), now(), now())
		ON CONFLICT (tenant_id, edge_id, session_id) DO UPDATE
		SET profile        = 'active',
		    self_pn        = EXCLUDED.self_pn,
		    self_pn_enc    = NULL,
		    self_pn_dek    = NULL,
		    self_pn_kek_id = NULL,
		    self_pn_bidx   = NULL
	`, tenantID, edgeID, sessionID, enClaro); err != nil {
		t.Fatalf("sembrar fila con self_pn en claro (%s): %v", sessionID, err)
	}
}
