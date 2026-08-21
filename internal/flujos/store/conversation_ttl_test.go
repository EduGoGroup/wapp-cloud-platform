// conversation_ttl_test.go cubre la MITAD EN MEMORIA del criterio (a) de T4.4 (Plan
// 046 · Ola 4, D-046.12, REQ-19): el tenant SIN fila en tenant_settings obtiene
// ConversationTTL = 2 h, y no el cero que valía antes.
//
// 🔴 POR QUÉ ESTA MITAD EXISTE, SI YA HAY UNA CONTRA POSTGRES. Los dos repositorios
// comparten DefaultTenantSettings a propósito, «para que los dos no puedan divergir en
// silencio como divergirían dos literales copiados» (store.go, cabecera de esa
// función). Ese contrato solo se sostiene si alguien lo mide EN LOS DOS: un test único
// contra Postgres dejaría al gemelo en memoria libre de volver al cero sin que nada se
// pusiera rojo, y el gemelo es el que usa medio árbol de tests del runtime.
package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// TestDefaultTenantSettings_ConversationTTLEsDosHoras ancla el valor en su origen.
//
// 💥 MUTACIÓN: quitar la línea `ConversationTTL: DefaultConversationTTL` de
// DefaultTenantSettings (que es exactamente como estaba antes de T4.4) ⇒ ROJO aquí.
// Es la regresión concreta que esta tarea vino a cerrar: mientras esa línea faltó, el
// tenant sin fila heredaba el cero de Go —«sin vencimiento»— aunque la columna de la
// BD dijera otra cosa, porque este camino no llega a mirar la BD.
func TestDefaultTenantSettings_ConversationTTLEsDosHoras(t *testing.T) {
	got := store.DefaultTenantSettings(uuid.NewString())
	if got.ConversationTTL != 2*time.Hour {
		t.Fatalf("ConversationTTL por defecto = %v, quiero 2h (7200s, D-046.12). "+
			"Un 0 aquí significa «sin vencimiento» y devuelve el hallazgo de privacidad "+
			"del Plan 046: el flow_state y sus vars —con el texto literal del cliente— "+
			"no caducarían nunca", got.ConversationTTL)
	}
	if got.ConversationTTL != store.DefaultConversationTTL {
		t.Fatalf("DefaultTenantSettings no usa la constante: %v vs %v",
			got.ConversationTTL, store.DefaultConversationTTL)
	}
}

// TestMemoryTenantSettings_SinFilaHeredaDosHoras es la mitad en memoria del criterio
// (a): el gemelo tiene que responder lo mismo que Postgres para un tenant que nunca
// configuró nada.
func TestMemoryTenantSettings_SinFilaHeredaDosHoras(t *testing.T) {
	repo := store.NewMemoryRepository()

	got, err := repo.GetTenantSettings(context.Background(), uuid.NewString())
	if err != nil {
		t.Fatalf("GetTenantSettings (sin fila): %v", err)
	}
	if got.ConversationTTL != 2*time.Hour {
		t.Fatalf("ConversationTTL sin fila = %v, quiero 2h: el gemelo en memoria "+
			"divergió del default compartido", got.ConversationTTL)
	}
}

// TestMemoryTenantSettings_CeroExplicitoDeConversacionSobrevive protege la otra mitad
// de la semántica, que es la que hace que el backfill de T4.4 tenga que ser un runbook
// y no un UPDATE dentro de la migración.
//
// 🔑 `0` sigue siendo un valor LEGÍTIMO: significa «esta empresa no vence
// conversaciones». Lo que T4.4 cambia es con qué NACE la columna, no que el 0 deje de
// poder elegirse. Si este test se pusiera rojo, el default estaría pisando overrides —
// y el runbook backfill-046-conversation-ttl.sql, que solo toca filas `= 0`, estaría
// borrando decisiones de cliente en cada arranque.
func TestMemoryTenantSettings_CeroExplicitoDeConversacionSobrevive(t *testing.T) {
	repo := store.NewMemoryRepository()
	tenant := uuid.NewString()
	fila := store.DefaultTenantSettings(tenant)
	fila.ConversationTTL = 0 // override explícito: esta empresa no vence conversaciones.
	repo.SetTenantSettings(fila)

	got, err := repo.GetTenantSettings(context.Background(), tenant)
	if err != nil {
		t.Fatalf("GetTenantSettings: %v", err)
	}
	if got.ConversationTTL != 0 {
		t.Fatalf("un 0 explícito se leyó como %v: el default pisó el override, "+
			"y con él la elección del cliente", got.ConversationTTL)
	}
}
