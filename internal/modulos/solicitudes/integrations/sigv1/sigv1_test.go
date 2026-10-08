package sigv1

import (
	"math"
	"strings"
	"testing"
)

// Los valores esperados de este fichero son VECTORES LITERALES calculados con el
// paquete viejo (internal/integrations/sigv1 @ 3a21138): el candado de fronteras
// impide importarlo desde aquí, así que la equivalencia viejo ↔ nuevo se fija con
// lo que el viejo devolvió para este mismo corpus.

const (
	baseSecret    = "s3cr3t"
	baseTimestamp = int64(1754611200)
	baseBody      = `{"a":1}`
	// baseSignature es Sign(baseSecret, baseTimestamp, baseBody) según el paquete viejo.
	baseSignature = "438b3ce20dc21dea8a3cb03b77d6bc396f1be681f0739768739462327f4159cd"
)

// TestSign_MatchesTheOldPackageVectors: la cadena canónica es "v1:<timestamp>:<body crudo>" y la
// firma su HMAC-SHA256 en hex. Cada vector es lo que firmó el paquete viejo, incluidos los casos
// en que el body o el timestamp podrían confundir a una cadena canónica mal armada.
func TestSign_MatchesTheOldPackageVectors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		secret    string
		timestamp int64
		body      string
		want      string
	}{
		{name: "baseline", secret: baseSecret, timestamp: baseTimestamp, body: baseBody, want: baseSignature},
		{name: "empty body", secret: baseSecret, timestamp: baseTimestamp, body: "", want: "eec931c08d897fe3fe8be887805d9ba905507af38044722d134d23fda0aed6f8"},
		{name: "body with colons", secret: baseSecret, timestamp: baseTimestamp, body: "a:b:c", want: "df2c175c1442489aef13a95f130dac5dba3dd2e2ea6f8cc495b6b061d717503d"},
		{name: "body that looks canonical", secret: baseSecret, timestamp: baseTimestamp, body: "v1:1754611200:{}", want: "d5cddecb0325f334082f1a614db3f8e67e3f37976ce0e377ed8771df0b89e766"},
		{name: "body with invalid utf8 and line breaks", secret: baseSecret, timestamp: baseTimestamp, body: "\xff\xfe\n\r\n\x00fin", want: "20156baac3120dd07d8f26d4b9b67fc483d326021af45fc98d7288776f1428c1"},
		{name: "timestamp zero", secret: baseSecret, timestamp: 0, body: baseBody, want: "e7748934f9d17d4ad22c6ddfb018380149af924ebf0b6d8ce39249d03867881c"},
		{name: "negative timestamp", secret: baseSecret, timestamp: -1, body: baseBody, want: "254e232bb33f9e27a95bb541b2f46dfb56d3fa4df76a2eb0300204c80c83e10f"},
		{name: "max int64 timestamp", secret: baseSecret, timestamp: math.MaxInt64, body: baseBody, want: "240be78cdf7727e79a3ba940281f088db1a015ac19855f9d94a9e55dc472ee56"},
		{name: "min int64 timestamp", secret: baseSecret, timestamp: math.MinInt64, body: baseBody, want: "297a204dfe76c657a3a841a5817b121908d86e472e3ac4e4a1e58e8349c9be6c"},
		{name: "empty secret", secret: "", timestamp: baseTimestamp, body: baseBody, want: "b9d57609255bbca31e029da3ac56b955ffc9c706977a9bc91964280228295228"},
		{name: "everything empty", secret: "", timestamp: 0, body: "", want: "fa33a7086f409676229546774bceeef0ce4507e4ef0a470aaca035b215d73f40"},
		// Los dos siguientes solo se distinguen por DÓNDE cae el separador: "v1:1:2:x" y "v1:12::x".
		{name: "digit on the body side of the separator", secret: baseSecret, timestamp: 1, body: "2:x", want: "75086ea32a8fbb78fc10f9f9a5a8c27676af18090dd8d850cbe5794f073377f7"},
		{name: "digit on the timestamp side of the separator", secret: baseSecret, timestamp: 12, body: ":x", want: "d770a8ec675e0879d36cce94fb1f974a4fd73305c105f1c321b1cae77b2cba27"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := Sign(c.secret, c.timestamp, []byte(c.body))
			if got != c.want {
				t.Errorf("Sign(%q, %d, %q) = %q, quería %q (lo que firma el paquete viejo)",
					c.secret, c.timestamp, c.body, got, c.want)
			}
			if !Verify(c.secret, c.timestamp, []byte(c.body), c.want) {
				t.Errorf("Verify rechaza el vector del paquete viejo para (%q, %d, %q)", c.secret, c.timestamp, c.body)
			}
		})
	}
}

// TestSign_IsLowercaseHexAndDeterministic: 64 caracteres de hex en minúscula, los mismos cada
// vez, y un body nil firma igual que uno vacío.
func TestSign_IsLowercaseHexAndDeterministic(t *testing.T) {
	t.Parallel()
	got := Sign(baseSecret, baseTimestamp, []byte(baseBody))
	if len(got) != 64 {
		t.Errorf("la firma mide %d caracteres, quería 64 (SHA-256 en hex)", len(got))
	}
	if strings.Trim(got, "0123456789abcdef") != "" {
		t.Errorf("la firma %q no es hex en minúscula", got)
	}
	if again := Sign(baseSecret, baseTimestamp, []byte(baseBody)); again != got {
		t.Errorf("Sign no es determinista: %q y después %q", got, again)
	}
	if withNil, withEmpty := Sign(baseSecret, baseTimestamp, nil), Sign(baseSecret, baseTimestamp, []byte{}); withNil != withEmpty {
		t.Errorf("un body nil firma %q y uno vacío %q: debían coincidir", withNil, withEmpty)
	}
}

// TestVerify_RejectsWhenAnyInputDiffers: la firma ata el secreto, el timestamp y el body; con uno
// solo distinto deja de verificar.
func TestVerify_RejectsWhenAnyInputDiffers(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		key       string
		timestamp int64
		body      string
	}{
		{name: "other secret", key: "otro-secreto", timestamp: baseTimestamp, body: baseBody},
		{name: "secret differs in case", key: "S3cr3t", timestamp: baseTimestamp, body: baseBody},
		{name: "timestamp one second later", key: baseSecret, timestamp: baseTimestamp + 1, body: baseBody},
		{name: "timestamp with the sign flipped", key: baseSecret, timestamp: -baseTimestamp, body: baseBody},
		{name: "other body", key: baseSecret, timestamp: baseTimestamp, body: `{"a":2}`},
		{name: "body with a trailing newline", key: baseSecret, timestamp: baseTimestamp, body: baseBody + "\n"},
		{name: "empty body", key: baseSecret, timestamp: baseTimestamp, body: ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if Verify(c.key, c.timestamp, []byte(c.body), baseSignature) {
				t.Errorf("Verify(%q, %d, %q) aceptó la firma de otros datos", c.key, c.timestamp, c.body)
			}
		})
	}
}

// TestVerify_SignatureForms: qué formas de escribir la firma verifican. Se comparan los bytes
// decodificados, así que la caja del hex no importa (lo fija el paquete viejo); todo lo que no
// sea el hex a secas de la firma entera es false, y sin pánico.
func TestVerify_SignatureForms(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		signature string
		want      bool
	}{
		{name: "exact lowercase", signature: baseSignature, want: true},
		{name: "uppercase", signature: "438B3CE20DC21DEA8A3CB03B77D6BC396F1BE681F0739768739462327F4159CD", want: true},
		{name: "mixed case", signature: "438B3CE20Dc21dea8a3cb03b77d6bc396f1be681f0739768739462327f4159cd", want: true},
		{name: "empty", signature: "", want: false},
		{name: "not hex", signature: "no-es-hex-válido", want: false},
		{name: "with the header prefix", signature: SignatureHeader(baseSignature), want: false},
		{name: "with a 0x prefix", signature: "0x" + baseSignature, want: false},
		{name: "odd length", signature: baseSignature[:63], want: false},
		{name: "truncated to 31 bytes", signature: baseSignature[:62], want: false},
		{name: "truncated to one byte", signature: baseSignature[:2], want: false},
		{name: "one extra byte", signature: baseSignature + "00", want: false},
		{name: "leading space", signature: " " + baseSignature, want: false},
		{name: "trailing newline", signature: baseSignature + "\n", want: false},
		{name: "first nibble changed", signature: "0" + baseSignature[1:], want: false},
		{name: "last nibble changed", signature: baseSignature[:63] + "c", want: false},
		{name: "all zeros", signature: strings.Repeat("0", 64), want: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := Verify(baseSecret, baseTimestamp, []byte(baseBody), c.signature); got != c.want {
				t.Errorf("Verify(…, %q) = %v, quería %v", c.signature, got, c.want)
			}
		})
	}
}

// TestVerify_HasNoTimeWindow: sigv1 no tiene reloj. Una firma con un timestamp de 1970, o del
// año 292277026596, verifica igual que una de ahora: la ventana ±300 s es de quien llama.
func TestVerify_HasNoTimeWindow(t *testing.T) {
	t.Parallel()
	for _, ts := range []int64{0, 1, -1, math.MinInt64, math.MaxInt64} {
		sig := Sign(baseSecret, ts, []byte(baseBody))
		if !Verify(baseSecret, ts, []byte(baseBody), sig) {
			t.Errorf("Verify rechazó una firma bien hecha con timestamp %d: sigv1 no debe aplicar ventana temporal", ts)
		}
	}
}

// TestSignatureHeader_PrefixesTheSchemeVersion: la cabecera es "v1=" más lo recibido, sin tocarlo.
func TestSignatureHeader_PrefixesTheSchemeVersion(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "a signature", in: baseSignature, want: "v1=438b3ce20dc21dea8a3cb03b77d6bc396f1be681f0739768739462327f4159cd"},
		{name: "short hex", in: "abcd", want: "v1=abcd"},
		{name: "empty", in: "", want: "v1="},
		{name: "uppercase is not normalized", in: "ABCD", want: "v1=ABCD"},
		{name: "already prefixed is prefixed again", in: "v1=abcd", want: "v1=v1=abcd"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := SignatureHeader(c.in); got != c.want {
				t.Errorf("SignatureHeader(%q) = %q, quería %q", c.in, got, c.want)
			}
		})
	}
}
