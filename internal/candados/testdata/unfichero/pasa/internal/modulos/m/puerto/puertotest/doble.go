package puertotest

// Doble es un doble con lógica; D-F1-3 = sí lo deja fuera de este candado.
type Doble struct{ n int }

// Leer devuelve el valor y lo incrementa.
func (d *Doble) Leer() int {
	d.n++
	return d.n
}
