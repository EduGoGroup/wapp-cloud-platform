package iamhttp_test

// active_tenant_test.go — LA PUERTA HTTP DE LA ELECCIÓN DE EMPRESA
// (Plan 047 · Ola 5 · T5.1).
//
// Aquí se prueba el TRANSPORTE y nada más: el doble no comprueba membresías
// porque el transporte tampoco. Lo que se fija son los códigos, que el
// `tenant_id` del cuerpo LLEGUE al puerto tal cual, y que el rechazo del no
// miembro no sirva de oráculo.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/ports/in"
	iamhttp "github.com/EduGoGroup/wapp-cloud-platform/internal/iam/transport/http"
)

// selectorFalso devuelve siempre el mismo error (o nil) y guarda lo que recibió.
type selectorFalso struct {
	err            error
	tenantRecibido string
	llamadas       int
}

// compile-time: el doble satisface el MISMO puerto que el servicio real, así que
// un cambio de firma en in.ActiveTenantSelector rompe aquí y no en silencio.
var _ in.ActiveTenantSelector = (*selectorFalso)(nil)

func (s *selectorFalso) SelectActiveTenant(_ context.Context, tenantID string) error {
	s.llamadas++
	s.tenantRecibido = tenantID
	return s.err
}

// elegir ejecuta una petición contra el handler con el cuerpo dado.
func elegir(t *testing.T, selector *selectorFalso, cuerpo string) *httptest.ResponseRecorder {
	t.Helper()
	h := iamhttp.NewActiveTenantHandler(selector).Select()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/active-tenant", strings.NewReader(cuerpo))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestElegirEmpresa_LaElegidaLlegaAlPuertoYResponde204.
//
// El 204 y el paso del dato se comprueban JUNTOS a propósito: un handler que
// respondiera 204 sin llamar al puerto —o llamándolo con la cadena vacía— saldría
// verde en cualquier test que solo mirara el código.
func TestElegirEmpresa_LaElegidaLlegaAlPuertoYResponde204(t *testing.T) {
	t.Parallel()
	const elegida = "22222222-2222-2222-2222-222222222222"
	sel := &selectorFalso{}

	rec := elegir(t, sel, `{"tenant_id":"`+elegida+`"}`)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("code = %d, quiero 204 (body %s)", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Errorf("204 con cuerpo (%s): lo que la persona necesita está en su SIGUIENTE Context Token", rec.Body.String())
	}
	if sel.llamadas != 1 {
		t.Fatalf("llamadas al puerto = %d, quiero 1", sel.llamadas)
	}
	if sel.tenantRecibido != elegida {
		t.Errorf("el puerto recibió %q, quiero %q", sel.tenantRecibido, elegida)
	}
}

// TestElegirEmpresa_LosDesenlaces fija el resto del contrato.
func TestElegirEmpresa_LosDesenlaces(t *testing.T) {
	t.Parallel()

	casos := []struct {
		nombre string
		err    error
		cuerpo string
		quiero int
	}{
		{"no es miembro (o no existe)", domain.ErrNotFound, `{"tenant_id":"22222222-2222-2222-2222-222222222222"}`, http.StatusNotFound},
		{"tenant vacío", domain.ErrInvalidInput, `{"tenant_id":""}`, http.StatusBadRequest},
		{"json roto", nil, `{`, http.StatusBadRequest},
		{"fallo de infra", errors.New("la base se cayó"), `{"tenant_id":"x"}`, http.StatusInternalServerError},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			t.Parallel()
			rec := elegir(t, &selectorFalso{err: c.err}, c.cuerpo)
			if rec.Code != c.quiero {
				t.Fatalf("code = %d, quiero %d (body %s)", rec.Code, c.quiero, rec.Body.String())
			}
		})
	}
}

// TestElegirEmpresa_ElRechazoNoEsUnOraculo: el cuerpo del 404 no puede decir si
// la empresa existe. Si distinguiera «no eres de ahí» de «eso no existe»,
// cualquiera con un token válido levantaría el censo de empresas sondeando UUIDs.
//
// Se comprueba sobre el cuerpo REAL que sale por el cable —no sobre la constante
// del paquete—, porque lo que delata es lo que el cliente recibe.
func TestElegirEmpresa_ElRechazoNoEsUnOraculo(t *testing.T) {
	t.Parallel()

	rec := elegir(t, &selectorFalso{err: domain.ErrNotFound}, `{"tenant_id":"22222222-2222-2222-2222-222222222222"}`)
	cuerpo := strings.ToLower(rec.Body.String())

	// GUARDA ANTI-HUECO: un cuerpo vacío no delataría nada y este test pasaría
	// vigilando una pared.
	if len(cuerpo) == 0 {
		t.Fatal("el 404 salió SIN cuerpo: el barrido de abajo no probaría nada")
	}
	for _, delator := range []string{"miembro", "member", "pertenec", "empresa", "tenant", "existe"} {
		if strings.Contains(cuerpo, delator) {
			t.Errorf("el cuerpo del rechazo dice %q y eso distingue «no eres de ahí» de «no existe»: %s",
				delator, rec.Body.String())
		}
	}
}
