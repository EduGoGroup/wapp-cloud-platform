//go:build integracion

package procesos

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake/intakehelpertest"
)

// Este fichero corre las TRES suites de contrato de la tabla `intake_jobs`
// (intakehelpertest.ContratoQueue, ContratoMachine y ContratoReanalysis) contra intake.Postgres
// sobre una base clonada de la plantilla migrada (T7.27 = T9.28). Como tenantvars_contrato_test.go,
// no es un proceso de caja negra: es la excepción de R9.4.d que decidió D-F1-8, y por eso solo
// importa la suite y el paquete del adaptador, del que solo usa el constructor (candado
// ProcessImports, regla 3). Un mismo intake.NewPostgres(db) sirve a las tres: es la cola
// (JobStore), la máquina (PipelineStore) y el segundo productor (los dos métodos del re-análisis).
//
// Del paquete intake SOLO se nombra NewPostgres (regla 3b). Por eso la fila se rellena y se lee
// CAMPO A CAMPO (r.Key.TenantID, r.SourceText.Enc, r.Reanalysis.From) sin nombrar los tipos de la
// clave, del sobre ni del contexto, y los estados son literales de cadena.
//
// Es la que prueba de verdad contra public.intake_jobs (migraciones 0072, 0078 y 0080) lo que los
// unitarios del adaptador, con su driver de mentira, solo pueden mirar como texto de la sentencia
// (hallazgo 11 de F7): las guardas `status =`, `next_attempt_at <= now()` y `tenant_id = $1` de
// los reclamos, el predicado del ON CONFLICT sobre el índice único parcial de la ventana viva, el
// vaciado de las tres columnas del sobre al entrar en un terminal y el `IS NULL` del sobre.
//
// 🔴 El lector lee la fila ENTERA —las veintitrés columnas de intakehelpertest.Row—: la marca de
// estado de las suites es la fila completa (hallazgo 35), y una columna que el lector no trajera
// sería una columna que una sentencia sin su guarda podría pisar sin que nadie lo viera.
//
// Requisitos del montaje (hallazgo 10 de F7): la tabla llega VACÍA a cada caso —una base por
// caso; ClaimNext y ListAggregating no filtran por tenant—, Rows(t, "") devuelve cero filas (el
// WHERE por tenant no casa ninguna), Seed inserta la fila tal cual con los ceros como NULL, y Now
// es el now() de la base, que es contra el que los reclamos comparan next_attempt_at.
//
// tenant_id es TEXT sin clave foránea: no hay fila de tenants que sembrar.

const (
	// intakeContractTimeout acota cada sentencia de la siembra y de la observación: la base es
	// local al contenedor.
	intakeContractTimeout = 15 * time.Second
	// intakeContractClockTries es el tope de lecturas del reloj en un Advance (ver
	// tenantvarsClockTries: con una basta; el tope evita un bucle sin fin).
	intakeContractClockTries = 1000
	// intakeContractColumns son las columnas de la fila entera, en el orden en que las escanea
	// intakeContractScanRow. Los NULL de texto y de entero salen ya como el valor cero de Row; los
	// de instante y los del sobre se resuelven al escanear.
	intakeContractColumns = `
		id::text, tenant_id, session_id, contact_id, event_id::text, status, COALESCE(stage, ''),
		message_ts, source_refs::text, source_text_enc, source_text_dek, COALESCE(source_text_kek_id, ''),
		artifacts::text, COALESCE(error, ''), COALESCE(intake_id::text, ''), attempts, next_attempt_at,
		created_at, updated_at, COALESCE(requested_by, ''), COALESCE(reanalysis_via, ''),
		COALESCE(reanalysis_source, ''), COALESCE(reanalyzed_from, 0)`
)

// intakeContractCases cuenta los Montajes pedidos en esta corrida, entre las tres suites: cada una
// llama a nuevo una vez por caso (en serie) y cada caso necesita una base con nombre propio.
var intakeContractCases atomic.Int64

// TestIntakeContratoQueue_Postgres corre las promesas de la cola (intake.JobStore), las mismas que
// pasan contra intake.MemoryStore, contra el adaptador Postgres. Cada caso recibe su base, con la
// tabla vacía, y un Postgres nuevo.
func TestIntakeContratoQueue_Postgres(t *testing.T) {
	intakehelpertest.ContratoQueue(t, func(t *testing.T) intakehelpertest.QueueMontaje {
		t.Helper()
		db, table := intakeContractNewTable(t)
		return intakehelpertest.QueueMontaje{
			Store:   intake.NewPostgres(db),
			TenantA: table.TenantA,
			TenantB: table.TenantB,
			Rows: func(t *testing.T, tenantID string) []intakehelpertest.Row {
				t.Helper()
				return intakeContractRows(t, db, tenantID)
			},
			Seed:    table.Seed,
			Advance: table.Advance,
		}
	})
}

// TestIntakeContratoMachine_Postgres corre las promesas de la máquina (intake.PipelineStore), las
// mismas que pasan contra intakehelpertest.MachineMemory, contra el adaptador Postgres.
func TestIntakeContratoMachine_Postgres(t *testing.T) {
	intakehelpertest.ContratoMachine(t, func(t *testing.T) intakehelpertest.MachineMontaje {
		t.Helper()
		db, table := intakeContractNewTable(t)
		return intakehelpertest.MachineMontaje{Store: intake.NewPostgres(db), Table: table}
	})
}

// TestIntakeContratoReanalysis_Postgres corre las promesas del segundo productor de jobs
// (LiveJobOfEvent y OpenReanalysis), las mismas que pasan contra intakehelpertest.MachineMemory,
// contra el adaptador Postgres.
func TestIntakeContratoReanalysis_Postgres(t *testing.T) {
	intakehelpertest.ContratoReanalysis(t, func(t *testing.T) intakehelpertest.ReanalysisMontaje {
		t.Helper()
		db, table := intakeContractNewTable(t)
		return intakehelpertest.ReanalysisMontaje{Store: intake.NewPostgres(db), Table: table}
	})
}

// intakeContractNewTable devuelve lo que un caso de cualquiera de las tres suites pide de la
// tabla: clona una base con nuevaBase (que la borra en el Cleanup del subtest), la abre con el
// arnés —la única conexión del caso, D-F9-6— y monta sobre ese *sql.DB la siembra, el lector y el
// reloj. Devuelve también el *sql.DB para que el llamante construya el adaptador sobre el MISMO.
// Los dos tenants llevan el número del caso, aunque con una base por caso ya vendrían sin filas.
func intakeContractNewTable(t *testing.T) (*sql.DB, intakehelpertest.Table) {
	t.Helper()
	n := intakeContractCases.Add(1)
	db := nuevaBase(t, fmt.Sprintf("intake_contrato_%02d", n)).Abrir(t)

	return db, intakehelpertest.Table{
		TenantA: fmt.Sprintf("intake-contrato-%02d-a", n),
		TenantB: fmt.Sprintf("intake-contrato-%02d-b", n),
		Seed: func(t *testing.T, r intakehelpertest.Row) string {
			t.Helper()
			return intakeContractSeed(t, db, r)
		},
		Row: func(t *testing.T, id string) intakehelpertest.Row {
			t.Helper()
			return intakeContractRow(t, db, id)
		},
		Now:     func(t *testing.T) time.Time { return intakeContractNow(t, db) },
		Advance: func(t *testing.T) { intakeContractAdvance(t, db) },
	}
}

// intakeContractNullText convierte un texto en lo que la columna anulable espera: "" es NULL.
func intakeContractNullText(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

// intakeContractNullTime convierte un instante en lo que la columna anulable espera: el cero es
// NULL. El instante viaja en UTC.
func intakeContractNullTime(at time.Time) sql.NullTime {
	return sql.NullTime{Time: at.UTC(), Valid: !at.IsZero()}
}

// intakeContractNullBytes convierte una mitad del sobre en lo que su columna BYTEA espera: vacía
// es NULL (y no un bytea de longitud cero, que el `IS NULL` del adaptador no vería igual).
func intakeContractNullBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

// intakeContractSeed es el Seed del Montaje: inserta la fila TAL CUAL en public.intake_jobs y
// devuelve su id.
//
//   - El id es el de la fila si lo trae; si viene vacío lo pone la base (gen_random_uuid()).
//   - Los ceros de las columnas anulables viajan como NULL: stage, message_ts, las tres del sobre,
//     error, intake_id y las cuatro del re-análisis (reanalyzed_from 0 es NULL: el 0 no se escribe
//     nunca, migración 0080).
//   - source_refs y artifacts son NOT NULL: sin referencias es `[]` y sin artefactos es `{}`, los
//     dos valores por defecto de la tabla.
//   - Las suites siembran siempre con status, created_at, updated_at y next_attempt_at puestos. Si
//     alguno llegara a cero, vale lo que vale en el doble en memoria: `pending`, el now() de la
//     base, el created_at y —next_attempt_at es NOT NULL DEFAULT now()— el now() de la base.
//
// Un status o un stage fuera del vocabulario cerrado lo rechaza el CHECK de la 0072 y falla el
// test: la suite solo siembra los del puerto. Todo parámetro va con su tipo EXPLÍCITO, porque
// dentro de un COALESCE Postgres no deduce el tipo de un parámetro por la columna de destino.
func intakeContractSeed(t *testing.T, db *sql.DB, r intakehelpertest.Row) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), intakeContractTimeout)
	defer cancel()

	refs := r.SourceRefs
	if refs == nil {
		refs = []string{}
	}
	rawRefs, err := json.Marshal(refs)
	if err != nil {
		t.Fatalf("serializar source_refs de la siembra: %v", err)
	}
	artifacts := r.Artifacts
	if artifacts == nil {
		artifacts = map[string]json.RawMessage{}
	}
	rawArtifacts, err := json.Marshal(artifacts)
	if err != nil {
		t.Fatalf("serializar artifacts de la siembra: %v", err)
	}

	var id string
	err = db.QueryRowContext(ctx, `
		INSERT INTO public.intake_jobs
			(id, tenant_id, session_id, contact_id, event_id, status, stage, message_ts,
			 source_refs, source_text_enc, source_text_dek, source_text_kek_id, artifacts, error,
			 intake_id, attempts, next_attempt_at, created_at, updated_at,
			 requested_by, reanalysis_via, reanalysis_source, reanalyzed_from)
		VALUES
			(COALESCE($1::uuid, gen_random_uuid()), $2::text, $3::text, $4::text, $5::uuid,
			 COALESCE($6::text, 'pending'), $7::text, $8::timestamptz,
			 $9::jsonb, $10::bytea, $11::bytea, $12::text, $13::jsonb, $14::text,
			 $15::uuid, $16::integer, COALESCE($17::timestamptz, now()),
			 COALESCE($18::timestamptz, now()), COALESCE($19::timestamptz, $18::timestamptz, now()),
			 $20::text, $21::text, $22::text, NULLIF($23::integer, 0))
		RETURNING id::text`,
		intakeContractNullText(r.ID), r.Key.TenantID, r.Key.SessionID, r.Key.ContactID, r.Key.EventID,
		intakeContractNullText(r.Status), intakeContractNullText(r.Stage), intakeContractNullTime(r.MessageTS),
		string(rawRefs), intakeContractNullBytes(r.SourceText.Enc), intakeContractNullBytes(r.SourceText.DEK),
		intakeContractNullText(r.SourceText.KEKID), string(rawArtifacts), intakeContractNullText(r.Error),
		intakeContractNullText(r.IntakeID), r.Attempts, intakeContractNullTime(r.NextAttemptAt),
		intakeContractNullTime(r.CreatedAt), intakeContractNullTime(r.UpdatedAt),
		intakeContractNullText(r.Reanalysis.RequestedBy), intakeContractNullText(r.Reanalysis.Via),
		intakeContractNullText(r.Reanalysis.Source), r.Reanalysis.From,
	).Scan(&id)
	if err != nil {
		t.Fatalf("sembrar el job %q (tenant %q, estado %q): %v", r.ID, r.Key.TenantID, r.Status, err)
	}
	return id
}

// intakeContractScanner es lo que comparten *sql.Row y *sql.Rows: el lector de una fila sirve a la
// lectura por id y al recorrido de las de un tenant.
type intakeContractScanner interface {
	Scan(dest ...any) error
}

// intakeContractScanRow lee una fila ENTERA de public.intake_jobs, en el orden de
// intakeContractColumns. message_ts NULL llega como instante cero; las dos mitades BYTEA del sobre,
// como nil; source_refs y artifacts se decodifican de su texto JSON (`[]` y `{}` quedan vacíos).
// No pasa por ningún método del adaptador, que es lo que se prueba. Devuelve el error del Scan sin
// envolver, para que el llamante distinga sql.ErrNoRows.
func intakeContractScanRow(s intakeContractScanner) (intakehelpertest.Row, error) {
	var (
		r         intakehelpertest.Row
		messageTS sql.NullTime
		rawRefs   string
		rawArts   string
	)
	if err := s.Scan(
		&r.ID, &r.Key.TenantID, &r.Key.SessionID, &r.Key.ContactID, &r.Key.EventID, &r.Status, &r.Stage,
		&messageTS, &rawRefs, &r.SourceText.Enc, &r.SourceText.DEK, &r.SourceText.KEKID,
		&rawArts, &r.Error, &r.IntakeID, &r.Attempts, &r.NextAttemptAt,
		&r.CreatedAt, &r.UpdatedAt, &r.Reanalysis.RequestedBy, &r.Reanalysis.Via,
		&r.Reanalysis.Source, &r.Reanalysis.From,
	); err != nil {
		return intakehelpertest.Row{}, err
	}
	r.MessageTS = messageTS.Time // NULL ⇒ instante cero
	if err := json.Unmarshal([]byte(rawRefs), &r.SourceRefs); err != nil {
		return intakehelpertest.Row{}, fmt.Errorf("decodificar source_refs del job %s: %w", r.ID, err)
	}
	if err := json.Unmarshal([]byte(rawArts), &r.Artifacts); err != nil {
		return intakehelpertest.Row{}, fmt.Errorf("decodificar artifacts del job %s: %w", r.ID, err)
	}
	return r, nil
}

// intakeContractRow es el Row del Montaje: la fila entera de ese id. Compara el id como texto para
// que uno que no sea UUID sea «no existe» y no un error de sintaxis. Falla el test si no existe:
// las suites solo preguntan por las filas que sembraron o que el puerto les devolvió.
func intakeContractRow(t *testing.T, db *sql.DB, id string) intakehelpertest.Row {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), intakeContractTimeout)
	defer cancel()
	r, err := intakeContractScanRow(db.QueryRowContext(ctx,
		`SELECT `+intakeContractColumns+` FROM public.intake_jobs WHERE id::text = $1`, id))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		t.Fatalf("Row(%q): no existe ese job en public.intake_jobs", id)
	case err != nil:
		t.Fatalf("Row(%q): %v", id, err)
	}
	return r
}

// intakeContractRows es el Rows del Montaje de la cola: TODAS las filas del tenant, enteras. El
// orden no es del contrato (la suite busca por clave); van por (created_at, id) para que un fallo
// se lea igual en dos corridas. Con el tenant vacío no casa ninguna fila y devuelve cero, que es
// lo que piden los casos de la clave incompleta (hallazgo 7 de F7).
func intakeContractRows(t *testing.T, db *sql.DB, tenantID string) []intakehelpertest.Row {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), intakeContractTimeout)
	defer cancel()
	rows, err := db.QueryContext(ctx,
		`SELECT `+intakeContractColumns+` FROM public.intake_jobs WHERE tenant_id = $1 ORDER BY created_at, id`,
		tenantID)
	if err != nil {
		t.Fatalf("Rows(tenant %q): %v", tenantID, err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			t.Errorf("Rows(tenant %q): cerrar las filas: %v", tenantID, cerr)
		}
	}()

	found := make([]intakehelpertest.Row, 0)
	for rows.Next() {
		r, err := intakeContractScanRow(rows)
		if err != nil {
			t.Fatalf("Rows(tenant %q): leer una fila: %v", tenantID, err)
		}
		found = append(found, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("Rows(tenant %q): recorrer las filas: %v", tenantID, err)
	}
	return found
}

// intakeContractNow es el Now del Montaje: el now() de la base, que es el reloj contra el que los
// reclamos comparan next_attempt_at y con el que el adaptador fecha lo que escribe. Fuera de una
// transacción, now() es el instante de la propia sentencia.
func intakeContractNow(t *testing.T, db *sql.DB) time.Time {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), intakeContractTimeout)
	defer cancel()
	var now time.Time
	if err := db.QueryRowContext(ctx, `SELECT now()`).Scan(&now); err != nil {
		t.Fatalf("leer el now() de la base: %v", err)
	}
	return now
}

// intakeContractAdvance es el Advance del Montaje contra Postgres: vuelve cuando el reloj de la
// BASE ha pasado del instante en que estaba al entrar.
//
// El adaptador fecha con now(), que es el instante en que empezó la sentencia. Toda escritura
// anterior a esta llamada empezó antes de la primera lectura de aquí, y toda escritura posterior
// empezará después de la última, que es estrictamente mayor: sus marcas no pueden coincidir. No se
// duerme: se le pregunta la hora a la base hasta que cambia, y con resolución de microsegundo y un
// viaje de red por lectura cambia a la primera.
func intakeContractAdvance(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), intakeContractTimeout)
	defer cancel()
	start := intakeContractClock(ctx, t, db)
	for range intakeContractClockTries {
		if intakeContractClock(ctx, t, db).After(start) {
			return
		}
	}
	t.Fatalf("el reloj de la base no pasó de %v en %d lecturas", start, intakeContractClockTries)
}

// intakeContractClock lee el reloj de pared de la base (clock_timestamp, que avanza dentro de una
// misma transacción, no now()).
func intakeContractClock(ctx context.Context, t *testing.T, db *sql.DB) time.Time {
	t.Helper()
	var now time.Time
	if err := db.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		t.Fatalf("leer el reloj de la base: %v", err)
	}
	return now
}
