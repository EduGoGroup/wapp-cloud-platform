package anclaje_test

import (
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/anclaje"
)

// ════════════════════════════════════════════════════════════════════════════
// EL FIXTURE (Plan 044 · Ola 3 · T3.3)
//
// 🔴 CALIDAD C: ESTE TEXTO LO REDACTÓ CLAUDE. NO ES EL TEXTO REAL DE AMBAR, y no
// existe transcrito en ninguna parte del repositorio de documentación — las fuentes
// (`brainstorm/…/08-caso-fusion-anatomia-del-flujo-real.md`) solo DESCRIBEN la
// solicitud en tercera persona. Lo de abajo está reconstruido a partir de esa
// descripción, de las evidencias de `design.md` §7.1 y del fixture hermano de la Ola
// 2 (`internal/intake/stages/ambar_fixture_test.go`), que lleva el mismo aviso.
//
// Consecuencia, dicha para que nadie lea dentro de dos meses «probado con el caso
// real»: esto es un DETECTOR DE REGRESIÓN y NO ACREDITA ACIERTO. Ninguna medida de
// calidad del reparto puede salir de aquí.
//
// # POR QUÉ AQUÍ EL HILO VA MENSAJE A MENSAJE Y NO COMPUESTO
//
// El fixture de la Ola 2 es el literal YA COMPUESTO —un solo string con sus rótulos
// y sus prefijos `cliente:`— porque eso es lo que recibe P2. Este paquete recibe otra
// cosa: los mensajes SUELTOS, cada uno con su orden y su instante, porque sin saber
// QUÉ mensaje trajo cada foto no hay proximidad que valga. Los DOS fixtures salen del
// mismo caso y no se contradicen: componer estos turnos con `runtime.ComposeSourceText`
// daría aquel texto.
//
// # LOS INSTANTES SON DEL RELOJ DEL CLIENTE Y ESTÁN FIJADOS
//
// Ni un `time.Now()` en este fichero. Las horas salen de la solicitud real de las
// 9:55 (doc 08) y son literales: un test de proximidad escrito contra el reloj de la
// máquina es un flake esperando a que la corrida caiga en el minuto equivocado.
// ════════════════════════════════════════════════════════════════════════════

// t9 construye un instante del 13/07/2026, el día de la solicitud de Ambar.
func t9(h, m, s int) time.Time {
	return time.Date(2026, 7, 13, h, m, s, 0, time.UTC)
}

// Las cuatro líneas del borrador de Ambar (design §7.4). La de envío va con evidencia
// VACÍA a propósito: no sale de ninguna frase del cliente, y tiene que ser incapaz de
// atraer una foto.
var lineasAmbar = []anclaje.Linea{
	{Idx: 0, Evidencia: "una torta sería con decoración infantil, de bizcocho húmedo de chocolate",
		Etiqueta: "Torta chocolate húmedo + crema choc."},
	{Idx: 1, Evidencia: "otra de bizcocho de vainilla que tenga lluvia de colores",
		Etiqueta: "Torta vainilla, lluvia de colores, dulce de leche y merengue"},
	{Idx: 2, Evidencia: "un paquete de tequeños congelados de 30",
		Etiqueta: "Tequeños congelados paquete x30"},
	{Idx: 3, Evidencia: "", Etiqueta: "Envío"},
}

// La conversación. Los turnos 3, 4, 6 y 8 son los adjuntos: llegan SIN texto, que es
// como llega una foto suelta por WhatsApp.
var turnosAmbar = []anclaje.Turno{
	{Seq: 1, En: t9(9, 55, 0), Texto: "Hola, buenas! Te quería pedir un presupuesto para el miércoles de la semana que viene"},
	{Seq: 2, En: t9(9, 55, 30), Texto: "Serían 2 tortas. Una torta sería con decoración infantil, de bizcocho húmedo de chocolate con crema de chocolate, de 10 o 12 porciones"},
	{Seq: 3, En: t9(9, 55, 40), Texto: ""},
	{Seq: 4, En: t9(9, 55, 45), Texto: ""},
	{Seq: 5, En: t9(9, 56, 10), Texto: "Y la otra de bizcocho de vainilla que tenga lluvia de colores, con dulce de leche y merengue, de 25 o 30 porciones"},
	{Seq: 6, En: t9(9, 56, 30), Texto: ""},
	{Seq: 7, En: t9(9, 56, 50), Texto: "También quería un paquete de tequeños congelados de 30"},
	{Seq: 8, En: t9(13, 10, 0), Texto: ""},
	{Seq: 9, En: t9(13, 11, 0), Texto: "Ah, y así de vainilla la quiero"},
}

// Las CINCO referencias del caso, una por cada destino posible del reparto:
//
//	foto1, foto2 → línea 0 por PROXIMIDAD (llegan pegadas al mensaje de la torta 1)
//	audio1       → SOLICITUD por ser audio (regla 1, sin excepción)
//	foto3        → SOLICITUD por quedar FUERA de la ventana (3 h después)
//	foto4        → línea 1 por MENCIÓN («de vainilla» en su propio pie)
var (
	refFoto1 = anclaje.MediaRef{Ref: "wapp/media/foto1.jpg", Kind: anclaje.KindImage, Seq: 3, En: t9(9, 55, 40)}
	refFoto2 = anclaje.MediaRef{Ref: "wapp/media/foto2.jpg", Kind: anclaje.KindImage, Seq: 4, En: t9(9, 55, 45)}
	refAudio = anclaje.MediaRef{Ref: "wapp/media/audio1.ogg", Kind: anclaje.KindAudio, Seq: 6, En: t9(9, 56, 30)}
	refFoto3 = anclaje.MediaRef{Ref: "wapp/media/foto3.jpg", Kind: anclaje.KindImage, Seq: 8, En: t9(13, 10, 0)}
	refFoto4 = anclaje.MediaRef{Ref: "wapp/media/foto4.jpg", Kind: anclaje.KindImage, Seq: 9, En: t9(13, 11, 0)}
)

var refsAmbar = []anclaje.MediaRef{refFoto1, refFoto2, refAudio, refFoto3, refFoto4}

// ---------------------------------------------------------------------------
// Ayudas de aserción. Ninguna cuenta: TODAS dicen QUÉ ref está DÓNDE.
// ---------------------------------------------------------------------------

// refsDe devuelve los identificadores de un tramo del reparto, EN ORDEN. Comparar
// identificadores y no longitudes es la diferencia entre «hay dos fotos en la línea
// 0» y «las dos fotos que hay en la línea 0 son la 1 y la 2»: la primera frase la
// cumple un reparto que puso la foto de la torta de vainilla ahí.
func refsDe(refs []anclaje.MediaRef) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.Ref)
	}
	return out
}

func exigirLinea(t *testing.T, r anclaje.Reparto, idx int, quiero ...string) {
	t.Helper()
	tengo := refsDe(r.PorLinea[idx])
	if !slices.Equal(tengo, quiero) {
		t.Fatalf("línea %d: esperaba %v, obtuve %v", idx, quiero, tengo)
	}
}

func exigirSolicitud(t *testing.T, r anclaje.Reparto, quiero ...string) {
	t.Helper()
	tengo := refsDe(r.Solicitud)
	if !slices.Equal(tengo, quiero) {
		t.Fatalf("solicitud: esperaba %v, obtuve %v", quiero, tengo)
	}
}

// exigirLineasVacias comprueba que NINGUNA línea fuera de las permitidas recibió
// adjuntos. Sin esto, «la foto está en la línea 0» sería compatible con «y también en
// la 1», que es justo la mitad del invariante contable.
func exigirLineasVacias(t *testing.T, r anclaje.Reparto, permitidas ...int) {
	t.Helper()
	for _, idx := range slices.Sorted(maps.Keys(r.PorLinea)) {
		if len(r.PorLinea[idx]) == 0 {
			continue
		}
		if !slices.Contains(permitidas, idx) {
			t.Fatalf("la línea %d no debía recibir nada y recibió %v", idx, refsDe(r.PorLinea[idx]))
		}
	}
}

// ---------------------------------------------------------------------------
// CRITERIO 1 — «2 fotos enviadas tras hablar de la torta 1 ⇒ ancladas a ESA línea»
// ---------------------------------------------------------------------------

func TestCriterio1_DosFotosTrasLaTorta1_AnclanAEsaLinea(t *testing.T) {
	r := anclaje.Repartir(turnosAmbar, lineasAmbar, refsAmbar, anclaje.Opciones{})

	// LAS DOS, EN LA LÍNEA 0, Y NINGUNA OTRA. La aserción nombra las refs: un reparto
	// que colgara de la 0 la foto de la torta de vainilla la pasaría si aquí solo se
	// contara «2».
	exigirLinea(t, r, 0, "wapp/media/foto1.jpg", "wapp/media/foto2.jpg")

	// Y NO ESTÁN EN NINGÚN OTRO SITIO. Es la otra mitad del criterio: «anclada a esa
	// línea» y «además en la cabecera» no es lo mismo que «anclada a esa línea».
	for _, ref := range []string{"wapp/media/foto1.jpg", "wapp/media/foto2.jpg"} {
		if slices.Contains(refsDe(r.Solicitud), ref) {
			t.Fatalf("%s quedó ADEMÁS a nivel de solicitud", ref)
		}
		if slices.Contains(refsDe(r.PorLinea[1]), ref) {
			t.Fatalf("%s quedó ADEMÁS en la línea 1", ref)
		}
	}
}

func TestCriterio1_LaSegundaFotoNoOlvidaDeQueSeHablaba(t *testing.T) {
	// El caso que justifica que un turno SIN TEXTO no gaste presupuesto: la foto2 tiene
	// delante a la foto1, y si los adjuntos contaran como «mensajes hacia atrás», una
	// ráfaga de tres fotos dejaría huérfana a la tercera. Con presupuesto 1 —el mínimo
	// posible— las dos siguen anclando.
	r := anclaje.Repartir(turnosAmbar, lineasAmbar, []anclaje.MediaRef{refFoto1, refFoto2},
		anclaje.Opciones{MaxMensajesAtras: 1})
	exigirLinea(t, r, 0, "wapp/media/foto1.jpg", "wapp/media/foto2.jpg")
	exigirSolicitud(t, r)
}

// ---------------------------------------------------------------------------
// CRITERIO 2 — «un audio ⇒ a nivel de solicitud», con su etiqueta
// ---------------------------------------------------------------------------

func TestCriterio2_ElAudioVaANivelDeSolicitudConSuEtiqueta(t *testing.T) {
	r := anclaje.Repartir(turnosAmbar, lineasAmbar, []anclaje.MediaRef{refAudio}, anclaje.Opciones{})

	exigirSolicitud(t, r, "wapp/media/audio1.ogg")
	exigirLineasVacias(t, r) // ninguna línea, ni una

	if got := r.Solicitud[0].Label; got != "🎙️ audio del cliente — escúchalo" {
		t.Fatalf("etiqueta del audio: esperaba %q, obtuve %q", "🎙️ audio del cliente — escúchalo", got)
	}
	// La constante y el literal de arriba tienen que ser la MISMA cosa. Comparar solo
	// contra la constante sería el test tautológico que pasa con cualquier valor.
	if anclaje.EtiquetaAudio != "🎙️ audio del cliente — escúchalo" {
		t.Fatalf("EtiquetaAudio dejó de ser el texto de T3.3/REQ-29: %q", anclaje.EtiquetaAudio)
	}
}

func TestCriterio2_ElAudioNoSeAnclaNiCuandoSuPropioMensajeNombraElProducto(t *testing.T) {
	// 🔴 EL BLINDAJE DE LA REGLA 1. Aquí el audio llega con un pie que nombra a UNA
	// sola línea sin ambigüedad —«de chocolate»—: si el audio pasara por la mención
	// textual, anclaría. La regla dice SIEMPRE, y «siempre» incluye este caso.
	turnos := []anclaje.Turno{
		{Seq: 1, En: t9(9, 55, 30), Texto: "Serían 2 tortas. Una torta sería con decoración infantil, de bizcocho húmedo de chocolate con crema de chocolate, de 10 o 12 porciones"},
		{Seq: 2, En: t9(9, 55, 40), Texto: "Acá te explico la de chocolate"},
	}
	audio := anclaje.MediaRef{Ref: "wapp/media/nota.ogg", Kind: anclaje.KindAudio, Seq: 2, En: t9(9, 55, 40)}

	r := anclaje.Repartir(turnos, lineasAmbar, []anclaje.MediaRef{audio}, anclaje.Opciones{})
	exigirSolicitud(t, r, "wapp/media/nota.ogg")
	exigirLineasVacias(t, r)
}

func TestCriterio2_LaNotaDeVozTambienEsAudio(t *testing.T) {
	// WhatsApp manda la nota de voz como `ptt`, no como `audio`, y las cuatro notas de
	// voz del caso real son exactamente eso. Reconocer solo `audio` dejaría que la nota
	// de voz —el caso frecuente— se colara hasta una línea por proximidad.
	for _, kind := range []string{anclaje.KindAudio, anclaje.KindPTT, anclaje.KindVoice, "PTT", " Audio "} {
		t.Run(kind, func(t *testing.T) {
			ref := anclaje.MediaRef{Ref: "wapp/media/nota.ogg", Kind: kind, Seq: 3, En: t9(9, 55, 40)}
			r := anclaje.Repartir(turnosAmbar, lineasAmbar, []anclaje.MediaRef{ref}, anclaje.Opciones{})
			exigirSolicitud(t, r, "wapp/media/nota.ogg")
			exigirLineasVacias(t, r)
			if r.Solicitud[0].Label != anclaje.EtiquetaAudio {
				t.Fatalf("%q no recibió la etiqueta de audio", kind)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// CRITERIO 3 — el INVARIANTE CONTABLE: ni se pierde ni se duplica
// ---------------------------------------------------------------------------

func TestInvarianteContable_NiSePierdeNiSeDuplica(t *testing.T) {
	// El caso con VARIAS refs y con los TRES destinos ocupados: dos anclajes por
	// proximidad, uno por mención, un audio en la cabecera y una foto que se fue a la
	// cabecera por falta de certeza. Un invariante comprobado sobre un reparto donde
	// todo cae en el mismo sitio no comprueba nada.
	r := anclaje.Repartir(turnosAmbar, lineasAmbar, refsAmbar, anclaje.Opciones{})

	// LA CUENTA ES SOBRE EL MULTICONJUNTO DE IDENTIFICADORES, no sobre el tamaño: así
	// una ref que aparece dos veces y otra que desaparece —que dejan el total
	// intacto— se ven igual de bien que una pérdida.
	salida := make([]string, 0, len(refsAmbar))
	for _, idx := range slices.Sorted(maps.Keys(r.PorLinea)) {
		salida = append(salida, refsDe(r.PorLinea[idx])...)
	}
	salida = append(salida, refsDe(r.Solicitud)...)
	slices.Sort(salida)

	entrada := refsDe(refsAmbar)
	slices.Sort(entrada)

	if !slices.Equal(entrada, salida) {
		t.Fatalf("el reparto no conserva las refs:\n entrada %v\n salida  %v", entrada, salida)
	}

	// Y el reparto CONCRETO, que es lo que hace que el invariante no sea satisfacible
	// mandándolo todo a la cabecera.
	exigirLinea(t, r, 0, "wapp/media/foto1.jpg", "wapp/media/foto2.jpg")
	exigirLinea(t, r, 1, "wapp/media/foto4.jpg")
	exigirSolicitud(t, r, "wapp/media/audio1.ogg", "wapp/media/foto3.jpg")
	exigirLineasVacias(t, r, 0, 1)
}

func TestInvarianteContable_ElRepartoEsEstable(t *testing.T) {
	// Dos ejecuciones sobre la misma entrada dan el MISMO reparto. Importa porque la
	// mención textual recorre un mapa por dentro: si el resultado dependiera de ese
	// orden, el fallo sería intermitente y solo saldría en producción.
	primero := anclaje.Repartir(turnosAmbar, lineasAmbar, refsAmbar, anclaje.Opciones{})
	for i := range 50 {
		otro := anclaje.Repartir(turnosAmbar, lineasAmbar, refsAmbar, anclaje.Opciones{})
		if !slices.Equal(refsDe(otro.Solicitud), refsDe(primero.Solicitud)) {
			t.Fatalf("vuelta %d: la solicitud cambió", i)
		}
		for _, idx := range slices.Sorted(maps.Keys(primero.PorLinea)) {
			if !slices.Equal(refsDe(otro.PorLinea[idx]), refsDe(primero.PorLinea[idx])) {
				t.Fatalf("vuelta %d: la línea %d cambió", i, idx)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// «SIN CERTEZA ⇒ SOLICITUD»: los cuatro caminos por los que NO se inventa un ancla
// ---------------------------------------------------------------------------

func TestSinCerteza_UnMensajeQueSostieneDosLineasNoAnclaANinguna(t *testing.T) {
	// El caso de Ambar si hubiera descrito las dos tortas en el MISMO mensaje: la foto
	// que llega después podría ser de cualquiera de las dos. Elegir una es inventar.
	turnos := []anclaje.Turno{
		{Seq: 1, En: t9(9, 55, 30), Texto: "Serían 2 tortas: una torta sería con decoración infantil, de bizcocho húmedo de chocolate " +
			"con crema de chocolate; la otra de bizcocho de vainilla que tenga lluvia de colores, con dulce de leche"},
		{Seq: 2, En: t9(9, 55, 40), Texto: ""},
	}
	foto := anclaje.MediaRef{Ref: "wapp/media/ambigua.jpg", Kind: anclaje.KindImage, Seq: 2, En: t9(9, 55, 40)}

	r := anclaje.Repartir(turnos, lineasAmbar, []anclaje.MediaRef{foto}, anclaje.Opciones{})
	exigirSolicitud(t, r, "wapp/media/ambigua.jpg")
	exigirLineasVacias(t, r)
}

func TestSinCerteza_UnaFotoAnteriorATodoVaALaSolicitud(t *testing.T) {
	// Llega antes de que el cliente hable de nada: no hay hacia atrás dónde mirar.
	foto := anclaje.MediaRef{Ref: "wapp/media/primera.jpg", Kind: anclaje.KindImage, Seq: 0, En: t9(9, 54, 0)}
	r := anclaje.Repartir(turnosAmbar, lineasAmbar, []anclaje.MediaRef{foto}, anclaje.Opciones{})
	exigirSolicitud(t, r, "wapp/media/primera.jpg")
	exigirLineasVacias(t, r)
}

func TestSinCerteza_LaVentanaTemporalCorta_ParDeControl(t *testing.T) {
	// A/B sobre EL MISMO reparto y la misma foto: lo único que cambia es cuándo llegó.
	// Sin el par, un verde en el lado «fuera de ventana» podría estar midiendo que la
	// foto nunca anclaba por otro motivo.
	dentro := anclaje.MediaRef{Ref: "wapp/media/x.jpg", Kind: anclaje.KindImage, Seq: 3, En: t9(9, 55, 40)}
	fuera := anclaje.MediaRef{Ref: "wapp/media/x.jpg", Kind: anclaje.KindImage, Seq: 3, En: t9(10, 55, 40)}

	rDentro := anclaje.Repartir(turnosAmbar, lineasAmbar, []anclaje.MediaRef{dentro}, anclaje.Opciones{})
	exigirLinea(t, rDentro, 0, "wapp/media/x.jpg")
	exigirSolicitud(t, rDentro)

	rFuera := anclaje.Repartir(turnosAmbar, lineasAmbar, []anclaje.MediaRef{fuera}, anclaje.Opciones{})
	exigirSolicitud(t, rFuera, "wapp/media/x.jpg")
	exigirLineasVacias(t, rFuera)
}

func TestSinCerteza_ElPresupuestoDeMensajesCorta_ParDeControl(t *testing.T) {
	// A/B sobre el OTRO tope: la misma foto, la misma distancia temporal, y lo único
	// que cambia es cuántos mensajes de charla se metieron por medio.
	base := []anclaje.Turno{
		{Seq: 1, En: t9(9, 55, 30), Texto: "Serían 2 tortas. Una torta sería con decoración infantil, de bizcocho húmedo de chocolate con crema de chocolate, de 10 o 12 porciones"},
	}
	charla := []anclaje.Turno{
		{Seq: 2, En: t9(9, 55, 35), Texto: "bueno"},
		{Seq: 3, En: t9(9, 55, 36), Texto: "dale"},
		{Seq: 4, En: t9(9, 55, 37), Texto: "gracias"},
	}
	foto := anclaje.MediaRef{Ref: "wapp/media/lejana.jpg", Kind: anclaje.KindImage, Seq: 9, En: t9(9, 55, 50)}

	// DOS mensajes de charla + el turno del adjunto (sin texto, gratis) ⇒ cabe en el
	// presupuesto de 3 y llega hasta la evidencia.
	cerca := append(slices.Clone(base), charla[:2]...)
	rCerca := anclaje.Repartir(cerca, lineasAmbar, []anclaje.MediaRef{foto}, anclaje.Opciones{})
	exigirLinea(t, rCerca, 0, "wapp/media/lejana.jpg")

	// TRES ⇒ el presupuesto se agota justo antes de la evidencia.
	lejos := append(slices.Clone(base), charla...)
	rLejos := anclaje.Repartir(lejos, lineasAmbar, []anclaje.MediaRef{foto}, anclaje.Opciones{})
	exigirSolicitud(t, rLejos, "wapp/media/lejana.jpg")
	exigirLineasVacias(t, rLejos)
}

func TestSinCerteza_LaLineaDeEnvioNoAtraeNada(t *testing.T) {
	// La línea de envío nace sin evidencia (no sale de ninguna frase del cliente) y su
	// etiqueta, «Envío», no aparece en ningún mensaje. Tiene que ser IMPOSIBLE que le
	// cuelgue una foto: sería la ilustración de un concepto que el cliente no describió.
	r := anclaje.Repartir(turnosAmbar, lineasAmbar, refsAmbar, anclaje.Opciones{})
	if refs := r.PorLinea[3]; len(refs) != 0 {
		t.Fatalf("la línea de envío recibió %v", refsDe(refs))
	}
}

// ---------------------------------------------------------------------------
// MENCIÓN TEXTUAL
// ---------------------------------------------------------------------------

func TestMencion_ElPieDelAdjuntoGanaALaProximidad(t *testing.T) {
	// La foto4 llega TRES HORAS después del último mensaje: por proximidad iría a la
	// cabecera (lo dice el criterio 3, donde la foto3 —misma hora, sin pie— acaba ahí).
	// Lo que la salva es su propio pie, «de vainilla», y eso es exactamente lo que este
	// test aísla: misma foto, mismo instante, con pie y sin él.
	r := anclaje.Repartir(turnosAmbar, lineasAmbar, []anclaje.MediaRef{refFoto3, refFoto4}, anclaje.Opciones{})
	exigirLinea(t, r, 1, "wapp/media/foto4.jpg")
	exigirSolicitud(t, r, "wapp/media/foto3.jpg")
}

func TestMencion_UnPieQueNombraADosLineasNoAnclaANinguna(t *testing.T) {
	turnos := []anclaje.Turno{
		{Seq: 1, En: t9(9, 55, 0), Texto: "Te mando fotos: la de chocolate y la de vainilla"},
	}
	foto := anclaje.MediaRef{Ref: "wapp/media/dos.jpg", Kind: anclaje.KindImage, Seq: 1, En: t9(9, 55, 0)}
	r := anclaje.Repartir(turnos, lineasAmbar, []anclaje.MediaRef{foto}, anclaje.Opciones{})
	exigirSolicitud(t, r, "wapp/media/dos.jpg")
	exigirLineasVacias(t, r)
}

func TestMencion_UnTokenCompartidoNoDistingue(t *testing.T) {
	// «torta» está en las etiquetas de LAS DOS tortas, así que no señala a ninguna.
	turnos := []anclaje.Turno{
		{Seq: 1, En: t9(9, 55, 0), Texto: "Mirá esta torta"},
	}
	foto := anclaje.MediaRef{Ref: "wapp/media/torta.jpg", Kind: anclaje.KindImage, Seq: 1, En: t9(9, 55, 0)}
	r := anclaje.Repartir(turnos, lineasAmbar, []anclaje.MediaRef{foto}, anclaje.Opciones{})
	exigirSolicitud(t, r, "wapp/media/torta.jpg")
	exigirLineasVacias(t, r)
}

func TestMencion_SeComparaPorTokenYNoPorSubcadena_ParDeControl(t *testing.T) {
	// La trampa de T3.2 con «sin sal» dentro de «salsa», aquí con un par MEDIDO:
	// «paquetería» contiene literalmente «paquete», que es el token distintivo de la
	// línea de tequeños. Y el falso positivo no es cosmético — la frase habla del
	// ENVÍO—: un strings.Contains colgaría la foto de la línea de tequeños.
	//
	// ⚠️ La primera versión de este test usaba «empaquetado», y era una trampa FALSA:
	// «empaquetado» lleva «paquet-A-do», así que no contiene «paquete» y ninguna
	// implementación caía en ella. Lo cazó la mutación M6 —cambiar el cotejo por token
	// por un strings.Contains— al quedarse SIN un solo test rojo. La palabra de ahora
	// está comprobada carácter a carácter.
	//
	// El par de control es lo que hace que el verde signifique algo: el mismo montaje
	// con «el paquete» SÍ ancla, así que el rojo del lado A no puede venir de que la
	// mención esté rota del todo.
	lineas := []anclaje.Linea{lineasAmbar[2], lineasAmbar[0]}

	trampa := []anclaje.Turno{{Seq: 1, En: t9(9, 55, 0), Texto: "¿me lo podés mandar por paquetería?"}}
	foto := anclaje.MediaRef{Ref: "wapp/media/p.jpg", Kind: anclaje.KindImage, Seq: 1, En: t9(9, 55, 0)}
	rTrampa := anclaje.Repartir(trampa, lineas, []anclaje.MediaRef{foto}, anclaje.Opciones{})
	exigirSolicitud(t, rTrampa, "wapp/media/p.jpg")
	exigirLineasVacias(t, rTrampa)

	bueno := []anclaje.Turno{{Seq: 1, En: t9(9, 55, 0), Texto: "¿y el paquete cómo viene?"}}
	rBueno := anclaje.Repartir(bueno, lineas, []anclaje.MediaRef{foto}, anclaje.Opciones{})
	exigirLinea(t, rBueno, 2, "wapp/media/p.jpg")
	exigirSolicitud(t, rBueno)
}

// ---------------------------------------------------------------------------
// EL CONTRATO DE SALIDA Y LOS BORDES
// ---------------------------------------------------------------------------

func TestSinInstantes_ElRepartoSigueDecidiendoPorOrden(t *testing.T) {
	// Un llamante que todavía no tenga los `ts_unix` a mano pasa los turnos con el
	// instante en cero. La ventana no descarta nada y el orden (`Seq`) hace el trabajo.
	// Es la forma DEGRADADA y sana: no hay un modo «adivina la hora».
	turnos := make([]anclaje.Turno, 0, len(turnosAmbar))
	for _, tn := range turnosAmbar {
		turnos = append(turnos, anclaje.Turno{Seq: tn.Seq, Texto: tn.Texto})
	}
	refs := []anclaje.MediaRef{
		{Ref: "wapp/media/foto1.jpg", Kind: anclaje.KindImage, Seq: 3},
		{Ref: "wapp/media/foto2.jpg", Kind: anclaje.KindImage, Seq: 4},
	}
	r := anclaje.Repartir(turnos, lineasAmbar, refs, anclaje.Opciones{})
	exigirLinea(t, r, 0, "wapp/media/foto1.jpg", "wapp/media/foto2.jpg")
	exigirSolicitud(t, r)
}

func TestLosTurnosDesordenadosSeOrdenanYNoSeMutanAlLlamante(t *testing.T) {
	desordenados := []anclaje.Turno{turnosAmbar[4], turnosAmbar[1], turnosAmbar[2], turnosAmbar[0], turnosAmbar[3]}
	copia := slices.Clone(desordenados)

	r := anclaje.Repartir(desordenados, lineasAmbar, []anclaje.MediaRef{refFoto1}, anclaje.Opciones{})
	exigirLinea(t, r, 0, "wapp/media/foto1.jpg")

	// Repartir NO reordena el slice del llamante: ordena una copia. Un paquete puro que
	// muta su entrada deja de ser puro justo cuando alguien lo llama dos veces.
	if !slices.Equal(refsDeTurnos(desordenados), refsDeTurnos(copia)) {
		t.Fatalf("Repartir reordenó el slice del llamante: %v", refsDeTurnos(desordenados))
	}
}

func refsDeTurnos(turnos []anclaje.Turno) []int {
	out := make([]int, 0, len(turnos))
	for _, t := range turnos {
		out = append(out, t.Seq)
	}
	return out
}

func TestSinRefs_ElRepartoEsVacioYUsable(t *testing.T) {
	r := anclaje.Repartir(turnosAmbar, lineasAmbar, nil, anclaje.Opciones{})
	if r.PorLinea == nil {
		t.Fatal("PorLinea llegó nil: el llamante no debería tener que comprobarlo")
	}
	exigirSolicitud(t, r)
	exigirLineasVacias(t, r)
}
