package tool

import (
	"encoding/json"
	"fmt"
)

type Error struct {
	Code    string
	Message string
	Details interface{}
}

func (e *Error) Error() string {
	return e.Message
}

func NewError(code, message string) *Error {
	return &Error{Code: code, Message: message}
}

func WrapError(code, message string, err error) *Error {
	return &Error{Code: code, Message: fmt.Sprintf("%s: %v", message, err)}
}

func ErrorDetails(value interface{}) json.RawMessage {
	if value == nil {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return data
}
