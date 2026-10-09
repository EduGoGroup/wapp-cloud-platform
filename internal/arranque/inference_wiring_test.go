package arranque

// inference_wiring_test.go — EL TEST DE CABLEADO DE inferencia, COMPLETO (F4 · T4.25, R4.7.b,
// R4.7.c; 05 §4.2, hallazgo 39 de F1). Es la segunda mitad obligatoria del test de
// bridge_inferencia.go (la primera, la traducción, vive en bridge_inferencia_test.go; se parten
// por tamaño, E-13). No basta mirar el tipo de un campo del contenedor: se afirma, sobre el
// arranque nuevo REAL (las fases 2–8 del contenedor de la huella, sin red ni BD), que el selector
// de vía y los almacenes son los NUEVOS, que hay UN solo selector y que todos sus consumidores
// —también el que lo recibe detrás de un adaptador— apuntan a esa MISMA instancia; y, por
// ruta de import, que ningún fichero de internal/arranque, de producción o de test, toca la
// inferencia vieja fuera del adaptador. También que las dos áreas de la cara HTTP nueva (FX TX.14)
// reciben los mismos almacenes.
//
// `inferencia` NO entra en Conmutados (internal/modulos/fronteras_test.go) mientras viva
// bridge_inferencia.go: entra en F8, cuando muera turneroBridge.

import (
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"slices"
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
// solo encuentre el adaptador y su test.
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

// inferenceBridgeFiles son los ÚNICOS ficheros de internal/arranque que pueden importar la
// inferencia vieja: el adaptador y su test de traducción (reglas.md T-18). Mueren en F8.
var inferenceBridgeFiles = []string{"bridge_inferencia.go", "bridge_inferencia_test.go"}

// isOldInferencePath dice si path es un paquete viejo de inferencia (o un subpaquete suyo).
func isOldInferencePath(path string) bool {
	for _, old := range []string{oldSelectorImportPath, oldTenantLLMImportPath, oldDegradationImportPath, oldPromptsImportPath} {
		if path == old || strings.HasPrefix(path, old+"/") {
			return true
		}
	}
	return false
}

// TestCableado_OnlyTheBridgeImportsTheOldInference (R4.7.a, definición de hecho 7 de F4): se
// barren por ruta de import TODOS los .go de internal/arranque, producción Y tests, y la
// inferencia vieja solo aparece en el adaptador y en su test. Sin esto, una fase podría construir
// un selector o un almacén viejos y pasárselos a su consumidor sin tocar ningún campo del
// contenedor; y un test que importara lo viejo para comparar salidas sería un puente a escondidas
// (T-18). Falla también si la excepción ya no se usa: el día que muera el adaptador, sobra.
func TestCableado_OnlyTheBridgeImportsTheOldInference(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("leyendo el directorio del arranque: %v", err)
	}
	fset := token.NewFileSet()
	scanned := 0
	bridgeUses := make(map[string]bool, len(inferenceBridgeFiles))
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
			if !isOldInferencePath(path) {
				continue
			}
			if slices.Contains(inferenceBridgeFiles, name) {
				bridgeUses[name] = true
				continue
			}
			t.Errorf("%s importa %s: en el arranque nuevo solo %v pueden importar la inferencia vieja",
				name, path, inferenceBridgeFiles)
		}
	}
	if scanned == 0 {
		t.Fatal("recorridos = 0: el barrido no miró ningún fichero")
	}
	for _, name := range inferenceBridgeFiles {
		if !bridgeUses[name] {
			t.Errorf("%s ya no importa ningún paquete viejo de inferencia: si el adaptador murió, la "+
				"excepción y este test sobran", name)
		}
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

// TestIdentidad_TheBridgesWrapTheContainerInstances (R4.7.c): lo que recibe turnoacotado.New es
// el adaptador de bridge_inferencia.go, y envuelve LA MISMA instancia que el contenedor: el turno
// acotado pregunta al único selector. Y el re-análisis, que desde F7 (conmutar(captacion)) es el
// NUEVO, ya no lee detrás de ningún adaptador: su LLMConfig ES c.tenantLLMStore, el
// *tenantllm.Postgres nuevo, el mismo del que lee el selector. Murió llmConfigBridge; lo que este
// test afirmaba de él (un solo camino al SQL de tenant_llm) se afirma ahora sin intermediario.
func TestIdentidad_TheBridgesWrapTheContainerInstances(t *testing.T) {
	c := contenedorDeHuella(t, "minimo")

	turnero := field(t, c.consultaResolver, "turnero")
	if turnero.IsNil() || turnero.Elem().Type() != reflect.TypeFor[*turneroBridge]() {
		t.Fatalf("el Turnero del resolutor del turno acotado no es un *turneroBridge (bridge_inferencia.go)")
	}
	if !sameInstance(inner(t, turnero, "sel"), c.llmSelector) {
		t.Error("el turneroBridge no envuelve c.llmSelector: habría dos selectores de vía en el proceso")
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
// (como proveedor y como calentador) y el turno acotado (detrás de su adaptador) usan el MISMO
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
		{"el turno acotado, detrás de turneroBridge", inner(t, field(t, c.consultaResolver, "turnero"), "sel")},
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
// único resolver de derechos. Y la cara VIEJA ya no recibe ninguno de los dos almacenes: con un
// valor ahí, publicapi volvería a registrar las cuatro rutas detrás de la nueva.
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

	old := depsDeLaAPIPublica(c)
	if old.TenantLLM != nil {
		t.Errorf("la cara vieja recibe un TenantLLM (%T); se espera nil: F1–F3 las sirve la nueva", old.TenantLLM)
	}
	if old.DegradationNotices != nil {
		t.Errorf("la cara vieja recibe un DegradationNotices (%T); se espera nil: F4 la sirve la nueva", old.DegradationNotices)
	}
}
