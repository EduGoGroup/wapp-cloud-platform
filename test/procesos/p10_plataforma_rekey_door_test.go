//go:build integracion

package procesos

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// La puerta de POST /admin/crypto/rekey: quién entra, con qué método, y la tabla adversaria de lo
// único que la ruta lee de la petición, el tamaño de lote (?batch=). El cuerpo no se lee. Y la
// auditoría de todo lo que el proceso pidió por esta ruta.

// p10RekeyCase es una petición a la ruta de rotación y lo que el binario viejo contesta.
type p10RekeyCase struct {
	name   string
	who    string // admin | other | viewer | operator | staff | anon
	method string
	query  string // tal cual va tras el «?»; vacío = sin query
	body   any
	code   int
	text   string // lo que trae el cuerpo de un rechazo; vacío en un 200
}

const (
	p10BadBatch  = "batch inválido (entero >= 0)"
	p10BadMethod = "método no permitido (usar POST)"
)

// p10RekeyCases es la tabla. Los 200 son todos el mismo no-op; los rechazos no tocan nada.
var p10RekeyCases = []p10RekeyCase{
	// Quién entra: solo quien tiene crypto.rekey, que es el «*» de tenant_admin. El staff de
	// plataforma no lo tiene (sus grants son los «.any»), y el viewer y el operator tampoco.
	{"sin token", "anon", http.MethodPost, "", nil, http.StatusUnauthorized, ""},
	{"viewer", "viewer", http.MethodPost, "", nil, http.StatusForbidden, ""},
	{"operator", "operator", http.MethodPost, "", nil, http.StatusForbidden, ""},
	{"staff de plataforma", "staff", http.MethodPost, "", nil, http.StatusForbidden, ""},
	{"administradora de otra empresa", "other", http.MethodPost, "", nil, http.StatusOK, ""},

	// El método: solo POST. El permiso va antes que el método.
	{"GET", "admin", http.MethodGet, "", nil, http.StatusMethodNotAllowed, p10BadMethod},
	{"PUT", "admin", http.MethodPut, "", nil, http.StatusMethodNotAllowed, p10BadMethod},
	{"DELETE", "admin", http.MethodDelete, "batch=1", nil, http.StatusMethodNotAllowed, p10BadMethod},
	{"GET del viewer", "viewer", http.MethodGet, "", nil, http.StatusForbidden, ""},

	// El lote: un entero >= 0 en base 10; 0 y vacío son «el de por defecto».
	{"batch=0", "admin", http.MethodPost, "batch=0", nil, http.StatusOK, ""},
	{"batch=200", "admin", http.MethodPost, "batch=200", nil, http.StatusOK, ""},
	{"batch vacío", "admin", http.MethodPost, "batch=", nil, http.StatusOK, ""},
	{"batch con ceros delante", "admin", http.MethodPost, "batch=007", nil, http.StatusOK, ""},
	{"batch con signo más", "admin", http.MethodPost, "batch=%2B5", nil, http.StatusOK, ""},
	{"batch repetido: manda el primero", "admin", http.MethodPost, "batch=1&batch=zz", nil, http.StatusOK, ""},
	{"batch repetido: el primero no vale", "admin", http.MethodPost, "batch=zz&batch=1", nil, http.StatusBadRequest, p10BadBatch},
	{"otro nombre de parámetro", "admin", http.MethodPost, "BATCH=zz&lote=-1", nil, http.StatusOK, ""},
	{"separadores repetidos", "admin", http.MethodPost, "&&batch=1&&", nil, http.StatusOK, ""},
	{"batch negativo", "admin", http.MethodPost, "batch=-1", nil, http.StatusBadRequest, p10BadBatch},
	{"batch con letras", "admin", http.MethodPost, "batch=abc", nil, http.StatusBadRequest, p10BadBatch},
	{"batch con «@@»", "admin", http.MethodPost, "batch=1%40%402", nil, http.StatusBadRequest, p10BadBatch},
	{"batch con «=» repetido", "admin", http.MethodPost, "batch==1", nil, http.StatusBadRequest, p10BadBatch},
	{"batch con dígitos no ASCII", "admin", http.MethodPost, "batch=%D9%A1%D9%A2%D9%A3", nil, http.StatusBadRequest, p10BadBatch},
	{"batch con espacio Unicode delante", "admin", http.MethodPost, "batch=%C2%A05", nil, http.StatusBadRequest, p10BadBatch},
	{"batch con espacio Unicode detrás", "admin", http.MethodPost, "batch=5%C2%A0", nil, http.StatusBadRequest, p10BadBatch},
	{"batch con espacio", "admin", http.MethodPost, "batch=%205", nil, http.StatusBadRequest, p10BadBatch},
	{"batch decimal", "admin", http.MethodPost, "batch=5.0", nil, http.StatusBadRequest, p10BadBatch},
	{"batch exponencial", "admin", http.MethodPost, "batch=1e3", nil, http.StatusBadRequest, p10BadBatch},
	{"batch hexadecimal", "admin", http.MethodPost, "batch=0x10", nil, http.StatusBadRequest, p10BadBatch},
	{"batch con guion bajo", "admin", http.MethodPost, "batch=1_000", nil, http.StatusBadRequest, p10BadBatch},
	{"batch fuera de rango", "admin", http.MethodPost, "batch=99999999999999999999", nil, http.StatusBadRequest, p10BadBatch},

	// El cuerpo no se lee: ni la empresa, ni un lote, ni una KEK salen de ahí.
	{"cuerpo con otra empresa y otra KEK", "admin", http.MethodPost, "",
		map[string]any{"tenant_id": tenantPlataformaID, "batch": -1, "current_key_id": p10ForeignKeyID}, http.StatusOK, ""},
	{"cuerpo que no es un objeto", "admin", http.MethodPost, "", "a@@b ١٢٣", http.StatusOK, ""},
}

// rekeyAdversarial recorre la tabla. Tras ella, las filas del censo siguen como estaban.
func (w *p10World) rekeyAdversarial(t *testing.T) {
	callers := map[string]p9Caller{
		"admin":    w.admin,
		"other":    w.other,
		"staff":    w.staff,
		"viewer":   {client: w.sc.S.Admin(w.viewer.Token), tenant: w.sc.Tenant},
		"operator": {client: w.sc.S.Admin(w.operator.Token), tenant: w.sc.Tenant},
		"anon":     {client: w.sc.S.Admin("")},
	}
	for _, c := range p10RekeyCases {
		t.Run(c.name, func(t *testing.T) {
			// El 401 y el 403 los corta el middleware, antes de la auditoría; lo demás llega al handler.
			audited := c.code != http.StatusUnauthorized && c.code != http.StatusForbidden
			r := w.rekey(t, callers[c.who], c.method, c.query, c.body, audited)
			switch {
			case c.code == http.StatusOK:
				p10WantNoop(t, c.name, r)
			case r.Codigo != c.code || !strings.Contains(string(r.Cuerpo), c.text):
				t.Errorf("HTTP %d %q, quería %d con %q", r.Codigo, recortar(r.Cuerpo), c.code, c.text)
			}
		})
	}
	w.wantMark(t, "tras la tabla adversaria")
}

// rekeyAudit compara audit_events con lo que el proceso apuntó llamada a llamada: por empresa y
// resultado, con la acción crypto.rekey y el recurso kek. La fila se escribe DESPUÉS de responder,
// así que se espera sondeando. Los 401 y 403 no dejaron ninguna, y la auditoría no lleva contenido.
func (w *p10World) rekeyAudit(t *testing.T) {
	const count = `SELECT count(*)::text FROM public.audit_events
		WHERE tenant_id = $1::uuid AND action = $2 AND resource = $3 AND result = $4`
	keys := make([]string, 0, len(w.calls.audit))
	total := 0
	for k, n := range w.calls.audit {
		keys = append(keys, k)
		total += n
	}
	slices.Sort(keys)
	for _, k := range keys {
		parts := strings.Split(k, "|")
		edgeEsperarValor(t, w.sc.DB, fmt.Sprint(w.calls.audit[k]), "audit_events de "+k, count, parts[0], parts[1], p10ResRekey, parts[2])
	}
	if n := consultaEntero(t, w.sc.DB, `SELECT count(*) FROM public.audit_events WHERE action = $1`, p10ActRekey); n != total {
		t.Errorf("audit_events de %s: %d filas, quería %d (%v)", p10ActRekey, n, total, w.calls.audit)
	}
	if n := consultaEntero(t, w.sc.DB, `SELECT count(*) FROM public.audit_events
		WHERE action = $1 AND (actor = '' OR NOT (meta ? 'status') OR meta::text LIKE '%' || $2 || '%')`, p10ActRekey, p10ForeignKeyID); n != 0 {
		t.Errorf("hay %d filas de audit_events de %s sin actor, sin meta.status o con un key_id dentro", n, p10ActRekey)
	}
}
