//go:build integracion

package procesos

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Las entradas adversarias de P1 (reglas.md §2, hallazgo 40 de F1): los dos identificadores que el
// proceso mete por la puerta —el slug de la empresa y el código de activación— con separadores
// repetidos, dígitos no ASCII y espacios Unicode; y la carrera de varios Edges por un mismo código.
// Se afirma lo que hace el binario VIEJO, que es el oráculo: si el nuevo difiere, es un hallazgo.

// checkTenantSlugs es la tabla adversaria del paso 1 (POST /admin/tenants). El servidor viejo solo
// comprueba que el slug no esté vacío: no recorta, no normaliza y no valida el alfabeto. Por eso
// «a@@b» y los dígitos árabe-índicos se aceptan y se guardan tal cual, y «acme», «acme » (con un
// espacio al final) y «acme» + U+00A0 son TRES empresas distintas; lo único que rechaza es el
// vacío (400) y el repetido byte a byte (409, por la unicidad de tenants.slug).
func (p *p1Run) checkTenantSlugs(t *testing.T) {
	esc := p.esc
	casos := []struct {
		nombre, slug string
		quiere       int
	}{
		{"separadores repetidos", "a@@b", http.StatusCreated},
		{"dígitos árabe-índicos", "١٢٣", http.StatusCreated},
		{"llano", "acme", http.StatusCreated},
		{"con un espacio al final", "acme ", http.StatusCreated},
		{"con un espacio de no separación al final", "acme ", http.StatusCreated},
		{"con un espacio largo delante", " acme", http.StatusCreated},
		{"solo un espacio", " ", http.StatusCreated},
		{"repetido", "acme", http.StatusConflict},
		{"repetido con su espacio", "acme ", http.StatusConflict},
		{"vacío", "", http.StatusBadRequest},
	}
	ids := map[string]string{}
	for _, c := range casos {
		r := esc.S.Admin(esc.TokenStaff).Post(t, rutaTenants, map[string]string{
			"slug": c.slug, "display_name": "P1 " + c.nombre, "plan_id": planTenantPorDefecto,
		})
		if r.Codigo != c.quiere {
			t.Errorf("slug %q (%s): HTTP %d, quería %d\ncuerpo: %s", c.slug, c.nombre, r.Codigo, c.quiere, recortar(r.Cuerpo))
			continue
		}
		if c.quiere != http.StatusCreated {
			p.tenantsRejected++
			continue
		}
		p.tenantsCreated++
		var creado struct{ ID, Slug string }
		r.JSON(t, &creado)
		var guardado string
		if err := esc.DB.QueryRowContext(t.Context(), `SELECT slug FROM public.tenants WHERE id::text = $1`, creado.ID).Scan(&guardado); err != nil {
			t.Errorf("slug %q (%s): leer la empresa creada %q: %v", c.slug, c.nombre, creado.ID, err)
			continue
		}
		if creado.Slug != c.slug || guardado != c.slug {
			t.Errorf("slug %q (%s): la respuesta dice %q y Postgres guarda %q; quería el slug tal cual, sin recortar", c.slug, c.nombre, creado.Slug, guardado)
		}
		if otro, repetido := ids[creado.ID]; repetido {
			t.Errorf("los slugs %q y %q devolvieron la misma empresa %s", otro, c.slug, creado.ID)
		}
		ids[creado.ID] = c.slug
	}
	// Tres «acme» distintos a la vista, uno solo exacto.
	if n := consultaEntero(t, esc.DB, `SELECT count(*) FROM public.tenants WHERE slug = 'acme'`); n != 1 {
		t.Errorf("empresas con el slug exacto «acme» = %d, quería 1", n)
	}
	if n := consultaEntero(t, esc.DB, `SELECT count(*) FROM public.tenants WHERE btrim(slug, E'   ') = 'acme'`); n != 4 {
		t.Errorf("empresas cuyo slug es «acme» salvo espacios = %d, quería 4 (el viejo no recorta)", n)
	}
	// Ninguna nace cortada, y cada alta queda auditada con la empresa creada como objetivo.
	if n := consultaEntero(t, esc.DB, `SELECT count(*) FROM public.tenants WHERE revoked_at IS NOT NULL`); n != 0 {
		t.Errorf("hay %d empresas con revoked_at recién creadas, quería 0", n)
	}
	edgeEsperarValor(t, esc.DB, fmt.Sprintf("%d/%d", p.tenantsCreated, p.tenantsRejected), "la auditoría de las altas de empresa (éxitos/fallos)", `
		SELECT count(*) FILTER (WHERE result = 'success' AND meta->>'status' = '201' AND meta->>'target_tenant_id' IN (SELECT id::text FROM public.tenants))::text
		    || '/' || count(*) FILTER (WHERE result = 'failure' AND meta->>'status' IN ('400', '409') AND NOT jsonb_exists(meta, 'target_tenant_id'))::text
		FROM public.audit_events WHERE tenant_id = $1::uuid AND action = 'tenants.create.any' AND resource = 'tenant'`, tenantPlataformaID)
}

// p1AdversarialCodes devuelve variantes del código de activación bueno que NO son ese código:
// con espacios Unicode pegados, con otra caja, con dígitos no ASCII y con separadores repetidos.
func p1AdversarialCodes(codigo string) map[string]string {
	arabes := strings.NewReplacer("0", "٠", "1", "١", "2", "٢", "3", "٣", "4", "٤", "5", "٥", "6", "٦", "7", "٧", "8", "٨", "9", "٩")
	conArabes := arabes.Replace(codigo)
	if conArabes == codigo { // un código sin ningún dígito (≈ 3 de cada mil millones)
		conArabes = codigo + "٣"
	}
	return map[string]string{
		"con U+00A0 al final":          codigo + " ",
		"con U+2003 delante":           " " + codigo,
		"con un espacio al final":      codigo + " ",
		"con un salto de línea":        codigo + "\n",
		"en mayúsculas":                strings.ToUpper(codigo),
		"con el prefijo en minúsculas": "wapp-" + strings.TrimPrefix(codigo, "WAPP-"),
		"con dígitos árabe-índicos":    conArabes,
		"con el separador repetido":    strings.Replace(codigo, "-", "--", 1),
		"solo separadores":             "WAPP-@@",
		"solo el prefijo":              "WAPP-",
	}
}

// checkAdversarialCodes es la tabla adversaria de EnrollEdge: el servidor viejo compara el código
// byte a byte contra enrollment_codes (sin recortar ni plegar la caja), así que ninguna variante
// del código bueno lo canjea: todas dan PermissionDenied, ninguna deja un certificado, y el código
// bueno sigue SIN quemar (el paso siguiente lo canjea).
func (p *p1Run) checkAdversarialCodes(t *testing.T) {
	esc := p.esc
	for nombre, variante := range p1AdversarialCodes(p.code) {
		e, err := enrolarErr(t, esc.S, variante)
		if e != nil || status.Code(err) != codes.PermissionDenied {
			t.Errorf("enrolar con el código %s (%q): edge %v, error %v; quería PermissionDenied", nombre, variante, e, err)
		}
	}
	if n := consultaEntero(t, esc.DB, `SELECT count(*) FROM public.enrollment_codes WHERE code = $1 AND used_at IS NULL`, p.code); n != 1 {
		t.Errorf("tras los intentos adversarios el código bueno ya no está sin usar")
	}
	if n := consultaEntero(t, esc.DB, `SELECT count(*) FROM public.edge_certs`); n != 0 {
		t.Errorf("los intentos adversarios dejaron %d certificados emitidos, quería 0", n)
	}
}

// checkSingleUse comprueba que un código vale para UN Edge: el ya canjeado y uno inexistente se
// rechazan (helper del arnés), y —la regla de TestIntegration_ConsumeIsAtomic, por la puerta— si
// varios Edges presentan a la vez un mismo código nuevo, gana exactamente uno, los demás reciben
// PermissionDenied y solo queda un certificado. La respuesta del ganador se guarda cruda para el
// candado ADR-0007.
func (p *p1Run) checkSingleUse(t *testing.T) {
	esc := p.esc
	edgeVerificarCodigoRepetido(t, esc, p.code)
	if n := consultaEntero(t, esc.DB, `SELECT count(*) FROM public.edge_certs WHERE tenant_id = $1::uuid`, esc.Tenant); n != 1 {
		t.Fatalf("certificados de la empresa tras repetir el código = %d, quería 1", n)
	}

	const rivales = 6
	codigo := p.issueCode(t)
	csrs := make([][]byte, rivales)
	for i := range csrs {
		clave, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatalf("generar la clave del rival %d: %v", i, err)
		}
		if csrs[i], err = edgeCSR(clave, fmt.Sprintf("edge-race-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	var (
		wg         sync.WaitGroup
		respuestas = make([]*cloudlinkv1.EnrollEdgeResponse, rivales)
		errores    = make([]error, rivales)
		salida     = make(chan struct{})
	)
	for i := range rivales {
		wg.Go(func() {
			<-salida
			respuestas[i], errores[i] = edgeLlamarEnrolamiento(t.Context(), esc.S, codigo, csrs[i])
		})
	}
	close(salida)
	wg.Wait()

	ganadores := 0
	for i, err := range errores {
		switch {
		case err == nil:
			ganadores++
			p.rawEnrollment = respuestas[i]
		case status.Code(err) != codes.PermissionDenied:
			t.Errorf("el rival %d perdió con %v, quería PermissionDenied", i, err)
		}
	}
	if ganadores != 1 {
		t.Fatalf("de %d Edges con el mismo código ganaron %d, quería exactamente 1", rivales, ganadores)
	}
	if n := consultaEntero(t, esc.DB, `SELECT count(*) FROM public.edge_certs WHERE tenant_id = $1::uuid AND subject_cn LIKE 'edge-race-%'`, esc.Tenant); n != 1 {
		t.Errorf("la carrera dejó %d certificados, quería 1", n)
	}
}
