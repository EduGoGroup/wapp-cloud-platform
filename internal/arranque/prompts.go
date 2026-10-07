// Copia de internal/bootstrap/arranque/prompts.go @ 80807ba (F0 · 05 §6). Desde F4 (T4.24,
// conmutar(inferencia)) el cargador es el de internal/modulos/inferencia/prompts.
package arranque

import (
	"fmt"

	"github.com/EduGoGroup/wapp-shared/llm"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/prompts"
)

// cargarPlantillasDePrompt lee los prompts ajustables de las etapas P2–P5 y deja
// dicho EN EL LOG de dónde salió cada uno.
//
// 🔴 LA LÍNEA DE LOG NO ES ADORNO. El fallo que esta palanca puede producir es
// «alguien editó un prompt en este entorno y nadie se acuerda»: sin esa línea, un
// artefacto raro manda a leer el código compilado, que no es lo que corrió. Con
// ella, el arranque dice `p4=/etc/wapp/prompts/p4-normalizar-cantidades.tmpl` y la
// pregunta se contesta sola.
//
// Un error aquí ABORTA EL ARRANQUE, y esa es la política entera de esta palanca:
// no hay ningún fallo que se degrade a seguir con el texto compilado. Un operador
// que editó un fichero y no ve el efecto es peor que un proceso que no arranca,
// porque el segundo se nota.
func cargarPlantillasDePrompt(log sharedlogger.Logger, dir string) (map[llm.Etapa]llm.Plantilla, error) {
	cargadas, err := prompts.Cargar(dir)
	if err != nil {
		return nil, fmt.Errorf("prompts ajustables de P2-P5: %w", err)
	}
	campos := []any{"dir", dir}
	if dir == "" {
		campos = []any{"dir", "(ninguno: corre el texto compilado en shared/wapp-shared/llm)"}
	}
	for _, e := range llm.EtapasAjustables {
		campos = append(campos, string(e), cargadas.Origen[e])
	}
	log.Info("prompts: plantillas de las etapas ajustables", campos...)
	return cargadas.Plantillas, nil
}
