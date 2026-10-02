package cosahelpertest

// Doble es un doble con lógica en un paquete …helpertest: nadie lo ejecuta desde ESTE paquete
// (lo ejecutan los tests de las implementaciones, que viven en otros), así que en el perfil
// sale al 0 %. D-F1-6 lo deja fuera de la cobertura por fichero, y D-F1-10 dice cómo se
// reconoce: el nombre del paquete termina en «helpertest».
type Doble struct{ n int }

// Leer devuelve el valor y lo incrementa.
func (d *Doble) Leer() int {
	d.n++
	return d.n
}
