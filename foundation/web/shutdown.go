package web

import "errors"

type shutdownError struct {
	Message string
}

func NewShutdownError(message string) error {
	return &shutdownError{message}
}

func (se *shutdownError) Error() string {
	return se.Message
}

func IsShutdown(err error) bool {
	var se *shutdownError
	return errors.As(err, &se)
}
