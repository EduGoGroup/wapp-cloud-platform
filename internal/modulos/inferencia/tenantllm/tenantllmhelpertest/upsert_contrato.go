package tenantllmhelpertest

// Los casos de Upsert: qué escribe cada vía, el reemplazo completo y los tres rechazos que no
// escriben (R4.4.d, R4.4.e).

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm"
)

// caseUpsertAPIStoresRow: la vía api deja las seis columnas del eje rellenas, y Get devuelve la
// configuración SIN la clave: solo dice que existe.
func caseUpsertAPIStoresRow(t *testing.T, m Montaje) {
	tenant := seedTenant(t, m)
	upsert(t, m, apiConfig(tenant), keyFirst, consentFirst)

	requireRow(t, m, tenant, rowAPI)
	cfg := requireConfig(t, m, tenant, tenantllm.Config{
		TenantID: tenant, Via: tenantllm.ViaAPI, Provider: tenantllm.ProviderAnthropic, Model: modelFirst,
		HasAPIKey: true, ConsentedAt: consentFirst,
	})
	if printed := fmt.Sprintf("%+v", cfg); strings.Contains(printed, keyPrefix) {
		t.Error("FUGA: el Config que devuelve Get lleva la clave")
	}
}

// caseAPIKeyReturnsStoredKey: lo que entra por Upsert sale por APIKey, igual, y solo por ahí.
func caseAPIKeyReturnsStoredKey(t *testing.T, m Montaje) {
	tenant := seedTenant(t, m)
	upsert(t, m, apiConfig(tenant), keyFirst, consentFirst)
	requireAPIKey(t, m, tenant, keyFirst)
	// Pedirla no la gasta ni la cambia.
	requireAPIKey(t, m, tenant, keyFirst)
	requireRow(t, m, tenant, rowAPI)
}

// caseUpsertAPIReplaces: cada upsert es la foto entera. El segundo reemplaza clave, proveedor,
// modelo y consentimiento; el alta (CreatedAt) no se pisa. A diferencia de las integraciones,
// aquí no hay «actualiza el modelo sin tocar la clave».
func caseUpsertAPIReplaces(t *testing.T, m Montaje) {
	tenant := seedTenant(t, m)
	upsert(t, m, apiConfig(tenant), keyFirst, consentFirst)
	before := mustGet(t, m, tenant)

	second := tenantllm.Config{TenantID: tenant, Via: tenantllm.ViaAPI, Provider: tenantllm.ProviderGemini, Model: modelSecond}
	upsert(t, m, second, keyRotated, consentSecond)

	requireAPIKey(t, m, tenant, keyRotated)
	requireRow(t, m, tenant, rowAPI)
	// El consentimiento es el del segundo Upsert: se re-afirma en cada uno.
	after := requireConfig(t, m, tenant, tenantllm.Config{
		TenantID: tenant, Via: tenantllm.ViaAPI, Provider: tenantllm.ProviderGemini, Model: modelSecond,
		HasAPIKey: true, ConsentedAt: consentSecond,
	})
	if !after.CreatedAt.Equal(before.CreatedAt) {
		t.Errorf("CreatedAt se pisó en el upsert: %v → %v", before.CreatedAt, after.CreatedAt)
	}
	if after.UpdatedAt.Before(before.UpdatedAt) {
		t.Errorf("UpdatedAt retrocedió: %v → %v", before.UpdatedAt, after.UpdatedAt)
	}
}

// requireLocalRow afirma la forma entera de un tenant en la vía local: fila sin eje api, Config
// con los NULL como valor cero y APIKey diciendo lo mismo que diría sin fila (R4.4.c).
func requireLocalRow(t *testing.T, m Montaje, tenant string) {
	t.Helper()
	requireRow(t, m, tenant, rowLocal)
	// El eje api llega en su valor cero: sin proveedor, sin modelo, sin clave y sin consentimiento.
	cfg := requireConfig(t, m, tenant, localConfig(tenant))
	if !cfg.ConsentedAt.IsZero() {
		t.Errorf("ConsentedAt de la fila local = %v, quería el instante cero", cfg.ConsentedAt)
	}
	requireNotConfigured(t, m, tenant)
}

// caseUpsertLocalRow: la vía local es una configuración completa que no exige nada: sin clave y
// sin consentimiento, la fila existe, tiene vía y no tiene nada del eje api.
func caseUpsertLocalRow(t *testing.T, m Montaje) {
	tenant := seedTenant(t, m)
	upsert(t, m, localConfig(tenant), "", time.Time{})
	requireLocalRow(t, m, tenant)
}

// caseUpsertLocalIgnoresAPIAxis: en la vía local, proveedor, modelo, clave y consentimiento se
// IGNORAN aunque vengan: la vía decide qué se escribe, y la local no manda nada a ningún tercero.
func caseUpsertLocalIgnoresAPIAxis(t *testing.T, m Montaje) {
	tenant := seedTenant(t, m)
	cfg := apiConfig(tenant)
	cfg.Via = tenantllm.ViaLocal
	cfg.HasAPIKey = true // un campo de lectura: tampoco se escribe
	upsert(t, m, cfg, keyFirst, consentFirst)
	requireLocalRow(t, m, tenant)
}

// caseUpsertAPIToLocal (R4.4.d): cambiar de vía RETIRA la credencial y el consentimiento. «Una
// sola vía activa»: no queda una credencial dormida esperando el regreso.
func caseUpsertAPIToLocal(t *testing.T, m Montaje) {
	tenant := seedTenant(t, m)
	upsert(t, m, apiConfig(tenant), keyFirst, consentFirst)
	before := mustGet(t, m, tenant)

	upsert(t, m, localConfig(tenant), "", time.Time{})

	requireLocalRow(t, m, tenant)
	if after := mustGet(t, m, tenant); !after.CreatedAt.Equal(before.CreatedAt) {
		t.Errorf("CreatedAt se pisó al cambiar de vía: %v → %v", before.CreatedAt, after.CreatedAt)
	}
}

// caseUpsertLocalToAPI: la vuelta. Un tenant local que configura la vía api queda con el eje
// entero, y lo que se retiró al irse no reaparece: la clave es la de ahora.
func caseUpsertLocalToAPI(t *testing.T, m Montaje) {
	tenant := seedTenant(t, m)
	upsert(t, m, apiConfig(tenant), keyFirst, consentFirst)
	upsert(t, m, localConfig(tenant), "", time.Time{})
	before := mustGet(t, m, tenant)

	second := apiConfig(tenant)
	second.Model = modelSecond
	upsert(t, m, second, keyRotated, consentSecond)

	requireRow(t, m, tenant, rowAPI)
	requireAPIKey(t, m, tenant, keyRotated)
	after := requireConfig(t, m, tenant, tenantllm.Config{
		TenantID: tenant, Via: tenantllm.ViaAPI, Provider: tenantllm.ProviderAnthropic, Model: modelSecond,
		HasAPIKey: true, ConsentedAt: consentSecond,
	})
	if !after.CreatedAt.Equal(before.CreatedAt) {
		t.Errorf("CreatedAt se pisó al volver a la vía api: %v → %v", before.CreatedAt, after.CreatedAt)
	}
}

// priorStates son los tres estados sobre los que un Upsert rechazado no puede escribir: sin
// fila, con fila api y con fila local.
func priorStates() []struct {
	name  string
	setup func(t *testing.T, m Montaje, tenant string)
} {
	return []struct {
		name  string
		setup func(t *testing.T, m Montaje, tenant string)
	}{
		{"no row", func(*testing.T, Montaje, string) {}},
		{"api row", func(t *testing.T, m Montaje, tenant string) {
			t.Helper()
			upsert(t, m, apiConfig(tenant), keyFirst, consentFirst)
		}},
		{"local row", func(t *testing.T, m Montaje, tenant string) {
			t.Helper()
			upsert(t, m, localConfig(tenant), "", time.Time{})
		}},
	}
}

// requireRejectedWithoutWriting (R4.4.e) hace el Upsert rechazado sobre cada estado previo y
// afirma, con la marca de estado, que no cambió NADA: ni la fila que hubiera ni su ausencia.
func requireRejectedWithoutWriting(t *testing.T, m Montaje, fragment string, attempt func(tenant string) error) {
	t.Helper()
	for _, prior := range priorStates() {
		tenant := seedTenant(t, m)
		prior.setup(t, m, tenant)
		before := captureState(t, m, tenant)

		err := attempt(tenant)
		if err == nil {
			t.Errorf("sobre «%s»: Upsert devolvió nil, quería el rechazo", prior.name)
		} else {
			requireRejected(t, err, fragment)
		}
		requireSameState(t, m, tenant, before)
	}
}

// caseUpsertRejectsVia: una vía vacía, inventada o casi buena no crea una fila que nadie sabría
// leer, y el rechazo lo da el store, no un CHECK de la base. La clave y el consentimiento van
// puestos: lo único malo es la vía.
func caseUpsertRejectsVia(t *testing.T, m Montaje) {
	for _, via := range []string{"", "remota", "API", " local", "api ", "ａｐｉ", "local|api"} {
		requireRejectedWithoutWriting(t, m, "fuera del vocabulario", func(tenant string) error {
			cfg := apiConfig(tenant)
			cfg.Via = via
			return m.Store.Upsert(context.Background(), cfg, keyRotated, consentSecond)
		})
	}
}

// caseUpsertRejectsMissingKey: la vía api no existe sin credencial. Una clave vacía se cifraría
// sin queja y dejaría una fila con sobre de no-valor: se para antes.
func caseUpsertRejectsMissingKey(t *testing.T, m Montaje) {
	requireRejectedWithoutWriting(t, m, "sin API key", func(tenant string) error {
		second := apiConfig(tenant)
		second.Model = modelSecond
		return m.Store.Upsert(context.Background(), second, "", consentSecond)
	})
}

// caseUpsertRejectsNoConsent: sin consentimiento no sale texto del cliente hacia un tercero. Un
// instante cero escribiría el año 1 en la columna: una mentira con fecha.
func caseUpsertRejectsNoConsent(t *testing.T, m Montaje) {
	requireRejectedWithoutWriting(t, m, "sin consentimiento", func(tenant string) error {
		second := apiConfig(tenant)
		second.Model = modelSecond
		return m.Store.Upsert(context.Background(), second, keyRotated, time.Time{})
	})
}
