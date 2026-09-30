package rest

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/staticurl"
	"github.com/gofiber/fiber/v3"
)

// signedStaticQuery returns the signature query for a file below /statics, or "" when signed
// URLs are off: they need APP_STATICS_AUTH, a secret and Basic Auth (fork: elphant, D-13).
func signedStaticQuery(rel string) string {
	if !config.AppStaticsAuth || config.AppStaticsSecret == "" || len(config.AppBasicAuthCredential) == 0 {
		return ""
	}
	ttl := time.Duration(max(1, config.AppStaticsURLTTLMinutes)) * time.Minute
	return staticurl.Sign([]byte(config.AppStaticsSecret), rel, time.Now().Add(ttl))
}

// withStaticSignature appends the signature for rel (the decoded path below /statics) to link.
func withStaticSignature(link, rel string) string {
	if query := signedStaticQuery(rel); query != "" {
		return link + "?" + query
	}
	return link
}

// staticLink is the public link to a file the API wrote under statics/ (the login QR image).
func staticLink(c fiber.Ctx, imagePath string) string {
	link := fmt.Sprintf("%s://%s%s/%s", c.Scheme(), c.Host(), config.AppBasePath, imagePath)
	return withStaticSignature(link, strings.TrimPrefix(imagePath, "statics/"))
}

// staticRel is the path below /statics of a public static path ("/statics/a%20b.jpeg" gives "a b.jpeg").
func staticRel(staticPath string) string {
	rel, err := url.PathUnescape(strings.TrimPrefix(staticPath, "/statics/"))
	if err != nil {
		return strings.TrimPrefix(staticPath, "/statics/")
	}
	return rel
}
