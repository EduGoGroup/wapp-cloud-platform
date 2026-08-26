package llmvia

import (
	"context"
	"io"
	"testing"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/llmvia/local"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/tenantllm"
)

// TestWithLocalOptions_localOptsAcumulaLasDosLlamadas es la prueba de ESTRUCTURA
// (caja blanca, package llmvia): inspecciona s.localOpts tras NewSelector con las
// MISMAS dos llamadas a WithLocalOptions que bootstrap.go hace, en el mismo orden.
// NO prueba conducta observable — para eso está
// TestWithLocalOptions_AcumulaEntreLlamadas en withlocaloptions_acumula_test.go
// (package llmvia_test), que mide el prompt que llega al frame. Esta prueba solo
// confirma el MECANISMO: cuántas funciones quedan en el slice tras las dos llamadas.
//
// El invariante es len == 2: cada llamada a WithLocalOptions AÑADE al slice, no lo
// reemplaza. Si alguien vuelve a escribir `s.localOpts = opts` en vez de
// `append(s.localOpts, opts...)`, esto cae a 1 y el test se pone rojo.
func TestWithLocalOptions_localOptsAcumulaLasDosLlamadas(t *testing.T) {
	t.Parallel()

	plantillas := map[llm.Etapa]llm.Plantilla{
		llm.EtapaP2: {Instruccion: "x", Esquema: "y"},
	}

	s, err := NewSelector(storeStub{}, logger.New(logger.WithWriter(io.Discard)),
		WithLocalOptions(local.ConPlantillas(plantillas)),
		WithLocalOptions(local.WithMaxOutputTokens(true)),
	)
	if err != nil {
		t.Fatalf("NewSelector: %v", err)
	}

	if got := len(s.localOpts); got != 2 {
		t.Fatalf("len(s.localOpts) = %d, quiero 2: cada llamada a WithLocalOptions debe "+
			"ACUMULAR (append), no PISAR (asignación) — si esto da 1, alguien volvió a "+
			"escribir `s.localOpts = opts`", got)
	}
}

// storeStub satisface llmvia.Store con lo mínimo para construir un Selector; este
// fichero no llama a For/Warm, así que sus métodos no se ejercitan de verdad.
type storeStub struct{}

func (storeStub) Get(context.Context, string) (tenantllm.Config, bool, error) {
	panic("no debería llamarse en este test")
}

func (storeStub) APIKey(context.Context, string) (string, error) {
	panic("no debería llamarse en este test")
}
