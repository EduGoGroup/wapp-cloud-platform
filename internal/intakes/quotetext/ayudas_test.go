package quotetext_test

import (
	"errors"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// asPendingPrice envuelve errors.As para que el test se lea. Existe solo para no
// repetir la declaración del puntero en cada caso.
func asPendingPrice(err error, out **intakes.PendingPriceError) bool {
	return errors.As(err, out)
}
