package stages_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/stages"
)

// ---------------------------------------------------------------------------
// Banco de pruebas de P4
// ---------------------------------------------------------------------------

// errNoLlamarP4 es lo que devuelven las cuatro etapas que P4 no debe tocar.
var errNoLlamarP4 = errors.New("fake: esta etapa no la llama P4")

// provFakeP4 es un llm.LLMProvider de mentira: solo NormalizeQuantities hace algo.
type provFakeP4 struct {
	respuesta json.RawMessage
	err       error
	entradas  []llm.NormalizeQuantitiesInput
}

func (p *provFakeP4) NormalizeQuantities(_ context.Context, in llm.NormalizeQuantitiesInput, _ llm.Options) (json.RawMessage, error) {
	p.entradas = append(p.entradas, in)
	return p.respuesta, p.err
}

func (p *provFakeP4) ClassifyRequest(context.Context, llm.ClassifyRequestInput, llm.Options) (json.RawMessage, error) {
	return nil, errNoLlamarP4
}

func (p *provFakeP4) ExtractMainIdeas(context.Context, llm.ExtractMainIdeasInput, llm.Options) (json.RawMessage, error) {
	return nil, errNoLlamarP4
}

func (p *provFakeP4) ExtractItemSpecs(context.Context, llm.ExtractItemSpecsInput, llm.Options) (json.RawMessage, error) {
	return nil, errNoLlamarP4
}

func (p *provFakeP4) GenerateQuoteText(context.Context, llm.GenerateQuoteTextInput, llm.Options) (json.RawMessage, error) {
	return nil, errNoLlamarP4
}

// ════════════════════════════════════════════════════════════════════════════
// 🔴 EL message_ts DEL FIXTURE ES FIJO, ABSOLUTO Y LEJANO. NO LO TOQUES.
// ════════════════════════════════════════════════════════════════════════════
//
// Es el instante del primer mensaje del caso Ambar: lunes 13/07/2026 a las 09:55 en
// UTC−3, tal cual lo escribe design §7.4 (`"message_ts": "2026-07-13T09:55:00-03:00"`).
//
// Y es lo que hace que el criterio «un job reanudado dos días después NO cambia la
// fecha» MIRE de verdad. P4 no lee el reloj: la única forma de que la fecha cambiase
// sería que alguien calculara desde `time.Now()`. Con un `message_ts` fijo y lejano,
// esa mutación da otra fecha y los tests se ponen rojos. Con un `message_ts = now()`,
// darían la misma y el criterio se quedaría CIEGO SIN QUE NADIE SE ENTERE — que es
// exactamente el fallo que la ola anterior dejó cuatro veces.
//
// Por eso assertFixtureLejosDeHoy exige margen: si alguien mueve esta constante cerca
// de hoy, el test no se queda callado, se cae.
var messageTSDeAmbar = time.Date(2026, 7, 13, 9, 55, 0, 0, time.FixedZone("-03", -3*60*60))

// margenDelFixture es lo que exigimos entre el `message_ts` del fixture y hoy. Treinta
// días es holgado a propósito: no se trata de medir nada, sino de que un `now()`
// disfrazado no pueda colarse.
const margenDelFixture = 30 * 24 * time.Hour

// assertFixtureLejosDeHoy protege al criterio de sí mismo. Ver el bloque de arriba.
func assertFixtureLejosDeHoy(t *testing.T, ts time.Time) {
	t.Helper()
	d := time.Since(ts)
	if d < 0 {
		d = -d
	}
	if d < margenDelFixture {
		t.Fatalf("el message_ts del fixture (%s) está a menos de %v de hoy: los tests de fecha "+
			"DEJAN DE VIGILAR si la base se acerca al reloj. Muévelo al pasado, no relajes el margen.",
			ts.Format(time.RFC3339), margenDelFixture)
	}
}

// jobAmbarP4 es el job del caso, con su `message_ts`.
func jobAmbarP4() intake.ClaimedJob {
	j := jobDeAmbar()
	j.MessageTS = messageTSDeAmbar
	return j
}

// specsDeAmbar son las TRES specs que P3 deja vivas para el caso, con la forma de
// design §7.2: los rangos siguen siendo TEXTUALES («10 o 12 porciones» en `variant`),
// que es justo lo que P4 tiene que estructurar.
func specsDeAmbar() []llm.ItemSpec {
	return []llm.ItemSpec{
		{
			Product: "torta", Variant: "10 o 12 porciones",
			AddonCandidates: []string{"decoración infantil"},
			Customizations:  []string{"sin lactosa"},
			Notes:           "bizcocho húmedo de chocolate con crema de chocolate",
			Evidence:        evidenciaTortaChocolate,
		},
		{
			Product: "torta", Variant: "25 o 30 porciones",
			Notes:    "bizcocho de vainilla con lluvia de colores, dulce de leche y merengue",
			Evidence: evidenciaTortaVainilla,
		},
		{Product: "tequeños congelados", Evidence: evidenciaTequenos},
	}
}

// pistaDeAmbar es la pista de entrega tal como la deja P2 (ya anclada al literal).
func pistaDeAmbar() *llm.Hint {
	return &llm.Hint{Text: hintDeAmbar()[0], Evidence: hintDeAmbar()[1]}
}

// respuestaP4Ambar es lo que devuelve un modelo BIEN PORTADO para el caso: los tres
// ítems en orden, el rango partido, el paquete etiquetado y ninguna cantidad inventada.
const respuestaP4Ambar = `{"version":1,
 "delivery_date":"2026-07-22","delivery_date_basis":"message_ts=2026-07-13",
 "items":[
  {"product":"torta","qty":1,"range":{"min":10,"max":12,"unit":"porciones"},
   "addon_candidates":["decoración infantil"],"customizations":["sin lactosa"],
   "notes":"bizcocho húmedo de chocolate con crema de chocolate",
   "evidence":"una torta sería con decoración infantil, de bizcocho húmedo de chocolate"},
  {"product":"torta","qty":1,"range":{"min":25,"max":30,"unit":"porciones"},
   "notes":"bizcocho de vainilla con lluvia de colores, dulce de leche y merengue",
   "evidence":"otra de bizcocho de vainilla que tenga lluvia de colores"},
  {"product":"tequeños congelados","qty":1,"unit_kind":"package","package_size":30,
   "evidence":"un paquete de tequeños congelados de 30"}]}`

// etapaP4 arma la etapa con sus dobles, la zona por defecto y el log capturado.
func etapaP4(t *testing.T, resp string, buf *bytes.Buffer) (*stages.P4, *provFakeP4, *storeFake) {
	t.Helper()
	prov := &provFakeP4{respuesta: json.RawMessage(resp)}
	sel := &selFake{prov: prov}
	store := &storeFake{}
	p4, err := stages.NewP4(logger.New(logger.WithWriter(buf)), sel, store, stages.ZonaPorDefecto)
	if err != nil {
		t.Fatalf("NewP4: %v", err)
	}
	return p4, prov, store
}

// corridaDeAmbar ejecuta el caso completo y devuelve el artefacto persistido.
func corridaDeAmbar(t *testing.T, resp string) (*llm.Quantities, *storeFake) {
	t.Helper()
	var buf bytes.Buffer
	p4, _, store := etapaP4(t, resp, &buf)
	art, err := p4.Run(context.Background(), jobAmbarP4(), textoAmbar, specsDeAmbar(), pistaDeAmbar())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(store.guardados) != 1 {
		t.Fatalf("se persistieron %d artefactos; se esperaba 1", len(store.guardados))
	}
	return art, store
}

// clavesDe decodifica un objeto JSON a su mapa de claves.
func clavesDe(t *testing.T, raw json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("no es un objeto JSON: %v", err)
	}
	return m
}

// assertClavesExactas comprueba que el objeto trae ESAS claves y ninguna más. Es lo que
// convierte «parecido a §7.3» en «§7.3»: una clave de más (un `unit_kind` en una torta)
// o de menos (un `range` que se perdió) rompe el contrato con el match y con la bandeja.
func assertClavesExactas(t *testing.T, donde string, obj map[string]json.RawMessage, quiero []string) {
	t.Helper()
	got := make([]string, 0, len(obj))
	for k := range obj {
		got = append(got, k)
	}
	falta := diferencia(quiero, got)
	sobra := diferencia(got, quiero)
	if len(falta) > 0 || len(sobra) > 0 {
		t.Fatalf("%s: claves que faltan %v, claves que sobran %v (se esperaban exactamente %v)",
			donde, falta, sobra, quiero)
	}
}

// diferencia son los elementos de `a` que no están en `b`.
func diferencia(a, b []string) []string {
	fuera := []string{}
	for _, x := range a {
		encontrado := false
		for _, y := range b {
			if x == y {
				encontrado = true
				break
			}
		}
		if !encontrado {
			fuera = append(fuera, x)
		}
	}
	return fuera
}

// ---------------------------------------------------------------------------
// CRITERIO 2 DEL PLAN: el caso Ambar produce el artefacto de design §7.3
// ---------------------------------------------------------------------------

// TestP4_CasoAmbar_ProduceElArtefactoDeLaSeccion73 es el criterio literal de T2.4 con el
// fixture del caso (⚠️ calidad C, ver ambar_fixture_test.go).
//
// ⚠️ QUÉ SIGNIFICA «EXACTO» AQUÍ, PORQUE §7.3 NO ES COPIABLE AL PIE DE LA LETRA. El
// ejemplo del design tiene DOS defectos que se descubren al ejecutarlo, y van dichos
// para que nadie los persiga como si fueran un fallo del código:
//
//  1. lista DOS ítems (torta de chocolate y tequeños) cuando el caso tiene TRES —§7.1,
//     §7.4 y el fixture traen también la torta de vainilla—;
//  2. su `evidence` de la torta es «de 10 o 12 porciones aprox», y la palabra «aprox»
//     no aparece en ninguna parte del texto del cliente: esa evidencia NO pasaría el
//     anclaje de esta misma ola.
//
// Así que «exacto» es lo que sí es contrato: las CLAVES del artefacto y de cada ítem,
// la fecha absoluta, la base desde la que se calculó, el rango partido sin colapsar y
// el paquete etiquetado con su tamaño.
//
// 💥 MUTACIONES EJECUTADAS, todas compilan y todas lo ponen rojo: quitar la asignación
// de DeliveryDateBasis en fechar ⇒ falta la clave; copiar `del.Product` en vez de
// conservar el de P3 ⇒ no lo ve este test pero sí el de la evidencia; poner
// `it.Range = nil` en aplicarCantidades ⇒ cae el rango.
func TestP4_CasoAmbar_ProduceElArtefactoDeLaSeccion73(t *testing.T) {
	assertFixtureLejosDeHoy(t, messageTSDeAmbar)
	art, store := corridaDeAmbar(t, respuestaP4Ambar)

	if store.guardados[0].Stage != intake.StageP4 {
		t.Fatalf("el artefacto se guardó bajo la etapa %q", store.guardados[0].Stage)
	}
	raiz := clavesDe(t, store.guardados[0].Payload)
	assertClavesExactas(t, "artefacto", raiz, []string{"version", "delivery_date", "delivery_date_basis", "items"})

	if art.Version != llm.ArtifactVersion {
		t.Fatalf("version = %d", art.Version)
	}
	if art.DeliveryDate != "2026-07-22" {
		t.Fatalf("delivery_date = %q; el plan dice 2026-07-22 (miércoles de la semana que viene desde el lunes 13/07)", art.DeliveryDate)
	}
	if art.DeliveryDateBasis != "message_ts=2026-07-13" {
		t.Fatalf("delivery_date_basis = %q; §7.3 dice message_ts=2026-07-13", art.DeliveryDateBasis)
	}
	if len(art.Items) != 3 {
		t.Fatalf("items = %d; el caso tiene TRES (§7.3 solo dibuja dos, ver el docstring)", len(art.Items))
	}
	assertTortaDeChocolate(t, art.Items[0], raiz)
	assertTequenos(t, art.Items[2], raiz)
}

// assertTortaDeChocolate comprueba el primer ítem de §7.3: rango partido, sin paquete.
func assertTortaDeChocolate(t *testing.T, it llm.NormalizedItem, raiz map[string]json.RawMessage) {
	t.Helper()
	if it.Product != "torta" || it.Qty != 1 {
		t.Fatalf("torta: product=%q qty=%d; se esperaba torta/1", it.Product, it.Qty)
	}
	quiero := llm.Range{Min: 10, Max: 12, Unit: "porciones"}
	if it.Range == nil || *it.Range != quiero {
		t.Fatalf("torta: range = %+v; §7.3 dice {min:10,max:12,unit:porciones}", it.Range)
	}
	if it.UnitKind != "" || it.PackageSize != 0 {
		t.Fatalf("torta: una torta no es un paquete (unit_kind=%q, package_size=%d)", it.UnitKind, it.PackageSize)
	}
	if !reflect.DeepEqual(it.AddonCandidates, []string{"decoración infantil"}) ||
		!reflect.DeepEqual(it.Customizations, []string{"sin lactosa"}) {
		t.Fatalf("torta: los candidatos y las personalizaciones no viajaron: %+v / %+v", it.AddonCandidates, it.Customizations)
	}
	obj := clavesDe(t, itemN(t, raiz, 0))
	assertClavesExactas(t, "items[0]", obj, []string{
		"product", "qty", "range", "addon_candidates", "customizations", "notes", "evidence",
	})
}

// assertTequenos comprueba el segundo ítem de §7.3: paquete, sin rango, JAMÁS qty 30.
func assertTequenos(t *testing.T, it llm.NormalizedItem, raiz map[string]json.RawMessage) {
	t.Helper()
	if it.Qty != 1 || it.UnitKind != llm.UnitKindPackage || it.PackageSize != 30 {
		t.Fatalf("tequeños: qty=%d unit_kind=%q package_size=%d; §7.3 dice 1/package/30",
			it.Qty, it.UnitKind, it.PackageSize)
	}
	if it.Range != nil {
		t.Fatalf("tequeños: no hay rango que partir, y salió %+v", it.Range)
	}
	obj := clavesDe(t, itemN(t, raiz, 2))
	assertClavesExactas(t, "items[2]", obj, []string{"product", "qty", "unit_kind", "package_size", "evidence"})
}

// itemN saca el ítem n del artefacto crudo.
func itemN(t *testing.T, raiz map[string]json.RawMessage, n int) json.RawMessage {
	t.Helper()
	var items []json.RawMessage
	if err := json.Unmarshal(raiz["items"], &items); err != nil {
		t.Fatalf("items no es un array: %v", err)
	}
	if n >= len(items) {
		t.Fatalf("no hay ítem %d (hay %d)", n, len(items))
	}
	return items[n]
}

// ---------------------------------------------------------------------------
// CRITERIO 3 DEL PLAN: un job reanudado dos días después NO cambia la fecha
// ---------------------------------------------------------------------------

// TestP4_JobReanudadoDosDiasDespues_NoCambiaLaFecha es el mejor criterio de la ola y el
// más fácil de dejar ciego, así que aquí va con las dos redes puestas.
//
// # QUÉ SIMULA UNA REANUDACIÓN, DE VERDAD
//
// Un job se reclama, muere el worker, vuelve a la cola y lo recoge otro DOS DÍAS
// después con sus artefactos intactos (design §3.2). Lo único que cambia entre las dos
// pasadas es el reloj de pared; el `message_ts` de la fila es el mismo. Por eso el test
// corre la etapa DOS VECES sobre el mismo job y exige artefactos IDÉNTICOS byte a byte,
// y por eso la fecha esperada está escrita a mano: comparar las dos pasadas entre sí
// pasaría también si las dos leyeran el mismo reloj.
//
// # LA RED QUE HACE QUE ESTO NO SE QUEDE CIEGO
//
// assertFixtureLejosDeHoy. Si alguien escribiera el fixture con `time.Now()`, calcular
// desde el reloj daría la misma fecha que calcular desde `message_ts` y este test
// seguiría verde SIN VIGILAR NADA. Con la base en 2026-07-13 y el margen exigido, esa
// mutación es imposible de esconder. Y TestStages_NoLeenElReloj la caza aunque alguien
// borre este test entero.
//
// 💥 MUTACIÓN EJECUTADA: en fechar, `base := time.Now().In(s.zona)` ⇒ delivery_date pasa
// a ser un miércoles de esta semana y las dos aserciones caen.
func TestP4_JobReanudadoDosDiasDespues_NoCambiaLaFecha(t *testing.T) {
	assertFixtureLejosDeHoy(t, messageTSDeAmbar)

	primera, storeA := corridaDeAmbar(t, respuestaP4Ambar)
	segunda, storeB := corridaDeAmbar(t, respuestaP4Ambar)

	if primera.DeliveryDate != "2026-07-22" || segunda.DeliveryDate != "2026-07-22" {
		t.Fatalf("la fecha salió de otro sitio que del message_ts: %q y %q",
			primera.DeliveryDate, segunda.DeliveryDate)
	}
	if !bytes.Equal(storeA.guardados[0].Payload, storeB.guardados[0].Payload) {
		t.Fatalf("dos pasadas del MISMO job dieron artefactos distintos:\n%s\n%s",
			storeA.guardados[0].Payload, storeB.guardados[0].Payload)
	}
}

// TestStages_NoLeenElReloj es el criterio 3 escrito como REGLA sobre el código, no como
// conducta: ninguna etapa de este paquete puede llamar a `time.Now` ni a `time.Since`.
//
// Se testea la regla y no N conductas porque la conducta solo se ve cuando además el
// fixture está lejos de hoy, y eso es una condición que se puede perder por descuido.
// Esto no: el día que alguien escriba `time.Now()` en p4.go o en fechas.go, sale rojo
// aunque los demás tests hayan quedado ciegos.
//
// 💥 MUTACIÓN EJECUTADA: añadir `_ = time.Now()` en fechar ⇒ rojo con el nombre del
// fichero y la línea.
func TestStages_NoLeenElReloj(t *testing.T) {
	prohibidos := map[string]bool{"Now": true, "Since": true}
	for _, fichero := range []string{"p4.go", "fechas.go", "p2.go", "p3.go"} {
		fset := token.NewFileSet()
		arbol, err := parser.ParseFile(fset, fichero, nil, 0)
		if err != nil {
			t.Fatalf("no se pudo leer %s: %v", fichero, err)
		}
		ast.Inspect(arbol, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if ok && pkg.Name == "time" && prohibidos[sel.Sel.Name] {
				t.Errorf("%s: %s lee el RELOJ (time.%s). La base de fechas es intake_jobs.message_ts "+
					"y solo ella (D-044.9): un job reanudado dos días después tiene que dar la misma fecha.",
					fset.Position(sel.Pos()), fichero, sel.Sel.Name)
			}
			return true
		})
	}
}

// ---------------------------------------------------------------------------
// CRITERIO 4: «paquete de 30» nunca produce qty 30
// ---------------------------------------------------------------------------

// TestP4_PaqueteDe30_NuncaSeConvierteEnQty30 cubre los DOS lados de la regla, que es lo
// que la hace una garantía y no una petición al modelo:
//
//   - el modelo se porta bien ⇒ el paquete viaja tal cual;
//   - el modelo comete EL error caro —`qty:30`, que multiplica el presupuesto por
//     treinta— ⇒ corregirPaquete lo deshace y el artefacto sale con 1 paquete de 30.
//
// El segundo caso es el que responde a «¿qué tendría que pasar para que esto fallara?»:
// sin la red en Go, la única defensa sería el prompt, y un test contra un modelo de
// mentira bien portado no probaría absolutamente nada.
//
// 💥 MUTACIONES EJECUTADAS: (a) en corregirPaquete, `return false` al principio ⇒ el
// segundo caso persiste qty 30; (b) mapear el tamaño a `it.Qty = tam` en vez de a
// `PackageSize` ⇒ los dos casos caen.
func TestP4_PaqueteDe30_NuncaSeConvierteEnQty30(t *testing.T) {
	casos := []struct {
		nombre string
		item   string
	}{
		{"el modelo etiqueta bien el paquete",
			`{"product":"tequeños congelados","qty":1,"unit_kind":"package","package_size":30,
			  "evidence":"un paquete de tequeños congelados de 30"}`},
		{"🔴 el modelo confunde el tamaño con la cantidad y Go lo corrige",
			`{"product":"tequeños congelados","qty":30,
			  "evidence":"un paquete de tequeños congelados de 30"}`},
		{"🔴 y también cuando encima puso la etiqueta: 30 paquetes de 30 son 900 unidades",
			`{"product":"tequeños congelados","qty":30,"unit_kind":"package","package_size":30,
			  "evidence":"un paquete de tequeños congelados de 30"}`},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			art := unSoloItem(t, specsDeAmbar()[2], c.item)
			if art.Items[0].Qty != 1 {
				t.Fatalf("qty = %d; «un paquete de 30» es UNA unidad de paquete, JAMÁS 30 (§7.3)", art.Items[0].Qty)
			}
			if art.Items[0].UnitKind != llm.UnitKindPackage || art.Items[0].PackageSize != 30 {
				t.Fatalf("unit_kind=%q package_size=%d; se esperaba package/30",
					art.Items[0].UnitKind, art.Items[0].PackageSize)
			}
		})
	}
}

// unSoloItem corre la etapa con UNA spec y UN ítem en la respuesta del modelo.
func unSoloItem(t *testing.T, spec llm.ItemSpec, itemJSON string) *llm.Quantities {
	t.Helper()
	var buf bytes.Buffer
	p4, _, _ := etapaP4(t, `{"version":1,"items":[`+itemJSON+`]}`, &buf)
	art, err := p4.Run(context.Background(), jobAmbarP4(), textoAmbar, []llm.ItemSpec{spec}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(art.Items) != 1 {
		t.Fatalf("items = %d; se esperaba 1", len(art.Items))
	}
	return art
}

// ---------------------------------------------------------------------------
// CRITERIO 5: un rango no se colapsa
// ---------------------------------------------------------------------------

// TestP4_ElRangoNoSeColapsa fija que «10 o 12 porciones» sale como rango y con la
// cantidad en 1: ni 11, ni 12, ni una torta de más.
//
// Elegir dentro del rango es del DUEÑO en la bandeja (Ola 3) y de nadie más: el sistema
// no sabe si el cliente quiere 10 o 12, y decidirlo por él es cobrar de más o quedarse
// corto en una fiesta.
//
// 💥 MUTACIONES EJECUTADAS: en aplicarCantidades, (a) `it.Qty = del.Range.Max` y
// `it.Range = nil` (colapsar al máximo) ⇒ rojo; (b) `it.Range = nil` a secas ⇒ rojo.
func TestP4_ElRangoNoSeColapsa(t *testing.T) {
	art := unSoloItem(t, specsDeAmbar()[0],
		`{"product":"torta","qty":1,"range":{"min":10,"max":12,"unit":"porciones"},
		  "evidence":"una torta sería con decoración infantil, de bizcocho húmedo de chocolate"}`)

	it := art.Items[0]
	if it.Range == nil {
		t.Fatal("el rango se perdió: «10 o 12 porciones» dejó de ser un rango")
	}
	if it.Range.Min != 10 || it.Range.Max != 12 || it.Range.Unit != "porciones" {
		t.Fatalf("el rango salió %+v; se esperaba {10 12 porciones}", *it.Range)
	}
	if it.Qty != 1 {
		t.Fatalf("qty = %d: el rango se coló en la cantidad", it.Qty)
	}
}

// ---------------------------------------------------------------------------
// CRITERIO 6: la cantidad omitida es 1, nunca 0
// ---------------------------------------------------------------------------

// TestP4_CantidadOmitida_EsUnoYNuncaCero cubre los dos caminos por los que un ítem
// puede quedarse sin cantidad, que NO son el mismo y acaban distinto a propósito:
//
//  1. **el modelo no devuelve el ítem** ⇒ sale con la normalización neutra (`qty` 1) y
//     el cliente no pierde lo que pidió;
//  2. **el modelo devuelve el ítem con `qty` omitida** (o sea, 0) ⇒ `llm.ParseQuantities`
//     lo rechaza como fallo de calidad y NO SE PERSISTE NADA. P4 no lo convierte en 1
//     por su cuenta: eso taparía una salida degenerada, y el job se reintenta (T2.5).
//
// 🔴 Lo segundo es una afirmación del plan que resultó IMPRECISA al ejecutarla: «qty
// omitida ⇒ 1» la aplica el PROMPT, y el parser compartido es la red. Ver qtyOmitida.
//
// 💥 MUTACIONES EJECUTADAS: (a) `const qtyOmitida = 0` ⇒ cae el primer caso; (b) en
// normalizar, tragarse el error de ParseQuantities y seguir ⇒ cae el segundo.
func TestP4_CantidadOmitida_EsUnoYNuncaCero(t *testing.T) {
	t.Run("el ítem que el modelo no devolvió sale con 1", func(t *testing.T) {
		var buf bytes.Buffer
		p4, _, store := etapaP4(t, `{"version":1,"items":[
			{"product":"torta","qty":2,
			 "evidence":"una torta sería con decoración infantil, de bizcocho húmedo de chocolate"}]}`, &buf)
		art, err := p4.Run(context.Background(), jobAmbarP4(), textoAmbar, specsDeAmbar(), nil)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(art.Items) != 3 {
			t.Fatalf("items = %d; los TRES ítems de P3 tienen que sobrevivir aunque el modelo devuelva uno", len(art.Items))
		}
		if art.Items[1].Qty != 1 || art.Items[2].Qty != 1 {
			t.Fatalf("qty de los ítems que el modelo no devolvió: %d y %d; se esperaba 1",
				art.Items[1].Qty, art.Items[2].Qty)
		}
		if art.Items[2].Product != "tequeños congelados" || art.Items[2].Evidence != evidenciaTequenos {
			t.Fatalf("el ítem neutro perdió los datos de P3: %+v", art.Items[2])
		}
		if len(store.guardados) != 1 {
			t.Fatalf("se persistieron %d artefactos", len(store.guardados))
		}
	})

	t.Run("un qty omitido por el modelo no se persiste como 0 ni se maquilla como 1", func(t *testing.T) {
		var buf bytes.Buffer
		p4, _, store := etapaP4(t, `{"version":1,"items":[
			{"product":"tequeños congelados",
			 "evidence":"un paquete de tequeños congelados de 30"}]}`, &buf)
		_, err := p4.Run(context.Background(), jobAmbarP4(), textoAmbar, specsDeAmbar()[2:], nil)
		if !errors.Is(err, llm.ErrLLMQuality) {
			t.Fatalf("err = %v; se esperaba un fallo de calidad", err)
		}
		if len(store.guardados) != 0 {
			t.Fatalf("se persistió un artefacto con una salida degenerada: %s", store.guardados[0].Payload)
		}
	})
}

// ---------------------------------------------------------------------------
// LA ZONA HORARIA: el hueco, fijado en un test para que se vea
// ---------------------------------------------------------------------------

// TestP4_LaZonaHorariaGobiernaElDia_YHoyEsUTC deja CLAVADO en un test el hueco que el
// bloque de p4.go documenta: no hay zona horaria por tenant en ninguna parte del
// repositorio, así que hoy manda `ZonaPorDefecto` = UTC, y con UTC un mensaje de la
// noche en UTC−3 se fecha con el día siguiente.
//
// El test NO dice que eso esté bien: dice qué hace el sistema HOY. El día que exista
// una zona de negocio y alguien la enchufe, este test se pone rojo y obliga a mirar la
// decisión en vez de dejarla pasar en silencio — que es exactamente lo que un cambio de
// zona hace si nadie lo vigila.
//
// 💥 MUTACIÓN EJECUTADA: en fechar, usar `job.MessageTS` sin `.In(s.zona)` ⇒ el segundo
// caso pasa a dar 2026-07-14 en el basis (el día local del mensaje) y cae.
func TestP4_LaZonaHorariaGobiernaElDia_YHoyEsUTC(t *testing.T) {
	casos := []struct {
		nombre     string
		ts         time.Time
		quieroBase string
	}{
		{"un mensaje de la mañana cae en el mismo día en las dos zonas",
			messageTSDeAmbar, "message_ts=2026-07-13"},
		{"🔴 EL HUECO: un mensaje del lunes a las 22:00 en UTC−3 ya es martes en UTC",
			time.Date(2026, 7, 13, 22, 0, 0, 0, time.FixedZone("-03", -3*60*60)),
			"message_ts=2026-07-14"},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			var buf bytes.Buffer
			p4, _, _ := etapaP4(t, respuestaP4Ambar, &buf)
			job := jobAmbarP4()
			job.MessageTS = c.ts
			art, err := p4.Run(context.Background(), job, textoAmbar, specsDeAmbar(), pistaDeAmbar())
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if art.DeliveryDateBasis != c.quieroBase {
				t.Fatalf("delivery_date_basis = %q; se esperaba %q", art.DeliveryDateBasis, c.quieroBase)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// El reparto con el modelo
// ---------------------------------------------------------------------------

// TestP4_LaFechaDelModeloNoSeUsa_MandaLaDeGo es la otra mitad de «la aritmética es de
// Go»: el prompt SÍ le pide una fecha al modelo y el parser SÍ la decodifica, así que
// hace falta una prueba de que no se cuela. Aquí el modelo propone una fecha distinta
// —y perfectamente bien formada— y el artefacto sale con la de Go.
//
// 💥 MUTACIÓN EJECUTADA: en normalizar, `art.DeliveryDate = out.DeliveryDate` ⇒ rojo.
func TestP4_LaFechaDelModeloNoSeUsa_MandaLaDeGo(t *testing.T) {
	var buf bytes.Buffer
	p4, _, _ := etapaP4(t, `{"version":1,"delivery_date":"2026-09-01",
		"delivery_date_basis":"message_ts=2026-08-25","items":[
		{"product":"tequeños congelados","qty":1,"unit_kind":"package","package_size":30,
		 "evidence":"un paquete de tequeños congelados de 30"}]}`, &buf)

	art, err := p4.Run(context.Background(), jobAmbarP4(), textoAmbar, specsDeAmbar()[2:], pistaDeAmbar())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if art.DeliveryDate != "2026-07-22" || art.DeliveryDateBasis != "message_ts=2026-07-13" {
		t.Fatalf("se coló la fecha del modelo: %q / %q", art.DeliveryDate, art.DeliveryDateBasis)
	}
	if !bytes.Contains(buf.Bytes(), []byte("no coincide con la que calculó Go")) {
		t.Fatalf("la discrepancia no dejó aviso en el log:\n%s", buf.String())
	}
}

// TestP4_EvidenciaInventada_SeConservaLaDeP3 aplica la regla de `internal/evidence` con
// la respuesta que le toca a esta etapa: aquí el ítem ya está probado desde P3, así que
// una evidencia inventada NO tumba nada y NO se guarda — simplemente no sustituye a la
// que había.
//
// 💥 MUTACIÓN EJECUTADA: en aplicarCantidades, `it.Evidence = del.Evidence` sin
// comprobar ⇒ el artefacto guarda una frase que el cliente nunca escribió y sale rojo.
func TestP4_EvidenciaInventada_SeConservaLaDeP3(t *testing.T) {
	art := unSoloItem(t, specsDeAmbar()[2],
		`{"product":"tequeños congelados","qty":1,"unit_kind":"package","package_size":30,
		  "evidence":"`+evidenciaInventada+`"}`)

	if art.Items[0].Evidence != evidenciaTequenos {
		t.Fatalf("evidence = %q; se esperaba la de P3, que sí está en el literal", art.Items[0].Evidence)
	}
}

// TestP4_MasItemsQueP3_LosSobrantesSeDescartan protege la asimetría que fundir
// documenta: de menos se completa, de más se descarta. Un ítem de más es una línea de
// más COBRADA, y ese es el lado que no se puede perdonar.
//
// 💥 MUTACIÓN EJECUTADA: en fundir, recorrer `norm` en vez de `specs` ⇒ salen dos ítems.
func TestP4_MasItemsQueP3_LosSobrantesSeDescartan(t *testing.T) {
	var buf bytes.Buffer
	p4, _, _ := etapaP4(t, `{"version":1,"items":[
		{"product":"tequeños congelados","qty":1,"unit_kind":"package","package_size":30,
		 "evidence":"un paquete de tequeños congelados de 30"},
		{"product":"tequeños congelados","qty":1,"unit_kind":"package","package_size":30,
		 "evidence":"un paquete de tequeños congelados de 30"}]}`, &buf)

	art, err := p4.Run(context.Background(), jobAmbarP4(), textoAmbar, specsDeAmbar()[2:], nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(art.Items) != 1 {
		t.Fatalf("items = %d; P3 dejó UNO y un ítem de más es una línea cobrada de más", len(art.Items))
	}
	if !bytes.Contains(buf.Bytes(), []byte("los sobrantes se descartan")) {
		t.Fatalf("el descarte no dejó aviso en el log:\n%s", buf.String())
	}
}

// TestP4_ElArtefactoPersistidoLoSigueLeyendoElParserCompartido cierra el círculo: lo
// que P4 escribe tiene que poder volver a entrar por `llm.ParseQuantities`, que es el
// lector que usarán el match y el borrador. «Lo comprobé leyendo el código» envejece;
// un test no.
func TestP4_ElArtefactoPersistidoLoSigueLeyendoElParserCompartido(t *testing.T) {
	_, store := corridaDeAmbar(t, respuestaP4Ambar)

	leido, err := llm.ParseQuantities(store.guardados[0].Payload)
	if err != nil {
		t.Fatalf("el artefacto persistido no pasa el parser compartido: %v", err)
	}
	if leido.DeliveryDate != "2026-07-22" || len(leido.Items) != 3 {
		t.Fatalf("el parser leyó otra cosa: %q con %d ítems", leido.DeliveryDate, len(leido.Items))
	}
}

// ---------------------------------------------------------------------------
// Los caminos que no llaman al modelo, y los cierres
// ---------------------------------------------------------------------------

// TestP4_SinFecha_LosTresMotivos fija los tres desenlaces sin fecha, que NO son fallos:
// sin pista, con una pista que no se reconoce, y sin `message_ts` en la fila.
//
// En los tres el artefacto se persiste igual y sin `delivery_date`: un presupuesto sin
// fecha lo arregla el dueño preguntando; un presupuesto con una fecha inventada no lo
// arregla nadie, porque tiene toda la cara de ser cierta.
//
// 💥 MUTACIÓN EJECUTADA: en fechar, quitar la guarda de `MessageTS.IsZero()` ⇒ el tercer
// caso pasa a fechar contra el año 1 y sale «0001-01-07».
func TestP4_SinFecha_LosTresMotivos(t *testing.T) {
	casos := []struct {
		nombre string
		pista  *llm.Hint
		sinTS  bool
	}{
		{"el cliente no dijo cuándo", nil, false},
		{"la pista no se reconoce", &llm.Hint{Text: "cuando puedas", Evidence: "cuando puedas"}, false},
		{"el job no trae message_ts", pistaDeAmbar(), true},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			var buf bytes.Buffer
			p4, _, store := etapaP4(t, respuestaP4Ambar, &buf)
			job := jobAmbarP4()
			if c.sinTS {
				job.MessageTS = time.Time{}
			}
			art, err := p4.Run(context.Background(), job, textoAmbar, specsDeAmbar(), c.pista)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if art.DeliveryDate != "" || art.DeliveryDateBasis != "" {
				t.Fatalf("salió fecha donde no la hay: %q / %q", art.DeliveryDate, art.DeliveryDateBasis)
			}
			if len(store.guardados) != 1 {
				t.Fatalf("el artefacto sin fecha tiene que persistirse igual (guardados: %d)", len(store.guardados))
			}
		})
	}
}

// TestP4_SinItems_NoSeLlamaAlModeloPeroSiSeFecha es design §3.2 al pie de la letra
// («cero resultados válidos tampoco es fatal») más lo que esta etapa añade: la fecha no
// depende del modelo, así que un pedido cuyo P3 se quedó sin ítems conserva el día que
// el cliente pidió y el dueño solo tiene que escribir las líneas.
//
// 💥 MUTACIÓN EJECUTADA: llamar a normalizar también con 0 ítems ⇒ el fake registra una
// llamada y sale rojo.
func TestP4_SinItems_NoSeLlamaAlModeloPeroSiSeFecha(t *testing.T) {
	var buf bytes.Buffer
	p4, prov, store := etapaP4(t, respuestaP4Ambar, &buf)

	art, err := p4.Run(context.Background(), jobAmbarP4(), textoAmbar, nil, pistaDeAmbar())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(prov.entradas) != 0 {
		t.Fatalf("se llamó al modelo %d veces sin un solo ítem que normalizar", len(prov.entradas))
	}
	if art.DeliveryDate != "2026-07-22" {
		t.Fatalf("delivery_date = %q; la fecha no depende del modelo", art.DeliveryDate)
	}
	if len(store.guardados) != 1 || len(art.Items) != 0 {
		t.Fatalf("guardados=%d items=%d; se esperaba un artefacto vacío persistido", len(store.guardados), len(art.Items))
	}
}

// TestP4_SinLiteral_NoSeLlamaAlModelo corta antes del cable: un job sin literal no gasta
// la plaza única del Edge (22–32 s) en un prompt sin texto del cliente.
func TestP4_SinLiteral_NoSeLlamaAlModelo(t *testing.T) {
	var buf bytes.Buffer
	p4, prov, store := etapaP4(t, respuestaP4Ambar, &buf)

	_, err := p4.Run(context.Background(), jobAmbarP4(), "", specsDeAmbar(), pistaDeAmbar())
	if !errors.Is(err, stages.ErrSinLiteral) {
		t.Fatalf("err = %v; se esperaba ErrSinLiteral", err)
	}
	if len(prov.entradas) != 0 || len(store.guardados) != 0 {
		t.Fatalf("llamadas=%d guardados=%d; no debía haber ni una", len(prov.entradas), len(store.guardados))
	}
}

// TestP4_ErrorDeInfraestructura_NiSeReintentaNiSePersiste: P4 hace UNA llamada, así que
// el reintento es del JOB (T2.5) y no de la etapa. El error sube con su familia intacta
// y no se guarda un artefacto a medias.
func TestP4_ErrorDeInfraestructura_NiSeReintentaNiSePersiste(t *testing.T) {
	var buf bytes.Buffer
	caido := errors.New("el Edge no tiene capacidad")
	prov := &provFakeP4{err: caido}
	sel := &selFake{prov: prov}
	store := &storeFake{}
	p4, err := stages.NewP4(logger.New(logger.WithWriter(&buf)), sel, store, stages.ZonaPorDefecto)
	if err != nil {
		t.Fatalf("NewP4: %v", err)
	}

	_, err = p4.Run(context.Background(), jobAmbarP4(), textoAmbar, specsDeAmbar(), pistaDeAmbar())
	if !errors.Is(err, caido) {
		t.Fatalf("err = %v; el error tiene que subir con su familia intacta", err)
	}
	if len(prov.entradas) != 1 {
		t.Fatalf("llamadas = %d; una caída de infraestructura NO se reintenta aquí", len(prov.entradas))
	}
	if len(store.guardados) != 0 {
		t.Fatalf("se persistió algo con el proveedor caído")
	}
}

// TestP4_JobQueDejoDeEstarEnProcessing: otro worker terminó el job mientras corría la
// etapa. No es un fallo de la base ni del modelo, y viaja como centinela.
func TestP4_JobQueDejoDeEstarEnProcessing(t *testing.T) {
	var buf bytes.Buffer
	prov := &provFakeP4{respuesta: json.RawMessage(respuestaP4Ambar)}
	store := &storeFake{perdido: true}
	p4, err := stages.NewP4(logger.New(logger.WithWriter(&buf)), &selFake{prov: prov}, store, stages.ZonaPorDefecto)
	if err != nil {
		t.Fatalf("NewP4: %v", err)
	}

	_, err = p4.Run(context.Background(), jobAmbarP4(), textoAmbar, specsDeAmbar(), pistaDeAmbar())
	if !errors.Is(err, stages.ErrJobFueraDeProcessing) {
		t.Fatalf("err = %v; se esperaba ErrJobFueraDeProcessing", err)
	}
}

// TestP4_LaViaSePideConLaSesionDeOrigen: el segundo dato es el que enruta la inferencia
// al Edge que recibió el mensaje, y ya se perdió una vez (T1.7-8).
func TestP4_LaViaSePideConLaSesionDeOrigen(t *testing.T) {
	var buf bytes.Buffer
	prov := &provFakeP4{respuesta: json.RawMessage(respuestaP4Ambar)}
	sel := &selFake{prov: prov}
	p4, err := stages.NewP4(logger.New(logger.WithWriter(&buf)), sel, &storeFake{}, stages.ZonaPorDefecto)
	if err != nil {
		t.Fatalf("NewP4: %v", err)
	}
	if _, err := p4.Run(context.Background(), jobAmbarP4(), textoAmbar, specsDeAmbar(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !reflect.DeepEqual(sel.pide, []string{tenant + "/" + sesion}) {
		t.Fatalf("el selector recibió %v", sel.pide)
	}
	if len(prov.entradas) != 1 || prov.entradas[0].SourceText != textoAmbar {
		t.Fatalf("el literal no llegó entero al prompt")
	}
	if !prov.entradas[0].MessageTS.Equal(messageTSDeAmbar) {
		t.Fatalf("el message_ts no llegó al prompt: %v", prov.entradas[0].MessageTS)
	}
}

// TestNewP4_SinCablearYSinZona: una etapa a medio cablear no nace «por si acaso», y la
// zona horaria no se hereda de un cero — es una decisión que el llamante tiene que
// escribir (ver el bloque de la zona en p4.go).
func TestNewP4_SinCablearYSinZona(t *testing.T) {
	log := logger.New(logger.WithWriter(&bytes.Buffer{}))
	sel := &selFake{}
	store := &storeFake{}

	casos := []struct {
		nombre string
		log    logger.Logger
		sel    stages.ProviderSelector
		store  stages.StageStore
		zona   *time.Location
		quiero error
	}{
		{"sin log", nil, sel, store, stages.ZonaPorDefecto, stages.ErrSinCablear},
		{"sin selector", log, nil, store, stages.ZonaPorDefecto, stages.ErrSinCablear},
		{"sin store", log, sel, nil, stages.ZonaPorDefecto, stages.ErrSinCablear},
		{"🔴 sin zona horaria", log, sel, store, nil, stages.ErrSinZonaHoraria},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			p4, err := stages.NewP4(c.log, c.sel, c.store, c.zona)
			if !errors.Is(err, c.quiero) {
				t.Fatalf("err = %v; se esperaba %v", err, c.quiero)
			}
			if p4 != nil {
				t.Fatal("se construyó la etapa a medias")
			}
		})
	}
}

// TestP4_NadaDeTextoDelClienteEnElLog es la regla de ADR-0034/INV-6 en la etapa que más
// cerca está de romperla: aquí se avisa de pistas que no se resuelven, de evidencias
// inventadas y de productos renombrados, y NINGUNO de esos avisos puede llevar la
// palabra del cliente que lo provocó.
func TestP4_NadaDeTextoDelClienteEnElLog(t *testing.T) {
	var buf bytes.Buffer
	p4, _, _ := etapaP4(t, `{"version":1,"items":[
		{"product":"tequenios","qty":30,"evidence":"`+evidenciaInventada+`"}]}`, &buf)

	pista := &llm.Hint{Text: "cuando terminen las vacaciones", Evidence: "cuando terminen las vacaciones"}
	if _, err := p4.Run(context.Background(), jobAmbarP4(), textoAmbar, specsDeAmbar()[2:], pista); err != nil {
		t.Fatalf("Run: %v", err)
	}

	log := buf.String()
	for _, prohibido := range []string{"vacaciones", evidenciaInventada, evidenciaTequenos, "tequenios"} {
		if bytes.Contains([]byte(log), []byte(prohibido)) {
			t.Fatalf("el log filtró texto del cliente (%q):\n%s", prohibido, log)
		}
	}
}
