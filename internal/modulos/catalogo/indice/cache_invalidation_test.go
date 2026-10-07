package indice_test

import (
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo/indice"
)

// cache_invalidation_test.go es el trozo de cache_test.go (E-13) con la
// invalidación por contenido y el aislamiento por tenant. Los documentos y la
// fuente falsa están en cache_test.go.

// ---------------------------------------------------------------------------
// INVALIDACIÓN: SIN REINICIAR EL PROCESO, POR LOS DOS CAMINOS
// ---------------------------------------------------------------------------

// TestObtener_ImportAndManualPutBothInvalidate.
//
// Los dos caminos se prueban por separado porque el criterio los nombra por
// separado, y conviene decir por qué acaban siendo el mismo test: desde la caché
// son INDISTINGUIBLES por construcción. Esa indistinguibilidad ES la razón de
// D-044.44: una invalidación por versión habría atendido al import y se habría
// comido el `PUT` en silencio.
func TestObtener_ImportAndManualPutBothInvalidate(t *testing.T) {
	t.Run("import: the document grows", func(t *testing.T) {
		f := newFakeSource()
		f.publish("t1", docV1)
		c := newCache(t, f, 0)

		old := get(t, c, "t1")
		if _, ok := old.PorSKU("CAFE-DESC"); ok || old.Articulos() != 2 {
			t.Fatalf("el índice de docV1 tiene %d artículos y CAFE-DESC = %v; se esperaban 2 y false", old.Articulos(), ok)
		}

		f.publish("t1", docV2) // ← el import escribe el documento nuevo

		fresh := get(t, c, "t1")
		if fresh == old {
			t.Fatalf("tras el import se devolvió el índice viejo")
		}
		if _, ok := fresh.PorSKU("CAFE-DESC"); !ok || fresh.Articulos() != 3 {
			t.Errorf("el índice nuevo tiene %d artículos y CAFE-DESC = %v; 🔴 el siguiente match usa el catálogo NUEVO, sin reiniciar", fresh.Articulos(), ok)
		}
		assertStats(t, c, indice.Estadisticas{Construcciones: 2}, "el cambio de contenido reconstruye")
		if c.Tamano() != 1 {
			t.Errorf("Tamano() = %d; el índice nuevo SUSTITUYE al viejo del tenant", c.Tamano())
		}
	})

	t.Run("manual PUT: one digit changes and the size does not", func(t *testing.T) {
		if len(docV1) != len(docV1PriceTouched) {
			t.Fatalf("el fixture tiene que pesar lo mismo (%d ≠ %d) o no prueba nada", len(docV1), len(docV1PriceTouched))
		}
		f := newFakeSource()
		f.publish("t1", docV1)
		c := newCache(t, f, 0)

		if coffee, _ := get(t, c, "t1").PorSKU("CAFE"); coffee.Articulo.Price != 2.5 {
			t.Fatalf("precio antes del PUT = %v; se esperaba 2.5", coffee.Articulo.Price)
		}

		f.publish("t1", docV1PriceTouched) // ← el PUT genérico, que NO versiona

		if coffee, _ := get(t, c, "t1").PorSKU("CAFE"); coffee.Articulo.Price != 2.9 {
			t.Errorf("precio tras el PUT = %v; 🔴 un catálogo editado a mano tiene que invalidar igual que uno importado (2.9)", coffee.Articulo.Price)
		}
		assertStats(t, c, indice.Estadisticas{Construcciones: 2}, "el PUT reconstruye")
	})

	t.Run("going back to the previous document also invalidates", func(t *testing.T) {
		f := newFakeSource()
		f.publish("t1", docV1)
		c := newCache(t, f, 0)

		get(t, c, "t1")
		f.publish("t1", docV2)
		get(t, c, "t1")
		f.publish("t1", docV1) // deshacer: el contenido «retrocede»

		if back := get(t, c, "t1"); back.Articulos() != 2 {
			t.Errorf("Articulos() = %d al volver a docV1; la huella no es un número de versión: no tiene sentido de avance", back.Articulos())
		}
		assertStats(t, c, indice.Estadisticas{Construcciones: 3}, "solo se guarda el índice del contenido vigente")
	})
}

// ---------------------------------------------------------------------------
// AISLAMIENTO POR TENANT
// ---------------------------------------------------------------------------

// TestObtener_AChangeInOneTenantDoesNotTouchAnother: la caché es por tenant y el
// aislamiento es la mitad silenciosa del criterio (INV-8, el tenant no se cruza).
func TestObtener_AChangeInOneTenantDoesNotTouchAnother(t *testing.T) {
	f := newFakeSource()
	f.publish("t1", docV1)
	f.publish("t2", docV1)
	c := newCache(t, f, 0)

	idx1 := get(t, c, "t1")
	idx2 := get(t, c, "t2")
	if idx1 == idx2 {
		t.Errorf("dos tenants con el mismo contenido comparten índice; la entrada es por tenant")
	}
	assertStats(t, c, indice.Estadisticas{Construcciones: 2}, "cada tenant construye el suyo aunque el contenido coincida")

	f.publish("t2", docV2)
	if fresh2 := get(t, c, "t2"); fresh2.Articulos() != 3 {
		t.Errorf("t2 tiene %d artículos tras su cambio; se esperaban 3", fresh2.Articulos())
	}
	if same1 := get(t, c, "t1"); same1 != idx1 || same1.Articulos() != 2 {
		t.Errorf("el catálogo de t1 no se ha tocado y su índice cambió (%d artículos)", same1.Articulos())
	}
	assertStats(t, c, indice.Estadisticas{Construcciones: 3, Aciertos: 1}, "t1, t2 y el t2 nuevo: tres, no cuatro")

	// Alternarlos no los hace pelearse por una entrada.
	for range 3 {
		get(t, c, "t1")
		get(t, c, "t2")
	}
	assertStats(t, c, indice.Estadisticas{Construcciones: 3, Aciertos: 7}, "alternar tenants no reconstruye")
	if c.Tamano() != 2 {
		t.Errorf("Tamano() = %d; se esperaban 2 tenants", c.Tamano())
	}
}
