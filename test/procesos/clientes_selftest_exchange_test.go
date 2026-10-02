//go:build integracion

package procesos

import (
	"database/sql"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// Los tests propios del canje y del alta de empresa contra el servidor real: TestArnes_Canje y
// TestArnes_StaffYTenant.
// Sale de clientes_test.go (D-F9-11: solo se movieron declaraciones).

// TestArnes_Canje prueba el canje de identidad contra un servidor real: un usuario sin membresías
// canjea un Identity Token válido y recibe un Context Token SIN empresa (el estado «en espera»), y
// los tokens malos —caducado, de otro emisor, de un system que wApp no acepta, basura, cuerpo
// vacío— se rechazan con el código real del servidor (401 / 400). Necesita Docker.
func TestArnes_Canje(t *testing.T) {
	t.Parallel()
	s := arrancar(t, opcionesServidor{Proceso: "clientes_canje"})
	usuario := uuidAleatorio(t)

	t.Run("un usuario sin membresías canjea y queda sin empresa", func(t *testing.T) { probarCanjeValido(t, s, usuario) })
	t.Run("el context token se verifica", func(t *testing.T) {
		token := canjear(t, s, s.Identidad.TokenDe(usuario, "wapp.bff"))
		r := s.Publica("").Post(t, "/api/v1/auth/verify", map[string]string{"token": token})
		var v struct {
			Valid   bool   `json:"valid"`
			Subject string `json:"subject"`
		}
		r.JSON(t, &v)
		if r.Codigo != http.StatusOK || !v.Valid || v.Subject != usuario {
			t.Errorf("verify del context token: HTTP %d %+v, quería 200 válido con subject %s", r.Codigo, v, usuario)
		}
	})
	t.Run("rechaza los tokens malos", func(t *testing.T) { probarCanjeRechazos(t, s, usuario) })
	t.Run("solo POST", func(t *testing.T) {
		if r := s.Publica("").Get(t, rutaCanje, nil); r.Codigo != http.StatusMethodNotAllowed {
			t.Errorf("GET %s: HTTP %d, quería 405", rutaCanje, r.Codigo)
		}
	})
}

// probarCanjeValido canjea un Identity Token de cada system que wApp acepta y comprueba el 200, la
// forma de la respuesta y el contexto: el usuario, sin empresa y sin roles.
func probarCanjeValido(t *testing.T, s *servidor, usuario string) {
	t.Helper()
	for _, system := range []string{"wapp.bff", "wapp.edge", "wapp.platform"} {
		r := canje(t, s, s.Identidad.TokenDe(usuario, system))
		if r.Codigo != http.StatusOK {
			t.Fatalf("canje de un token de %s: HTTP %d, quería 200\ncuerpo: %s", system, r.Codigo, recortar(r.Cuerpo))
		}
		var res resultadoCanje
		r.JSON(t, &res)
		caduca, err := time.Parse(time.RFC3339, res.ExpiresAt)
		if res.ContextToken == "" || res.TokenType != "Bearer" || err != nil || !caduca.After(time.Now()) {
			t.Errorf("canje de %s: respuesta incompleta (token vacío=%v, token_type=%q, expires_at=%q, err=%v)",
				system, res.ContextToken == "", res.TokenType, res.ExpiresAt, err)
		}
		if res.Context.UserID != usuario || res.Context.TenantID != "" || len(res.Context.Roles) != 0 {
			t.Errorf("canje de %s: contexto %+v, quería user_id=%s sin empresa ni roles", system, res.Context, usuario)
		}
	}
}

// probarCanjeRechazos canjea un token caducado, uno de otro emisor, uno de un system que wApp no
// acepta, una basura y un token vacío, y comprueba el código de cada rechazo y que ninguno trae un
// context_token.
func probarCanjeRechazos(t *testing.T, s *servidor, usuario string) {
	t.Helper()
	rechazos := []struct {
		nombre string
		token  string
		codigo int
	}{
		{"caducado", s.Identidad.TokenCaducado(usuario, "wapp.bff"), http.StatusUnauthorized},
		{"de otro emisor", s.Identidad.TokenDeOtroEmisor(usuario, "wapp.bff"), http.StatusUnauthorized},
		{"de un system equivocado", s.Identidad.TokenDe(usuario, "edugo.kmp"), http.StatusUnauthorized},
		{"que es basura", "esto-no-es-un-jwt", http.StatusUnauthorized},
		{"vacío", "", http.StatusBadRequest},
	}
	for _, c := range rechazos {
		r := canje(t, s, c.token)
		if r.Codigo != c.codigo {
			t.Errorf("canje de un token %s: HTTP %d, quería %d\ncuerpo: %s", c.nombre, r.Codigo, c.codigo, recortar(r.Cuerpo))
		}
		if strings.Contains(string(r.Cuerpo), "context_token") {
			t.Errorf("el rechazo de un token %s no debe traer un context_token: %s", c.nombre, recortar(r.Cuerpo))
		}
	}
}

// TestArnes_StaffYTenant prueba el alta de un staff de plataforma y de una empresa por la puerta
// HTTP: sin alta, el canje funciona pero /admin/tenants responde 403; con altaStaffPlataforma, el
// Context Token lleva la empresa de plataforma y el rol platform_admin y crearTenant da de alta
// la empresa (comprobada por SQL, con su plan), un slug repetido es 409 y el staff la lee. Necesita
// Docker.
func TestArnes_StaffYTenant(t *testing.T) {
	t.Parallel()
	s := arrancar(t, opcionesServidor{Proceso: "clientes_staff"})
	db := s.Base.Abrir(t)
	staff, sinAlta := uuidAleatorio(t), uuidAleatorio(t)

	t.Run("sin alta de staff: 401 sin token y 403 con un token sin empresa", func(t *testing.T) { probarSinAltaStaff(t, s, db, sinAlta) })

	altaStaffPlataforma(t, db, staff)
	r := canje(t, s, s.Identidad.TokenDe(staff, "wapp.bff"))
	var res resultadoCanje
	r.JSON(t, &res)
	if r.Codigo != http.StatusOK || res.Context.TenantID != tenantPlataformaID || !slices.Contains(res.Context.Roles, "platform_admin") {
		t.Fatalf("canje del staff: HTTP %d contexto %+v, quería 200 en %s con el rol platform_admin", r.Codigo, res.Context, tenantPlataformaID)
	}

	t.Run("con alta de staff: crearTenant da de alta la empresa", func(t *testing.T) {
		probarCrearTenant(t, s, db, res.ContextToken, canjear(t, s, s.Identidad.TokenDe(sinAlta, "wapp.bff")))
	})
}

// probarSinAltaStaff comprueba que sin token /admin/tenants da 401, que con el Context Token de un
// usuario sin alta de staff (canje correcto, sin empresa) da 403, y que el 403 no crea nada.
func probarSinAltaStaff(t *testing.T, s *servidor, db *sql.DB, sinAlta string) {
	t.Helper()
	cuerpo := map[string]string{"slug": "no-debe-existir", "display_name": "No debe existir"}
	if r := s.Admin("").Post(t, rutaTenants, cuerpo); r.Codigo != http.StatusUnauthorized {
		t.Errorf("POST %s sin token: HTTP %d, quería 401", rutaTenants, r.Codigo)
	}
	token := canjear(t, s, s.Identidad.TokenDe(sinAlta, "wapp.bff"))
	if r := s.Admin(token).Post(t, rutaTenants, cuerpo); r.Codigo != http.StatusForbidden {
		t.Errorf("POST %s sin alta de staff: HTTP %d, quería 403\ncuerpo: %s", rutaTenants, r.Codigo, recortar(r.Cuerpo))
	}
	if hay := consultaEntero(t, db, `SELECT count(*) FROM public.tenants WHERE slug = 'no-debe-existir'`); hay != 0 {
		t.Errorf("el 403 dejó %d empresa(s) creadas", hay)
	}
}

// probarCrearTenant da de alta dos empresas con el token del staff (una con el plan por defecto, otra
// con basic), comprueba por SQL su slug y su plan, que un slug repetido es 409 y que el staff lee la
// empresa (200) mientras quien no es staff (tokenAjeno) recibe 403.
func probarCrearTenant(t *testing.T, s *servidor, db *sql.DB, tokenStaff, tokenAjeno string) {
	t.Helper()
	plan := func(id string) (slug, plan string) {
		if err := db.QueryRowContext(t.Context(), `SELECT slug, plan_id FROM public.tenants WHERE id = $1::uuid`, id).Scan(&slug, &plan); err != nil {
			t.Fatalf("la empresa %s no está en tenants: %v", id, err)
		}
		return slug, plan
	}
	id := crearTenant(t, s, tokenStaff, "cliente-uno")
	if slug, p := plan(id); slug != "cliente-uno" || p != planTenantPorDefecto {
		t.Errorf("empresa %s = slug %q plan %q, quería cliente-uno y %s", id, slug, p, planTenantPorDefecto)
	}
	otra := crearTenantConPlan(t, s, tokenStaff, "cliente-dos", "basic")
	if slug, p := plan(otra); otra == id || slug != "cliente-dos" || p != "basic" {
		t.Errorf("segunda empresa %s = slug %q plan %q, quería otro id, cliente-dos y basic", otra, slug, p)
	}

	if dup := s.Admin(tokenStaff).Post(t, rutaTenants, map[string]string{"slug": "cliente-uno", "display_name": "Otra"}); dup.Codigo != http.StatusConflict {
		t.Errorf("slug repetido: HTTP %d, quería 409", dup.Codigo)
	}
	if lectura := s.Admin(tokenStaff).Get(t, rutaTenants+"/"+id, nil); lectura.Codigo != http.StatusOK || !strings.Contains(string(lectura.Cuerpo), "cliente-uno") {
		t.Errorf("GET %s/%s: HTTP %d %s, quería 200 con la empresa", rutaTenants, id, lectura.Codigo, recortar(lectura.Cuerpo))
	}
	if lectura := s.Admin(tokenAjeno).Get(t, rutaTenants+"/"+id, nil); lectura.Codigo != http.StatusForbidden {
		t.Errorf("GET %s/%s sin staff: HTTP %d, quería 403", rutaTenants, id, lectura.Codigo)
	}
}
