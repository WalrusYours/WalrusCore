// Package apperr is the one error type the services return when a request cannot be served: an
// HTTP status, a machine-readable code and a message for people. The API turns it into its error
// body; anything else becomes a 500.
package apperr

import "fmt"

type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func New(status int, code, format string, args ...any) *Error {
	return &Error{Status: status, Code: code, Message: fmt.Sprintf(format, args...)}
}
