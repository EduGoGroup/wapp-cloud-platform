// buyer_data_leak_test.go barre las SALIDAS DE LA API pública buscando los datos
// del comprador (Plan 041 · T4.5, D-041.13 / INV-04).
//
// Aquí es donde el requisito se puede probar de verdad: lo que sale por el cable.
// Los tests del dominio miran structs; estos miran los BYTES que recibe quien
// descarga el CSV, el XLSX o el summary.json. Se siembra el checklist en una
// solicitud del fixture y luego se busca el valor —como subcadena— en cada archivo
// y en cada respuesta.
//
// Lo que sí se publica es `buyer_data_present`: un booleano en el detalle, y solo
// ahí. Eso también se afirma, porque "no filtrar" no puede resolverse no guardando.
package publicapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// Valores RAROS a propósito, para poder buscarlos como subcadena sin falsos
// positivos contra otro campo del fixture.
const (
	rutEnLaAPI       = "22.333.444-W-jjx"
	direcciónEnLaAPI = "Avenida Siempreviva 742-jjx"
)

// bandejaConComprador siembra el fixture de siempre y le cuelga a A1 el checklist
// del comprador, por el MISMO camino que el proyector del carrito.
func bandejaConComprador(t *testing.T) *intakes.MemoryStore {
	t.Helper()
	st := seedIntakes()
	ctx := context.Background()
	if err := st.PutBuyerField(ctx, intakeA1, "rut", rutEnLaAPI); err != nil {
		t.Fatalf("PutBuyerField(rut): %v", err)
	}
	if err := st.PutBuyerField(ctx, intakeA1, "direccion", direcciónEnLaAPI); err != nil {
		t.Fatalf("PutBuyerField(direccion): %v", err)
	}
	return st
}

// sinDatosDelComprador falla si el cuerpo contiene alguno de los dos valores.
func sinDatosDelComprador(t *testing.T, dónde string, body []byte) {
	t.Helper()
	for _, valor := range []string{rutEnLaAPI, direcciónEnLaAPI} {
		if bytes.Contains(body, []byte(valor)) {
			t.Fatalf("FUGA en %s: contiene el dato del comprador %q.\nCuerpo:\n%s", dónde, valor, body)
		}
	}
}

// TestBuyerData_NoSalePorNingunaSalidaDeLaAPI recorre las CUATRO salidas que un
// tercero puede pedir y barre las cuatro. Un solo camino olvidado bastaría para
// que el dato saliera de la plataforma.
func TestBuyerData_NoSalePorNingunaSalidaDeLaAPI(t *testing.T) {
	api := newAPI(exportDeps(bandejaConComprador(t)), intakesKeys())

	casos := []struct {
		nombre string
		ruta   string
	}{
		{"el listado", "/api/v1/intakes"},
		{"el detalle", "/api/v1/intakes/" + intakeA1},
		{"el export CSV", "/api/v1/intakes/export"},
		{"el summary.json", "/api/v1/intakes/summary.json"},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			rec := call(api, keyAIntakes, http.MethodGet, c.ruta, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("code=%d, quiero 200; body=%s", rec.Code, rec.Body.String())
			}
			sinDatosDelComprador(t, c.nombre, rec.Body.Bytes())
		})
	}

	// El XLSX es binario: se barre la celda a celda, ya abierto, porque un
	// bytes.Contains sobre el zip comprimido no probaría nada.
	t.Run("el export XLSX", func(t *testing.T) {
		rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes/export?format=xlsx", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("code=%d, quiero 200", rec.Code)
		}
		f, err := excelize.OpenReader(bytes.NewReader(rec.Body.Bytes()))
		if err != nil {
			t.Fatalf("abriendo el XLSX: %v", err)
		}
		defer func() {
			if cerr := f.Close(); cerr != nil {
				t.Logf("cerrando el XLSX: %v", cerr)
			}
		}()
		filas, err := f.GetRows("solicitudes")
		if err != nil {
			t.Fatalf("leyendo la hoja: %v", err)
		}
		if len(filas) < 2 {
			t.Fatalf("el XLSX salió sin filas: el barrido no probaría nada")
		}
		for _, fila := range filas {
			sinDatosDelComprador(t, "el export XLSX", []byte(strings.Join(fila, "\x1f")))
		}
	})
}

// TestBuyerData_ElDetallePublicaElBooleano: lo ÚNICO que la API dice del checklist.
// Va con su contraejemplo en el mismo test —otra solicitud del mismo tenant, sin
// datos— porque un `true` constante pasaría la primera mitad sin decir nada.
func TestBuyerData_ElDetallePublicaElBooleano(t *testing.T) {
	api := newAPI(exportDeps(bandejaConComprador(t)), intakesKeys())

	casos := map[string]bool{
		intakeA1: true,  // se le sembró el checklist
		intakeA2: false, // misma bandeja, sin datos del comprador
	}
	for id, quiero := range casos {
		rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes/"+id, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("detalle de %s: code=%d; body=%s", id, rec.Code, rec.Body.String())
		}
		var got struct {
			BuyerDataPresent bool `json:"buyer_data_present"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("unmarshal del detalle de %s: %v; body=%s", id, err, rec.Body.String())
		}
		if got.BuyerDataPresent != quiero {
			t.Fatalf("buyer_data_present de %s = %v, quiero %v", id, got.BuyerDataPresent, quiero)
		}
	}
}

// TestBuyerData_LaCabeceraDelExportNoCambia: el checklist NO añade columnas al
// archivo (D-041.15). No es un olvido: la cabecera es un contrato con las
// plantillas y macros que el dueño ya tenga armadas, y publicar ahí ni el dato ni
// su existencia es lo que mantiene el export libre de PII de punta a punta.
func TestBuyerData_LaCabeceraDelExportNoCambia(t *testing.T) {
	api := newAPI(exportDeps(bandejaConComprador(t)), intakesKeys())

	rec := call(api, keyAIntakes, http.MethodGet, "/api/v1/intakes/export", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, quiero 200", rec.Code)
	}
	primera, _, _ := strings.Cut(strings.TrimPrefix(rec.Body.String(), "\xef\xbb\xbf"), "\r\n")
	if primera != csvHeader {
		t.Fatalf("la cabecera del CSV cambió:\n got %q\nwant %q", primera, csvHeader)
	}
}

// compilación: el escritor de datos del comprador que consume el proyector del
// carrito lo satisface también el store en memoria de la API. Si las firmas
// divergieran, los tests de arriba estarían sembrando por un camino que no es el
// que corre en producción.
var _ interface {
	PutBuyerField(ctx context.Context, intakeID, key, value string) error
} = (*intakes.MemoryStore)(nil)
