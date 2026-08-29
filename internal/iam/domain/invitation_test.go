package domain_test

// invitation_test.go — EL TOKEN Y EL ESTADO DERIVADO (Plan 047 · Ola A).
//
// Lo que se vigila aquí no es "que la función haga algo", sino las DOS
// propiedades de las que depende que el canje (T-A3) encuentre la fila meses
// después de que alguien emitiera el código:
//
//  1. el digest es DETERMINISTA (sin sal): el mismo texto da siempre los mismos
//     32 bytes, o el canje no puede buscar por él;
//  2. la normalización vive DENTRO de la función, así que el texto tal y como
//     vuelve de WhatsApp —con espacios, con la caja cambiada por el teclado del
//     móvil— produce el MISMO digest que el que se guardó al emitir.

import (
	"crypto/sha256"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/iam/domain"
)

// TestNewInvitationToken_TieneElPrefijoYLaEntropiaAcordados.
//
// El largo se comprueba EXACTO y no ">= algo": 16 bytes en hex son 32 runas más
// el prefijo, y un generador que devolviera 10 bytes (los del código de
// enrolamiento, que es de donde se copió el patrón) pasaría un ">=" sin que
// nadie se entere de que el token que viaja por WhatsApp perdió 48 bits.
func TestNewInvitationToken_TieneElPrefijoYLaEntropiaAcordados(t *testing.T) {
	t.Parallel()
	tok, err := domain.NewInvitationToken()
	if err != nil {
		t.Fatalf("NewInvitationToken: %v", err)
	}
	if !strings.HasPrefix(tok, domain.InvitationTokenPrefix) {
		t.Fatalf("token %q no empieza por %q", tok, domain.InvitationTokenPrefix)
	}
	cuerpo := strings.TrimPrefix(tok, domain.InvitationTokenPrefix)
	if len(cuerpo) != 32 {
		t.Fatalf("cuerpo del token = %d runas, quiero 32 (16 bytes en hex): %q", len(cuerpo), cuerpo)
	}
}

// TestNewInvitationToken_NoSeRepite: dos emisiones seguidas dan tokens
// distintos. Es la comprobación mínima de que hay aleatoriedad de por medio y no
// una constante.
func TestNewInvitationToken_NoSeRepite(t *testing.T) {
	t.Parallel()
	vistos := make(map[string]bool, 64)
	for range 64 {
		tok, err := domain.NewInvitationToken()
		if err != nil {
			t.Fatalf("NewInvitationToken: %v", err)
		}
		if vistos[tok] {
			t.Fatalf("token repetido en 64 emisiones: %q", tok)
		}
		vistos[tok] = true
	}
}

// TestHashInvitationToken_EsDeterministaYDe32Bytes.
//
// Los 32 bytes no son un detalle estético: es lo que exige el CHECK
// tenant_invitations_token_hash_len_check, así que un digest de otro tamaño no
// llega a guardarse — falla el INSERT.
func TestHashInvitationToken_EsDeterministaYDe32Bytes(t *testing.T) {
	t.Parallel()
	const tok = "WAPP-INV-0123456789abcdef0123456789abcdef"
	uno := domain.HashInvitationToken(tok)
	otro := domain.HashInvitationToken(tok)

	if len(uno) != sha256.Size {
		t.Fatalf("digest de %d bytes, quiero %d", len(uno), sha256.Size)
	}
	if string(uno) != string(otro) {
		t.Fatalf("el digest NO es determinista: dos llamadas con el mismo token dieron %x y %x", uno, otro)
	}
	// Sin sal: el digest es EXACTAMENTE el SHA-256 del texto normalizado. Se
	// comprueba contra el cálculo directo para que "reforzarlo" con una sal
	// —que rompería el canje, porque ya no se podría buscar por el hash— se
	// tope con un test rojo y no con una revisión de código que quizá no llegue.
	esperado := sha256.Sum256([]byte(strings.ToUpper(tok)))
	if string(uno) != string(esperado[:]) {
		t.Fatalf("el digest no es SHA-256 a secas del token normalizado: %x vs %x", uno, esperado[:])
	}
}

// TestHashInvitationToken_NormalizaLoQueVuelveDeWhatsApp.
//
// 🔴 ES EL TEST DE SIMETRÍA ESCRITOR/LECTOR, y por eso vive aquí y no en el
// paquete del canje: la emisión hashea el token recién generado y el canje
// hashea lo que una persona pegó. Si la normalización estuviera fuera de la
// función, cada lado podría normalizar distinto y los dos seguirían siendo
// correctos consigo mismos mientras el sistema deja de canjear.
func TestHashInvitationToken_NormalizaLoQueVuelveDeWhatsApp(t *testing.T) {
	t.Parallel()
	tok, err := domain.NewInvitationToken()
	if err != nil {
		t.Fatalf("NewInvitationToken: %v", err)
	}
	patron := domain.HashInvitationToken(tok)

	casos := map[string]string{
		"con espacios alrededor":   "  " + tok + "  ",
		"con salto de línea":       tok + "\n",
		"en mayúsculas":            strings.ToUpper(tok),
		"en minúsculas":            strings.ToLower(tok),
		"pegado con espacio y \\n": " " + strings.ToLower(tok) + "\n",
	}
	for nombre, variante := range casos {
		if string(domain.HashInvitationToken(variante)) != string(patron) {
			t.Errorf("%s: el digest cambia (%x) respecto al del token emitido (%x): el canje no encontraría la fila",
				nombre, domain.HashInvitationToken(variante), patron)
		}
	}

	// La otra mitad, sin la cual lo de arriba sería vacuo: un token DISTINTO da un
	// digest distinto. Sin esto, una función que devolviera siempre lo mismo
	// pasaría todos los casos anteriores.
	otro, err := domain.NewInvitationToken()
	if err != nil {
		t.Fatalf("NewInvitationToken: %v", err)
	}
	if string(domain.HashInvitationToken(otro)) == string(patron) {
		t.Fatal("dos tokens distintos dan el mismo digest: la función no depende de su entrada")
	}
}

// TestInvitationStatus_LasCuatroSalidasYSuPRECEDENCIA.
//
// La precedencia es la parte que un test de "cada estado por separado" no
// vigila: una fila puede cumplir varias condiciones a la vez —una canjeada hace
// un mes también está vencida— y lo que hay que contar es lo que PASÓ, no lo que
// el reloj dice después.
func TestInvitationStatus_LasCuatroSalidasYSuPrecedencia(t *testing.T) {
	t.Parallel()
	ahora := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	antes := ahora.Add(-time.Hour)
	despues := ahora.Add(time.Hour)

	casos := []struct {
		nombre string
		inv    domain.Invitation
		quiero domain.InvitationStatus
	}{
		{"viva", domain.Invitation{ExpiresAt: despues}, domain.InvitationPending},
		{"vencida", domain.Invitation{ExpiresAt: antes}, domain.InvitationExpired},
		{"revocada", domain.Invitation{ExpiresAt: despues, RevokedAt: &antes}, domain.InvitationRevoked},
		{"canjeada", domain.Invitation{ExpiresAt: despues, RedeemedAt: &antes}, domain.InvitationRedeemed},
		{
			"revocada Y vencida gana la revocación: la escritura explícita cuenta más que el paso del tiempo",
			domain.Invitation{ExpiresAt: antes, RevokedAt: &antes},
			domain.InvitationRevoked,
		},
		{
			"canjeada Y vencida gana el canje: dejó una membresía detrás",
			domain.Invitation{ExpiresAt: antes, RedeemedAt: &antes},
			domain.InvitationRedeemed,
		},
		{
			"canjeada Y revocada gana el canje (estado que la base no impide: la 0085 no pone CHECK)",
			domain.Invitation{ExpiresAt: despues, RedeemedAt: &antes, RevokedAt: &antes},
			domain.InvitationRedeemed,
		},
		{
			"vencida JUSTO en el instante del reloj: expires_at <= now, como el WHERE del canje",
			domain.Invitation{ExpiresAt: ahora},
			domain.InvitationExpired,
		},
	}
	for _, c := range casos {
		if got := c.inv.Status(ahora); got != c.quiero {
			t.Errorf("%s: Status = %q, quiero %q", c.nombre, got, c.quiero)
		}
	}
}
