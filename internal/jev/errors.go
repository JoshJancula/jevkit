package jev

import (
	"errors"
	"fmt"
)

// Code is the failure taxonomy shared with ralph's exit codes.
type Code int

const (
	// CodeDeclined means jev is unavailable and the caller should silently
	// fall back (breaker open, unknown question set).
	CodeDeclined Code = 1
	// CodeTransport covers transport, HTTP and protocol errors.
	CodeTransport Code = 2
	// CodeRejected means the input was rejected before any network use.
	CodeRejected Code = 3
)

// Error is the error type returned by the client.
type Error struct {
	Code   Code
	Reason string
	// Config marks errors that retrying cannot fix (HTTP 401/422).
	Config bool
	Err    error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("jev: %s (code %d): %v", e.Reason, e.Code, e.Err)
	}
	return fmt.Sprintf("jev: %s (code %d)", e.Reason, e.Code)
}

func (e *Error) Unwrap() error { return e.Err }

// CodeOf returns the taxonomy code of err, or 0 if it is not an *Error.
func CodeOf(err error) Code {
	var je *Error
	if errors.As(err, &je) {
		return je.Code
	}
	return 0
}
