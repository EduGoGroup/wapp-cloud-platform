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
	"encoding/json"
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

// listerFalso devuelve siempre la misma lista (o el mismo error) y cuenta las
// llamadas. No consulta membresías porque el transporte tampoco.
type listerFalso struct {
	tenants  []domain.UserTenant
	activeID string
	err      error
	llamadas int
}

// compile-time: el doble satisface el MISMO puerto que el servicio real.
var _ in.TenantLister = (*listerFalso)(nil)

func (l *listerFalso) TenantsOfCaller(context.Context) ([]domain.UserTenant, string, error) {
	l.llamadas++
	return l.tenants, l.activeID, l.err
}

// elegir ejecuta una petición contra el handler con el cuerpo dado.
func elegir(t *testing.T, selector *selectorFalso, cuerpo string) *httptest.ResponseRecorder {
	t.Helper()
	h := iamhttp.NewActiveTenantHandler(selector, &listerFalso{}).Select()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/active-tenant", strings.NewReader(cuerpo))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// listar ejecuta un GET contra el handler de listado.
func listar(t *testing.T, lister *listerFalso) *httptest.ResponseRecorder {
	t.Helper()
	h := iamhttp.NewActiveTenantHandler(&selectorFalso{}, lister).List()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/tenants", nil)
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

// ---------------------------------------------------------------------------
// GET /api/v1/auth/tenants — el listado que hace posible el selector
// ---------------------------------------------------------------------------

// TestListarEmpresas_DevuelveIdNombreYLaActivaMarcada fija la FORMA EXACTA de la
// respuesta, que es el contrato con la consola.
//
// Se comprueba el JSON deserializado y no la cadena, salvo en lo que sí es
// literal: los nombres de las claves. Un `display_name` que se llamara `name`
// pintaría un selector vacío sin que ningún test de tipos lo notara.
func TestListarEmpresas_DevuelveIdNombreYLaActivaMarcada(t *testing.T) {
	t.Parallel()
	const idA, idB = "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"
	l := &listerFalso{
		tenants: []domain.UserTenant{
			{ID: idA, DisplayName: "Panadería Doña Rosa"},
			{ID: idB, DisplayName: "Catering del Sur"},
		},
		activeID: idB,
	}

	rec := listar(t, l)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, quiero 200 (body %s)", rec.Code, rec.Body.String())
	}
	if l.llamadas != 1 {
		t.Fatalf("llamadas al puerto = %d, quiero 1", l.llamadas)
	}

	var out struct {
		Tenants []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
			Active      bool   `json:"active"`
		} `json:"tenants"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("deserializando la respuesta: %v (body %s)", err, rec.Body.String())
	}
	if len(out.Tenants) != 2 {
		t.Fatalf("devolvió %d empresas, quiero 2: %s", len(out.Tenants), rec.Body.String())
	}
	// El ORDEN se conserva: el puerto ya lo trae estable y el transporte no
	// reordena. Un selector que cambie de orden entre recargas está roto.
	if out.Tenants[0].ID != idA || out.Tenants[1].ID != idB {
		t.Errorf("el orden no se conservó: %+v", out.Tenants)
	}
	if out.Tenants[0].DisplayName != "Panadería Doña Rosa" || out.Tenants[1].DisplayName != "Catering del Sur" {
		t.Errorf("los nombres legibles no llegaron: %s", rec.Body.String())
	}
	// La activa, y SOLO la activa. Las dos mitades: sin la negativa, un handler
	// que marcara todas saldría verde.
	if out.Tenants[0].Active {
		t.Errorf("marcó como activa la que NO lo es (%s)", idA)
	}
	if !out.Tenants[1].Active {
		t.Errorf("no marcó la activa (%s): el selector nacería sin nada seleccionado", idB)
	}
}

// TestListarEmpresas_SinEmpresasEsListaVaciaY200 es el requisito de D-056.12 por
// el cable, y la mitad del JSON importa tanto como el código.
//
// 🔴 SE COMPRUEBA LA CADENA LITERAL `"tenants":[]`, no `len()==0`. Un slice nil
// se serializa como `null`, que en JavaScript no es iterable: la consola
// reventaría al pintar el selector justo para el usuario recién registrado, que
// es el caso más frecuente del sistema. `len()==0` no distingue los dos.
func TestListarEmpresas_SinEmpresasEsListaVaciaY200(t *testing.T) {
	t.Parallel()

	// El doble devuelve un nil DE VERDAD, que es lo que un repositorio descuidado
	// devolvería: si el handler no lo normaliza, este test lo caza.
	rec := listar(t, &listerFalso{tenants: nil, activeID: ""})

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, quiero 200: cero empresas es un estado legítimo (D-056.12), no un 404", rec.Code)
	}
	if cuerpo := strings.TrimSpace(rec.Body.String()); !strings.Contains(cuerpo, `"tenants":[]`) {
		t.Fatalf("cuerpo = %s, quiero que contenga `\"tenants\":[]`: un `null` no es iterable en el cliente", cuerpo)
	}
}

// TestListarEmpresas_SinNingunaActivaNoMarcaNada: dos empresas y ninguna elegida
// —el caso que estrena el selector— no puede marcar ninguna opción.
//
// Es la guarda contra el atajo `t.ID == activeID` sin comprobar que activeID no
// esté vacío: ningún ID real es "", pero un doble o un repositorio que devolviera
// "" para todos haría coincidir la comparación. Aquí se prueba con activeID
// vacío y elementos con ID real, que es el caso de producción.
func TestListarEmpresas_SinNingunaActivaNoMarcaNada(t *testing.T) {
	t.Parallel()
	rec := listar(t, &listerFalso{
		tenants: []domain.UserTenant{
			{ID: "11111111-1111-1111-1111-111111111111", DisplayName: "A"},
			{ID: "22222222-2222-2222-2222-222222222222", DisplayName: "B"},
		},
		activeID: "",
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, quiero 200", rec.Code)
	}
	if strings.Contains(rec.Body.String(), `"active":true`) {
		t.Fatalf("marcó una empresa activa sin haberla: %s", rec.Body.String())
	}
}

// TestListarEmpresas_LosDesenlacesDeFallo: el listado NO tiene un 404 y no debe
// tenerlo. Los dos fallos posibles son de cableado (400) y de infraestructura
// (500).
func TestListarEmpresas_LosDesenlacesDeFallo(t *testing.T) {
	t.Parallel()

	casos := []struct {
		nombre string
		err    error
		quiero int
	}{
		{"el contexto no acredita a nadie", domain.ErrInvalidInput, http.StatusBadRequest},
		{"fallo de infra", errors.New("la base se cayó"), http.StatusInternalServerError},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			t.Parallel()
			rec := listar(t, &listerFalso{err: c.err})
			if rec.Code != c.quiero {
				t.Fatalf("code = %d, quiero %d (body %s)", rec.Code, c.quiero, rec.Body.String())
			}
		})
	}
}

// TestListarEmpresas_NoDelataNadaDeFuera: la respuesta no puede llevar totales,
// conteos ni ninguna clave que insinúe cuántas empresas hay más allá de las del
// sujeto. Es el mismo criterio anti-oráculo del 404 del setter, aplicado al
// cuerpo del 200.
//
// Se barre el JSON REAL que sale por el cable, así que un campo añadido al DTO
// dispara este test sin que nadie tenga que acordarse de actualizarlo.
func TestListarEmpresas_NoDelataNadaDeFuera(t *testing.T) {
	t.Parallel()
	rec := listar(t, &listerFalso{
		tenants:  []domain.UserTenant{{ID: "11111111-1111-1111-1111-111111111111", DisplayName: "A"}},
		activeID: "11111111-1111-1111-1111-111111111111",
	})
	cuerpo := strings.ToLower(rec.Body.String())

	// GUARDA ANTI-HUECO: un cuerpo vacío no delataría nada y este test pasaría
	// vigilando una pared.
	if !strings.Contains(cuerpo, "tenants") {
		t.Fatalf("el cuerpo no parece la respuesta del listado: %s", rec.Body.String())
	}
	for _, delator := range []string{"total", "count", "conteo", "otras", "restant", "hay_mas", "has_more"} {
		if strings.Contains(cuerpo, delator) {
			t.Errorf("la respuesta lleva %q: eso insinúa cuántas empresas hay FUERA de las suyas (%s)",
				delator, rec.Body.String())
		}
	}
}
