package ingest_test

import (
	"context"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/ingest"
)

// TestMemoryDeduper_PrimerAvistamientoLuegoDuplicado: la MISMA clave devuelve false
// la primera vez (nuevo) y true después (duplicado); claves distintas son
// independientes (por session_id y por wa_message_id).
func TestMemoryDeduper_PrimerAvistamientoLuegoDuplicado(t *testing.T) {
	d := ingest.NewMemoryDeduper()
	ctx := context.Background()

	seen, err := d.Seen(ctx, "sess-1", "wamid.A")
	if err != nil || seen {
		t.Fatalf("primer avistamiento: seen=%v err=%v (quiero false,nil)", seen, err)
	}
	seen, err = d.Seen(ctx, "sess-1", "wamid.A")
	if err != nil || !seen {
		t.Fatalf("repetición: seen=%v err=%v (quiero true,nil)", seen, err)
	}

	// Distinto wa_message_id en la misma sesión ⇒ nuevo.
	if seen, err := d.Seen(ctx, "sess-1", "wamid.B"); err != nil || seen {
		t.Fatalf("wa_message_id distinto debió ser nuevo (seen=%v err=%v)", seen, err)
	}
	// Mismo wa_message_id en OTRA sesión ⇒ nuevo (la clave incluye session_id).
	if seen, err := d.Seen(ctx, "sess-2", "wamid.A"); err != nil || seen {
		t.Fatalf("misma wamid en otra sesión debió ser nueva (seen=%v err=%v)", seen, err)
	}
}
