package redact

import "fmt"

// Error hides credential patterns while retaining errors.Is/errors.As behavior.
func Error(err error) error {
	if err == nil {
		return nil
	}
	return maskedError{err}
}

type maskedError struct{ cause error }

func (e maskedError) Error() string { return Text(fmt.Sprint(e.cause)) }
func (e maskedError) Unwrap() error { return e.cause }
