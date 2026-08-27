package intakes_test

// correct_test.go — LA ACCIÓN «CORREGIR» DEL 044 (Plan 044 · Ola 4 · T4.4).
//
// `correct` es el `PUT …/items` del 041 con un campo más (D-044.48 §1), así que este
// fichero tiene DOS trabajos y el primero pesa tanto como el segundo:
//
//  1. demostrar que SIN el campo no cambió nada — ni un byte del payload, ni el
//     estado, ni las precondiciones;
//  2. demostrar que CON el campo se produce y se guarda la señal few-shot, y que el
//     empuje al CRM NO depende de él.
//
// Lo que aquí NO se prueba, porque no existe: que el few-shot funcione.
// `quote_style_examples` no tiene un solo consumidor en producción (Ola 5).

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// líneaCorregida es la edición que manda el dueño en todos los casos de abajo: una
// sola línea, para que lo que se compare sea el payload y no la aritmética.
var líneaCorregida = []intakes.Item{{SKU: "torta-v1", Label: "Torta 15 porciones", Qty: 1, UnitPrice: 22000}}

// escenaCorregir arma la solicitud por aprobar con el espía del CRM cableado: es la
// única forma de mirar el empuje, que es la mitad no obvia de esta tarea.
type escenaCorregir struct {
	svc   *intakes.Service
	store *intakes.MemoryStore
	crm   *crmSpy
}

func nuevaEscenaCorregir(t *testing.T, status string) *escenaCorregir {
	t.Helper()
	st := seedStore(t, status)
	crm := &crmSpy{}
	return &escenaCorregir{svc: intakes.NewService(st, intakes.WithCRMPusher(crm)), store: st, crm: crm}
}

// --- (1) CERO REGRESIÓN DEL 041 ---------------------------------------------

// TestCorregir_SinElCampoElPayloadNoCambiaNiUnByte es EL test de la no-regresión, y
// se escribe contra el JSON literal a propósito.
//
// Comparar campo a campo pasaría igual con una clave de más: `as_correction:false`,
// un `correction:null`, un objeto vacío. Lo que el 041 promete —y lo que consumen sus
// golden files y cualquier integrador que lea la revisión— es la FORMA que sale, así
// que la afirmación tiene que ser sobre los bytes.
func TestCorregir_SinElCampoElPayloadNoCambiaNiUnByte(t *testing.T) {
	e := nuevaEscenaCorregir(t, intakes.StatusPendingApproval)

	detail, err := e.svc.ReplaceItems(context.Background(), tenantA, intakeDePrueba, líneaCorregida, intakes.EditPlain)
	if err != nil {
		t.Fatalf("ReplaceItems: %v", err)
	}

	const quiero = `{"version":1,"total":22000,"items":[{"sku":"torta-v1","label":"Torta 15 porciones","qty":1,"unit_price":22000}]}`
	got := string(últimaRevisión(t, detail.Revisions).Payload)
	if got != quiero {
		t.Fatalf("el payload del PUT del 041 CAMBIÓ.\ngot   =%s\nquiero=%s", got, quiero)
	}
}

// TestCorregir_SinElCampoNoHayNiUnaClaveDeLaSeñal lo dice por el otro lado: ninguna
// de las tres claves del contrato aparece. Es redundante con el test de arriba —y a
// propósito—: aquél se romperá el día que alguien reordene un campo del struct, y
// entonces conviene tener a mano una afirmación que solo hable de la señal.
func TestCorregir_SinElCampoNoHayNiUnaClaveDeLaSeñal(t *testing.T) {
	e := nuevaEscenaCorregir(t, intakes.StatusPendingApproval)

	detail, err := e.svc.ReplaceItems(context.Background(), tenantA, intakeDePrueba, líneaCorregida, intakes.EditPlain)
	if err != nil {
		t.Fatalf("ReplaceItems: %v", err)
	}

	raíz := objetoDelPayload(t, últimaRevisión(t, detail.Revisions).Payload)
	for _, clave := range []string{intakes.ClaveAsCorrection, intakes.ClaveCorrectsRevisionNo, intakes.ClaveCorrectsKind} {
		if _, hay := raíz[clave]; hay {
			t.Fatalf("la edición del 041 dejó %q en el payload: %s", clave, raíz[clave])
		}
	}
}

// --- (2) LA SEÑAL FEW-SHOT --------------------------------------------------

// TestCorregir_ConElCampoGuardaLaSeñalYELPar: la marca sola no enseña nada. Un
// few-shot aprende de «esto propuso la máquina, esto dejó la persona», así que la
// señal tiene que nombrar la revisión corregida y su clase.
func TestCorregir_ConElCampoGuardaLaSeñalYELPar(t *testing.T) {
	e := nuevaEscenaCorregir(t, intakes.StatusPendingApproval)
	sembrarRevisión(t, e.store, intakes.RevisionKindCart, `{"version":1,"total":18000,"items":[]}`)
	sembrarRevisión(t, e.store, intakes.RevisionKindInterpreted, `{"version":1,"lines":[]}`)

	detail, err := e.svc.ReplaceItems(context.Background(), tenantA, intakeDePrueba, líneaCorregida, intakes.EditAsCorrection)
	if err != nil {
		t.Fatalf("ReplaceItems: %v", err)
	}

	rev := últimaRevisión(t, detail.Revisions)
	if rev.RevisionNo != 3 {
		t.Fatalf("la corrección es la revisión %d, quiero la 3 (dos sembradas + ésta)", rev.RevisionNo)
	}
	señal := señalDe(t, rev.Payload)
	if !señal.AsCorrection {
		t.Fatalf("la revisión no lleva la marca de corrección: %s", rev.Payload)
	}
	if señal.CorrectsRevisionNo != 2 {
		t.Fatalf("corrects_revision_no=%d, quiero 2: es el borrador que el dueño estaba corrigiendo",
			señal.CorrectsRevisionNo)
	}
	if señal.CorrectsKind != intakes.RevisionKindInterpreted {
		t.Fatalf("corrects_kind=%q, quiero %q: es lo que le permite a la Ola 5 quedarse solo con las "+
			"correcciones que enseñan algo sobre la interpretación del LLM",
			señal.CorrectsKind, intakes.RevisionKindInterpreted)
	}
	if rev.Kind != intakes.RevisionKindCorrected || rev.CreatedBy != intakes.RevisionByOwner {
		t.Fatalf("revisión kind=%q created_by=%q; la corrección del 044 sigue siendo corrected/owner",
			rev.Kind, rev.CreatedBy)
	}
}

// TestCorregir_SinRevisionesPreviasDejaLaMarcaSinElPar: una solicitud que llegó a
// `pending_approval` sin que nadie la retratara. La marca se guarda igual y el par se
// omite en vez de inventar un `corrects_revision_no: 0`, que apuntaría a una revisión
// que no existe (y que el contrato del CRM rechaza justo por ese valor).
func TestCorregir_SinRevisionesPreviasDejaLaMarcaSinElPar(t *testing.T) {
	e := nuevaEscenaCorregir(t, intakes.StatusPendingApproval)

	detail, err := e.svc.ReplaceItems(context.Background(), tenantA, intakeDePrueba, líneaCorregida, intakes.EditAsCorrection)
	if err != nil {
		t.Fatalf("ReplaceItems: %v", err)
	}

	raíz := objetoDelPayload(t, últimaRevisión(t, detail.Revisions).Payload)
	if _, hay := raíz[intakes.ClaveAsCorrection]; !hay {
		t.Fatal("sin revisiones previas se perdió la marca: la corrección la declaró el dueño igual")
	}
	for _, clave := range []string{intakes.ClaveCorrectsRevisionNo, intakes.ClaveCorrectsKind} {
		if _, hay := raíz[clave]; hay {
			t.Fatalf("%q viaja apuntando a una revisión que no existe: %s", clave, raíz[clave])
		}
	}
}

// TestCorregir_LasClavesSonLasDelContrato ata las constantes públicas a las etiquetas
// JSON reales por reflexión. Sin esto, renombrar una etiqueta dejaría los tests de
// arriba buscando una clave que ya no se escribe y pasando por la razón equivocada.
func TestCorregir_LasClavesSonLasDelContrato(t *testing.T) {
	tipo := reflect.TypeOf(intakes.CorrectionSignal{})
	casos := map[string]string{
		"AsCorrection":       intakes.ClaveAsCorrection,
		"CorrectsRevisionNo": intakes.ClaveCorrectsRevisionNo,
		"CorrectsKind":       intakes.ClaveCorrectsKind,
	}
	for campo, clave := range casos {
		f, ok := tipo.FieldByName(campo)
		if !ok {
			t.Fatalf("CorrectionSignal ya no tiene el campo %s", campo)
		}
		got, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if got != clave {
			t.Fatalf("CorrectionSignal.%s se serializa como %q y la constante dice %q", campo, got, clave)
		}
		if !strings.Contains(f.Tag.Get("json"), "omitempty") {
			t.Fatalf("CorrectionSignal.%s perdió el omitempty: sin él, el payload del 041 gana una "+
				"clave y la no-regresión se rompe en silencio", campo)
		}
	}
}

// --- (3) EL ESTADO: LA «VUELTA A PENDING_APPROVAL» --------------------------

// TestCorregir_LaVueltaAPendingApprovalYaEsCierta.
//
// El enunciado dice que `correct` «vuelve a pending_approval». No se transiciona a
// ninguna parte porque la solicitud YA está ahí: es el único estado desde el que este
// PUT escribe. Lo que este test fija es el HECHO —después de corregir, la solicitud
// está por aprobar— sin depender de cómo se consiga.
func TestCorregir_LaVueltaAPendingApprovalYaEsCierta(t *testing.T) {
	for nombre, modo := range map[string]intakes.EditMode{"sin el campo": intakes.EditPlain, "con el campo": intakes.EditAsCorrection} {
		t.Run(nombre, func(t *testing.T) {
			e := nuevaEscenaCorregir(t, intakes.StatusPendingApproval)

			detail, err := e.svc.ReplaceItems(context.Background(), tenantA, intakeDePrueba, líneaCorregida, modo)
			if err != nil {
				t.Fatalf("ReplaceItems: %v", err)
			}
			if detail.Status != intakes.StatusPendingApproval {
				t.Fatalf("status devuelto=%q, quiero pending_approval", detail.Status)
			}
			if got := estadoDe(t, e.store); got != intakes.StatusPendingApproval {
				t.Fatalf("status persistido=%q, quiero pending_approval", got)
			}
		})
	}
}

// TestCorregir_ElCampoNoAmplíaLosEstadosEditables es el borde que la fusión de
// `correct` con el PUT del 041 hace fácil de romper: `needs_info` es un estado desde
// el que el dueño querría corregir, y `as_correction` NO lo abre.
//
// Para corregir un pedido al que se le pidió información, el dueño lo devuelve a
// `pending_approval` por el selector de estado —esa transición SÍ es legal— y
// entonces corrige. Abrirlo aquí habría sido inventar una transición por la puerta de
// atrás y cambiarle las precondiciones a una ruta del 041 en producción.
func TestCorregir_ElCampoNoAmplíaLosEstadosEditables(t *testing.T) {
	for _, estado := range []string{intakes.StatusNeedsInfo, intakes.StatusConfirmed, intakes.StatusOpen} {
		t.Run(estado, func(t *testing.T) {
			e := nuevaEscenaCorregir(t, estado)

			_, err := e.svc.ReplaceItems(context.Background(), tenantA, intakeDePrueba, líneaCorregida, intakes.EditAsCorrection)

			var noEditable *intakes.NotEditableError
			if !errors.As(err, &noEditable) {
				t.Fatalf("err=%v, quiero *NotEditableError: `as_correction` no amplía los estados editables", err)
			}
			if noEditable.Status != estado {
				t.Fatalf("el error dice %q y la solicitud está en %q", noEditable.Status, estado)
			}
			if len(e.crm.all()) != 0 {
				t.Fatal("una corrección rechazada empujó al CRM: no se escribió ninguna revisión")
			}
		})
	}
}

// --- (4) EL EMPUJE AL CRM ---------------------------------------------------

// TestCorregir_ElEmpujeCuelgaDeLaRevisiónYNoDelCampo (T4.10 §2: «toda revisión
// posterior al cierre re-empuja»).
//
// Es la decisión de esta tarea que más fácil sería tomar al revés: hacer que el push
// dependa de `as_correction` habría dejado al CRM con un documento desactualizado
// cada vez que el dueño corrigiera por la puerta del 041 —mismas líneas cambiadas,
// mismo total nuevo, ningún aviso—, y habría hecho que el mismo efecto de dominio
// tuviera dos conductas de integración según un campo de la UI.
func TestCorregir_ElEmpujeCuelgaDeLaRevisiónYNoDelCampo(t *testing.T) {
	e := nuevaEscenaCorregir(t, intakes.StatusPendingApproval)

	if _, err := e.svc.ReplaceItems(context.Background(), tenantA, intakeDePrueba, líneaCorregida, intakes.EditPlain); err != nil {
		t.Fatalf("ReplaceItems sin el campo: %v", err)
	}
	if _, err := e.svc.ReplaceItems(context.Background(), tenantA, intakeDePrueba, líneaCorregida, intakes.EditAsCorrection); err != nil {
		t.Fatalf("ReplaceItems con el campo: %v", err)
	}

	empujes := e.crm.all()
	if len(empujes) != 2 {
		t.Fatalf("empujes=%d, quiero 2: cada revisión re-empuja, con campo y sin campo", len(empujes))
	}
	if empujes[0].revisionNo != 1 || empujes[1].revisionNo != 2 {
		t.Fatalf("revision_no empujados = %d y %d; quiero 1 y 2, estrictamente crecientes y REALES "+
			"(el literal `1` es justo el bug que T4.10 mitad 1 arregló)",
			empujes[0].revisionNo, empujes[1].revisionNo)
	}
	if empujes[1].status != intakes.StatusPendingApproval {
		t.Fatalf("el empuje dice que la solicitud está en %q; corregir no la mueve de pending_approval",
			empujes[1].status)
	}
	if empujes[1].total != 22000 {
		t.Fatalf("el empuje lleva total=%v y la corrección dejó 22000: el CRM tiene que recibir lo "+
			"que quedó, no lo que había", empujes[1].total)
	}
}

// TestCorregir_SinPuenteCableadoNoEmpujaNiFalla: sin CRMPusher el servicio funciona
// igual y no encola nada (criterio (e) de T4.10). Es la promesa de WithCRMPusher, y
// se comprueba aquí porque esta puerta es su segundo llamante.
func TestCorregir_SinPuenteCableadoNoEmpujaNiFalla(t *testing.T) {
	st := seedStore(t, intakes.StatusPendingApproval)
	svc := intakes.NewService(st) // sin WithCRMPusher

	if _, err := svc.ReplaceItems(context.Background(), tenantA, intakeDePrueba, líneaCorregida, intakes.EditAsCorrection); err != nil {
		t.Fatalf("ReplaceItems sin puente CRM: %v", err)
	}
}

// --- helpers ----------------------------------------------------------------

// objetoDelPayload deserializa el payload a claves crudas: es la única forma de
// preguntar si una clave ESTÁ, que es distinto de preguntar por su valor (un `int` de
// Go no distingue la clave ausente del cero).
func objetoDelPayload(t *testing.T, payload json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var raíz map[string]json.RawMessage
	if err := json.Unmarshal(payload, &raíz); err != nil {
		t.Fatalf("el payload de la revisión no es un objeto JSON (%v): %s", err, payload)
	}
	return raíz
}

// señalDe extrae la señal few-shot del payload de una revisión.
func señalDe(t *testing.T, payload json.RawMessage) intakes.CorrectionSignal {
	t.Helper()
	var señal intakes.CorrectionSignal
	if err := json.Unmarshal(payload, &señal); err != nil {
		t.Fatalf("no se pudo leer la señal del payload (%v): %s", err, payload)
	}
	return señal
}
