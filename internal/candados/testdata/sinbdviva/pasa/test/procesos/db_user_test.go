package procesos

import (
	"database/sql"
	"errors"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Usar una conexión que otro abrió no es abrirla: los tipos y los errores de database/sql y
// de pgx (sql.DB, sql.NullTime, sql.ErrNoRows, pgx.Identifier, pgconn.PgError) son legítimos
// en cualquier fichero. Tampoco muerde un método PROPIO que se llama como una apertura, ni
// os.Open, que abre un fichero y no es de ninguna de esas librerías.
type fakeDialer struct{ calls int }

func (d *fakeDialer) Connect() int { d.calls++; return d.calls }

func (d *fakeDialer) Open() int { return d.Connect() }

func (d *fakeDialer) New() *fakeDialer { return &fakeDialer{} }

func countRows(db *sql.DB, table string) (int, error) {
	var n sql.NullInt64
	err := db.QueryRow("SELECT count(*) FROM " + pgx.Identifier{table}.Sanitize()).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return 0, errors.New(pgErr.Code)
	}
	d := &fakeDialer{}
	_ = d.New().Open()
	f, err := os.Open(os.DevNull)
	if err != nil {
		return 0, err
	}
	return int(n.Int64), f.Close()
}
