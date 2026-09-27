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
		cuerpo, errLectura := io.ReadAll(resp.Body)
		if errLectura != nil {
			return "", 0, fmt.Errorf("ollama respondió HTTP %d (y su cuerpo no se pudo leer: %w)", resp.StatusCode, errLectura)
		}
		return "", 0, fmt.Errorf("ollama respondió HTTP %d: %s", resp.StatusCode, string(cuerpo))
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

// ejecutarFrase recorre la cadena entera para una frase: P1 → bifurcación →
// P2 → P3 → P4 → match → resumen. Cada etapa imprime lo suyo y devuelve false
// cuando la cadena no debe seguir.
func ejecutarFrase(ctx context.Context, baseURL, model, texto string, forzarPipeline bool) {
	fmt.Println("\n" + strings.Repeat("=", 80))
	fmt.Printf("📝 MENSAJE A ANALIZAR: \"%s\"\n", texto)
	fmt.Println(strings.Repeat("=", 80))

	clasificacion, ok := etapaP1(ctx, baseURL, model, texto)
	if !ok || !veredictoBifurcacion(clasificacion, forzarPipeline) {
		return
	}
	ideas, ok := etapaP2(ctx, baseURL, model, texto)
	if !ok {
		return
	}
	specs, ok := etapaP3(ctx, baseURL, model, texto, ideas)
	if !ok {
		return
	}
	cantidades, ok := etapaP4(ctx, baseURL, model, texto, specs)
	if !ok {
		return
	}
	resumenDraft(etapaMatch(cantidades))
}

// ETAPA P1: Detección de Intención (Clasificación).
func etapaP1(ctx context.Context, baseURL, model, texto string) (*llm.Classification, bool) {
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
		return nil, false
	}

	jsonP1, err := llm.ExtractJSON(respP1)
	if err != nil {
		fmt.Printf("   ❌ Error extrayendo JSON de P1 (%v). Salida cruda:\n%s\n", err, respP1)
		return nil, false
	}

	clasificacion, err := llm.ParseClassification(jsonP1, inP1)
	if err != nil {
		fmt.Printf("   ❌ Error validando esquema de P1 (%v). JSON:\n%s\n", err, string(jsonP1))
		return nil, false
	}

	fmt.Printf("   ⏱️  Tiempo P1: %v\n", durP1)
	fmt.Printf("   📌 Intención detectada: \033[1;36m%s\033[0m\n", clasificacion.Intent)
	fmt.Printf("   📊 Confianza:           %.2f\n", clasificacion.Confidence)
	fmt.Printf("   🔎 Evidencia copiada:   \"%s\"\n", clasificacion.Evidence)
	return clasificacion, true
}

// veredictoBifurcacion explica qué haría wApp con la clasificación, y dice si
// la cadena sigue hacia P2 (siempre que sea pedido, o si se fuerza).
func veredictoBifurcacion(clasificacion *llm.Classification, forzarPipeline bool) bool {
	esPedido := clasificacion.Intent == "intake_request" && clasificacion.Confidence >= 0.6
	fmt.Println("\n🔀 [VEREDICTO DE BIFURCACIÓN EN WAPP]")
	if esPedido {
		fmt.Println("   \033[1;32m✅ BIFURCA HACIA CAPTACIÓN (intake_request confirmado):\033[0m")
		fmt.Println("      1. En producción, la nube ADELANTA el cierre de la ventana de agregación (no espera 45s).")
		fmt.Println("      2. El job pasa de 'aggregating' a 'pending' en la cola intake_jobs.")
		fmt.Println("      3. Se activa la cadena del Worker: P2 -> P3 -> P4 -> Match -> Draft.")
		return true
	}
	fmt.Printf("   \033[1;33mℹ️  NO BIFURCA HACIA PEDIDO (intención: %s, confianza: %.2f):\033[0m\n", clasificacion.Intent, clasificacion.Confidence)
	fmt.Println("      - No abre ventana de presupuesto ni crea solicitud en la bandeja.")
	fmt.Println("      - Se deriva al flujo estándar de conversación / respuesta rápida.")
	if !forzarPipeline {
		fmt.Println("\n   (Tip: Usa flag -force-pipeline si deseas probar P2-P4 con esta frase)")
		return false
	}
	fmt.Println("\n   ⚠️  [MODO FORCE-PIPELINE ACTIVO: Continuando con P2-P4 de todos modos]")
	return true
}

// ETAPA P2: Extracción de Ideas Principales.
func etapaP2(ctx context.Context, baseURL, model, texto string) (*llm.MainIdeas, bool) {
	fmt.Println("\n💡 [ETAPA P2: Extracción de Ideas Principales (Wants)]")
	inP2 := llm.ExtractMainIdeasInput{
		SourceText: texto,
	}
	promptP2 := llm.BuildExtractMainIdeasPrompt(inP2)
	respP2, durP2, err := llamarOllama(ctx, baseURL, model, promptP2)
	if err != nil {
		fmt.Printf("   ❌ Error en P2: %v\n", err)
		return nil, false
	}

	jsonP2, err := llm.ExtractJSON(respP2)
	if err != nil {
		fmt.Printf("   ❌ Error extrayendo JSON de P2 (%v). Salida cruda:\n%s\n", err, respP2)
		return nil, false
	}

	// Saneo defensivo: si el modelo devuelve "delivery_hint": {}, lo normalizamos a null
	jsonP2 = reEmptyDeliveryHint.ReplaceAll(jsonP2, []byte(`"delivery_hint": null`))

	ideas, err := llm.ParseMainIdeas(jsonP2)
	if err != nil {
		fmt.Printf("   ❌ Error validando esquema de P2 (%v). JSON:\n%s\n", err, string(jsonP2))
		return nil, false
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
		return nil, false
	}
	return ideas, true
}

// ETAPA P3: Especificación de cada Ítem (Fan-out). Una idea que falla se
// salta; la cadena solo se corta si no queda ningún ítem.
func etapaP3(ctx context.Context, baseURL, model, texto string, ideas *llm.MainIdeas) ([]llm.ItemSpec, bool) {
	fmt.Println("\n📋 [ETAPA P3: Especificación Detallada de Ítems]")
	specsRecolectadas := make([]llm.ItemSpec, 0, len(ideas.Wants))

	for i, want := range ideas.Wants {
		fmt.Printf("   -> Procesando idea [%d/%d]: \"%s\"...\n", i+1, len(ideas.Wants), want.Idea)
		specsRecolectadas = append(specsRecolectadas, especificarIdea(ctx, baseURL, model, texto, i, want.Idea)...)
	}

	if len(specsRecolectadas) == 0 {
		fmt.Println("   ⚠️  No se pudieron especificar ítems en P3. Fin del pipeline.")
		return nil, false
	}
	return specsRecolectadas, true
}

// especificarIdea corre P3 para una sola idea e imprime sus ítems.
func especificarIdea(ctx context.Context, baseURL, model, texto string, i int, idea string) []llm.ItemSpec {
	inP3 := llm.ExtractItemSpecsInput{
		SourceText: texto,
		Idea:       idea,
	}
	promptP3 := llm.BuildExtractItemSpecsPrompt(inP3)
	respP3, durP3, err := llamarOllama(ctx, baseURL, model, promptP3)
	if err != nil {
		fmt.Printf("      ❌ Error en P3 para idea %d: %v\n", i+1, err)
		return nil
	}

	jsonP3, err := llm.ExtractJSON(respP3)
	if err != nil {
		fmt.Printf("      ❌ Error extrayendo JSON P3 (%v)\n", err)
		return nil
	}

	itemSpecs, err := llm.ParseItemSpecs(jsonP3)
	if err != nil {
		fmt.Printf("      ❌ Error validando esquema P3 (%v). JSON:\n%s\n", err, string(jsonP3))
		return nil
	}

	fmt.Printf("      ⏱️  Tiempo P3: %v\n", durP3)
	for _, it := range itemSpecs.Items {
		imprimirItemSpec(it)
	}
	return itemSpecs.Items
}

func imprimirItemSpec(it llm.ItemSpec) {
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

// ETAPA P4: Normalización de Cantidades y Fechas.
func etapaP4(ctx context.Context, baseURL, model, texto string, specs []llm.ItemSpec) (*llm.Quantities, bool) {
	fmt.Println("\n🔢 [ETAPA P4: Normalización de Cantidades y Fechas]")
	inP4 := llm.NormalizeQuantitiesInput{
		SourceText: texto,
		Items:      specs,
		MessageTS:  time.Now(),
	}
	promptP4 := llm.BuildNormalizeQuantitiesPrompt(inP4)
	respP4, durP4, err := llamarOllama(ctx, baseURL, model, promptP4)
	if err != nil {
		fmt.Printf("   ❌ Error en P4: %v\n", err)
		return nil, false
	}

	jsonP4, err := llm.ExtractJSON(respP4)
	if err != nil {
		fmt.Printf("   ❌ Error extrayendo JSON de P4 (%v). Salida cruda:\n%s\n", err, respP4)
		return nil, false
	}

	cantidades, err := llm.ParseQuantities(jsonP4)
	if err != nil {
		fmt.Printf("   ❌ Error validando esquema de P4 (%v). JSON:\n%s\n", err, string(jsonP4))
		return nil, false
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
	return cantidades, true
}

// ETAPA MATCH: Cruce determinista con el Catálogo de Productos. Devuelve el
// total estimado de lo que casó.
func etapaMatch(cantidades *llm.Quantities) int {
	fmt.Println("\n🏷️  [ETAPA MATCH: Cruce con Catálogo del Negocio (Sin LLM)]")
	totalEstimado := 0

	for i, norm := range cantidades.Items {
		prod, encontrado := buscarEnCatalogo(norm)
		if !encontrado {
			fmt.Printf("   [%d] \033[1;33m[UNMATCHED]\033[0m Producto no reconocido en catálogo: \"%s\"\n", i+1, norm.Product)
			fmt.Println("       -> La dueña lo revisará en la consola para asignar precio manual.")
			continue
		}
		subtotal := prod.Price * norm.Qty
		totalEstimado += subtotal
		fmt.Printf("   [%d] \033[1;32m[MATCHED]\033[0m %s (SKU: %s)\n", i+1, prod.Name, prod.SKU)
		fmt.Printf("       Cantidad: %d x $%d = $%d\n", norm.Qty, prod.Price, subtotal)
	}
	return totalEstimado
}

// buscarEnCatalogo devuelve el primer producto del catálogo de demo cuya
// palabra clave aparece en el texto del ítem (producto + personalizaciones +
// añadidos), o que contiene el nombre del producto.
func buscarEnCatalogo(norm llm.NormalizedItem) (DemoProduct, bool) {
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
				return prod, true
			}
		}
	}
	return DemoProduct{}, false
}

// RESUMEN DRAFT: lo que la dueña vería en la bandeja.
func resumenDraft(totalEstimado int) {
	fmt.Println("\n📄 [RESULTADO FINAL EN LA BANDEJA (Draft)]")
	fmt.Printf("   • Estado inicial:   \033[1;34mpending_approval\033[0m (Visible en :8107 Consola del Cliente)\n")
	fmt.Printf("   • Total estimado:   \033[1;32m$%d\033[0m\n", totalEstimado)
	fmt.Printf("   • Acciones listas:  [Aprobar y Enviar WhatsApp] [Corregir Precios] [Sugerir Cotización P5]\n")
	fmt.Println(strings.Repeat("=", 80))
}
