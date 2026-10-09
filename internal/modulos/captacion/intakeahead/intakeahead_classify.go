// Porta internal/intakeahead/intakeahead.go @ 56097aa (E-13: la mitad «clasificar» del
// fichero viejo, partida por tema; el contrato vive en intakeahead.go).

package intakeahead

import (
	"context"
	"errors"
	"time"

	"github.com/EduGoGroup/wapp-shared/intents"
	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intentcfg"
)

// serve (antes `atender`) resuelve UNA petición: pide la clasificación con su propio
// presupuesto y, si sale algo, se lo entrega al sink. Suelta el cerrojo pase lo que
// pase.
func (p *Pool) serve(ctx context.Context, req request) {
	defer p.release(req.key)

	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	c, ok := p.classify(ctx, req)
	if !ok {
		return
	}
	p.sink.OnClassified(req.key, c.Intent, c.Confidence)
}

// classify (antes `clasificar`) arma el prompt desde el catálogo del tenant, pide P1
// por la vía configurada y devuelve la clasificación ya saneada.
//
// Devuelve (nil, false) en TODOS los caminos de fallo, y ninguno de ellos es un
// error hacia arriba: no hay nadie arriba a quien devolvérselo, y lo que se pierde
// es un adelanto (REQ-35).
func (p *Pool) classify(ctx context.Context, req request) (*llm.Classification, bool) {
	in, ok := p.input(ctx, "adelanto", req.key.TenantID, req.text)
	if !ok {
		return nil, false
	}
	prov, err := p.sel.For(ctx, req.key.TenantID, req.key.SessionID)
	if err != nil {
		// El aviso al dueño ya lo escribió el selector si el error tenía motivo
		// (T1.6-6): aquí NO se duplica el mapeo error→motivo ni se inventa uno.
		p.log.Debug("adelanto: sin proveedor LLM para el tenant; la ventana cerrará por su reloj",
			"tenant_id", req.key.TenantID, "error", err)
		return nil, false
	}
	c, err := p.ask(ctx, prov, in)
	if err != nil {
		p.log.Debug("adelanto: la clasificación no salió; la ventana cerrará por su reloj",
			"tenant_id", req.key.TenantID, "session_id", req.key.SessionID, "error", err)
		return nil, false
	}
	if !p.accept(c, in, req.key) {
		return nil, false
	}
	return c, true
}

// input (antes `entrada`) arma la ClassifyRequestInput desde el catálogo publicado del
// tenant.
//
// 🔴 TIENE DOS LLAMANTES Y ESO ES EL PUNTO, no una casualidad: la clasificación real
// (classify) y el calentamiento de la caché de prefijo (Warm, warmup.go). El
// calentamiento solo sirve si el prefijo que deja cacheado es EL MISMO BYTE A BYTE que
// el de la P1 real, y todo lo que forma ese prefijo —catálogo aplanado, vocabulario,
// etiqueta de desconocido— se decide aquí. Dos funciones «que hacen lo mismo» habrían
// divergido en un campo el día que alguien tocara una y no la otra, y el síntoma
// habría sido un calentamiento que calienta un prompt que nadie pide: cero error, cero
// log, y la latencia igual que antes. `who` (antes `quien`) solo cambia el rótulo del
// log.
//
// Sin catálogo NO SE PREGUNTA NADA, y no es una guarda defensiva: con `Catalog`
// vacío el parser rechaza cualquier artefacto (su docstring lo dice: «un catálogo
// vacío rechaza TODO a propósito»), así que preguntar sería gastar una inferencia de
// ocho segundos para tirarla. Un tenant sin config de intents es un estado NORMAL —la
// mayoría lo está—, así que no se loguea y no hay aviso: es un motivo SANO y REQ-38
// manda que los sanos no notifiquen.
func (p *Pool) input(ctx context.Context, who, tenantID, text string) (llm.ClassifyRequestInput, bool) {
	cfg, err := p.cfg.Get(ctx, tenantID)
	if err != nil {
		if !errors.Is(err, intentcfg.ErrNotFound) {
			p.log.Warn(who+": no se pudo leer el catálogo de intenciones del tenant",
				"tenant_id", tenantID, "error", err)
		}
		return llm.ClassifyRequestInput{}, false
	}
	cat, err := intents.ParseAndValidate(cfg.Blob)
	if err != nil {
		// El blob se validó al publicarlo (PUT /api/v1/intents), así que llegar aquí
		// significa que la fila se escribió por fuera del API o que el contrato cambió
		// bajo los pies. Es un fallo de CONFIGURACIÓN, no de la vía: se dice y no se
		// notifica como degradación.
		p.log.Warn(who+": el catálogo publicado del tenant no valida; no se pide inferencia",
			"tenant_id", tenantID, "version", cfg.Version, "error", err)
		return llm.ClassifyRequestInput{}, false
	}
	return llm.ClassifyRequestInput{
		Text:         text,
		Catalog:      flatten(cat),
		UnknownLabel: intents.ReservedUnknown,
		Vocabulary:   cat.Vocabulario,
	}, true
}

// flatten (antes `aplanar`) traduce el catálogo de negocio del tenant a la forma POBRE
// que el prompt necesita. Es la traducción que el módulo llm NO hace a propósito: su
// doc.go declara que no importa otro módulo de wapp-shared, así que el puente lo pone
// el caller.
//
// ⚠️ `params: []` VIAJA TAL CUAL Y ES LA FORMA CORRECTA (D-044.20). El intent
// publicado en campo declara la lista vacía a propósito: la salida útil de P1 es «esto
// es una solicitud de pedido», no la lista de productos. Quien descompone en ítems es
// P2–P4, en la nube y sobre el texto acumulado. Rellenar aquí unos params «que faltan»
// sería deshacer esa decisión.
func flatten(cat *intents.Config) []llm.IntentSpec {
	out := make([]llm.IntentSpec, 0, len(cat.Intents))
	for _, it := range cat.Intents {
		spec := llm.IntentSpec{
			Name:        it.Name,
			Description: it.Descripcion,
			Params:      it.Params,
			Examples:    make([]llm.IntentExample, 0, len(it.Ejemplos)),
		}
		for _, ex := range it.Ejemplos {
			spec.Examples = append(spec.Examples, llm.IntentExample{Message: ex.Mensaje, Params: ex.Params})
		}
		out = append(out, spec)
	}
	return out
}

// ask (antes `pedir`) hace la llamada y, si la salida no fue interpretable, REINTENTA
// UNA VEZ a TemperatureRetry (REQ-02/REQ-03).
//
// El reintento es del CALLER por contrato —los dos adaptadores lo dicen en su
// docstring y ninguno reintenta por su cuenta—, y es UNO: el segundo fallo de calidad
// seguido no es mala suerte, es que el modelo no sabe responder esto, y una tercera
// pasada solo gasta ventana.
//
// 🔴 EL FALLO DE CALIDAD NO AVISA AL DUEÑO, y no hace falta hacer nada para eso: el
// decorador de llmvia trata ErrLLMQuality como «el proveedor funciona y el modelo
// escribió mal un JSON» y no escribe fila. Aquí solo hay que no confundirlo con un
// fallo de vía, que es justo lo que hace el errors.Is.
//
// # 🔴 EL REINTENTO SOLO ARRANCA SI LE QUEDA PLAZO PARA SER UN REINTENTO
//
// Las dos pasadas comparten UN presupuesto (el de serve), y desde que el adaptador
// hereda el plazo eso tiene una consecuencia que antes quedaba tapada por su reloj
// propio: si la primera pasada consumió casi todo, la segunda arranca con lo que
// sobre. Un reintento que empieza con dos segundos no es un reintento, es un fallo
// garantizado que además OCUPA UN WORKER y, peor, produce un `timeout` CON motivo —o
// sea, un aviso de degradación al dueño por una avería que no existe—.
//
// SE DESCARTÓ REPARTIR MITAD Y MITAD, que era la otra opción sobre la mesa: partir 45 s
// en dos mitades de 22,5 s dejaría el presupuesto de la PRIMERA pasada por debajo del
// máximo real medido en campo (36,5 s), o sea, penalizaría el caso frecuente —una
// inferencia normal que no falla de calidad— para financiar el raro. Habría cambiado un
// defecto por su espejo.
//
// El criterio es MEDIA VUELTA DEL PRESUPUESTO, comprobada sobre el reloj real y no
// sobre un número aparte: se reintenta si al ctx le queda al menos la mitad de lo que
// se le dio. Da lo mismo que el reparto en el caso bueno —una primera pasada corta deja
// sitio de sobra— sin gravar el caso en que la primera pasada necesita el presupuesto
// entero, y se auto-escala si alguien mueve el plazo con WithTimeout. Sin deadline no
// se comprueba nada: no hay presupuesto que repartir.
func (p *Pool) ask(ctx context.Context, prov llm.LLMProvider, in llm.ClassifyRequestInput) (*llm.Classification, error) {
	c, err := attempt(ctx, prov, in, llm.TemperatureGreedy)
	if err == nil || !errors.Is(err, llm.ErrLLMQuality) {
		return c, err
	}
	if !p.retryFits(ctx) {
		// Se devuelve el error de calidad ORIGINAL, no uno de plazo: lo que pasó de
		// verdad es que el modelo escribió mal el JSON, y esa sigue siendo la causa que
		// el llamante tiene que registrar. Inventar aquí un error nuevo cambiaría la
		// familia del fallo —y con ella la decisión de avisar al dueño— por un detalle
		// de nuestra política de reintento.
		return nil, err
	}
	return attempt(ctx, prov, in, llm.TemperatureRetry)
}

// retryFits (antes `cabeElReintento`) dice si al presupuesto le queda al menos la
// MITAD, que es el umbral razonado en el docstring de ask. Sin deadline devuelve true:
// no hay plazo que agotar.
func (p *Pool) retryFits(ctx context.Context) bool {
	dl, ok := ctx.Deadline()
	if !ok {
		return true
	}
	return time.Until(dl) >= p.timeout/2
}

// attempt (antes `intentar`) es UNA pasada: pedir y parsear. Los dos pasos fallan con
// el MISMO centinela cuando el problema es la salida del modelo, que es lo que permite
// que el reintento de arriba se decida con una sola comprobación.
func attempt(ctx context.Context, prov llm.LLMProvider, in llm.ClassifyRequestInput, temp float64) (*llm.Classification, error) {
	raw, err := prov.ClassifyRequest(ctx, in, llm.Options{Temperature: temp})
	if err != nil {
		return nil, err
	}
	return llm.ParseClassification(raw, in)
}

// accept (antes `aceptar`) aplica el SANEO (ver sanitize.go) y decide si la
// clasificación se entrega.
//
// Rechazar aquí NO es un fallo de la vía y no escribe aviso: el proveedor respondió,
// el cable funcionó, y lo que pasó es que la respuesta no se sostiene sobre el texto
// del cliente. Es exactamente la familia de ErrLLMQuality y se trata igual.
func (p *Pool) accept(c *llm.Classification, in llm.ClassifyRequestInput, key intake.WindowKey) bool {
	evidenceOK, dropped := sanitize(c, in)
	if dropped > 0 {
		// El NÚMERO sí va al log; los valores NO (INV-6: son texto del cliente).
		p.log.Debug("adelanto: params descartados por el allowlist",
			"tenant_id", key.TenantID, "intent", c.Intent, "descartados", dropped)
	}
	if !evidenceOK {
		p.log.Debug("adelanto: la evidencia no aparece en el mensaje; la clasificación se descarta",
			"tenant_id", key.TenantID, "session_id", key.SessionID, "intent", c.Intent)
		return false
	}
	return true
}
