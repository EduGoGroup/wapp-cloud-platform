// Porta internal/catalogimport/tabular.go @ 3c74b80 (la mini-sintaxis de las celdas múltiples y el precio, líneas 338-459 y 583-609; partido de tabular.go por E-13)

package catalogimport

import (
	"math"
	"strconv"
	"strings"
)

// ============================ celdas múltiples ============================

// tags lee las etiquetas: entradas de un solo campo separadas por «;».
func (p *tabular) tags(n int, subject, cell string) []string {
	entries := splitEntries(cell)
	if len(entries) == 0 {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.Contains(e, TabularFieldSeparator) {
			p.c.atRow(n, colTags, subject+": la etiqueta "+strconv.Quote(e)+" lleva «|»; las etiquetas se escriben "+
				"una detrás de otra separadas por «;», por ejemplo «decorada; sin_lactosa».")
			continue
		}
		out = append(out, e)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// attributes lee los pares clave|valor de la ficha.
func (p *tabular) attributes(n int, subject, cell string) map[string]string {
	entries := splitEntries(cell)
	if len(entries) == 0 {
		return nil
	}
	out := make(map[string]string, len(entries))
	for _, e := range entries {
		fields := splitFields(e)
		if len(fields) != 2 || fields[0] == "" || fields[1] == "" {
			p.c.atRow(n, colAtributos, subject+": el atributo "+strconv.Quote(e)+
				" se escribe «clave|valor», por ejemplo «porciones|10-12».")
			continue
		}
		out[fields[0]] = fields[1]
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// variants lee las presentaciones: `codigo|nombre|precio` por entrada.
func (p *tabular) variants(n int, subject, cell string) []ImportVariant {
	entries := splitEntries(cell)
	if len(entries) == 0 {
		return nil
	}
	out := make([]ImportVariant, 0, len(entries))
	for _, e := range entries {
		fields := splitFields(e)
		if len(fields) != 3 || fields[0] == "" || fields[1] == "" {
			p.c.atRow(n, colVariantes, subject+": la variante "+strconv.Quote(e)+
				" se escribe «codigo|nombre|precio», por ejemplo «V1|10-12 porciones|18000».")
			continue
		}
		out = append(out, ImportVariant{
			Code:  fields[0],
			Label: fields[1],
			Price: p.price(n, colVariantes, subject+", variante "+strconv.Quote(fields[0]), fields[2]),
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// components lee los integrantes del combo: `sku|cantidad` por entrada.
//
// LA CANTIDAD SE EXIGE, también cuando vale 1. En el JSON es omitible porque el
// contrato dice que ausente vale 1, pero la planilla la escribe SIEMPRE (template.go,
// componentsCell) y aceptar además el sku a secas sería tener dos gramáticas para lo
// mismo: la que emitimos y otra que solo conoce el parser.
func (p *tabular) components(n int, subject, cell string) []ImportComponent {
	entries := splitEntries(cell)
	if len(entries) == 0 {
		return nil
	}
	out := make([]ImportComponent, 0, len(entries))
	for _, e := range entries {
		fields := splitFields(e)
		if len(fields) != 2 || fields[0] == "" {
			p.c.atRow(n, colComponentes, subject+": el componente "+strconv.Quote(e)+
				" se escribe «sku|cantidad», por ejemplo «TEQUENOS-15|1».")
			continue
		}
		qty, err := strconv.Atoi(fields[1])
		if err != nil || qty < 1 {
			p.c.atRow(n, colComponentes, subject+", componente "+strconv.Quote(fields[0])+": la cantidad "+
				strconv.Quote(fields[1])+" debe ser un número entero de 1 o más.")
			continue
		}
		out = append(out, ImportComponent{SKU: fields[0], Qty: qty})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// price lee un importe de una celda. Es el ÚNICO campo que la lectura tabular juzga
// por su cuenta antes del validador, y no por gusto: en el documento el precio es un
// número, así que una celda ilegible no tiene forma de llegar al validador como
// «precio malo» —llegaría como 0, que es un precio válido— y el artículo se
// importaría regalado.
func (p *tabular) price(n int, column, subject, cell string) float64 {
	if cell == "" {
		p.c.atRow(n, column, subject+" no tiene el precio: es obligatorio.")
		return 0
	}
	f, err := strconv.ParseFloat(cell, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		p.c.atRow(n, column, subject+": el precio "+strconv.Quote(cell)+" no es un número; escríbelo sin símbolo "+
			"de moneda y sin separadores de miles (18000, no \"$18.000\").")
		return 0
	}
	return f
}

// splitEntries deshace una celda múltiple en sus entradas y descarta las vacías: un
// «;» de más al final de la celda es un desliz de quien escribe, no un defecto del
// catálogo.
func splitEntries(cell string) []string {
	if strings.TrimSpace(cell) == "" {
		return nil
	}
	parts := strings.Split(cell, TabularEntrySeparator)
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if e := strings.TrimSpace(part); e != "" {
			out = append(out, e)
		}
	}
	return out
}

// splitFields deshace UNA entrada en sus campos. Un campo no puede contener «|»: es
// el separador, y admitirlo dentro obligaría a inventar un escape que nadie va a
// teclear en una celda. Un nombre con «|» se importa por JSON.
func splitFields(entry string) []string {
	parts := strings.Split(entry, TabularFieldSeparator)
	for i, part := range parts {
		parts[i] = strings.TrimSpace(part)
	}
	return parts
}
