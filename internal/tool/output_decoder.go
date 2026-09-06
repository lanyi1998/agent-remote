package tool

import (
	"bytes"
	"fmt"
	"strings"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/transform"
)

type outputDecoder interface {
	Decode([]byte) []byte
	Flush() []byte
}

type passthroughOutputDecoder struct{}

func (passthroughOutputDecoder) Decode(data []byte) []byte { return data }

func (passthroughOutputDecoder) Flush() []byte { return nil }

func isUTF8Encoding(name string) bool {
	switch strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(name), "_", "-"), " ", "")) {
	case "", "utf-8", "utf8":
		return true
	default:
		return false
	}
}

type textOutputDecoder struct {
	transformer transform.Transformer
	pending     []byte
}

func newTextOutputDecoder(name string) (outputDecoder, error) {
	codec, err := outputEncoding(name)
	if err != nil {
		return nil, err
	}
	return &textOutputDecoder{transformer: codec.NewDecoder()}, nil
}

func outputEncoding(name string) (encoding.Encoding, error) {
	switch strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(name), "_", "-"), " ", "")) {
	case "", "utf-8", "utf8":
		return encoding.Nop, nil
	case "gbk", "cp936", "windows-936":
		return simplifiedchinese.GBK, nil
	case "gb18030":
		return simplifiedchinese.GB18030, nil
	case "big5", "big-5", "cp950", "windows-950":
		return traditionalchinese.Big5, nil
	case "shift-jis", "shiftjis", "sjis", "cp932", "windows-932":
		return japanese.ShiftJIS, nil
	case "windows-1252", "cp1252":
		return charmap.Windows1252, nil
	default:
		return nil, fmt.Errorf("unsupported output encoding %q", name)
	}
}

func (d *textOutputDecoder) Decode(data []byte) []byte {
	return d.decode(data, false)
}

func (d *textOutputDecoder) Flush() []byte {
	return d.decode(nil, true)
}

func (d *textOutputDecoder) decode(data []byte, atEOF bool) []byte {
	input := append(append([]byte(nil), d.pending...), data...)
	d.pending = nil
	if len(input) == 0 && !atEOF {
		return nil
	}
	var output bytes.Buffer
	for {
		destination := make([]byte, len(input)*4+16)
		written, consumed, err := d.transformer.Transform(destination, input, atEOF)
		output.Write(destination[:written])
		input = input[consumed:]
		if err == transform.ErrShortDst {
			continue
		}
		if err == transform.ErrShortSrc && !atEOF {
			d.pending = append([]byte(nil), input...)
			break
		}
		if err != nil {
			output.Write(input)
			break
		}
		if len(input) == 0 {
			break
		}
	}
	return output.Bytes()
}
