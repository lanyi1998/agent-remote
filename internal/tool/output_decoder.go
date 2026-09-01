package tool

type outputDecoder interface {
	Decode([]byte) []byte
	Flush() []byte
}
