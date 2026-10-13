package storehelpertest

import (
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
)

// Los casos de TenantSettingsReader: GetTenantSettings sobre tenant_settings. «Hay fila» y «no
// hay fila» son dos caminos distintos, y esa diferencia es todo lo que estos casos vigilan.

// mustSettings lee la configuración del tenant o falla el test.
func mustSettings(t *testing.T, m Montaje, tenant string) Settings {
	t.Helper()
	got, err := m.Store.GetTenantSettings(ctx, tenant)
	if err != nil {
		t.Fatalf("GetTenantSettings(%s): %v", tenant, err)
	}
	return got
}

// requireSettings afirma la configuración entera del tenant.
func requireSettings(t *testing.T, m Montaje, what, tenant string, want Settings) {
	t.Helper()
	if got := mustSettings(t, m, tenant); !sameSettings(got, want) {
		t.Errorf("%s: GetTenantSettings(%s) = %+v, quería %+v", what, tenant, got, want)
	}
}

// caseSettingsDefaults: un tenant SIN fila recibe los valores de plataforma, sin error, con su
// tenant puesto. Se afirman uno a uno y por su número, no contra las constantes: es el camino por
// el que pasan la mayoría de los tenants, y un cero donde va un plazo apaga una funcionalidad
// entera (la conversación que no caduca, la agregación desactivada, la bienvenida en cada mensaje).
func caseSettingsDefaults(t *testing.T, m Montaje) {
	got := mustSettings(t, m, m.TenantA)
	want := Settings{
		TenantID:           m.TenantA,
		PageSize:           5,
		OrderTTL:           time.Hour,
		ConversationTTL:    2 * time.Hour,
		EventInactivityTTL: 2 * time.Hour,
		EventHistoryTTL:    0,
		AggregationWindow:  45 * time.Second,
		AggregationMax:     120 * time.Second,
		WelcomeText:        "¡Hola! Recibimos tu mensaje y lo estamos procesando. Te respondemos en unos minutos.",
		WelcomeSilence:     24 * time.Hour,
	}
	if !sameSettings(got, want) {
		t.Errorf("configuración de un tenant sin fila = %+v, quería %+v", got, want)
	}
	if got.ConversationTTL != store.DefaultConversationTTL {
		t.Errorf("ConversationTTL sin fila = %v, quería DefaultConversationTTL (%v)", got.ConversationTTL, store.DefaultConversationTTL)
	}
	if !sameSettings(got, store.DefaultTenantSettings(m.TenantA)) {
		t.Errorf("configuración de un tenant sin fila = %+v, quería DefaultTenantSettings", got)
	}
}

// caseSettingsZeros: con fila, lo que diga la fila, CEROS INCLUIDOS. Un 0 es un override explícito
// («sin vencimiento», «flush inmediato», «vencido siempre») y un texto vacío es lo que la columna
// trae por defecto: ninguno se sustituye por el valor de plataforma. El otro tenant, que no tiene
// fila, sigue con los de plataforma.
func caseSettingsZeros(t *testing.T, m Montaje) {
	before := take(t, m)
	zeros := Settings{TenantID: m.TenantA}
	m.SetSettings(t, zeros)
	requireSettings(t, m, "fila a cero", m.TenantA, zeros)
	requireSettings(t, m, "el tenant sin fila", m.TenantB, store.DefaultTenantSettings(m.TenantB))

	// La marca de estado ve la fila (la configuración de A cambió) y nada más.
	after := take(t, m)
	want := before.tenants[m.TenantA]
	want.settings = zeros
	before.tenants[m.TenantA] = want
	requireSameWorld(t, "sembrar la configuración", after, before)
}

// caseSettingsValues: cada columna sale por su campo y en su unidad (los segundos, como duración),
// y el checklist del comprador sale en su orden. Sembrar otra vez sustituye la fila.
func caseSettingsValues(t *testing.T, m Montaje) {
	custom := Settings{
		TenantID:        m.TenantA,
		PageSize:        8,
		OrderTTL:        30 * time.Minute,
		ConversationTTL: 15 * time.Minute,
		BuyerFields: []store.BuyerField{
			{Key: "rut", Label: "RUT", Required: true},
			{Key: "referencia", Label: "Referencia de entrega", Required: false},
		},
		EventInactivityTTL: 20 * time.Minute,
		EventHistoryTTL:    24 * time.Hour,
		AggregationWindow:  30 * time.Second,
		AggregationMax:     90 * time.Second,
		WelcomeText:        "Gracias, ya lo estamos viendo.",
		WelcomeSilence:     3 * time.Hour,
	}
	m.SetSettings(t, custom)
	requireSettings(t, m, "fila con valores propios", m.TenantA, custom)

	// Diez valores distintos entre sí y distintos de los de arriba: una columna leída en el campo
	// de otra no puede pasar.
	other := Settings{
		TenantID: m.TenantB, PageSize: 3, OrderTTL: 11 * time.Second, ConversationTTL: 12 * time.Second,
		BuyerFields:        []store.BuyerField{{Key: "direccion", Label: "", Required: true}},
		EventInactivityTTL: 13 * time.Second, EventHistoryTTL: 14 * time.Second,
		AggregationWindow: 15 * time.Second, AggregationMax: 16 * time.Second,
		WelcomeText: "Otro texto", WelcomeSilence: 17 * time.Second,
	}
	m.SetSettings(t, other)
	requireSettings(t, m, "fila del otro tenant", m.TenantB, other)
	requireSettings(t, m, "la primera fila sigue igual", m.TenantA, custom)

	custom.PageSize, custom.BuyerFields, custom.WelcomeText = 10, nil, ""
	m.SetSettings(t, custom)
	requireSettings(t, m, "fila sustituida", m.TenantA, custom)
}
