//go:build integracion

package procesos

import (
	"context"
	"encoding/pem"
	"fmt"
	"slices"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"
	"google.golang.org/protobuf/proto"
)

// Lo que un test puede mirar del Edge de prueba (textos, configs, diagnósticos, inferencias, leases,
// errores, si puede operar) y las esperas y ayudas comunes con las que lo mira.
// Sale de edge_falso_test.go (D-F9-11: solo se movieron declaraciones).

// ---------------------------------------------------------------------------------------------
// Lo que el test puede mirar
// ---------------------------------------------------------------------------------------------

// Textos devuelve el canal por el que llegan los SendText que el servidor empuja (búfer de 256) y
// que el Edge «entregó»: los que llegaron sin lease vigente no están (se rechazaron con ok=false).
// Cada texto llega una sola vez; leerlo lo saca del canal.
func (e *edge) Textos() <-chan textoRecibido { return e.textos }

// Configs devuelve una copia de los ConfigUpdate recibidos, en orden de llegada, desde que el Edge
// existe (no se vacía al reconectar).
func (e *edge) Configs() []configRecibida {
	e.mu.Lock()
	defer e.mu.Unlock()
	copia := make([]configRecibida, len(e.configs))
	for i, c := range e.configs {
		c.Payload = slices.Clone(c.Payload)
		copia[i] = c
	}
	return copia
}

// Diagnosticos devuelve una copia de los DiagnosticsRequest recibidos, en orden de llegada.
func (e *edge) Diagnosticos() []diagnosticoPedido {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.diagnosticos)
}

// Inferencias devuelve una copia de las InferenceRequest recibidas (calentamientos incluidos, y
// también las que el gate de lease bloqueó), en orden de llegada. Sirve para ver qué pidió la nube:
// el prompt, el techo de tokens, la clase. No dice cuáles se sirvieron: eso lo dice el guion.
func (e *edge) Inferencias() []*cloudlinkv1.InferenceRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	copia := make([]*cloudlinkv1.InferenceRequest, 0, len(e.inferencias))
	for _, r := range e.inferencias {
		if c, ok := proto.Clone(r).(*cloudlinkv1.InferenceRequest); ok {
			copia = append(copia, c)
		}
	}
	return copia
}

// Leases devuelve cuántos LeaseUpdate ha recibido el Edge en la conexión actual (los rechazados y
// las revocaciones cuentan; el rechazo además queda en Errores). Cuenta los YA procesados por el
// Validator: no dice que haya un lease vigente —eso es puedeOperar()—.
func (e *edge) Leases() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.leases
}

// Errores devuelve una copia de los errores que el núcleo anotó: leases rechazados por el
// Validator (errors.Is con cllease.ErrStaleCounter o cllease.ErrBadSignature), envíos que fallaron,
// textos descartados por canal lleno. Un test que no espera ninguno puede exigir que esté vacío.
func (e *edge) Errores() []error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.errores)
}

// puedeOperar dice lo que dice el Validator con la DEK presente: hay un lease vigente aplicado y
// no está revocado ni vencido. Falso antes de recibir el primero. Es el gate de los envíos
// (blockedByLease) y, con gracia, el de la inferencia (inferenceBlockedByLease). El doble no late
// solo: el lease vence a su TTL (15 min por defecto en el servidor) contado desde la última
// renovación, y un proceso que durase más tendría que latir.
func (e *edge) puedeOperar() bool {
	e.mu.Lock()
	v := e.validador
	e.mu.Unlock()
	return v.CanOperate(true)
}

// revocado dice si el Validator vio un lease de revocación (el kill-switch es pegajoso: una vez
// revocado, ningún lease vigente lo deshace).
func (e *edge) revocado() bool {
	e.mu.Lock()
	v := e.validador
	e.mu.Unlock()
	return v.Revoked()
}

// esperarTexto espera, con tope, el siguiente SendText del servidor y lo devuelve. Falla
// (t.Fatalf) si no llega a tiempo o si el test termina mientras tanto.
func (e *edge) esperarTexto(t *testing.T, tope time.Duration) textoRecibido {
	t.Helper()
	txt, err := e.recibirTexto(t.Context(), tope)
	if err != nil {
		t.Fatalf("esperarTexto: %v", err)
	}
	return txt
}

// recibirTexto es la espera de esperarTexto sin test: devuelve el siguiente texto, o un error si
// pasa el tope o se cancela el contexto antes.
func (e *edge) recibirTexto(ctx context.Context, tope time.Duration) (textoRecibido, error) {
	timer := time.NewTimer(tope)
	defer timer.Stop()
	select {
	case txt := <-e.textos:
		return txt, nil
	case <-timer.C:
		return textoRecibido{}, fmt.Errorf("no llegó ningún SendText en %s", tope)
	case <-ctx.Done():
		return textoRecibido{}, fmt.Errorf("se canceló la espera de un SendText: %w", ctx.Err())
	}
}

// esperarConfig espera, con tope, un ConfigUpdate del kind dado y devuelve el primero que haya (los
// recibidos antes de llamar también cuentan). Falla (t.Fatalf) si no llega a tiempo.
func (e *edge) esperarConfig(t *testing.T, kind string, tope time.Duration) configRecibida {
	t.Helper()
	var hallada configRecibida
	edgeEsperar(t, tope, fmt.Sprintf("un ConfigUpdate de kind %q", kind), func() bool {
		for _, c := range e.Configs() {
			if c.Kind == kind {
				hallada = c
				return true
			}
		}
		return false
	})
	return hallada
}

// esperarLeases espera, con tope, a que el Edge haya recibido al menos minimo LeaseUpdate en la
// conexión actual. Falla (t.Fatalf) si no llegan a tiempo.
func (e *edge) esperarLeases(t *testing.T, minimo int, tope time.Duration) {
	t.Helper()
	edgeEsperar(t, tope, fmt.Sprintf("al menos %d LeaseUpdate", minimo), func() bool { return e.Leases() >= minimo })
}

// ---------------------------------------------------------------------------------------------
// Esperas y ayudas comunes
// ---------------------------------------------------------------------------------------------

// edgeEsperar sondea cond cada 25 ms hasta que devuelve true, con tope. Falla el test (t.Fatalf)
// con la descripción si pasa el tope o si el test termina antes. cond se ejecuta en la goroutine del
// test, así que puede llamar a t.Fatalf.
func edgeEsperar(t *testing.T, tope time.Duration, descripcion string, cond func() bool) {
	t.Helper()
	ctx, cancelar := context.WithTimeout(t.Context(), tope)
	defer cancelar()
	if !edgeSondear(ctx, cond) {
		t.Fatalf("pasaron %s sin que se diera: %s", tope, descripcion)
	}
}

// edgeSondear evalúa cond ya y después cada 25 ms hasta que devuelve true (devuelve true) o hasta
// que el contexto termina (devuelve false, tras una última evaluación).
func edgeSondear(ctx context.Context, cond func() bool) bool {
	tic := time.NewTicker(edgeSondeo)
	defer tic.Stop()
	for {
		if cond() {
			return true
		}
		select {
		case <-ctx.Done():
			return cond()
		case <-tic.C:
		}
	}
}

// edgePEM codifica un bloque PEM del tipo dado.
func edgePEM(tipo string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: tipo, Bytes: der})
}

// edgePEMPrimero devuelve el contenido (DER) del primer bloque PEM de datos, que debe ser del tipo
// dado. Falla el test (t.Fatalf) si no hay ningún bloque o es de otro tipo.
func edgePEMPrimero(t *testing.T, tipo string, datos []byte) []byte {
	t.Helper()
	bloque, _ := pem.Decode(datos)
	if bloque == nil || bloque.Type != tipo {
		t.Fatalf("no hay un bloque PEM %q al principio de %.60q", tipo, datos)
	}
	return bloque.Bytes
}
