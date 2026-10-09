package arranque

// solicitudes_cableado_identidad_test.go — la mitad de IDENTIDAD del test de cableado de
// solicitudes (F6 · T6.24–T6.25, R6.5.a–c, R6.6.b–c; FX TX.18). Sale de
// solicitudes_cableado_test.go por tamaño (E-13). Sobre el arranque nuevo REAL (el contenedor de
// la huella) afirma que cada pieza de solicitudes recibe LAS MISMAS instancias que el contenedor,
// que la cara HTTP nueva sirve G1–G18 con ellas, que a la cara vieja no le queda nada de
// solicitudes ni de captación (desde F7 tampoco el centinela de montaje de H1, D-F6-13) y que el
// plazo de escritura de G7 sale del plazo del generador, que es el suelo del pipeline NUEVO (T-13).

import (
	"go/ast"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/pipeline"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/publicapi"
)

// TestIdentidad_TheRequestsPiecesShareTheContainerInstances (R6.6.b, reglas.md §3): el Service
// cablea el único notificador (como avisador y como emisor de la cotización) y los dos
// recordatorios; el notificador habla por el único gateway y lee la config del único almacén; los
// recordatorios y el generador de cotización leen de ese mismo almacén NUEVO; y el gate del
// puente CRM pregunta al único resolver de derechos y al único almacén de integraciones.
func TestIdentidad_TheRequestsPiecesShareTheContainerInstances(t *testing.T) {
	c := contenedorDeHuella(t, "minimo")

	shared := []struct {
		name string
		got  reflect.Value
		want any
	}{
		{"el Store del Service", field(t, c.intakeService, "store"), c.intakeStore},
		{"el StatusNotifier del Service", field(t, c.intakeService, "notifier"), c.intakeNotifier},
		{"el QuoteSender del Service", field(t, c.intakeService, "quotes"), c.intakeNotifier},
		{"el DepositTouch del Service", field(t, c.intakeService, "deposits"), c.depositReminder},
		{"el ExpiryTouch del Service", field(t, c.intakeService, "expiry"), c.expiryReminder},
		{"el MessageSender del notificador", field(t, c.intakeNotifier, "sender"), c.gw},
		{"el SettingsReader del notificador", field(t, c.intakeNotifier, "settings"), c.intakeStore},
		{"el notificador del recordatorio de la seña", field(t, c.depositReminder, "notifier"), c.intakeNotifier},
		{"el almacén del recordatorio de la seña", field(t, c.depositReminder, "store"), c.intakeStore},
		{"el almacén del recordatorio del plazo", field(t, c.expiryReminder, "store"), c.intakeStore},
		{"el resolver de derechos del gate del puente CRM", field(t, c.webhookGate, "features"), c.entResolver},
		{"el almacén del gate del puente CRM", field(t, c.webhookGate, "store"), c.integrationsStore},
		{"el IntakeReader del generador de cotización", field(t, c.quoteSvc, "reader"), c.intakeStore},
		{"el HistoryReader del generador de cotización", field(t, c.quoteSvc, "history"), c.intakeStore},
	}
	for _, k := range shared {
		if !sameInstance(k.got, k.want) {
			t.Errorf("%s no es la MISMA instancia que la del contenedor (%T)", k.name, k.want)
		}
	}
	for _, name := range []string{"crm", "metrics"} {
		if field(t, c.intakeService, name).IsNil() {
			t.Errorf("el Service no tiene cableado %q: la fase 6 perdió una opción", name)
		}
	}
}

// TestIdentidad_TheNotifierUsesTheOneContactResolver (arquitectura.md §4.1 de F6): el notificador
// NUEVO recibe el resolver de contactos del núcleo SIN envolver, y es la MISMA instancia en la
// que delega el contactBridge que sigue recibiendo el motor viejo. Dos resolvers serían dos vías
// custodiadas de PII; y con otro KeyProvider, otro índice ciego y contactos duplicados.
func TestIdentidad_TheNotifierUsesTheOneContactResolver(t *testing.T) {
	c := contenedorDeHuella(t, "minimo")

	bridge, ok := c.flowDeps.contacts.(*contactBridge)
	if !ok {
		t.Fatalf("flowDeps.contacts = %T; se espera *contactBridge", c.flowDeps.contacts)
	}
	if c.flowDeps.contactResolver == nil {
		t.Fatal("la fase 3 no dejó flowDeps.contactResolver")
	}
	if !sameInstance(reflect.ValueOf(bridge).Elem().FieldByName("next"), c.flowDeps.contactResolver) {
		t.Error("flowDeps.contactResolver no es el resolver en el que delega el contactBridge: habría dos")
	}
	if !sameInstance(field(t, c.intakeNotifier, "contacts"), c.flowDeps.contactResolver) {
		t.Error("el notificador de solicitudes no usa flowDeps.contactResolver")
	}
}

// TestIdentidad_TheNewFaceSharesTheRequestsInstances (FX TX.18): las seis áreas de solicitudes de
// la cara NUEVA (G1–G18 del mapa) reciben LAS MISMAS instancias que el contenedor. Un Service o
// un almacén construidos aparte para la API serían una segunda máquina de estados y una segunda
// verdad sobre las mismas filas.
func TestIdentidad_TheNewFaceSharesTheRequestsInstances(t *testing.T) {
	c := contenedorDeHuella(t, "minimo")

	face := requestsDepsOfTheNewFace(c)
	shared := []struct {
		name string
		got  any
		want any
	}{
		{"el Intakes de G1–G6 y G8", face.intakes.Intakes, c.intakeService},
		{"el Entitlements de G1–G6 y G8", face.intakes.Entitlements, c.entResolver},
		{"el Intakes de G7, G9 y G10", face.intakeReports.Intakes, c.intakeService},
		{"el Entitlements de G7, G9 y G10", face.intakeReports.Entitlements, c.entResolver},
		{"el QuoteSuggestions de G7", face.intakeReports.QuoteSuggestions, c.quoteSvc},
		{"el TenantVariables de G11–G12", face.tenantVariables.TenantVariables, c.tenantVars},
		{"el Integrations de G13–G16", face.integrations.Integrations, c.integrationsStore},
		{"el Entitlements de G13–G16", face.integrations.Entitlements, c.entResolver},
		{"el CRMSecrets de G17", face.crmCallback.CRMSecrets, c.integrationsStore},
		{"el CRMGate de G17", face.crmCallback.CRMGate, c.webhookGate},
		{"el CRMReflect de G17", face.crmCallback.CRMReflect, c.intakeStore},
		{"el CRMNotify de G17", face.crmCallback.CRMNotify, c.intakeNotifier},
	}
	for _, k := range shared {
		if !sameInstance(reflect.ValueOf(k.got), k.want) {
			t.Errorf("%s no es la MISMA instancia que la del contenedor (%T)", k.name, k.want)
		}
	}
	if face.tenantVariables.DBTimeout != c.cfg.PublicAPIDBTimeout {
		t.Errorf("el DBTimeout de G11 es %s; se espera el de config, %s", face.tenantVariables.DBTimeout, c.cfg.PublicAPIDBTimeout)
	}
	if _, ok := face.eventTelemetry.EventTelemetry.(*apipublica.PostgresEventTelemetryStore); !ok {
		t.Errorf("el EventTelemetry de G18 es un %T; se espera el *apipublica.PostgresEventTelemetryStore de la cara nueva",
			face.eventTelemetry.EventTelemetry)
	}
	// Los relojes se dejan a nil (⇒ time.Now): un reloj inyectado en producción sería un bug.
	if face.intakes.Now != nil || face.intakeReports.Now != nil || face.crmCallback.Now != nil {
		t.Error("alguna de las áreas de solicitudes recibe un Now en producción; se espera nil (time.Now)")
	}
	// G17 decide «no aviso» comparando CRMNotify con nil: sin notificador tiene que llegarle un nil
	// DE INTERFAZ, no un *intakes.Notifier nil dentro de ella.
	if got := crmStatusNotifierPort(nil); got != nil {
		t.Errorf("crmStatusNotifierPort(nil) = %T; se espera un nil de verdad", got)
	}
}

// Las tres rutas de captación (F7) y una de la bandeja (F6): ninguna puede seguir en la cara vieja.
const (
	reanalyzeRoute  = "POST /api/v1/intakes/{id}/reanalyze" // H1
	getIntentsRoute = "GET /api/v1/intents"                 // E1
	putIntentsRoute = "PUT /api/v1/intents"                 // E2
	listIntakeRoute = "GET /api/v1/intakes"                 // G1
)

// TestCableado_TheOldFaceKeepsNothingOfRequestsNorCapture (FX TX.18 y TX.21; muere D-F6-13): de
// solicitudes y de captación, la cara VIEJA no recibe NADA. Intakes es nil de verdad —ya no el
// centinela de montaje, que existía solo para que la vieja siguiera registrando H1—, los otros
// ocho campos de solicitudes son nil, y también Reanalysis, Intents y ConfigPush: con un valor en
// cualquiera, publicapi volvería a registrar esa ruta detrás de la nueva.
//
// Y lo fija contra el publicapi viejo REAL, que es lo que afirmaba el test del centinela al
// revés: con las deps del arranque, el mux viejo NO registra H1, ni E1, ni E2, ni la bandeja (G1,
// que con el centinela sí volvía a registrar, tapada). Esas rutas las sirve la cara nueva del
// arranque real, con su mismo patrón.
func TestCableado_TheOldFaceKeepsNothingOfRequestsNorCapture(t *testing.T) {
	c := contenedorDeHuella(t, "minimo")

	old := depsDeLaAPIPublica(c)
	absent := []struct {
		name string
		got  any
	}{
		{"Intakes", old.Intakes},
		{"QuoteSuggestions", old.QuoteSuggestions},
		{"TenantVariables", old.TenantVariables},
		{"Integrations", old.Integrations},
		{"CRMSecrets", old.CRMSecrets},
		{"CRMGate", old.CRMGate},
		{"CRMReflect", old.CRMReflect},
		{"CRMNotify", old.CRMNotify},
		{"EventTelemetry", old.EventTelemetry},
		{"Reanalysis", old.Reanalysis},
		{"Intents", old.Intents},
		{"ConfigPush", old.ConfigPush},
	}
	for _, k := range absent {
		if k.got != nil {
			t.Errorf("la cara vieja recibe un %s (%T); se espera nil: esa ruta la sirve la nueva", k.name, k.got)
		}
	}

	served := c.publicCara.Patrones()
	for _, route := range []string{reanalyzeRoute, getIntentsRoute, putIntentsRoute, listIntakeRoute} {
		if oldFaceRegisters(t, c, old, route) {
			t.Errorf("la cara vieja sigue registrando %s: la serviría dos veces, y la vieja con un servicio que no tiene", route)
		}
		if !slices.Contains(served, route) {
			t.Errorf("la cara nueva del arranque real NO registra %s: la ruta habría desaparecido del :8103", route)
		}
	}
}

// oldFaceRegisters dice si el publicapi viejo, montado con deps y el middleware del contenedor,
// registra pattern. No sirve la petición: solo pregunta al mux qué patrón la atendería.
func oldFaceRegisters(t *testing.T, c *contenedor, deps publicapi.Deps, pattern string) bool {
	t.Helper()
	mux := http.NewServeMux()
	publicapi.Register(mux, deps, c.authMW, c.auditor, c.log)
	req := peticionDe(pattern)
	_, got := mux.Handler(httptest.NewRequest(req.Method, req.URL.Path, nil))
	return got == pattern
}

// TestCableado_TheQuoteWriteDeadlineIsDerived (FX mapa §4.3, reglas.md T-9; T6.25): el plazo de
// ESCRITURA de G7 que recibe la cara nueva es el plazo de la llamada al modelo que el arranque le
// puso al generador de cotización, más 12 s. Se afirma sobre los VALORES del arranque real (lo
// que el generador guardó y lo que la cara recibe) y, por AST, que los dos salen de las
// constantes de fase5_captacion.go y no de un literal: dos números iguales escritos a mano
// pasarían la primera mitad hasta el día en que alguien moviera uno.
func TestCableado_TheQuoteWriteDeadlineIsDerived(t *testing.T) {
	c := contenedorDeHuella(t, "minimo")

	callTimeout := time.Duration(field(t, c.quoteSvc, "timeout").Int())
	if callTimeout <= 0 {
		t.Fatalf("el generador de cotización no tiene plazo por llamada (%s): quotetext.WithTimeout no se cableó", callTimeout)
	}
	if callTimeout != quoteCallTimeout {
		t.Errorf("el plazo del generador es %s; se espera quoteCallTimeout, %s", callTimeout, quoteCallTimeout)
	}
	got := requestsDepsOfTheNewFace(c).intakeReports.QuoteWriteDeadline
	if want := callTimeout + 12*time.Second; got != want {
		t.Errorf("el plazo de escritura de G7 es %s; se espera el plazo del generador (%s) + 12 s = %s", got, callTimeout, want)
	}

	_, files := astDelArranque(t)
	var timeoutArg, deadlineValue []string
	for _, f := range files {
		local := importsOf(t, f)[newQuoteTextImportPath]
		ast.Inspect(f, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.CallExpr:
				if local != "" && esLlamada(v, local, "WithTimeout") && len(v.Args) == 1 {
					timeoutArg = append(timeoutArg, campoCompletoDe(v.Args[0]))
				}
			case *ast.KeyValueExpr:
				if campoCompletoDe(v.Key) == "QuoteWriteDeadline" {
					deadlineValue = append(deadlineValue, campoCompletoDe(v.Value))
				}
			}
			return true
		})
	}
	if len(timeoutArg) != 1 || timeoutArg[0] != "quoteCallTimeout" {
		t.Errorf("quotetext.WithTimeout recibe %q; se espera una sola llamada, con quoteCallTimeout", timeoutArg)
	}
	if len(deadlineValue) != 1 || deadlineValue[0] != "quoteWriteDeadline" {
		t.Errorf("QuoteWriteDeadline se cablea con %q; se espera una sola vez, con quoteWriteDeadline", deadlineValue)
	}
}

// TestCableado_TheQuoteCallTimeoutIsTheNewPipelineFloor (reglas de F7, T-13; T7.24): al conmutar
// captación cambia de DÓNDE se lee el suelo por llamada del que sale el plazo de G7, y tiene que
// seguir siendo el mismo valor por el mismo camino. Se afirma la IGUALDAD —quoteCallTimeout es el
// CallTimeoutFloor del pipeline NUEVO, y es lo que el generador de cotización guardó al recibir
// quotetext.WithTimeout— y, por AST, que no es una coincidencia de dos números: en el fichero que
// declara la constante, su valor es `pipeline.CallTimeoutFloor`, ese `pipeline` es el import de
// internal/modulos/captacion/pipeline, y el pipeline viejo no se importa. Las etapas P2–P4 reciben
// ese MISMO suelo (stages.WithCallTimeout): un solo valor para las cuatro llamadas al modelo.
func TestCableado_TheQuoteCallTimeoutIsTheNewPipelineFloor(t *testing.T) {
	if quoteCallTimeout != pipeline.CallTimeoutFloor {
		t.Errorf("quoteCallTimeout = %s; se espera el CallTimeoutFloor del pipeline nuevo, %s",
			quoteCallTimeout, pipeline.CallTimeoutFloor)
	}
	c := contenedorDeHuella(t, "minimo")
	if got := time.Duration(field(t, c.quoteSvc, "timeout").Int()); got != pipeline.CallTimeoutFloor {
		t.Errorf("quotetext.WithTimeout recibió %s; se espera el CallTimeoutFloor del pipeline nuevo, %s",
			got, pipeline.CallTimeoutFloor)
	}

	_, files := astDelArranque(t)
	declared, stageFloors := 0, 0
	for _, f := range files {
		d, s := callTimeoutFloorUses(t, f)
		declared, stageFloors = declared+d, stageFloors+s
	}
	if declared != 1 {
		t.Errorf("quoteCallTimeout se declara %d veces en la producción de internal/arranque; se espera 1", declared)
	}
	if stageFloors != 1 {
		t.Errorf("stages.WithCallTimeout aparece %d veces en la producción de internal/arranque; se espera 1 (compartida por P2–P4)", stageFloors)
	}
}

// callTimeoutFloorUses cuenta, en f, las declaraciones de quoteCallTimeout y las llamadas a
// stages.WithCallTimeout (del stages NUEVO), y falla si alguna no toma su valor de
// `<pipeline nuevo>.CallTimeoutFloor`, o si el fichero que declara la constante importa además
// el pipeline viejo.
func callTimeoutFloorUses(t *testing.T, f *ast.File) (declared, stageFloors int) {
	t.Helper()
	imports := importsOf(t, f)
	floor := imports[newPipelineImportPath] + ".CallTimeoutFloor"
	stagesLocal, hasStages := imports[newStagesImportPath]
	ast.Inspect(f, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.ValueSpec:
			i := slices.IndexFunc(v.Names, func(name *ast.Ident) bool { return name.Name == "quoteCallTimeout" })
			if i < 0 {
				return true
			}
			declared++
			if _, old := imports[oldPipelineImportPath]; old {
				t.Errorf("el fichero que declara quoteCallTimeout importa %s: el suelo saldría del pipeline viejo", oldPipelineImportPath)
			}
			if i >= len(v.Values) || campoCompletoDe(v.Values[i]) != floor {
				t.Errorf("quoteCallTimeout no se declara como el CallTimeoutFloor de %s: un literal o el "+
					"suelo de otro paquete se separaría del pipeline el primer día", newPipelineImportPath)
			}
		case *ast.CallExpr:
			if !hasStages || !esLlamada(v, stagesLocal, "WithCallTimeout") {
				return true
			}
			stageFloors++
			if len(v.Args) != 1 || campoCompletoDe(v.Args[0]) != floor {
				t.Errorf("stages.WithCallTimeout no recibe el CallTimeoutFloor de %s: P2–P4 y G7 tendrían suelos distintos", newPipelineImportPath)
			}
		}
		return true
	})
	return declared, stageFloors
}
