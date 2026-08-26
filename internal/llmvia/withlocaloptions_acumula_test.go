package llmvia_test

import (
	"context"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/llmvia"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/llmvia/local"
)

// TestWithLocalOptions_AcumulaEntreLlamadas defiende el invariante de
// WithLocalOptions (llmvia.go): DOS llamadas separadas dentro de la MISMA
// construcción de NewSelector deben ACUMULARSE, no pisarse. Reproduce el cableado
// REAL de internal/bootstrap/bootstrap.go (nuevoStackLLMDeCaptacion, ~líneas
// 995 y 1000): primero local.ConPlantillas(plantillas), luego por separado
// local.WithMaxOutputTokens(...), en ESE orden.
//
// Hasta el arreglo, WithLocalOptions ASIGNABA (`s.localOpts = opts`) en vez de
// acumular, así que la segunda llamada pisaba por completo a la primera y
// ConPlantillas nunca llegaba al local.Provider: la palanca WAPP_LLM_PROMPTS_DIR
// (docs/funcionalidades/36-...) quedaba muerta en silencio. Este test mide
// CONDUCTA OBSERVABLE —el prompt que de verdad viaja al frame (transporte)—, no el
// estado interno del Selector: para eso está la prueba de caja blanca hermana,
// TestWithLocalOptions_localOptsAcumulaLasDosLlamadas en
// withlocaloptions_acumula_whitebox_test.go.
func TestWithLocalOptions_AcumulaEntreLlamadas(t *testing.T) {
	t.Parallel()

	const marcaAjustada = "INSTRUCCION-AJUSTADA-DE-TEST-QUE-NO-EXISTE-EN-LA-COMPILADA"
	plantillas := map[llm.Etapa]llm.Plantilla{
		llm.EtapaP2: {
			Instruccion: marcaAjustada,
			Esquema:     "ESQUEMA-AJUSTADO-DE-TEST",
		},
	}

	f := &frameFake{out: `{"version":1,"wants":[{"idea":"x","evidence":"y"}]}`}

	// MISMO ORDEN que bootstrap.go: primero ConPlantillas, luego WithMaxOutputTokens,
	// ambas como llamadas SEPARADAS a llmvia.WithLocalOptions.
	p, err := selector(t, &storeFake{hay: false},
		llmvia.WithFrame(f),
		llmvia.WithLocalOptions(local.ConPlantillas(plantillas)),
		llmvia.WithLocalOptions(local.WithMaxOutputTokens(true)),
	).For(context.Background(), "t1", "")
	if err != nil {
		t.Fatalf("For: %v", err)
	}

	// El error de ExtractMainIdeas no importa para esta afirmación: Provider.run
	// manda el prompt al frame ANTES de intentar parsear la respuesta, y el frame
	// falso no devuelve un JSON interpretable. Se recoge igualmente porque el
	// errcheck de esta casa corre con check-blank: `_ =` no exime de mirar.
	if _, errIdeas := p.ExtractMainIdeas(context.Background(), llm.ExtractMainIdeasInput{SourceText: "hola"}, llm.Options{}); errIdeas != nil {
		t.Logf("ExtractMainIdeas devolvió %v (esperado: el frame falso no responde JSON); lo que importa es el prompt que YA viajó", errIdeas)
	}

	if !strings.Contains(f.visto.Prompt, marcaAjustada) {
		t.Fatalf("la plantilla ajustada por ConPlantillas NO llegó al prompt del frame "+
			"(prompt real: %q): la 2.ª llamada a WithLocalOptions (WithMaxOutputTokens) "+
			"pisó a la 1.ª (ConPlantillas) en vez de acumularse", f.visto.Prompt)
	}
	// Y LA SEGUNDA LLAMADA TAMBIÉN TIENE QUE SOBREVIVIR: acumular no vale si arregla
	// una pisada cambiándola de sentido. Con local.WithMaxOutputTokens(true) el campo
	// del frame viaja con el techo de la etapa (etapaP2 = 512), nunca ausente (0).
	if f.visto.MaxOutputTokens == 0 {
		t.Fatalf("MaxOutputTokens llegó en 0: la 1.ª llamada a WithLocalOptions " +
			"(ConPlantillas) pisó a la 2.ª (WithMaxOutputTokens) — las dos deben " +
			"sobrevivir, no solo una")
	}
}
