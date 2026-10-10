package store_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// Aserciones de compilación: los dos adaptadores leen la configuración y llevan la bienvenida.
// WelcomeStore NO entra en Repository a propósito: solo lo necesita el runtime cuando la
// bienvenida está cableada.
var (
	_ store.TenantSettingsReader = (*store.MemoryRepository)(nil)
	_ store.TenantSettingsReader = (*store.PostgresRepository)(nil)
	_ store.TenantSettingsReader = store.Repository(nil)
	_ store.WelcomeStore         = (*store.MemoryRepository)(nil)
	_ store.WelcomeStore         = (*store.PostgresRepository)(nil)
)

// TestDefaults_MirrorTheColumnDefaults: cada valor por defecto es el DEFAULT de su columna en
// public.tenant_settings (migraciones 0013, 0052, 0067, 0072 y 0076), por su número.
func TestDefaults_MirrorTheColumnDefaults(t *testing.T) {
	if store.DefaultPageSize != 5 {
		t.Errorf("DefaultPageSize = %d, quería 5", store.DefaultPageSize)
	}
	cases := []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"DefaultOrderTTL", store.DefaultOrderTTL, 3600 * time.Second},
		{"DefaultConversationTTL", store.DefaultConversationTTL, 7200 * time.Second},
		{"DefaultEventInactivityTTL", store.DefaultEventInactivityTTL, 7200 * time.Second},
		{"DefaultEventHistoryTTL", store.DefaultEventHistoryTTL, 0},
		{"DefaultAggregationWindow", store.DefaultAggregationWindow, 45 * time.Second},
		{"DefaultAggregationMax", store.DefaultAggregationMax, 120 * time.Second},
		{"DefaultWelcomeSilence", store.DefaultWelcomeSilence, 86400 * time.Second},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %v, quería %v", tc.name, tc.got, tc.want)
		}
	}
	// 24 h de silencio están muy por encima de los dos relojes de conversación (2 h): es lo que
	// impide que el umbral de la bienvenida venza DURANTE una conversación viva.
	if store.DefaultWelcomeSilence <= store.DefaultConversationTTL || store.DefaultWelcomeSilence <= store.DefaultEventInactivityTTL {
		t.Errorf("DefaultWelcomeSilence (%v) no está por encima de los relojes de conversación", store.DefaultWelcomeSilence)
	}
}

// TestDefaultWelcomeText_Literal: el texto de plataforma de la bienvenida, byte a byte. Lo ve el
// cliente final por WhatsApp.
func TestDefaultWelcomeText_Literal(t *testing.T) {
	const want = "¡Hola! Recibimos tu mensaje y lo estamos procesando. Te respondemos en unos minutos."
	if store.DefaultWelcomeText != want {
		t.Errorf("DefaultWelcomeText = %q, quería %q", store.DefaultWelcomeText, want)
	}
}

// TestDefaultTenantSettings_NamesEveryKey: la configuración de un tenant SIN fila nombra TODAS las
// claves con su valor de plataforma y lleva el tenant pedido. Omitir una dejaría el cero de Go,
// que en ConversationTTL es «sin vencimiento» y en las de la agregación y la bienvenida es
// «vencido siempre»: una funcionalidad entera apagada para la mayoría de los tenants.
func TestDefaultTenantSettings_NamesEveryKey(t *testing.T) {
	for _, tenant := range []string{"tenant-1", ""} {
		got := store.DefaultTenantSettings(tenant)
		want := store.TenantSettings{
			TenantID:           tenant,
			PageSize:           5,
			OrderTTL:           time.Hour,
			ConversationTTL:    2 * time.Hour,
			EventInactivityTTL: 2 * time.Hour,
			EventHistoryTTL:    0,
			AggregationWindow:  45 * time.Second,
			AggregationMax:     120 * time.Second,
			WelcomeText:        store.DefaultWelcomeText,
			WelcomeSilence:     24 * time.Hour,
		}
		if got.BuyerFields != nil {
			t.Errorf("DefaultTenantSettings(%q).BuyerFields = %+v, quería nil (el carrito no pregunta nada)", tenant, got.BuyerFields)
		}
		got.BuyerFields = nil
		if got.TenantID != want.TenantID || got.PageSize != want.PageSize || got.OrderTTL != want.OrderTTL ||
			got.ConversationTTL != want.ConversationTTL || got.EventInactivityTTL != want.EventInactivityTTL ||
			got.EventHistoryTTL != want.EventHistoryTTL || got.AggregationWindow != want.AggregationWindow ||
			got.AggregationMax != want.AggregationMax || got.WelcomeText != want.WelcomeText ||
			got.WelcomeSilence != want.WelcomeSilence {
			t.Errorf("DefaultTenantSettings(%q) = %+v, quería %+v", tenant, got, want)
		}
	}
}

// TestBuyerField_JSONContract: las etiquetas json son el contrato con la columna JSONB y con el
// viaje por Conversation.Vars: `key`, `label` y `required`, y las tres viajan siempre.
func TestBuyerField_JSONContract(t *testing.T) {
	raw, err := json.Marshal(store.BuyerField{Key: "rut", Label: "RUT", Required: true})
	if err != nil || string(raw) != `{"key":"rut","label":"RUT","required":true}` {
		t.Errorf("BuyerField serializado = %s (%v), quería {\"key\":\"rut\",\"label\":\"RUT\",\"required\":true}", raw, err)
	}
	raw, err = json.Marshal(store.BuyerField{Key: "ref"})
	if err != nil || string(raw) != `{"key":"ref","label":"","required":false}` {
		t.Errorf("BuyerField sin etiqueta = %s (%v), quería las tres claves aunque estén a cero", raw, err)
	}
	var got []store.BuyerField
	if err := json.Unmarshal([]byte(`[{"key":"rut","label":"RUT","required":true},{"key":"ref"}]`), &got); err != nil {
		t.Fatalf("deserializar el checklist: %v", err)
	}
	if len(got) != 2 || got[0] != (store.BuyerField{Key: "rut", Label: "RUT", Required: true}) || got[1] != (store.BuyerField{Key: "ref"}) {
		t.Errorf("checklist = %+v, quería los dos campos en orden", got)
	}
}

// TestWelcomeMark_ZeroMeansNeverSpokeNeverWelcomed: los dos ceros de la marca significan cosas
// distintas: sin LastIncomingAt no hay fila; sin WelcomedAt nunca se le saludó.
func TestWelcomeMark_ZeroMeansNeverSpokeNeverWelcomed(t *testing.T) {
	zero := store.WelcomeMark{}
	if !zero.LastIncomingAt.IsZero() || !zero.WelcomedAt.IsZero() {
		t.Errorf("la marca cero = %+v, quería los dos instantes a cero", zero)
	}
	at := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	spoke := store.WelcomeMark{LastIncomingAt: at}
	if spoke.LastIncomingAt.IsZero() || !spoke.WelcomedAt.IsZero() {
		t.Errorf("la marca de quien habló y no fue saludado = %+v", spoke)
	}
}
