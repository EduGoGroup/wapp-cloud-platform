//go:build integracion

package procesos

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// Lo que comparten los tests propios del Edge de prueba que corren CONTRA EL SERVIDOR REAL: el
// escenario (servidor, staff, empresa, código de activación) y las esperas sobre Postgres y el log.
// Sale de edge_falso_test.go (D-F9-11: solo se movieron declaraciones).

// ---------------------------------------------------------------------------------------------
// Tests contra el servidor real
// ---------------------------------------------------------------------------------------------

// edgeRolTenantAdmin es la plantilla global del rol tenant_admin, sembrada por la migración 0015.
const edgeRolTenantAdmin = "10000000-0000-0000-0000-000000000001"

// edgeEscenario es lo que necesitan los tests del Edge contra el servidor real: el servidor, su base
// abierta, una empresa ya creada y el Context Token del staff que la creó y, si se pidió, el de una
// administradora de esa empresa.
type edgeEscenario struct {
	S          *servidor
	DB         *sql.DB
	Tenant     string
	TokenStaff string
	TokenAdmin string // vacío si no se pidió administradora
}

// edgeEscenarioNuevo arranca un servidor para el proceso dado, da de alta a un staff de plataforma
// (altaStaffPlataforma), crea la empresa slug por la puerta HTTP con su token y, si conAdmin, da de
// alta una administradora de esa empresa (edgeAltaAdminDelTenant) y canjea su token. Falla
// (t.Fatalf) si algo de eso no sale.
func edgeEscenarioNuevo(t *testing.T, proceso, slug string, conAdmin bool) edgeEscenario {
	t.Helper()
	s := arrancar(t, opcionesServidor{Proceso: proceso})
	db := s.Base.Abrir(t)
	staff := uuidAleatorio(t)
	altaStaffPlataforma(t, db, staff)
	tokenStaff := canjear(t, s, s.Identidad.TokenDe(staff, "wapp.bff"))
	esc := edgeEscenario{S: s, DB: db, TokenStaff: tokenStaff, Tenant: crearTenant(t, s, tokenStaff, slug)}
	if conAdmin {
		admin := uuidAleatorio(t)
		edgeAltaAdminDelTenant(t, db, admin, esc.Tenant)
		esc.TokenAdmin = canjear(t, s, s.Identidad.TokenDe(admin, "wapp.bff"))
	}
	return esc
}

// edgeAltaAdminDelTenant deja a usuario (un UUID) como administradora de la empresa tenant: miembro
// (tenant_members) y con el rol tenant_admin acotado a esa empresa (iam_user_roles). Después, el
// canje de su Identity Token da un Context Token de esa empresa con todos los permisos de su ámbito.
// Es idempotente. Falla (t.Fatalf) si algún id no es un UUID, la empresa no existe o el SQL falla.
//
// 🔧 POR QUÉ NO HAY PUERTA HTTP: la primera administradora de una empresa no la puede dar de alta
// nadie desde dentro (las invitaciones las crea una administradora que ya exista) y la ruta del
// staff que sí lo haría (aprobar una solicitud de acceso) llama a identity-core, que el arnés no
// levanta. Es la misma situación que altaStaffPlataforma; este fixture es privado de este fichero
// y puede subir a fixtures_test.go cuando lo necesite otro proceso.
func edgeAltaAdminDelTenant(t *testing.T, db *sql.DB, usuario, tenant string) {
	t.Helper()
	for nombre, valor := range map[string]string{"el usuario": usuario, "la empresa": tenant} {
		if err := exigirUUID(nombre, valor); err != nil {
			t.Fatalf("edgeAltaAdminDelTenant: %v", err)
		}
	}
	ctx, cancelar := context.WithTimeout(t.Context(), topeFixture)
	defer cancelar()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.tenant_members (user_id, tenant_id) VALUES ($1::uuid, $2::uuid)
		ON CONFLICT DO NOTHING`, usuario, tenant); err != nil {
		t.Fatalf("edgeAltaAdminDelTenant: insertar en tenant_members: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.iam_user_roles (user_id, role_id, tenant_id) VALUES ($1::uuid, $2::uuid, $3::uuid)
		ON CONFLICT DO NOTHING`, usuario, edgeRolTenantAdmin, tenant); err != nil {
		t.Fatalf("edgeAltaAdminDelTenant: insertar en iam_user_roles: %v", err)
	}
}

// edgeEmitirCodigo pide un código de enrolamiento para la empresa por la puerta HTTP del staff
// (POST /admin/tenants/{id}/enrollment-codes, plazo por defecto de 24 h) y lo devuelve. Falla
// (t.Fatalf) si la respuesta no es un 201 con un código y un vencimiento futuro.
func edgeEmitirCodigo(t *testing.T, s *servidor, tokenStaff, tenant string) string {
	t.Helper()
	r := s.Admin(tokenStaff).Post(t, rutaTenants+"/"+tenant+"/enrollment-codes", nil)
	if r.Codigo != http.StatusCreated {
		t.Fatalf("emitir un código de enrolamiento: HTTP %d, quería 201\ncuerpo: %s", r.Codigo, recortar(r.Cuerpo))
	}
	var res struct {
		Code      string    `json:"code"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	r.JSON(t, &res)
	if res.Code == "" || !res.ExpiresAt.After(time.Now()) {
		t.Fatalf("el código de enrolamiento llegó sin código o ya vencido: %s", recortar(r.Cuerpo))
	}
	return res.Code
}

// edgeEsperarValor sondea una consulta de una fila y una columna de texto hasta que valga quiere,
// con tope de 10 s. Una consulta sin filas cuenta como «». Falla (t.Fatalf) con el último valor
// visto si no llega a valer lo esperado.
func edgeEsperarValor(t *testing.T, db *sql.DB, quiere, descripcion, consulta string, args ...any) {
	t.Helper()
	ctx, cancelar := context.WithTimeout(t.Context(), edgeTopeFila)
	defer cancelar()
	ultimo := ""
	hecho := edgeSondear(ctx, func() bool {
		var valor string
		// La consulta usa el contexto del test y no el del sondeo: al vencer el tope, la última
		// evaluación tiene que poder leer el valor real para decir qué vio.
		err := db.QueryRowContext(t.Context(), consulta, args...).Scan(&valor)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			valor = ""
		case err != nil:
			valor = "error: " + err.Error()
		}
		ultimo = valor
		return valor == quiere
	})
	if !hecho {
		t.Fatalf("%s: pasaron %s y vale %q, quería %q", descripcion, edgeTopeFila, ultimo, quiere)
	}
}

// edgeEstadoSesion es la consulta del estado de una sesión del Edge en la flota.
const edgeEstadoSesion = `SELECT state FROM public.fleet_sessions WHERE tenant_id = $1::uuid AND edge_id = $2 AND session_id = $3`

// edgeLineaLog busca en el log JSON del servidor una línea con el mensaje msg cuyo campo clave de
// texto valga valor. Devuelve la línea y true, o nil y false si aún no está.
func edgeLineaLog(s *servidor, msg, clave, valor string) (map[string]any, bool) {
	for _, l := range s.LineasLog() {
		if l["msg"] != msg {
			continue
		}
		if v, ok := l[clave].(string); ok && v == valor {
			return l, true
		}
	}
	return nil, false
}

// edgeEsperarLinea espera, con tope de 10 s, a que el log del servidor tenga la línea
// edgeLineaLog(msg, clave, valor) y la devuelve. Falla (t.Fatalf) si no aparece.
func edgeEsperarLinea(t *testing.T, s *servidor, msg, clave, valor string) map[string]any {
	t.Helper()
	var linea map[string]any
	edgeEsperar(t, edgeTopeFila, fmt.Sprintf("una línea de log %q con %s=%q", msg, clave, valor), func() bool {
		var ok bool
		linea, ok = edgeLineaLog(s, msg, clave, valor)
		return ok
	})
	return linea
}

// edgeSinErrores falla el test (t.Errorf) por cada línea de nivel ERROR del log del servidor que no
// esté prevista. esperados dice cuántas líneas ERROR de cada mensaje (campo msg) puede haber como
// máximo; nil o vacío significa que no se espera ninguna.
func edgeSinErrores(t *testing.T, s *servidor, esperados map[string]int) {
	t.Helper()
	vistos := map[string]int{}
	for _, l := range s.LineasLog() {
		if l["level"] != "ERROR" {
			continue
		}
		msg, ok := l["msg"].(string)
		if !ok {
			msg = fmt.Sprint(l["msg"])
		}
		vistos[msg]++
		if vistos[msg] > esperados[msg] {
			t.Errorf("línea ERROR inesperada en el log del servidor: %v", l)
		}
	}
}
