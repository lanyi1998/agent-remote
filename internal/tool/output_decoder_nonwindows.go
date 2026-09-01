//go:build !windows

package tool

type passthroughOutputDecoder struct{}

func newOutputDecoder() outputDecoder {
	return passthroughOutputDecoder{}
}

func (passthroughOutputDecoder) Decode(data []byte) []byte {
	return data
}

func (passthroughOutputDecoder) Flush() []byte {
	return nil
}
