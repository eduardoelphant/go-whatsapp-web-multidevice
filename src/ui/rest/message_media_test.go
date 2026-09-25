package rest

import "testing"

func TestAttachmentDisposition(t *testing.T) {
	cases := map[string]string{
		"a.pdf":                `attachment; filename=a.pdf`,
		"contrato final.pdf":   `attachment; filename="contrato final.pdf"`,
		"relatório.pdf":        `attachment; filename*=utf-8''relat%C3%B3rio.pdf`,
		"evil\r\nX-Bad: 1.pdf": `attachment; filename*=utf-8''evil%0D%0AX-Bad%3A%201.pdf`, // CR/LF never reach the header
		"":                     `attachment`,
	}
	for name, want := range cases {
		if got := attachmentDisposition(name); got != want {
			t.Errorf("attachmentDisposition(%q) = %q, want %q", name, got, want)
		}
	}
}
