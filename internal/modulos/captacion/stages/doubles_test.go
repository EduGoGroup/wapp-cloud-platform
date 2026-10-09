package stages_test

// doubles_test.go — LOS DOBLES COMUNES de los tests de las etapas (sin fichero de
// producción gemelo): el provider, el selector de vía, el store de artefactos y el job
// del caso Ambar. Los usan P2, P3, P4, el tope y el candado del audio. Aquí solo vive lo
// que usa P2, para que este fichero pueda perder la etiqueta con el verde de p2.go; lo
// propio de P3 y de P4 está en p3_test.go y en p4_test.go.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
)

const (
	jobID     = "11111111-1111-1111-1111-111111111111"
	tenantID  = "t-p2"
	sessionID = "s-p2"
	contactID = "c-p2"
	eventID   = "e-p2"
)

// originRoute es cómo fakeSelector anota una petición: tenant y sesión de origen.
const originRoute = tenantID + "/" + sessionID

// errNotThisStage es lo que devuelve el provider para una etapa que el test no esperaba.
// Un error explícito hace ruido el día que una etapa llame a otra; un `nil, nil` no.
var errNotThisStage = errors.New("fake: la etapa bajo prueba no llama a esta operación")

// fakeProvider es un llm.LLMProvider de mentira: cada etapa enchufa SU operación y las
// demás devuelven errNotThisStage.
type fakeProvider struct {
	onMainIdeas  func(context.Context, llm.ExtractMainIdeasInput, llm.Options) (json.RawMessage, error)
	onItemSpecs  func(context.Context, llm.ExtractItemSpecsInput, llm.Options) (json.RawMessage, error)
	onQuantities func(context.Context, llm.NormalizeQuantitiesInput, llm.Options) (json.RawMessage, error)
}

func (p *fakeProvider) ExtractMainIdeas(ctx context.Context, in llm.ExtractMainIdeasInput, opts llm.Options) (json.RawMessage, error) {
	if p.onMainIdeas == nil {
		return nil, errNotThisStage
	}
	return p.onMainIdeas(ctx, in, opts)
}

func (p *fakeProvider) ExtractItemSpecs(ctx context.Context, in llm.ExtractItemSpecsInput, opts llm.Options) (json.RawMessage, error) {
	if p.onItemSpecs == nil {
		return nil, errNotThisStage
	}
	return p.onItemSpecs(ctx, in, opts)
}

func (p *fakeProvider) NormalizeQuantities(ctx context.Context, in llm.NormalizeQuantitiesInput, opts llm.Options) (json.RawMessage, error) {
	if p.onQuantities == nil {
		return nil, errNotThisStage
	}
	return p.onQuantities(ctx, in, opts)
}

func (p *fakeProvider) ClassifyRequest(context.Context, llm.ClassifyRequestInput, llm.Options) (json.RawMessage, error) {
	return nil, errNotThisStage
}

func (p *fakeProvider) GenerateQuoteText(context.Context, llm.GenerateQuoteTextInput, llm.Options) (json.RawMessage, error) {
	return nil, errNotThisStage
}

// fakeSelector es el selector de vía. Anota con qué tenant y con qué sesión de origen
// se le pidió el provider: el segundo dato enruta la inferencia al Edge que recibió el
// mensaje.
type fakeSelector struct {
	provider llm.LLMProvider
	err      error
	asked    []string
}

func (s *fakeSelector) For(_ context.Context, tenant, originSession string) (llm.LLMProvider, error) {
	s.asked = append(s.asked, tenant+"/"+originSession)
	if s.err != nil {
		return nil, s.err
	}
	return s.provider, nil
}

// fakeStore es el doble de la máquina de estados. Valida con la MISMA puerta que el
// adaptador —intake.Artifact.Validate, antes de «escribir»—: si validara con su propia
// regla, los tests probarían al doble y no al sistema.
type fakeStore struct {
	saved []intake.Artifact
	jobs  []string
	// bounded anota, por escritura, si el ctx que llegó traía deadline.
	bounded []bool
	// lost hace que SaveStage devuelva (false, nil): el job dejó de estar en
	// `processing` mientras corría la etapa.
	lost bool
	err  error
}

func (s *fakeStore) SaveStage(ctx context.Context, job string, a intake.Artifact) (bool, error) {
	if err := a.Validate(); err != nil {
		return false, err
	}
	if s.err != nil {
		return false, s.err
	}
	_, hasDeadline := ctx.Deadline()
	s.bounded = append(s.bounded, hasDeadline)
	s.jobs = append(s.jobs, job)
	s.saved = append(s.saved, a)
	return !s.lost, nil
}

// ambarJob es el job reclamado del caso, sin `message_ts` (lo pone P4).
func ambarJob() intake.ClaimedJob {
	return intake.ClaimedJob{
		ID:  jobID,
		Key: intake.WindowKey{TenantID: tenantID, SessionID: sessionID, ContactID: contactID, EventID: eventID},
	}
}

// captureLog devuelve un logger que escribe en el búfer, para afirmar sobre el log.
func captureLog(buf *bytes.Buffer) logger.Logger {
	return logger.New(logger.WithWriter(buf))
}

// ambarWants son las tres ideas del caso como pares idea/evidencia (design §7.1).
func ambarWants() [][2]string {
	return [][2]string{
		{"torta con decoración infantil, chocolate húmedo, 10 o 12 porciones", chocolateCakeEvidence},
		{"torta de vainilla con lluvia de colores, dulce de leche y merengue, 25 o 30 porciones", vanillaCakeEvidence},
		{"paquete de tequeños congelados de 30", tequenosEvidence},
	}
}

// ambarHintText es la pista de entrega del caso, en las palabras del cliente.
const ambarHintText = "el miércoles de la semana que viene"
