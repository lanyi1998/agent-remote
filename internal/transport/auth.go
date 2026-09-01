package transport

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

func Authorized(request *http.Request, expectedToken string) bool {
	parts := strings.Fields(request.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return false
	}
	provided := parts[1]
	if provided == "" || expectedToken == "" || len(provided) != len(expectedToken) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expectedToken)) == 1
}
