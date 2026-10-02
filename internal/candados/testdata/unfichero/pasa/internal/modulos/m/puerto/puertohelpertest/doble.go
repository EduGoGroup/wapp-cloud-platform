package puertohelpertest

// Doble es un doble con lógica; D-F1-3 = sí lo deja fuera de este candado (D-F1-10: por el
// sufijo compuesto «helpertest» del nombre del paquete).
type Doble struct{ n int }

// Leer devuelve el valor y lo incrementa.
func (d *Doble) Leer() int {
	d.n++
	return d.n
}
