//go:build laboratorio

package common

import (
	"flag"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/llmvia/local"
)

// Los valores por defecto de las banderas comunes.
const (
	// LabModel es el modelo del laboratorio (Plan 058, principio 2). NO es el del producto
	// (qwen3:1.7b, ADR-0020): solo lo fija esta bandera.
	LabModel = "gemma4:e2b-it-qat"
	// LabOllamaURL es la URL del Ollama local.
	LabOllamaURL = "http://127.0.0.1:11434"
)

// Los dos frames que se pueden elegir con -frame.
const (
	FrameNameScript = "script"
	FrameNameOllama = "ollama"
)

// Flags son las opciones que comparten todos los escalones.
type Flags struct {
	// Frame es script (determinista) u ollama (el modelo de verdad).
	Frame string
	// Model es el modelo de Ollama (solo con -frame ollama).
	Model string
	// OllamaURL es la URL base de Ollama (solo con -frame ollama).
	OllamaURL string
	// Verbose saca el log del cloud por stderr desde Debug.
	Verbose bool
}

// RegisterFlags registra -frame, -model, -ollama y -v en fs (nil ⇒ flag.CommandLine). El escalón
// registra las suyas y luego llama a flag.Parse.
func RegisterFlags(fs *flag.FlagSet) *Flags {
	if fs == nil {
		fs = flag.CommandLine
	}
	b := &Flags{}
	fs.StringVar(&b.Frame, "frame", FrameNameScript, "quién contesta la inferencia: script (el guion fijo del caso Ámbar, determinista) u ollama (el modelo de verdad)")
	fs.StringVar(&b.Model, "model", LabModel, "modelo de Ollama (con -frame ollama)")
	fs.StringVar(&b.OllamaURL, "ollama", LabOllamaURL, "URL base de Ollama (con -frame ollama)")
	fs.BoolVar(&b.Verbose, "v", false, "saca el log del cloud por stderr (desde Debug)")
	return b
}

// FrameFromFlags construye el frame que piden las banderas, ya envuelto en un SpyFrame. El frame
// de dentro se recupera con Inner(): *ScriptFrame o *OllamaFrame.
func FrameFromFlags(b *Flags) (*SpyFrame, error) {
	var f local.Frame
	switch b.Frame {
	case FrameNameScript:
		f = NewAmberScriptFrame()
	case FrameNameOllama:
		f = NewOllamaFrame(b.OllamaURL, b.Model)
	default:
		return nil, fmt.Errorf("-frame %q: vale %s u %s", b.Frame, FrameNameScript, FrameNameOllama)
	}
	return Spy(f), nil
}

// Describe dice en una línea qué frame es, para la cabecera del escalón.
func (b *Flags) Describe() string {
	if b.Frame == FrameNameOllama {
		return fmt.Sprintf("ollama · modelo %s en %s", b.Model, b.OllamaURL)
	}
	return "script · el guion de respuestas fijas del caso Ámbar"
}
