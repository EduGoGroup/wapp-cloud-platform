//go:build integracion

package procesos

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// La cara del Edge por el cable, parte de flota: el número propio cifrado y con índice ciego
// (diseno.md de F3 §4, «fleet») y el corte comercial con dos Edge vivos (R-G21). Son pasos de
// TestP1_EdgeFaceOverTheWire (p1_enrolamiento_wire_test.go).

const (
	// p1WireSelfPnRow dice, de UNA sesión, si las cuatro columnas del sobre del número propio están
	// pobladas y con contenido: el cifrado, su DEK envuelta, la KEK que la envolvió y el índice ciego.
	p1WireSelfPnRow = `SELECT num_nonnulls(self_pn_enc, self_pn_dek, self_pn_kek_id, self_pn_bidx)::text || '/' ||
			(length(self_pn_enc) > 0 AND length(self_pn_dek) > 0 AND self_pn_kek_id <> '' AND self_pn_bidx <> '')::text
		FROM public.fleet_sessions WHERE tenant_id = $1::uuid AND edge_id = $2 AND session_id = $3`
	// p1WireSelfPnBidx y p1WireSelfPnEnc leen el índice ciego y el cifrado (en hex) de una sesión.
	p1WireSelfPnBidx = `SELECT self_pn_bidx FROM public.fleet_sessions WHERE tenant_id = $1::uuid AND edge_id = $2 AND session_id = $3`
	p1WireSelfPnEnc  = `SELECT encode(self_pn_enc, 'hex') FROM public.fleet_sessions WHERE tenant_id = $1::uuid AND edge_id = $2 AND session_id = $3`
	// p1WireSelfPnClear cuenta las filas de fleet_sessions (de TODAS las empresas) donde aparece un
	// literal: dentro del cifrado o de la DEK como bytes, o en cualquier columna vista como texto
	// (to_jsonb de la fila entera: el índice ciego, y cualquier columna en claro que apareciera).
	p1WireSelfPnClear = `SELECT count(*) FROM public.fleet_sessions f
		WHERE position($1::bytea in self_pn_enc) > 0 OR position($1::bytea in self_pn_dek) > 0
		   OR to_jsonb(f)::text LIKE '%' || $2 || '%'`
)

// beatSelfPn manda un latido que declara el número propio de la sesión, con la grafía dada, y
// espera la renovación del lease que provoca. El contador tiene que superar al último aplicado.
func (p *p1WireRun) beatSelfPn(t *testing.T, e *edge, counter int64, pn string) {
	t.Helper()
	before := e.Leases()
	hb := edgeLatido(e.SessionID, counter)
	hb.GetHeartbeat().SelfPn = pn
	if err := e.emitir(hb); err != nil {
		t.Fatalf("latido con número propio de %s: %v", e.EdgeID, err)
	}
	e.esperarLeases(t, before+1, edgeTopeFila)
}

// checkSelfPnSealed afirma, por la puerta y en Postgres, cómo guarda la nube el número propio que
// una sesión declara en su latido (PII: es el teléfono del negocio):
//
//   - la fila de fleet_sessions lleva las cuatro columnas del sobre y el número NO está en claro en
//     ninguna columna, ni en la grafía que mandó el Edge ni en la normalizada;
//   - el índice ciego es del número NORMALIZADO y de la empresa: dos sesiones de la misma empresa que
//     declaran el mismo número con grafías distintas («+57…» y «57…») comparten índice, y la sesión de
//     OTRA empresa con ese mismo número tiene otro; el cifrado, en cambio, es distinto en cada fila;
//   - la nube sabe abrirlo: el aviso de sesión pasiva sale hacia ese número, normalizado, y una sola
//     vez por sesión (el literal del aviso lo congela P3; aquí solo importa el destinatario).
//
// El número tampoco aparece en el log del servidor.
func (p *p1WireRun) checkSelfPnSealed(t *testing.T) {
	esc := p.esc
	cases := []struct {
		e          *edge
		tenant, pn string
	}{{p.a, esc.Tenant, p1WireSelfPnPlus}, {p.b, esc.Tenant, p1WireSelfPn}, {p.other, p.otherTenant, p1WireSelfPn}}
	bidx := make([]string, len(cases))
	enc := make([]string, len(cases))
	for i, c := range cases {
		p.beatSelfPn(t, c.e, 5, c.pn)
		if got := c.e.esperarTexto(t, 5*time.Second); got.A != p1WireSelfPn || got.Texto == "" {
			t.Errorf("el aviso a la sesión de %s fue a %q (texto de %d bytes), quería al número propio normalizado %q", c.e.EdgeID, got.A, len(got.Texto), p1WireSelfPn)
		}
		// Los trabajos de una sesión van en serie: cuando llega el lease de ESTE latido, el del
		// anterior terminó entero, con su decisión de avisar o no.
		p.beatSelfPn(t, c.e, 7, c.pn)
		edgeEsperarValor(t, esc.DB, "4/true", "el sobre del número propio de "+c.e.EdgeID, p1WireSelfPnRow, c.tenant, c.e.EdgeID, c.e.SessionID)
		bidx[i] = p9Scalar(t, esc.DB, p1WireSelfPnBidx, c.tenant, c.e.EdgeID, c.e.SessionID)
		enc[i] = p9Scalar(t, esc.DB, p1WireSelfPnEnc, c.tenant, c.e.EdgeID, c.e.SessionID)
	}
	p.expectNoTexts(t, "tras latir otra vez con las sesiones ya avisadas")

	if bidx[0] != bidx[1] {
		t.Errorf("dos grafías del mismo número en la misma empresa dieron índices ciegos distintos: %q y %q", bidx[0], bidx[1])
	}
	if bidx[2] == bidx[0] {
		t.Errorf("el mismo número en OTRA empresa dio el mismo índice ciego (%q): el índice no lleva la empresa", bidx[2])
	}
	if enc[0] == enc[1] || enc[1] == enc[2] || enc[0] == enc[2] {
		t.Errorf("el mismo número se cifró igual en dos filas: el cifrado es determinista")
	}
	for _, literal := range []string{p1WireSelfPn, p1WireSelfPnPlus, strings.TrimPrefix(p1WireSelfPn, "57")} {
		if n := consultaEntero(t, esc.DB, p1WireSelfPnClear, []byte(literal), literal); n != 0 {
			t.Errorf("el número propio (%q) aparece en claro en %d filas de fleet_sessions", literal, n)
		}
		if strings.Contains(esc.S.Log(), literal) {
			t.Errorf("el número propio (%q) aparece en el log del servidor", literal)
		}
	}
}

// checkTenantCutBothEdges es R-G21 por el cable: la plataforma corta la empresa y el aviso llega a
// TODOS sus Edge vivos —los dos reciben la revocación firmada y dejan de operar— y a ninguno más: el
// Edge de la otra empresa sigue operando, también tras su siguiente latido. El corte vive en
// tenants: leases.revoked de los dos Edge no cambia. Al final la plataforma reactiva la empresa (los
// Edge siguen cortados hasta reconectar: lo afirma TestP1_EnrollmentAndLease).
//
// Los dos Edge conectaron con sesión y latieron: están en la flota, que es de donde el binario viejo
// saca a quién avisar (el nuevo además avisa a los que la flota no lista, D-F3-10; aquí no hay).
func (p *p1WireRun) checkTenantCutBothEdges(t *testing.T) {
	esc := p.esc
	staff := esc.S.Admin(esc.TokenStaff)
	for _, e := range []*edge{p.a, p.b, p.other} {
		if !e.puedeOperar() || e.revocado() {
			t.Fatalf("antes del corte, %s: puedeOperar=%v revocado=%v; quería verdadero y falso", e.EdgeID, e.puedeOperar(), e.revocado())
		}
	}
	if n := consultaEntero(t, esc.DB, `SELECT count(*) FROM public.fleet_sessions WHERE tenant_id = $1::uuid AND state = 'online'`, esc.Tenant); n != 2 {
		t.Fatalf("la flota lista %d sesiones online de la empresa, quería las 2 de sus Edge", n)
	}
	before := [2]int{p.a.Leases(), p.b.Leases()}

	if r := staff.Post(t, rutaTenantCut, map[string]string{"tenant_id": esc.Tenant}); !p1WireIs(r, http.StatusNoContent, "") {
		t.Fatalf("cortar la empresa: HTTP %d %q, quería 204 sin cuerpo", r.Codigo, recortar(r.Cuerpo))
	}
	for i, e := range []*edge{p.a, p.b} {
		edgeEsperar(t, edgeTopeFila, "que "+e.EdgeID+" reciba el corte", e.revocado)
		if e.puedeOperar() || e.Leases() != before[i]+1 {
			t.Errorf("%s tras el corte: puedeOperar=%v y %d LeaseUpdate nuevos; quería falso y 1 (la revocación, sin latir)", e.EdgeID, e.puedeOperar(), e.Leases()-before[i])
		}
		p1WaitLease(t, esc, e, 8, false)
	}
	edgeEsperarValor(t, esc.DB, "true", "tenants.revoked_at de la empresa cortada", p1TenantRevoked, esc.Tenant)
	edgeEsperarValor(t, esc.DB, "false", "tenants.revoked_at de la otra empresa", p1TenantRevoked, p.otherTenant)

	// «Y a ninguno más»: el latido del Edge ajeno se atiende DESPUÉS del corte y le renueva un lease
	// vigente. Si el corte lo hubiera alcanzado, su Validator, que es pegajoso, ya no operaría.
	otherBefore := p.other.Leases()
	p.other.latir(t, 8)
	p.other.esperarLeases(t, otherBefore+1, edgeTopeFila)
	p1WaitLease(t, edgeEscenario{DB: esc.DB, Tenant: p.otherTenant}, p.other, 9, false)
	if !p.other.puedeOperar() || p.other.revocado() {
		t.Errorf("el corte de una empresa alcanzó al Edge de otra: puedeOperar=%v revocado=%v", p.other.puedeOperar(), p.other.revocado())
	}

	if r := staff.Post(t, rutaTenantBack, map[string]string{"tenant_id": esc.Tenant}); !p1WireIs(r, http.StatusNoContent, "") {
		t.Fatalf("reactivar la empresa: HTTP %d %q, quería 204 sin cuerpo", r.Codigo, recortar(r.Cuerpo))
	}
	edgeEsperarValor(t, esc.DB, "false", "tenants.revoked_at tras reactivar", p1TenantRevoked, esc.Tenant)
	p.expectNoTexts(t, "tras el corte y la reactivación")
}
