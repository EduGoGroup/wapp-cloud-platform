package stages_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/stages"
)

// ---------------------------------------------------------------------------
// Banco de pruebas de P4
// ---------------------------------------------------------------------------

// 🔴 EL message_ts DEL FIXTURE ES FIJO, ABSOLUTO Y DEL PASADO. NO LO TOQUES.
//
// Es el instante del primer mensaje del caso Ambar: lunes 13/07/2026 a las 09:55 en
// UTC−3. Las fechas esperadas de estos tests están ESCRITAS A MANO contra él, y eso es
// lo que hace que «un job reanudado dos días después NO cambia la fecha» mire de
// verdad: una etapa que calculase desde el reloj daría otra fecha y caería. Con un
// `message_ts` tomado del reloj, los tests se quedarían ciegos sin que nadie se entere.
var ambarMessageTS = time.Date(2026, 7, 13, 9, 55, 0, 0, time.FixedZone("-03", -3*60*60))

// ambarP4Job es el job del caso, con su `message_ts`.
func ambarP4Job() intake.ClaimedJob {
	job := ambarJob()
	job.MessageTS = ambarMessageTS
	return job
}

// ambarSpecs son las TRES specs que P3 deja vivas para el caso: los rangos siguen siendo
// TEXTUALES («10 o 12 porciones» en `variant`), que es lo que P4 tiene que estructurar.
func ambarSpecs() []llm.ItemSpec {
	return []llm.ItemSpec{
		{
			Product: "torta", Variant: "10 o 12 porciones",
			AddonCandidates: []string{"decoración infantil"},
			Customizations:  []string{"sin lactosa"},
			Notes:           "bizcocho húmedo de chocolate con crema de chocolate",
			Evidence:        chocolateCakeEvidence,
		},
		{
			Product: "torta", Variant: "25 o 30 porciones",
			Notes:    "bizcocho de vainilla con lluvia de colores, dulce de leche y merengue",
			Evidence: vanillaCakeEvidence,
		},
		{Product: "tequeños congelados", Evidence: tequenosEvidence},
	}
}

// ambarDeliveryHint es la pista de entrega tal como la deja P2, ya anclada.
func ambarDeliveryHint() *llm.Hint {
	return &llm.Hint{Text: ambarHintText, Evidence: deliveryEvidence}
}

// ambarP4Output es lo que devuelve un modelo BIEN PORTADO para el caso: los tres ítems
// en orden, el rango partido, el paquete etiquetado y ninguna cantidad inventada.
const ambarP4Output = `{"version":1,
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

// tequenosP4Output es la respuesta bien portada para el tercer ítem solo.
const tequenosP4Output = `{"version":1,"items":[
  {"product":"tequeños congelados","qty":1,"unit_kind":"package","package_size":30,
   "evidence":"un paquete de tequeños congelados de 30"}]}`

// p4Call es lo que el provider anotó de UNA llamada de P4.
type p4Call struct {
	in          llm.NormalizeQuantitiesInput
	temperature float64
	bounded     bool
	remaining   time.Duration
}

// p4Bench son la etapa y sus dobles.
type p4Bench struct {
	stage    *stages.P4
	calls    []p4Call
	selector *fakeSelector
	store    *fakeStore
	log      bytes.Buffer
}

// newP4Bench arma P4 en la zona por defecto con un provider que contesta `response` (o
// `providerErr`).
func newP4Bench(t *testing.T, response string, providerErr error, opts ...stages.Option) *p4Bench {
	t.Helper()
	b := &p4Bench{store: &fakeStore{}}
	b.selector = &fakeSelector{provider: &fakeProvider{
		onQuantities: func(ctx context.Context, in llm.NormalizeQuantitiesInput, o llm.Options) (json.RawMessage, error) {
			call := p4Call{in: in, temperature: o.Temperature}
			if deadline, ok := ctx.Deadline(); ok {
				call.bounded, call.remaining = true, time.Until(deadline)
			}
			b.calls = append(b.calls, call)
			return json.RawMessage(response), providerErr
		},
	}}
	stage, err := stages.NewP4(captureLog(&b.log), b.selector, b.store, stages.DefaultZone, opts...)
	if err != nil {
		t.Fatalf("NewP4: %v", err)
	}
	b.stage = stage
	return b
}

// runAmbarP4 ejecuta el caso completo y devuelve el banco y el artefacto.
func runAmbarP4(t *testing.T) (*p4Bench, *llm.Quantities) {
	t.Helper()
	b := newP4Bench(t, ambarP4Output, nil)
	art, err := b.stage.Run(context.Background(), ambarP4Job(), ambarText, ambarSpecs(), ambarDeliveryHint())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(b.store.saved) != 1 {
		t.Fatalf("se persistieron %d artefactos; se esperaba 1", len(b.store.saved))
	}
	return b, art
}

// runOneItem corre la etapa con UNA spec y UN ítem en la respuesta del modelo, sin pista
// de entrega.
func runOneItem(t *testing.T, spec llm.ItemSpec, itemJSON string) (*p4Bench, llm.NormalizedItem) {
	t.Helper()
	b := newP4Bench(t, `{"version":1,"items":[`+itemJSON+`]}`, nil)
	art, err := b.stage.Run(context.Background(), ambarP4Job(), ambarText, []llm.ItemSpec{spec}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(art.Items) != 1 {
		t.Fatalf("items = %d; se esperaba 1", len(art.Items))
	}
	return b, art.Items[0]
}

// keysOf decodifica un objeto JSON a sus claves, ordenadas.
func keysOf(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("no es un objeto JSON: %v", err)
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// assertExactKeys comprueba que el objeto trae ESAS claves y ninguna más: una clave de
// más (un `unit_kind` en una torta) o de menos (un `range` que se perdió) rompe el
// contrato con el match y con la bandeja.
func assertExactKeys(t *testing.T, where string, raw json.RawMessage, want ...string) {
	t.Helper()
	slices.Sort(want)
	if got := keysOf(t, raw); !slices.Equal(got, want) {
		t.Fatalf("%s: claves %v; se esperaban exactamente %v", where, got, want)
	}
}

// savedItems saca los ítems crudos del artefacto persistido.
func savedItems(t *testing.T, payload json.RawMessage) []json.RawMessage {
	t.Helper()
	var root struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(payload, &root); err != nil {
		t.Fatalf("el artefacto persistido no decodifica: %v", err)
	}
	return root.Items
}

// ---------------------------------------------------------------------------
// El caso Ambar produce el artefacto de design §7.3
// ---------------------------------------------------------------------------

// TestP4Run_AmbarCase_ProducesTheContractArtifact fija lo que SÍ es contrato de §7.3:
// las CLAVES del artefacto y de cada ítem, la fecha absoluta, la base desde la que se
// calculó, el rango partido sin colapsar y el paquete etiquetado con su tamaño.
func TestP4Run_AmbarCase_ProducesTheContractArtifact(t *testing.T) {
	b, art := runAmbarP4(t)

	if b.store.saved[0].Stage != intake.StageP4 || b.store.jobs[0] != jobID {
		t.Fatalf("el artefacto se guardó bajo la etapa %q y el job %q", b.store.saved[0].Stage, b.store.jobs[0])
	}
	assertExactKeys(t, "artefacto", b.store.saved[0].Payload, "version", "delivery_date", "delivery_date_basis", "items")

	if art.Version != llm.ArtifactVersion {
		t.Fatalf("version = %d", art.Version)
	}
	if art.DeliveryDate != "2026-07-22" {
		t.Fatalf("delivery_date = %q; es 2026-07-22 (miércoles de la semana que viene desde el lunes 13/07)", art.DeliveryDate)
	}
	if art.DeliveryDateBasis != "message_ts=2026-07-13" {
		t.Fatalf("delivery_date_basis = %q; se esperaba message_ts=2026-07-13", art.DeliveryDateBasis)
	}
	if len(art.Items) != 3 {
		t.Fatalf("items = %d; el caso tiene TRES", len(art.Items))
	}
	raw := savedItems(t, b.store.saved[0].Payload)
	assertChocolateCake(t, art.Items[0], raw[0])
	assertTequenos(t, art.Items[2], raw[2])

	log := b.log.String()
	for _, want := range []string{"p4: cantidades y fecha normalizadas y persistidas", "items=3", "con_fecha=true"} {
		if !strings.Contains(log, want) {
			t.Fatalf("la línea de cierre no lleva %q: %q", want, log)
		}
	}
}

// assertChocolateCake comprueba el primer ítem del caso: rango partido, sin paquete.
func assertChocolateCake(t *testing.T, cake llm.NormalizedItem, raw json.RawMessage) {
	t.Helper()
	if cake.Product != "torta" || cake.Qty != 1 {
		t.Fatalf("torta: product=%q qty=%d; se esperaba torta/1", cake.Product, cake.Qty)
	}
	if cake.Range == nil || *cake.Range != (llm.Range{Min: 10, Max: 12, Unit: "porciones"}) {
		t.Fatalf("torta: range = %+v; se esperaba {min:10,max:12,unit:porciones}", cake.Range)
	}
	if cake.UnitKind != "" || cake.PackageSize != 0 {
		t.Fatalf("torta: una torta no es un paquete (unit_kind=%q, package_size=%d)", cake.UnitKind, cake.PackageSize)
	}
	if !reflect.DeepEqual(cake.AddonCandidates, []string{"decoración infantil"}) ||
		!reflect.DeepEqual(cake.Customizations, []string{"sin lactosa"}) {
		t.Fatalf("torta: los candidatos y las personalizaciones no viajaron: %v / %v", cake.AddonCandidates, cake.Customizations)
	}
	assertExactKeys(t, "items[0]", raw,
		"product", "qty", "range", "addon_candidates", "customizations", "notes", "evidence")
}

// assertTequenos comprueba el tercer ítem del caso: paquete, sin rango, JAMÁS qty 30.
func assertTequenos(t *testing.T, tequenos llm.NormalizedItem, raw json.RawMessage) {
	t.Helper()
	if tequenos.Qty != 1 || tequenos.UnitKind != llm.UnitKindPackage || tequenos.PackageSize != 30 {
		t.Fatalf("tequeños: qty=%d unit_kind=%q package_size=%d; se esperaba 1/package/30",
			tequenos.Qty, tequenos.UnitKind, tequenos.PackageSize)
	}
	if tequenos.Range != nil {
		t.Fatalf("tequeños: no hay rango que partir, y salió %+v", tequenos.Range)
	}
	assertExactKeys(t, "items[2]", raw, "product", "qty", "unit_kind", "package_size", "evidence")
}

// TestP4Run_AsksOnceWithTheOriginSessionTheLiteralAndTheMessageTS: UNA llamada greedy,
// con el literal ENTERO, las specs de P3 y el `message_ts`; el provider se pide con la
// sesión que enruta la inferencia al Edge de origen.
func TestP4Run_AsksOnceWithTheOriginSessionTheLiteralAndTheMessageTS(t *testing.T) {
	b, _ := runAmbarP4(t)

	if !reflect.DeepEqual(b.selector.asked, []string{originRoute}) {
		t.Fatalf("el selector recibió %v; se esperaba [%q]", b.selector.asked, originRoute)
	}
	if len(b.calls) != 1 {
		t.Fatalf("llamadas = %d; P4 hace UNA", len(b.calls))
	}
	call := b.calls[0]
	if call.in.SourceText != ambarText {
		t.Fatal("el literal no llegó entero al prompt")
	}
	if !reflect.DeepEqual(call.in.Items, ambarSpecs()) {
		t.Fatalf("las specs de P3 no llegaron tal cual al prompt: %+v", call.in.Items)
	}
	if !call.in.MessageTS.Equal(ambarMessageTS) {
		t.Fatalf("el message_ts no llegó al prompt: %v", call.in.MessageTS)
	}
	if call.temperature != llm.TemperatureGreedy {
		t.Fatalf("temperatura = %v; P4 llama a %v", call.temperature, llm.TemperatureGreedy)
	}
}

// TestP4Run_ResumedJob_GivesTheSameArtifactByteForByte: lo único que cambia entre dos
// pasadas del mismo job es el reloj de pared, y la etapa no lo lee. La fecha esperada
// está escrita a mano: comparar las dos pasadas entre sí pasaría también si las dos
// leyeran el mismo reloj.
func TestP4Run_ResumedJob_GivesTheSameArtifactByteForByte(t *testing.T) {
	first, firstArt := runAmbarP4(t)
	second, secondArt := runAmbarP4(t)

	if firstArt.DeliveryDate != "2026-07-22" || secondArt.DeliveryDate != "2026-07-22" {
		t.Fatalf("la fecha salió de otro sitio que del message_ts: %q y %q", firstArt.DeliveryDate, secondArt.DeliveryDate)
	}
	if !bytes.Equal(first.store.saved[0].Payload, second.store.saved[0].Payload) {
		t.Fatalf("dos pasadas del MISMO job dieron artefactos distintos:\n%s\n%s",
			first.store.saved[0].Payload, second.store.saved[0].Payload)
	}
}

// ---------------------------------------------------------------------------
// Los caminos que no persisten
// ---------------------------------------------------------------------------

// TestP4Run_NoLiteral_NeitherAsksForAProviderNorCallsTheModel corta antes del cable.
func TestP4Run_NoLiteral_NeitherAsksForAProviderNorCallsTheModel(t *testing.T) {
	b := newP4Bench(t, ambarP4Output, nil)

	art, err := b.stage.Run(context.Background(), ambarP4Job(), "", ambarSpecs(), ambarDeliveryHint())
	if !errors.Is(err, stages.ErrNoLiteral) {
		t.Fatalf("err = %v; se esperaba ErrNoLiteral", err)
	}
	if art != nil || len(b.selector.asked) != 0 || len(b.calls) != 0 || len(b.store.saved) != 0 {
		t.Fatalf("art=%v provider=%d llamadas=%d guardados=%d; sin literal no se hace nada",
			art, len(b.selector.asked), len(b.calls), len(b.store.saved))
	}
}

// TestP4Run_SelectorFails_WrapsTheErrorAndCallsNothing: sin provider no hay llamada.
func TestP4Run_SelectorFails_WrapsTheErrorAndCallsNothing(t *testing.T) {
	errVia := errors.New("el tenant no tiene vía configurada")
	b := newP4Bench(t, ambarP4Output, nil)
	b.selector.err = errVia

	art, err := b.stage.Run(context.Background(), ambarP4Job(), ambarText, ambarSpecs(), ambarDeliveryHint())
	if !errors.Is(err, errVia) {
		t.Fatalf("err = %v; el fallo del selector tiene que salir envuelto con %%w", err)
	}
	if want := "p4: elegir el proveedor del tenant: " + errVia.Error(); err.Error() != want {
		t.Fatalf("texto del error = %q; se esperaba %q", err.Error(), want)
	}
	if art != nil || len(b.calls) != 0 || len(b.store.saved) != 0 {
		t.Fatalf("art=%v llamadas=%d guardados=%d", art, len(b.calls), len(b.store.saved))
	}
}

// TestP4Run_InfrastructureError_IsNeitherRetriedNorPersisted: P4 hace UNA llamada, así
// que el reintento es del JOB. El error sube con su familia intacta y no se guarda un
// artefacto a medias.
func TestP4Run_InfrastructureError_IsNeitherRetriedNorPersisted(t *testing.T) {
	errInfra := errors.New("el Edge no tiene capacidad")
	b := newP4Bench(t, "", errInfra)

	art, err := b.stage.Run(context.Background(), ambarP4Job(), ambarText, ambarSpecs(), ambarDeliveryHint())
	if !errors.Is(err, errInfra) || errors.Is(err, llm.ErrLLMQuality) {
		t.Fatalf("err = %v; el error tiene que subir con su familia intacta, y no como calidad", err)
	}
	if want := "p4: pedir la normalización de cantidades: " + errInfra.Error(); err.Error() != want {
		t.Fatalf("texto del error = %q; se esperaba %q", err.Error(), want)
	}
	if art != nil || len(b.calls) != 1 || len(b.store.saved) != 0 {
		t.Fatalf("art=%v llamadas=%d guardados=%d; una llamada, sin reintento y sin artefacto",
			art, len(b.calls), len(b.store.saved))
	}
}

// TestP4Run_StoreDidNotSave_TellsTheTwoWaysApart: la base caída y la transición que no
// aplicó no son el mismo error.
func TestP4Run_StoreDidNotSave_TellsTheTwoWaysApart(t *testing.T) {
	t.Run("the store fails", func(t *testing.T) {
		errDB := errors.New("conexión perdida")
		b := newP4Bench(t, ambarP4Output, nil)
		b.store.err = errDB

		art, err := b.stage.Run(context.Background(), ambarP4Job(), ambarText, ambarSpecs(), ambarDeliveryHint())
		if !errors.Is(err, errDB) || errors.Is(err, stages.ErrJobNotProcessing) {
			t.Fatalf("err = %v; se esperaba el fallo del store envuelto", err)
		}
		if want := "p4: persistir el artefacto: " + errDB.Error(); err.Error() != want {
			t.Fatalf("texto del error = %q; se esperaba %q", err.Error(), want)
		}
		if art != nil {
			t.Fatal("Run devolvió artefacto sin haberlo persistido")
		}
	})

	t.Run("the job left processing", func(t *testing.T) {
		b := newP4Bench(t, ambarP4Output, nil)
		b.store.lost = true

		art, err := b.stage.Run(context.Background(), ambarP4Job(), ambarText, ambarSpecs(), ambarDeliveryHint())
		if !errors.Is(err, stages.ErrJobNotProcessing) {
			t.Fatalf("err = %v; se esperaba ErrJobNotProcessing", err)
		}
		if art != nil {
			t.Fatal("Run devolvió artefacto sin haberlo persistido")
		}
	})
}

// ---------------------------------------------------------------------------
// El plazo por llamada (R-03) y el constructor
// ---------------------------------------------------------------------------

// TestP4Run_CallTimeout_BoundsTheModelCallAndNotThePersistence: con la opción, el ctx de
// LA llamada llega acotado y la escritura del artefacto no lo hereda; sin ella, se
// hereda el ctx del llamante.
func TestP4Run_CallTimeout_BoundsTheModelCallAndNotThePersistence(t *testing.T) {
	const limit = 48 * time.Second

	t.Run("with the option", func(t *testing.T) {
		b := newP4Bench(t, ambarP4Output, nil, stages.WithCallTimeout(limit))
		if _, err := b.stage.Run(context.Background(), ambarP4Job(), ambarText, ambarSpecs(), nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(b.calls) != 1 || !b.calls[0].bounded {
			t.Fatal("la llamada al modelo llegó SIN deadline: el adaptador caería a su default de 30 s")
		}
		if got := b.calls[0].remaining; got > limit || got < limit-5*time.Second {
			t.Fatalf("plazo que llegó a la llamada = %v; se esperaba ≈ %v", got, limit)
		}
		if len(b.store.bounded) != 1 || b.store.bounded[0] {
			t.Fatal("la persistencia heredó el plazo de la llamada")
		}
	})

	t.Run("without the option", func(t *testing.T) {
		b := newP4Bench(t, ambarP4Output, nil)
		if _, err := b.stage.Run(context.Background(), ambarP4Job(), ambarText, ambarSpecs(), nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(b.calls) != 1 || b.calls[0].bounded {
			t.Fatal("sin la opción la llamada hereda el ctx del llamante, que aquí no trae deadline")
		}
	})
}

// TestDefaultZone_IsUTC: la zona por defecto es la ausencia de decisión, y es la única
// que coincide con la fecha de referencia que imprime el prompt compartido.
func TestDefaultZone_IsUTC(t *testing.T) {
	if stages.DefaultZone != time.UTC {
		t.Fatalf("DefaultZone = %v; hoy la zona que gobierna las fechas es UTC", stages.DefaultZone)
	}
}

// TestNewP4_NotWiredOrWithoutZone: una etapa a medio cablear no nace, y la zona horaria
// no se hereda de un cero: es una decisión que el llamante tiene que escribir.
func TestNewP4_NotWiredOrWithoutZone(t *testing.T) {
	log := captureLog(&bytes.Buffer{})
	selector, store := &fakeSelector{}, &fakeStore{}

	cases := []struct {
		name     string
		log      logger.Logger
		selector stages.ProviderSelector
		store    stages.StageStore
		zone     *time.Location
		want     error
	}{
		{"without log", nil, selector, store, stages.DefaultZone, stages.ErrNotWired},
		{"without selector", log, nil, store, stages.DefaultZone, stages.ErrNotWired},
		{"without store", log, selector, nil, stages.DefaultZone, stages.ErrNotWired},
		{"without time zone", log, selector, store, nil, stages.ErrNoTimeZone},
		{"without store and without time zone", log, selector, nil, nil, stages.ErrNotWired},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stage, err := stages.NewP4(c.log, c.selector, c.store, c.zone)
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v; se esperaba %v", err, c.want)
			}
			if stage != nil {
				t.Fatal("se construyó la etapa a medias")
			}
		})
	}

	const text = "stages: P4 necesita la zona horaria que gobierna el cálculo de fechas (hoy, stages.ZonaPorDefecto)"
	if stages.ErrNoTimeZone.Error() != text {
		t.Fatalf("texto de ErrNoTimeZone = %q; es observable y se conserva literal", stages.ErrNoTimeZone.Error())
	}
}
