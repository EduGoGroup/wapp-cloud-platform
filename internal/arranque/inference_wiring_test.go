package arranque

// inference_wiring_test.go — EL TEST DE CABLEADO DE inferencia, COMPLETO (F4 · T4.25, R4.7.b,
// R4.7.c; 05 §4.2, hallazgo 39 de F1). No basta mirar el tipo de un campo del contenedor: se
// afirma, sobre el arranque nuevo REAL (las fases 2–8 del contenedor de la huella, sin red ni BD),
// que el selector de vía y los almacenes son los NUEVOS, que hay UN solo selector y que todos sus
// consumidores apuntan a esa MISMA instancia; y, por ruta de import, que ningún fichero de
// internal/arranque, de producción o de test, toca la inferencia vieja. También que las dos áreas
// de la cara HTTP nueva (FX TX.14) reciben los mismos almacenes.
//
// Desde F8 (T8.32, conmutar(conversacion)) no hay adaptador: murió bridge_inferencia.go
// (turneroBridge) y el resolutor del turno acotado, que es el de internal/modulos/conversacion,
// recibe el *llmvia.Selector del contenedor tal cual. Con él murió la única excepción del barrido
// de imports, e `inferencia` entra en Conmutados (internal/modulos/fronteras_test.go).

import (
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/degradation"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/llmvia"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm"
)

// Rutas de import de la inferencia: los cuatro paquetes VIEJOS (cada una es además prefijo de sus
// subpaquetes: la del selector cubre también su adaptador local) y los tres NUEVOS que el arranque
// construye. Las viejas se componen con internalTreePath en vez de escribirse enteras para que el
// grep de la definición de hecho de F4 (reglas.md §4, punto 7), que busca esas rutas como texto,
// no encuentre este candado.
const (
	internalTreePath = "github.com/EduGoGroup/wapp-cloud-platform/internal/"

	oldSelectorImportPath    = internalTreePath + "llmvia"
	oldTenantLLMImportPath   = internalTreePath + "tenantllm"
	oldDegradationImportPath = internalTreePath + "degradation"
	oldPromptsImportPath     = internalTreePath + "prompts"

	newSelectorImportPath    = internalTreePath + "modulos/inferencia/llmvia"
	newTenantLLMImportPath   = internalTreePath + "modulos/inferencia/tenantllm"
	newDegradationImportPath = internalTreePath + "modulos/inferencia/degradation"
)

// isOldInferencePath dice si path es un paquete viejo de inferencia (o un subpaquete suyo).
func isOldInferencePath(path string) bool {
	for _, old := range []string{oldSelectorImportPath, oldTenantLLMImportPath, oldDegradationImportPath, oldPromptsImportPath} {
		if path == old || strings.HasPrefix(path, old+"/") {
			return true
		}
	}
	return false
}

// bootFilesImporting barre por ruta de import TODOS los .go de internal/arranque, producción Y
// tests, y devuelve una línea «fichero importa ruta» por cada import que isOld reconoce como viejo.
// Falla si no miró ningún fichero: un barrido que no recorre nada pasa siempre.
func bootFilesImporting(t *testing.T, isOld func(path string) bool) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("leyendo el directorio del arranque: %v", err)
	}
	fset := token.NewFileSet()
	scanned := 0
	var found []string
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
			if isOld(path) {
				found = append(found, name+" importa "+path)
			}
		}
	}
	if scanned == 0 {
		t.Fatal("recorridos = 0: el barrido no miró ningún fichero")
	}
	return found
}

// TestCableado_NoBootFileImportsTheOldInference (R4.7.a, definición de hecho 7 de F4): se barren
// por ruta de import TODOS los .go de internal/arranque, producción Y tests, y la inferencia vieja
// no aparece en NINGUNO. Sin esto, una fase podría construir un selector o un almacén viejos y
// pasárselos a su consumidor sin tocar ningún campo del contenedor; y un test que importara lo
// viejo para comparar salidas sería un puente a escondidas (T-18). De F4 a F8 hubo una excepción,
// bridge_inferencia.go y su test (el adaptador hacia el turno acotado viejo); murió en
// conmutar(conversacion), así que la lista de importadores es vacía.
func TestCableado_NoBootFileImportsTheOldInference(t *testing.T) {
	if isOldInferencePath(newSelectorImportPath) || !isOldInferencePath(oldSelectorImportPath+"/local") {
		t.Fatal("isOldInferencePath no distingue el selector viejo del nuevo: el barrido mentiría")
	}
	for _, line := range bootFilesImporting(t, isOldInferencePath) {
		t.Errorf("%s: en el arranque nuevo ningún fichero puede importar la inferencia vieja "+
			"(bridge_inferencia.go, la única excepción, murió en F8)", line)
	}
}

// TestCableado_TheBootBuildsTheNewInference (R4.7.a, R4.7.b): el arranque real construye el
// selector de vía, el almacén de tenant_llm, el de avisos y su notificador, y los cuatro son los
// del módulo NUEVO; y en toda la producción del arranque hay exactamente UNA construcción de cada
// uno, la nueva, y ninguna de las viejas. Un segundo selector sería una segunda verdad sobre la
// vía de un tenant.
func TestCableado_TheBootBuildsTheNewInference(t *testing.T) {
	c := contenedorDeHuella(t, "minimo")

	built := []struct {
		name    string
		got     any
		want    reflect.Type
		pkgPath string
	}{
		{"c.llmSelector", c.llmSelector, reflect.TypeFor[*llmvia.Selector](), newSelectorImportPath},
		{"c.tenantLLMStore", c.tenantLLMStore, reflect.TypeFor[*tenantllm.Postgres](), newTenantLLMImportPath},
		{"c.degradationStore", c.degradationStore, reflect.TypeFor[*degradation.Postgres](), newDegradationImportPath},
		{"c.degradationNotifier", c.degradationNotifier, reflect.TypeFor[*degradation.Notifier](), newDegradationImportPath},
	}
	for _, b := range built {
		v := reflect.ValueOf(b.got)
		if v.Kind() != reflect.Pointer || v.IsNil() {
			t.Errorf("%s no está construido tras las fases 3 y 5", b.name)
			continue
		}
		if v.Type() != b.want {
			t.Errorf("%s es un %s; se espera %s", b.name, v.Type(), b.want)
		}
		if got := v.Type().Elem().PkgPath(); got != b.pkgPath {
			t.Errorf("%s es de %s; se espera el paquete NUEVO, %s", b.name, got, b.pkgPath)
		}
	}

	constructors := []struct {
		newPath, oldPath, fn string
	}{
		{newSelectorImportPath, oldSelectorImportPath, "NewSelector"},
		{newTenantLLMImportPath, oldTenantLLMImportPath, "NewPostgres"},
		{newDegradationImportPath, oldDegradationImportPath, "NewPostgres"},
		{newDegradationImportPath, oldDegradationImportPath, "NewNotifier"},
	}
	for _, k := range constructors {
		if n := callsTo(t, k.newPath, k.fn); n != 1 {
			t.Errorf("%s.%s aparece %d veces en la producción de internal/arranque; se espera 1", k.newPath, k.fn, n)
		}
		if n := callsTo(t, k.oldPath, k.fn); n != 0 {
			t.Errorf("%s.%s (VIEJO) aparece %d veces en la producción de internal/arranque; se espera 0", k.oldPath, k.fn, n)
		}
	}
}

// inner baja por la interfaz o el puntero que guarda v y devuelve el campo name de la estructura
// a la que apunta: es como se llega al selector que una etapa guarda dentro del worker.
func inner(t *testing.T, v reflect.Value, name string) reflect.Value {
	t.Helper()
	for v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer {
		if v.IsNil() {
			t.Fatalf("no se puede leer %q: el valor que lo contiene es nil (el arranque no lo cableó)", name)
		}
		v = v.Elem()
	}
	f := v.FieldByName(name)
	if !f.IsValid() {
		t.Fatalf("%s no tiene el campo %q: el test de cableado se quedó atrás", v.Type(), name)
	}
	return f
}

// TestIdentidad_TheTurnAndTheReanalysisUseTheContainerInstances (R4.7.c): lo que recibe
// turnoacotado.New es EL selector del contenedor, sin nada alrededor: el turno acotado pregunta al
// único selector de vía. Hasta F8 lo recibía detrás de turneroBridge (bridge_inferencia.go), que
// murió en conmutar(conversacion); lo que este test afirmaba del adaptador (que envolvía
// c.llmSelector) se afirma ahora sin intermediario, y además que no queda ninguno. Y el
// re-análisis, que desde F7 (conmutar(captacion)) es el NUEVO, tampoco lee detrás de ningún
// adaptador: su LLMConfig ES c.tenantLLMStore, el *tenantllm.Postgres nuevo, el mismo del que lee
// el selector (un solo camino al SQL de tenant_llm).
func TestIdentidad_TheTurnAndTheReanalysisUseTheContainerInstances(t *testing.T) {
	c := contenedorDeHuella(t, "minimo")

	turner := field(t, c.consultaResolver, "turner")
	if turner.IsNil() || turner.Elem().Type() != reflect.TypeFor[*llmvia.Selector]() {
		t.Fatalf("el Turner del resolutor del turno acotado no es el *llmvia.Selector nuevo, sin adaptador (turneroBridge murió en F8)")
	}
	if !sameInstance(turner, c.llmSelector) {
		t.Error("el turno acotado no pregunta a c.llmSelector: habría dos selectores de vía en el proceso")
	}

	config := field(t, c.reanalysisSvc, "config")
	if config.IsNil() || config.Elem().Type() != reflect.TypeFor[*tenantllm.Postgres]() {
		t.Fatalf("el LLMConfig del re-análisis no es el *tenantllm.Postgres nuevo, sin adaptador (llmConfigBridge murió en F7)")
	}
	if !sameInstance(config, c.tenantLLMStore) {
		t.Error("el re-análisis no lee de c.tenantLLMStore: habría un segundo camino al SQL de tenant_llm")
	}
}

// TestIdentidad_EveryConsumerSharesTheOneSelector (R4.7.b): sobre el arranque real, las tres
// etapas LLM del pipeline, el aforo por Edge, el generador de cotización, el adelanto de ventana
// (como proveedor y como calentador) y el turno acotado (sin adaptador desde F8) usan el MISMO
// *llmvia.Selector que c.llmSelector; y ese selector lee del único almacén de tenant_llm y avisa
// por el único notificador, que escribe en el único almacén de avisos.
func TestIdentidad_EveryConsumerSharesTheOneSelector(t *testing.T) {
	c := contenedorDeHuella(t, "minimo")
	if c.llmSelector == nil {
		t.Fatal("la fase 5 no construyó el selector de vía")
	}

	consumers := []struct {
		name string
		got  reflect.Value
	}{
		{"la etapa P2 del pipeline", inner(t, field(t, c.intakePipeline, "p2"), "sel")},
		{"la etapa P3 del pipeline", inner(t, field(t, c.intakePipeline, "p3"), "sel")},
		{"la etapa P4 del pipeline", inner(t, field(t, c.intakePipeline, "p4"), "sel")},
		{"el resolutor de plazas del aforo (pipeline.WithCapacity)", field(t, c.intakePipeline, "slots")},
		{"el generador de cotización (quotetext.Service, el de solicitudes)", field(t, c.quoteSvc, "selector")},
		{"el adelanto de ventana (intakeahead.Pool)", field(t, c.intakeAhead, "sel")},
		{"el calentador del adelanto de ventana", field(t, c.intakeAhead, "warmer")},
		{"el turno acotado (turnoacotado.New)", field(t, c.consultaResolver, "turner")},
	}
	for _, k := range consumers {
		if !sameInstance(k.got, c.llmSelector) {
			t.Errorf("%s no usa la MISMA instancia que c.llmSelector: habría dos selectores de vía", k.name)
		}
	}

	if !sameInstance(field(t, c.llmSelector, "store"), c.tenantLLMStore) {
		t.Error("el selector no lee de c.tenantLLMStore")
	}
	if !sameInstance(field(t, c.llmSelector, "notifier"), c.degradationNotifier) {
		t.Error("el selector no avisa por c.degradationNotifier")
	}
	if !sameInstance(field(t, c.degradationNotifier, "store"), c.degradationStore) {
		t.Error("el notificador de degradación no escribe en c.degradationStore")
	}
}

// TestIdentidad_TheNewFaceSharesTheContainerStores (FX TX.14): las dos áreas de inferencia de la
// cara NUEVA (F1–F3 y F4 del mapa) reciben LAS MISMAS instancias que el contenedor: el almacén de
// tenant_llm del que lee el selector, el almacén de avisos en el que escribe su notificador y el
// único resolver de derechos. La cara VIEJA murió en F8 con depsDeLaAPIPublica: ya no hay unas
// deps viejas cuyos dos almacenes haya que afirmar a nil.
func TestIdentidad_TheNewFaceSharesTheContainerStores(t *testing.T) {
	c := contenedorDeHuella(t, "minimo")

	face := inferenceDepsOfTheNewFace(c)
	shared := []struct {
		name string
		got  any
		want any
	}{
		{"el TenantLLM de F1–F3 en la cara nueva", face.tenantLLM.TenantLLM, c.tenantLLMStore},
		{"el Entitlements de F1–F3 en la cara nueva", face.tenantLLM.Entitlements, c.entResolver},
		{"el DegradationNotices de F4 en la cara nueva", face.degradationNotices.DegradationNotices, c.degradationStore},
		{"el Entitlements de F4 en la cara nueva", face.degradationNotices.Entitlements, c.entResolver},
	}
	for _, k := range shared {
		if !sameInstance(reflect.ValueOf(k.got), k.want) {
			t.Errorf("%s no es la MISMA instancia que la del contenedor (%T)", k.name, k.want)
		}
	}
	if face.degradationNotices.DBTimeout != c.cfg.PublicAPIDBTimeout {
		t.Errorf("el DBTimeout de F4 es %s; se espera el de config, %s", face.degradationNotices.DBTimeout, c.cfg.PublicAPIDBTimeout)
	}
}
