package cosa

// Doble es el MISMO código que cosatest/doble.go, pero en un paquete cuyo nombre no termina
// en «test»: no hay exención, así que su 0 % en el perfil muerde.
type Doble struct{ n int }

// Leer devuelve el valor y lo incrementa.
func (d *Doble) Leer() int {
	d.n++
	return d.n
}
