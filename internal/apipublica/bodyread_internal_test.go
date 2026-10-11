package apipublica

// bodyread_internal_test.go — el 400 {"error":"no se pudo leer el cuerpo"} que los contratos de
// MountCatalogImport y MountTenantVariables prometen cuando la LECTURA del cuerpo falla por algo
// que no es el techo de bytes (la conexión se corta a medias). Se afirma sobre las dos funciones
// que leen, con un lector que falla: es el mismo molde que decodeIntegration en
// integrations_validate_test.go.

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/iotest"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo/catalogimport"
)

// bodyReadWantUnreadable afirma el desenlace común: 400 y el cuerpo de error simple.
func bodyReadWantUnreadable(t *testing.T, what string, code int, errBody any) {
	t.Helper()
	body, ok := errBody.(map[string]string)
	if !ok || code != http.StatusBadRequest || len(body) != 1 || body["error"] != "no se pudo leer el cuerpo" {
		t.Errorf("%s con lectura fallida: %d %v, quiero 400 {\"error\":\"no se pudo leer el cuerpo\"}", what, code, errBody)
	}
}

// TestDecodeTenantVars_ReadFailureIs400: un cuerpo que no se puede leer es 400, no un conjunto
// vacío de variables —que el PUT guardaría borrando las que hubiera—.
func TestDecodeTenantVars_ReadFailureIs400(t *testing.T) {
	vars, code, errBody := decodeTenantVars(iotest.ErrReader(errors.New("conexión cortada")))
	bodyReadWantUnreadable(t, "decodeTenantVars", code, errBody)
	if vars != nil {
		t.Errorf("con lectura fallida devolvió las variables %v; quiero ninguna", vars)
	}
}

// TestCatalogImportDecodeBody_ReadFailureIs400: el fallo de lectura que NO es el techo de bytes
// responde el error simple (sin max_bytes y sin lista de defectos) y ningún documento.
func TestCatalogImportDecodeBody_ReadFailureIs400(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", iotest.ErrReader(errors.New("conexión cortada")))
	doc, code, errBody := catalogImportDecodeBody(req, catalogimport.Limits{MaxJSONBytes: 1 << 10})
	bodyReadWantUnreadable(t, "catalogImportDecodeBody", code, errBody)
	if len(doc.Catalog.Categories) != 0 || doc.Format != "" {
		t.Errorf("con lectura fallida devolvió el documento %+v; quiero el cero", doc)
	}
}
