package cmd

import (
	"os"
	"path/filepath"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/callrec"
	"github.com/gofiber/fiber/v3"
)

// Fork (elphant): GET <base>/call/:call_id/recording serves a call's stereo recording (voice
// calls stage 2b, B6). A valid signature (exp, sig) from the call.ended webhook opens it without
// credentials; otherwise it needs Basic Auth when Basic Auth is configured. It is mounted before
// the request timeout, like /statics, so a long download is not cut.
func mountCallRecordings(app *fiber.App, basePath, dir string, accounts map[string]string, secret []byte) {
	var basicAuth fiber.Handler
	if len(accounts) > 0 {
		basicAuth = newBasicAuthMiddleware(accounts)
	}
	gate := func(c fiber.Ctx) error {
		id := c.Params("call_id")
		if !callrec.ValidCallID(id) {
			return c.Status(fiber.StatusBadRequest).SendString("invalid call id")
		}
		if basicAuth == nil || callrec.Verify(secret, id, c.Query("exp"), c.Query("sig"), time.Now()) {
			return c.Next()
		}
		return basicAuth(c)
	}
	app.Get(basePath+"/call/:call_id/recording", gate, func(c fiber.Ctx) error {
		f, err := os.Open(filepath.Join(dir, c.Params("call_id")+".ogg"))
		if err != nil {
			return c.Status(fiber.StatusGone).SendString("recording expired or not found")
		}
		fi, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return c.Status(fiber.StatusGone).SendString("recording expired or not found")
		}
		c.Set(fiber.HeaderContentType, "audio/ogg")
		c.Set(fiber.HeaderCacheControl, "private, no-store")
		c.Set(fiber.HeaderContentDisposition, `attachment; filename="`+c.Params("call_id")+`.ogg"`)
		return c.SendStream(f, int(fi.Size()))
	})
}
