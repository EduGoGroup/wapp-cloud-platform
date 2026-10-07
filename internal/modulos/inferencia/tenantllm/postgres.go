// Porta internal/tenantllm/postgres.go @ ebf4eb7

package tenantllm

import (
	"context"
	"database/sql"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// Postgres es la implementación real de Store sobre database/sql (mismo estilo
// que internal/integrations/postgres.go: SQL raw con placeholders $1..$n, sin
// ORM), contra public.tenant_llm (migraciones 0071 y 0073).
//
// Las cuatro sentencias se portan BYTE A BYTE del paquete viejo —su test afirma
// el texto exacto de cada una contra constantes escritas aparte— y no se
// «mejoran» al portar. Que ese SQL haga en un Postgres de verdad lo que el puerto
// promete lo prueba tenantllmhelpertest.Contrato en los procesos de F9.
//
// 🔴 HOMÓNIMO (T-14): la `api_key_dek` de esta tabla es la DEK por-fila del SOBRE
// de crypto.FieldCipher —la que cifra la API key del proveedor y viaja envuelta
// por la KEK del keyring—. NO es la llave del almacén de whatsmeow que custodia
// el Edge del cliente: esa jamás llega a la nube y este paquete no sabe nada de
// ella.
type Postgres struct{}

// NewPostgres construye el store con la conexión y el cifrador de campo que
// custodia la API key (el MISMO KeyProvider de los planes 011/012 que ya usan
// internal/intakes para buyer_data e internal/integrations para el secreto HMAC
// — patrón replicado por la migración 0071). Un único keyring, una única
// rotación que gestionar.
//
// No abre ni comprueba la conexión, ni cifra nada: construir no emite ninguna
// sentencia.
func NewPostgres(db *sql.DB, cipher *crypto.FieldCipher) *Postgres {
	panic(pendiente.Implementar("tenantllm.NewPostgres"))
}

// Get implementa Store.Get.
//
// NO selecciona api_key_enc para leerlo, solo para saber si existe
// (`api_key_enc IS NOT NULL` → HasAPIKey): la credencial no tiene por qué
// materializarse en memoria cuando lo que se pregunta es si está configurada.
//
// 🔧 EL DÍA QUE SE AFLOJÓ EL NOT NULL LLEGÓ (0073, T1.5-2): `api_key_enc IS NOT
// NULL` ya no es siempre cierto sobre una fila que existe —las filas de la vía
// local no tienen sobre— y por eso NO se sustituyó nunca por un `true` literal.
// Tres columnas pueden venir NULL (provider, model, consented_at) y llegan como
// su valor cero: son las que la vía local no tiene, y quien las lea ya sabe por
// `Via` si tienen sentido. `via` NO admite NULL a propósito —es NOT NULL en la
// 0073—: si algún día llegara NULL, la lectura tiene que fallar ruidosamente en
// vez de devolver una vía vacía que nadie sabría interpretar.
//
// Sin fila (sql.ErrNoRows) no es un error: (Config cero, false, nil). Cualquier
// otro fallo —del driver o de una fila ilegible— vuelve envuelto como
// "tenantllm: leer configuración de <tenant>: …", con Config cero y found=false.
func (p *Postgres) Get(ctx context.Context, tenantID string) (Config, bool, error) {
	panic(pendiente.Implementar("tenantllm.Postgres.Get"))
}

// Upsert implementa Store.Upsert.
//
// El upsert reemplaza las SIETE columnas de negocio —la vía y las seis del eje
// `api`, sobre incluido—: cada PUT es la foto entera. No existe el camino
// «actualiza el modelo sin tocar la clave» que sí tiene
// integrations.UpsertTenantIntegration, y es deliberado (ver el comentario del
// puerto y el de la migración 0071).
//
// 🔴 LA VALIDACIÓN VA ANTES DEL SQL, en este orden, y con estos tres textos
// exactos. Ninguno de los tres casos emite una sentencia ni llama al cifrador:
// dejar que lo rechazara el CHECK de la tabla convertiría un error del cliente
// en un 500.
//
//  1. vía fuera del vocabulario (ValidVia) ⇒
//     `tenantllm: upsert de <tenant> con vía "<vía>": fuera del vocabulario (local|api)`
//     (la vía va con %q). Gana a los otros dos: una vía inventada no llega a
//     mirarse la clave.
//  2. ViaAPI con apiKey == "" ⇒
//     `tenantllm: upsert de <tenant> en vía api sin API key: esa vía no existe sin credencial`
//     (el envelope cifraría la cadena vacía sin quejarse y dejaría una fila con
//     sobre de no-valor: mejor un error nombrado que una fila que miente).
//  3. ViaAPI con consentedAt cero ⇒
//     `tenantllm: upsert de <tenant> en vía api sin consentimiento: la fila no puede existir sin él`
//     (un cero escribiría el año 1 en la columna: una mentira con fecha).
//
// 🔴 LA VÍA DECIDE QUÉ SE ESCRIBE (T1.5-2):
//
//   - ViaAPI   ⇒ las seis columnas del eje `api` se rellenan: provider y model de
//     cfg, el sobre que devuelve cipher.Encrypt(apiKey) —api_key_enc, api_key_dek
//     y api_key_kek_id, la KEK current— y consentedAt EN UTC.
//   - ViaLocal ⇒ las seis viajan como NULL, vengan como vengan en cfg, y apiKey y
//     consentedAt se ignoran. La fila queda diciendo «este tenant usa su propio
//     fierro», sin proveedor al que llamar, sin sobre que descifrar y sin
//     consentimiento que fingir. Los NULL van como `nil` sin tipo: una cadena
//     vacía en `provider` reventaría contra el CHECK del vocabulario.
//
// `created_at` NO se pisa en el DO UPDATE: el alta es el alta aunque la
// configuración cambie después. `consented_at` SÍ se pisa: el cuerpo re-afirma
// el consentimiento en cada PUT de la vía API, y se BORRA al pasar a la local
// (el permiso muere con la vía que lo usaba). `updated_at` lo pone now().
//
// Un fallo del cifrador vuelve envuelto como "tenantllm: cifrar la API key de
// <tenant>: …" y no emite la sentencia; uno del driver, como "tenantllm: upsert
// de <tenant>: …". Ninguno de los dos lleva la clave en el texto.
func (p *Postgres) Upsert(ctx context.Context, cfg Config, apiKey string, consentedAt time.Time) error {
	panic(pendiente.Implementar("tenantllm.Postgres.Upsert"))
}

// Delete implementa Store.Delete: borra la fila entera y con ella la credencial
// cifrada y el consentimiento. Es la ÚNICA forma de retirar la clave sin cambiar
// de vía (el PUT de la vía API nunca la borra: siempre la reemplaza por otra).
// Idempotente: borrar lo que no hay no es un error. Un fallo del driver vuelve
// envuelto como "tenantllm: borrar configuración de <tenant>: …".
func (p *Postgres) Delete(ctx context.Context, tenantID string) error {
	panic(pendiente.Implementar("tenantllm.Postgres.Delete"))
}

// APIKey implementa Store.APIKey: descifra con la KEK QUE ENVOLVIÓ ESTA FILA
// (api_key_kek_id), no la current (T-15) — tras una rotación parcial del Plan
// 012 coexisten filas envueltas por distintas KEK, igual que intake_buyer_data
// (buyerdata.go) y tenant_integrations (integrations/postgres.go:241-243). Esto
// no lo ve la suite del puerto (el doble no cifra): lo fija el test de este
// fichero y lo cubre F9.
//
// Devuelve ErrNotConfigured, sin envolver y sin llamar al cifrador, en tres casos
// que para el llamante son el mismo («este tenant no tiene credencial que pedir»):
//
//   - no hay fila (sql.ErrNoRows);
//   - la fila no tiene sobre: `api_key_kek_id` es NULL. Se mira esa columna y no
//     `api_key_enc` porque el CHECK del sobre garantiza que las tres van juntas, y
//     es la que el descifrado necesita para elegir la KEK;
//   - la fila tiene sobre pero `via` no es ViaAPI. 🔴 Cinturón y tirantes con
//     `tenant_llm_local_sin_credencial_check` (0073 · f.4): el CHECK impide que la
//     fila EXISTA, y esto impide que el código la USE si alguna vez existiera —un
//     restore parcial, una edición a mano, una base que se quedó sin la
//     constraint—. Devolver la credencial de una fila `via='local'` sería servir
//     una clave de un tercero bajo la vía que declara no llamar a nadie (REQ-33).
//     La guarda va en Go y NO en el WHERE: un `AND via = 'api'` no se distinguiría
//     de «no hay fila» al depurar.
//
// Un fallo de lectura vuelve envuelto como "tenantllm: leer la API key de
// <tenant>: …"; uno del descifrado, como "tenantllm: descifrar la API key de
// <tenant>: …", SIN el valor y sin el blob: un fallo de KEK no es motivo para
// volcar material cifrado a un log. En los dos la clave devuelta es "".
func (p *Postgres) APIKey(ctx context.Context, tenantID string) (string, error) {
	panic(pendiente.Implementar("tenantllm.Postgres.APIKey"))
}
