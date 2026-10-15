package error

import "net/http"

// Fork (elphant): ConflictError is a 409, for example a voice call answered twice (stage 2b).
type ConflictError string

func (e ConflictError) Error() string { return string(e) }

// ErrCode will return the error code based on the error data type
func (e ConflictError) ErrCode() string { return "CONFLICT" }

// StatusCode will return the HTTP status code based on the error data type
func (e ConflictError) StatusCode() int { return http.StatusConflict }
