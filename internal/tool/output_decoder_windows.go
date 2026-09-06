//go:build windows

package tool

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/sys/windows"
)

const windowsUTF8CodePage = 65001

type windowsOutputDecoder struct {
	codePage uint32
	pending  []byte
}

func newOutputDecoder(name string) (outputDecoder, error) {
	if !isUTF8Encoding(name) {
		return newTextOutputDecoder(name)
	}
	if strings.TrimSpace(name) != "" {
		return passthroughOutputDecoder{}, nil
	}
	return &windowsOutputDecoder{codePage: windows.GetACP()}, nil
}

func (d *windowsOutputDecoder) Decode(data []byte) []byte {
	combined := append(append([]byte(nil), d.pending...), data...)
	d.pending = nil
	if len(combined) == 0 {
		return nil
	}
	if d.codePage == windowsUTF8CodePage {
		return d.decodeUTF8(combined)
	}
	if utf8.Valid(combined) {
		return combined
	}
	if suffix := incompleteUTF8Suffix(combined); suffix > 0 {
		prefix := combined[:len(combined)-suffix]
		if utf8.Valid(prefix) {
			d.pending = append([]byte(nil), combined[len(combined)-suffix:]...)
			return prefix
		}
	}
	if suffix := incompleteDBCSSuffix(d.codePage, combined); suffix > 0 {
		d.pending = append([]byte(nil), combined[len(combined)-suffix:]...)
		combined = combined[:len(combined)-suffix]
	}
	return decodeWindowsCodePage(d.codePage, combined)
}

func (d *windowsOutputDecoder) decodeUTF8(data []byte) []byte {
	if suffix := incompleteUTF8Suffix(data); suffix > 0 {
		d.pending = append([]byte(nil), data[len(data)-suffix:]...)
		return data[:len(data)-suffix]
	}
	return data
}

func (d *windowsOutputDecoder) Flush() []byte {
	data := d.pending
	d.pending = nil
	if len(data) == 0 {
		return nil
	}
	return decodeWindowsCodePage(d.codePage, data)
}

func decodeWindowsCodePage(codePage uint32, data []byte) []byte {
	if len(data) == 0 || codePage == windowsUTF8CodePage || utf8.Valid(data) {
		return data
	}
	wideLength, err := windows.MultiByteToWideChar(codePage, 0, &data[0], int32(len(data)), nil, 0)
	if err != nil || wideLength <= 0 {
		return data
	}
	wide := make([]uint16, wideLength)
	written, err := windows.MultiByteToWideChar(codePage, 0, &data[0], int32(len(data)), &wide[0], wideLength)
	if err != nil || written <= 0 {
		return data
	}
	return []byte(string(utf16.Decode(wide[:written])))
}

func incompleteUTF8Suffix(data []byte) int {
	if len(data) == 0 || utf8.Valid(data) {
		return 0
	}
	start := len(data) - 1
	for start >= 0 && data[start]&0xc0 == 0x80 {
		start--
	}
	if start < 0 {
		return 0
	}
	width := utf8LeadingWidth(data[start])
	if width <= 1 || len(data)-start >= width {
		return 0
	}
	return len(data) - start
}

func utf8LeadingWidth(value byte) int {
	switch {
	case value < 0x80:
		return 1
	case value >= 0xc2 && value <= 0xdf:
		return 2
	case value >= 0xe0 && value <= 0xef:
		return 3
	case value >= 0xf0 && value <= 0xf4:
		return 4
	default:
		return 0
	}
}

func incompleteDBCSSuffix(codePage uint32, data []byte) int {
	if !isDBCSCodePage(codePage) {
		return 0
	}
	for index := 0; index < len(data); index++ {
		if !isDBCSLeadByte(codePage, data[index]) {
			continue
		}
		if index+1 == len(data) {
			return 1
		}
		index++
	}
	return 0
}

func isDBCSCodePage(codePage uint32) bool {
	switch codePage {
	case 932, 936, 949, 950, 1361, 20932:
		return true
	default:
		return false
	}
}

func isDBCSLeadByte(codePage uint32, value byte) bool {
	switch codePage {
	case 932:
		return (value >= 0x81 && value <= 0x9f) || (value >= 0xe0 && value <= 0xfc)
	case 936, 949, 950, 1361, 20932:
		return value >= 0x81 && value <= 0xfe
	default:
		return false
	}
}
