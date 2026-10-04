// Porta internal/iam/domain/canje.go @ 9a77307

package domain

// canje.go — EL VEREDICTO SOBRE UNA INVITACIÓN QUE ALGUIEN INTENTA CANJEAR (Plan 047 · Ola A ·
// T-A3). El token, su hash y la entidad viven en invitation.go; aquí vive solo la decisión de si
// ese canje procede.
//
// 🔴 POR QUÉ ESTA DECISIÓN ES UNA FUNCIÓN PURA Y VIVE EN EL DOMINIO, Y NO UN `switch` DENTRO DEL
// ADAPTADOR POSTGRES. Es la mitad estructural del requisito ANTI-ORÁCULO de T-A3: «no existe» y
// «caducada» tienen que costar lo mismo, o alguien puede sondear con un cronómetro qué tokens
// existieron alguna vez. Un comentario que diga «no metas aquí una consulta» envejece; una FIRMA
// que no recibe `context.Context`, ni un `*sql.Tx`, ni un `Executor`, no puede consultar nada
// aunque quien la edite lo olvide. Y este fichero no importa `database/sql` ni `net/http`: su
// bloque de imports es de dos, `errors` y `time`.
//
// El otro extremo del mismo requisito —que los dos desenlaces salgan por la MISMA sentencia de
// escritura HTTP, con el código como única diferencia— vive en transport/http/canje.go.
//
// Los nombres en español de este fichero (EvaluarCanje, ResultadoCanje, Canje*) son los del
// viejo y se conservan: ya están decididos en la spec de F2 y los consumirá el adaptador.

import (
	"errors"
	"time"
)

// ErrInvitationExpired es el centinela del canje de una invitación VENCIDA (410). Su texto,
// «iam: invitación caducada», es observable y no cambia.
//
// Nace aquí y no en errors.go porque no es un error genérico: es el único desenlace del canje
// que ningún centinela existente sabe expresar. ErrNotFound y ErrConflict sí valían tal cual
// para la ausencia y para el terminal-por-escritura, y por eso NO se han duplicado con primos de
// nombre más largo — un centinela nuevo por cada matiz es como se llega a un `errors.Is` de
// veinte ramas donde nadie sabe cuál gana.
//
// 🔴 Que este error tenga cuerpo HTTP PROPIO no significa que tenga cuerpo DISTINTO del 404:
// por requisito anti-oráculo comparten cuerpo exacto y solo difieren en el código. Quien venga
// a «mejorar» el mensaje para explicar que la invitación caducó estará construyendo justo el
// oráculo que esto evita.
var ErrInvitationExpired = errors.New("iam: invitación caducada")

// ResultadoCanje es el veredicto sobre una invitación en el instante en que alguien la
// presenta. Son CUATRO y no tres: la ausencia es un veredicto de pleno derecho, no la falta de
// uno.
//
// ⚠️ NO es InvitationStatus con otro nombre. Aquél describe la FILA («esta invitación está
// revocada»); éste describe el INTENTO («este canje no procede, y por eso»). `redeemed` y
// `revoked` son dos estados de la fila y UN SOLO veredicto aquí (CanjeConsumido), a propósito.
type ResultadoCanje int

const (
	// CanjeProcede es el ÚNICO veredicto que deja seguir: la invitación existe, nadie la usó,
	// nadie la anuló y no ha vencido.
	CanjeProcede ResultadoCanje = iota
	// CanjeAusente es el veredicto cuando no hay ninguna fila con ese digest: un token
	// inventado, uno de otro sistema, o uno bien tecleado de una empresa que se borró (la FK va
	// ON DELETE CASCADE). El canje no distingue entre esos tres casos y no debe: son todos «eso
	// no abre nada».
	CanjeAusente
	// CanjeCaducado es el veredicto cuando la fila existe y su `expires_at` ya pasó. Es el único
	// terminal SIN escritura: ocurre por el paso del tiempo, y nadie lo marca.
	CanjeCaducado
	// CanjeConsumido es el veredicto cuando la fila existe y ya es terminal por una ESCRITURA:
	// el canje de otra persona (`redeemed_at`) o la revocación de la dueña (`revoked_at`).
	//
	// 🔴 LOS DOS COMPARTEN VEREDICTO A PROPÓSITO. Quien presenta un token que no es suyo no
	// tiene por qué enterarse de si alguien se le adelantó o de si la dueña se arrepintió: son
	// dos hechos sobre TERCERAS personas, y separarlos convertiría el canje en un chivato de lo
	// que pasa dentro de una empresa a la que quien pregunta no pertenece.
	CanjeConsumido
)

// EvaluarCanje decide si un canje procede, y si no, por qué (R-D5): inv nil → CanjeAusente;
// estado InvitationPending en `ahora` → CanjeProcede; InvitationExpired → CanjeCaducado;
// cualquier otro estado (InvitationRedeemed, InvitationRevoked y todo estado futuro) →
// CanjeConsumido. La precedencia entre estados es la de Invitation.Status, sin repetirla.
//
// `inv` es un PUNTERO y nil es un valor esperado, no un descuido del llamante: es exactamente
// como se representa «no había fila». Que la ausencia entre por el mismo parámetro que la
// presencia es lo que permite que los dos caminos compartan de aquí en adelante todo el
// código, que es lo que iguala su coste.
//
// `ahora` es un parámetro y no `time.Now()` dentro: la caducidad es la única transición que
// ocurre sin que nadie escriba, así que probarla exige poder mover el reloj. Esta función es la
// traducción de Invitation.Status: el ORDEN de las ramas de allí es la regla, y aquí no se
// repite para no tener dos sitios donde equivocarse.
func EvaluarCanje(inv *Invitation, ahora time.Time) ResultadoCanje {
	if inv == nil {
		return CanjeAusente
	}
	switch inv.Status(ahora) {
	case InvitationPending:
		return CanjeProcede
	case InvitationExpired:
		return CanjeCaducado
	default:
		// InvitationRedeemed e InvitationRevoked. El `default` y no dos `case` explícitos: si
		// mañana nace un quinto estado terminal, el canje lo rechaza por omisión en vez de
		// dejarlo pasar como pendiente. Un estado nuevo que abriera la puerta por olvido es el
		// fallo caro; uno que la cierre de más se ve el primer día.
		return CanjeConsumido
	}
}
