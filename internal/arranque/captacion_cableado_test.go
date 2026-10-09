package arranque

// captacion_cableado_test.go — EL TEST DE CABLEADO DE captación, COMPLETO (F7 · T7.23–T7.24,
// R7.2.a, R7.6.b, R7.7.a; arquitectura.md §6 de F7; 05 §4.2, hallazgo 39 de F1). Es la segunda
// mitad obligatoria del test de bridge_captacion.go (la primera, la traducción, vive en
// bridge_captacion_test.go). No basta mirar el tipo de un campo del contenedor: se afirma, sobre
// el arranque nuevo REAL (las fases 2–8 del contenedor de la huella, sin red ni BD), que la cola,
// el worker con su aforo y su índice, el pool de clasificaciones, el servicio de re-análisis y el
// store de intenciones son los de internal/modulos/captacion (y el índice, el de
// internal/modulos/catalogo); que de cada uno hay UNA construcción, la nueva; que los adaptadores
// envuelven LOS MISMOS objetos del contenedor —un solo compositor, un solo pool— y que la cara
// HTTP nueva sirve H1, E1 y E2 con ellos; y, por ruta de import, que ningún fichero de
// internal/arranque, de producción o de test, toca la captación vieja fuera del adaptador.
//
// `captacion` NO entra en Conmutados (internal/modulos/fronteras_test.go) al conmutar: entra en
// F8, cuando muera bridge_captacion.go. Quien entra con este commit es `catalogo` (D-R-4): su
// último importador viejo en el arranque era la caché del índice.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	flowruntime "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intakeahead"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intentcfg"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/pipeline"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/reanalisis"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo/indice"
)

// Rutas de import de la captación: los seis árboles VIEJOS (cada una es además prefijo de sus
// subpaquetes: la de la cola cubre pipeline, stages, catalogo y anclaje) y los paquetes NUEVOS que
// el arranque construye. Las viejas se componen con internalTreePath en vez de escribirse enteras
// para que el grep de la definición de hecho de F7, que busca esas rutas como texto, solo
// encuentre el adaptador y su test.
//
// ⚠️ La cola vieja se llama como solicitudes sin la «s» final (reglas de F7, T-1):
// isOldCapturePath compara la ruta entera o el prefijo con «/», así que internal/intakes —que es
// solicitudes, y tiene su propio candado— NO cuenta como captación.
const (
	oldCaptureQueueImportPath = internalTreePath + "intake"
	oldPipelineImportPath     = oldCaptureQueueImportPath + "/pipeline"
	oldStagesImportPath       = oldCaptureQueueImportPath + "/stages"
	oldCatalogIndexImportPath = oldCaptureQueueImportPath + "/catalogo"
	oldAheadImportPath        = internalTreePath + "intakeahead"
	oldReanalysisImportPath   = internalTreePath + "reanalisis"
	oldIntentCfgImportPath    = internalTreePath + "intentcfg"
	oldEvidenceImportPath     = internalTreePath + "evidence"
	oldCaseBankImportPath     = internalTreePath + "casebank"

	newCaptureQueueImportPath = internalTreePath + "modulos/captacion/intake"
	newPipelineImportPath     = internalTreePath + "modulos/captacion/pipeline"
	newStagesImportPath       = internalTreePath + "modulos/captacion/stages"
	newAheadImportPath        = internalTreePath + "modulos/captacion/intakeahead"
	newReanalysisImportPath   = internalTreePath + "modulos/captacion/reanalisis"
	newIntentCfgImportPath    = internalTreePath + "modulos/captacion/intentcfg"
	newCatalogIndexImportPath = internalTreePath + "modulos/catalogo/indice"

	// oldFlowRuntimeImportPath es el runtime VIEJO de flujos, de donde salen el compositor y el
	// agregador. No es captación y no muere en F7 (T-2): se nombra para contar sus construcciones.
	oldFlowRuntimeImportPath = internalTreePath + "flujos/runtime"
)

// captureBridgeFiles son los ÚNICOS ficheros de internal/arranque que pueden importar la captación
// vieja: el adaptador y su test de traducción. Mueren en F8.
var captureBridgeFiles = []string{"bridge_captacion.go", "bridge_captacion_test.go"}

// isOldCapturePath dice si path es un paquete viejo de captación (o un subpaquete suyo).
func isOldCapturePath(path string) bool {
	for _, old := range []string{
		oldCaptureQueueImportPath, oldAheadImportPath, oldReanalysisImportPath,
		oldIntentCfgImportPath, oldEvidenceImportPath, oldCaseBankImportPath,
	} {
		if path == old || strings.HasPrefix(path, old+"/") {
			return true
		}
	}
	return false
}

// TestCableado_OnlyTheBridgeImportsTheOldCapture (R7.2.a, definición de hecho 5 de F7; hallazgo
// 39): se barren por ruta de import TODOS los .go de internal/arranque, producción Y tests, y la
// captación vieja —la cola con sus subpaquetes (pipeline, stages, catalogo, anclaje), el adelanto
// de ventana, el re-análisis, el store de intenciones, evidence y casebank— solo aparece en el
// adaptador y en su test. Sin esto, una fase podría construir un worker, un pool o un servicio
// viejos y pasárselos a su consumidor sin tocar ningún campo del contenedor; y un test que
// importara lo viejo para comparar salidas sería un puente a escondidas. Por eso la segunda
// instancia vieja de la cola se construye DENTRO del adaptador. Falla también si la excepción ya
// no se usa: el día que muera el adaptador, sobra.
func TestCableado_OnlyTheBridgeImportsTheOldCapture(t *testing.T) {
	if isOldCapturePath(oldIntakesImportPath) || isOldCapturePath(newCaptureQueueImportPath) {
		t.Fatal("isOldCapturePath toma por captación vieja a solicitudes o a la cola nueva: el barrido mentiría (T-1)")
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("leyendo el directorio del arranque: %v", err)
	}
	fset := token.NewFileSet()
	scanned := 0
	bridgeUses := make(map[string]bool, len(captureBridgeFiles))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parseando %s: %v", name, err)
		}
		scanned++
		for path := range importsOf(t, f) {
			if !isOldCapturePath(path) {
				continue
			}
			if slices.Contains(captureBridgeFiles, name) {
				bridgeUses[name] = true
				continue
			}
			t.Errorf("%s importa %s: en el arranque nuevo solo %v pueden importar la captación vieja",
				name, path, captureBridgeFiles)
		}
	}
	if scanned == 0 {
		t.Fatal("recorridos = 0: el barrido no miró ningún fichero")
	}
	for _, name := range captureBridgeFiles {
		if !bridgeUses[name] {
			t.Errorf("%s ya no importa ningún paquete viejo de captación: si el adaptador murió, la "+
				"excepción y este test sobran", name)
		}
	}
}

// TestCableado_TheBootBuildsTheNewCapture (R7.2.a): el arranque real construye la cola, el worker,
// el pool, el servicio de re-análisis y el store de intenciones, y los cinco son los del módulo
// NUEVO; el worker lleva el aforo nuevo y el índice del catálogo nuevo. La única pieza vieja es la
// declarada: la segunda instancia de la cola (D-F7-1).
func TestCableado_TheBootBuildsTheNewCapture(t *testing.T) {
	c := contenedorDeHuella(t, "minimo")

	built := []struct {
		name    string
		got     any
		want    reflect.Type
		pkgPath string
	}{
		{"c.intakeJobStore", c.intakeJobStore, reflect.TypeFor[*intake.Postgres](), newCaptureQueueImportPath},
		{"c.intakePipeline", c.intakePipeline, reflect.TypeFor[*pipeline.Worker](), newPipelineImportPath},
		{"c.intakeAhead", c.intakeAhead, reflect.TypeFor[*intakeahead.Pool](), newAheadImportPath},
		{"c.reanalysisSvc", c.reanalysisSvc, reflect.TypeFor[*reanalisis.Service](), newReanalysisImportPath},
		{"c.intentStore", c.intentStore, reflect.TypeFor[*intentcfg.PostgresStore](), newIntentCfgImportPath},
		{"c.legacyIntakeJobs (D-F7-1)", c.legacyIntakeJobs, nil, oldCaptureQueueImportPath},
	}
	for _, b := range built {
		v := reflect.ValueOf(b.got)
		if v.Kind() != reflect.Pointer || v.IsNil() {
			t.Errorf("%s no está construido tras las fases 3, 5 y 7", b.name)
			continue
		}
		if b.want != nil && v.Type() != b.want {
			t.Errorf("%s es un %s; se espera %s", b.name, v.Type(), b.want)
		}
		if got := v.Type().Elem().PkgPath(); got != b.pkgPath {
			t.Errorf("%s es de %s; se espera %s", b.name, got, b.pkgPath)
		}
	}

	// El aforo y el índice no salen al contenedor (el aforo, a propósito: fase5_captacion.go): se
	// leen del worker que los recibió.
	capacity := field(t, c.intakePipeline, "capacity")
	if capacity.IsNil() || capacity.Type() != reflect.TypeFor[*pipeline.Capacity]() {
		t.Errorf("el aforo del worker es %s (nil = %v); se espera un *pipeline.Capacity NUEVO: sin él no hay K por Edge",
			capacity.Type(), capacity.IsNil())
	}
	catalogs := field(t, c.intakePipeline, "catalogs")
	if catalogs.IsNil() || catalogs.Elem().Type() != reflect.TypeFor[*indice.Cache]() {
		t.Errorf("el catálogo del worker no es el *indice.Cache de %s", newCatalogIndexImportPath)
	}
}

// TestCableado_OneNewConstructionOfEachCapturePiece (R7.6.b, reglas de F7 §3): en toda la
// producción del arranque hay exactamente UNA llamada a cada constructor nuevo de captación y
// NINGUNA a su gemelo viejo, con una sola excepción declarada: la cola vieja se construye
// exactamente UNA vez (la segunda instancia, D-F7-1, dentro del adaptador). Dos workers serían
// bloqueo en cabeza en la única plaza del Edge; dos aforos, dos ideas de cuántas plazas hay; dos
// pools, dos colas de clasificación; y dos compositores, dos `source_text` que divergen (T-5) —por
// eso el compositor viejo de flujos/runtime, que no tiene gemelo en F7, se cuenta también—.
func TestCableado_OneNewConstructionOfEachCapturePiece(t *testing.T) {
	constructors := []struct {
		newPath, oldPath, fn string
		oldFn                string // nombre del gemelo viejo si cambió (E-11); "" ⇒ el mismo
		oldCalls             int
	}{
		{newPipelineImportPath, oldPipelineImportPath, "NewWorker", "", 0},
		{newPipelineImportPath, oldPipelineImportPath, "NewCapacity", "NuevoAforo", 0},
		{newAheadImportPath, oldAheadImportPath, "New", "", 0},
		{newReanalysisImportPath, oldReanalysisImportPath, "NewService", "NewServicio", 0},
		{newCatalogIndexImportPath, oldCatalogIndexImportPath, "NewCache", "", 0},
		{newCaptureQueueImportPath, oldCaptureQueueImportPath, "NewPostgres", "", 1},
		{newIntentCfgImportPath, oldIntentCfgImportPath, "NewPostgresStore", "", 0},
		{newStagesImportPath, oldStagesImportPath, "NewP2", "", 0},
		{newStagesImportPath, oldStagesImportPath, "NewP3", "", 0},
		{newStagesImportPath, oldStagesImportPath, "NewP4", "", 0},
		{newStagesImportPath, oldStagesImportPath, "NewMatch", "", 0},
		{newStagesImportPath, oldStagesImportPath, "NewDraft", "", 0},
	}
	for _, k := range constructors {
		if n := callsTo(t, k.newPath, k.fn); n != 1 {
			t.Errorf("%s.%s aparece %d veces en la producción de internal/arranque; se espera 1", k.newPath, k.fn, n)
		}
		oldFn := k.fn
		if k.oldFn != "" {
			oldFn = k.oldFn
		}
		if n := callsTo(t, k.oldPath, oldFn); n != k.oldCalls {
			t.Errorf("%s.%s (VIEJO) aparece %d veces en la producción de internal/arranque; se espera %d",
				k.oldPath, oldFn, n, k.oldCalls)
		}
	}
	for _, fn := range []string{"NewSourceTextComposer", "NewIntakeAggregator"} {
		if n := callsTo(t, oldFlowRuntimeImportPath, fn); n != 1 {
			t.Errorf("flowruntime.%s aparece %d veces en la producción de internal/arranque; se espera 1", fn, n)
		}
	}
}

// TestCableado_TheCaptureBridgesWrapTheBootObjects (R7.6.b, T-3, T-5): sobre el arranque real, lo
// que reciben reanalisis.NewService y flowruntime.WithAheadRequester son los adaptadores de
// bridge_captacion.go, y envuelven LAS MISMAS instancias que el contenedor: el re-análisis compone
// con el ÚNICO compositor —el mismo que recibe el agregador— y el agregador pide por el ÚNICO
// pool, el que arranca la fase de fondo. El worker, las etapas y el re-análisis leen de la cola
// NUEVA; el agregador y el compositor escriben por la segunda instancia vieja, y solo ellos. Y el
// re-análisis lee el hilo con el MISMO límite con el que lo lee ese compositor.
func TestCableado_TheCaptureBridgesWrapTheBootObjects(t *testing.T) {
	c := contenedorDeHuella(t, "minimo")
	if c.intakeComposer == nil || c.intakeAggregator == nil {
		t.Fatal("las fases 5 y 7 no construyeron el compositor y el agregador")
	}

	composer := field(t, c.reanalysisSvc, "composer")
	if composer.IsNil() || composer.Elem().Type() != reflect.TypeFor[*composerBridge]() {
		t.Fatalf("el Composer del re-análisis no es un *composerBridge (bridge_captacion.go)")
	}
	ahead := field(t, c.intakeAggregator, "ahead")
	if ahead.IsNil() || ahead.Elem().Type() != reflect.TypeFor[*aheadBridge]() {
		t.Fatalf("el AheadRequester del agregador no es un *aheadBridge (bridge_captacion.go)")
	}

	shared := []struct {
		name string
		got  reflect.Value
		want any
	}{
		{"el compositor que envuelve composerBridge dentro del re-análisis", inner(t, composer, "composer"), c.intakeComposer},
		{"el compositor del agregador (flowruntime.WithSourceComposer)", field(t, c.intakeAggregator, "compose"), c.intakeComposer},
		{"el pool que envuelve aheadBridge dentro del agregador", inner(t, ahead, "pool"), c.intakeAhead},
		{"la cola del worker (intake.PipelineStore)", field(t, c.intakePipeline, "store"), c.intakeJobStore},
		{"la cola del re-análisis (reanalisis.Jobs)", field(t, c.reanalysisSvc, "jobs"), c.intakeJobStore},
		{"el store de artefactos de la etapa P2", inner(t, field(t, c.intakePipeline, "p2"), "store"), c.intakeJobStore},
		{"el store de artefactos de la etapa P3", inner(t, field(t, c.intakePipeline, "p3"), "store"), c.intakeJobStore},
		{"el store de artefactos de la etapa P4", inner(t, field(t, c.intakePipeline, "p4"), "store"), c.intakeJobStore},
		{"el store de artefactos de la etapa match", inner(t, field(t, c.intakePipeline, "match"), "store"), c.intakeJobStore},
		{"el store de artefactos de la etapa draft", inner(t, field(t, c.intakePipeline, "draft"), "store"), c.intakeJobStore},
		{"la cola del agregador (la instancia vieja, D-F7-1)", field(t, c.intakeAggregator, "jobs"), c.legacyIntakeJobs},
		{"la cola del compositor (la instancia vieja, D-F7-1)", field(t, c.intakeComposer, "jobs"), c.legacyIntakeJobs},
		{"el store de intenciones del pool (intakeahead.ConfigStore)", field(t, c.intakeAhead, "cfg"), c.intentStore},
		{"los derechos del re-análisis (reanalisis.Features)", field(t, c.reanalysisSvc, "features"), c.entResolver},
		{"el hilo del re-análisis (reanalisis.Thread)", field(t, c.reanalysisSvc, "thread"), c.eventStore},
	}
	for _, k := range shared {
		if !sameInstance(k.got, k.want) {
			t.Errorf("%s no es la MISMA instancia que la del contenedor (%T)", k.name, k.want)
		}
	}

	if got := field(t, c.reanalysisSvc, "threadLimit").Int(); got != flowruntime.DefaultThreadLimit {
		t.Errorf("el límite del hilo del re-análisis es %d; se espera flowruntime.DefaultThreadLimit, %d", got, flowruntime.DefaultThreadLimit)
	}
	if got, want := field(t, c.reanalysisSvc, "threadLimit").Int(), field(t, c.intakeComposer, "limit").Int(); got != want {
		t.Errorf("el re-análisis lee el hilo con límite %d y el compositor con %d: recompondrían literales distintos", got, want)
	}
	if field(t, c.intakeAhead, "sink").IsNil() {
		t.Error("el pool no tiene sink: las clasificaciones no volverían al agregador")
	}
}

// TestCableado_ThePoolAnswersThroughTheBridgeSink (T-3): el sink del pool es una clausura y por
// reflexión no se puede ver a quién llama, así que se afirma por AST: el cuarto argumento de la
// ÚNICA llamada a intakeahead.New es classifiedSink(c), la clausura diferida del adaptador, que es
// quien convierte la clave nueva en la del agregador viejo. Y el agregador pide por un
// &aheadBridge{pool: c.intakeAhead}, no por el pool a secas ni por otro.
func TestCableado_ThePoolAnswersThroughTheBridgeSink(t *testing.T) {
	fset, files := astDelArranque(t)
	seen := map[string]int{}
	for _, f := range files {
		imports := importsOf(t, f)
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			for _, k := range captureBridgeCalls {
				local, imported := imports[k.importPath]
				if !imported || !esLlamada(call, local, k.fn) {
					continue
				}
				seen[k.fn]++
				for _, problem := range k.check(call) {
					t.Errorf("%s: %s", fset.Position(call.Pos()), problem)
				}
			}
			return true
		})
	}
	for _, k := range captureBridgeCalls {
		if seen[k.fn] != 1 {
			t.Errorf("%s aparece %d veces en la producción de internal/arranque; se espera 1", k.fn, seen[k.fn])
		}
	}
}

// captureBridgeCalls son las tres llamadas de producción por las que pasa un adaptador de
// bridge_captacion.go, con lo que cada una tiene que recibir. check devuelve lo que esté mal.
var captureBridgeCalls = []struct {
	importPath, fn string
	check          func(call *ast.CallExpr) []string
}{
	{newAheadImportPath, "New", func(call *ast.CallExpr) []string {
		sink, ok := argAt(call, 3).(*ast.CallExpr)
		if ok && campoCompletoDe(sink.Fun) == "classifiedSink" && len(sink.Args) == 1 && campoCompletoDe(sink.Args[0]) == "c" {
			return nil
		}
		return []string{"el sink de intakeahead.New no es classifiedSink(c): la clasificación no llegaría al agregador por el adaptador"}
	}},
	{oldFlowRuntimeImportPath, "WithAheadRequester", func(call *ast.CallExpr) []string {
		if got := bridgeLiteralField(argAt(call, 0), "aheadBridge", "pool"); got != "c.intakeAhead" {
			return []string{"flowruntime.WithAheadRequester no recibe &aheadBridge{pool: c.intakeAhead} (pool = \"" + got + "\")"}
		}
		return nil
	}},
	{newReanalysisImportPath, "NewService", func(call *ast.CallExpr) []string {
		var problems []string
		if got := bridgeLiteralField(argAt(call, 4), "composerBridge", "composer"); got != "c.intakeComposer" {
			problems = append(problems, "reanalisis.NewService no recibe &composerBridge{composer: c.intakeComposer} (composer = \""+got+"\")")
		}
		if got := campoCompletoDe(argAt(call, 7)); got != "legacyThreadLimit" {
			problems = append(problems, "el límite del hilo de reanalisis.NewService es \""+got+"\"; se espera legacyThreadLimit, derivado del compositor viejo y no un literal")
		}
		return problems
	}},
}

// argAt devuelve el argumento i de call, o nil si no lo tiene.
func argAt(call *ast.CallExpr, i int) ast.Expr {
	if i >= len(call.Args) {
		return nil
	}
	return call.Args[i]
}

// bridgeLiteralField lee, de una expresión `&<typeName>{<key>: <valor>}`, el valor de key como
// texto (`c.intakeAhead`). Devuelve "" si la expresión no tiene esa forma.
func bridgeLiteralField(e ast.Expr, typeName, key string) string {
	addr, ok := e.(*ast.UnaryExpr)
	if !ok || addr.Op != token.AND {
		return ""
	}
	lit, ok := addr.X.(*ast.CompositeLit)
	if !ok || campoCompletoDe(lit.Type) != typeName {
		return ""
	}
	for _, elt := range lit.Elts {
		if kv, ok := elt.(*ast.KeyValueExpr); ok && campoCompletoDe(kv.Key) == key {
			return campoCompletoDe(kv.Value)
		}
	}
	return ""
}

// TestIdentidad_TheNewFaceServesCaptureWithTheContainerObjects (FX TX.21, R7.7.a): las dos áreas
// de captación de la cara NUEVA (H1 y E1–E2 del mapa) reciben LAS MISMAS instancias que el
// contenedor: el único servicio de re-análisis, el store de intenciones del que también lee el
// pool, el único resolver de derechos y el único gateway. Y las dos costuras conservan el nil de
// verdad: la cara decide «no monto H1» y «no empujo» comparando con nil, y un puntero nil dentro
// de la interfaz no lo es. Que la cara VIEJA ya no reciba ninguno de los tres lo afirma
// TestCableado_TheOldFaceKeepsNothingOfRequestsNorCapture.
func TestIdentidad_TheNewFaceServesCaptureWithTheContainerObjects(t *testing.T) {
	c := contenedorDeHuella(t, "minimo")

	face := captureDepsOfTheNewFace(c)
	shared := []struct {
		name string
		got  any
		want any
	}{
		{"el Reanalysis de H1 en la cara nueva", face.reanalyze.Reanalysis, c.reanalysisSvc},
		{"el Intents de E1–E2 en la cara nueva", face.intents.Intents, c.intentStore},
		{"el Entitlements de E2 en la cara nueva", face.intents.Entitlements, c.entResolver},
		{"el ConfigPush de E2 en la cara nueva", face.intents.ConfigPush, c.gw},
	}
	for _, k := range shared {
		if !sameInstance(reflect.ValueOf(k.got), k.want) {
			t.Errorf("%s no es la MISMA instancia que la del contenedor (%T)", k.name, k.want)
		}
	}
	if face.intents.DBTimeout != c.cfg.PublicAPIDBTimeout {
		t.Errorf("el DBTimeout de E1 es %s; se espera el de config, %s", face.intents.DBTimeout, c.cfg.PublicAPIDBTimeout)
	}

	if got := reanalysisServicePort(nil); got != nil {
		t.Errorf("reanalysisServicePort(nil) = %T; se espera un nil de verdad: H1 se montaría sobre un receptor nil", got)
	}
	if got := configPusherPort(nil); got != nil {
		t.Errorf("configPusherPort(nil) = %T; se espera un nil de verdad: E2 empujaría sobre un receptor nil", got)
	}
}
