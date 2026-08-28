package quotetext_test

// herminia_fixture_test.go — LAS DOS COTIZACIONES DE HERMINIA que T5.1 exige como
// few-shot.
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 QUÉ SON ESTOS DOS TEXTOS EXACTAMENTE, PORQUE IMPORTA
// ════════════════════════════════════════════════════════════════════════════
//
// El criterio de T5.1 pide «un fixture de 2 cotizaciones REALES de Herminia». Busqué
// la transcripción y NO EXISTE en el repositorio ni en la carpeta del plan. Lo único
// que hay es la fila de las 17:23–17:24 de la tabla del caso Fusión
// (`docs/plans/044-…/design.md` §1), que las describe ABREVIADAS y con puntos
// suspensivos:
//
//	«Cotización 1: "Pastel para 15 personas, chocolate húmedo, relleno chocolate y
//	 oreo… 2100. Incluye impresiones no comestibles" · Cotización 2: "25-30 personas,
//	 vainilla, ddl, merengue… 2950 pesos"»
//
// Lo de abajo es esa descripción REHIDRATADA a un mensaje de WhatsApp verosímil. NO
// es una transcripción y no debe citarse como tal. Lo que sí conserva —y es lo único
// que el few-shot necesita— es la ESTRUCTURA que el design fija como formato objetivo
// (§1 punto 8): producto + tamaño + specs + precio + qué incluye, en el orden y con el
// desaliño con el que lo escribe una persona por WhatsApp.
//
// Lo que este fixture NO puede demostrar, y conviene decirlo aquí y no en la
// bitácora: que un modelo real imite bien ESTA voz. Eso se mide en campo con la voz
// de verdad, y para eso hace falta la transcripción que no tenemos.
// ════════════════════════════════════════════════════════════════════════════

const (
	// herminia1 es la cotización de la torta de chocolate (design §1, 17:23).
	herminia1 = "Pastel para 15 personas, chocolate húmedo, relleno chocolate y oreo, " +
		"decoración infantil segun las fotos que me mandaste. 2100. " +
		"Incluye impresiones no comestibles"
	// herminia2 es la de la torta de vainilla (design §1, 17:24).
	herminia2 = "El otro para 25-30 personas, vainilla, ddl y merengue, " +
		"con la lluvia de colores. 2950 pesos"
)

// herminias son las dos, en el orden en que ella las mandó.
func herminias() []string { return []string{herminia1, herminia2} }
