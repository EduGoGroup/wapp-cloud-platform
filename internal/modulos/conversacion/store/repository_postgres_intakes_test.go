package store

import (
	"context"
	"database/sql/driver"
	"testing"

	"github.com/google/uuid"
)

// Aserciones de compilación de lo que promete repository_postgres_intakes.go.
var (
	_ func(*PostgresRepository, context.Context, Intake) error                         = (*PostgresRepository).UpsertIntake
	_ func(*PostgresRepository, context.Context, string, string) (Intake, bool, error) = (*PostgresRepository).GetOpenIntake
	_ func(*PostgresRepository, context.Context, string, string) (Intake, bool, error) = (*PostgresRepository).GetIntakeByEvent
	_ func(*PostgresRepository, context.Context, string) ([]IntakeItem, error)         = (*PostgresRepository).ListIntakeItems
	_ func(*PostgresRepository, context.Context, string, []IntakeItem) error           = (*PostgresRepository).ReplaceIntakeItems
	_ func(*PostgresRepository, context.Context, string, string, float64) error        = (*PostgresRepository).MarkIntakeStatus
	_ func(*PostgresRepository, context.Context, IntakeClose) (string, error)          = (*PostgresRepository).CloseIntake
)

// pgHeader es una fila de cabecera con la proyección de las dos lecturas: diez columnas.
func pgHeader(status string, expires, event driver.Value) pgReply {
	return pgOne(pgIntakeID, pgTenant, pgContact, "sess-1", status, float64(7.5), pgAt, pgAt.Add(1), expires, event)
}

// TestPostgres_Intakes_DatabaseFailure_Wrapped: los métodos de una sentencia suelta envuelven el
// fallo de la base con su prefijo literal.
func TestPostgres_Intakes_DatabaseFailure_Wrapped(t *testing.T) {
	runWrappedCases(t, []wrappedCase{
		{"UpsertIntake", "store: upsert solicitud: ", func(t *testing.T, r *PostgresRepository) error {
			return r.UpsertIntake(t.Context(), Intake{ID: pgIntakeID})
		}},
		{"GetOpenIntake", "store: leer solicitud abierta: ", func(t *testing.T, r *PostgresRepository) error {
			_, _, err := r.GetOpenIntake(t.Context(), pgTenant, pgContact)
			return err
		}},
		{"GetIntakeByEvent", "store: leer la solicitud del evento: ", func(t *testing.T, r *PostgresRepository) error {
			_, _, err := r.GetIntakeByEvent(t.Context(), pgTenant, pgEventID)
			return err
		}},
		{"ListIntakeItems", "store: listar líneas de solicitud: ", func(t *testing.T, r *PostgresRepository) error {
			_, err := r.ListIntakeItems(t.Context(), pgIntakeID)
			return err
		}},
		{"MarkIntakeStatus", "store: marcar estado de solicitud: ", func(t *testing.T, r *PostgresRepository) error {
			return r.MarkIntakeStatus(t.Context(), pgIntakeID, "closed", 1)
		}},
	})
}

// TestPostgres_HeaderReads_SameProjection: GetOpenIntake y GetIntakeByEvent mapean la MISMA fila
// de diez columnas al mismo Intake; expires_at y event_id en NULL salen a cero; sin fila es
// found=false sin error; y CustomerNote no se lee nunca. Las dos van acotadas por el tenant.
func TestPostgres_HeaderReads_SameProjection(t *testing.T) {
	reads := []struct {
		name   string
		second string
		read   func(*testing.T, *PostgresRepository) (Intake, bool, error)
	}{
		{"GetOpenIntake", pgContact, func(t *testing.T, r *PostgresRepository) (Intake, bool, error) {
			return r.GetOpenIntake(t.Context(), pgTenant, pgContact)
		}},
		{"GetIntakeByEvent", pgEventID, func(t *testing.T, r *PostgresRepository) (Intake, bool, error) {
			return r.GetIntakeByEvent(t.Context(), pgTenant, pgEventID)
		}},
	}
	for _, tc := range reads {
		t.Run(tc.name, func(t *testing.T) {
			h := newFakeRepository(t, pgHeader("pending_approval", pgAt.Add(2), pgEventID), pgHeader("open", nil, nil))

			got, found, err := tc.read(t, h.repo)
			requireNoError(t, "con todas las columnas", err)
			requireEqual(t, "found", found, true)
			requireEqual(t, "cabecera", got, Intake{ID: pgIntakeID, TenantID: pgTenant, ContactID: pgContact,
				SessionID: "sess-1", Status: "pending_approval", Total: 7.5, EventID: pgEventID,
				CreatedAt: pgAt, UpdatedAt: pgAt.Add(1), ExpiresAt: pgAt.Add(2)})

			got, found, err = tc.read(t, h.repo)
			requireNoError(t, "con NULL", err)
			requireEqual(t, "found con NULL", found, true)
			requireEqual(t, "cabecera con NULL", got, Intake{ID: pgIntakeID, TenantID: pgTenant, ContactID: pgContact,
				SessionID: "sess-1", Status: "open", Total: 7.5, CreatedAt: pgAt, UpdatedAt: pgAt.Add(1)})

			got, found, err = tc.read(t, h.repo) // sin guion: sin filas.
			requireNoError(t, "sin fila", err)
			requireEqual(t, "found sin fila", found, false)
			requireEqual(t, "cabecera sin fila", got, Intake{})

			for _, stmt := range loose(t, h.fake, 3) {
				requireArgs(t, "lectura de cabecera", stmt, pgTenant, tc.second)
			}
		})
	}
}

// TestPostgres_MalformedIDs_NeverReachTheDatabase: un evento que no es un UUID no puede estar en
// la columna (found=false, sin error); un id de solicitud que no lo es, es un ERROR al listar sus
// líneas. Ninguno de los dos llega a la base.
func TestPostgres_MalformedIDs_NeverReachTheDatabase(t *testing.T) {
	h := newFakeRepository(t)
	requirePgUntouched(t, h.fake)
	for _, id := range []string{"", "no-soy-un-uuid"} {
		got, found, err := h.repo.GetIntakeByEvent(t.Context(), pgTenant, id)
		requireNoError(t, "GetIntakeByEvent con un evento malformado", err)
		requireEqual(t, "found", found, false)
		requireEqual(t, "cabecera", got, Intake{})

		items, err := h.repo.ListIntakeItems(t.Context(), id)
		requireErrPrefix(t, "ListIntakeItems con un id malformado", err, `store: listar líneas de solicitud: id "`+id+`" inválido: `)
		requireEqual(t, "líneas junto al error", len(items), 0)
	}
	requirePgUntouched(t, h.fake)
}

// pgItemRows son dos filas de la lectura de líneas: seis columnas.
func pgItemRows() [][]driver.Value {
	return [][]driver.Value{
		{"CAFE", "Café", "sin azúcar", int64(2), float64(2.5), pgAt},
		{"TE", "Té", "", int64(1), float64(2), pgAt.Add(1)},
	}
}

// TestPostgres_ListIntakeItems_MapsRowsAndClosesThem: seis columnas por fila más el IntakeID, que
// es el argumento; sin filas, la lista vacía; y los fallos del recorrido con su texto propio (D-17).
func TestPostgres_ListIntakeItems_MapsRowsAndClosesThem(t *testing.T) {
	h := newFakeRepository(t, pgReply{rows: pgItemRows()}, pgReply{},
		pgReply{rows: pgItemRows(), endErr: errPgBoom}, pgReply{rows: pgItemRows(), closeErr: errPgBoom})

	got, err := h.repo.ListIntakeItems(t.Context(), pgIntakeID)
	requireNoError(t, "ListIntakeItems", err)
	requireEqual(t, "filas", len(got), 2)
	requireEqual(t, "primera línea", got[0], IntakeItem{IntakeID: pgIntakeID, SKU: "CAFE", Label: "Café",
		Customization: "sin azúcar", Qty: 2, UnitPrice: 2.5, AddedAt: pgAt})
	requireEqual(t, "segunda línea", got[1], IntakeItem{IntakeID: pgIntakeID, SKU: "TE", Label: "Té",
		Qty: 1, UnitPrice: 2, AddedAt: pgAt.Add(1)})

	got, err = h.repo.ListIntakeItems(t.Context(), pgIntakeID)
	requireNoError(t, "ListIntakeItems sin filas", err)
	requireEqual(t, "filas sin filas", len(got), 0)

	_, err = h.repo.ListIntakeItems(t.Context(), pgIntakeID)
	requirePgWrapped(t, err, "store: iterar líneas de solicitud: ", errPgBoom)
	got, err = h.repo.ListIntakeItems(t.Context(), pgIntakeID)
	requirePgWrapped(t, err, "store: cerrar filas de líneas: ", errPgBoom)
	requireEqual(t, "líneas junto al error del cierre", len(got), 0)
	requireArgs(t, "ListIntakeItems", loose(t, h.fake, 4)[0], pgIntakeID)
}

// TestPostgres_UpsertIntakeAndMarkStatus_Arguments: UpsertIntake es una sentencia suelta con ocho
// argumentos —la nota NO viaja—, y un ExpiresAt cero y un EventID vacío viajan como NULL.
// MarkIntakeStatus es otra, con (id, estado, total).
func TestPostgres_UpsertIntakeAndMarkStatus_Arguments(t *testing.T) {
	h := newFakeRepository(t, pgReply{}, pgReply{}, pgReply{noneAffected: true})
	in := Intake{ID: pgIntakeID, TenantID: pgTenant, ContactID: pgContact, SessionID: "sess-1", Status: "open",
		Total: 7.5, CustomerNote: "no viaja"}
	requireNoError(t, "UpsertIntake", h.repo.UpsertIntake(t.Context(), in))
	in.EventID, in.ExpiresAt = pgEventID, pgAt
	requireNoError(t, "UpsertIntake con evento y vencimiento", h.repo.UpsertIntake(t.Context(), in))
	requireNoError(t, "MarkIntakeStatus sin filas afectadas", h.repo.MarkIntakeStatus(t.Context(), pgIntakeID, "cancelled", 3.5))

	stmts := loose(t, h.fake, 3)
	requireArgs(t, "UpsertIntake", stmts[0], pgIntakeID, pgTenant, pgContact, "sess-1", "open", 7.5, nil, nil)
	requireArgs(t, "UpsertIntake con evento y vencimiento", stmts[1],
		pgIntakeID, pgTenant, pgContact, "sess-1", "open", 7.5, pgAt, pgEventID)
	requireArgs(t, "MarkIntakeStatus", stmts[2], pgIntakeID, "cancelled", 3.5)
}

// pgTwoLines son las dos líneas que estos tests escriben.
func pgTwoLines() []IntakeItem {
	return []IntakeItem{
		{SKU: "CAFE", Label: "Café", Customization: "sin azúcar", Qty: 2, UnitPrice: 2.5},
		{SKU: "TE", Label: "Té", Qty: 1, UnitPrice: 2},
	}
}

// TestPostgres_ReplaceIntakeItems_DeleteThenOneInsert: en UNA transacción, primero el DELETE que
// respeta el prefijo reservado "_" y después UN INSERT multi-fila de seis argumentos por línea, en
// el orden recibido y con la personalización siempre (vacía incluida). Una foto vacía solo borra.
func TestPostgres_ReplaceIntakeItems_DeleteThenOneInsert(t *testing.T) {
	h := newFakeRepository(t, pgReply{})
	requireNoError(t, "ReplaceIntakeItems", h.repo.ReplaceIntakeItems(t.Context(), pgIntakeID, pgTwoLines()))
	requirePgKinds(t, h.fake, pgBegin, pgExec, pgExec, pgCommit)
	stmts := statements(t, h.fake, 2, true)
	requireArgs(t, "DELETE", stmts[0], pgIntakeID, "_")
	requireArgs(t, "INSERT", stmts[1],
		pgIntakeID, "CAFE", "Café", "sin azúcar", 2, 2.5,
		pgIntakeID, "TE", "Té", "", 1, 2)

	h = newFakeRepository(t, pgReply{})
	requireNoError(t, "ReplaceIntakeItems con la foto vacía", h.repo.ReplaceIntakeItems(t.Context(), pgIntakeID, nil))
	requirePgKinds(t, h.fake, pgBegin, pgExec, pgCommit)
}

// TestPostgres_ReplaceIntakeItems_FailureRollsBack: cada paso que falla revierte, con su texto.
func TestPostgres_ReplaceIntakeItems_FailureRollsBack(t *testing.T) {
	cases := []struct {
		prefix string
		script []pgReply
	}{
		{"store: retirar líneas de solicitud: ", []pgReply{pgFails()}},
		{"store: insertar líneas de solicitud: ", []pgReply{{}, pgFails()}},
	}
	for _, tc := range cases {
		t.Run(tc.prefix, func(t *testing.T) {
			h := newFakeRepository(t, tc.script...)
			requirePgWrapped(t, h.repo.ReplaceIntakeItems(t.Context(), pgIntakeID, pgTwoLines()), tc.prefix, errPgBoom)
			requirePgRolledBack(t, h.fake)
		})
	}
}

// pgClose es la entrada del cierre de estos tests: una línea.
func pgClose() IntakeClose {
	return IntakeClose{TenantID: pgTenant, ContactID: pgContact, SessionID: "sess-2", Total: 9, CustomerNote: "Portería",
		EventID: pgEventID, Items: []IntakeItem{{SKU: "CAFE", Label: "Café", Qty: 1, UnitPrice: 9}}}
}

// TestPostgres_CloseIntake_ClosesTheOpenOne: con solicitud abierta, UNA transacción: la bloquea
// por (tenant, contacto), la actualiza con (total, nota, evento) y reemplaza sus líneas. Devuelve
// SU id.
func TestPostgres_CloseIntake_ClosesTheOpenOne(t *testing.T) {
	h := newFakeRepository(t, pgOne(pgIntakeID))
	id, err := h.repo.CloseIntake(t.Context(), pgClose())
	requireNoError(t, "CloseIntake", err)
	requireEqual(t, "id cerrado", id, pgIntakeID)

	requirePgKinds(t, h.fake, pgBegin, pgQuery, pgExec, pgExec, pgExec, pgCommit)
	stmts := statements(t, h.fake, 4, true)
	requireArgs(t, "bloqueo", stmts[0], pgTenant, pgContact)
	requireArgs(t, "UPDATE", stmts[1], pgIntakeID, 9, "Portería", pgEventID)
	requireArgs(t, "DELETE de líneas", stmts[2], pgIntakeID, "_")
	requireArgs(t, "INSERT de líneas", stmts[3], pgIntakeID, "CAFE", "Café", "", 1, 9)
}

// TestPostgres_CloseIntake_CreatesAClosedOne: sin solicitud abierta, UNA transacción: inserta una
// cerrada con un UUID nuevo, la sesión, el total, la nota y el evento del cierre, y le escribe las
// líneas. Devuelve ese id nuevo.
func TestPostgres_CloseIntake_CreatesAClosedOne(t *testing.T) {
	h := newFakeRepository(t, pgReply{}) // el bloqueo no encuentra fila.
	id, err := h.repo.CloseIntake(t.Context(), pgClose())
	requireNoError(t, "CloseIntake", err)
	_, perr := uuid.Parse(id)
	requireNoError(t, "el id devuelto no es un UUID", perr)
	requireEqual(t, "el id es nuevo", id != pgIntakeID, true)

	requirePgKinds(t, h.fake, pgBegin, pgQuery, pgExec, pgExec, pgExec, pgCommit)
	stmts := statements(t, h.fake, 4, true)
	requireArgs(t, "INSERT de la cabecera", stmts[1], id, pgTenant, pgContact, "sess-2", 9, "Portería", pgEventID)
	requireArgs(t, "DELETE de líneas", stmts[2], id, "_")
	requireArgs(t, "INSERT de líneas", stmts[3], id, "CAFE", "Café", "", 1, 9)
}

// TestPostgres_CloseIntake_FailureAtEachStep_RollsBack: un fallo en cualquier paso revierte la
// transacción y devuelve el id vacío, con el prefijo de ESE paso.
func TestPostgres_CloseIntake_FailureAtEachStep_RollsBack(t *testing.T) {
	found := pgOne(pgIntakeID)
	cases := []struct {
		prefix string
		script []pgReply
	}{
		{"store: bloquear solicitud abierta: ", []pgReply{pgFails()}},
		{"store: cerrar solicitud: ", []pgReply{found, pgFails()}},
		{"store: insertar solicitud cerrada: ", []pgReply{{}, pgFails()}},
		{"store: retirar líneas de solicitud: ", []pgReply{found, {}, pgFails()}},
		{"store: insertar líneas de solicitud: ", []pgReply{found, {}, {}, pgFails()}},
	}
	for _, tc := range cases {
		t.Run(tc.prefix, func(t *testing.T) {
			h := newFakeRepository(t, tc.script...)
			id, err := h.repo.CloseIntake(t.Context(), pgClose())
			requirePgWrapped(t, err, tc.prefix, errPgBoom)
			requireEqual(t, "id junto al error", id, "")
			requirePgRolledBack(t, h.fake)
		})
	}
}
