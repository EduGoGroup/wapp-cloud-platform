package uno

import (
	"net/http"
	"testing"
)

func TestSoloTest(t *testing.T) {
	http.NewServeMux().Handle("/solo-test", http.NotFoundHandler())
}
