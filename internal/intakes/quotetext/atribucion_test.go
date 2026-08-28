package quotetext_test

// atribucion_test.go — LOS IMPORTES CORRECTOS EN EL SITIO EQUIVOCADO.
//
// Los tres casos de esta tabla usan EXCLUSIVAMENTE importes que salen del borrador: no
// hay ni un número inventado. Aun así, los tres le mandan al cliente un precio que no
// es el suyo, y ése es el sentido de INV-2 que de verdad le importa a la dueña.
//
// Es un hueco que ninguna mutación podía revelar, porque no era un test que faltara:
// era la REGLA. La primera versión del verificador comparaba el CONJUNTO de importes
// —«¿este número sale de alguna línea?»— y por construcción no podía ver ninguno de los
// tres.

import (
	"context"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes/quotetext"
)

// textoSwap son las dos tortas del caso Fusión con los precios INTERCAMBIADOS. Ni un
// número inventado: 2950 y 2100 son los dos precios reales, cambiados de sitio.
const textoSwap = "Hola! Te paso el presupuesto:\n" +
	"Pastel para 15 personas, chocolate húmedo, relleno chocolate y oreo — $2950\n" +
	"El otro para 25-30 personas, vainilla, ddl y merengue — $2100\n" +
	"Envío — $490\n" +
	"Total $5540"

// TestVerificar_ImportesLegitimosEnElSitioEquivocado son los tres casos, sobre el
// borrador del caso Fusión (torta choc. $2100 · torta vainilla $2950 · envío $490).
//
// El motivo esperado NO es el mismo en los tres, y la diferencia dice algo: cuando la
// repetición PISA un importe, el que se pisó desaparece del texto y lo caza antes la
// cobertura (C1), con un diagnóstico mejor —«la línea 2 vale $2950 y no está»—. Cuando
// la repetición se AÑADE sin pisar nada, la cobertura está satisfecha y lo único que
// queda descuadrado es la secuencia. Los dos se rechazan; lo que cambia es qué se le
// cuenta a quien lea el log.
func TestVerificar_ImportesLegitimosEnElSitioEquivocado(t *testing.T) {
	casos := []struct {
		nombre string
		texto  string
		motivo string
		porque string
	}{
		{
			nombre: "SWAP: los precios de las dos tortas, intercambiados",
			texto:  textoSwap,
			motivo: quotetext.MotivoImportesFueraDeSitio,
			porque: "el cliente lee dos precios que no son los de sus productos",
		},
		{
			nombre: "cargo inventado que REUTILIZA un importe existente",
			// «Seña por adelantado: $490» — 490 es legítimo (es el envío), pero aquí
			// aparece una segunda vez como un concepto que no existe en el pedido.
			texto:  textoDelModelo + "\nSeña por adelantado: $490",
			motivo: quotetext.MotivoImportesFueraDeSitio,
			porque: "aparece un concepto que no está en el borrador, con un importe legítimo",
		},
		{
			nombre: "repetir un importe AÑADIENDO una mención",
			texto:  textoDelModelo + "\nY el segundo también a $2100",
			motivo: quotetext.MotivoImportesFueraDeSitio,
			porque: "el texto le atribuye a la segunda torta el precio de la primera",
		},
		{
			nombre: "repetir un importe PISANDO el que había",
			texto:  strings.Replace(textoDelModelo, "merengue — $2950", "merengue — $2100", 1),
			motivo: quotetext.MotivoFaltaUnitario,
			porque: "las dos tortas salen al mismo precio y una de las dos está mal",
		},
	}
	b := borradorFusion()
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			v := quotetext.Verificar(b, c.texto)
			if v.OK {
				t.Fatalf("el texto se ACEPTÓ y se le habría mandado al cliente: %s", c.porque)
			}
			if v.Motivo != c.motivo {
				t.Errorf("motivo = %q; se esperaba %q (detalle: %s)", v.Motivo, c.motivo, v.Detalle)
			}
		})
	}
}

// TestSugerir_SwapDePrecios_CaeAlDeterminista comprueba que el hueco se cierra por el
// camino REAL —el del servicio— y no solo en la función pura.
//
// 🔴 El assert no es vacuo: se exige que al modelo SE LE LLAMARA (`prov.veces() == 1`).
// Sin eso, este test pasaría igual si el generador nunca hubiera llegado a llamar.
func TestSugerir_SwapDePrecios_CaeAlDeterminista(t *testing.T) {
	e := nuevaEscena(t, artefactoP5(t, textoSwap)).conHistorialDeHerminia(t)

	out, err := e.svc.Sugerir(context.Background(), tenantDePrueba, intakeDePrueba)
	if err != nil {
		t.Fatalf("Sugerir: %v", err)
	}
	if got := e.prov.veces(); got != 1 {
		t.Fatalf("se llamó al modelo %d veces; con historial tiene que llamarse 1 "+
			"(si es 0, este test no mira la rama que dice)", got)
	}
	if out.Origen != quotetext.OrigenDeterminista || out.Motivo != quotetext.MotivoImportesFueraDeSitio {
		t.Fatalf("origen=%q motivo=%q; un swap de precios tiene que caer al determinista",
			out.Origen, out.Motivo)
	}
}

// TestVerificar_DosLineasAlMismoPrecio_EsLegitimo es el contrapeso: contar apariciones
// no puede rechazar el caso REAL en que dos líneas valen lo mismo.
func TestVerificar_DosLineasAlMismoPrecio_EsLegitimo(t *testing.T) {
	b := quotetext.BorradorDe([]intakes.Item{
		{SKU: "A", Label: "Torta chocolate — 15 porciones", Qty: 1, UnitPrice: 2100},
		{SKU: "B", Label: "Torta vainilla — 15 porciones", Qty: 1, UnitPrice: 2100},
	})
	texto := "Hola! La de chocolate te queda en $2100 y la de vainilla también en $2100. Total $4200"

	if v := quotetext.Verificar(b, texto); !v.OK {
		t.Fatalf("dos líneas al mismo precio son legítimas y se rechazó: motivo=%q detalle=%q",
			v.Motivo, v.Detalle)
	}
}

// TestVerificar_ElNumeroDesnudoNoApagaElGenerador es el segundo hallazgo de la
// auditoría: una línea barata NO puede convertir cualquier fecha, hora o cantidad del
// texto en un rechazo.
//
// Con `galleta $2 + torta $2100`, la primera versión de C4 ponía el listón en 2 —el
// importe más barato— y rechazaba «te llamo en 3 días». El síntoma habría sido que «la
// voz de la dueña» dejaba de funcionar de facto en cuanto el pedido llevara algo
// barato, y el único rastro sería un `fallback_reason` en el log.
func TestVerificar_ElNumeroDesnudoNoApagaElGenerador(t *testing.T) {
	b := quotetext.BorradorDe([]intakes.Item{
		{SKU: "GALLETA", Label: "Galleta decorada", Qty: 1, UnitPrice: 2},
		{SKU: "TORTA", Label: "Torta chocolate", Qty: 1, UnitPrice: 2100},
	})
	base := "Hola! La galleta te queda en $2 y la torta en $2100. Total $2102."

	inocuos := map[string]string{
		"un plazo en días":    " Te llamo en 3 días.",
		"una fecha":           " Es para el 30 de agosto.",
		"unas porciones":      " La torta es de 12 porciones.",
		"una hora sin puntos": " Te lo llevo a las 1930.",
	}
	for nombre, cola := range inocuos {
		t.Run(nombre, func(t *testing.T) {
			if v := quotetext.Verificar(b, base+cola); !v.OK {
				t.Fatalf("un número inocuo apagó el generador: motivo=%q detalle=%q",
					v.Motivo, v.Detalle)
			}
		})
	}

	// 🔴 CONDICIÓN (a) DE LA AUDITORÍA: el caso peligroso sigue cayendo igual de duro.
	t.Run("un importe MARCADO ajeno sigue cayendo", func(t *testing.T) {
		v := quotetext.Verificar(b, base+" Con decoración extra son $3000.")
		if v.OK || v.Motivo != quotetext.MotivoImporteAjeno {
			t.Fatalf("motivo=%q (OK=%v); un importe marcado ajeno tiene que caer por C3",
				v.Motivo, v.OK)
		}
	})
}
