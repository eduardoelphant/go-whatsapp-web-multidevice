package usecase

import (
	"context"
	"testing"

	domainCall "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/call"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCallRequestsNeedTheirSDP(t *testing.T) {
	svc := serviceCall{}
	ctx := context.Background()
	cases := map[string]error{}
	_, cases["answer without call_id"] = svc.AnswerCall(ctx, domainCall.AnswerCallRequest{SDP: "v=0"})
	_, cases["answer without sdp"] = svc.AnswerCall(ctx, domainCall.AnswerCallRequest{CallID: "IN-1"})
	_, cases["start without phone"] = svc.StartCall(ctx, domainCall.StartCallRequest{SDP: "v=0"})
	_, cases["start without sdp"] = svc.StartCall(ctx, domainCall.StartCallRequest{Phone: "+5511900000000"})
	_, cases["get without call_id"] = svc.GetCall(ctx, "")
	for name, err := range cases {
		var ge pkgError.GenericError
		require.ErrorAs(t, err, &ge, name)
		assert.Equal(t, 400, ge.StatusCode(), name)
	}
}

func TestRejectTrackedCallFallsThroughWithoutCalls(t *testing.T) {
	handled, err := rejectTrackedCall(context.Background(), "IN-1")
	assert.False(t, handled)
	assert.NoError(t, err)
}
