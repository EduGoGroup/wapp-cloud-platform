package intakes_test

// status_notice_test.go — QUIÉN le cuenta al cliente la transición (D-044.49,
// Plan 044 · Ola 4 · Tanda 2, cimiento de T4.3/T4.4).
//
// Las dos mitades del mismo hecho, y la segunda es la que importa más:
//
//   - con NoticeByCaller la plataforma NO manda el aviso genérico, porque el
//     llamante ya le escribió al cliente con el texto del dueño;
//   - con NoticeToClient sale exactamente lo de siempre, y el <select> de estado
//     de la consola (Plan 041 · T4.2) es esa puerta. Ese es el requisito duro:
//     esta tarea NO puede apagarle el aviso a nadie más.
//
// 🔴 POR QUÉ NO BASTABA CON BORRAR DE statusTemplates. La forma "rápida" de
// callar el aviso de `confirmed`/`needs_info` era quitar sus entradas del mapa —y
// eso lo apaga para TODO el que transicione, la consola incluida—. Ese atajo lo
// vigila TestStatusTemplates_SiguenTeniendoTextoParaLosDosEstados, que arma el
// Notifier REAL: los tests que cuentan avisos sobre el doble (avisoSpy) NO pueden
// verlo, porque el doble no consulta el mapa. Son dos redes distintas a propósito.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// transicionesQueElDueñoAnuncia son los dos destinos de D-044.49: al aprobar y al
// pedir información, el cliente recibe UN SOLO mensaje, el del dueño.
//
// Se prueban los DOS y no uno de muestra porque la decisión de producto nombra
// dos caminos distintos —aprobar y pedir un dato— y cada uno entra desde un
// origen distinto de la máquina de estados.
var transicionesQueElDueñoAnuncia = []struct {
	nombre string
	desde  string
	hasta  string
}{
	{"aprobar", intakes.StatusPendingApproval, intakes.StatusConfirmed},
	{"pedir información", intakes.StatusPendingApproval, intakes.StatusNeedsInfo},
}

// TestSetStatus_NoticeByCaller_NoAvisa: con el llamante anunciando, la plataforma
// se calla — y la transición se aplica igual. Las dos comprobaciones van juntas a
// propósito: callar no puede ser lo mismo que no registrar.
func TestSetStatus_NoticeByCaller_NoAvisa(t *testing.T) {
	for _, c := range transicionesQueElDueñoAnuncia {
		t.Run(c.nombre, func(t *testing.T) {
			spy := &avisoSpy{}
			svc := intakes.NewService(seedStore(t, c.desde), intakes.WithNotifier(spy))

			got, err := svc.SetStatus(context.Background(), tenantA, intakeDePrueba, c.hasta, intakes.NoticeByCaller)
			if err != nil {
				t.Fatalf("SetStatus(%s): %v", c.hasta, err)
			}
			if got.Status != c.hasta {
				t.Fatalf("status=%q, quiero %q: silenciar el aviso NO puede dejar de aplicar la transición",
					got.Status, c.hasta)
			}
			if spy.count() != 0 {
				t.Fatalf("avisos = %d, quiero 0: con NoticeByCaller el cliente ya recibió el mensaje "+
					"del dueño, y el genérico sería el segundo (D-044.49). Avisos: %v", spy.count(), spy.avisos)
			}
		})
	}
}

// TestSetStatus_NoticeToClient_SigueAvisando es la CONTRAPARTE, y es el test que
// caza la regresión del 041: las MISMAS dos transiciones, por la puerta genérica,
// tienen que seguir mandando su mensaje.
//
// Lo que este test ve es que el SERVICIO sigue llamando al notificador por esta
// puerta. Lo que NO ve —porque el avisoSpy es un doble y no mira statusTemplates—
// es que el notificador de verdad tenga texto que mandar: eso lo cubre el último
// test del fichero. Las dos mitades hacen falta.
func TestSetStatus_NoticeToClient_SigueAvisando(t *testing.T) {
	for _, c := range transicionesQueElDueñoAnuncia {
		t.Run(c.nombre, func(t *testing.T) {
			spy := &avisoSpy{}
			svc := intakes.NewService(seedStore(t, c.desde), intakes.WithNotifier(spy))

			if _, err := svc.SetStatus(context.Background(), tenantA, intakeDePrueba, c.hasta, intakes.NoticeToClient); err != nil {
				t.Fatalf("SetStatus(%s): %v", c.hasta, err)
			}
			if spy.count() != 1 {
				t.Fatalf("avisos = %d, quiero 1. El <select> de estado de la consola (Plan 041 · T4.2) "+
					"es esta puerta: apagarle el aviso deja al cliente sin enterarse de que su pedido "+
					"cambió. Avisos: %v", spy.count(), spy.avisos)
			}
			if got, want := spy.avisos[0], c.desde+"→"+c.hasta; got != want {
				t.Fatalf("aviso = %q, quiero %q", got, want)
			}
		})
	}
}

// TestStatusNotice_CeroEsElQueHabla: el valor CERO del tipo tiene que ser
// NoticeToClient. No es cosmética — es la regla de seguridad del mecanismo: un
// StatusNotice que llegue sin inicializar (un campo de struct, un mapa, un
// decode) manda el aviso en vez de tragárselo. Un silencio solo puede nacer de
// una decisión escrita.
func TestStatusNotice_CeroEsElQueHabla(t *testing.T) {
	var cero intakes.StatusNotice
	if cero != intakes.NoticeToClient {
		t.Fatalf("el cero de StatusNotice = %v, quiero NoticeToClient (%v)", cero, intakes.NoticeToClient)
	}

	spy := &avisoSpy{}
	svc := intakes.NewService(seedStore(t, intakes.StatusPendingApproval), intakes.WithNotifier(spy))
	if _, err := svc.SetStatus(context.Background(), tenantA, intakeDePrueba, intakes.StatusConfirmed, cero); err != nil {
		t.Fatalf("SetStatus con el cero del tipo: %v", err)
	}
	if spy.count() != 1 {
		t.Fatalf("avisos con el CERO del tipo = %d, quiero 1: el valor por descuido tiene que hablar", spy.count())
	}
}

// TestStatusTemplates_SiguenTeniendoTextoParaLosDosEstados es la red del atajo:
// arma el Notifier REAL y comprueba que `confirmed` y `needs_info` siguen
// produciendo un mensaje.
//
// 🔴 ES EL ÚNICO TEST DE ESTE FICHERO QUE VE statusTemplates. Los otros cuentan
// llamadas sobre un doble (avisoSpy): si alguien vaciara esas dos entradas del
// mapa, el doble las seguiría contando y todo saldría verde mientras el cliente
// se queda sin enterarse de que su pedido cambió. Aquí se mira lo que llega al
// Sender, que es lo que llega a un teléfono.
func TestStatusTemplates_SiguenTeniendoTextoParaLosDosEstados(t *testing.T) {
	for _, c := range transicionesQueElDueñoAnuncia {
		t.Run(c.nombre, func(t *testing.T) {
			sender := &stubSender{}
			n := newNotifier(sender, intakes.NewMemoryStore(), &logSpy{})

			n.NotifyStatus(context.Background(), tenantA, intakeEn(c.hasta), c.desde)

			enviados := sender.messages()
			if len(enviados) != 1 {
				t.Fatalf("mensajes enviados = %d, quiero 1. La supresión de D-044.49 se hace con "+
					"NoticeByCaller en el LLAMANTE; vaciar la entrada de %q en statusTemplates apaga "+
					"el aviso para todo el mundo, el <select> de la consola incluido (Plan 041 · T4.2)",
					len(enviados), c.hasta)
			}
			if strings.TrimSpace(enviados[0].text) == "" {
				t.Fatalf("el texto de %q quedó vacío: un WhatsApp en blanco es peor que ninguno", c.hasta)
			}
		})
	}
}

// --- el puerto del puente CRM (Plan 044 · Ola 4 · Tanda 2) --------------------

// empujeSpy graba cada empuje al puente. Satisface intakes.CRMPusher por forma
// estructural, igual que lo hace *crmpush.RevisionPusher en producción: este
// paquete NO puede importar crmpush (crmpush importa a intakes, sería un ciclo) y
// por eso el puerto se declara aquí y se satisface desde fuera.
type empujeSpy struct{ empujes []string } // "intakeID@revisionNo→estado"

func (e *empujeSpy) PushRevision(_ context.Context, _ string, d intakes.Detail, revisionNo int) {
	e.empujes = append(e.empujes, fmt.Sprintf("%s@%d→%s", d.ID, revisionNo, d.Status))
}

// TestPushRevisionToCRM_SinCablearNoHaceNada: sin WithCRMPusher el servicio es
// exactamente el de antes. Es la misma promesa que WithNotifier —un test de dominio
// no le hace sonar el teléfono a nadie— llevada al puente: un consumidor que solo
// quiera mover filas no encola entregas hacia el CRM de un cliente por accidente.
func TestPushRevisionToCRM_SinCablearNoHaceNada(t *testing.T) {
	svc := intakes.NewService(seedStore(t, intakes.StatusPendingApproval))
	// No hay nada que afirmar más allá de que no panica: el puerto nil es el estado
	// de arranque y tiene que ser inofensivo.
	svc.PushRevisionToCRM(context.Background(), tenantA, intakes.Detail{}, 1)
}

// TestPushRevisionToCRM_CableadoEmpujaElNúmeroQueLeDan: el número viaja EXPLÍCITO
// y no se deduce de d.Revisions.
//
// 🔴 POR QUÉ IMPORTA QUE SEA EXPLÍCITO: el puente hace UPSERT por
// (intake_id, revision_no) y descarta los pares repetidos. Solo el llamante sabe
// cuál de las revisiones acaba de escribir; deducirla aquí —«la última»— haría que
// dos escrituras concurrentes empujaran el mismo número y que una de las dos
// desapareciera en el puente sin un solo error.
func TestPushRevisionToCRM_CableadoEmpujaElNúmeroQueLeDan(t *testing.T) {
	spy := &empujeSpy{}
	svc := intakes.NewService(seedStore(t, intakes.StatusPendingApproval), intakes.WithCRMPusher(spy))

	d, err := svc.Get(context.Background(), tenantA, intakeDePrueba)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	svc.PushRevisionToCRM(context.Background(), tenantA, d, 5)

	if len(spy.empujes) != 1 {
		t.Fatalf("empujes = %d, quiero 1: %v", len(spy.empujes), spy.empujes)
	}
	want := intakeDePrueba + "@5→" + intakes.StatusPendingApproval
	if spy.empujes[0] != want {
		t.Fatalf("empuje = %q, quiero %q", spy.empujes[0], want)
	}
}
