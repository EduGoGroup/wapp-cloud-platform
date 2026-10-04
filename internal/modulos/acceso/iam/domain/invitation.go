// Porta internal/iam/domain/invitation.go @ 9a77307

package domain

// invitation.go — LA INVITACIÓN DE UN SOLO USO Y SU TOKEN (Plan 047 · Ola A · T-A2/T-A8, tabla
// public.tenant_invitations de la migración 0085).
//
// 🔴 POR QUÉ EL TOKEN VIVE AQUÍ Y NO EN EL USECASE NI EN EL TRANSPORTE. Generar el token y
// hashearlo son las DOS MITADES DE UNA MISMA CONVENCIÓN, con dos dueños distintos en el tiempo:
// la emisión genera y guarda el digest, y el canje llega meses después con un texto pegado
// desde WhatsApp y tiene que producir EXACTAMENTE el mismo digest o no encuentra la fila. Si
// cada lado se escribe su hash, la primera divergencia —un TrimSpace de más en uno de los dos—
// no la caza ningún test unitario de ninguno de los dos paquetes: cada uno sigue siendo
// correcto consigo mismo y el sistema deja de canjear. Por eso HashInvitationToken es UNA
// función, con la normalización DENTRO, y es el único símbolo que ambos lados consumen.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// InvitationTokenPrefix encabeza todo token de invitación: el literal "WAPP-INV-".
//
// Es PROPIO y distinto del "WAPP-" de los códigos de enrolamiento
// (platformadmin.generateEnrollmentCode) a propósito: los dos códigos de un solo uso de la
// casa acaban pegados en pantallas distintas por personas distintas, y un prefijo compartido
// haría que un código de enrolamiento tecleado en el canje —o al revés— fallara con un "no
// existe" en vez de con "eso no es lo que te pedimos". El prefijo no aporta entropía: la aporta
// el cuerpo.
//
//nolint:gosec // G101: no es una credencial, es un prefijo fijo y público.
const InvitationTokenPrefix = "WAPP-INV-"

// invitationTokenBytes es la entropía del token: 16 bytes = 128 bits, que en hex son 32
// caracteres.
//
// Son 16 y no los 10 del código de enrolamiento porque los dos NO viajan por el mismo sitio: el
// de enrolamiento lo teclea un operador en una máquina que ya está detrás de mTLS, y este viaja
// por WhatsApp, que se reenvía, se archiva en el teléfono de dos personas y sobrevive a la
// conversación. No hay diccionario que atacar —es aleatorio puro— pero sí hay tiempo, y 128
// bits lo cierran sin discusión.
const invitationTokenBytes = 16

// NewInvitationToken genera el token EN CLARO de una invitación: InvitationTokenPrefix seguido
// de 16 bytes aleatorios (crypto/rand) en hex minúscula, es decir, EXACTAMENTE 9 + 32 = 41
// caracteres; dos emisiones no se repiten (R-D1). Solo falla si el generador aleatorio falla,
// y entonces devuelve "" y el error envuelto.
//
// Se devuelve a quien emite UNA sola vez (la respuesta de POST /api/v1/invitations) y jamás se
// persiste: en la tabla solo vive su digest (ver HashInvitationToken). Se genera EN EL CLOUD y
// no en la consola: la consola es un BFF sin base de datos, y habría dos generadores que
// mantener alineados. Mismo criterio que generateEnrollmentCode.
func NewInvitationToken() (string, error) {
	var b [invitationTokenBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("iam: generando token de invitación: %w", err)
	}
	return InvitationTokenPrefix + hex.EncodeToString(b[:]), nil
}

// HashInvitationToken devuelve el SHA-256 del token NORMALIZADO: los 32 bytes EXACTOS que exige
// el CHECK tenant_invitations_token_hash_len_check (R-D2). Es determinista: la misma entrada da
// siempre el mismo digest. La normalización es exactamente strings.TrimSpace y luego
// strings.ToUpper, y va DENTRO (R-D3).
//
// 🔴 SHA-256 A SECAS, SIN SAL Y SIN BCRYPT, y no es un descuido de seguridad: es el requisito.
// Esto NO es una contraseña —no lo elige una persona y no hay diccionario contra 128 bits de
// aleatoriedad—, y el canje NECESITA encontrar la fila por el digest, o sea que la función
// tiene que ser DETERMINISTA. Una sal por fila obligaría a leer la tabla entera y comparar una
// a una; un bcrypt lo haría además caro por intento. Quien venga a "reforzarlo" con sal habrá
// roto el canje, no mejorado nada.
//
// LA NORMALIZACIÓN VA DENTRO, Y ESA ES LA MITAD QUE IMPORTA. El token vuelve del canje pegado
// desde WhatsApp: con espacios alrededor y, si el teclado del móvil se puso creativo, con la
// caja cambiada. Se recorta y se pasa a mayúsculas ANTES de hashear, en el único sitio que
// hashea, de modo que emisor y canje no pueden discrepar aunque uno de los dos olvide
// normalizar —no tiene dónde olvidarlo—. Subir a mayúsculas no cuesta entropía: el alfabeto
// es hex más un prefijo fijo, ya insensible a la caja.
//
// Lo que esa normalización hace con lo raro (fijado por el corpus adversario del test):
//   - El recorte quita de los bordes todo lo que unicode.IsSpace llama espacio, no solo el
//     ASCII: U+00A0 (el espacio duro que meten los móviles al pegar), U+2003, U+202F, U+3000,
//     U+0085, tabuladores y saltos de línea. Por dentro no recorta nada.
//   - Recorta TAMBIÉN en los bordes U+200B (espacio de ancho cero) y U+FEFF (BOM), que no son
//     espacios para Go y TrimSpace dejaba: un token pegado con uno de ellos daba otro digest y
//     no canjeaba, sin que quien lo pegó pudiera verlo. Aquí el nuevo se APARTA del viejo
//     (D-F2-11, Jhoan, 2026-10-04). Solo en los bordes: por dentro siguen dando otro digest.
//   - ToUpper es Unicode, no ASCII: U+0131 («ı» sin punto) sube a «I» ASCII y colisiona con el
//     canónico; los dígitos no ASCII (arábigos, de ancho completo) no se pliegan a 0-9.
//   - Separadores repetidos o espacios interiores se quedan: dan otro digest.
func HashInvitationToken(token string) []byte {
	sum := sha256.Sum256([]byte(strings.ToUpper(strings.TrimFunc(token, isTokenPadding))))
	return sum[:]
}

// isTokenPadding dice si r es relleno que se recorta de los bordes de un token: todo lo que Go
// llama espacio, más los dos invisibles que no lo son para Go y que llegan al pegar (D-F2-11).
func isTokenPadding(r rune) bool {
	return unicode.IsSpace(r) || r == '\u200B' || r == '\uFEFF'
}

// InvitationStatus es el estado DERIVADO de una invitación: no hay columna que lo guarde, y no
// la hay a propósito. Un estado escrito sería una cuarta cosa que mantener en sincronía con las
// tres marcas de tiempo que ya lo dicen todo, y la caducidad ni siquiera tiene escritura que lo
// actualice —ocurre por el paso del tiempo, sin que nadie toque la fila—. Sus literales son los
// que salen por el cable en el listado de invitaciones.
type InvitationStatus string

const (
	// InvitationPending ("pending") es la invitación viva: ni canjeada, ni revocada, ni vencida.
	// Es la ÚNICA que el canje puede aceptar.
	InvitationPending InvitationStatus = "pending"
	// InvitationRedeemed ("redeemed") es terminal: alguien la usó y ya es miembro.
	InvitationRedeemed InvitationStatus = "redeemed"
	// InvitationRevoked ("revoked") es terminal: la dueña la anuló (T-A8).
	InvitationRevoked InvitationStatus = "revoked"
	// InvitationExpired ("expired") es terminal POR EL PASO DEL TIEMPO: nadie la escribió y
	// nadie tiene que barrerla.
	InvitationExpired InvitationStatus = "expired"
)

// Invitation es una fila de public.tenant_invitations.
//
// 🔴 CERO PII, y aquí es una afirmación fuerte: no hay correo, ni nombre, ni teléfono, porque
// la tabla no tiene esas columnas y no las va a tener (D-047.11, ADR-0034). Quien emite reparte
// el código por su canal; la nube nunca necesita saber a quién se lo mandó.
type Invitation struct {
	// ID identifica la invitación. NO es el token ni se deriva de él: es lo que cita el listado
	// y lo que revoca T-A8.
	ID string
	// TenantID es la empresa a la que invita. Sale SIEMPRE del token de quien emite (INV-04),
	// nunca del cuerpo de la petición.
	TenantID string
	// TokenHash es el SHA-256 del token, 32 bytes.
	//
	// 🔴 ESTÁ EN LA ENTIDAD Y NO EN EL DTO, y esa es toda la frontera: la entidad es la fila; lo
	// que decide si el digest sale por el cable es la PROYECCIÓN, y que no salga lo vigila el
	// test del listado que compara el conjunto EXACTO de claves del JSON.
	TokenHash []byte
	// RoleID es el rol que se concederá AL CANJEAR. nil = alta sin rol, que es legítimo: dar de
	// alta a alguien y darle un rol son dos decisiones.
	RoleID *string
	// ExpiresAt es el vencimiento. Siempre informado: una invitación sin caducidad no es una
	// invitación, es una puerta abierta.
	ExpiresAt time.Time
	// CreatedBy es quien emitió (UUID de identity, opaco, sin FK).
	CreatedBy string
	// RedeemedBy y RedeemedAt son las dos mitades de UN hecho y viajan siempre juntas (CHECK
	// tenant_invitations_redeemed_pair_check).
	RedeemedBy *string
	RedeemedAt *time.Time
	// RevokedAt es cuándo la anuló la dueña. Terminal, como el canje.
	RevokedAt *time.Time
	// CreatedAt es el instante de la emisión.
	CreatedAt time.Time
}

// Status resuelve el estado de la invitación en el instante `now` (R-D4), con esta precedencia:
// RedeemedAt informado → InvitationRedeemed; si no, RevokedAt informado → InvitationRevoked; si
// no, ExpiresAt informado y now >= ExpiresAt (el borde exacto ya está vencida, como el
// `expires_at <= now()` del canje) → InvitationExpired; en otro caso, InvitationPending. Un
// ExpiresAt cero no vence nunca.
//
// EL ORDEN DE LAS RAMAS ES LA REGLA, no una casualidad de escritura. Una fila puede cumplir
// varias condiciones a la vez —una invitación canjeada hace un mes también está vencida— y lo
// que hay que contar es lo que PASÓ, no lo que el reloj dice después: las dos escrituras
// terminales ganan a la caducidad, y entre ellas gana el canje, porque es la que dejó una
// membresía detrás.
//
// `now` es un parámetro y no time.Now() dentro: la caducidad es la única transición sin
// escritura, así que probarla exige poder mover el reloj.
func (i Invitation) Status(now time.Time) InvitationStatus {
	switch {
	case i.RedeemedAt != nil:
		return InvitationRedeemed
	case i.RevokedAt != nil:
		return InvitationRevoked
	case !i.ExpiresAt.IsZero() && !now.Before(i.ExpiresAt):
		return InvitationExpired
	default:
		return InvitationPending
	}
}
