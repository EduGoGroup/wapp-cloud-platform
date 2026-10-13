//go:build integracion

package procesos

import (
	"net/http"
	"testing"
)

// Las tablas de casos adversarios de TestP2_ExchangeAndPermissions (reglas.md §2, hallazgo 40 de
// F1): separadores repetidos, dígitos no ASCII y espacios Unicode por las dos puertas de P2 que
// guardan un texto libre —el nombre de un rol y el patrón de un grant—. Se afirma lo que hace el
// binario VIEJO, medido: no valida el contenido, solo recorta los bordes con strings.TrimSpace
// (que conoce los espacios Unicode) y rechaza lo que queda vacío.

// p2AdversarialRoleNames mete nombres de rol adversarios por POST /api/v1/roles y comprueba el
// código y el nombre que queda en iam_roles.
func p2AdversarialRoleNames(t *testing.T, esc edgeEscenario) {
	adm := esc.S.Publica(esc.TokenAdmin)
	const rolesConNombre = `SELECT count(*) FROM public.iam_roles WHERE tenant_id = $1::uuid AND name = $2`
	casos := []struct {
		caso     string
		nombre   string
		codigo   int
		guardado string // el nombre con el que debe quedar UNA fila; «» = no se comprueba
	}{
		{"separador repetido", "a@@b", http.StatusCreated, "a@@b"},
		{"dígitos árabe-índicos", "١٢٣", http.StatusCreated, "١٢٣"},
		{"espacio Unicode en medio", "caja dos", http.StatusCreated, "caja dos"},
		{"solo espacios Unicode", "  ", http.StatusBadRequest, ""},
		{"vacío", "", http.StatusBadRequest, ""},
		{"bordes U+00A0 y U+2003", " bordes ", http.StatusCreated, "bordes"},
		{"el recortado, repetido", "bordes", http.StatusConflict, "bordes"},
		{"otros bordes, mismo nombre", " bordes ", http.StatusConflict, "bordes"},
	}
	for _, c := range casos {
		r := adm.Post(t, p2RouteRoles, map[string]string{"name": c.nombre})
		p2WantCode(t, "rol con nombre «"+c.caso+"»", r, c.codigo)
		if c.codigo == http.StatusCreated {
			var rol struct {
				Name string `json:"name"`
			}
			r.JSON(t, &rol)
			if rol.Name != c.guardado {
				t.Errorf("rol «%s»: la respuesta dice %q, quería %q", c.caso, rol.Name, c.guardado)
			}
		}
		if c.guardado != "" {
			if n := consultaEntero(t, esc.DB, rolesConNombre, esc.Tenant, c.guardado); n != 1 {
				t.Errorf("rol «%s»: %d filas en iam_roles con el nombre %q, quería 1", c.caso, n, c.guardado)
			}
		}
	}
	// Ni el nombre sin recortar ni un nombre vacío llegaron a la tabla.
	if n := consultaEntero(t, esc.DB, `SELECT count(*) FROM public.iam_roles WHERE tenant_id = $1::uuid AND (name = '' OR name <> btrim(name, E'   '))`, esc.Tenant); n != 0 {
		t.Errorf("iam_roles tiene %d nombre(s) vacíos o con bordes sin recortar", n)
	}
}

// p2AdversarialGrantPatterns mete patrones de grant adversarios por POST
// /api/v1/members/{user_id}/grants y comprueba el código y lo que queda en iam_user_grants. Repetir
// un grant no es un conflicto: contesta 204 y sigue habiendo una fila.
func p2AdversarialGrantPatterns(t *testing.T, esc edgeEscenario) {
	adm := esc.S.Publica(esc.TokenAdmin)
	miembro := uuidAleatorio(t)
	p2AddMember(t, esc.DB, miembro, esc.Tenant, "")
	ruta := p2RouteMembers + "/" + miembro + "/grants"
	const grantsConPatron = `SELECT count(*) FROM public.iam_user_grants WHERE user_id = $1::uuid AND pattern = $2 AND effect = 'deny'`
	casos := []struct {
		caso     string
		patron   string
		codigo   int
		guardado string // el patrón con el que debe quedar UNA fila; «» = no se comprueba
	}{
		{"separador repetido", "a@@b", http.StatusNoContent, "a@@b"},
		{"dígitos árabe-índicos", "١٢٣", http.StatusNoContent, "١٢٣"},
		{"solo espacios Unicode", "  ", http.StatusBadRequest, ""},
		{"vacío", "", http.StatusBadRequest, ""},
		{"bordes U+00A0 y U+2003", " flows.* ", http.StatusNoContent, "flows.*"},
		{"el recortado, repetido", "flows.*", http.StatusNoContent, "flows.*"},
		{"otros bordes, mismo patrón", " flows.* ", http.StatusNoContent, "flows.*"},
	}
	for _, c := range casos {
		// El efecto lleva también bordes Unicode: se recorta igual que el patrón.
		r := adm.Post(t, ruta, map[string]string{"pattern": c.patron, "effect": " deny "})
		p2WantCode(t, "grant con patrón «"+c.caso+"»", r, c.codigo)
		if c.guardado != "" {
			if n := consultaEntero(t, esc.DB, grantsConPatron, miembro, c.guardado); n != 1 {
				t.Errorf("grant «%s»: %d filas en iam_user_grants con el patrón %q, quería 1", c.caso, n, c.guardado)
			}
		}
	}
	if n := consultaEntero(t, esc.DB, `SELECT count(*) FROM public.iam_user_grants WHERE user_id = $1::uuid`, miembro); n != 3 {
		t.Errorf("iam_user_grants del miembro: %d filas, quería 3 (a@@b, ١٢٣ y flows.*)", n)
	}
	// Un patrón que no encaja con ningún permiso no rompe el canje de esa persona.
	p2WantContext(t, "canje con grants adversarios", p2Exchange(t, esc.S, miembro), esc.Tenant)
}
