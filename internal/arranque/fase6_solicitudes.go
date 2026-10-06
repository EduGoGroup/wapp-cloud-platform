// Copia de internal/bootstrap/arranque/fase6_solicitudes.go @ 80807ba (F0 · 05 §6): cablea paquetes VIEJOS,
// salvo acceso (F2, T2.31, conmutar(acceso)) y edge (F3, T3.28, conmutar(edge)), que son
// internal/modulos/{acceso,edge}: un solo gateway, el nuevo, que recibe acceso sin adaptador.
package arranque

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes/telemetria"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/integrations"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/integrations/crmpush"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
)

// faseSolicitudes arma la bandeja del dueño: la salida hacia WhatsApp, los dos
// recordatorios perezosos y el Service que gobierna la máquina de estados.
//
// 🔴 VA ANTES QUE EL MOTOR DE FLUJOS, y esa es toda la razón de que sea una fase
// aparte: desde el Plan 043 el Service tiene DOS consumidores —la API del dueño y el
// motor, que abandona la solicitud del evento que el cliente cierra al empezar otro
// del mismo tipo (E-11)—. Es un solo objeto a propósito: dos serían dos máquinas de
// estados opinando sobre las mismas filas.
//
// Y va DESPUÉS del gateway porque todo lo de aquí que habla con un cliente lo hace por
// él.
type faseSolicitudes struct{}

func (faseSolicitudes) nombre() string { return "solicitudes" }

func (faseSolicitudes) requiere() []string {
	return []string{"gateway", "almacenes"}
}

func (faseSolicitudes) ejecutar(_ context.Context, c *contenedor) error {
	c.webhookGate = integrations.NewEntitlementsGate(c.entResolver, c.integrationsStore, entitlements.FeatureCRMBridge)

	// El aviso al cliente y el recordatorio de la seña son la MISMA salida hacia
	// WhatsApp con dos motivos (D-041.14 y D-041.12): el notificador se construye una
	// vez y el recordatorio lo reusa entero —texto de la seña, vía custodiada de PII y
	// SendText del Gateway—. Se arma AQUÍ, antes del runtime, porque el recordatorio
	// tiene tres disparadores y dos de ellos están en sitios distintos: el motor
	// (cuando el cliente escribe) y las lecturas del dueño (en el Service, más abajo).
	// Es un solo objeto: dos serían dos criterios de "ya recordé".
	c.intakeNotifier = intakes.NewNotifier(c.gw, c.flowDeps.contacts, c.intakeStore, c.log)
	c.depositReminder = intakes.NewDepositReminder(c.intakeNotifier, c.intakeStore)

	// El recordatorio del PLAZO del presupuesto (Plan 044 · T4.5, D-044.50 §2). Es el
	// HERMANO del de arriba y no una variante suya: aquel le habla al CLIENTE por
	// WhatsApp para recordarle una seña, éste le habla al DUEÑO para recordarle una
	// decisión que no ha tomado. Por eso NO reusa intakeNotifier —no hay ningún
	// teléfono de cliente en este camino— y por eso son dos objetos.
	//
	// 🔴 SU EMISOR ES UN SUMIDERO DE TRAZA, Y ESO ESTÁ DECIDIDO. El canal real es el
	// push del Plan 045, que todavía no existe: hoy esto escribe una línea en el log y
	// gasta la marca expiry_reminded_at de la solicitud. NADIE PUEDE AFIRMAR QUE EL
	// DUEÑO RECIBE EL RECORDATORIO. El día que exista el push, esta línea cambia el
	// argumento y nada más — que es exactamente para lo que se declaró el puerto.
	c.expiryReminder = intakes.NewExpiryReminder(intakes.NewLogOwnerNotice(c.log), c.intakeStore, c.log)

	// 🔴 ESTA ASIGNACIÓN CIERRA EL CICLO QUE ABRIÓ LA FASE DE CAPTACIÓN. La etapa
	// `draft` recibió una clausura que lee `c.intakeService`; hasta esta línea ese
	// campo es nil. Ver el comentario del campo en contenedor.go.
	c.intakeService = intakes.NewService(c.intakeStore,
		intakes.WithNotifier(c.intakeNotifier),
		intakes.WithDepositReminder(c.depositReminder),
		// Los DOS recordatorios perezosos van cableados por separado a propósito: son
		// dos colaboradores independientes de Service.touch y ninguno depende del
		// otro (ver la guarda de touch, que pregunta por cada uno).
		intakes.WithExpiryReminder(c.expiryReminder),
		// La cotización que el DUEÑO le manda al cliente al aprobar (Plan 044 · T4.3).
		// Es el MISMO notificador de dos líneas más arriba a propósito: una sola
		// salida hacia WhatsApp y un solo criterio sobre la plantilla de seña del
		// tenant. Sin esta línea, Approve corta con ErrNoQuoteSender en vez de
		// confirmar el pedido dejando al cliente sin enterarse.
		intakes.WithQuoteSender(c.intakeNotifier),
		// El puente CRM del DUEÑO (Plan 044 · Ola 4 · Tanda 2). Reusa las DOS piezas
		// que ya alimentan al sink del cierre de carrito —el MISMO integrationsStore
		// y el MISMO webhookGate de la línea de arriba—, así que un tenant no puede
		// tener el puente abierto para una puerta y cerrado para la otra: es una sola
		// evaluación de D-042.8, no dos criterios que puedan divergir.
		//
		// ⚠️ HOY ESTO NO EMPUJA NADA POR SÍ SOLO: el Service solo encola cuando alguien
		// llama a PushRevisionToCRM, y ninguna de sus escrituras lo hace todavía. El
		// cable está puesto para que las puertas del dueño (T4.3/T4.4) no tengan que
		// tocar el arranque; hasta que exista una, el único productor de intake.push
		// sigue siendo el WebhookSink.
		intakes.WithCRMPusher(crmpush.NewRevisionPusher(
			crmpush.NewPusher(c.log, c.integrationsStore, c.webhookGate), c.log)),
		// LA TELEMETRÍA DE LA BANDEJA (Plan 044 · T5.2, design §10). El adaptador va
		// sobre el MISMO `flowStore` que ya recibe la etapa `draft` del pipeline como
		// escritor de eventos, y tiene que serlo: `flow_events` es UNA tabla y los cinco
		// eventos del plan se leen juntos desde las consultas del runbook.
		//
		// El envoltorio `telemetria.New` NO es ceremonia: `intakes` no puede importar
		// `flujos/store` —ciclo con el test in-package de aquel paquete— así que su
		// puerto habla de tenant/contacto/nombre/payload y quien sabe firmar la fila es
		// este adaptador. Ver internal/intakes/telemetria.
		//
		// Sin esta línea el dominio funciona entero y NO publica nada — lo mismo que
		// promete WithNotifier para el teléfono del cliente—, así que su ausencia no
		// rompe ninguna acción del dueño: lo que deja vacíos son los tres KPIs que
		// dependen de la bandeja (`intake_line_corrected`, `intake_approved`,
		// `intake_info_requested`).
		intakes.WithMetrics(telemetria.New(c.flowStore), c.log))

	c.marca("solicitudes")
	return nil
}
