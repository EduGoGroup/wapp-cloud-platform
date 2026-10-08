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
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/EduGoGroup/wapp-shared/logger"

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
// Lleva los tres campos del viejo, ni uno más: el TTL del literal NO es un campo (lo
// decide la base por tenant, con DefaultLiteralTTL como segundo argumento de la
// lectura de revisiones).
type Postgres struct {
	db *sql.DB
	// cipher cifra y descifra el LITERAL de nivel 2 de una revisión (Plan 044 ·
	// T3.5, migración 0079). Puede ser nil: la mayoría de las revisiones —todas las
	// del carrito numérico— no llevan literal y no lo necesitan. Lo que NO pasa con
	// nil es degradar a texto en claro: una revisión CON literal y sin cipher se
	// rechaza al escribir y se rechaza al leer (ver openLiteral, en
	// postgres_revisions_read.go, y la escritura, en postgres_revisions.go).
	cipher *crypto.FieldCipher
	// log es por dónde sale el EVENTO DE PODA (T3.5). Nunca es nil tras
	// NewPostgres: sin logger la poda destruiría el literal en silencio, y una
	// destrucción de datos sin rastro no es aceptable ni en alpha.
	log logger.Logger
}

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
	return func(p *Postgres) { p.cipher = c }
}

// WithRetentionLog sustituye el logger por el que sale el EVENTO DE PODA del literal.
// Era `ConLogDeRetencion` en el paquete viejo.
//
// Existe para dos cosas: cablear el logger de la aplicación en producción, y capturar
// el evento en los tests (la poda tiene que quedar registrada, y un criterio que no se
// puede observar no se puede verificar). Con nil NO hace nada: el store conserva el
// logger que tenía, porque sin logger la poda destruiría el literal en silencio.
func WithRetentionLog(l logger.Logger) PostgresOption {
	return func(p *Postgres) {
		if l != nil {
			p.log = l
		}
	}
}

// NewPostgres construye el store sobre el pool dado y le aplica las opciones en orden
// (la última gana). Sin opciones: sin cifrador de literal y con logger.Default() como
// logger de retención.
//
// No abre ni comprueba la conexión, ni cifra nada: construir no emite ninguna
// sentencia.
func NewPostgres(db *sql.DB, opts ...PostgresOption) *Postgres {
	p := &Postgres{db: db, log: logger.Default()}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Lo que sigue es lo que comparten TODOS los postgres_<tema>.go: la proyección de la
// cabecera, su escáner y las dos abstracciones de «quién ejecuta».

// intakeCols es la proyección de la cabecera. tenant_id NO se lee: quien consulta
// ya es el dueño del tenant (INV-8) y repetirlo en la respuesta no informa de nada.
//
// customer_note (D-041.19) va al final y no en medio: el orden de esta lista es el
// de los Scan de scanIntake y scanDetailRow, y meter una columna entre dos ya
// existentes obligaría a mover los destinos de los dos escaneos a la vez —un
// descuadre que compila y devuelve el estado en el total—. Las dos marcas de la
// SEÑA (T4.4) y la del PLAZO del presupuesto (T4.5) se añaden al final por la misma
// razón.
const intakeCols = `id::text, contact_id, session_id, status, total, created_at, updated_at, customer_note,
	deposit_due_at, deposit_reminded_at, expiry_reminded_at`

// rowScanner abstrae *sql.Row y *sql.Rows para compartir el escaneo de cabecera.
type rowScanner interface {
	Scan(dest ...any) error
}

// querier abstrae *sql.DB y *sql.Tx: las MISMAS lecturas (líneas, revisiones) se
// hacen sueltas desde Get y dentro de la transacción de una edición, y duplicarlas
// dejaría dos consultas que tendrían que envejecer juntas.
//
// ExecContext se añadió con T3.5 y hace que el nombre se quede a medias: la lectura
// de revisiones ESCRIBE cuando poda. Se deja el nombre en vez de renombrar a
// `ejecutor` en trece sitios por una tarea de retención — pero conviene saberlo:
// esto ya no es solo un lector.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// scanIntake lee una cabecera y NORMALIZA su estado: el `closed` que sigue
// escribiendo el módulo cart sale de aquí como `confirmed`, en un único punto.
//
// Las dos marcas de la seña son NULLables en la tabla (la inmensa mayoría de las
// solicitudes no llega a pedir seña) y se leen por sql.NullTime: un NULL sale como
// tiempo CERO, que es lo que el dominio entiende por "no se ha pedido" / "nunca se
// recordó". No hay un tercer significado que distinguir. La marca del PLAZO (T4.5)
// se lee igual y significa lo mismo: NULL ⇒ al dueño nunca se le recordó.
func scanIntake(sc rowScanner) (Intake, error) {
	var (
		in                Intake
		dueAt, remindedAt sql.NullTime
		expiryRemindedAt  sql.NullTime
	)
	if err := sc.Scan(&in.ID, &in.ContactID, &in.SessionID, &in.Status, &in.Total,
		&in.CreatedAt, &in.UpdatedAt, &in.CustomerNote, &dueAt, &remindedAt, &expiryRemindedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Intake{}, err // lo traduce el llamante (ErrNotFound)
		}
		return Intake{}, fmt.Errorf("intakes: leer solicitud: %w", err)
	}
	in.Status = NormalizeStatus(in.Status)
	in.DepositDueAt, in.DepositRemindedAt = dueAt.Time, remindedAt.Time
	in.ExpiryRemindedAt = expiryRemindedAt.Time
	return in, nil
}
