//go:build laboratorio

package common

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/llmvia/local"

	edgegrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/grpc"
)

// OllamaFrame manda la inferencia a un Ollama de verdad, como lo haría el cajero del Edge
// (edge/wapp-edge-agent, internal/app/cajero/servidor.go): POST /api/chat sin streaming, UN mensaje
// de usuario con el prompt entero, y la salida cruda en message.content.
//
// Lo que viaja, campo a campo, y de dónde sale:
//
//   - model: el del laboratorio (-model). En producción lo elige el Edge, no la nube (ADR-0045);
//   - format: el de la petición, normalizado como lo hace el Edge: un JSON Schema (empieza por «{»)
//     va crudo, cualquier otra cosa («json») como cadena JSON;
//   - options.temperature: la de la petición (0 en la primera llamada, 0,3 en el reintento);
//   - options.num_ctx: 4096, el default del cajero;
//   - options.num_predict: MaxOutputTokens, el techo por etapa que fija el Cloud, si es > 0;
//   - keep_alive: -1 en el PRIMER nivel (dentro de options Ollama lo ignora en silencio);
//   - think: false SOLO si /api/show declara la capability «thinking». Mandarlo a un modelo que no la
//     tiene es un error de Ollama. Se pregunta UNA vez y se guarda.
//
// El plazo es el de la petición (req.Timeout), que ya trae descontado el margen del veredicto. Es seguro
// entre goroutines; Ultima devuelve la medida de la última llamada terminada.
type OllamaFrame struct {
	url    string
	model  string
	client *http.Client

	mu       sync.Mutex
	thinking *bool
	last     OllamaMeasure
}

var _ local.Frame = (*OllamaFrame)(nil)

// LabNumCtx es la ventana de contexto que se pide a Ollama (la del cajero por defecto).
const LabNumCtx = 4096

// OllamaMeasure es lo que costó una inferencia: el reloj de pared desde aquí y el desglose que devuelve
// Ollama (carga del modelo, prefill del prompt y generación).
type OllamaMeasure struct {
	Duration     time.Duration
	Load         time.Duration
	Prefill      time.Duration
	Generation   time.Duration
	PromptTokens int
	OutputTokens int
	Think        *bool
}

// String la resume en una línea legible.
func (m OllamaMeasure) String() string {
	think := "sin think"
	if m.Think != nil {
		think = fmt.Sprintf("think=%v", *m.Think)
	}
	return fmt.Sprintf("%s de pared (carga %s · prefill %s, %d tokens · generación %s, %d tokens · %s)",
		roundMs(m.Duration), roundMs(m.Load), roundMs(m.Prefill), m.PromptTokens,
		roundMs(m.Generation), m.OutputTokens, think)
}

// NewOllamaFrame construye el frame contra la URL base de Ollama (sin barra final) y el modelo.
func NewOllamaFrame(url, model string) *OllamaFrame {
	return &OllamaFrame{url: strings.TrimRight(url, "/"), model: model, client: &http.Client{}}
}

// Model es el modelo al que se le pregunta.
func (f *OllamaFrame) Model() string { return f.model }

// Last devuelve la medida de la última inferencia terminada (la cero si no hubo ninguna).
func (f *OllamaFrame) Last() OllamaMeasure {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last
}

// chatWire es la forma exacta que espera /api/chat.
type chatWire struct {
	Model     string          `json:"model"`
	Messages  []chatMessage   `json:"messages"`
	Stream    bool            `json:"stream"`
	Format    json.RawMessage `json:"format,omitempty"`
	Options   map[string]any  `json:"options,omitempty"`
	Think     *bool           `json:"think,omitempty"`
	KeepAlive int             `json:"keep_alive"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatReply struct {
	Message            chatMessage `json:"message"`
	LoadDuration       int64       `json:"load_duration"`
	PromptEvalCount    int         `json:"prompt_eval_count"`
	PromptEvalDuration int64       `json:"prompt_eval_duration"`
	EvalCount          int         `json:"eval_count"`
	EvalDuration       int64       `json:"eval_duration"`
}

// Infer implementa local.Frame.
func (f *OllamaFrame) Infer(ctx context.Context, _ string, req edgegrpc.InferRequest) (string, error) {
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}
	think, err := f.thinkFlag(ctx)
	if err != nil {
		return "", err
	}
	opts := map[string]any{"temperature": req.Temperature, "num_ctx": LabNumCtx}
	if req.MaxOutputTokens > 0 {
		opts["num_predict"] = int(req.MaxOutputTokens)
	}
	body, err := json.Marshal(chatWire{
		Model:     f.model,
		Messages:  []chatMessage{{Role: "user", Content: req.Prompt}},
		Format:    normalizeFormat(req.Format),
		Options:   opts,
		Think:     think,
		KeepAlive: -1,
	})
	if err != nil {
		return "", fmt.Errorf("ollama: armando la petición: %w", err)
	}

	start := time.Now()
	var out chatReply
	if err := f.post(ctx, "/api/chat", body, &out); err != nil {
		return "", err
	}
	f.mu.Lock()
	f.last = OllamaMeasure{
		Duration:     time.Since(start),
		Load:         time.Duration(out.LoadDuration),
		Prefill:      time.Duration(out.PromptEvalDuration),
		Generation:   time.Duration(out.EvalDuration),
		PromptTokens: out.PromptEvalCount,
		OutputTokens: out.EvalCount,
		Think:        think,
	}
	f.mu.Unlock()
	return out.Message.Content, nil
}

// thinkFlag devuelve &false si el modelo declara «thinking» y nil si no. Pregunta a /api/show la
// primera vez y guarda la respuesta; un fallo NO se guarda (vuelve a preguntar en la siguiente), y se
// devuelve como error porque casi siempre es «Ollama no corre» o «ese modelo no está».
func (f *OllamaFrame) thinkFlag(ctx context.Context) (*bool, error) {
	f.mu.Lock()
	cached := f.thinking
	f.mu.Unlock()
	if cached != nil {
		if *cached {
			off := false
			return &off, nil
		}
		return nil, nil
	}
	body, err := json.Marshal(map[string]string{"model": f.model})
	if err != nil {
		return nil, fmt.Errorf("ollama: armando /api/show: %w", err)
	}
	var show struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := f.post(ctx, "/api/show", body, &show); err != nil {
		return nil, fmt.Errorf("%w (¿está el modelo? `ollama pull %s`)", err, f.model)
	}
	has := slices.Contains(show.Capabilities, "thinking")
	f.mu.Lock()
	f.thinking = &has
	f.mu.Unlock()
	return f.thinkFlag(ctx)
}

// ModelThinks dice si el modelo declara la capability «thinking» (consulta /api/show una vez).
func (f *OllamaFrame) ModelThinks(ctx context.Context) (bool, error) {
	think, err := f.thinkFlag(ctx)
	return think != nil, err
}

// post manda body a la ruta y decodifica la respuesta en dst.
func (f *OllamaFrame) post(ctx context.Context, path string, body []byte, dst any) error {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, f.url+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("ollama: %s: %w", path, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := f.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("ollama no responde en %s: %w", f.url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("ollama %s devolvió %d: %s", path, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("ollama %s: respuesta ilegible: %w", path, err)
	}
	return nil
}

// normalizeFormat hace con el format lo mismo que app.NormalizarFormato del Edge: vacío ⇒ sin campo;
// un objeto (JSON Schema) ⇒ crudo; cualquier otra cosa ⇒ cadena JSON.
func normalizeFormat(format string) json.RawMessage {
	if format == "" {
		return nil
	}
	if format[0] == '{' {
		return json.RawMessage(format)
	}
	quoted, err := json.Marshal(format)
	if err != nil {
		return nil
	}
	return quoted
}

// redondeo deja una duración en milisegundos, que es la escala que se lee.
func roundMs(d time.Duration) time.Duration { return d.Round(time.Millisecond) }
