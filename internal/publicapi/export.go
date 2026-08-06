package publicapi

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// exportColumns es el CONTRATO de columnas del export (design D-041.15), en este
// orden exacto. Es desnormalizado: una fila por LÍNEA de solicitud, con la
// cabecera repetida en cada una — así el archivo se abre y se filtra en una hoja
// de cálculo sin que nadie tenga que cruzar dos pestañas.
//
// `customer_note` y `customization` salen VACÍAS por ahora, y la columna existe a
// propósito: las columnas de un CSV son POSICIONALES. Las escribe el cliente final
// y nacen en la Ola 4 (T4.1b/T4.1c, D-041.17/D-041.19); si se omitieran hoy,
// añadirlas después las metería EN MEDIO y correría todo lo que viene detrás,
// rompiendo cualquier plantilla o macro que alguien ya hubiera armado sobre el
// archivo. Reservado el hueco, la Ola 4 solo tiene que llenar el valor.
var exportColumns = []string{
	"intake_id", "created_at", "status", "session_id", "contact_ref", "intake_total",
	"customer_note", "sku", "label", "customization", "qty", "unit_price", "line_total",
}

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
// Una solicitud SIN líneas produce igualmente una fila, con las cinco columnas de
// línea vacías. Es deliberado: con "una fila por línea" a rajatabla, una solicitud
// abierta que nadie llegó a llenar desaparecería del export sin dejar rastro.
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
			"", // customer_note — Ola 4 (T4.1c)
		}
		if len(d.Items) == 0 {
			rows = append(rows, append(head, nil, nil, nil, nil, nil))
			continue
		}
		for _, it := range d.Items {
			row := make([]any, 0, len(exportColumns))
			row = append(row, head...)
			rows = append(rows, append(row,
				it.SKU,
				it.Label,
				"", // customization — Ola 4 (T4.1b)
				it.Qty,
				it.UnitPrice,
				float64(it.Qty)*it.UnitPrice, // line_total
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
