// Porta internal/catalogimport/validator.go @ 3c74b80 (los decodificadores de campo y las utilidades de los mensajes, líneas 746-876; partido de validator.go por E-13)

package catalogimport

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// ============================ decodificadores de campo ============================

// requiredText decodifica un campo textual obligatorio.
func (v *validation) requiredText(l loc, subject, field, human string, raw json.RawMessage) string {
	s, ok := v.optionalText(l, subject, field, human, raw)
	if !ok {
		return ""
	}
	if strings.TrimSpace(s) == "" {
		v.c.at(l, field, subject+" no tiene "+human+": es obligatorio.")
		return ""
	}
	return s
}

// optionalText decodifica un campo textual opcional. ok=false significa que el
// campo vino con otro tipo (el defecto ya quedó anotado); ausente devuelve la
// cadena vacía con ok=true.
func (v *validation) optionalText(l loc, subject, field, human string, raw json.RawMessage) (string, bool) {
	if isAbsent(raw) {
		return "", true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		v.c.at(l, field, subject+": "+human+" debe ir entre comillas (es un texto, no un número ni una lista).")
		return "", false
	}
	return s, true
}

// plainText decodifica un campo textual opcional del que solo importa el valor.
func (v *validation) plainText(l loc, subject, field, human string, raw json.RawMessage) string {
	s, _ := v.optionalText(l, subject, field, human, raw)
	return s
}

// requiredPrice decodifica un importe obligatorio. El mensaje del tipo equivocado
// nombra el error real que se ve en la práctica: el precio escrito como texto, con
// símbolo de moneda o separador de miles, que es la salida típica de un LLM y de
// una hoja de cálculo.
func (v *validation) requiredPrice(l loc, subject, field, human string, raw json.RawMessage) float64 {
	if isAbsent(raw) {
		v.c.at(l, field, subject+" no tiene "+human+": es obligatorio.")
		return 0
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		v.c.at(l, field, subject+": "+human+" debe ser un número, sin comillas, sin símbolo de moneda y sin separadores de miles (18000, no \"$18.000\").")
		return 0
	}
	if f < 0 {
		v.c.at(l, field, subject+": "+human+" no puede ser negativo.")
		return 0
	}
	return f
}

// ============================ utilidades ============================

// isAbsent indica si un campo no vino en el JSON (o vino como null, que para el
// contrato es lo mismo: no hay valor).
func isAbsent(raw json.RawMessage) bool {
	return len(raw) == 0 || string(raw) == "null"
}

// categorySubject nombra una categoría en los mensajes: por su nombre si lo tiene,
// por su código si no, y por su posición si le falta todo.
func categorySubject(index int, label, code string) string {
	switch {
	case label != "":
		return "la categoría " + strconv.Quote(label)
	case code != "":
		return "la categoría con código " + strconv.Quote(code)
	default:
		return "la categoría " + strconv.Itoa(index+1)
	}
}

// itemSubject nombra un artículo en los mensajes. La posición va SIEMPRE —es lo
// único que no puede faltar y lo que ata la frase con los índices de la
// respuesta— y el nombre se añade cuando existe.
func itemSubject(index int, label, categorySubj string) string {
	s := "el artículo " + strconv.Itoa(index+1)
	if label != "" {
		s += " (" + strconv.Quote(label) + ")"
	}
	return s + " de " + categorySubj
}

// scalarToText convierte a texto un valor simple de JSON. Es la MISMA conversión
// que hace el parseo del runtime al poblar los atributos: si divergieran, un
// documento válido produciría un catálogo distinto del que el motor lee.
func scalarToText(value any) (string, bool) {
	switch t := value.(type) {
	case string:
		return t, true
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(t), true
	default:
		return "", false
	}
}

// trimNumber escribe un número como lo escribiría una persona (sin ceros de
// relleno) para citarlo en un mensaje.
func trimNumber(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// malformedReason traduce el fallo de encoding/json a algo que un dueño de negocio
// pueda accionar: dónde mirar, no qué token esperaba el parser.
func malformedReason(raw []byte, err error) string {
	var se *json.SyntaxError
	if errors.As(err, &se) {
		return "el archivo no es un JSON válido: hay un error de escritura hacia la línea " + strconv.Itoa(lineAt(raw, se.Offset)) + ". Revisa que no falte una coma, una comilla o una llave."
	}
	return "el archivo debe ser un objeto JSON con \"format\", \"version\" y \"catalog\": lo que llegó no tiene esa forma."
}

// lineAt devuelve la línea (base 1) en la que cae un desplazamiento en bytes.
func lineAt(raw []byte, offset int64) int {
	if offset < 0 {
		offset = 0
	}
	if offset > int64(len(raw)) {
		offset = int64(len(raw))
	}
	return 1 + bytes.Count(raw[:offset], []byte{'\n'})
}
