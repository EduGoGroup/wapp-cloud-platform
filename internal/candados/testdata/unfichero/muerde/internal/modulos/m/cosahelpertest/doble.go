package cosahelpertest

// Doble es un doble con lógica en un paquete …helpertest: D-F1-3 = sí lo deja fuera de este
// candado aunque no tenga test al lado, y D-F1-10 dice cómo se reconoce (el nombre del
// paquete termina en «helpertest»).
type Doble struct{ n int }

// Leer devuelve el valor y lo incrementa.
func (d *Doble) Leer() int {
	d.n++
	return d.n
}
