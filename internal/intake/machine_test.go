package intake

import (
	"strings"
	"testing"
)

// machine_test.go — la mitad de T2.1 que NO necesita Postgres: el vocabulario de
// etapas y la puerta del artefacto.
//
// 🔴 VA EN `package intake` (interno) Y NO EN `intake_test`, y es a propósito: el
// test de simetría compara `stageOrder` con el ARRAY escrito dentro de
// `saveStageSQL`, y las dos cosas son unexported. Desde fuera del paquete ese test
// no se puede escribir — y la duplicación quedaría sin custodio.
//
// Lo que muerde de verdad —los guards, el vaciado del sobre, el doble-claim— está
// en machine_integration_test.go, contra Postgres real. Aquí no se simula ningún
// UPDATE: un doble en Go de estas cinco sentencias las reescribiría a mano y la
// suite pasaría a probar el doble.

// TestStageOrder_CoincideConElArrayDelSQL custodia la ÚNICA duplicación de este
// fichero: la secuencia `p2→p3→p4→match→draft` está en Go (stageOrder) y otra vez
// dentro de saveStageSQL, porque `array_position` necesita el array DENTRO de la
// sentencia.
//
// 🔴 QUÉ PASA SI DERIVAN, que es el motivo de que este test exista: si alguien añade
// una etapa a `stageOrder` y no al SQL, `Artifact.Validate` la aceptaría y
// `array_position` devolvería NULL para ella. Una comparación con NULL no es TRUE,
// así que el UPDATE afectaría 0 filas y SaveStage devolvería `false, nil` — «la
// transición no aplicó», sin error, sin pista. Un job se quedaría atascado en su
// etapa anterior para siempre y nadie sabría por qué.
//
// MUTACIÓN QUE LO PONE ROJO (ejecutada): añadir "p5" a stageOrder sin tocar el SQL.
func TestStageOrder_CoincideConElArrayDelSQL(t *testing.T) {
	var b strings.Builder
	b.WriteString("ARRAY[")
	for i, s := range stageOrder {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString("'" + s + "'")
	}
	b.WriteString("]")

	if got := b.String(); got != sqlStageArray {
		t.Fatalf("el array del SQL y stageOrder DERIVARON:\n  stageOrder ⇒ %s\n  sqlStageArray = %s\n"+
			"Una etapa que esté en Go y no en el SQL hace que array_position devuelva NULL y que "+
			"SaveStage devuelva false SIN error: el job se atasca en silencio", got, sqlStageArray)
	}
	// Y el array tiene que estar EN la sentencia, las DOS veces: el guard compara la
	// etapa actual contra la nueva y necesita el array en los dos lados. Editar solo
	// uno dejaría el guard comparando dos vocabularios distintos.
	if n := strings.Count(saveStageSQL, sqlStageArray); n != 2 {
		t.Fatalf("saveStageSQL contiene el array %d veces; ESPERADO 2 (un array por cada lado de la "+
			"comparación de posiciones). Con una sola, el guard compara contra un vocabulario que no es el suyo", n)
	}
}

// TestStageIndex_ConoceLasCincoYSoloLasCinco fija el vocabulario CERRADO contra el
// CHECK `intake_jobs_stage_check` de la 0072. Cinco etapas, en ese orden, y nada más.
func TestStageIndex_ConoceLasCincoYSoloLasCinco(t *testing.T) {
	for i, s := range []string{"p2", "p3", "p4", "match", "draft"} {
		if got := StageIndex(s); got != i {
			t.Errorf("StageIndex(%q) = %d; ESPERADO %d — el orden ES la máquina: es lo que "+
				"impide que una reanudación retroceda", s, got, i)
		}
	}
	for _, s := range []string{"", "p1", "p5", "P2", "match ", "done"} {
		if got := StageIndex(s); got != -1 {
			t.Errorf("StageIndex(%q) = %d; ESPERADO -1 — una etapa fuera del vocabulario tiene que "+
				"morir en Go, no en el CHECK de Postgres (que devuelve un error del motor sin decir "+
				"cuáles eran las etapas válidas)", s, got)
		}
	}
}

// TestIsTerminal_SoloDoneYFailed. Los terminales son los que disparan INV-13, así
// que confundir uno tiene consecuencia: un estado que se creyera terminal sin serlo
// dejaría el literal puesto.
func TestIsTerminal_SoloDoneYFailed(t *testing.T) {
	for _, s := range []string{StatusDone, StatusFailed} {
		if !IsTerminal(s) {
			t.Errorf("IsTerminal(%q) = false; ESPERADO true", s)
		}
	}
	for _, s := range []string{StatusAggregating, StatusPending, StatusProcessing, ""} {
		if IsTerminal(s) {
			t.Errorf("IsTerminal(%q) = true; ESPERADO false", s)
		}
	}
}

// TestArtifact_Validate es LA PUERTA del criterio «artefacto inválido JAMÁS se
// persiste», medida donde se decide: antes de tocar la base.
//
// 🔴 EL CASO QUE IMPORTA es «objeto JSON válido SIN version». Los otros —JSON roto,
// array, escalar— los rechazaría también el `::jsonb` o el `||` de Postgres, así que
// un test que solo los llevara pasaría igual con la validación BORRADA: sería
// tautológico. El objeto sin `version` es JSON perfectamente válido y Postgres lo
// escribiría encantado; es el único caso que prueba que esta puerta existe.
//
// MUTACIÓN QUE LO PONE ROJO (ejecutada): borrar la comprobación de `version` de
// Artifact.Validate.
func TestArtifact_Validate(t *testing.T) {
	casos := []struct {
		nombre string
		art    Artifact
		quiero bool // true = válido
		porQue string
	}{
		{"etapa desconocida", Artifact{Stage: "p5", Payload: []byte(`{"version":1}`)}, false,
			"el vocabulario es CERRADO por el CHECK de la 0072"},
		{"etapa vacía", Artifact{Stage: "", Payload: []byte(`{"version":1}`)}, false,
			"sin etapa no hay clave bajo la que guardar el artefacto"},
		{"payload vacío", Artifact{Stage: StageP2}, false,
			"un artefacto sin payload no es un artefacto"},
		{"JSON roto", Artifact{Stage: StageP2, Payload: []byte(`{"version":1`)}, false,
			"el texto crudo acabaría en el mensaje de error del motor — y puede llevar literal del cliente"},
		{"array en vez de objeto", Artifact{Stage: StageP3, Payload: []byte(`[{"version":1}]`)}, false,
			"`artifacts` es un objeto por etapa: un array ahí dentro rompe a todo lector"},
		{"escalar", Artifact{Stage: StageP3, Payload: []byte(`3`)}, false, "ídem"},
		{"objeto SIN version", Artifact{Stage: StageP4, Payload: []byte(`{"fechas":[]}`)}, false,
			"🔴 ESTE es el que prueba que la puerta existe: Postgres lo aceptaría sin rechistar"},
		{"version 0", Artifact{Stage: StageP4, Payload: []byte(`{"version":0}`)}, false,
			"la numeración empieza en 1 (design §3.2); un 0 es un campo puesto por rellenar"},
		{"version como texto", Artifact{Stage: StageMatch, Payload: []byte(`{"version":"1"}`)}, false,
			"un tipo distinto hoy es un lector roto mañana"},
		{"objeto vacío", Artifact{Stage: StageMatch, Payload: []byte(`{}`)}, false,
			"tampoco lleva version"},
		{"válido mínimo", Artifact{Stage: StageP2, Payload: []byte(`{"version":1}`)}, true, ""},
		{"válido con contenido", Artifact{Stage: StageDraft,
			Payload: []byte(`{"version":2,"lines":[{"sku":"a"}]}`)}, true, ""},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			err := c.art.Validate()
			if c.quiero && err != nil {
				t.Fatalf("Validate() = %v; ESPERADO nil — %s", err, c.nombre)
			}
			if !c.quiero && err == nil {
				t.Fatalf("Validate() = nil; ESPERADO error — %s", c.porQue)
			}
		})
	}
}

// TestArtifact_Validate_ElErrorNoCitaElPayload. El payload de una etapa puede llevar
// literal del cliente (P2 guarda `evidence`, que son frases suyas). Un error que lo
// vuelque acaba en el log, y ADR-0034 lo prohíbe: el log no es sitio para eso.
func TestArtifact_Validate_ElErrorNoCitaElPayload(t *testing.T) {
	// textoEnClaro NO es una credencial: es el LITERAL DEL CLIENTE, la frase que
	// viaja dentro del sobre cifrado y que P2 copia en `evidence`. El nombre importa:
	// llamarlo `secreto` disparaba el G101 de gosec por heurística de nombre, y el
	// falso positivo tapaba la lectura del test.
	const textoEnClaro = "quiero 12 porciones para el jueves"
	art := Artifact{Stage: StageP2, Payload: []byte(`{"evidence":"` + textoEnClaro + `"`)} // JSON roto a propósito
	err := art.Validate()
	if err == nil {
		t.Fatalf("Validate() = nil sobre un JSON roto")
	}
	if strings.Contains(err.Error(), textoEnClaro) {
		t.Fatalf("el error VUELCA el payload: %v — un mensaje de error acaba en el log, y el "+
			"literal del cliente no puede acabar ahí (ADR-0034)", err)
	}
}
