//go:build integracion

package procesos

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// P7 · Catálogo (diseno.md §4 · T9.20): la dueña descarga la plantilla, la llena y la sube —como JSON
// del contrato o como planilla—; el import estricto le dice TODO lo que está mal con su ubicación, o
// escribe el catálogo y archiva el anterior; y el siguiente pedido que entra por WhatsApp se cotiza
// contra el catálogo recién importado, sin reiniciar nada. Más la URL prefirmada para subir media.
//
// El proceso está partido por tema: este fichero trae el mundo, los guardias de las puertas, la
// auditoría y el cierre; el import JSON va en p7_catalogo_import_test.go; la planilla en
// p7_catalogo_tabular_test.go; los topes en p7_catalogo_limits_test.go; las tablas adversarias en
// p7_catalogo_adversarial_test.go y p7_catalogo_adversarial_tabular_test.go; la caché del índice y la
// media en p7_catalogo_cache_test.go; y las ayudas en p7_catalogo_helpers_test.go.

const (
	// p7PlanNoImport es un plan sembrado SIN la feature catalog_import (0039: `basic` trae menu,
	// cart_basic e intakes_export). p7PlanCommerce sí la trae, y es otro plan que el del escenario.
	p7PlanNoImport = "basic"
	p7PlanCommerce = "commerce"

	// Los cuerpos de error de los guardias.
	p7ErrUnauthenticated = "autenticación requerida"
	p7ErrForbidden       = "permiso denegado"
	p7ErrFeature         = "feature_not_enabled"
	p7FeatureImport      = "catalog_import"
)

// p7World es lo que comparten los subtests de P7: el escenario de P4 (servidor, empresa con el plan
// advisor_ai_local, administradora, Edge conectado y READY con el guion del caso Ámbar, el catálogo
// de draftCatalog en la ref `catalogo`, el flujo y su disparo) y quienes llaman a las puertas.
type p7World struct {
	sc *draftScene
	// calls presta su método call (reintenta el 429 sondeando y apunta la fila de audit_events que
	// cada llamada tiene que dejar: hallazgo 32 de F9) y lleva las cuentas; send las comparte.
	calls *p9World

	admin     p9Caller // administradora de la empresa del proceso
	viewer    p9Caller // un viewer de la misma empresa: lee contenido, no lo escribe
	anon      p9Caller // sin token
	noFeature p9Caller // administradora de una empresa de un plan sin catalog_import
	other     p9Caller // administradora de OTRA empresa con catalog_import
}

// TestP7_Catalog es el proceso P7. Los subtests comparten UN servidor y corren EN ORDEN (ninguno es
// paralelo): cada uno parte de lo que dejó el anterior. Se afirma lo que hace el binario viejo; el
// mismo test corre contra el nuevo sin distinguirlos.
func TestP7_Catalog(t *testing.T) {
	t.Parallel()
	w := p7NewWorld(t)

	t.Run("template_and_prompt", w.templateAndPrompt)
	t.Run("strict_import", w.strictImport)
	t.Run("rejected_documents", w.rejectedDocuments)
	t.Run("tabular_import", w.tabularImport)
	t.Run("tabular_rejections", w.tabularRejections)
	t.Run("item_limit", w.itemLimit)
	t.Run("byte_limit", w.byteLimit)
	t.Run("guards", w.guards)
	t.Run("tenant_isolation", w.tenantIsolation)
	t.Run("adversarial_json", w.adversarialJSON)
	t.Run("adversarial_refs", w.adversarialRefs)
	t.Run("adversarial_tabular", w.adversarialTabular)
	t.Run("index_cache", w.indexCache)
	t.Run("media_upload_url", w.mediaUploadURL)
	t.Run("audit", w.auditTrail)
	t.Run("closing", w.closing)
}

// p7NewWorld arranca el servidor del proceso con el escenario de P4 y da de alta al resto de quienes
// llaman. El Edge se conecta con el test del PROCESO (hallazgo 33). El import del catálogo que hace
// draftScenario ya dejó su fila de auditoría: se apunta aquí para que la cuenta final cuadre.
func p7NewWorld(t *testing.T) *p7World {
	t.Helper()
	sc := draftScenario(t, "p7", "p7-catalogo")
	w := &p7World{
		sc:    sc,
		calls: &p9World{root: t, audit: map[string]int{}, diags: map[string][]string{}},
		admin: p9Caller{client: sc.Pub, tenant: sc.Tenant},
		anon:  p9Caller{client: sc.S.Publica(""), tenant: sc.Tenant},
	}
	w.calls.audit[sc.Tenant+"|"+p7ActContent+"|success"] = 1

	viewer := p10NewMember(t, sc.edgeEscenario, sc.Tenant, p2RoleViewer)
	w.viewer = p9Caller{client: sc.S.Publica(viewer.Token), tenant: sc.Tenant}
	w.noFeature = w.newTenant(t, "p7-sin-import", p7PlanNoImport)
	w.other = w.newTenant(t, "p7-otra-empresa", p7PlanCommerce)
	return w
}

// newTenant da de alta otra empresa con ese plan y devuelve a su administradora en la API pública.
func (w *p7World) newTenant(t *testing.T, slug, plan string) p9Caller {
	t.Helper()
	tenant := crearTenantConPlan(t, w.sc.S, w.sc.TokenStaff, slug, plan)
	admin := p10NewMember(t, w.sc.edgeEscenario, tenant, edgeRolTenantAdmin)
	return p9Caller{client: w.sc.S.Publica(admin.Token), tenant: tenant}
}

// p7Door es una puerta de P7 llamada con un cuerpo válido: lo que cambia entre casos es QUIÉN llama.
type p7Door struct {
	name   string
	action string // la acción que audita cuando la petición pasa del middleware («» = lectura)
	call   func(t *testing.T, w *p7World, c p9Caller, action string) respuesta
}

// p7Doors son las seis puertas, cada una con una petición que la administradora haría bien.
func p7Doors() []p7Door {
	doc := p7Doc(p7Cat("1", "Bebidas", p7Item("1", "CAFE", "Café", "2.5")))
	get := func(path string) func(*testing.T, *p7World, p9Caller, string) respuesta {
		return func(t *testing.T, w *p7World, c p9Caller, _ string) respuesta {
			return w.send(t, c, "", http.MethodGet, path, "", nil).respuesta
		}
	}
	return []p7Door{
		{"template", "", get(p7RouteTemplate)},
		{"prompt", "", get(p7RoutePrompt)},
		{"import", p7ActContent, func(t *testing.T, w *p7World, c p9Caller, action string) respuesta {
			return w.send(t, c, action, http.MethodPost, p7RouteImport+p7Query("apply", "p7-guardias"), "application/json", doc).respuesta
		}},
		{"tabular", p7ActContent, func(t *testing.T, w *p7World, c p9Caller, action string) respuesta {
			body, contentType := p7Multipart(t, p7FormField, p7Sheet(t, p7Row("1|Bebidas", "", "1", "CAFE", "Café", "2.5")))
			return w.send(t, c, action, http.MethodPost, p7RouteTabular+p7Query("apply", "p7-guardias"), contentType, body).respuesta
		}},
		{"upload_url", p7ActMedia, func(t *testing.T, w *p7World, c p9Caller, action string) respuesta {
			return w.calls.call(t, c, action, http.MethodPost, p7RouteUpload, map[string]string{"filename": "a.pdf", "mime": "application/pdf"})
		}},
		{"put_content", p7ActContent, func(t *testing.T, w *p7World, c p9Caller, action string) respuesta {
			return w.calls.call(t, c, action, http.MethodPut, p7RouteContent+"/p7-guardias-put", map[string]int{"a": 1})
		}},
	}
}

// guards afirma los tres guardias de las puertas, que no se sustituyen entre sí: sin token, 401; con
// token pero sin el permiso de escritura (un viewer), 403 «permiso denegado» —y la plantilla y el
// prompt, que son lectura, SÍ se le sirven—; con permiso pero sin la feature catalog_import en el
// plan, 403 `feature_not_enabled` en las cuatro rutas del import, plantilla y prompt incluidos.
//
// Lo que el viejo hace y aquí queda fijado: la feature NO gatea ni la URL de media ni el PUT genérico
// de tenant-content. Una empresa sin catalog_import puede escribir a mano la ref `catalogo`.
//
// Auditoría: el 401 y el 403 de permiso los corta el middleware y no dejan fila; el 403 de la feature
// lo corta el gate, ya dentro de la ruta, y SÍ la deja (`failure`).
func (w *p7World) guards(t *testing.T) {
	cases := []struct {
		name    string
		caller  p9Caller
		audited bool
		// want es, por puerta, «código cuerpo»: el cuerpo es el `error` (y la `feature`) o «» si no importa.
		want map[string]string
	}{
		{"anonymous", w.anon, false, map[string]string{
			"template": "401 " + p7ErrUnauthenticated, "prompt": "401 " + p7ErrUnauthenticated, "import": "401 " + p7ErrUnauthenticated,
			"tabular": "401 " + p7ErrUnauthenticated, "upload_url": "401 " + p7ErrUnauthenticated, "put_content": "401 " + p7ErrUnauthenticated,
		}},
		{"viewer", w.viewer, false, map[string]string{
			"template": "200", "prompt": "200", "import": "403 " + p7ErrForbidden,
			"tabular": "403 " + p7ErrForbidden, "upload_url": "403 " + p7ErrForbidden, "put_content": "403 " + p7ErrForbidden,
		}},
		{"plan_without_feature", w.noFeature, true, map[string]string{
			"template": "403 " + p7ErrFeature + "/" + p7FeatureImport, "prompt": "403 " + p7ErrFeature + "/" + p7FeatureImport,
			"import": "403 " + p7ErrFeature + "/" + p7FeatureImport, "tabular": "403 " + p7ErrFeature + "/" + p7FeatureImport,
			"upload_url": "200", "put_content": "200",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := p7Mark(t, w.sc, tc.caller.tenant)
			for _, door := range p7Doors() {
				action := ""
				if tc.audited {
					action = door.action
				}
				r := door.call(t, w, tc.caller, action)
				if got := p7GuardSummary(t, r); got != tc.want[door.name] {
					t.Errorf("%s: %q, quería %q\ncuerpo: %s", door.name, got, tc.want[door.name], recortar(r.Cuerpo))
				}
			}
			after := p7Mark(t, w.sc, tc.caller.tenant)
			if tc.name != "plan_without_feature" {
				if after != before {
					t.Errorf("las llamadas rechazadas escribieron contenido:\nantes:   %s\ndespués: %s", before, after)
				}
				return
			}
			// Sin la feature, lo ÚNICO que quedó escrito es el PUT genérico: ni el import ni la planilla.
			if got := p9Scalar(t, w.sc.DB, `SELECT coalesce(string_agg(ref, ',' ORDER BY ref), '') FROM public.tenant_content WHERE tenant_id = $1`, tc.caller.tenant); got != "p7-guardias-put" {
				t.Errorf("las refs de la empresa sin catalog_import = %q, quería solo la del PUT genérico", got)
			}
			if got := p7Versions(t, w.sc, tc.caller.tenant, "p7-guardias"); got != "" {
				t.Errorf("la empresa sin catalog_import tiene versiones archivadas: %q", got)
			}
		})
	}
}

// p7GuardSummary resume una respuesta para la tabla de guardias: el código y, en un 4xx, el `error`
// del cuerpo (con la `feature` detrás, si viene).
func p7GuardSummary(t *testing.T, r respuesta) string {
	t.Helper()
	if r.Codigo < http.StatusBadRequest {
		return fmt.Sprint(r.Codigo)
	}
	var body struct {
		Error   string `json:"error"`
		Feature string `json:"feature"`
	}
	r.JSON(t, &body)
	out := fmt.Sprintf("%d %s", r.Codigo, body.Error)
	if body.Feature != "" {
		out += "/" + body.Feature
	}
	return out
}

// tenantIsolation afirma INV-8 en el import: la empresa sale del token y el documento no la lleva. Otra
// empresa importando a la MISMA ref no ve el catálogo de esta en su diff (todo le sale como alta), no
// lo toca y no archiva nada bajo ella; y el versionado de cada una va por su cuenta.
func (w *p7World) tenantIsolation(t *testing.T) {
	before := p7Mark(t, w.sc, w.admin.tenant)
	if got := p7Versions(t, w.sc, w.admin.tenant, p7StrictRef); got == "" {
		t.Fatalf("la empresa del proceso no tiene versiones en %s: el caso no probaría el aislamiento", p7StrictRef)
	}

	first := p7Decode(t, w.importJSON(t, w.other, "apply", p7StrictRef, p7DocNext()))
	if !first.Applied || first.ArchivedVersion != 0 || first.diff() != "|AGUA,CAFE,TE|||0" {
		t.Errorf("el primer import de la otra empresa = aplicado %v, versión %d, diff %q; quería todo como alta y nada archivado",
			first.Applied, first.ArchivedVersion, first.diff())
	}
	second := p7Decode(t, w.importFile(t, w.other, "apply", p7StrictRef, p7Sheet(t, p7Row("1|Bebidas", "", "1", "CAFE", "Café", "2.9"))))
	if !second.Applied || second.ArchivedVersion != 1 || second.diff() != "||AGUA,TE||1" {
		t.Errorf("el segundo import de la otra empresa = aplicado %v, versión %d, diff %q", second.Applied, second.ArchivedVersion, second.diff())
	}
	if got := p7Versions(t, w.sc, w.other.tenant, p7StrictRef); got != "1:import_tabular" {
		t.Errorf("las versiones de la otra empresa = %q, quería la 1, de la planilla", got)
	}
	if after := p7Mark(t, w.sc, w.admin.tenant); after != before {
		t.Errorf("el import de otra empresa tocó el contenido de esta:\nantes:   %s\ndespués: %s", before, after)
	}
}

// auditTrail compara lo que quedó en audit_events con lo que el proceso apuntó llamada a llamada, por
// empresa, acción y resultado. La fila se escribe DESPUÉS de responder, así que se espera sondeando.
// Las dos puertas del import auditan el mismo recurso (`catalog_import`): por cuál entró queda en la
// procedencia de la versión, no en la auditoría. La auditoría no lleva contenido: solo el código HTTP.
func (w *p7World) auditTrail(t *testing.T) {
	const query = `SELECT count(*)::text FROM public.audit_events WHERE tenant_id = $1::uuid AND action = $2 AND result = $3`
	keys := make([]string, 0, len(w.calls.audit))
	total := 0
	for k, n := range w.calls.audit {
		keys = append(keys, k)
		total += n
	}
	slices.Sort(keys)
	for _, k := range keys {
		parts := strings.Split(k, "|")
		edgeEsperarValor(t, w.sc.DB, fmt.Sprint(w.calls.audit[k]), "audit_events de "+k, query, parts[0], parts[1], parts[2])
	}
	actions := "{" + p7ActContent + "," + p7ActMedia + "}"
	if got := p9Scalar(t, w.sc.DB, `SELECT count(*)::text FROM public.audit_events WHERE action = ANY($1::text[])`, actions); got != fmt.Sprint(total) {
		t.Errorf("audit_events de las dos acciones de P7 = %s filas, quería %d (%v)", got, total, w.calls.audit)
	}
	if got := p9Scalar(t, w.sc.DB, `SELECT count(*)::text FROM public.audit_events
		WHERE action = ANY($1::text[]) AND (actor = '' OR NOT (meta ? 'status') OR meta - 'status' <> '{}'::jsonb
			OR resource NOT IN ('catalog_import', 'tenant_content', 'media')
			OR (action = $2 AND resource <> 'media') OR (action = $3 AND resource = 'media'))`,
		actions, p7ActMedia, p7ActContent); got != "0" {
		t.Errorf("hay %s filas de audit_events de P7 sin actor, con algo más que meta.status o con otro resource", got)
	}
}

// closing es el cierre del proceso, ANTES de parar el servidor: el guion atendió todo lo que se le
// pidió, el Edge no anotó errores ni quedó texto sin leer, el doble de S3 no recibió más que el
// HeadBucket del arranque (las URLs se firman en local) y el log del servidor no trae ERROR.
func (w *p7World) closing(t *testing.T) {
	sc := w.sc
	if problems := sc.Script.Problems(); len(problems) != 0 {
		t.Errorf("el guion no supo atender: %v", problems)
	}
	if errs := sc.Edge.Errores(); len(errs) != 0 {
		t.Errorf("errores del núcleo del Edge: %v", errs)
	}
	sc.expectNoPendingText(t, "al cerrar el proceso")
	want := []peticionS3{{Metodo: http.MethodHead, Ruta: "/" + bucketServidor, Host: strings.TrimPrefix(sc.S.S3.URL(), "http://")}}
	if got := sc.S.S3.Peticiones(); !slices.Equal(got, want) {
		t.Errorf("el doble de S3 recibió %+v, quería solo el HeadBucket del arranque, por path-style", got)
	}
	edgeSinErrores(t, sc.S, nil)
	t.Logf("peticiones reintentadas por 429 (límite por credencial de la API pública): %d", w.calls.throttled)
}
