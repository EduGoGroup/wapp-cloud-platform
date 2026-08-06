package publicapi

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// Las columnas del export van en DOS mitades declaradas por separado, y no en una
// lista suelta, porque cada fila del archivo se arma con esa misma división: la
// cabecera de la solicitud se REPITE en cada una de sus líneas (desnormalizado,
// D-041.15) y detrás van las columnas de la línea. Teniendo las dos mitades, el
// relleno de una solicitud sin líneas se DERIVA de cuántas columnas de línea hay
// —no se escribe a mano— y añadir una columna no puede dejar esas filas con un
// hueco de menos, que es un desajuste que el CSV no delata al abrirlo.
//
// `customer_note` ocupa el hueco que T4.1b le dejó reservado tras `intake_total`
// (D-041.15): las columnas de una hoja son POSICIONALES para quien la llena, así
// que meterla EN MEDIO más adelante habría corrido todo lo que viene detrás y roto
// cualquier plantilla o macro ya armada sobre el archivo.
var (
	// exportHeadColumns son las columnas de la SOLICITUD (se repiten por línea).
	exportHeadColumns = []string{
		"intake_id", "created_at", "status", "session_id", "contact_ref",
		"intake_total", "customer_note",
	}
	// exportLineColumns son las columnas de la LÍNEA. `customization` va entre
	// `label` y `qty` (D-041.15): pegada a lo que describe y antes de lo que se
	// cobra, porque no se cobra (INV-13).
	exportLineColumns = []string{
		"sku", "label", "customization", "qty", "unit_price", "line_total",
	}
	// exportColumns es el CONTRATO de columnas del export, en este orden exacto.
	// ES LA ÚNICA definición del orden: quien lo cambie rompe el test que afirma
	// la cabecera completa, no el archivo que abre el operador.
	exportColumns = slices.Concat(exportHeadColumns, exportLineColumns)
)

// exportFormats son los formatos aceptados por ?format=. El default es csv.
const (
	formatCSV  = "csv"
	formatXLSX = "xlsx"
)

// exportSheet es el nombre de la única hoja del XLSX.
const exportSheet = "solicitudes"

// exportRows desnormaliza las solicitudes a filas del export. Las celdas viajan
// como `any` —string, int, float64 o nil— porque los dos formatos las quieren
// distintas: el CSV las formatea a texto y el XLSX escribe los números COMO
// números (si fueran texto, la hoja no los sumaría).
//
// Una solicitud SIN líneas produce igualmente una fila, con las columnas de línea
// vacías. Es deliberado: con "una fila por línea" a rajatabla, una solicitud
// abierta que nadie llegó a llenar desaparecería del export sin dejar rastro.
//
// Toda fila mide EXACTAMENTE len(exportColumns): el relleno de la solicitud sin
// líneas se dimensiona con len(exportLineColumns) en vez de contar `nil` a mano.
func exportRows(details []intakes.Detail) [][]any {
	rows := make([][]any, 0, len(details))
	for _, d := range details {
		head := []any{
			d.ID,
			d.CreatedAt.UTC().Format(time.RFC3339),
			d.Status, // ya NORMALIZADO por el dominio: el `closed` legado sale confirmed
			d.SessionID,
			d.ContactID, // contact_ref: el opaco TAL CUAL (ADR-0010), nunca número ni JID
			d.Total,
			// La indicación del PEDIDO (D-041.19), desnormalizada como el total: se
			// repite en cada línea de la solicitud. Sale como texto —lo escribe el
			// cliente final— y por eso pasa por el escape de fórmulas, igual que la
			// personalización de la línea. No toca el dinero (INV-13).
			d.CustomerNote,
		}
		if len(d.Items) == 0 {
			rows = append(rows, append(head, make([]any, len(exportLineColumns))...))
			continue
		}
		for _, it := range d.Items {
			row := make([]any, 0, len(exportColumns))
			row = append(row, head...)
			rows = append(rows, append(row,
				it.SKU,
				it.Label,
				// La personalización de la línea (D-041.17): quien prepara la lee
				// AQUÍ. Sale como texto —lo escribe el cliente final— y por eso
				// pasa por el escape de fórmulas del formateador, igual que label.
				it.Customization,
				it.Qty,
				it.UnitPrice,
				// line_total sale de qty × unit_price y de NADA más: la
				// personalización no toca el dinero (INV-13).
				float64(it.Qty)*it.UnitPrice,
			))
		}
	}
	return rows
}

// csvBOM es el BOM UTF-8. Sin él, Excel en Windows lee el archivo con la
// codificación del sistema y destroza cada tilde y cada ñ (design D-041.15).
const csvBOM = "\xef\xbb\xbf"

// writeCSV serializa las filas como CSV RFC-4180 (CRLF) precedido del BOM.
//
// Las columnas se reciben, no se leen de una global: este escritor sirve al export
// de solicitudes y a la plantilla del import de catálogo, que tienen cabeceras
// distintas. Lo que comparten —BOM, CRLF, el escape de fórmulas, el formato de los
// números— es exactamente lo que no conviene tener escrito dos veces.
func writeCSV(w io.Writer, columns []string, rows [][]any) error {
	if err := checkRowWidth(columns, rows); err != nil {
		return err
	}
	if _, err := io.WriteString(w, csvBOM); err != nil {
		return fmt.Errorf("export: escribir BOM: %w", err)
	}
	cw := csv.NewWriter(w)
	cw.UseCRLF = true

	if err := cw.Write(columns); err != nil {
		return fmt.Errorf("export: escribir cabecera CSV: %w", err)
	}
	record := make([]string, len(columns))
	for _, row := range rows {
		for i, cell := range row {
			record[i] = csvCell(cell)
		}
		if err := cw.Write(record); err != nil {
			return fmt.Errorf("export: escribir fila CSV: %w", err)
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return fmt.Errorf("export: volcar CSV: %w", err)
	}
	return nil
}

// checkRowWidth exige que cada fila traiga UNA celda por columna. No es una
// paranoia de estilo: el escritor de CSV reutiliza el mismo buffer de registro
// entre filas, así que una fila corta no sale corta — sale con las celdas que
// sobran HEREDADAS de la fila anterior, y el archivo se abre perfectamente con un
// dato de otro pedido en la última columna. Un desajuste es un error del
// generador, y vale mucho más un 500 que una hoja de cálculo que miente.
func checkRowWidth(columns []string, rows [][]any) error {
	for i, row := range rows {
		if len(row) != len(columns) {
			return fmt.Errorf("export: la fila %d trae %d celdas para %d columnas",
				i+1, len(row), len(columns))
		}
	}
	return nil
}

// csvCell formatea una celda para el CSV. Los números van con punto decimal y sin
// separador de miles (son datos, no presentación) y sin ceros de relleno.
func csvCell(cell any) string {
	switch v := cell.(type) {
	case nil:
		return ""
	case string:
		return escapeFormula(v)
	case int:
		return strconv.Itoa(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return escapeFormula(fmt.Sprint(v))
	}
}

// escapeFormula neutraliza una celda de TEXTO que una hoja de cálculo
// interpretaría como fórmula (D-041.15): Excel ejecuta lo que empieza por `=`,
// `+`, `-`, `@`, tabulador o retorno de carro, y en un archivo alimentado con
// texto que escribe el cliente final eso es una inyección. El prefijo `'` es la
// convención de escape que Excel entiende al importar un CSV.
//
// Solo se aplica al TEXTO: prefijar un número negativo lo convertiría en una
// cadena y la hoja dejaría de sumarlo.
func escapeFormula(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}

// writeXLSX serializa las filas como libro de Excel con una sola hoja.
//
// Aquí NO se prefija con `'`: el equivalente en XLSX de "esto es texto" es el tipo
// de la celda, y SetCellStr la escribe como cadena (`<t>`), no como fórmula
// (`<f>`) — Excel no la evalúa. El apóstrofo, que en un CSV es un escape que el
// importador consume, en un XLSX sería un carácter MÁS dentro del dato y el
// usuario lo vería en pantalla.
func writeXLSX(w io.Writer, sheet string, columns []string, rows [][]any) (err error) {
	if werr := checkRowWidth(columns, rows); werr != nil {
		return werr
	}
	f := excelize.NewFile()
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("export: cerrar libro: %w", cerr)
		}
	}()

	if rerr := f.SetSheetName(f.GetSheetName(0), sheet); rerr != nil {
		return fmt.Errorf("export: nombrar la hoja: %w", rerr)
	}
	for i, col := range columns {
		if cerr := setXLSXCell(f, sheet, i+1, 1, col); cerr != nil {
			return cerr
		}
	}
	for r, row := range rows {
		for i, cell := range row {
			if cerr := setXLSXCell(f, sheet, i+1, r+2, cell); cerr != nil {
				return cerr
			}
		}
	}
	if werr := f.Write(w); werr != nil {
		return fmt.Errorf("export: serializar el libro: %w", werr)
	}
	return nil
}

// setXLSXCell escribe una celda en (col, fila), 1-based. Una celda nil se deja sin
// escribir: en una hoja de cálculo "vacía" y "cero" no son lo mismo, y un cero
// fantasma en qty o line_total falsearía cualquier suma que alguien arrastre.
func setXLSXCell(f *excelize.File, sheet string, col, row int, cell any) error {
	if cell == nil {
		return nil
	}
	name, err := excelize.CoordinatesToCellName(col, row)
	if err != nil {
		return fmt.Errorf("export: coordenada (%d,%d): %w", col, row, err)
	}
	if s, ok := cell.(string); ok {
		if serr := f.SetCellStr(sheet, name, s); serr != nil {
			return fmt.Errorf("export: escribir celda %s: %w", name, serr)
		}
		return nil
	}
	if serr := f.SetCellValue(sheet, name, cell); serr != nil {
		return fmt.Errorf("export: escribir celda %s: %w", name, serr)
	}
	return nil
}

// exportIntakesHandler sirve GET /api/v1/intakes/export?format=csv|xlsx: las
// solicitudes del tenant del token (INV-8) desnormalizadas a una fila por línea,
// con LOS MISMOS filtros que la lista (from/to/status/session) — si divergieran, el
// archivo no contendría lo que la bandeja enseña.
//
// No pagina (una hoja de cálculo partida en páginas no sirve); la cota es
// MaxExportIntakes y al superarla responde 422 en vez de recortar en silencio.
//
// El archivo se genera ENTERO en memoria antes de tocar la respuesta: una vez
// escrita la primera cabecera HTTP ya no se puede cambiar el código, y un fallo a
// media serialización dejaría al cliente con un 200 y un archivo corrupto.
func exportIntakesHandler(svc IntakeService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}
		format := r.URL.Query().Get("format")
		if format == "" {
			format = formatCSV
		}
		if format != formatCSV && format != formatXLSX {
			writeError(w, http.StatusBadRequest, "format inválido: usa csv o xlsx")
			return
		}
		filter, msg := parseIntakeFilter(r)
		if msg != "" {
			writeError(w, http.StatusBadRequest, msg)
			return
		}

		details, err := svc.ListDetails(r.Context(), id.TenantID, filter)
		switch {
		case errors.Is(err, intakes.ErrTooLarge):
			writeError(w, http.StatusUnprocessableEntity, fmt.Sprintf(
				"el filtro abarca más de %d solicitudes: acótalo con from/to", intakes.MaxExportIntakes))
			return
		case err != nil:
			writeError(w, http.StatusInternalServerError, "no se pudieron leer las solicitudes")
			return
		}

		var buf bytes.Buffer
		rows := exportRows(details)
		if format == formatCSV {
			err = writeCSV(&buf, exportColumns, rows)
		} else {
			err = writeXLSX(&buf, exportSheet, exportColumns, rows)
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "no se pudo generar el archivo")
			return
		}

		w.Header().Set("Content-Type", exportContentType(format))
		w.Header().Set("Content-Disposition",
			fmt.Sprintf("attachment; filename=%q", exportFilename(format, time.Now())))
		w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
		w.WriteHeader(http.StatusOK)
		if _, werr := w.Write(buf.Bytes()); werr != nil {
			return
		}
	})
}

// exportContentType es el MIME de cada formato.
func exportContentType(format string) string {
	if format == formatXLSX {
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	}
	return "text/csv; charset=utf-8"
}

// exportFilename nombra la descarga con el instante de generación: dos exports del
// mismo día no se pisan en la carpeta de descargas.
func exportFilename(format string, at time.Time) string {
	return "intakes-" + at.UTC().Format("20060102-150405") + "." + format
}
