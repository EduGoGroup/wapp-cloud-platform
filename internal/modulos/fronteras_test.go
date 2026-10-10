package modulos

import (
	"strconv"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/candados"
)

// moduloGo es la ruta del módulo Go: Fronteras relativiza con ella los imports del repo.
const moduloGo = "github.com/EduGoGroup/wapp-cloud-platform"

// dirsFronteras: el árbol nuevo Y el viejo, producción y tests (reglas 1–6 de
// candados.Fronteras; un test nuevo que importa lo viejo sería portar a escondidas, 05 E-8).
var dirsFronteras = []string{"internal", "cmd"}

// reglas es la tabla de fronteras (plan/F0-andamiaje/diseno.md §4.1).
var reglas = candados.Reglas{
	// Módulo nuevo → módulos nuevos que PUEDE importar (además de platform, nucleo y pendiente,
	// que todo el árbol nuevo puede importar). Lista blanca de HOY congelada (04 §3): las 13
	// aristas módulo→módulo medidas con el script de 02 §5 y el MAP de D-5 (platformadmin y
	// entitlements → acceso) sobre dev @ c55e9e3, el 2026-09-30, SOLO imports de producción
	// (.Imports). Entre paréntesis, el import viejo que justifica cada arista.
	// No están aquí: platform → {acceso, edge} (los ✎ de platform, bloque E, se cortan con
	// alias; platform no es un módulo), ni las aristas que solo sumarían los tests viejos
	// (acceso→edge, edge→conversacion, edge→inferencia, solicitudes→captacion): los tests
	// viejos no se portan (05 E-8).
	//
	// El índice del catálogo (D-F5-2, F5): la arista PROHIBIDA `conversacion/** →
	// catalogo/indice` —el índice no entra en el turno conversacional, INV-02/T1.5; era el
	// candado AST internal/intake/catalogo/frontera_test.go— vivió en esta regla 1 mientras
	// Capas["conversacion"] no incluía "catalogo" (decisión de Jhoan, 2026-10-07): el motor solo
	// distingue MÓDULOS, así que nada de conversacion podía importar nada de catalogo. Desde F8-03
	// (decisión de Jhoan, 2026-10-09, «Capas + test propio») el carrito nuevo importa el paquete
	// RAÍZ de catalogo (ParseCatalog y los tipos del árbol), "catalogo" está en
	// Capas["conversacion"] y esta regla ya NO cubre al índice: la prohibición por SUBPAQUETE, que
	// el motor no sabe expresar y que no se amplía para esto, la vigila
	// TestConversacionDoesNotImportCatalogIndex, más abajo. La arista permitida
	// `catalogo → conversacion/model` es Capas["catalogo"]: los dos módulos se importan entre sí,
	// pero no los mismos paquetes (catalogo no importa conversacion/modules/cart), así que no hay
	// ciclo de compilación.
	Capas: map[string][]string{
		"acceso":      {},
		"edge":        {"acceso"},       // gateway/grpc → iam/domain
		"inferencia":  {"edge"},         // llmvia → gateway/grpc
		"catalogo":    {"conversacion"}, // catalogimport → flujos/modules/cart
		"solicitudes": {"conversacion"}, // intakes/telemetria → flujos/store
		"captacion": {
			"acceso",       // reanalisis → entitlements
			"catalogo",     // intake/pipeline → intake/catalogo
			"conversacion", // intake/pipeline → flujos/modules/cart
			"inferencia",   // reanalisis → tenantllm
			"solicitudes",  // intake/pipeline → intakes
		},
		"conversacion": {
			"acceso",      // flujos/events → entitlements
			"captacion",   // flujos/runtime → intake
			"catalogo",    // modules/cart → catalogo (ParseCatalog; Catalog, Category, Article, Variant). NUNCA catalogo/indice
			"edge",        // flujos/admin → gateway/fleet
			"inferencia",  // turnoacotado → llmvia
			"solicitudes", // flujos/modules/cart → intakes
		},
	},
	// Viejo → módulo (04 §4; gana el prefijo más largo: internal/flujos/contact va a nucleo
	// aunque internal/flujos vaya a conversacion, e internal/intake/catalogo a catalogo aunque
	// internal/intake vaya a captacion). Sirve para saber a qué módulo pertenece el destino de
	// un puente y para prohibir que internal/arranque cablee lo viejo de un módulo conmutado.
	Mapa: map[string]string{
		"internal/iam":           "acceso",
		"internal/platformadmin": "acceso",
		"internal/entitlements":  "acceso",

		"internal/gateway":     "edge",
		"internal/diagnostics": "edge",
		"internal/inferstats":  "edge",
		"internal/receipts":    "edge",
		"internal/ingest":      "edge",
		"internal/filtercfg":   "edge",

		"internal/flujos":         "conversacion",
		"internal/turnoacotado":   "conversacion",
		"internal/flujos/contact": "nucleo",

		"internal/catalogimport":   "catalogo",
		"internal/intake/catalogo": "catalogo",

		"internal/intake":      "captacion",
		"internal/intakeahead": "captacion",
		"internal/evidence":    "captacion",
		"internal/reanalisis":  "captacion",
		"internal/casebank":    "captacion",
		"internal/intentcfg":   "captacion",

		"internal/llmvia":      "inferencia",
		"internal/prompts":     "inferencia",
		"internal/tenantllm":   "inferencia",
		"internal/degradation": "inferencia",

		"internal/intakes":      "solicitudes",
		"internal/integrations": "solicitudes",
		"internal/contracts":    "solicitudes",
		"internal/tenantvars":   "solicitudes",
	},
	// Módulos conmutados (el arranque nuevo ya los cablea SOLO con lo nuevo). Un módulo entra con
	// el commit que retira su ÚLTIMO adaptador internal/arranque/bridge_<x>.go, o con su
	// conmutar(<m>) si nunca tuvo adaptador (05 §4.2, D-F1-15): mientras viva un adaptador, la
	// regla 3 falla, porque el adaptador existe para importar lo viejo. Por eso nucleo, conmutado
	// en F1 con bridge_contact.go, no está aquí: entra en F8, cuando muera ese adaptador. acceso
	// (conmutado en F2) entra en F3, con el conmutar(edge) que borra bridge_iam.go. edge entra
	// al cierre de F45-02 (D-F3-14): su adaptador bridge_gateway.go murió en F4 (conmutar(inferencia))
	// y se borró internal/arranque/session_identity_test.go, que importaba internal/gateway/session
	// viejo para afirmar que el centinela ErrSessionOffline viejo y el nuevo son la misma variable
	// (D-F3-2). Era redundante: lo afirman edge/session/registry_test.go contra el de platform, los
	// tests de platform/httpapi por comportamiento (502) y el proceso P1 por el cable.
	// inferencia no entra hasta F8, cuando muera bridge_inferencia.go. catalogo entra en F7, con
	// el conmutar(captacion) (D-R-4 ✎ 2026-10-07, decisión de Jhoan; F5 reglas.md §4.10): nunca
	// tuvo adaptador, pero su conmutación de F5 fue nominal y el arranque siguió importando el
	// índice viejo (internal/intake/catalogo) hasta que el worker nuevo de captación pasó a pedir
	// el *indice.Indice de internal/modulos/catalogo/indice. captacion NO entra con ese commit:
	// nace con él su adaptador, bridge_captacion.go, y entra en F8, cuando muera.
	Conmutados: []string{"acceso", "edge", "catalogo"},
	// Puentes: import de un paquete NUEVO a uno VIEJO, declarado (05 §4.1). Vacía de F0 a F5;
	// el primero nace en F6 (F6-04); el segundo, en F7 (F7-02, T7.9); el tercero, en F7 (F7-03,
	// T7.12). El PUENTE 3 de plan/F7-captacion/arquitectura.md §2 (reanalisis → flujos/runtime,
	// por DefaultThreadLimit) se evitó: el límite del hilo entra por reanalisis.NewService.
	Puentes: []candados.Puente{
		{
			Desde: "internal/modulos/solicitudes/intakes/telemetria",
			Hacia: "internal/flujos/store",
			Motivo: "flow_events lo escribe el almacén viejo de flujos (store.FlowEvent, InsertFlowEvent), " +
				"que aún no tiene gemelo nuevo en conversacion; intakes no puede importarlo (ciclo con el " +
				"test in-package del store), así que lo importa este adaptador",
			Nace:  "F6",
			Muere: "F8",
		},
		{
			Desde: "internal/modulos/captacion/stages",
			Hacia: "internal/flujos/store",
			Motivo: "la etapa draft declara en sus puertos los tipos del almacén viejo de flujos: store.Intake " +
				"(IntakeStore: GetIntakeByEvent, UpsertIntake) y store.FlowEvent (EventWriter: InsertFlowEvent). " +
				"public.intakes y flow_events los sigue sirviendo ese almacén, que no tiene gemelo nuevo en " +
				"conversacion hasta F8 (PUENTE 1 de plan/F7-captacion/arquitectura.md §2)",
			Nace:  "F7",
			Muere: "F8",
		},
		{
			Desde: "internal/modulos/captacion/reanalisis",
			Hacia: "internal/flujos/events",
			Motivo: "el re-análisis declara en su puerto Thread el tipo del hilo del almacén viejo de eventos " +
				"(events.ThreadEntry, en ListThread) y decide si hay material contando las entradas " +
				"events.KindMessage. conversation_event_messages la sigue sirviendo ese almacén, que no " +
				"tiene gemelo nuevo en conversacion hasta F8 (PUENTE 2 de plan/F7-captacion/arquitectura.md §2)",
			Nace:  "F7",
			Muere: "F8",
		},
	},
	// Fases cerradas: el commit que cierra cada fase añade aquí su id ("F0", "F1"…); un Puente
	// cuyo Muere está en esta lista debió borrarse (regla 6). F0 aún no está cerrada.
	FasesCerradas: []string{},
}

// TestFronteras: el árbol real (nuevo y viejo, producción y tests) cumple la tabla reglas.
// Además de recorridos > 0, exige haber visto un fichero del árbol nuevo y uno del viejo: una
// raíz vacía o equivocada no puede pasar en verde.
func TestFronteras(t *testing.T) {
	fuentes := recorrerAlcance(t, dirsFronteras)
	for _, testigo := range []string{
		"internal/candados/fronteras.go",             // árbol nuevo
		"internal/bootstrap/arranque/orquestador.go", // árbol viejo
	} {
		if !vio(fuentes, testigo) {
			t.Fatalf("el recorrido (%d ficheros) no vio %s: raíz o directorios equivocados", len(fuentes), testigo)
		}
	}
	for _, v := range candados.Fronteras(moduloGo, fuentes, reglas) {
		t.Errorf("fronteras: %s", v)
	}
	t.Logf("recorridos=%d", len(fuentes))
}

// conversationDir y catalogIndexPkg delimitan la arista prohibida por SUBPAQUETE: nada bajo el
// primero puede importar el segundo ni un subpaquete suyo.
const (
	conversationDir = "internal/modulos/conversacion/"
	catalogIndexPkg = moduloGo + "/internal/modulos/catalogo/indice"
)

// TestConversacionDoesNotImportCatalogIndex: ningún fichero de internal/modulos/conversacion
// —producción o test, con la etiqueta de compilación que lleve— importa
// internal/modulos/catalogo/indice ni un subpaquete suyo. El índice del catálogo no entra en el
// turno conversacional (INV-02/T1.5, D-F5-2): el carrito lee el catálogo por el paquete raíz y
// nada más. Es la prohibición que la regla 1 de TestFronteras dejó de cubrir al entrar
// "catalogo" en Capas["conversacion"] (F8-03); recorre las mismas fuentes que ese test.
//
// Exige haber visto al menos un fichero de conversacion: con la raíz o el prefijo equivocados
// no puede pasar en verde.
func TestConversacionDoesNotImportCatalogIndex(t *testing.T) {
	fuentes := recorrerAlcance(t, dirsFronteras)
	seen := 0
	for _, f := range fuentes {
		if !strings.HasPrefix(f.Ruta, conversationDir) {
			continue
		}
		seen++
		for _, imp := range f.Archivo.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Errorf("%s: import ilegible %s: %v", f.Ruta, imp.Path.Value, err)
				continue
			}
			if path == catalogIndexPkg || strings.HasPrefix(path, catalogIndexPkg+"/") {
				t.Errorf("%s importa %s: conversacion no puede importar el índice del catálogo (catalogo/indice); "+
					"el carrito usa solo el paquete raíz internal/modulos/catalogo", f.Ruta, path)
			}
		}
	}
	if seen == 0 {
		t.Fatalf("el recorrido (%d ficheros) no vio ningún fichero bajo %s: el candado no mira nada", len(fuentes), conversationDir)
	}
	t.Logf("ficheros de conversacion=%d", seen)
}
