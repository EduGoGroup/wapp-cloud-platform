//go:build integracion

package procesos

import (
	"net/http"
	"testing"
)

// La configuración de la vía LLM del tenant, por el cable (F45-03): GET, PUT y DELETE de
// /api/v1/tenant-llm contra el servidor real. Es un paso de P4 porque es la puerta por la que la
// dueña elige con qué vía se interpreta lo que P4 capta. Se afirma el código y el cuerpo EXACTOS
// que da el binario viejo; el mismo paso corre contra el nuevo sin distinguirlos.
//
// 🔴 Cero gasto: aquí solo viaja la vía `local`. Un PUT con la vía de pago no sale del arnés
// (viaAPIProhibida, clientes_test.go), y este fichero no lo intenta.

const (
	// p4LLMRoute es la ruta única de las tres operaciones.
	p4LLMRoute = prefijoTenantLLM
	// p4LLMPlan es el plan de la empresa que sí puede configurar la vía: de los sembrados (0039) es
	// uno de los dos que traen `api_llm`. La empresa del escenario (draftPlan) NO la trae, y por eso
	// sirve de caso del gate.
	p4LLMPlan = "advisor_ai_pro"

	// Los cuerpos literales que este paso fija.
	p4LLMNoRow      = `{"configured":false,"via":"local","key_set":false}`
	p4LLMNoFeature  = `{"error":"feature_not_enabled","feature":"api_llm"}`
	p4LLMNoAuth     = `{"error":"autenticación requerida"}`
	p4LLMDenied     = `{"error":"permiso denegado"}`
	p4LLMNotJSON    = `{"error":"el cuerpo debe ser un JSON {via, provider, model, api_key, consented}"}`
	p4LLMUnknownVia = `{"error":"invalid_via","via":"remota"}`
	p4LLMEmptyVia   = `{"error":"invalid_via","via":""}`

	// p4LLMRowBody arma, desde la fila, el cuerpo que GET y PUT devuelven con una fila de la vía
	// local: sin proveedor, sin modelo y sin consentimiento (se omiten), con los instantes de la fila
	// en UTC y con segundos.
	p4LLMRowBody = `SELECT '{"configured":true,"via":"' || via || '","key_set":false,"created_at":"'
			|| to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"') || '","updated_at":"'
			|| to_char(updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"') || '"}'
		FROM public.tenant_llm WHERE tenant_id = $1`
	// p4LLMRow lee lo que la fila guarda: la vía y qué columnas quedaron vacías.
	p4LLMRow = `SELECT via || '|' || (provider IS NULL)::text || (model IS NULL)::text
			|| (api_key_enc IS NULL)::text || (api_key_dek IS NULL)::text || (api_key_kek_id IS NULL)::text
			|| (consented_at IS NULL)::text || '|' || (updated_at >= created_at)::text
		FROM public.tenant_llm WHERE tenant_id = $1`
	// p4LLMRowLocal es p4LLMRow de una fila de la vía local: nada más que la vía.
	p4LLMRowLocal = "local|truetruetruetruetruetrue|true"
)

// p4LLMWant afirma el código y el cuerpo exactos de una respuesta.
func p4LLMWant(t *testing.T, what string, r respuesta, code int, body string) {
	t.Helper()
	if r.Codigo != code || string(r.Cuerpo) != body {
		t.Errorf("%s: HTTP %d con cuerpo %q, quería %d con %q", what, r.Codigo, recortar(r.Cuerpo), code, body)
	}
}

// p4LLMRows cuenta las filas de tenant_llm de TODA la base: el paso deja a lo sumo una.
func p4LLMRows(t *testing.T, sc *draftScene) int {
	t.Helper()
	return consultaEntero(t, sc.DB, `SELECT count(*) FROM public.tenant_llm`)
}

// p4TenantLLM es el paso. La empresa del escenario no tiene `api_llm`: sus tres rutas las corta el
// gate. El recorrido va sobre OTRA empresa del mismo servidor, con un plan que sí la trae, y cuatro
// personas: su administradora, una consultora (lee, no escribe), una operadora (ni lo uno ni lo otro)
// y nadie (sin token). Nada de lo que hace toca la empresa del escenario, que sigue sin fila.
func p4TenantLLM(t *testing.T, sc *draftScene) {
	if n := p4LLMRows(t, sc); n != 0 {
		t.Fatalf("tenant_llm ya tiene %d filas antes del paso", n)
	}
	tenant := crearTenantConPlan(t, sc.S, sc.TokenStaff, "p4-via-llm", p4LLMPlan)
	admin := sc.S.Publica(p10NewMember(t, sc.edgeEscenario, tenant, edgeRolTenantAdmin).Token)
	viewer := sc.S.Publica(p10NewMember(t, sc.edgeEscenario, tenant, p2RoleViewer).Token)
	operator := sc.S.Publica(p10NewMember(t, sc.edgeEscenario, tenant, p2RoleOperator).Token)
	local := map[string]string{"via": "local"}

	t.Run("gate", func(t *testing.T) { p4LLMGate(t, sc, local) })
	t.Run("guardias", func(t *testing.T) { p4LLMGuards(t, sc, viewer, operator, local) })
	t.Run("cuerpos_invalidos", func(t *testing.T) { p4LLMInvalidBodies(t, sc, admin) })
	t.Run("recorrido", func(t *testing.T) { p4LLMRoundTrip(t, sc, admin, tenant) })
}

// p4LLMGate: con el permiso y sin la feature `api_llm`, las TRES rutas —la lectura también— dan el
// 403 del gate con su cuerpo propio, y no se escribe nada. Tener `llm_intake` no lo abre.
func p4LLMGate(t *testing.T, sc *draftScene, local map[string]string) {
	p4LLMWant(t, "GET sin api_llm", sc.Pub.Get(t, p4LLMRoute, nil), http.StatusForbidden, p4LLMNoFeature)
	p4LLMWant(t, "PUT sin api_llm", sc.Pub.Put(t, p4LLMRoute, local), http.StatusForbidden, p4LLMNoFeature)
	p4LLMWant(t, "DELETE sin api_llm", sc.Pub.Delete(t, p4LLMRoute, nil), http.StatusForbidden, p4LLMNoFeature)
	if n := p4LLMRows(t, sc); n != 0 {
		t.Errorf("tenant_llm tiene %d filas tras el corte del gate, quería 0", n)
	}
}

// p4LLMGuards: sin token, 401 en las tres; la consultora (`*.read`) lee y no escribe; la operadora
// no tiene ninguno de los dos permisos. Ningún rechazo escribe.
func p4LLMGuards(t *testing.T, sc *draftScene, viewer, operator *clienteHTTP, local map[string]string) {
	anon := sc.S.Publica("")
	p4LLMWant(t, "GET sin token", anon.Get(t, p4LLMRoute, nil), http.StatusUnauthorized, p4LLMNoAuth)
	p4LLMWant(t, "PUT sin token", anon.Put(t, p4LLMRoute, local), http.StatusUnauthorized, p4LLMNoAuth)
	p4LLMWant(t, "DELETE sin token", anon.Delete(t, p4LLMRoute, nil), http.StatusUnauthorized, p4LLMNoAuth)

	p4LLMWant(t, "GET de la consultora", viewer.Get(t, p4LLMRoute, nil), http.StatusOK, p4LLMNoRow)
	p4LLMWant(t, "PUT de la consultora", viewer.Put(t, p4LLMRoute, local), http.StatusForbidden, p4LLMDenied)
	p4LLMWant(t, "DELETE de la consultora", viewer.Delete(t, p4LLMRoute, nil), http.StatusForbidden, p4LLMDenied)

	p4LLMWant(t, "GET de la operadora", operator.Get(t, p4LLMRoute, nil), http.StatusForbidden, p4LLMDenied)
	p4LLMWant(t, "PUT de la operadora", operator.Put(t, p4LLMRoute, local), http.StatusForbidden, p4LLMDenied)
	p4LLMWant(t, "DELETE de la operadora", operator.Delete(t, p4LLMRoute, nil), http.StatusForbidden, p4LLMDenied)
	if n := p4LLMRows(t, sc); n != 0 {
		t.Errorf("tenant_llm tiene %d filas tras los rechazos, quería 0", n)
	}
}

// p4LLMInvalidBodies: lo que la administradora manda mal. La vía no tiene defecto en el cuerpo
// (ausente es `invalid_via` con la vía vacía), una vía desconocida se nombra en la respuesta, y un
// cuerpo que no es el objeto esperado (vacío, una cadena, otro tipo en un campo) es un 400 de forma.
// Ningún 400 escribe.
func p4LLMInvalidBodies(t *testing.T, sc *draftScene, admin *clienteHTTP) {
	for _, tc := range []struct {
		name string
		body any
		want string
	}{
		{"vía desconocida", map[string]string{"via": "remota"}, p4LLMUnknownVia},
		{"vía desconocida, con espacios alrededor", map[string]string{"via": "  remota "}, p4LLMUnknownVia},
		{"objeto vacío", map[string]string{}, p4LLMEmptyVia},
		{"forma vieja, sin vía", map[string]any{"provider": "anthropic", "model": "m", "consented": true}, p4LLMEmptyVia},
		{"sin cuerpo", nil, p4LLMNotJSON},
		{"una cadena JSON", "local", p4LLMNotJSON},
		{"la vía con otro tipo", map[string]int{"via": 7}, p4LLMNotJSON},
	} {
		p4LLMWant(t, "PUT con "+tc.name, admin.Put(t, p4LLMRoute, tc.body), http.StatusBadRequest, tc.want)
	}
	if n := p4LLMRows(t, sc); n != 0 {
		t.Errorf("tenant_llm tiene %d filas tras los cuerpos inválidos, quería 0", n)
	}
}

// p4LLMRoundTrip es el recorrido de la administradora: sin fila, el GET dice vía local sin
// configurar; el PUT de la vía local guarda SOLO la vía —el proveedor, el modelo y la empresa ajena
// que traiga el cuerpo no se guardan— y responde lo mismo que el GET siguiente; repetirlo reemplaza
// la fila sin duplicarla y conserva su alta; el DELETE la borra con 204 sin cuerpo, el GET vuelve al
// estado sin fila, y un segundo DELETE da el mismo 204. La empresa del escenario nunca tiene fila.
func p4LLMRoundTrip(t *testing.T, sc *draftScene, admin *clienteHTTP, tenant string) {
	p4LLMWant(t, "GET sin fila", admin.Get(t, p4LLMRoute, nil), http.StatusOK, p4LLMNoRow)

	put := admin.Put(t, p4LLMRoute, map[string]any{
		"via": " local ", "provider": "anthropic", "model": "modelo-que-no-se-guarda", "tenant_id": sc.Tenant,
	})
	stored := p9Scalar(t, sc.DB, p4LLMRowBody, tenant)
	if stored == "" {
		t.Fatalf("el PUT de la vía local no dejó fila: HTTP %d\ncuerpo: %s", put.Codigo, recortar(put.Cuerpo))
	}
	p4LLMWant(t, "PUT de la vía local", put, http.StatusOK, stored)
	p4LLMWant(t, "GET con fila", admin.Get(t, p4LLMRoute, nil), http.StatusOK, stored)
	if got := p9Scalar(t, sc.DB, p4LLMRow, tenant); got != p4LLMRowLocal {
		t.Errorf("la fila de tenant_llm = %q, quería %q", got, p4LLMRowLocal)
	}
	if n := consultaEntero(t, sc.DB, `SELECT count(*) FROM public.tenant_llm WHERE tenant_id = $1`, sc.Tenant); n != 0 {
		t.Errorf("la empresa del escenario tiene %d filas en tenant_llm: el tenant_id del cuerpo se usó", n)
	}
	created := p9Scalar(t, sc.DB, `SELECT created_at::text FROM public.tenant_llm WHERE tenant_id = $1`, tenant)

	again := admin.Put(t, p4LLMRoute, map[string]string{"via": "local"})
	p4LLMWant(t, "PUT repetido", again, http.StatusOK, p9Scalar(t, sc.DB, p4LLMRowBody, tenant))
	if got := p9Scalar(t, sc.DB, `SELECT created_at::text FROM public.tenant_llm WHERE tenant_id = $1`, tenant); got != created {
		t.Errorf("el PUT repetido movió created_at de %s a %s", created, got)
	}
	if got := p9Scalar(t, sc.DB, p4LLMRow, tenant); got != p4LLMRowLocal || p4LLMRows(t, sc) != 1 {
		t.Errorf("tras el PUT repetido la fila = %q y hay %d en la tabla, quería %q y una", got, p4LLMRows(t, sc), p4LLMRowLocal)
	}

	p4LLMWant(t, "DELETE con fila", admin.Delete(t, p4LLMRoute, nil), http.StatusNoContent, "")
	if n := p4LLMRows(t, sc); n != 0 {
		t.Errorf("tenant_llm tiene %d filas tras el DELETE, quería 0", n)
	}
	p4LLMWant(t, "GET tras el DELETE", admin.Get(t, p4LLMRoute, nil), http.StatusOK, p4LLMNoRow)
	p4LLMWant(t, "DELETE sin fila", admin.Delete(t, p4LLMRoute, nil), http.StatusNoContent, "")
	p4LLMWant(t, "GET tras el segundo DELETE", admin.Get(t, p4LLMRoute, nil), http.StatusOK, p4LLMNoRow)
}
