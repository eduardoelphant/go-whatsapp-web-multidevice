package usecase

import (
	"errors"
	"testing"

	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/stretchr/testify/assert"
)

func TestMapCallErrorMasksAndValidates(t *testing.T) {
	err := mapCallError(errors.New("usync 5511987654321@s.whatsapp.net failed"), "could not place the call: ")
	var ge pkgError.GenericError
	assert.True(t, errors.As(err, &ge))
	assert.Equal(t, 400, ge.StatusCode())
	assert.NotRegexp(t, `[0-9]{8,}`, err.Error())
	assert.Contains(t, err.Error(), "could not place the call: ")

	known := pkgError.ValidationError("x")
	assert.Equal(t, known, mapCallError(known, "p: "))
	assert.NoError(t, mapCallError(nil, "p: "))
}
