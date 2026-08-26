package stages_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/stages"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// match_test.go — LOS CRITERIOS DE T3.2, uno por test y con literales escritos a
// mano. Nada de recalcular en el test lo que el código calcula: un total que el test
// vuelve a sumar con la misma fórmula que el código pasa siempre.

// ---------------------------------------------------------------------------
// CRITERIO PRINCIPAL — EL REPLAY DEL CASO AMBAR
// ---------------------------------------------------------------------------

// itemsDeAmbar son los tres ítems tal como los deja P4 (design §7.3).
func itemsDeAmbar() []llm.NormalizedItem {
	return []llm.NormalizedItem{
		{
			Product:         "torta de chocolate",
			Qty:             1,
			Range:           &llm.Range{Min: 10, Max: 12, Unit: "porciones"},
			AddonCandidates: []string{"decoración infantil"},
			Customizations:  []string{"sin lactosa"},
			Evidence:        evidenciaTortaChocolate,
		},
		{
			Product:  "torta de vainilla con lluvia de colores",
			Qty:      1,
			Range:    &llm.Range{Min: 25, Max: 30, Unit: "porciones"},
			Evidence: evidenciaTortaVainilla,
		},
		{
			Product:     "tequeños congelados",
			Qty:         1,
			UnitKind:    "package",
			PackageSize: 30,
			Evidence:    evidenciaTequenos,
		},
	}
}

// TestMatch_ReplayDeAmbar es el criterio del plan entero: tequeños con match exacto
// a $490, torta de chocolate con `variant_options` 10-12, torta de vainilla
// `unmatched`, línea de envío presente y el escalón caro invocado como mucho UNA vez
// por ítem no cubierto.
//
// 🔴 CADA ASERCIÓN MIRA EL CONTENIDO, NO EL RECUENTO. Contar líneas es exactamente
// lo que en la Ola 2 dejó pasar un `done` con `items=0`: un artefacto con cinco
// renglones vacíos también tiene cinco renglones.
func TestMatch_ReplayDeAmbar(t *testing.T) {
	gz := &zonaGrisFalsa{respuestas: map[string]int{"torta de chocolate": 0}}
	m, store := matchDe(t, stages.ConZonaGris(gz))

	art, err := m.Run(context.Background(), jobDeAmbar(), stages.EntradaMatch{
		Cantidades: p4De(itemsDeAmbar()...),
		Indice:     indiceDe(t, catalogoAmbar()),
		Nota:       stages.SinNotaDePedido,
	})
	require.NoError(t, err)

	// El orden ES contrato: por cada ítem su línea y detrás sus añadidos; el envío
	// el último (design §7.5).
	require.Len(t, art.Lines, 5)

	// (1) TORTA DE CHOCOLATE — la resolvió la zona gris y su tamaño lo elige el dueño.
	choc := art.Lines[0]
	require.Equal(t, stages.KindMatched, choc.Kind)
	require.Equal(t, "TORTA-CHOC", choc.SKU)
	require.Equal(t, "Torta chocolate húmedo + crema choc.", choc.Label, "la etiqueta se COPIA del catálogo, no se deja la del cliente")
	require.Nil(t, choc.UnitPrice, "el rango cruza dos variantes: el precio lo pone el dueño, no el match")
	require.Equal(t, []stages.OpcionVariante{
		{SKU: "TORTA-CHOC#10", Label: "Torta chocolate húmedo + crema choc. — 10 porciones", Price: 2100},
		{SKU: "TORTA-CHOC#12", Label: "Torta chocolate húmedo + crema choc. — 12 porciones", Price: 2400},
	}, choc.VariantOptions, "las 25 porciones NO son candidatas de un rango 10-12")
	require.Equal(t, "sin lactosa", choc.Customization)
	require.Equal(t, &llm.Range{Min: 10, Max: 12, Unit: "porciones"}, choc.Range, "el rango no se colapsa")
	require.NotNil(t, choc.Match)
	require.Equal(t, "zona_gris_falsa", choc.Match.Strategy, "la procedencia tiene que decir QUIÉN lo decidió")

	// (2) EL AÑADIDO FACTURABLE — línea propia, detrás de la torta, con su precio.
	deco := art.Lines[1]
	require.Equal(t, stages.KindMatched, deco.Kind)
	require.Equal(t, "DECO-INF", deco.SKU)
	require.Equal(t, 800.0, precio(t, deco))
	require.Equal(t, 1, deco.Qty)

	// (3) TORTA DE VAINILLA — el catálogo no la tiene: `unmatched` y precio VACÍO.
	vainilla := art.Lines[2]
	require.Equal(t, stages.KindUnmatched, vainilla.Kind)
	require.Empty(t, vainilla.SKU)
	require.Nil(t, vainilla.UnitPrice)
	require.Equal(t, "torta de vainilla con lluvia de colores", vainilla.Label,
		"sin catálogo que copiar, la etiqueta es lo que dijo el cliente")
	require.Empty(t, vainilla.VariantOptions, "un producto que no está no tiene variantes que ofrecer")

	// (4) TEQUEÑOS — match EXACTO a $490.
	teq := art.Lines[3]
	require.Equal(t, stages.KindMatched, teq.Kind)
	require.Equal(t, "TEQ-30", teq.SKU)
	require.Equal(t, 490.0, precio(t, teq))
	require.Equal(t, "exact", teq.Match.Strategy)
	require.Equal(t, 1.0, teq.Match.Confidence)
	require.Equal(t, "package", teq.UnitKind, "que el cliente pidiera un PAQUETE no se pierde al tomar el nombre del catálogo")
	require.Equal(t, 30, teq.PackageSize)

	// (5) ENVÍO — siempre.
	envio := art.Lines[4]
	require.Equal(t, stages.KindShipping, envio.Kind)
	require.Equal(t, intakes.ShippingSKU, envio.SKU)
	require.Nil(t, envio.UnitPrice, "sin zonas configuradas el envío lo precifica el dueño")
	require.Equal(t, "por confirmar zona", envio.Note)

	// EL CONTADOR DEL ESCALÓN CARO: exactamente uno por ítem no cubierto, y por LOS
	// ítems, no por otra cosa. Afirmar solo «≤ 2» dejaría pasar un cero.
	require.Equal(t, 2, art.GrayZoneCalls)
	require.Equal(t, []string{"torta de chocolate", "torta de vainilla con lluvia de colores"}, gz.pedidos,
		"se pregunta por los DOS ítems que los escalones deterministas no cubrieron, y por ninguno más")
	require.Equal(t, []string{"Torta chocolate húmedo + crema choc."}, gz.candidatos[0],
		"al modelo se le ofrecen los candidatos que COMPARTEN TOKENS, no el catálogo entero")

	// El total es una VISTA de las líneas y se compara contra un literal.
	total, pendientes := art.TotalParcial()
	require.Equal(t, 1290.0, total, "800 de la decoración + 490 de los tequeños; la customization no suma")
	require.Equal(t, 3, pendientes, "torta de chocolate, torta de vainilla y envío")

	// Y todo eso quedó persistido bajo `artifacts.match`.
	require.Len(t, store.guardados, 1)
	require.Equal(t, intake.StageMatch, store.guardados[0].Stage)
	var releido stages.ArtefactoMatch
	require.NoError(t, json.Unmarshal(store.guardados[0].Payload, &releido))
	require.Equal(t, art.Lines, releido.Lines, "lo que se devuelve y lo que se persiste son lo mismo")
}

// ---------------------------------------------------------------------------
// CRITERIO (a) — «HAMBURGUESA SIN SAL» CON UN CATÁLOGO QUE VENDE SAL Y SALSA
// ---------------------------------------------------------------------------

// TestMatch_CriterioA_SinSalNoLlegaAlMatcher.
//
// La trampa está en el catálogo: hay un artículo «Sal» ($50) y otro «Salsa» ($60).
// Si «sin sal» entrara a la cascada, el escalón exacto casaría «sal» con «Sal» y el
// presupuesto cobraría sal que el cliente pidió NO tener.
//
// 🔴 EL SEGUNDO SUBTEST NO ES UN DUPLICADO: en el primero el producto casa por
// clave (O(1)) y el comparador NO LLEGA A EJECUTARSE, así que «el espía no vio "sin
// sal"» sería cierto por vacío. El segundo escribe el producto con una errata para
// que el bucle SÍ corra, y entonces la afirmación mide algo.
func TestMatch_CriterioA_SinSalNoLlegaAlMatcher(t *testing.T) {
	casos := []struct {
		nombre            string
		producto          string
		bucleEjecutado    bool
		estrategia        string
		confianzaEsperada float64
	}{
		{"escrito igual: casa por clave y el bucle ni corre", "hamburguesa", false, "exact", 1.0},
		{"con errata: el bucle SÍ corre y el fuzzy la rescata", "hamburgueza", true, "fuzzy", 1 - 1.0/11.0},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			espia := &comparadorEspia{real: stages.CascadaPorDefecto()}
			gz := &zonaGrisFalsa{}
			m, _ := matchDe(t, stages.ConComparador(espia), stages.ConZonaGris(gz))

			art, err := m.Run(context.Background(), jobDeAmbar(), stages.EntradaMatch{
				Cantidades: p4De(llm.NormalizedItem{
					Product:        c.producto,
					Qty:            1,
					Customizations: []string{"sin sal"},
					Evidence:       "una hamburguesa sin sal",
				}),
				Indice: indiceDe(t, catalogoAmbar()),
			})
			require.NoError(t, err)

			// UNA sola línea de producto, más el envío.
			require.Len(t, art.Lines, 2)
			hamb := art.Lines[0]
			require.Equal(t, "HAMB", hamb.SKU)
			require.Equal(t, 3000.0, precio(t, hamb))
			require.Equal(t, "sin sal", hamb.Customization)
			require.Equal(t, c.estrategia, hamb.Match.Strategy)
			require.InDelta(t, c.confianzaEsperada, hamb.Match.Confidence, 0.0001)

			// Ni «Sal» ni «Salsa» entraron en el presupuesto.
			require.Nil(t, lineaConSKU(art, "SAL"), "«sin sal» NO puede convertirse en una línea de Sal")
			require.Nil(t, lineaConSKU(art, "SALSA"))

			// CERO llamadas del matcher por la personalización, en los dos escalones.
			require.Equal(t, c.bucleEjecutado, espia.llamadas > 0,
				"el subtest depende de si el bucle corre o no; si esto falla, la aserción de abajo no mide nada")
			require.False(t, espia.vioAlgunoQueContenga("sin sal"),
				"la personalización NUNCA puede llegar al comparador; llegaron: %v", espia.esperados)
			require.Zero(t, art.GrayZoneCalls, "y tampoco se gasta una llamada al modelo por ella")
			require.Empty(t, gz.pedidos)
		})
	}
}

// TestMatch_UnaPersonalizacionQueEXISTEEnElCatalogoTampocoSeCobra es la prueba
// LIMPIA de la restricción «las personalizaciones se separan ANTES del matcher».
//
// 🔴 POR QUÉ NO BASTA CON EL TEST DE «SIN SAL». Porque «sin sal» lo para ADEMÁS la
// guarda de negación de los añadidos, así que ese test sigue verde aunque la
// separación se rompa: es el caso de la defensa duplicada tapando al test de
// conducta. Aquí la personalización es «salsa aparte» —una instrucción de
// preparación de las de verdad, sin negación— y el catálogo vende «Salsa» a $60. Si
// las personalizaciones llegaran al matcher, el n-grama «salsa» casaría y el cliente
// pagaría 60 pesos por pedir la salsa aparte.
func TestMatch_UnaPersonalizacionQueEXISTEEnElCatalogoTampocoSeCobra(t *testing.T) {
	espia := &comparadorEspia{real: stages.CascadaPorDefecto()}
	m, _ := matchDe(t, stages.ConComparador(espia))

	art, err := m.Run(context.Background(), jobDeAmbar(), stages.EntradaMatch{
		Cantidades: p4De(llm.NormalizedItem{
			Product:        "hamburguesa",
			Qty:            1,
			Customizations: []string{"salsa aparte"},
			Evidence:       "una hamburguesa con la salsa aparte",
		}),
		Indice: indiceDe(t, catalogoAmbar()),
	})
	require.NoError(t, err)

	require.Len(t, art.Lines, 2, "hamburguesa y envío: la personalización NO puede volverse una línea")
	require.Nil(t, lineaConSKU(art, "SALSA"), "🔴 «salsa aparte» es cómo se prepara, no algo que se cobre")
	require.Equal(t, "salsa aparte", art.Lines[0].Customization)
	require.False(t, espia.vioAlgunoQueContenga("salsa aparte"),
		"la personalización no llega al comparador; llegaron: %v", espia.esperados)

	total, _ := art.TotalParcial()
	require.Equal(t, 3000.0, total, "y el total es el de la hamburguesa sola")
}

// ---------------------------------------------------------------------------
// CRITERIOS (b) y (c) — EL MISMO TEXTO CON Y SIN EL ARTÍCULO EN EL CATÁLOGO
// ---------------------------------------------------------------------------

// TestMatch_CriterioB_AnadidoConArticuloEsLineaConPrecio: «hamburguesa con extra de
// queso» y un catálogo que vende «Queso» ⇒ DOS líneas, la segunda con su precio, y
// el total sube.
func TestMatch_CriterioB_AnadidoConArticuloEsLineaConPrecio(t *testing.T) {
	m, _ := matchDe(t)

	art, err := m.Run(context.Background(), jobDeAmbar(), stages.EntradaMatch{
		Cantidades: p4De(llm.NormalizedItem{
			Product:         "hamburguesa",
			Qty:             1,
			AddonCandidates: []string{"extra de queso"},
			Evidence:        "una hamburguesa con extra de queso",
		}),
		Indice: indiceDe(t, catalogoAmbar()),
	})
	require.NoError(t, err)

	require.Len(t, art.Lines, 3, "hamburguesa, queso y envío")
	require.Equal(t, "HAMB", art.Lines[0].SKU)
	require.Empty(t, art.Lines[0].Customization, "el añadido que ES artículo NO se queda además como indicación")

	queso := art.Lines[1]
	require.Equal(t, stages.KindMatched, queso.Kind)
	require.Equal(t, "QUESO", queso.SKU)
	require.Equal(t, "Queso", queso.Label)
	require.Equal(t, 150.0, precio(t, queso))
	require.Equal(t, "ngrama", queso.Match.Strategy, "casó el sustantivo del añadido, no la frase entera")

	total, pendientes := art.TotalParcial()
	require.Equal(t, 3150.0, total, "3000 de la hamburguesa + 150 del queso")
	require.Equal(t, 1, pendientes, "solo el envío queda sin precio")
}

// TestMatch_CriterioC_AnadidoSinArticuloEsIndicacionYNoMueveElTotal: el MISMO texto
// contra un catálogo SIN «Queso» ⇒ UNA línea, con la indicación pegada, y el total
// EXACTAMENTE el mismo que si el cliente no hubiera dicho la frase.
//
// 🔴 El «mismo total» se mide contra la corrida SIN la frase, no contra un número
// copiado: es la comparación la que demuestra que la personalización no toca el
// dinero (D-044.14).
func TestMatch_CriterioC_AnadidoSinArticuloEsIndicacionYNoMueveElTotal(t *testing.T) {
	cat := sinArticulo(catalogoAmbar(), "QUESO")
	base := llm.NormalizedItem{Product: "hamburguesa", Qty: 1, Evidence: "una hamburguesa"}
	conFrase := base
	conFrase.AddonCandidates = []string{"más queso"}

	m, _ := matchDe(t)
	sinLaFrase, err := m.Run(context.Background(), jobDeAmbar(), stages.EntradaMatch{
		Cantidades: p4De(base), Indice: indiceDe(t, cat),
	})
	require.NoError(t, err)

	art, err := m.Run(context.Background(), jobDeAmbar(), stages.EntradaMatch{
		Cantidades: p4De(conFrase), Indice: indiceDe(t, cat),
	})
	require.NoError(t, err)

	require.Len(t, art.Lines, 2, "hamburguesa y envío: «más queso» NO inventa un renglón")
	require.Equal(t, "más queso", art.Lines[0].Customization, "y llega con el acento que escribió el cliente")
	require.Nil(t, lineaConSKU(art, "QUESO"))

	conTotal, conPend := art.TotalParcial()
	sinTotal, sinPend := sinLaFrase.TotalParcial()
	require.Equal(t, sinTotal, conTotal, "la customization no entra en ningún total")
	require.Equal(t, sinPend, conPend)
	require.Equal(t, 3000.0, conTotal, "y el número es el del catálogo, no el que salga de sumar")
}

// ---------------------------------------------------------------------------
// CRITERIO (d) — LO QUE ES DEL PEDIDO ENTERO NO SE REPARTE POR LAS LÍNEAS
// ---------------------------------------------------------------------------

// TestMatch_CriterioD_LaNotaDelPedidoNoSeRepartePorLasLineas: con la nota entrando
// por su ranura, va a `customer_note`, NO crea líneas y NO ensucia ninguna
// `customization`.
func TestMatch_CriterioD_LaNotaDelPedidoNoSeRepartePorLasLineas(t *testing.T) {
	m, _ := matchDe(t)

	art, err := m.Run(context.Background(), jobDeAmbar(), stages.EntradaMatch{
		Cantidades: p4De(llm.NormalizedItem{Product: "hamburguesa", Qty: 1, Evidence: "una hamburguesa"}),
		Indice:     indiceDe(t, catalogoAmbar()),
		Nota:       "dejarlo\ten   portería",
	})
	require.NoError(t, err)

	require.Equal(t, "dejarlo en portería", art.CustomerNote,
		"la nota pasa por cart.SanitizeNote: el tabulador se vuelve espacio y los repetidos se colapsan")
	require.Len(t, art.Lines, 2, "hamburguesa y envío: la nota NO crea líneas")
	for _, l := range art.Lines {
		require.Emptyf(t, l.Customization, "la nota del pedido no se reparte: la línea %q no puede llevarla", l.Label)
	}
}

// TestMatch_CriterioD_HoyNadieProduceLaNotaYAsiSEVE fija el HUECO con literales, en
// vez de dejar que el criterio (d) parezca cerrado de punta a punta.
//
// 🔴 LO QUE ESTE TEST AFIRMA ES LO QUE HOY PASA, NO LO QUE DEBERÍA PASAR. Ninguna
// etapa anterior emite una nota de pedido (ver stages.NotaDePedido), así que
// «dejarlo en portería» llega como UN ÍTEM MÁS de P4 y sale como línea `unmatched`.
// Si algún día P2 aprende a separarla, este test se pondrá rojo — y eso será la
// señal de que el hueco se cerró, no una regresión.
func TestMatch_CriterioD_HoyNadieProduceLaNotaYAsiSEVE(t *testing.T) {
	m, _ := matchDe(t)

	art, err := m.Run(context.Background(), jobDeAmbar(), stages.EntradaMatch{
		Cantidades: p4De(
			llm.NormalizedItem{Product: "hamburguesa", Qty: 1, Evidence: "una hamburguesa"},
			llm.NormalizedItem{Product: "dejarlo en portería", Qty: 1, Evidence: "y dejarlo en portería"},
		),
		Indice: indiceDe(t, catalogoAmbar()),
		Nota:   stages.SinNotaDePedido,
	})
	require.NoError(t, err)

	require.Empty(t, art.CustomerNote, "hoy la nota del pedido NO llega por su ranura: nadie la produce")
	require.Len(t, art.Lines, 3)
	require.Equal(t, stages.KindUnmatched, art.Lines[1].Kind)
	require.Equal(t, "dejarlo en portería", art.Lines[1].Label,
		"hoy la instrucción del pedido acaba como un renglón sin precio en la bandeja")
}

// ---------------------------------------------------------------------------
// DEUDA-044.16 — UN ÍTEM MALO NO TIRA EL BORRADOR
// ---------------------------------------------------------------------------

// TestMatch_Deuda04416_UnItemMaloNoSeLlevaALosDemas.
//
// La decisión está escrita en la cabecera de match.go: se DEGRADA el ítem y el resto
// vive. Este test la fija con los dos ítems degenerados que hoy son alcanzables —el
// que no trae producto y el que trae una cantidad imposible— y comprobando que los
// ítems buenos salen INTACTOS, con su sku y su precio, no solo que «hay tres líneas».
//
// El de la cantidad es alcanzable de verdad: la reanudación del worker decodifica el
// artefacto persistido con `json.Unmarshal` a secas —sin el validador de calidad de
// `llm.ParseQuantities`—, así que un `qty` inválido escrito por una versión anterior
// llega hasta aquí.
func TestMatch_Deuda04416_UnItemMaloNoSeLlevaALosDemas(t *testing.T) {
	m, _ := matchDe(t)

	art, err := m.Run(context.Background(), jobDeAmbar(), stages.EntradaMatch{
		Cantidades: p4De(
			llm.NormalizedItem{Product: "tequeños congelados", Qty: 2, Evidence: evidenciaTequenos},
			llm.NormalizedItem{}, // ni producto ni evidencia: no hay nada que enseñar
			llm.NormalizedItem{Product: "hamburguesa", Qty: 0, Evidence: "una hamburguesa"},
			llm.NormalizedItem{Product: "queso", Qty: 3, Evidence: "tres quesos"},
		),
		Indice: indiceDe(t, catalogoAmbar()),
	})
	require.NoError(t, err, "🔴 un ítem malo NO puede tirar el borrador entero")

	// Los ítems buenos, intactos.
	teq := lineaConSKU(art, "TEQ-30")
	require.NotNil(t, teq)
	require.Equal(t, 490.0, precio(t, *teq))
	require.Equal(t, 2, teq.Qty)
	queso := lineaConSKU(art, "QUESO")
	require.NotNil(t, queso)
	require.Equal(t, 150.0, precio(t, *queso))
	require.Equal(t, 3, queso.Qty)

	// El ítem sin producto NO genera línea; el de cantidad inválida SÍ, con la
	// cantidad tal como vino.
	require.Len(t, art.Lines, 4, "tequeños, hamburguesa, queso y envío: el ítem vacío no deja renglón")
	hamb := lineaConSKU(art, "HAMB")
	require.NotNil(t, hamb)
	require.Equal(t, 0, hamb.Qty, "la cantidad no se maquilla a 1: se enseña como vino y se avisa")

	require.Equal(t, []stages.Aviso{
		{ItemPos: 1, Reason: stages.MotivoSinProducto},
		{ItemPos: 2, Reason: stages.MotivoCantidadInvalida},
	}, art.Warnings, "los avisos dicen QUÉ ítem y POR QUÉ, no «hubo un problema»")

	total, _ := art.TotalParcial()
	require.Equal(t, 1430.0, total, "490×2 + 150×3 + 3000×0: la hamburguesa de cantidad 0 no suma")
}

// TestMatch_ItemSinProductoPeroConEvidencia_SigueSiendoUnRenglon: si al menos hay
// evidencia, el cliente pidió ALGO y el dueño tiene que verlo.
func TestMatch_ItemSinProductoPeroConEvidencia_SigueSiendoUnRenglon(t *testing.T) {
	m, _ := matchDe(t)

	art, err := m.Run(context.Background(), jobDeAmbar(), stages.EntradaMatch{
		Cantidades: p4De(llm.NormalizedItem{Qty: 1, Evidence: "y algo dulce para el final"}),
		Indice:     indiceDe(t, catalogoAmbar()),
	})
	require.NoError(t, err)

	require.Len(t, art.Lines, 2)
	require.Equal(t, stages.KindUnmatched, art.Lines[0].Kind)
	require.Equal(t, "y algo dulce para el final", art.Lines[0].Label)
	require.Equal(t, []stages.Aviso{{ItemPos: 0, Reason: stages.MotivoSinProducto}}, art.Warnings)
}

// TestMatch_LaZonaGrisCaida_DegradaElItemYNoElJob: el escalón caro es el TERCERO de
// una cascada cuyos dos primeros ya corrieron, así que su caída no tumba el job.
func TestMatch_LaZonaGrisCaida_DegradaElItemYNoElJob(t *testing.T) {
	gz := &zonaGrisFalsa{err: errZonaGris}
	m, _ := matchDe(t, stages.ConZonaGris(gz))

	art, err := m.Run(context.Background(), jobDeAmbar(), stages.EntradaMatch{
		Cantidades: p4De(
			llm.NormalizedItem{Product: "torta de chocolate", Qty: 1, Evidence: evidenciaTortaChocolate},
			llm.NormalizedItem{Product: "tequeños congelados", Qty: 1, Evidence: evidenciaTequenos},
		),
		Indice: indiceDe(t, catalogoAmbar()),
	})
	require.NoError(t, err, "que el modelo no conteste NO es un fallo del pipeline")

	require.Equal(t, stages.KindUnmatched, art.Lines[0].Kind, "el ítem que no se pudo rescatar cae a unmatched")
	teq := lineaConSKU(art, "TEQ-30")
	require.NotNil(t, teq, "el ítem que YA había resuelto el escalón determinista conserva su precio")
	require.Equal(t, 490.0, precio(t, *teq))
	require.Equal(t, []stages.Aviso{{ItemPos: 0, Reason: stages.MotivoZonaGrisCaida}}, art.Warnings)
}

// ---------------------------------------------------------------------------
// LA LÍNEA DE ENVÍO — SIEMPRE, Y CON EL PRECIO QUE DICTE LA CONFIGURACIÓN
// ---------------------------------------------------------------------------

// TestMatch_LaLineaDeEnvioVaSIEMPRE recorre las tres configuraciones posibles de
// zonas. El precio NO lo decide esta etapa: lo decide `intakes.DesiredShippingLine`,
// la misma función que gobierna el cierre del carrito numérico.
func TestMatch_LaLineaDeEnvioVaSIEMPRE(t *testing.T) {
	casos := []struct {
		nombre   string
		zonas    []intakes.ShippingZone
		etiqueta string
		precio   *float64
		nota     string
	}{
		{"sin zonas: por confirmar", nil, "Envío por confirmar", nil, "por confirmar zona"},
		{"una zona: su tarifa, cobrada", []intakes.ShippingZone{{Code: "z1", Label: "Providencia", Price: 3000}},
			"Envío — Providencia", ptr(3000.0), ""},
		{"dos zonas: wApp NO elige", []intakes.ShippingZone{
			{Code: "z1", Label: "Providencia", Price: 3000}, {Code: "z2", Label: "Puente Alto", Price: 5000},
		}, "Envío por confirmar", nil, "por confirmar zona"},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			m, _ := matchDe(t)
			art, err := m.Run(context.Background(), jobDeAmbar(), stages.EntradaMatch{
				Cantidades: p4De(), Indice: indiceDe(t, catalogoAmbar()), Zonas: c.zonas,
			})
			require.NoError(t, err)

			require.Len(t, art.Lines, 1, "un pedido sin un solo ítem sigue llevando su línea de envío")
			envio := art.Lines[0]
			require.Equal(t, stages.KindShipping, envio.Kind)
			require.Equal(t, c.etiqueta, envio.Label)
			require.Equal(t, c.precio, envio.UnitPrice)
			require.Equal(t, c.nota, envio.Note)
		})
	}
}

func ptr(f float64) *float64 { return &f }

// ---------------------------------------------------------------------------
// LA INDICACIÓN QUE NO CABE — NI SE TRUNCA NI SE PIERDE EN SILENCIO
// ---------------------------------------------------------------------------

// TestMatch_UnaIndicacionQueNoCabe_NiSeTruncaNiTiraLaLinea: REQ-33e dice que
// pasarse NO trunca, porque el final es donde va el alérgeno. Aquí no hay a quién
// repreguntar, así que la línea sobrevive sin indicación y con su aviso.
func TestMatch_UnaIndicacionQueNoCabe_NiSeTruncaNiTiraLaLinea(t *testing.T) {
	larga := ""
	for range 30 {
		larga += "sin lactosa y sin maní, "
	}
	require.Greater(t, len([]rune(larga)), 280, "el fixture tiene que pasarse de MaxNoteRunes de verdad")

	m, _ := matchDe(t)
	art, err := m.Run(context.Background(), jobDeAmbar(), stages.EntradaMatch{
		Cantidades: p4De(llm.NormalizedItem{
			Product: "hamburguesa", Qty: 1, Customizations: []string{larga}, Evidence: "una hamburguesa",
		}),
		Indice: indiceDe(t, catalogoAmbar()),
	})
	require.NoError(t, err)

	require.Len(t, art.Lines, 2, "la línea del producto NO se pierde por una indicación larga")
	require.Equal(t, 3000.0, precio(t, art.Lines[0]))
	require.Empty(t, art.Lines[0].Customization, "y no se trunca: o cabe entera o no va")
	require.Equal(t, []stages.Aviso{{ItemPos: 0, Reason: stages.MotivoIndicacionLarga}}, art.Warnings)
}

// TestMatch_VariasIndicacionesSeUnenEnLaUnicaRanura: la línea tiene UNA ranura y las
// indicaciones pueden ser varias, incluidos los añadidos que no encontraron artículo.
func TestMatch_VariasIndicacionesSeUnenEnLaUnicaRanura(t *testing.T) {
	m, _ := matchDe(t)
	art, err := m.Run(context.Background(), jobDeAmbar(), stages.EntradaMatch{
		Cantidades: p4De(llm.NormalizedItem{
			Product:         "hamburguesa",
			Qty:             1,
			Customizations:  []string{"sin cebolla", "bien cocida"},
			AddonCandidates: []string{"pan sin gluten"},
			Evidence:        "una hamburguesa",
		}),
		Indice: indiceDe(t, catalogoAmbar()),
	})
	require.NoError(t, err)

	require.Len(t, art.Lines, 2)
	require.Equal(t, "sin cebolla, bien cocida, pan sin gluten", art.Lines[0].Customization,
		"las personalizaciones primero y detrás los añadidos que no eran artículo, en su orden")
}

// TestMatch_UnAnadidoNegadoNuncaSeCobra: P3 tiene instrucciones de mandar a
// `addon_candidates` todo lo que le genere duda, así que una negación puede llegar
// por esa ranura. «Sin X» no puede ser un añadido facturable de X.
func TestMatch_UnAnadidoNegadoNuncaSeCobra(t *testing.T) {
	m, _ := matchDe(t)
	art, err := m.Run(context.Background(), jobDeAmbar(), stages.EntradaMatch{
		Cantidades: p4De(llm.NormalizedItem{
			Product:         "hamburguesa",
			Qty:             1,
			AddonCandidates: []string{"Sin salsa"},
			Evidence:        "una hamburguesa sin salsa",
		}),
		Indice: indiceDe(t, catalogoAmbar()),
	})
	require.NoError(t, err)

	require.Len(t, art.Lines, 2, "«sin salsa» no puede volverse la línea de «Salsa» ($60)")
	require.Nil(t, lineaConSKU(art, "SALSA"))
	require.Equal(t, "Sin salsa", art.Lines[0].Customization)
	total, _ := art.TotalParcial()
	require.Equal(t, 3000.0, total)
}

// ---------------------------------------------------------------------------
// LA VARIANTE — CUÁNDO SE RESUELVE Y CUÁNDO LA ELIGE EL DUEÑO
// ---------------------------------------------------------------------------

// TestMatch_LaVariante_SoloSeResuelveCuandoSOLOUNASIRVE cubre las cuatro situaciones
// de conArticulo con literales.
func TestMatch_LaVariante_SoloSeResuelveCuandoSOLOUNASIRVE(t *testing.T) {
	casos := []struct {
		nombre   string
		rango    *llm.Range
		sku      string
		precio   *float64
		opciones int
		avisos   []stages.Aviso
	}{
		{"el rango señala UNA sola variante: se resuelve", &llm.Range{Min: 11, Max: 12, Unit: "porciones"},
			"TORTA-CHOC#12", ptr(2400.0), 0, nil},
		{"el rango cruza DOS: elige el dueño", &llm.Range{Min: 10, Max: 12, Unit: "porciones"},
			"TORTA-CHOC", nil, 2, nil},
		{"sin rango: no hay con qué decidir, se ofrecen todas", nil,
			"TORTA-CHOC", nil, 3, nil},
		{"el rango pedido no existe en el catálogo: todas y AVISO", &llm.Range{Min: 40, Max: 50, Unit: "porciones"},
			"TORTA-CHOC", nil, 3, []stages.Aviso{{ItemPos: 0, Reason: stages.MotivoRangoSinVariante}}},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			gz := &zonaGrisFalsa{respuestas: map[string]int{"torta de chocolate": 0}}
			m, _ := matchDe(t, stages.ConZonaGris(gz))
			art, err := m.Run(context.Background(), jobDeAmbar(), stages.EntradaMatch{
				Cantidades: p4De(llm.NormalizedItem{
					Product: "torta de chocolate", Qty: 1, Range: c.rango, Evidence: evidenciaTortaChocolate,
				}),
				Indice: indiceDe(t, catalogoAmbar()),
			})
			require.NoError(t, err)

			linea := art.Lines[0]
			require.Equal(t, stages.KindMatched, linea.Kind,
				"el PRODUCTO está en el catálogo aunque el tamaño no se pueda decidir")
			require.Equal(t, c.sku, linea.SKU)
			require.Equal(t, c.precio, linea.UnitPrice)
			require.Len(t, linea.VariantOptions, c.opciones)
			require.Equal(t, c.avisos, art.Warnings)
		})
	}
}

// TestMatch_ElSKUDeVarianteEsElMISMOQueElDelCart ata la duplicación de
// `variantSKUSuffix` y `variantLabelSep`, que en `cart` no están exportados.
//
// 🔴 Sin este test, el día que el cart cambiara el separador habría DOS convenciones
// de sku de variante conviviendo —la del pedido numérico y la del presupuesto— y el
// síntoma sería un sku que no resuelve contra la lista de precios, meses después.
func TestMatch_ElSKUDeVarianteEsElMISMOQueElDelCart(t *testing.T) {
	cat := catalogoAmbar()
	gz := &zonaGrisFalsa{respuestas: map[string]int{"torta de chocolate": 0}}
	m, _ := matchDe(t, stages.ConZonaGris(gz))

	art, err := m.Run(context.Background(), jobDeAmbar(), stages.EntradaMatch{
		Cantidades: p4De(llm.NormalizedItem{
			Product: "torta de chocolate", Qty: 1,
			Range:    &llm.Range{Min: 11, Max: 12, Unit: "porciones"},
			Evidence: evidenciaTortaChocolate,
		}),
		Indice: indiceDe(t, cat),
	})
	require.NoError(t, err)

	// La lista de precios la publica el CART con sus propias constantes.
	lista := cart.PriceListOf(cat)
	entrada, ok := lista.Lookup(art.Lines[0].SKU)
	require.Truef(t, ok, "el sku %q que construye el match no existe en la lista de precios del cart", art.Lines[0].SKU)
	require.Equal(t, entrada.Label, art.Lines[0].Label, "y la etiqueta compuesta también tiene que coincidir")
	require.Equal(t, entrada.Price, precio(t, art.Lines[0]))
}

// ---------------------------------------------------------------------------
// EL CABLEADO Y LOS BORDES
// ---------------------------------------------------------------------------

// TestMatch_NoNaceAMedioCablear.
func TestMatch_NoNaceAMedioCablear(t *testing.T) {
	_, err := stages.NewMatch(nil, &storeFake{})
	require.ErrorIs(t, err, stages.ErrMatchSinCablear)
	_, err = stages.NewMatch(logger.New(), nil)
	require.ErrorIs(t, err, stages.ErrMatchSinCablear)
}

// TestMatch_SinCatalogoNoSeInventaUnBorrador: sin índice NINGÚN ítem puede casar, y
// un borrador con todo `unmatched` mentiría sobre el catálogo del tenant. Es de
// infraestructura y se reintenta.
func TestMatch_SinCatalogoNoSeInventaUnBorrador(t *testing.T) {
	m, store := matchDe(t)
	_, err := m.Run(context.Background(), jobDeAmbar(), stages.EntradaMatch{Cantidades: p4De()})
	require.ErrorIs(t, err, stages.ErrSinCatalogo)
	require.Empty(t, store.guardados, "y no se persiste nada a medias")

	_, err = m.Run(context.Background(), jobDeAmbar(), stages.EntradaMatch{Indice: indiceDe(t, catalogoAmbar())})
	require.ErrorIs(t, err, stages.ErrSinCantidades)
}

// TestMatch_ElJobQueSaleDeProcessingNoPersiste: mismo contrato que las etapas LLM.
func TestMatch_ElJobQueSaleDeProcessingNoPersiste(t *testing.T) {
	store := &storeFake{perdido: true}
	m, err := stages.NewMatch(logger.New(), store)
	require.NoError(t, err)

	_, err = m.Run(context.Background(), jobDeAmbar(), stages.EntradaMatch{
		Cantidades: p4De(), Indice: indiceDe(t, catalogoAmbar()),
	})
	require.ErrorIs(t, err, stages.ErrJobFueraDeProcessing)
}

// TestMatch_ElArtefactoPasaLaPuertaDeLaMaquina: `intake.Artifact.Validate` exige
// etapa del vocabulario, objeto JSON y `version >= 1`. El doble valida con la MISMA
// puerta que Postgres.
func TestMatch_ElArtefactoPasaLaPuertaDeLaMaquina(t *testing.T) {
	m, store := matchDe(t)
	_, err := m.Run(context.Background(), jobDeAmbar(), stages.EntradaMatch{
		Cantidades: p4De(), Indice: indiceDe(t, catalogoAmbar()),
	})
	require.NoError(t, err)
	require.Len(t, store.guardados, 1)
	require.NoError(t, store.guardados[0].Validate())
	require.Equal(t, intake.StageMatch, store.guardados[0].Stage)

	var crudo map[string]any
	require.NoError(t, json.Unmarshal(store.guardados[0].Payload, &crudo))
	require.Equal(t, float64(llm.ArtifactVersion), crudo["version"])
	// `unit_price: null` viaja EXPLÍCITO: un campo ausente no dice lo mismo que un
	// precio vacío, y design §7.4 lo escribe como null.
	lineas, ok := crudo["lines"].([]any)
	require.True(t, ok, "el artefacto tiene que traer `lines` como lista JSON")
	envio, ok := lineas[len(lineas)-1].(map[string]any)
	require.True(t, ok)
	precioEnvio, presente := envio["unit_price"]
	require.True(t, presente, "la clave unit_price tiene que estar aunque el precio esté vacío")
	require.Nil(t, precioEnvio)
}
