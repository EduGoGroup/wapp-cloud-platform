// Porta internal/intakes/postgres.go @ 64c181a

// postgres.go es la cabeza del adaptador Postgres del puerto Store: el tipo, sus
// opciones, el constructor y las aserciones de los puertos que satisface. El fichero
// viejo medía 1.734 líneas y aquí NACE PARTIDO por tema (E-13): cada grupo de métodos
// vive en su postgres_<tema>.go, y los cuatro métodos de *Postgres que el paquete
// viejo tenía repartidos por aprobadas.go, crm.go, customernote.go y reanalisis.go
// nacen también con el adaptador (D-F6-6 ampliada):
//
//   - postgres_read.go          List · ListDetails · Get
//   - postgres_revisions.go     InsertRevision (y la lectura, la poda y el sobre del literal)
//   - postgres_status.go        UpdateStatus
//   - postgres_reminders.go     MarkDepositReminded · MarkExpiryReminded · PendingDepositReminders · NotifySettings
//   - postgres_shipping.go      EnsureShippingLine · ShippingZones
//   - postgres_items.go         ReplaceItems · ApplyRevalidation
//   - postgres_discard.go       Discard · AbandonByEvent
//   - postgres_approved.go      ApprovedRenderedTexts   (era de aprobadas.go)
//   - postgres_crm.go           ReflectCRMStatus        (era de crm.go)
//   - postgres_customernote.go  GetCustomerNote         (era de customernote.go)
//   - postgres_reanalysis.go    ReanalysisTargetOf      (era de reanalisis.go)
//
// Reglas que valen para TODOS los ficheros del adaptador:
//
//   - el SQL se porta BYTE A BYTE del paquete viejo y no se «mejora» al portar. Que
//     ese SQL haga en un Postgres de verdad lo que el puerto promete lo prueba
//     intakeshelpertest.Contrato en los procesos de F9
//     (test/procesos/intakes_contrato_test.go); el test de cada fichero afirma, con un
//     driver de mentira, la forma: qué sentencias salen, en qué orden, dentro o fuera
//     de una transacción, y el mapeo de filas y errores;
//   - INV-8: toda sentencia sobre public.intakes va acotada por tenant_id, y «no
//     existe» y «es de otro tenant» son la MISMA respuesta (ErrNotFound o found=false);
//   - un id que no es un UUID no puede existir: se responde lo que el método responda
//     a «no existe» SIN ir a la base (Postgres devolvería un error de sintaxis que el
//     transporte traduciría a 500). La única excepción es ReflectCRMStatus, que no lo
//     mira (ver su contrato);
//   - los estados salen SIEMPRE normalizados (NormalizeStatus): la base puede guardar
//     todavía una clave legada;
//   - las fechas NULL (deposit_due_at, deposit_reminded_at, expiry_reminded_at,
//     literal_pruned_at) salen como time.Time cero;
//   - todo fallo de la base vuelve envuelto con %w y con el prefijo literal que dice
//     el contrato de cada método, y con los valores de retorno a cero: nunca hay
//     filas a medias;
//   - T-13: el cierre de las filas (rows.Close) NUNCA se calla con `_ =`. Si es lo
//     único que falla, el método falla con él y su prefijo propio; si ya había un
//     error, ese es el que cuenta y no se pisa.

package intakes

import (
	"database/sql"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// Postgres implementa Store, RevisionWriter, DepositStore, ExpiryStore y
// SettingsReader con SQL crudo sobre public.intakes, public.intake_items y
// public.intake_revisions (las dos primeras del Plan 016, renombradas por la migración
// 0041; la tercera nace en la 0045). Lee además public.tenant_settings (plazo de la
// seña, plantilla, zonas de envío, TTL del literal), public.intake_buyer_data (solo
// si existe la fila) y public.conversation_events (el evento padre del descarte).
//
// Sobre solicitudes y líneas NO crea solicitudes —eso es del módulo cart—: transiciona
// el estado, reemplaza o re-precia líneas, materializa la línea de envío y escribe
// revisiones.
//
// 🔴 HOMÓNIMO (T-1): la `literal_dek` de intake_revisions es la DEK por-fila del SOBRE
// de crypto.FieldCipher —la que cifra el literal de nivel 2 y viaja envuelta por la
// KEK del keyring—. NO es la DEK del ADR-0007 que descifra el almacén de whatsmeow:
// esa la custodia el Edge del cliente, jamás llega a la nube y este paquete no sabe
// nada de ella.
//
// En el rojo no lleva campos. El verde le pone tres: el *sql.DB, el cifrador del
// literal (puede ser nil, ver WithLiteralCipher) y el logger del evento de poda
// (nunca nil tras NewPostgres).
type Postgres struct{}

// Los puertos que *Postgres satisface. Si un método cambia de firma, el paquete deja
// de compilar aquí y no en el cableado.
var (
	_ Store          = (*Postgres)(nil)
	_ RevisionWriter = (*Postgres)(nil)
	_ DepositStore   = (*Postgres)(nil)
	_ ExpiryStore    = (*Postgres)(nil)
	_ SettingsReader = (*Postgres)(nil)
)

// PostgresOption configura el store. Era `OpciónPostgres` en el paquete viejo.
//
// Se añade así, y no como parámetros de NewPostgres, porque el store lo construye
// mucho código que no tiene —ni necesita— un FieldCipher: cambiarle la firma
// convertiría una tarea de cifrado en una migración de llamantes, con el riesgo de
// que alguno pasara nil «para compilar» y se llevara por delante la garantía.
type PostgresOption func(*Postgres)

// WithLiteralCipher le da al store la llave con la que sella y abre el LITERAL de
// nivel 2 de las revisiones (Plan 044 · T3.5, migración 0079). Era `ConCifraDeLiteral`
// en el paquete viejo.
//
// Sin ella (o con nil) el store funciona entero para todo lo que no lleva literal —la
// mayoría de las revisiones, todas las del carrito numérico— y FALLA, explícitamente,
// en lo que sí: una revisión CON literal no se escribe en claro (InsertRevision) ni se
// lee (Get). Lo que nunca pasa es degradar a texto en claro.
func WithLiteralCipher(c *crypto.FieldCipher) PostgresOption {
	panic(pendiente.Implementar("intakes.WithLiteralCipher"))
}

// WithRetentionLog sustituye el logger por el que sale el EVENTO DE PODA del literal.
// Era `ConLogDeRetencion` en el paquete viejo.
//
// Existe para dos cosas: cablear el logger de la aplicación en producción, y capturar
// el evento en los tests (la poda tiene que quedar registrada, y un criterio que no se
// puede observar no se puede verificar). Con nil NO hace nada: el store conserva el
// logger que tenía, porque sin logger la poda destruiría el literal en silencio.
func WithRetentionLog(l logger.Logger) PostgresOption {
	panic(pendiente.Implementar("intakes.WithRetentionLog"))
}

// NewPostgres construye el store sobre el pool dado y le aplica las opciones en orden
// (la última gana). Sin opciones: sin cifrador de literal y con logger.Default() como
// logger de retención.
//
// No abre ni comprueba la conexión, ni cifra nada: construir no emite ninguna
// sentencia.
func NewPostgres(db *sql.DB, opts ...PostgresOption) *Postgres {
	panic(pendiente.Implementar("intakes.NewPostgres"))
}
