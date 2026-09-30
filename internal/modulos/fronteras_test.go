package modulos

import (
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
	// Módulos conmutados (el arranque nuevo ya los cablea con lo nuevo): lo añade el commit
	// conmutar(<m>). Vacía en F0.
	Conmutados: []string{},
	// Puentes: import de un paquete NUEVO a uno VIEJO, declarado (05 §4.1). Vacía en F0.
	Puentes: []candados.Puente{},
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
