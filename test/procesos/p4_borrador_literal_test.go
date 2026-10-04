//go:build integracion

package procesos

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// El barrido del literal de P4: lo que el cliente escribió no puede quedar en claro en ninguna columna
// de ninguna tabla, ni en el log. Es el criterio de REQ-10c que
// internal/flujos/runtime/source_composer_integration_test.go escribió como test
// (TestIntegration_ElLiteralNoQuedaEnClaroEnNingunaTabla), aquí con el pipeline entero detrás.

// p4NoLiteralInClear busca la aguja que el cliente escribió en TODA columna de TODA tabla del esquema
// public (las bytea por bytes, el resto por su texto) y en el log del servidor: no puede estar en
// ningún sitio. El hilo, el sobre del job y el literal de la revisión van cifrados. Tampoco aparecen
// en el log las frases del cliente ni su teléfono.
func p4NoLiteralInClear(t *testing.T, sc *draftScene, run p4Run) {
	t.Helper()
	if hits := p4ScanAllTables(t, sc, p4Needle); len(hits) != 0 {
		t.Errorf("el literal del cliente aparece en claro en: %v", hits)
	}
	if hits := p4ScanAllTables(t, sc, run.ids[0]); strings.Join(hits, ",") != "ingest_dedupe.wa_message_id,intake_jobs.source_refs" {
		t.Errorf("la referencia opaca del primer mensaje aparece en %v; quería solo el dedupe de ingesta y las referencias del job "+
			"(si esto falla, el barrido de la aguja no está mirando nada)", hits)
	}
	log := sc.S.Log()
	for _, secret := range []string{p4Needle, scriptEvidenceChoc, scriptEvidenceTequenos, "decoración infantil", run.contact} {
		if strings.Contains(log, secret) {
			t.Errorf("el log del servidor contiene texto o teléfono del cliente: %q", secret)
		}
	}
}

// p4ScanAllTables devuelve las columnas («tabla.columna», ordenadas) de las tablas base del esquema
// public en las que aparece needle: en las bytea se busca como bytes y en las demás en su forma de
// texto. Falla (t.Fatalf) si el catálogo no se puede leer o una consulta falla.
func p4ScanAllTables(t *testing.T, sc *draftScene, needle string) []string {
	t.Helper()
	rows, err := sc.DB.QueryContext(t.Context(), `SELECT c.table_name, c.column_name, c.data_type
		FROM information_schema.columns c JOIN information_schema.tables tb
		  ON tb.table_schema = c.table_schema AND tb.table_name = c.table_name
		WHERE c.table_schema = 'public' AND tb.table_type = 'BASE TABLE'
		ORDER BY c.table_name, c.column_name`)
	if err != nil {
		t.Fatalf("leer el catálogo de columnas: %v", err)
	}
	type column struct{ table, name, kind string }
	var columns []column
	for rows.Next() {
		var c column
		if err := rows.Scan(&c.table, &c.name, &c.kind); err != nil {
			t.Fatalf("leer una columna del catálogo: %v", err)
		}
		columns = append(columns, c)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("cerrar el catálogo de columnas: %v", err)
	}
	if len(columns) < 100 {
		t.Fatalf("el catálogo solo trae %d columnas: el barrido no vería nada", len(columns))
	}
	var hits []string
	for _, c := range columns {
		table, col := pgx.Identifier{"public", c.table}.Sanitize(), pgx.Identifier{c.name}.Sanitize()
		query := `SELECT count(*) FROM ` + table + ` WHERE ` + col + `::text LIKE '%' || $1 || '%'`
		var arg any = needle
		if c.kind == "bytea" {
			query, arg = `SELECT count(*) FROM `+table+` WHERE position($1::bytea in `+col+`) > 0`, []byte(needle)
		}
		if consultaEntero(t, sc.DB, query, arg) > 0 {
			hits = append(hits, c.table+"."+c.name)
		}
	}
	return hits
}
