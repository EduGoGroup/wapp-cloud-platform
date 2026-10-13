//go:build integracion

package procesos

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Las tablas adversarias del callback (reglas.md §2): lo que un puente roto —o alguien que sondea—
// pone en las tres cabeceras y en el cuerpo. Se afirma lo que hace el binario viejo, también donde es
// más laxo que el contrato escrito (los casos «se acepta»).

// p6MaxCallbackBody es el techo del cuerpo del callback: 64 KiB.
const p6MaxCallbackBody = 64 * 1024

// p6AuthCase es una forma de estropear las cabeceras de un callback bien firmado. accepted dice si el
// servidor lo deja pasar igual (200, sin cambio) o lo corta con el 401 único.
type p6AuthCase struct {
	name     string
	mutate   func(cb *crmFakeCallback)
	accepted bool
}

// p6Unix devuelve el X-Wapp-Timestamp del callback como entero, o 0 si no lo es.
func p6Unix(cb *crmFakeCallback) int64 {
	ts, err := strconv.ParseInt(cb.Timestamp, 10, 64)
	if err != nil {
		return 0
	}
	return ts
}

// p6SignatureCases son los adversarios de X-Wapp-Signature.
func (w *p6World) p6SignatureCases() []p6AuthCase {
	hexOf := func(cb *crmFakeCallback) string { return strings.TrimPrefix(cb.Signature, crmFakeSignaturePrefix) }
	return []p6AuthCase{
		{name: "firma ausente", mutate: func(cb *crmFakeCallback) { cb.Signature = "" }},
		{name: "firma de otro secreto", mutate: func(cb *crmFakeCallback) {
			cb.Signature = crmFakeSignaturePrefix + crmFakeSign(w.otherSecret, p6Unix(cb), cb.Body)
		}},
		{name: "cuerpo cambiado tras firmar", mutate: func(cb *crmFakeCallback) { cb.Body = append(bytes.Clone(cb.Body), ' ') }},
		{name: "prefijo v1== (separador repetido)", mutate: func(cb *crmFakeCallback) { cb.Signature = "v1==" + hexOf(cb) }},
		{name: "prefijo repetido", mutate: func(cb *crmFakeCallback) { cb.Signature = "v1=v1=" + hexOf(cb) }},
		{name: "prefijo V1= en mayúscula", mutate: func(cb *crmFakeCallback) { cb.Signature = "V1=" + hexOf(cb) }},
		{name: "prefijo v2=", mutate: func(cb *crmFakeCallback) { cb.Signature = "v2=" + hexOf(cb) }},
		{name: "prefijo v1= sin firma", mutate: func(cb *crmFakeCallback) { cb.Signature = "v1=" }},
		{name: "firma que no es hexadecimal", mutate: func(cb *crmFakeCallback) { cb.Signature = "v1=" + strings.Repeat("z", 64) }},
		{name: "firma en dígitos no ASCII", mutate: func(cb *crmFakeCallback) { cb.Signature = "v1=" + strings.Repeat("١", 64) }},
		{name: "firma truncada", mutate: func(cb *crmFakeCallback) { cb.Signature = cb.Signature[:len(cb.Signature)-2] }},
		{name: "firma con U+00A0 delante", mutate: func(cb *crmFakeCallback) { cb.Signature = "\u00a0" + cb.Signature }},
		// 🔴 Más laxo que el contrato («hexadecimal minúscula», prefijo `v1=`): el servidor decodifica el
		// hexadecimal antes de comparar y recorta el prefijo solo si está.
		{name: "hexadecimal en MAYÚSCULAS", accepted: true, mutate: func(cb *crmFakeCallback) { cb.Signature = "v1=" + strings.ToUpper(hexOf(cb)) }},
		{name: "firma SIN el prefijo v1=", accepted: true, mutate: func(cb *crmFakeCallback) { cb.Signature = hexOf(cb) }},
	}
}

// p6TenantCases son los adversarios de X-Wapp-Tenant.
func (w *p6World) p6TenantCases() []p6AuthCase {
	return []p6AuthCase{
		{name: "tenant ausente", mutate: func(cb *crmFakeCallback) { cb.Tenant = "" }},
		{name: "tenant solo espacios", mutate: func(cb *crmFakeCallback) { cb.Tenant = " \u00a0 " }},
		{name: "tenant con separador repetido", mutate: func(cb *crmFakeCallback) { cb.Tenant = "a@@b" }},
		{name: "tenant con @@ pegado", mutate: func(cb *crmFakeCallback) { cb.Tenant += "@@" + cb.Tenant }},
		{name: "tenant en dígitos no ASCII", mutate: func(cb *crmFakeCallback) { cb.Tenant = p9ArabicDigits(cb.Tenant) }},
		{name: "tenant en mayúsculas", mutate: func(cb *crmFakeCallback) { cb.Tenant = strings.ToUpper(cb.Tenant) }},
		{name: "tenant con U+00A0 dentro", mutate: func(cb *crmFakeCallback) { cb.Tenant = cb.Tenant[:8] + "\u00a0" + cb.Tenant[8:] }},
		{name: "el slug en vez del id", mutate: func(cb *crmFakeCallback) { cb.Tenant = "p6-crm" }},
		// La cabecera se recorta (también los espacios Unicode) antes de buscar el secreto.
		{name: "tenant con U+00A0 en los extremos", accepted: true, mutate: func(cb *crmFakeCallback) { cb.Tenant = "\u00a0" + cb.Tenant + "\u00a0" }},
		{name: "tenant con espacios en los extremos", accepted: true, mutate: func(cb *crmFakeCallback) { cb.Tenant = "  " + cb.Tenant + " " }},
	}
}

// p6TimestampCases son los adversarios de X-Wapp-Timestamp (la ventana va en callbackWindow).
func (w *p6World) p6TimestampCases() []p6AuthCase {
	return []p6AuthCase{
		{name: "timestamp ausente", mutate: func(cb *crmFakeCallback) { cb.Timestamp = "" }},
		{name: "timestamp en dígitos no ASCII", mutate: func(cb *crmFakeCallback) { cb.Timestamp = p9ArabicDigits(cb.Timestamp) }},
		{name: "timestamp con decimales", mutate: func(cb *crmFakeCallback) { cb.Timestamp += ".0" }},
		{name: "timestamp en milisegundos", mutate: func(cb *crmFakeCallback) { cb.Timestamp += "000" }},
		{name: "timestamp con separador repetido", mutate: func(cb *crmFakeCallback) { cb.Timestamp = cb.Timestamp[:5] + "@@" + cb.Timestamp[5:] }},
		{name: "timestamp con U+00A0 dentro", mutate: func(cb *crmFakeCallback) { cb.Timestamp = cb.Timestamp[:5] + "\u00a0" + cb.Timestamp[5:] }},
		{name: "timestamp en RFC3339", mutate: func(cb *crmFakeCallback) { cb.Timestamp = time.Now().UTC().Format(time.RFC3339) }},
		{name: "timestamp de otro segundo", mutate: func(cb *crmFakeCallback) { cb.Timestamp = strconv.FormatInt(p6Unix(cb)+1, 10) }},
		// El timestamp se recorta y se lee con strconv.ParseInt: el signo y los espacios de los extremos pasan.
		{name: "timestamp con espacios en los extremos", accepted: true, mutate: func(cb *crmFakeCallback) { cb.Timestamp = " " + cb.Timestamp + " " }},
		{name: "timestamp con U+00A0 delante", accepted: true, mutate: func(cb *crmFakeCallback) { cb.Timestamp = "\u00a0" + cb.Timestamp }},
		{name: "timestamp con signo +", accepted: true, mutate: func(cb *crmFakeCallback) { cb.Timestamp = "+" + cb.Timestamp }},
	}
}

// callbackAuthAdversarial recorre las tres tablas de cabeceras sobre un callback que, bien firmado, no
// cambiaría nada. Lo rechazado contesta SIEMPRE el mismo 401 sin detalle y no mueve una columna de la
// solicitud; lo aceptado contesta 200 sin cambio. En ningún caso el cliente recibe un aviso.
func (w *p6World) callbackAuthAdversarial(t *testing.T) {
	status := w.reflection(t).CRMStatus.String
	cases := append(append(w.p6SignatureCases(), w.p6TenantCases()...), w.p6TimestampCases()...)
	for _, c := range cases {
		mark := p9Scalar(t, w.sc.DB, p6IntakeMark, w.approved)
		cb := w.signed(w.noop(t))
		c.mutate(&cb)
		r := w.post(t, cb)
		if c.accepted {
			p6WantApplied(t, "callback con "+c.name+" (se acepta)", r, w.approved, status, false)
			continue
		}
		p6WantUnauthenticated(t, "callback con "+c.name, r)
		p6WantMark(t, w.sc, "tras el callback con "+c.name, mark, p6IntakeMark, w.approved)
	}
	w.sc.expectNoPendingText(t, "tras la tabla de cabeceras del callback")
}

// p6BodyCase es un cuerpo de callback BIEN firmado que la puerta tiene que rechazar: el código, un
// trozo del motivo, si el schema PUBLICADO lo da por válido y cuántos ERROR deja en el log.
type p6BodyCase struct {
	name   string
	body   []byte
	code   int
	text   string
	schema bool
	errors int
}

// p6BodyRejections es la tabla de cuerpos rechazados.
func (w *p6World) p6BodyRejections(t *testing.T) []p6BodyCase {
	t.Helper()
	const statusMsg = "status debe ser uno de: paid, preparing, delivered, rejected"
	now := time.Now()
	// doc es un intake.status válido de la solicitud aprobada con una mutación encima.
	doc := func(mutate func(d map[string]any)) []byte {
		d := map[string]any{
			"contract_version": "1", "verb": "intake.status", "intake_id": w.approved,
			"status": "paid", "occurred_at": now.UTC().Format(time.RFC3339),
		}
		mutate(d)
		body, err := json.Marshal(d)
		if err != nil {
			t.Fatalf("serializar el cuerpo adversario: %v", err)
		}
		return body
	}
	set := func(field string, value any) []byte { return doc(func(d map[string]any) { d[field] = value }) }
	intake := func(id string) []byte { return crmFakeStatusBody(t, id, "paid", "", now) }
	const reflectMsg = "no se pudo aplicar el estado"
	base := []p6BodyCase{
		// El campo de más se rechaza y se NOMBRA: `tenant` es el primero que un puente manda de más.
		{name: "campo tenant en el cuerpo", body: set("tenant", "p6-crm"), code: 422, text: `el cuerpo trae un campo que el contrato no admite: \"tenant\"`},
		{name: "campo con separador repetido", body: set("a@@b", "١٢٣"), code: 422, text: `el cuerpo trae un campo que el contrato no admite: \"a@@b\"`},
		{name: "contract_version numérica", body: set("contract_version", 1), code: 422, text: "el campo contract_version tiene un tipo que el contrato no admite"},
		{name: "contract_version 2", body: set("contract_version", "2"), code: 422, text: `contract_version debe ser \"1\"`},
		{name: "contract_version en dígitos no ASCII", body: set("contract_version", "١"), code: 422, text: `contract_version debe ser \"1\"`},
		{name: "el verbo de la ida", body: set("verb", "intake.push"), code: 422, text: `verb debe ser \"intake.status\"`},
		{name: "occurred_at que no es una fecha", body: set("occurred_at", "ayer"), code: 422, text: "occurred_at debe ser una marca RFC3339"},
		{name: "occurred_at en dígitos no ASCII", body: set("occurred_at", p9ArabicDigits(now.UTC().Format(time.RFC3339))), code: 422, text: "occurred_at debe ser una marca RFC3339"},
		{name: "intake_id solo espacios", body: set("intake_id", " \u00a0 "), code: 422, text: "intake_id es obligatorio"},
		{name: "no es JSON", body: []byte(`{`), code: 422, text: "el cuerpo no es un intake.status válido"},
		{name: "es una lista", body: []byte(`[]`), code: 422, text: "tiene un tipo que el contrato no admite"},
		{name: "cuerpo vacío", body: []byte{}, code: 422, text: "el cuerpo no es un intake.status válido"},
		{name: "solicitud inexistente", body: intake(uuidAleatorio(t)), code: 404, text: p6IntakeNotFound, schema: true},
		// 🔴 El schema pide que intake_id sea un uuid; el servidor no lo valida y el SQL revienta: 500 y ERROR.
		{name: "intake_id con separador repetido", body: intake("a@@b"), code: 500, text: reflectMsg, errors: 1},
		{name: "intake_id en dígitos no ASCII", body: intake(p9ArabicDigits(w.approved)), code: 500, text: reflectMsg, errors: 1},
		{name: "intake_id con U+00A0 en los extremos", body: intake("\u00a0" + w.approved + "\u00a0"), code: 500, text: reflectMsg, errors: 1},
	}
	cases := make([]p6BodyCase, 0, len(base)+20)
	cases = append(cases, base...)
	for field, text := range map[string]string{
		"contract_version": `contract_version debe ser \"1\"`, "verb": `verb debe ser \"intake.status\"`,
		"intake_id": "intake_id es obligatorio", "status": "status es obligatorio", "occurred_at": "occurred_at es obligatorio",
	} {
		cases = append(cases, p6BodyCase{name: "sin " + field, body: doc(func(d map[string]any) { delete(d, field) }), code: 422, text: text})
	}
	for _, status := range []string{"shipped", "confirmed", "pending_approval", "PAID", "Paid", "paid ", "paid\u00a0", "\u00a0paid", "pa@@id", "paid@@paid", "١٢٣", "paiԁ"} {
		cases = append(cases, p6BodyCase{name: "estado desconocido «" + status + "»", body: crmFakeStatusBody(t, w.approved, status, "", now), code: 422, text: statusMsg})
	}
	cases = append(cases, p6BodyCase{name: "estado vacío", body: crmFakeStatusBody(t, w.approved, "", "", now), code: 422, text: "status es obligatorio"})
	return cases
}

// callbackBodyAdversarial: un callback BIEN firmado con un cuerpo que no es un `intake.status` del
// contrato se rechaza sin tocar la solicitud ni avisar al cliente; los estados desconocidos, con un 422
// que nombra los cuatro canónicos. Cada caso se coteja además con el schema publicado. Y el techo de
// 64 KiB: justo en el techo entra, un byte más es un 413 antes incluso de mirar la firma.
func (w *p6World) callbackBodyAdversarial(t *testing.T) {
	for _, c := range w.p6BodyRejections(t) {
		mark := p9Scalar(t, w.sc.DB, p6IntakeMark, w.approved)
		p6WantError(t, "callback con "+c.name, w.post(t, w.signed(c.body)), c.code, c.text)
		p6WantMark(t, w.sc, "tras el callback con "+c.name, mark, p6IntakeMark, w.approved)
		if valid := w.crm.ValidateStatus(c.body) == nil; valid != c.schema {
			t.Errorf("callback con %s: el schema publicado lo da por válido=%v, quería %v", c.name, valid, c.schema)
		}
		w.errors[p6MsgReflectFailed] += c.errors
	}

	// El id en mayúsculas es el mismo uuid: se acepta, y la respuesta devuelve el id tal como vino.
	status := w.reflection(t).CRMStatus.String
	upper := strings.ToUpper(w.approved)
	r := w.post(t, w.signed(crmFakeStatusBody(t, upper, status, "", time.Now())))
	p6WantApplied(t, "callback con el intake_id en mayúsculas", r, upper, status, false)

	// El techo: el cuerpo se rellena con espacios DETRÁS del JSON, que es un cuerpo válido y firmable.
	sized := func(n int) []byte {
		body := w.noop(t)
		return append(body, bytes.Repeat([]byte(" "), n-len(body))...)
	}
	p6WantApplied(t, "callback de 64 KiB justos", w.post(t, w.signed(sized(p6MaxCallbackBody))), w.approved, status, false)
	mark := p9Scalar(t, w.sc.DB, p6IntakeMark, w.approved)
	// Los tres que siguen TIENEN que rechazarse: van por postRejected, que da por rechazo la respuesta
	// de error o, si no llega ninguna, el corte de la conexión al enviar. La marca de después dice que
	// nada se aplicó en ninguno de los dos casos.
	over := w.signed(sized(p6MaxCallbackBody + 1))
	if r, answered := w.postRejected(t, over); answered {
		p6WantError(t, "callback de 64 KiB + 1", r, http.StatusRequestEntityTooLarge, `"max_bytes":65536`)
	}
	over.Signature = crmFakeSignaturePrefix + strings.Repeat("0", 64)
	if r, answered := w.postRejected(t, over); answered {
		p6WantError(t, "callback de 64 KiB + 1 mal firmado", r, http.StatusRequestEntityTooLarge, `"max_bytes":65536`)
	}
	// La ventana va ANTES que el cuerpo: fuera de ventana ni se lee. Es el que el servidor contesta
	// con el cuerpo a medio llegar, y por eso el que puede acabar en corte.
	stale := crmFakeSignedCallback(w.secret, w.sc.Tenant, time.Now().Add(-time.Hour), sized(p6MaxCallbackBody+1))
	if r, answered := w.postRejected(t, stale); answered {
		p6WantUnauthenticated(t, "callback de 64 KiB + 1 fuera de ventana", r)
	}
	p6WantMark(t, w.sc, "tras los callbacks sobre el techo", mark, p6IntakeMark, w.approved)
	w.sc.expectNoPendingText(t, "tras la tabla de cuerpos del callback")
}
