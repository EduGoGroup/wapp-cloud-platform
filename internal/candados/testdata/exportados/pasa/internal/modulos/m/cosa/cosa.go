package cosa

// Limite es una constante exportada.
const Limite = 3

// Cosa es un tipo exportado; su campo no se exige.
type Cosa struct{ Campo int }

// Medir es un método exportado.
func (c Cosa) Medir() int { return c.Campo + interno() }

// Hacer es una función exportada.
func Hacer() int { return Limite }

func interno() int { return 0 }
