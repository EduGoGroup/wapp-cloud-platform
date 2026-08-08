package sigv1

import "testing"

func TestSignVerify_RoundTrip(t *testing.T) {
	sig := Sign("s3cr3t", 1754611200, []byte(`{"a":1}`))
	if !Verify("s3cr3t", 1754611200, []byte(`{"a":1}`), sig) {
		t.Fatal("una firma recién calculada debe verificar con el mismo secreto/timestamp/body")
	}
}

func TestVerify_SecretoDistintoFalla(t *testing.T) {
	sig := Sign("s3cr3t", 1754611200, []byte(`{"a":1}`))
	if Verify("otro-secreto", 1754611200, []byte(`{"a":1}`), sig) {
		t.Fatal("un secreto distinto no debe verificar")
	}
}

func TestVerify_TimestampDistintoFalla(t *testing.T) {
	sig := Sign("s3cr3t", 1754611200, []byte(`{"a":1}`))
	if Verify("s3cr3t", 1754611201, []byte(`{"a":1}`), sig) {
		t.Fatal("un timestamp distinto en la cadena canónica no debe verificar (anti-replay)")
	}
}

func TestVerify_BodyDistintoFalla(t *testing.T) {
	sig := Sign("s3cr3t", 1754611200, []byte(`{"a":1}`))
	if Verify("s3cr3t", 1754611200, []byte(`{"a":2}`), sig) {
		t.Fatal("un body distinto no debe verificar")
	}
}

func TestVerify_FirmaMalFormadaNoPanica(t *testing.T) {
	if Verify("s3cr3t", 1754611200, []byte(`{}`), "no-es-hex-válido") {
		t.Fatal("una firma no-hex debe rechazarse, no verificar")
	}
	if Verify("s3cr3t", 1754611200, []byte(`{}`), "") {
		t.Fatal("una firma vacía debe rechazarse")
	}
}

func TestSignatureHeader_LlevaElPrefijoDeVersion(t *testing.T) {
	got := SignatureHeader("abcd")
	if got != "v1=abcd" {
		t.Fatalf("SignatureHeader = %q, quiero %q", got, "v1=abcd")
	}
}

func TestSign_MismosInputsMismaFirma(t *testing.T) {
	a := Sign("s", 1, []byte("x"))
	b := Sign("s", 1, []byte("x"))
	if a != b {
		t.Fatal("Sign debe ser determinista para los mismos inputs")
	}
}
