package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

// fixedToken es un token con la forma exacta que emite NewInvitationToken (hex en minúscula).
//
//nolint:gosec // G101: token de prueba fijo, no una credencial.
const fixedToken = "WAPP-INV-0123456789abcdef0123456789abcdef"

// oldRuleDigest es la regla del viejo (internal/iam/domain/invitation.go @ 9a77307) escrita a
// mano: SHA-256 de strings.ToUpper(strings.TrimSpace(x)). Es el oráculo de equivalencia; no se
// importa el paquete viejo.
func oldRuleDigest(x string) []byte {
	sum := sha256.Sum256([]byte(strings.ToUpper(strings.TrimSpace(x))))
	return sum[:]
}

// r convierte un punto de código en cadena; así el corpus nombra cada carácter invisible por
// su número en vez de llevarlo crudo en el fuente.
func r(c rune) string { return string(c) }

func TestInvitationTokenPrefix_Literal(t *testing.T) {
	if InvitationTokenPrefix != "WAPP-INV-" { //nolint:gosec // G101: compara el prefijo público, no una credencial.
		t.Errorf("InvitationTokenPrefix = %q; quiere el literal %q", InvitationTokenPrefix, "WAPP-INV-")
	}
}

// R-D1: prefijo + 16 bytes aleatorios en hex, largo EXACTO. Un ">=" dejaría pasar un generador
// de 10 bytes (los del código de enrolamiento, de donde se copió el patrón) sin que nadie se
// entere de que el token que viaja por WhatsApp perdió 48 bits.
func TestNewInvitationToken_PrefixAndExactLength(t *testing.T) {
	tok, err := NewInvitationToken()
	if err != nil {
		t.Fatalf("NewInvitationToken devolvió error: %v", err)
	}
	if len(tok) != len("WAPP-INV-")+32 {
		t.Fatalf("token de %d caracteres; quiere exactamente %d: %q", len(tok), len("WAPP-INV-")+32, tok)
	}
	body, ok := strings.CutPrefix(tok, InvitationTokenPrefix)
	if !ok {
		t.Fatalf("el token %q no empieza por %q", tok, InvitationTokenPrefix)
	}
	raw, err := hex.DecodeString(body)
	if err != nil || len(raw) != 16 {
		t.Fatalf("el cuerpo %q no son 16 bytes en hex (bytes=%d, err=%v)", body, len(raw), err)
	}
	if body != strings.ToLower(body) {
		t.Errorf("el cuerpo %q no está en hex minúscula", body)
	}
}

// R-D1: dos emisiones no se repiten (hay aleatoriedad de por medio, no una constante).
func TestNewInvitationToken_NeverRepeats(t *testing.T) {
	seen := make(map[string]bool, 64)
	for range 64 {
		tok, err := NewInvitationToken()
		if err != nil {
			t.Fatalf("NewInvitationToken devolvió error: %v", err)
		}
		if seen[tok] {
			t.Fatalf("token repetido en 64 emisiones: %q", tok)
		}
		seen[tok] = true
	}
}

// R-D2: determinista, 32 bytes (el CHECK de la tabla) y SHA-256 a secas del token normalizado:
// una sal rompería el canje, que busca por el digest.
func TestHashInvitationToken_DeterministicSHA256Of32Bytes(t *testing.T) {
	first := HashInvitationToken(fixedToken)
	second := HashInvitationToken(fixedToken)
	if len(first) != 32 {
		t.Fatalf("digest de %d bytes; quiere 32", len(first))
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("el digest no es determinista: %x y %x", first, second)
	}
	want := sha256.Sum256([]byte(strings.ToUpper(fixedToken)))
	if !bytes.Equal(first, want[:]) {
		t.Fatalf("el digest no es el SHA-256 a secas del token normalizado: %x; quiere %x", first, want[:])
	}
}

// R-D3, simetría escritor/lector: el emisor hashea el token recién generado y el canje hashea
// lo que una persona pegó desde WhatsApp. Las variantes que produce un móvil dan el MISMO
// digest; y un token distinto da otro (sin esto, una función constante pasaría).
func TestHashInvitationToken_WriterReaderSymmetry(t *testing.T) {
	tok, err := NewInvitationToken()
	if err != nil {
		t.Fatalf("NewInvitationToken devolvió error: %v", err)
	}
	written := HashInvitationToken(tok)
	for _, c := range []struct{ name, pasted string }{
		{"surrounding_spaces", "  " + tok + "  "},
		{"trailing_newline", tok + "\n"},
		{"upper_case", strings.ToUpper(tok)},
		{"lower_case", strings.ToLower(tok)},
		{"space_lower_and_newline", " " + strings.ToLower(tok) + "\n"},
		{"nbsp_around", r(0x00A0) + tok + r(0x00A0)},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := HashInvitationToken(c.pasted); !bytes.Equal(got, written) {
				t.Errorf("el digest de lo pegado (%x) difiere del emitido (%x): el canje no encontraría la fila", got, written)
			}
		})
	}
	other, err := NewInvitationToken()
	if err != nil {
		t.Fatalf("NewInvitationToken devolvió error: %v", err)
	}
	if bytes.Equal(HashInvitationToken(other), written) {
		t.Fatal("dos tokens distintos dan el mismo digest: la función no depende de su entrada")
	}
}

// R-D3, corpus ADVERSARIO (reglas F2 §5): en TODA entrada el digest nuevo es el de la regla
// vieja (equivalencia), y además se fija si cae en el digest canónico o no, para que un cambio
// de normalización (NFKC, recorte de invisibles…) se vea aquí y se decida, no se cuele.
func TestHashInvitationToken_AdversarialCorpus(t *testing.T) {
	canonical := HashInvitationToken(fixedToken)
	const body = "0123456789abcdef0123456789abcdef"
	cases := []struct {
		name          string
		input         string
		sameCanonical bool
	}{
		// Espacios Unicode no ASCII en los bordes: Go los trata como espacio (unicode.IsSpace).
		{"nbsp_u00a0_edges", r(0x00A0) + fixedToken + r(0x00A0), true},
		{"em_space_u2003_edges", r(0x2003) + fixedToken + r(0x2003), true},
		{"narrow_nbsp_u202f_trailing", fixedToken + r(0x202F), true},
		{"ideographic_space_u3000_leading", r(0x3000) + fixedToken, true},
		{"next_line_u0085_trailing", fixedToken + r(0x0085), true},
		{"tabs_cr_lf_vt_ff_edges", "\t\r\n" + fixedToken + "\t\v\f", true},
		{"repeated_spaces_and_newlines", "   \n\n " + fixedToken + " \r\n\r\n", true},
		// Caja.
		{"all_lower", strings.ToLower(fixedToken), true},
		{"mixed_case", "wApP-iNv-" + strings.ToUpper(body[:16]) + body[16:], true},
		// ToUpper es Unicode: «ı» sin punto sube a «I» ASCII y colisiona con el canónico.
		{"dotless_i_u0131_in_prefix", "wapp-" + r(0x0131) + "nv-" + body, true},
		// Invisibles que NO son espacio para Go: no se recortan y cambian el digest.
		{"zero_width_space_u200b_leading", r(0x200B) + fixedToken, false},
		{"bom_ufeff_leading", r(0xFEFF) + fixedToken, false},
		{"nbsp_inside", "WAPP-INV-" + r(0x00A0) + body, false},
		// Separadores repetidos y espacios interiores: el recorte es solo de bordes.
		{"repeated_dash_separator", "WAPP--INV-" + body, false},
		{"double_prefix", "WAPP-INV-" + fixedToken, false},
		{"inner_space", "WAPP-INV- " + body, false},
		// Dígitos no ASCII: ToUpper no los pliega a 0-9.
		{"arabic_indic_zero_u0660", "WAPP-INV-" + r(0x0660) + body[1:], false},
		{"fullwidth_zero_uff10", "WAPP-INV-" + r(0xFF10) + body[1:], false},
		{"fullwidth_letter_a_uff41", "WAPP-INV-0123456789" + r(0xFF41) + body[11:], false},
		// Bordes del dominio.
		{"empty", "", false},
		{"only_spaces", " " + r(0x00A0) + "\t", false},
		{"prefix_only", "WAPP-INV-", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := HashInvitationToken(c.input)
			if want := oldRuleDigest(c.input); !bytes.Equal(got, want) {
				t.Fatalf("diverge de la regla vieja: %x; la vieja da %x (entrada %+q)", got, want, c.input)
			}
			if same := bytes.Equal(got, canonical); same != c.sameCanonical {
				t.Errorf("¿mismo digest que el canónico? = %v; quiere %v (entrada %+q)", same, c.sameCanonical, c.input)
			}
		})
	}
}

func TestInvitationStatus_LiteralValues(t *testing.T) {
	for _, c := range []struct {
		got  InvitationStatus
		want string
	}{
		{InvitationPending, "pending"},
		{InvitationRedeemed, "redeemed"},
		{InvitationRevoked, "revoked"},
		{InvitationExpired, "expired"},
	} {
		if string(c.got) != c.want {
			t.Errorf("estado %q; quiere el literal %q", c.got, c.want)
		}
	}
}

// R-D4: las cuatro salidas y su PRECEDENCIA (redeemed > revoked > expired > pending), con el
// reloj inyectado. Una fila puede cumplir varias condiciones a la vez y cuenta lo que PASÓ, no
// lo que el reloj dice después.
func TestInvitationStatus_OutcomesAndPrecedence(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	before := now.Add(-time.Hour)
	after := now.Add(time.Hour)
	cases := []struct {
		name string
		inv  Invitation
		want InvitationStatus
	}{
		{"alive", Invitation{ExpiresAt: after}, InvitationPending},
		{"expired", Invitation{ExpiresAt: before}, InvitationExpired},
		{"expires_exactly_now", Invitation{ExpiresAt: now}, InvitationExpired},
		{"expires_one_nanosecond_later", Invitation{ExpiresAt: now.Add(time.Nanosecond)}, InvitationPending},
		{"zero_expiry_never_expires", Invitation{}, InvitationPending},
		{"revoked", Invitation{ExpiresAt: after, RevokedAt: &before}, InvitationRevoked},
		{"redeemed", Invitation{ExpiresAt: after, RedeemedAt: &before}, InvitationRedeemed},
		{"revoked_beats_expired", Invitation{ExpiresAt: before, RevokedAt: &before}, InvitationRevoked},
		{"redeemed_beats_expired", Invitation{ExpiresAt: before, RedeemedAt: &before}, InvitationRedeemed},
		{"redeemed_beats_revoked", Invitation{ExpiresAt: after, RedeemedAt: &before, RevokedAt: &before}, InvitationRedeemed},
		{"redeemed_beats_revoked_and_expired", Invitation{ExpiresAt: before, RedeemedAt: &before, RevokedAt: &before}, InvitationRedeemed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.inv.Status(now); got != c.want {
				t.Errorf("Status = %q; quiere %q", got, c.want)
			}
		})
	}
}

// La fila entera, sin PII: identificadores opacos, marcas de tiempo y el digest.
func TestInvitation_ExactShape(t *testing.T) {
	want := []string{
		"ID string", "TenantID string", "TokenHash []uint8", "RoleID *string", "ExpiresAt time.Time",
		"CreatedBy string", "RedeemedBy *string", "RedeemedAt *time.Time", "RevokedAt *time.Time",
		"CreatedAt time.Time",
	}
	if got := fieldsOf(Invitation{}); strings.Join(got, "; ") != strings.Join(want, "; ") {
		t.Errorf("Invitation tiene los campos\n  %v\nquiere exactamente\n  %v", got, want)
	}
}
