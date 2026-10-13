package apipublica_test

// crmcallback_auth_test.go — la CREDENCIAL de G17 del contrato de MountCRMCallback: el tenant
// del header, la ventana anti-replay con el reloj inyectado, la firma sobre el cuerpo crudo y el
// techo del cuerpo. Es un trozo de crmcallback_test.go, partido por tema (05 E-13). Con entradas
// adversarias: cabeceras malformadas, dígitos no ASCII, espacios, timestamps extremos.

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/integrations/sigv1"
)

// crmWantRejected exige el 401 sin detalle y que no se haya pasado de la autenticación: ni gate,
// ni reflejo, ni aviso.
func crmWantRejected(t *testing.T, name string, rig *crmRig, req crmRequest) {
	t.Helper()
	rec := crmSend(rig.cara, req)
	wantCode(t, name, rec, http.StatusUnauthorized)
	wantExactBody(t, name, rec, crmUnauthorized)
	if len(rig.gate.tenants) != 0 || len(rig.reflector.calls) != 0 || len(rig.notifier.notices) != 0 {
		t.Errorf("%s: una petición sin autenticar pasó de la puerta (gate=%d, reflejos=%d, avisos=%d)",
			name, len(rig.gate.tenants), len(rig.reflector.calls), len(rig.notifier.notices))
	}
}

// TestMountCRMCallback_TenantHeader: sin tenant no hay conversación, y no se consulta nada.
func TestMountCRMCallback_TenantHeader(t *testing.T) {
	for name, tenant := range map[string]string{"missing": "", "blank": "   ", "tab": "\t"} {
		t.Run(name, func(t *testing.T) {
			rig := newCRMRig(t)
			req := crmGood(crmBody("paid"))
			req.tenant = tenant
			crmWantRejected(t, name, rig, req)
			if rig.secrets.calls != 0 {
				t.Errorf("sin tenant se consultó el secreto %d veces", rig.secrets.calls)
			}
		})
	}
	t.Run("surrounding_spaces_are_trimmed", func(t *testing.T) {
		rig := newCRMRig(t)
		req := crmGood(crmBody("paid"))
		req.tenant = "  " + tenantA + " "
		wantCode(t, "tenant con espacios en los bordes", crmSend(rig.cara, req), http.StatusOK)
		if rig.reflector.calls[0].tenant != tenantA {
			t.Errorf("el reflejo recibió el tenant %q, quiero el recortado", rig.reflector.calls[0].tenant)
		}
	})
	t.Run("tenant_without_integration_is_401_not_403", func(t *testing.T) {
		rig := newCRMRig(t)
		rig.gate.enabled = false
		const unknown = "cccccccc-cccc-cccc-cccc-cccccccccccc"
		crmWantRejected(t, "tenant sin integración", rig, crmSigned(unknown, crmFakeSigner, crmBody("paid"), crmNow.Unix()))
	})
	t.Run("secret_of_one_tenant_does_not_open_another", func(t *testing.T) {
		rig := newCRMRig(t)
		// Firmado con el secreto de tenantA, pero diciendo ser tenantB.
		crmWantRejected(t, "secreto ajeno", rig, crmSigned(tenantB, crmFakeSigner, crmBody("paid"), crmNow.Unix()))
	})
}

// TestMountCRMCallback_TimestampIsUnreadable: lo que no es un entero decimal en dígitos ASCII es
// el mismo 401, y se corta ANTES de leer el secreto. La firma de cada caso es la buena del
// instante actual: lo único malo es el header.
func TestMountCRMCallback_TimestampIsUnreadable(t *testing.T) {
	now := strconv.FormatInt(crmNow.Unix(), 10)
	cases := map[string]string{
		"missing":             "",
		"blank":               "   ",
		"word":                "ahora",
		"decimal":             now + ".5",
		"exponent":            "1.78e9",
		"hex":                 "0x6A0C1B40",
		"underscore":          now[:4] + "_" + now[4:],
		"inner_space":         now[:5] + " " + now[5:],
		"inner_nbsp":          now[:5] + " " + now[5:],
		"arabic_indic_digits": "١٧٨٨٢٦٣٢٠٠",
		"fullwidth_digits":    "１７８８２６３２００",
		"trailing_letter":     now + "s",
		"rfc3339":             "2026-09-01T12:00:00Z",
		"over_int64":          "9223372036854775808",
		"under_int64":         "-9223372036854775809",
		"twenty_digits":       strings.Repeat("9", 20),
		"two_signs":           "+-" + now,
		"comma_list":          now + "," + now,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			rig := newCRMRig(t)
			req := crmGood(crmBody("paid"))
			req.timestamp = raw
			crmWantRejected(t, name, rig, req)
			if rig.secrets.calls != 0 {
				t.Errorf("%s: con el timestamp ilegible se consultó el secreto %d veces, quiero 0", name, rig.secrets.calls)
			}
		})
	}
}

// TestMountCRMCallback_Window: ±300 s alrededor del reloj INYECTADO, con los dos bordes dentro.
// Cada caso va bien firmado con su propio timestamp: lo único que decide es la ventana.
func TestMountCRMCallback_Window(t *testing.T) {
	now := crmNow.Unix()
	cases := []struct {
		name string
		ts   int64
		want int
	}{
		{"exactly_now", now, http.StatusOK},
		{"four_minutes_ago", now - 240, http.StatusOK},
		{"300s_ago_is_inside", now - 300, http.StatusOK},
		{"300s_ahead_is_inside", now + 300, http.StatusOK},
		{"301s_ago", now - 301, http.StatusUnauthorized},
		{"301s_ahead", now + 301, http.StatusUnauthorized},
		{"ten_minutes_ago", now - 600, http.StatusUnauthorized},
		{"ten_minutes_ahead", now + 600, http.StatusUnauthorized},
		{"a_day_ago", now - 86400, http.StatusUnauthorized},
		{"epoch", 0, http.StatusUnauthorized},
		{"negative", -now, http.StatusUnauthorized},
		// El viejo aceptaba estos tres: la resta satura y su negación desborda.
		{"milliseconds_instead_of_seconds", now * 1000, http.StatusUnauthorized},
		{"year_2400", now + 374*365*86400, http.StatusUnauthorized},
		{"three_centuries_ahead", now + 300*365*86400, http.StatusUnauthorized},
		{"max_int64", math.MaxInt64, http.StatusUnauthorized},
		{"min_int64", math.MinInt64, http.StatusUnauthorized},
		{"three_centuries_ago", now - 300*365*86400, http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := newCRMRig(t)
			req := crmSigned(tenantA, crmFakeSigner, crmBody("paid"), tc.ts)
			if tc.want == http.StatusUnauthorized {
				crmWantRejected(t, tc.name, rig, req)
				return
			}
			wantCode(t, tc.name, crmSend(rig.cara, req), tc.want)
		})
	}
}

// TestMountCRMCallback_TimestampSpelling: los espacios de los bordes y el signo «+» no cambian
// el número, y el número es el que entra en la firma.
func TestMountCRMCallback_TimestampSpelling(t *testing.T) {
	for name, spell := range map[string]func(string) string{
		"surrounding_spaces": func(ts string) string { return "  " + ts + "\t" },
		"plus_sign":          func(ts string) string { return "+" + ts },
		"leading_zeros":      func(ts string) string { return "000" + ts },
	} {
		t.Run(name, func(t *testing.T) {
			rig := newCRMRig(t)
			req := crmGood(crmBody("paid"))
			req.timestamp = spell(req.timestamp)
			wantCode(t, name, crmSend(rig.cara, req), http.StatusOK)
		})
	}
}

// TestMountCRMCallback_Signature: la firma es sobre el timestamp del header y el cuerpo CRUDO,
// con el secreto del tenant. Todo lo demás es el mismo 401.
func TestMountCRMCallback_Signature(t *testing.T) {
	body := crmBody("paid")
	hex := sigv1.Sign(crmFakeSigner, crmNow.Unix(), []byte(body))

	rejected := map[string]string{
		"missing":                 "",
		"prefix_only":             "v1=",
		"other_secret":            "v1=" + sigv1.Sign(crmOtherSigner, crmNow.Unix(), []byte(body)),
		"empty_secret":            "v1=" + sigv1.Sign("", crmNow.Unix(), []byte(body)),
		"other_timestamp":         "v1=" + sigv1.Sign(crmFakeSigner, crmNow.Unix()-1, []byte(body)),
		"other_body":              "v1=" + sigv1.Sign(crmFakeSigner, crmNow.Unix(), []byte(crmBody("rejected"))),
		"truncated":               "v1=" + hex[:len(hex)-2],
		"odd_length":              "v1=" + hex[:len(hex)-1],
		"extra_byte":              "v1=" + hex + "00",
		"not_hex":                 "v1=" + strings.Repeat("z", len(hex)),
		"one_flipped_nibble":      "v1=" + hex[:len(hex)-1] + map[bool]string{true: "1", false: "0"}[hex[len(hex)-1] == '0'],
		"uppercase_prefix":        "V1=" + hex,
		"double_prefix":           "v1=v1=" + hex,
		"other_version_prefix":    "v2=" + hex,
		"sha256_prefix":           "sha256=" + hex,
		"leading_space":           " v1=" + hex,
		"trailing_space":          "v1=" + hex + " ",
		"space_after_prefix":      "v1= " + hex,
		"base64_instead_of_hex":   "v1=dmFsb3ItZmljdGljaW8tcXVlLW5vLWVzLWhleA==",
		"quoted":                  `v1="` + hex + `"`,
		"comma_separated_schemes": "v1=" + hex + ",v1=" + hex,
		"fullwidth_hex":           "v1=" + strings.Repeat("ａ", len(hex)),
	}
	for name, signature := range rejected {
		t.Run(name, func(t *testing.T) {
			rig := newCRMRig(t)
			req := crmGood(body)
			req.signature = signature
			crmWantRejected(t, name, rig, req)
		})
	}

	accepted := map[string]string{
		"canonical":     "v1=" + hex,
		"uppercase_hex": "v1=" + strings.ToUpper(hex),
		"bare_hex":      hex,
	}
	for name, signature := range accepted {
		t.Run(name, func(t *testing.T) {
			rig := newCRMRig(t)
			req := crmGood(body)
			req.signature = signature
			wantCode(t, name, crmSend(rig.cara, req), http.StatusOK)
		})
	}
}

// TestMountCRMCallback_SignatureCoversTheRawBody: el mismo JSON con otros bytes (un espacio, el
// orden de las claves) NO es el cuerpo firmado.
func TestMountCRMCallback_SignatureCoversTheRawBody(t *testing.T) {
	signed := crmBody("paid")
	for name, sent := range map[string]string{
		"extra_space":      strings.Replace(signed, `{"contract_version"`, `{ "contract_version"`, 1),
		"trailing_newline": signed + "\n",
		"status_swapped":   crmBody("delivered"),
		"empty":            "",
	} {
		t.Run(name, func(t *testing.T) {
			rig := newCRMRig(t)
			req := crmGood(signed)
			req.body = sent
			crmWantRejected(t, name, rig, req)
		})
	}
	t.Run("whitespace_is_fine_when_it_is_what_was_signed", func(t *testing.T) {
		rig := newCRMRig(t)
		wantCode(t, "cuerpo con espacios, firmado así", crmSend(rig.cara, crmGood("\n  "+signed+"\n")), http.StatusOK)
	})
}

// TestMountCRMCallback_SecretStoreFailureIs503: un fallo de infraestructura NO se disfraza de
// 401, deja su Warn, y el secreto no aparece en el log en ningún desenlace.
func TestMountCRMCallback_SecretStoreFailureIs503(t *testing.T) {
	rig := newCRMRig(t)
	rig.secrets.err = errors.New("no se pudo abrir el envelope")
	rec := crmSend(rig.cara, crmGood(crmBody("paid")))
	wantCode(t, "almacén de secretos caído", rec, http.StatusServiceUnavailable)
	wantErrorBody(t, "almacén de secretos caído", rec, "no se pudo verificar la petición")
	entry, ok := crmLogged(rig.h, "warn", "callback CRM: no se pudo leer el secreto del tenant")
	if !ok || entry.Fields["tenant"] != tenantA || entry.Fields["error"] == nil {
		t.Errorf("el fallo del almacén no dejó su Warn con tenant y error: %+v", rig.h.Log().Entries())
	}
	if len(rig.gate.tenants) != 0 || len(rig.reflector.calls) != 0 {
		t.Error("con el almacén de secretos caído se pasó de la puerta")
	}

	// Barrido: tras un recorrido por todos los desenlaces, el secreto no está en el log.
	rig = newCRMRig(t)
	crmSend(rig.cara, crmGood(crmBody("paid")))
	crmSend(rig.cara, crmSigned(tenantA, crmOtherSigner, crmBody("paid"), crmNow.Unix()))
	rig.gate.err = errors.New("bd caída")
	crmSend(rig.cara, crmGood(crmBody("paid")))
	rig.gate.err, rig.reflector.err = nil, errors.New("bd caída")
	crmSend(rig.cara, crmGood(crmBody("paid")))
	if logged := fmt.Sprintf("%+v", rig.h.Log().Entries()); strings.Contains(logged, crmFakeSigner) || strings.Contains(logged, crmOtherSigner) {
		t.Errorf("FUGA: el secreto de firma aparece en el log:\n%s", logged)
	}
}

// crmPadded devuelve un intake.status válido de exactamente n bytes: el JSON precedido de
// espacios, que son parte del cuerpo firmado y no cambian el valor.
func crmPadded(n int) string {
	body := crmBody("paid")
	return strings.Repeat(" ", n-len(body)) + body
}

// TestMountCRMCallback_BodyCeiling: 64 KiB justos entran; un byte más es el 413 con max_bytes,
// antes de consultar el secreto; y sin la ventana buena ni siquiera se lee el cuerpo.
func TestMountCRMCallback_BodyCeiling(t *testing.T) {
	const ceiling = 64 * 1024
	t.Run("exactly_64KiB", func(t *testing.T) {
		rig := newCRMRig(t)
		wantCode(t, "64 KiB justos", crmSend(rig.cara, crmGood(crmPadded(ceiling))), http.StatusOK)
	})
	t.Run("64KiB_plus_1_is_413", func(t *testing.T) {
		rig := newCRMRig(t)
		rec := crmSend(rig.cara, crmGood(crmPadded(ceiling+1)))
		wantCode(t, "64 KiB + 1", rec, http.StatusRequestEntityTooLarge)
		wantExactBody(t, "64 KiB + 1", rec, `{"error":"el cuerpo del callback excede el tamaño máximo de 65536 bytes","max_bytes":65536}`)
		if rig.secrets.calls != 0 || len(rig.gate.tenants) != 0 || len(rig.reflector.calls) != 0 {
			t.Errorf("el cuerpo excesivo pasó de la lectura (secretos=%d, gate=%d, reflejos=%d)",
				rig.secrets.calls, len(rig.gate.tenants), len(rig.reflector.calls))
		}
	})
	t.Run("huge_body_with_bad_signature_is_still_413", func(t *testing.T) {
		rig := newCRMRig(t)
		req := crmGood(crmPadded(ceiling + 1))
		req.signature = "v1=00"
		wantCode(t, "cuerpo excesivo con firma mala", crmSend(rig.cara, req), http.StatusRequestEntityTooLarge)
	})
	t.Run("huge_body_outside_the_window_is_401", func(t *testing.T) {
		rig := newCRMRig(t)
		crmWantRejected(t, "cuerpo excesivo fuera de ventana", rig,
			crmSigned(tenantA, crmFakeSigner, crmPadded(ceiling+1), crmNow.Unix()-3600))
	})
}
