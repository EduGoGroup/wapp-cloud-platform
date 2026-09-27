package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/EduGoGroup/wapp-shared/llm"
)

// Catálogo de intenciones del sistema para P1
func catalogoIntenciones() []llm.IntentSpec {
	return []llm.IntentSpec{
		{
			Name:        "intake_request",
			Description: "el cliente pide un presupuesto, cotización o quiere encargar productos (ejemplo: quiero encargar una torta, me mandas 2 hamburguesas)",
			Examples: []llm.IntentExample{
				{Message: "quiero encargar una torta para el sábado"},
				{Message: "hola, me mandas 2 hamburguesas con queso"},
				{Message: "necesito presupuesto para 30 empanadas"},
			},
		},
		{
			Name:        "consulta_estado",
			Description: "el cliente pregunta por el estado de un pedido ya realizado",
			Examples: []llm.IntentExample{
				{Message: "cómo va mi pedido"},
				{Message: "ya salió el repartidor con mi encargo?"},
			},
		},
		{
			Name:        "pregunta_frecuente",
			Description: "el cliente consulta horarios, ubicación, métodos de pago o información general del negocio",
			Examples: []llm.IntentExample{
				{Message: "a qué hora abren hoy?"},
				{Message: "dónde están ubicados?"},
				{Message: "aceptan transferencia bancaria?"},
			},
		},
	}
}

// Catálogo de productos de demostración para la etapa de Match
type DemoProduct struct {
	SKU      string
	Name     string
	Keywords []string
	Price    int
}

var catalogoDemo = []DemoProduct{
	{SKU: "HAM-01", Name: "Hamburguesa Clásica", Keywords: []string{"hamburguesa clasica", "hamburguesa tradicional", "hamburguesa simple"}, Price: 6500},
	{SKU: "HAM-02", Name: "Hamburguesa con Queso", Keywords: []string{"hamburguesa con queso", "cheeseburger", "hamburguesa queso"}, Price: 7200},
	{SKU: "HAM-03", Name: "Hamburguesa Doble", Keywords: []string{"hamburguesa doble", "doble carne", "doble queso"}, Price: 8500},
	{SKU: "EMPA-01", Name: "Empanada de Carne", Keywords: []string{"empanada de carne", "empanadas carne", "empanada"}, Price: 2500},
	{SKU: "TEQ-30", Name: "Tequeños Congelados (Pack 30)", Keywords: []string{"tequeños", "tequenos", "tequeños congelados"}, Price: 12000},
	{SKU: "TORT-01", Name: "Torta de Chocolate", Keywords: []string{"torta de chocolate", "torta chocolate", "pastel de chocolate"}, Price: 18000},
	{SKU: "BEB-01", Name: "Gaseosa 1.5L", Keywords: []string{"gaseosa", "coca cola", "bebida", "refresco"}, Price: 2000},
}

type ollamaChatRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Format   string          `json:"format"`
	Stream   bool            `json:"stream"`
	Think    *bool           `json:"think,omitempty"`
	Options  map[string]any  `json:"options"`
}

type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaChatResponse struct {
	Message            ollamaMessage `json:"message"`
	Done               bool          `json:"done"`
	TotalDuration      int64         `json:"total_duration"`
	LoadDuration       int64         `json:"load_duration"`
	PromptEvalCount    int           `json:"prompt_eval_count"`
	PromptEvalDuration int64         `json:"prompt_eval_duration"`
	EvalCount          int           `json:"eval_count"`
	EvalDuration       int64         `json:"eval_duration"`
}

var reEmptyDeliveryHint = regexp.MustCompile(`"delivery_hint"\s*:\s*\{\s*\}`)

func llamarOllama(ctx context.Context, baseURL, model, prompt string) (string, time.Duration, error) {
	think := false
	reqBody := ollamaChatRequest{
		Model: model,
		Messages: []ollamaMessage{
			{Role: "user", Content: prompt},
		},
		Format: "json",
		Stream: false,
		Think:  &think,
		Options: map[string]any{
			"temperature": 0.0,
		},
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", 0, fmt.Errorf("error serializando petición: %w", err)
	}

	inicio := time.Now()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/chat", bytes.NewReader(payload))
	if err != nil {
		return "", 0, fmt.Errorf("error creando http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", 0, fmt.Errorf("error llamando a Ollama en %s: %w", baseURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		cuerpo, _ := io.ReadAll(resp.Body)
		return "", 0, fmt.Errorf("Ollama respondió HTTP %d: %s", resp.StatusCode, string(cuerpo))
	}

	var resWire ollamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&resWire); err != nil {
		return "", 0, fmt.Errorf("error decodificando respuesta de Ollama: %w", err)
	}

	duracion := time.Since(inicio)
	return resWire.Message.Content, duracion, nil
}

func main() {
	modelFlag := flag.String("model", "qwen3:1.7b", "Modelo de Ollama a utilizar")
	ollamaFlag := flag.String("ollama", "http://127.0.0.1:11434", "URL base de Ollama")
	forcePipelineFlag := flag.Bool("force-pipeline", false, "Forzar ejecución del pipeline P2-P4 aunque P1 no sea intake_request")
	flag.Parse()

	args := flag.Args()
	if len(args) > 0 {
		texto := strings.Join(args, " ")
		ejecutarFrase(context.Background(), *ollamaFlag, *modelFlag, texto, *forcePipelineFlag)
		return
	}

	// Modo interactivo
	fmt.Println("================================================================================")
	fmt.Println("  🤖 wApp Debugger de Inferencia & Bifurcación (Ollama Local)")
	fmt.Printf("  Modelo: %s | URL: %s\n", *modelFlag, *ollamaFlag)
	fmt.Println("  Escribe cualquier frase para analizarla (ej: 'quiero una hamburguesa con queso')")
	fmt.Println("  Para salir escribe 'exit' o presiona Ctrl+C")
	fmt.Println("================================================================================")

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("\n💬 Ingresa una frase > ")
		if !scanner.Scan() {
			break
		}
		linea := strings.TrimSpace(scanner.Text())
		if linea == "" {
			continue
		}
		if strings.EqualFold(linea, "exit") || strings.EqualFold(linea, "salir") {
			fmt.Println("Saliendo...")
			break
		}

		ejecutarFrase(context.Background(), *ollamaFlag, *modelFlag, linea, *forcePipelineFlag)
	}
}

func ejecutarFrase(ctx context.Context, baseURL, model, texto string, forzarPipeline bool) {
	fmt.Println("\n" + strings.Repeat("=", 80))
	fmt.Printf("📝 MENSAJE A ANALIZAR: \"%s\"\n", texto)
	fmt.Println(strings.Repeat("=", 80))

	// ========================================================================
	// ETAPA P1: Detección de Intención (Clasificación)
	// ========================================================================
	fmt.Println("\n🔍 [ETAPA P1: Detección de Intención]")
	fmt.Println("   Construyendo prompt con el catálogo de intenciones del tenant...")

	inP1 := llm.ClassifyRequestInput{
		Text:         texto,
		Catalog:      catalogoIntenciones(),
		UnknownLabel: "desconocido",
		Vocabulary:   []string{"hamburguesa", "queso", "empanadas", "tequeños", "torta"},
	}

	promptP1 := llm.BuildClassifyRequestPrompt(inP1)
	respP1, durP1, err := llamarOllama(ctx, baseURL, model, promptP1)
	if err != nil {
		fmt.Printf("   ❌ Error en P1: %v\n", err)
		return
	}

	jsonP1, err := llm.ExtractJSON(respP1)
	if err != nil {
		fmt.Printf("   ❌ Error extrayendo JSON de P1 (%v). Salida cruda:\n%s\n", err, respP1)
		return
	}

	clasificacion, err := llm.ParseClassification(jsonP1, inP1)
	if err != nil {
		fmt.Printf("   ❌ Error validando esquema de P1 (%v). JSON:\n%s\n", err, string(jsonP1))
		return
	}

	fmt.Printf("   ⏱️  Tiempo P1: %v\n", durP1)
	fmt.Printf("   📌 Intención detectada: \033[1;36m%s\033[0m\n", clasificacion.Intent)
	fmt.Printf("   📊 Confianza:           %.2f\n", clasificacion.Confidence)
	fmt.Printf("   🔎 Evidencia copiada:   \"%s\"\n", clasificacion.Evidence)

	// ========================================================================
	// VEREDICTO DE BIFURCACIÓN
	// ========================================================================
	esPedido := clasificacion.Intent == "intake_request" && clasificacion.Confidence >= 0.6
	fmt.Println("\n🔀 [VEREDICTO DE BIFURCACIÓN EN WAPP]")
	if esPedido {
		fmt.Println("   \033[1;32m✅ BIFURCA HACIA CAPTACIÓN (intake_request confirmado):\033[0m")
		fmt.Println("      1. En producción, la nube ADELANTA el cierre de la ventana de agregación (no espera 45s).")
		fmt.Println("      2. El job pasa de 'aggregating' a 'pending' en la cola intake_jobs.")
		fmt.Println("      3. Se activa la cadena del Worker: P2 -> P3 -> P4 -> Match -> Draft.")
	} else {
		fmt.Printf("   \033[1;33mℹ️  NO BIFURCA HACIA PEDIDO (intención: %s, confianza: %.2f):\033[0m\n", clasificacion.Intent, clasificacion.Confidence)
		fmt.Println("      - No abre ventana de presupuesto ni crea solicitud en la bandeja.")
		fmt.Println("      - Se deriva al flujo estándar de conversación / respuesta rápida.")
		if !forzarPipeline {
			fmt.Println("\n   (Tip: Usa flag -force-pipeline si deseas probar P2-P4 con esta frase)")
			return
		}
		fmt.Println("\n   ⚠️  [MODO FORCE-PIPELINE ACTIVO: Continuando con P2-P4 de todos modos]")
	}

	// ========================================================================
	// ETAPA P2: Extracción de Ideas Principales
	// ========================================================================
	fmt.Println("\n💡 [ETAPA P2: Extracción de Ideas Principales (Wants)]")
	inP2 := llm.ExtractMainIdeasInput{
		SourceText: texto,
	}
	promptP2 := llm.BuildExtractMainIdeasPrompt(inP2)
	respP2, durP2, err := llamarOllama(ctx, baseURL, model, promptP2)
	if err != nil {
		fmt.Printf("   ❌ Error en P2: %v\n", err)
		return
	}

	jsonP2, err := llm.ExtractJSON(respP2)
	if err != nil {
		fmt.Printf("   ❌ Error extrayendo JSON de P2 (%v). Salida cruda:\n%s\n", err, respP2)
		return
	}

	// Saneo defensivo: si el modelo devuelve "delivery_hint": {}, lo normalizamos a null
	jsonP2 = reEmptyDeliveryHint.ReplaceAll(jsonP2, []byte(`"delivery_hint": null`))

	ideas, err := llm.ParseMainIdeas(jsonP2)
	if err != nil {
		fmt.Printf("   ❌ Error validando esquema de P2 (%v). JSON:\n%s\n", err, string(jsonP2))
		return
	}

	fmt.Printf("   ⏱️  Tiempo P2: %v\n", durP2)
	fmt.Printf("   📦 Ideas encontradas: %d\n", len(ideas.Wants))
	for i, w := range ideas.Wants {
		fmt.Printf("      [%d] Idea: \"%s\" | Evidencia: \"%s\"\n", i+1, w.Idea, w.Evidence)
	}
	if ideas.DeliveryHint != nil && ideas.DeliveryHint.Text != "" {
		fmt.Printf("      🚚 Pista de entrega: \"%s\" (Evidencia: \"%s\")\n", ideas.DeliveryHint.Text, ideas.DeliveryHint.Evidence)
	}

	if len(ideas.Wants) == 0 {
		fmt.Println("   ⚠️  No se extrajeron ideas en P2. Fin del pipeline.")
		return
	}

	// ========================================================================
	// ETAPA P3: Especificación de cada Ítem (Fan-out)
	// ========================================================================
	fmt.Println("\n📋 [ETAPA P3: Especificación Detallada de Ítems]")
	var specsRecolectadas []llm.ItemSpec

	for i, want := range ideas.Wants {
		fmt.Printf("   -> Procesando idea [%d/%d]: \"%s\"...\n", i+1, len(ideas.Wants), want.Idea)
		inP3 := llm.ExtractItemSpecsInput{
			SourceText: texto,
			Idea:       want.Idea,
		}
		promptP3 := llm.BuildExtractItemSpecsPrompt(inP3)
		respP3, durP3, err := llamarOllama(ctx, baseURL, model, promptP3)
		if err != nil {
			fmt.Printf("      ❌ Error en P3 para idea %d: %v\n", i+1, err)
			continue
		}

		jsonP3, err := llm.ExtractJSON(respP3)
		if err != nil {
			fmt.Printf("      ❌ Error extrayendo JSON P3 (%v)\n", err)
			continue
		}

		itemSpecs, err := llm.ParseItemSpecs(jsonP3)
		if err != nil {
			fmt.Printf("      ❌ Error validando esquema P3 (%v). JSON:\n%s\n", err, string(jsonP3))
			continue
		}

		fmt.Printf("      ⏱️  Tiempo P3: %v\n", durP3)
		for _, it := range itemSpecs.Items {
			specsRecolectadas = append(specsRecolectadas, it)
			fmt.Printf("      • Producto base:     \033[1;37m%s\033[0m\n", it.Product)
			if it.Variant != "" {
				fmt.Printf("        Variante:          %s\n", it.Variant)
			}
			if len(it.AddonCandidates) > 0 {
				fmt.Printf("        Añadidos (addons): %s\n", strings.Join(it.AddonCandidates, ", "))
			}
			if len(it.Customizations) > 0 {
				fmt.Printf("        Personalizaciones: %s\n", strings.Join(it.Customizations, ", "))
			}
			if it.Notes != "" {
				fmt.Printf("        Notas:             %s\n", it.Notes)
			}
		}
	}

	if len(specsRecolectadas) == 0 {
		fmt.Println("   ⚠️  No se pudieron especificar ítems en P3. Fin del pipeline.")
		return
	}

	// ========================================================================
	// ETAPA P4: Normalización de Cantidades y Fechas
	// ========================================================================
	fmt.Println("\n🔢 [ETAPA P4: Normalización de Cantidades y Fechas]")
	inP4 := llm.NormalizeQuantitiesInput{
		SourceText: texto,
		Items:      specsRecolectadas,
		MessageTS:  time.Now(),
	}
	promptP4 := llm.BuildNormalizeQuantitiesPrompt(inP4)
	respP4, durP4, err := llamarOllama(ctx, baseURL, model, promptP4)
	if err != nil {
		fmt.Printf("   ❌ Error en P4: %v\n", err)
		return
	}

	jsonP4, err := llm.ExtractJSON(respP4)
	if err != nil {
		fmt.Printf("   ❌ Error extrayendo JSON de P4 (%v). Salida cruda:\n%s\n", err, respP4)
		return
	}

	cantidades, err := llm.ParseQuantities(jsonP4)
	if err != nil {
		fmt.Printf("   ❌ Error validando esquema de P4 (%v). JSON:\n%s\n", err, string(jsonP4))
		return
	}

	fmt.Printf("   ⏱️  Tiempo P4: %v\n", durP4)
	if cantidades.DeliveryDate != "" {
		fmt.Printf("   📅 Fecha entrega calculada: %s (Base: %s)\n", cantidades.DeliveryDate, cantidades.DeliveryDateBasis)
	}

	for i, norm := range cantidades.Items {
		unidad := "unidades"
		if norm.UnitKind == "package" {
			unidad = fmt.Sprintf("paquetes de %d u.", norm.PackageSize)
		}
		fmt.Printf("   [%d] %d %s x \"%s\"", i+1, norm.Qty, unidad, norm.Product)
		if len(norm.Customizations) > 0 {
			fmt.Printf(" (%s)", strings.Join(norm.Customizations, ", "))
		}
		fmt.Println()
	}

	// ========================================================================
	// ETAPA MATCH: Cruce determinista con el Catálogo de Productos
	// ========================================================================
	fmt.Println("\n🏷️  [ETAPA MATCH: Cruce con Catálogo del Negocio (Sin LLM)]")
	totalEstimado := 0

	for i, norm := range cantidades.Items {
		encontrado := false
		nombreBuscado := strings.ToLower(norm.Product)

		textoCompleto := nombreBuscado
		for _, c := range norm.Customizations {
			textoCompleto += " " + strings.ToLower(c)
		}
		for _, a := range norm.AddonCandidates {
			textoCompleto += " " + strings.ToLower(a)
		}

		for _, prod := range catalogoDemo {
			for _, kw := range prod.Keywords {
				if strings.Contains(textoCompleto, kw) || strings.Contains(kw, nombreBuscado) {
					subtotal := prod.Price * norm.Qty
					totalEstimado += subtotal
					fmt.Printf("   [%d] \033[1;32m[MATCHED]\033[0m %s (SKU: %s)\n", i+1, prod.Name, prod.SKU)
					fmt.Printf("       Cantidad: %d x $%d = $%d\n", norm.Qty, prod.Price, subtotal)
					encontrado = true
					break
				}
			}
			if encontrado {
				break
			}
		}

		if !encontrado {
			fmt.Printf("   [%d] \033[1;33m[UNMATCHED]\033[0m Producto no reconocido en catálogo: \"%s\"\n", i+1, norm.Product)
			fmt.Println("       -> La dueña lo revisará en la consola para asignar precio manual.")
		}
	}

	// ========================================================================
	// RESUMEN DRAFT
	// ========================================================================
	fmt.Println("\n📄 [RESULTADO FINAL EN LA BANDEJA (Draft)]")
	fmt.Printf("   • Estado inicial:   \033[1;34mpending_approval\033[0m (Visible en :8107 Consola del Cliente)\n")
	fmt.Printf("   • Total estimado:   \033[1;32m$%d\033[0m\n", totalEstimado)
	fmt.Printf("   • Acciones listas:  [Aprobar y Enviar WhatsApp] [Corregir Precios] [Sugerir Cotización P5]\n")
	fmt.Println(strings.Repeat("=", 80))
}
