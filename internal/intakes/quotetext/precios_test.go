package quotetext_test

// precios_test.go — EL VERIFICADOR (INV-2: el LLM nunca calcula precios).
//
// Las cuatro condiciones C1–C4 están documentadas en precios.go; aquí se ejercita cada
// una con el caso que la hace fallar, y la propiedad que las ata todas: el render
// determinista PASA su propio verificador, siempre. Sin esa propiedad el respaldo
// podría ser peor que lo que respalda, y nadie se enteraría.

import (
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes/quotetext"
)

// borradorFusion es el del caso base: dos tortas y el envío.
func borradorFusion() quotetext.Borrador { return quotetext.BorradorDe(lineasFusion) }

func TestVerificar_TextoBueno_Pasa(t *testing.T) {
	if v := quotetext.Verificar(borradorFusion(), textoDelModelo); !v.OK {
		t.Fatalf("el texto bueno se rechazó: motivo=%q detalle=%q", v.Motivo, v.Detalle)
	}
}

// TestVerificar_LasCuatroCondiciones recorre un fallo por cada una.
func TestVerificar_LasCuatroCondiciones(t *testing.T) {
	casos := []struct {
		nombre string
		texto  string
		motivo string
	}{
		{
			// C1 — falta el precio unitario del envío.
			nombre: "C1: falta un unitario",
			texto:  strings.Replace(textoDelModelo, "Envío — $490", "Envío incluido", 1),
			motivo: quotetext.MotivoFaltaUnitario,
		},
		{
			// C2 — están los tres unitarios y NO está el total.
			nombre: "C2: falta el total",
			texto:  strings.Replace(textoDelModelo, "Total $5540", "Te lo dejo así", 1),
			motivo: quotetext.MotivoFaltaTotal,
		},
		{
			// C3 — el modelo se inventa un importe MARCADO.
			nombre: "C3: importe inventado con $",
			texto:  textoDelModelo + "\nRecargo por decoración $800",
			motivo: quotetext.MotivoImporteAjeno,
		},
		{
			// C4 — el número inventado viene SIN marca de dinero. Es el agujero que C3
			// no ve, y el que hace falta C4.
			nombre: "C4: número grande inventado sin $",
			texto:  textoDelModelo + "\nSi lo quieres para 20 personas seria 6200 en total",
			motivo: quotetext.MotivoNumeroAjeno,
		},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			v := quotetext.Verificar(borradorFusion(), c.texto)
			if v.OK {
				t.Fatalf("el texto se aceptó y no debía")
			}
			if v.Motivo != c.motivo {
				t.Fatalf("motivo = %q; se esperaba %q (detalle: %s)", v.Motivo, c.motivo, v.Detalle)
			}
			if v.Detalle == "" {
				t.Error("un rechazo sin detalle no le sirve a nadie que lea el log")
			}
		})
	}
}

// TestVerificar_NumerosDelBorradorSePerdonan es la contraparte de C4: los números que
// YA estaban en el borrador —los que el prompt le pide al modelo que copie— no pueden
// tumbar un texto correcto aunque sean grandes.
//
// El caso es real: una etiqueta con «paquete x30» y un pedido cuyo importe más barato
// es 12, de modo que el umbral queda por DEBAJO de 30 y C4 sí lo mira.
func TestVerificar_NumerosDelBorradorSePerdonan(t *testing.T) {
	b := quotetext.BorradorDe([]intakes.Item{
		{SKU: "TEQ", Label: "Tequeños congelados paquete x30", Qty: 1, UnitPrice: 12},
	})
	texto := "Hola! El paquete x30 de tequeños te queda en $12. Total $12"

	if v := quotetext.Verificar(b, texto); !v.OK {
		t.Fatalf("el 30 de la etiqueta tumbó un texto correcto: motivo=%q detalle=%q", v.Motivo, v.Detalle)
	}

	// Control: el mismo texto con un número igual de grande que NO sale del borrador sí
	// se rechaza. Sin este control, el test de arriba pasaría también con C4 desactivada.
	if v := quotetext.Verificar(b, texto+" y el x40 sale igual"); v.OK {
		t.Fatal("un número grande ajeno al borrador tiene que tumbar el texto (si no, C4 no está haciendo nada)")
	}
}

// TestVerificar_TextoIlegible: lo que viene del modelo se valida ANTES de buscarle
// números, y un carácter de control no puede llegar a una columna TEXT de Postgres.
func TestVerificar_TextoIlegible(t *testing.T) {
	casos := map[string]string{
		"vacío":                 "   \n ",
		"con byte nulo":         "Total $5540\x00",
		"más largo que el tope": strings.Repeat("a", quotetext.MaxRunasTexto+1),
	}
	for nombre, texto := range casos {
		t.Run(nombre, func(t *testing.T) {
			if v := quotetext.Verificar(borradorFusion(), texto); v.OK ||
				v.Motivo != quotetext.MotivoTextoIlegible {
				t.Fatalf("motivo = %q (OK=%v); se esperaba %q", v.Motivo, v.OK, quotetext.MotivoTextoIlegible)
			}
		})
	}
}

// TestVerificar_NumeroDesbordado es la lección cara de este plan: lo que viene del
// modelo se valida en Go ANTES de convertirlo. Un número imposible tiene que dar un
// ERROR DE DATO nombrado, no un pánico ni un cero silencioso.
func TestVerificar_NumeroDesbordado(t *testing.T) {
	texto := "Total $" + strings.Repeat("9", 400)
	v := quotetext.Verificar(borradorFusion(), texto)
	if v.OK {
		t.Fatal("un número imposible no puede dar un texto válido")
	}
	if v.Motivo != quotetext.MotivoNumeroIlegible {
		t.Fatalf("motivo = %q; se esperaba %q", v.Motivo, quotetext.MotivoNumeroIlegible)
	}
}

// TestVerificar_BorradorSinImportes: sin nada con qué comparar no se puede afirmar
// nada, así que no se acepta.
func TestVerificar_BorradorSinImportes(t *testing.T) {
	b := quotetext.BorradorDe([]intakes.Item{{SKU: "X", Label: "Torta por presupuestar", Qty: 1}})
	if v := quotetext.Verificar(b, "Torta $2100. Total $2100"); v.OK ||
		v.Motivo != quotetext.MotivoSinImportes {
		t.Fatalf("motivo = %q (OK=%v); se esperaba %q", v.Motivo, v.OK, quotetext.MotivoSinImportes)
	}
}

// TestVerificar_LineaPorConfirmar_NoExigePrecioPeroSiLoProhibe: el envío sin precio no
// tiene unitario que exigir (C1 lo salta), pero si el modelo se inventa uno cae por C3.
func TestVerificar_LineaPorConfirmar_NoExigePrecioPeroSiLoProhibe(t *testing.T) {
	b := quotetext.BorradorDe([]intakes.Item{
		{SKU: "TORTA", Label: "Torta chocolate", Qty: 1, UnitPrice: 2100},
		{SKU: intakes.ShippingSKU, Label: "Envío", Qty: 1},
	})
	if v := quotetext.Verificar(b, "Torta $2100, el envío te lo confirmo con la zona. Total $2100"); !v.OK {
		t.Fatalf("se rechazó un texto correcto: motivo=%q detalle=%q", v.Motivo, v.Detalle)
	}
	if v := quotetext.Verificar(b, "Torta $2100, envío $700. Total $2100"); v.OK ||
		v.Motivo != quotetext.MotivoImporteAjeno {
		t.Fatalf("motivo = %q (OK=%v); un envío inventado tiene que caer por C3", v.Motivo, v.OK)
	}
}

// TestVerificar_SeparadoresDeMiles: el modelo puede escribir «$2.100» o «$5.540» y eso
// NO puede tumbar un texto correcto en es-CL.
func TestVerificar_SeparadoresDeMiles(t *testing.T) {
	texto := strings.NewReplacer(
		"$2100", "$2.100", "$2950", "$2.950", "$5540", "$5.540",
	).Replace(textoDelModelo)
	if v := quotetext.Verificar(borradorFusion(), texto); !v.OK {
		t.Fatalf("un texto con separador de miles se rechazó: motivo=%q detalle=%q", v.Motivo, v.Detalle)
	}
}

// TestVerificar_MonedaPorPalabra: «2950 pesos» es tan importe como «$2950», y es
// exactamente como lo escribió Herminia en el caso real.
func TestVerificar_MonedaPorPalabra(t *testing.T) {
	texto := strings.Replace(textoDelModelo, "$2950", "2950 pesos", 1)
	if v := quotetext.Verificar(borradorFusion(), texto); !v.OK {
		t.Fatalf("«2950 pesos» no se leyó como importe: motivo=%q detalle=%q", v.Motivo, v.Detalle)
	}
}

// TestRender_PasaSuPropioVerificador es LA PROPIEDAD del respaldo: pase lo que pase,
// lo que devolvemos cuando el modelo falla cuadra con las líneas.
//
// Se corre sobre varias formas de pedido —cantidades > 1, decimales, líneas por
// confirmar, etiquetas con números grandes— porque el riesgo real no es el caso
// bonito: es que el render escriba un `line_total` que el verificador no espere, o que
// una etiqueta con un número gordo dispare C4 contra el propio render.
//
// 🔴 LOS CASOS DE MÁS DE DOS DECIMALES ESTÁN AQUÍ PORQUE FALLABAN. `Importe` escribe
// dinero con dos decimales, así que un `unit_price` de 2100,005 salía como `$2100,01`
// y el verificador —que comparaba contra el crudo— lo llamaba `importe_ajeno`: el
// respaldo no pasaba su propio respaldo. Se arregló haciendo que el verificador razone
// en CÉNTIMOS, la misma precisión en la que se escribe (ver aCentimos).
func TestRender_PasaSuPropioVerificador(t *testing.T) {
	casos := map[string][]intakes.Item{
		"caso Fusión": lineasFusion,
		"con más de dos decimales": {
			{SKU: "A", Label: "Torta", Qty: 1, UnitPrice: 2100.005},
			{SKU: "B", Label: "Café", Qty: 3, UnitPrice: 0.333},
		},
		"decimales que se acumulan al multiplicar": {
			{SKU: "A", Label: "Empanada", Qty: 3, UnitPrice: 0.1},
			{SKU: "B", Label: "Jugo", Qty: 7, UnitPrice: 1.15},
		},
		"cantidades mayores que uno": {
			{SKU: "TEQ", Label: "Tequeños bandeja x30", Qty: 4, UnitPrice: 490},
			{SKU: "EMP", Label: "Empanadas", Qty: 12, UnitPrice: 250},
		},
		"con decimales": {
			{SKU: "A", Label: "Torta", Qty: 3, UnitPrice: 1234.5},
			{SKU: "B", Label: "Café", Qty: 1, UnitPrice: 0.99},
		},
		"con línea por confirmar": {
			{SKU: "A", Label: "Torta chocolate — 15 porciones", Qty: 1, UnitPrice: 2100},
			{SKU: intakes.ShippingSKU, Label: "Envío", Qty: 1},
		},
		"etiqueta con un número más grande que el precio": {
			{SKU: "A", Label: "Bandeja de 3000 mini empanadas", Qty: 1, UnitPrice: 12},
		},
		"con personalización": {
			{SKU: "A", Label: "Torta chocolate", Customization: "sin lactosa", Qty: 2, UnitPrice: 2100},
		},
	}
	for nombre, items := range casos {
		t.Run(nombre, func(t *testing.T) {
			b := quotetext.BorradorDe(items)
			texto := quotetext.Render(b)
			if v := quotetext.Verificar(b, texto); !v.OK {
				t.Fatalf("el render determinista NO pasa su propio verificador:\nmotivo=%q detalle=%q\n---\n%s",
					v.Motivo, v.Detalle, texto)
			}
		})
	}
}

// TestImporte_YaNumero_SonInversas ata el escritor con el lector. Sin este test, un
// cambio en el formato de Importe podría dejar de ser legible por el verificador y el
// único síntoma sería que TODO cae al determinista... que también es lo que el
// determinista produce, así que nadie lo notaría.
func TestImporte_YaNumero_SonInversas(t *testing.T) {
	for _, v := range []float64{0, 1, 12, 490, 2100, 5540, 1234.5, 0.99, 1000000} {
		b := quotetext.BorradorDe([]intakes.Item{{SKU: "A", Label: "Cosa", Qty: 1, UnitPrice: v}})
		if v == 0 {
			continue // una línea sin precio no tiene importe que releer.
		}
		texto := "Cosa " + quotetext.Importe(v) + ". Total " + quotetext.Importe(v)
		if ver := quotetext.Verificar(b, texto); !ver.OK {
			t.Errorf("Importe(%v) = %q no se relee como el mismo importe: motivo=%q detalle=%q",
				v, quotetext.Importe(v), ver.Motivo, ver.Detalle)
		}
	}
}
