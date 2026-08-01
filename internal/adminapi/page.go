package adminapi

import (
	_ "embed"
	"net/http"

	"github.com/lev-goryachev/lctk/internal/adminsession"
)

// page is the whole admin interface: one file, no build step, no dependencies.
//
// That is a decision rather than a shortcut. A build step would put a Node
// toolchain in front of anyone building a Go program, and a script or stylesheet
// from a CDN would be a third party with the ability to administer LCTK on the
// user's machine. The page is embedded in the binary, so what shipped is what
// runs.
//
//go:embed page.html
var page []byte

func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	if !adminsession.LoopbackHost(r.Host) {
		refuseHTML(w)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Nothing is loaded from anywhere else, so say so. If an injection ever did
	// get into this page, the policy is what stops it from reaching the network.
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; form-action 'none'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	_, _ = w.Write(page)
}

func refuseHTML(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte("This request did not name a loopback host.\n"))
}
