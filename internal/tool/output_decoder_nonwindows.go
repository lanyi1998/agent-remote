//go:build !windows

package tool

import "strings"

func newOutputDecoder(name string) (outputDecoder, error) {
	if strings.TrimSpace(name) == "" || isUTF8Encoding(name) {
		return passthroughOutputDecoder{}, nil
	}
	return newTextOutputDecoder(name)
}
