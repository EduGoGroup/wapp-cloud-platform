package modulos

import (
	"fmt"
	"slices"
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
	// Módulo nuevo → módulos nuevos que PUEDE importar (además de platform y nucleo,
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
	// Viejo → módulo: vacío desde el relevo (F10 · T10.11), que borró el árbol viejo entero. Mientras
	// existió decía a qué módulo pertenecía cada paquete viejo, para juzgar los puentes y prohibir
	// que internal/arranque cableara lo viejo de un módulo conmutado.
	Mapa: map[string]string{},
	// Módulos conmutados (el arranque nuevo ya los cablea SOLO con lo nuevo). Un módulo entra con
	// el commit que retira su ÚLTIMO adaptador internal/arranque/bridge_<x>.go, o con su
	// conmutar(<m>) si nunca tuvo adaptador (05 §4.2, D-F1-15): mientras viva un adaptador, la
	// regla 3 falla, porque el adaptador existe para importar lo viejo. acceso (conmutado en F2)
	// entró en F3, con el conmutar(edge) que borró bridge_iam.go. edge entró al cierre de F45-02
	// (D-F3-14): su adaptador bridge_gateway.go murió en F4 (conmutar(inferencia)) y se borró
	// internal/arranque/session_identity_test.go, que importaba internal/gateway/session viejo para
	// afirmar que el centinela ErrSessionOffline viejo y el nuevo son la misma variable (D-F3-2).
	// Era redundante: lo afirman edge/session/registry_test.go contra el de platform, los tests de
	// platform/httpapi por comportamiento (502) y el proceso P1 por el cable. catalogo entró en F7,
	// con el conmutar(captacion) (D-R-4 ✎ 2026-10-07, decisión de Jhoan; F5 reglas.md §4.10): nunca
	// tuvo adaptador, pero su conmutación de F5 fue nominal y el arranque siguió importando el
	// índice viejo (internal/intake/catalogo) hasta que el worker nuevo de captación pasó a pedir
	// el *indice.Indice de internal/modulos/catalogo/indice.
	//
	// 🔀 F8 · conmutar(conversacion): la lista queda COMPLETA. Con ese commit mueren los tres
	// adaptadores que quedaban y entra el dueño de cada uno: nucleo (bridge_contact.go, vivo desde
	// F1), inferencia (bridge_inferencia.go, desde F4), captacion (bridge_captacion.go, desde F7) y
	// solicitudes (la segunda instancia vieja de internal/intakes que el arranque construía para el
	// proyector del carrito viejo); y entra conversacion, que nunca tuvo adaptador propio. Desde
	// aquí la regla 3 prohíbe a internal/arranque cablear CUALQUIER paquete viejo del Mapa.
	Conmutados: []string{"acceso", "edge", "catalogo", "nucleo", "inferencia", "captacion", "solicitudes", "conversacion"},
	// Puentes: import de un paquete NUEVO a uno VIEJO, declarado (05 §4.1). Vacía de F0 a F5;
	// hubo tres, los tres hacia el almacén viejo de flujos: solicitudes/intakes/telemetria →
	// flujos/store (F6-04), captacion/stages → flujos/store (F7-02, T7.9) y captacion/reanalisis →
	// flujos/events (F7-03, T7.12). El PUENTE 3 de plan/F7-captacion/arquitectura.md §2
	// (reanalisis → flujos/runtime, por DefaultThreadLimit) se evitó: el límite del hilo entra por
	// reanalisis.NewService.
	//
	// 🔀 F8 · conmutar(conversacion): VACÍA otra vez, y para siempre (T8.31). Los tres paquetes
	// importan ya internal/modulos/conversacion/{store,events}, que son aristas de Capas
	// (solicitudes → conversacion, captacion → conversacion), no puentes. F8 es la última fase de
	// módulo: no puede quedar ninguno «para luego» (plan/F8-conversacion/reglas.md §3).
	Puentes: []candados.Puente{},
	// Fases cerradas: el commit que cierra cada fase añade aquí su id ("F0", "F1"…); un Puente
	// cuyo Muere está en esta lista debió borrarse (regla 6). F0 aún no está cerrada.
	FasesCerradas: []string{},
}

// TestFronteras: el árbol real (nuevo y viejo, producción y tests) cumple la tabla reglas.
// Además de recorridos > 0, exige haber visto un fichero del árbol nuevo y uno de platform: una
// raíz vacía o equivocada no puede pasar en verde.
func TestFronteras(t *testing.T) {
	fuentes := recorrerAlcance(t, dirsFronteras)
	for _, testigo := range []string{
		"internal/candados/fronteras.go",     // árbol nuevo
		"internal/platform/config/config.go", // platform
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

// allModules son los ocho módulos del árbol nuevo. Tras el relevo (F10) los ocho están
// conmutados: es la lista que Conmutados debe tener, ni uno más ni uno menos.
var allModules = []string{"acceso", "edge", "catalogo", "nucleo", "inferencia", "captacion", "solicitudes", "conversacion"}

// checkRelayTable comprueba lo que el relevo dejó FIJO en una tabla de fronteras y devuelve un
// motivo por cada cosa que no se cumple (ninguno = bien):
//   - Puentes está vacía. Un puente era un import declarado de un paquete nuevo a uno VIEJO; el
//     árbol viejo se borró, así que no hay a qué tender uno. Lo que haga falta de otro módulo es
//     una arista de Capas o un puerto, no un puente;
//   - Conmutados tiene exactamente allModules, sin repetir (el orden no importa).
func checkRelayTable(r candados.Reglas) []string {
	var problems []string
	if n := len(r.Puentes); n != 0 {
		problems = append(problems, fmt.Sprintf("Puentes tiene %d entradas (la primera, %s → %s) y debe estar vacía: "+
			"tras el relevo no hay árbol viejo al que tender un puente; una dependencia entre módulos es una arista de Capas",
			n, r.Puentes[0].Desde, r.Puentes[0].Hacia))
	}
	seen := make(map[string]int, len(r.Conmutados))
	for _, m := range r.Conmutados {
		seen[m]++
		switch {
		case !slices.Contains(allModules, m):
			problems = append(problems, fmt.Sprintf("Conmutados nombra %q, que no es un módulo", m))
		case seen[m] == 2:
			problems = append(problems, fmt.Sprintf("Conmutados repite %q", m))
		}
	}
	for _, m := range allModules {
		if seen[m] == 0 {
			problems = append(problems, fmt.Sprintf("Conmutados no tiene %q: tras el relevo los ocho módulos están conmutados", m))
		}
	}
	return problems
}

// TestRelayTable: la tabla real no tiene puentes y tiene los ocho módulos conmutados (F10 ·
// T10.12). Es un tope: Puentes no puede volver a crecer ni Conmutados encoger.
func TestRelayTable(t *testing.T) {
	for _, p := range checkRelayTable(reglas) {
		t.Errorf("tabla de fronteras: %s", p)
	}
}

// TestRelayTableBites prueba la comprobación contra copias de la tabla real con UNA cosa rota:
// la real pasa, y cada mutación da exactamente el motivo que le toca.
func TestRelayTableBites(t *testing.T) {
	mutate := func(f func(r *candados.Reglas)) candados.Reglas {
		r := reglas
		r.Puentes = slices.Clone(reglas.Puentes)
		r.Conmutados = slices.Clone(reglas.Conmutados)
		f(&r)
		return r
	}
	cases := []struct {
		name  string
		table candados.Reglas
		want  []string // un fragmento por motivo esperado, en orden
	}{
		{"the real table", reglas, nil},
		{"same modules in another order", mutate(func(r *candados.Reglas) { slices.Reverse(r.Conmutados) }), nil},
		{"a bridge is added", mutate(func(r *candados.Reglas) {
			r.Puentes = append(r.Puentes, candados.Puente{Desde: "internal/modulos/edge", Hacia: "internal/viejo", Motivo: "x", Nace: "F10", Muere: "F11"})
		}), []string{"Puentes tiene 1 entradas (la primera, internal/modulos/edge → internal/viejo)"}},
		{"a module is removed", mutate(func(r *candados.Reglas) {
			r.Conmutados = slices.DeleteFunc(r.Conmutados, func(m string) bool { return m == "captacion" })
		}), []string{`Conmutados no tiene "captacion"`}},
		{"no module at all", mutate(func(r *candados.Reglas) { r.Conmutados = nil }), []string{
			`no tiene "acceso"`, `no tiene "edge"`, `no tiene "catalogo"`, `no tiene "nucleo"`,
			`no tiene "inferencia"`, `no tiene "captacion"`, `no tiene "solicitudes"`, `no tiene "conversacion"`}},
		{"an unknown module is added", mutate(func(r *candados.Reglas) { r.Conmutados = append(r.Conmutados, "platform") }),
			[]string{`Conmutados nombra "platform"`}},
		{"a module is repeated", mutate(func(r *candados.Reglas) { r.Conmutados = append(r.Conmutados, "edge", "edge") }),
			[]string{`Conmutados repite "edge"`}},
		{"a module swapped for a bridge", mutate(func(r *candados.Reglas) {
			r.Conmutados = r.Conmutados[1:]
			r.Puentes = append(r.Puentes, candados.Puente{Desde: "a", Hacia: "b"})
		}), []string{"Puentes tiene 1 entradas", `Conmutados no tiene "acceso"`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := checkRelayTable(tc.table)
			if len(got) != len(tc.want) {
				t.Fatalf("motivos = %q; quiero %d: %q", got, len(tc.want), tc.want)
			}
			for i, want := range tc.want {
				if !strings.Contains(got[i], want) {
					t.Errorf("motivo %d = %q; quiero uno que diga %q", i, got[i], want)
				}
			}
		})
	}
	if len(reglas.Puentes) != 0 || len(reglas.Conmutados) != len(allModules) {
		t.Errorf("los casos mutaron la tabla real: Puentes=%v Conmutados=%v", reglas.Puentes, reglas.Conmutados)
	}
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

// eventsDir es el paquete del evento conversacional; classifierMarks, los trozos de ruta que
// delatan una dependencia del clasificador (los siete del candado viejo, sin tocar).
const eventsDir = "internal/modulos/conversacion/events/"

var classifierMarks = []string{"intent", "llm", "ollama", "openai", "anthropic", "clasific", "classif"}

// TestEventsDoesNotDependOnTheClassifier: ningún fichero de PRODUCCIÓN del paquete
// internal/modulos/conversacion/events importa una ruta que contenga uno de classifierMarks
// (REQ-21, T3.3: el resumen determinista y el menú del evento son el camino SIN LLM). Era
// TestPaqueteEventsNoDependeDelClasificador (internal/flujos/events/summary_test.go:784), un
// test de imports dentro del paquete; D-F8-4 lo trae aquí porque es una regla de frontera
// (conversacion/events no importa inferencia/** ni captacion/intentcfg) y no una conducta del
// resumen. Como el viejo, mira solo producción y solo el directorio del paquete, no sus
// subpaquetes: eventshelpertest es soporte de tests.
//
// Por qué imports y no un doble que haga t.Fatal: el resumen no tiene costura por la que
// inyectar un clasificador; lo que hay que impedir es que alguien la abra, y eso se ve aquí.
//
// Exige haber visto al menos un fichero: con la raíz o el prefijo equivocados no pasa en verde.
func TestEventsDoesNotDependOnTheClassifier(t *testing.T) {
	fuentes := recorrerAlcance(t, dirsFronteras)
	seen := 0
	for _, f := range fuentes {
		rest, ok := strings.CutPrefix(f.Ruta, eventsDir)
		if !ok || strings.Contains(rest, "/") || strings.HasSuffix(rest, "_test.go") {
			continue
		}
		seen++
		for _, imp := range f.Archivo.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Errorf("%s: import ilegible %s: %v", f.Ruta, imp.Path.Value, err)
				continue
			}
			lower := strings.ToLower(path)
			for _, mark := range classifierMarks {
				if strings.Contains(lower, mark) {
					t.Errorf("%s importa %s (contiene %q): el evento conversacional no puede depender del clasificador (REQ-21)",
						f.Ruta, path, mark)
				}
			}
		}
	}
	if seen == 0 {
		t.Fatalf("el recorrido (%d ficheros) no vio ningún fichero de producción en %s: el candado no mira nada", len(fuentes), eventsDir)
	}
	t.Logf("ficheros de producción de events=%d", seen)
}
