package error

import "net/http"

type ValidationError string

func (err ValidationError) Error() string {
	return string(err)
}

// ErrCode will return the error code based on the error data type
func (err ValidationError) ErrCode() string {
	return "VALIDATION_ERROR"
}

// StatusCode will return the HTTP status code based on the error data type
func (err ValidationError) StatusCode() int {
	return http.StatusBadRequest
}

// UnprocessableError (fork: elphant) is a well-formed request that names something the
// target does not have, such as a poll option that does not exist. Answers 422.
type UnprocessableError string

func (err UnprocessableError) Error() string {
	return string(err)
}

func (err UnprocessableError) ErrCode() string {
	return "UNPROCESSABLE_ENTITY"
}

func (err UnprocessableError) StatusCode() int {
	return http.StatusUnprocessableEntity
}
