package intakes

import (
	"database/sql/driver"
	"testing"
)

// Este fichero afirma el texto BYTE A BYTE de las sentencias de postgres_items.go (ReplaceItems y
// ApplyRevalidation) contra el del paquete viejo. Va aparte de postgres_items_test.go por tamaño
// (E-13). Las sentencias que son de otro fichero (total, relectura de líneas y de revisiones) se
// dejan pasar aquí: las afirma su test.

// wantLockEditableSQL es el bloqueo de la cabecera (internal/intakes/postgres.go:1358).
const wantLockEditableSQL = `SELECT status FROM public.intakes WHERE tenant_id = $1 AND id = $2 FOR UPDATE`

// wantDeleteClientItemsSQL es el borrado de las líneas de cliente (internal/intakes/postgres.go:1418).
const wantDeleteClientItemsSQL = `
		DELETE FROM public.intake_items
		WHERE intake_id = $1 AND left(sku, 1) <> $2
	`

// wantInsertItemSQL es el INSERT de una línea (internal/intakes/postgres.go:1429).
const wantInsertItemSQL = `
			INSERT INTO public.intake_items (intake_id, sku, label, customization, qty, unit_price)
			VALUES ($1, $2, $3, $4, $5, $6)
		`

// wantLastRevisionSQL es la lectura de la última revisión (internal/intakes/postgres.go:1391).
const wantLastRevisionSQL = `
			SELECT revision_no, kind
			FROM public.intake_revisions
			WHERE intake_id = $1
			ORDER BY revision_no DESC
			LIMIT 1
		`

// wantRepriceItemSQL es el cambio de precio de una línea (internal/intakes/postgres.go:1508).
const wantRepriceItemSQL = `
			UPDATE public.intake_items
			SET label = $3, unit_price = $4
			WHERE intake_id = $1 AND sku = $2 AND left(sku, 1) <> $5
		`

// wantRemoveItemSQL es la retirada de una línea (internal/intakes/postgres.go:1517).
const wantRemoveItemSQL = `
			DELETE FROM public.intake_items
			WHERE intake_id = $1 AND sku = $2 AND left(sku, 1) <> $3
		`

// TestPostgres_ReplaceItems_SQLIsTheOldOneByteForByte: bloqueo, borrado, un INSERT por línea,
// lectura de la última revisión e INSERT de la revisión, con el texto del paquete viejo.
func TestPostgres_ReplaceItems_SQLIsTheOldOneByteForByte(t *testing.T) {
	store, fake := newFakePostgres(t)
	last := pgOne(int64(1), RevisionKindInterpreted)
	scriptReplaceItems(fake, len(pgNewItems), &last)
	if _, err := store.ReplaceItems(t.Context(), pgTenant, pgIntakeID, pgNewItems, pgEditable, EditAsCorrection); err != nil {
		t.Fatalf("ReplaceItems: error inesperado %v", err)
	}
	requirePgSQL(t, fake, wantLockEditableSQL, wantDeleteClientItemsSQL, wantInsertItemSQL, wantInsertItemSQL,
		"", wantGetItemsSQL, wantLastRevisionSQL, wantInsertRevisionSQL, wantSelectRevisionsSQL)
}

// TestPostgres_ApplyRevalidation_SQLIsTheOldOneByteForByte: bloqueo, cambio de precio, retirada e
// INSERT de la revisión, con el texto del paquete viejo.
func TestPostgres_ApplyRevalidation_SQLIsTheOldOneByteForByte(t *testing.T) {
	store, fake := newFakePostgres(t)
	fake.script(pgOne(StatusPendingApproval), pgReply{}, pgReply{}, pgOne(pgIntakeRow(StatusPendingApproval, 24)...),
		pgInserted(3, RevisionKindRevalidated, `{"v":1}`, nil, nil),
		pgReply{rows: [][]driver.Value{pgItemRow("A", 2, 12)}}, pgReply{})
	if _, err := store.ApplyRevalidation(t.Context(), pgTenant, pgIntakeID, pgRevalidation, "", pgEditable); err != nil {
		t.Fatalf("ApplyRevalidation: error inesperado %v", err)
	}
	requirePgSQL(t, fake, wantLockEditableSQL, wantRepriceItemSQL, wantRemoveItemSQL, "",
		wantInsertRevisionSQL, wantGetItemsSQL, wantSelectRevisionsSQL)
}
