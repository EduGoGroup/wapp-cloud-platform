//go:build laboratorio

package common

import (
	"io"
	"log/slog"
	"os"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/llmvia"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/llmvia/local"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm/tenantllmhelpertest"
)

// NewSelector arma el llmvia.Selector REAL sobre una configuración LLM en memoria
// (tenantllmhelpertest.Memoria) y el frame dado. Como el tenant no tiene fila, el selector elige la
// vía LOCAL (REQ-33) y cada etapa sale por local.Provider: el prompt real, el techo real por etapa,
// el plazo heredado real y el llm.ExtractJSON real. Lo único que no es de producción es el frame.
//
// Sin notificador (WithNotifier): una caída no escribe aviso de degradación y el provider va sin
// envoltura, que es lo que quiere un escalón que no estudia la degradación.
//
// ⚠️ Con un frame del laboratorio, NewSelector escribe UN Warn de arranque —«el transporte de la vía
// local no sabe decir qué Edge atiende…»—: es cierto (estos frames no tienen PlazaDe) y no afecta a
// nada fuera del aforo de plaza del pipeline de lote. Con NewLog(false) no se ve.
//
// tenantllmhelpertest arrastra el paquete testing (sus suites de contrato viven en el mismo
// paquete). En un main no estorba: sus banderas solo se registran si alguien llama a testing.Init.
func NewSelector(frame local.Frame, log logger.Logger, opts ...llmvia.SelectorOption) (*llmvia.Selector, error) {
	all := append([]llmvia.SelectorOption{llmvia.WithFrame(frame)}, opts...)
	return llmvia.NewSelector(tenantllmhelpertest.NewMemoria(), log, all...)
}

// NewLog devuelve el logger de los escalones, por stderr para no mezclarse con el narrador: con
// verbose, desde Debug; sin él, solo errores.
func NewLog(verbose bool) logger.Logger {
	level := slog.LevelError
	if verbose {
		level = slog.LevelDebug
	}
	var w io.Writer = os.Stderr
	return logger.New(logger.WithLevel(level), logger.WithWriter(w))
}
