package arranque

// captacion_cableado_test.go — EL TEST DE CABLEADO DE captación, COMPLETO (F7 · T7.23–T7.24,
// R7.2.a, R7.6.b, R7.7.a; arquitectura.md §6 de F7; 05 §4.2, hallazgo 39 de F1). No basta mirar el
// tipo de un campo del contenedor: se afirma, sobre el arranque nuevo REAL (las fases 2–8 del
// contenedor de la huella, sin red ni BD), que la cola, el worker con su aforo y su índice, el
// pool de clasificaciones, el servicio de re-análisis y el store de intenciones son los de
// internal/modulos/captacion (y el índice, el de internal/modulos/catalogo); que de cada uno hay
// UNA construcción, la nueva; que cada consumidor recibe LOS MISMOS objetos del contenedor —un
// solo compositor, un solo pool, una sola cola— y que la cara HTTP nueva sirve H1, E1 y E2 con
// ellos; y, por ruta de import, que ningún fichero de internal/arranque, de producción o de test,
// toca la captación vieja.
//
// Desde F8 (T8.32, conmutar(conversacion)) no hay adaptadores: murió bridge_captacion.go
// (composerBridge, aheadBridge, classifiedSink y la segunda instancia vieja de la cola, D-F7-1). El
// compositor y el agregador son los de internal/modulos/conversacion/runtime y nombran el MISMO
// intake.WindowKey que el pool, así que todo va directo; `captacion` entra en Conmutados
// (internal/modulos/fronteras_test.go).

import (
	"go/ast"
	"reflect"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intakeahead"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intentcfg"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/pipeline"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/reanalisis"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo/indice"
	flowruntime "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
)

// Rutas de import de la captación: los seis árboles VIEJOS (cada una es además prefijo de sus
// subpaquetes: la de la cola cubre pipeline, stages, catalogo y anclaje) y los paquetes NUEVOS que
// el arranque construye. Las viejas se componen con internalTreePath en vez de escribirse enteras
// para que el grep de la definición de hecho de F7, que busca esas rutas como texto, no encuentre
// este candado.
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

	// El runtime de flujos, de donde salen el compositor y el agregador: el VIEJO, que no era
	// captación y murió para el arranque en F8 (se nombra para contar que nadie lo construye), y el
	// NUEVO, el de conversación.
	oldFlowRuntimeImportPath = internalTreePath + "flujos/runtime"
	newFlowRuntimeImportPath = internalTreePath + "modulos/conversacion/runtime"
)

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

// TestCableado_NoBootFileImportsTheOldCapture (R7.2.a, definición de hecho 5 de F7; hallazgo 39):
// se barren por ruta de import TODOS los .go de internal/arranque, producción Y tests, y la
// captación vieja —la cola con sus subpaquetes (pipeline, stages, catalogo, anclaje), el adelanto
// de ventana, el re-análisis, el store de intenciones, evidence y casebank— no aparece en NINGUNO.
// Sin esto, una fase podría construir un worker, un pool o un servicio viejos y pasárselos a su
// consumidor sin tocar ningún campo del contenedor; y un test que importara lo viejo para comparar
// salidas sería un puente a escondidas. De F7 a F8 hubo una excepción, bridge_captacion.go y su
// test (donde se construía la segunda instancia vieja de la cola); murió en
// conmutar(conversacion), así que la lista de importadores es vacía.
func TestCableado_NoBootFileImportsTheOldCapture(t *testing.T) {
	if isOldCapturePath(oldIntakesImportPath) || isOldCapturePath(newCaptureQueueImportPath) {
		t.Fatal("isOldCapturePath toma por captación vieja a solicitudes o a la cola nueva: el barrido mentiría (T-1)")
	}
	if !isOldCapturePath(oldPipelineImportPath) {
		t.Fatal("isOldCapturePath no reconoce el pipeline viejo: el barrido no vería nada")
	}
	for _, line := range bootFilesImporting(t, isOldCapturePath) {
		t.Errorf("%s: en el arranque nuevo ningún fichero puede importar la captación vieja "+
			"(bridge_captacion.go, la única excepción, murió en F8)", line)
	}
}

// TestCableado_TheBootBuildsTheNewCapture (R7.2.a): el arranque real construye la cola, el worker,
// el pool, el servicio de re-análisis y el store de intenciones, y los cinco son los del módulo
// NUEVO; el worker lleva el aforo nuevo y el índice del catálogo nuevo. Ya no hay pieza vieja: la
// segunda instancia de la cola (D-F7-1, el campo legacyIntakeJobs) murió en F8, y el compositor y
// el agregador —que eran sus dos únicos lectores— son los de internal/modulos/conversacion.
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
		{"c.intakeComposer", c.intakeComposer, reflect.TypeFor[*flowruntime.SourceTextComposer](), newFlowRuntimeImportPath},
		{"c.intakeAggregator", c.intakeAggregator, reflect.TypeFor[*flowruntime.IntakeAggregator](), newFlowRuntimeImportPath},
	}
	for _, b := range built {
		v := reflect.ValueOf(b.got)
		if v.Kind() != reflect.Pointer || v.IsNil() {
			t.Errorf("%s no está construido tras las fases 3, 5 y 7", b.name)
			continue
		}
		if v.Type() != b.want {
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
// NINGUNA a su gemelo viejo, ya sin excepción: la cola vieja se construía UNA vez de F7 a F8 (la
// segunda instancia, D-F7-1, dentro del adaptador) y ahora ninguna. Dos workers serían bloqueo en
// cabeza en la única plaza del Edge; dos aforos, dos ideas de cuántas plazas hay; dos pools, dos
// colas de clasificación; y dos compositores, dos `source_text` que divergen (T-5) —por eso el
// compositor y el agregador, que desde F8 son los de conversacion/runtime, se cuentan también:
// UNO de cada uno nuevo y ninguno viejo—.
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
		{newCaptureQueueImportPath, oldCaptureQueueImportPath, "NewPostgres", "", 0},
		{newIntentCfgImportPath, oldIntentCfgImportPath, "NewPostgresStore", "", 0},
		{newStagesImportPath, oldStagesImportPath, "NewP2", "", 0},
		{newStagesImportPath, oldStagesImportPath, "NewP3", "", 0},
		{newStagesImportPath, oldStagesImportPath, "NewP4", "", 0},
		{newStagesImportPath, oldStagesImportPath, "NewMatch", "", 0},
		{newStagesImportPath, oldStagesImportPath, "NewDraft", "", 0},
		{newFlowRuntimeImportPath, oldFlowRuntimeImportPath, "NewSourceTextComposer", "", 0},
		{newFlowRuntimeImportPath, oldFlowRuntimeImportPath, "NewIntakeAggregator", "", 0},
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
}

// TestCableado_TheCaptureConsumersShareTheBootObjects (R7.6.b, T-3, T-5): sobre el arranque real,
// reanalisis.NewService y flowruntime.WithAheadRequester reciben LAS MISMAS instancias que el
// contenedor, sin nada alrededor: el re-análisis compone con el ÚNICO compositor —el mismo que
// recibe el agregador— y el agregador pide por el ÚNICO pool, el que arranca la fase de fondo.
// Hasta F8 los dos pasaban por composerBridge y aheadBridge (bridge_captacion.go), que murieron
// en conmutar(conversacion); lo que este test afirmaba de ellos (que envolvían c.intakeComposer y
// c.intakeAhead) se afirma ahora sin intermediario, y además que no queda ninguno. El worker, las
// etapas, el re-análisis, el agregador y el compositor leen y escriben por LA cola,
// c.intakeJobStore: la segunda instancia vieja (D-F7-1) murió. Y el re-análisis lee el hilo con el
// MISMO límite con el que lo lee ese compositor.
func TestCableado_TheCaptureConsumersShareTheBootObjects(t *testing.T) {
	c := contenedorDeHuella(t, "minimo")
	if c.intakeComposer == nil || c.intakeAggregator == nil {
		t.Fatal("las fases 5 y 7 no construyeron el compositor y el agregador")
	}

	composer := field(t, c.reanalysisSvc, "composer")
	if composer.IsNil() || composer.Elem().Type() != reflect.TypeFor[*flowruntime.SourceTextComposer]() {
		t.Fatalf("el Composer del re-análisis no es el *flowruntime.SourceTextComposer nuevo, sin adaptador (composerBridge murió en F8)")
	}
	ahead := field(t, c.intakeAggregator, "ahead")
	if ahead.IsNil() || ahead.Elem().Type() != reflect.TypeFor[*intakeahead.Pool]() {
		t.Fatalf("el AheadRequester del agregador no es el *intakeahead.Pool nuevo, sin adaptador (aheadBridge murió en F8)")
	}

	shared := []struct {
		name string
		got  reflect.Value
		want any
	}{
		{"el compositor del re-análisis (reanalisis.Composer)", composer, c.intakeComposer},
		{"el compositor del agregador (flowruntime.WithSourceComposer)", field(t, c.intakeAggregator, "compose"), c.intakeComposer},
		{"el pool por el que pide el agregador (flowruntime.WithAheadRequester)", ahead, c.intakeAhead},
		{"la cola del worker (intake.PipelineStore)", field(t, c.intakePipeline, "store"), c.intakeJobStore},
		{"la cola del re-análisis (reanalisis.Jobs)", field(t, c.reanalysisSvc, "jobs"), c.intakeJobStore},
		{"el store de artefactos de la etapa P2", inner(t, field(t, c.intakePipeline, "p2"), "store"), c.intakeJobStore},
		{"el store de artefactos de la etapa P3", inner(t, field(t, c.intakePipeline, "p3"), "store"), c.intakeJobStore},
		{"el store de artefactos de la etapa P4", inner(t, field(t, c.intakePipeline, "p4"), "store"), c.intakeJobStore},
		{"el store de artefactos de la etapa match", inner(t, field(t, c.intakePipeline, "match"), "store"), c.intakeJobStore},
		{"el store de artefactos de la etapa draft", inner(t, field(t, c.intakePipeline, "draft"), "store"), c.intakeJobStore},
		{"la cola del agregador (intake.JobStore)", field(t, c.intakeAggregator, "jobs"), c.intakeJobStore},
		{"el hilo del compositor (flowruntime.ThreadReader)", field(t, c.intakeComposer, "thread"), c.eventStore},
		{"los derechos del agregador (entitlements.Resolver)", field(t, c.intakeAggregator, "ents"), c.entResolver},
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

// TestCableado_ThePoolAnswersToTheAggregator (T-3): el sink del pool es una clausura y por
// reflexión no se puede ver a quién llama, así que se afirma por AST: el cuarto argumento de la
// ÚNICA llamada a intakeahead.New es intakeahead.SinkFunc(<clausura>), y esa clausura llama a
// c.intakeAggregator.OnClassified —diferida: el agregador se construye después—. Hasta F8 era
// classifiedSink(c), la clausura del adaptador que convertía la clave nueva en la del agregador
// viejo; murió con él. Y el agregador pide por c.intakeAhead a secas, y el re-análisis compone con
// c.intakeComposer y lee el hilo con flowruntime.DefaultThreadLimit, la constante del compositor,
// no con un literal.
func TestCableado_ThePoolAnswersToTheAggregator(t *testing.T) {
	fset, files := astDelArranque(t)
	seen := map[string]int{}
	for _, f := range files {
		imports := importsOf(t, f)
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			for _, k := range captureDirectCalls {
				local, imported := imports[k.importPath]
				if !imported || !esLlamada(call, local, k.fn) {
					continue
				}
				seen[k.fn]++
				for _, problem := range k.check(call, imports) {
					t.Errorf("%s: %s", fset.Position(call.Pos()), problem)
				}
			}
			return true
		})
	}
	for _, k := range captureDirectCalls {
		if seen[k.fn] != 1 {
			t.Errorf("%s aparece %d veces en la producción de internal/arranque; se espera 1", k.fn, seen[k.fn])
		}
	}
}

// captureDirectCalls son las tres llamadas de producción por las que hasta F8 pasaba un adaptador
// de bridge_captacion.go, con lo que cada una tiene que recibir ahora, sin él. check recibe además
// los imports del fichero (ruta → nombre local) y devuelve lo que esté mal.
var captureDirectCalls = []struct {
	importPath, fn string
	check          func(call *ast.CallExpr, imports map[string]string) []string
}{
	{newAheadImportPath, "New", func(call *ast.CallExpr, imports map[string]string) []string {
		sink, ok := argAt(call, 3).(*ast.CallExpr)
		if !ok || !esLlamada(sink, imports[newAheadImportPath], "SinkFunc") || len(sink.Args) != 1 {
			return []string{"el sink de intakeahead.New no es intakeahead.SinkFunc(<clausura>)"}
		}
		closure, ok := sink.Args[0].(*ast.FuncLit)
		if !ok || !callsMethod(closure, "c.intakeAggregator.OnClassified") {
			return []string{"la clausura del sink de intakeahead.New no llama a c.intakeAggregator.OnClassified: la clasificación no llegaría al agregador"}
		}
		return nil
	}},
	{newFlowRuntimeImportPath, "WithAheadRequester", func(call *ast.CallExpr, _ map[string]string) []string {
		if got := campoCompletoDe(argAt(call, 0)); len(call.Args) != 1 || got != "c.intakeAhead" {
			return []string{"flowruntime.WithAheadRequester no recibe c.intakeAhead a secas (recibe \"" + got + "\")"}
		}
		return nil
	}},
	{newReanalysisImportPath, "NewService", func(call *ast.CallExpr, imports map[string]string) []string {
		var problems []string
		if got := campoCompletoDe(argAt(call, 4)); got != "c.intakeComposer" {
			problems = append(problems, "reanalisis.NewService no recibe c.intakeComposer a secas (recibe \""+got+"\")")
		}
		want := imports[newFlowRuntimeImportPath] + ".DefaultThreadLimit"
		if got := campoCompletoDe(argAt(call, 7)); got != want {
			problems = append(problems, "el límite del hilo de reanalisis.NewService es \""+got+"\"; se espera "+want+", la constante del compositor nuevo y no un literal")
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

// callsMethod dice si dentro de fn hay una llamada cuyo destino, escrito entero, es target
// (`c.intakeAggregator.OnClassified`).
func callsMethod(fn *ast.FuncLit, target string) bool {
	found := false
	ast.Inspect(fn, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && campoCompletoDe(call.Fun) == target {
			found = true
		}
		return !found
	})
	return found
}

// TestIdentidad_TheNewFaceServesCaptureWithTheContainerObjects (FX TX.21, R7.7.a): las dos áreas
// de captación de la cara NUEVA (H1 y E1–E2 del mapa) reciben LAS MISMAS instancias que el
// contenedor: el único servicio de re-análisis, el store de intenciones del que también lee el
// pool, el único resolver de derechos y el único gateway. Y las dos costuras conservan el nil de
// verdad: la cara decide «no monto H1» y «no empujo» comparando con nil, y un puntero nil dentro
// de la interfaz no lo es. La cara VIEJA murió en F8: que las tres rutas las resuelve la nueva en
// el compuesto real lo afirma TestCableado_TheNewFaceResolvesRequestsAndCapture.
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
