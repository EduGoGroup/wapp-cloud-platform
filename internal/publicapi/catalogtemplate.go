package publicapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/catalogimport"
)

// formatJSON es el formato por defecto de la plantilla: el contrato mismo. Los
// otros dos (formatCSV/formatXLSX) los declara el export de solicitudes y se reusan
// tal cual: dos juegos de nombres para "csv" en la misma API sería una trampa para
// quien la consume.
const formatJSON = "json"

// templateFilename es el nombre con el que se descarga la plantilla. Sin fecha, a
// diferencia del export: dos exports distintos no deben pisarse en la carpeta de
// descargas, pero dos plantillas SÍ —la nueva sustituye a la vieja, y tener
// «catalogo-plantilla-20260806.json» y otras cuatro al lado es justo cómo alguien
// acaba llenando la caducada.
const templateFilename = "catalogo-plantilla"

// catalogTemplateHandler sirve
// GET /api/v1/catalog/import/template?format=json|csv|xlsx: la plantilla de
// ejemplo que el dueño del negocio descarga para partir de ella (patrón
// buildImportTemplate de EduGo 038, design §1).
//
// LA SIRVE EL BACKEND, no una copia pegada en la consola, y ese es el punto entero
// del patrón: la plantilla sale de los MISMOS structs del contrato
// (catalogimport.BuildTemplate), así que no puede describir un contrato que este
// servidor ya no acepta. Una plantilla estática en el front se queda vieja el día
// que sube ImportVersion y reparte documentos que el import rechaza en bloque.
//
// TRES FORMATOS, UN SOLO CATÁLOGO. El JSON es el contrato (y es lo que se pega en
// un LLM junto al prompt); el CSV y el XLSX son la MISMA información en la planilla
// canónica (D-041.9), para quien prefiere una hoja de cálculo a un archivo de
// texto. Las filas de las dos salen del mismo documento, no de un literal paralelo.
//
// No lleva nada del tenant: la plantilla es idéntica para todos y no toca la BD.
// Va gateada igual que el resto del import porque es parte de la MISMA capacidad
// vendida: repartir la plantilla a quien no puede importar sería enseñar la puerta
// y no dar la llave.
//
// Respuestas: 200 con el archivo; 400 si el formato es desconocido —no se adivina,
// por lo mismo que el modo del import: «xls» tecleado a las prisas no puede
// degradar en silencio a otra cosa—; 401 sin identidad; 403 sin la feature.
func catalogTemplateHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		format := r.URL.Query().Get("format")
		if format == "" {
			format = formatJSON
		}

		var buf bytes.Buffer
		var err error
		switch format {
		case formatJSON:
			err = writeTemplateJSON(&buf)
		case formatCSV:
			err = writeCSV(&buf, catalogimport.TabularColumns(), catalogimport.TemplateSheetRows())
		case formatXLSX:
			err = writeXLSX(&buf, catalogimport.TabularSheetName,
				catalogimport.TabularColumns(), catalogimport.TemplateSheetRows())
		default:
			writeError(w, http.StatusBadRequest, "format inválido: usa json, csv o xlsx")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "no se pudo generar la plantilla")
			return
		}

		w.Header().Set("Content-Type", templateContentType(format))
		w.Header().Set("Content-Disposition",
			fmt.Sprintf("attachment; filename=%q", templateFilename+"."+format))
		w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
		w.WriteHeader(http.StatusOK)
		if _, werr := w.Write(buf.Bytes()); werr != nil {
			return
		}
	})
}

// writeTemplateJSON serializa la plantilla INDENTADA y con salto final. No es
// cosmética: este archivo lo abre y lo edita a mano el dueño del negocio, y una
// sola línea de 900 caracteres es ilegible en cualquier editor y en la caja de
// texto de un LLM.
func writeTemplateJSON(w io.Writer) error {
	body, err := json.MarshalIndent(catalogimport.BuildTemplate(), "", "  ")
	if err != nil {
		return fmt.Errorf("plantilla: serializar el documento: %w", err)
	}
	if _, werr := w.Write(append(body, '\n')); werr != nil {
		return fmt.Errorf("plantilla: escribir el documento: %w", werr)
	}
	return nil
}

// templateContentType es el MIME de cada formato de la plantilla.
func templateContentType(format string) string {
	if format == formatJSON {
		return "application/json; charset=utf-8"
	}
	return exportContentType(format)
}

// catalogPromptResponse es el prompt-plantilla servido a la consola.
//
// Viaja con el format y la VERSIÓN del contrato al que corresponde, y no por
// simetría: el BFF puede cachear este texto y una pantalla que muestre el prompt de
// la versión 1 cuando el servidor ya valida la 2 mandaría al dueño a generar
// documentos que se rechazan. Con la versión al lado, la pantalla puede notarlo.
type catalogPromptResponse struct {
	Format  string `json:"format"`
	Version int    `json:"version"`
	Prompt  string `json:"prompt"`
}

// catalogPromptHandler sirve GET /api/v1/catalog/import/prompt: el texto que el
// dueño del negocio pega en SU LLM (design §6) junto con la plantilla y su lista de
// productos.
//
// Existe como ENDPOINT y no como texto en la plantilla del BFF porque el prompt
// está versionado junto al contrato (vive en internal/catalogimport/prompt.go, con
// un test que lo ata a ImportVersion). Copiado en un HTML de otro repo sería una
// segunda fuente, y envejecería sin que nadie se enterase: el BFF lo pide y lo
// muestra como texto copiable.
//
// 🟡 El texto es el del design §6 y NO se ha probado todavía contra un LLM externo
// real (deuda visible de T3.2, decidida el 2026-08-06). Sirve, pero no está
// verificado en campo.
func catalogPromptHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, catalogPromptResponse{
			Format:  catalogimport.ImportFormat,
			Version: catalogimport.ImportVersion,
			Prompt:  catalogimport.ImportPrompt(),
		})
	})
}
