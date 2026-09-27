package ui

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base32"
	"net"
	"net/http"
	"strings"
)

// cookieName is where the browser keeps the token after the first page load.
const cookieName = "corium_ui"

// newToken mints the secret that authorises one dashboard session.
//
// 160 bits, because this is the only thing between a page in another tab and
// an operator certificate that can drain a node. It lives for as long as the
// process and is never written to disk.
func newToken() (string, error) {
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}

	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)), nil
}

// guard is what makes it safe to hold an operator certificate behind an
// unauthenticated-looking HTTP port.
//
// A server on 127.0.0.1 is not private. Every page the browser loads can reach
// it, and two attacks follow from that: a script on any origin can POST to
// http://127.0.0.1:7500 and have the browser attach whatever it has, and a
// hostile DNS name that resolves to 127.0.0.1 can make those requests
// same-origin by the browser's reckoning. Four checks, in order of how much
// they are relied on:
//
//   - The token. A shared secret the browser proves it has, printed once on the
//     terminal that started the server and never guessable from a page.
//   - The Host header must name loopback and this server's port. A rebinding
//     attack arrives with the attacker's hostname in Host, and is refused
//     before anything reads a cookie.
//   - Origin, when the browser sends one, must be this server. That is a
//     cross-site POST refused on the browser's own evidence.
//   - Sec-Fetch-Site, when present, must be same-origin or none -- a request
//     the browser itself labels as coming from elsewhere.
//
// None of the four is sufficient alone, which is why all four are here.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Nothing served here may be cached or embedded anywhere.
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")

		if !s.hostIsOurs(r.Host) {
			http.Error(w, "this dashboard answers on loopback only", http.StatusForbidden)

			return
		}

		if !originIsOurs(r) {
			http.Error(w, "cross-site request refused", http.StatusForbidden)

			return
		}

		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
			http.Error(w, "cross-site request refused", http.StatusForbidden)

			return
		}

		// The token may arrive in the URL exactly once, on the link printed by
		// `cctl ui`. It is moved into a cookie and the browser is sent to the
		// bare path, so it stops being in the address bar, the history and
		// every Referer the page might later produce.
		if token := r.URL.Query().Get("token"); token != "" && r.Method == http.MethodGet {
			if !s.tokenMatches(token) {
				http.Error(w, "wrong token", http.StatusForbidden)

				return
			}

			// No Secure attribute, deliberately: this server speaks plain
			// HTTP on loopback -- a certificate for 127.0.0.1 is a
			// certificate every operator would have to be talked through
			// trusting -- and Secure on an http:// origin means the browser
			// never sends the cookie back. HttpOnly and SameSite=Strict are
			// the two that do work here, and the Host and Origin checks above
			// cover what Secure would have.
			http.SetCookie(w, &http.Cookie{ //nolint:gosec // plain HTTP on loopback; see above
				Name:     cookieName,
				Value:    s.token,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteStrictMode,
			})

			http.Redirect(w, r, "/", http.StatusSeeOther)

			return
		}

		cookie, err := r.Cookie(cookieName)
		if err != nil || !s.tokenMatches(cookie.Value) {
			http.Error(w, "open the link `cctl ui` printed; this page needs its token",
				http.StatusForbidden)

			return
		}

		next.ServeHTTP(w, r)
	})
}

func (s *Server) tokenMatches(candidate string) bool {
	return subtle.ConstantTimeCompare([]byte(candidate), []byte(s.token)) == 1
}

// hostIsOurs rejects a Host header that is not this server on loopback, which
// is what a DNS rebinding attack cannot forge: the browser sends the name the
// page used, and an attacker's name is not one of these.
func (s *Server) hostIsOurs(host string) bool {
	name, port, err := net.SplitHostPort(host)
	if err != nil {
		// No port at all means port 80, which this server never binds.
		return false
	}

	if _, ours, err := net.SplitHostPort(s.listen); err == nil && port != ours {
		return false
	}

	if name == "localhost" {
		return true
	}

	address := net.ParseIP(strings.Trim(name, "[]"))

	return address != nil && address.IsLoopback()
}

// originIsOurs checks the browser's own account of where a request came from.
// An absent Origin is not a failure: plain navigations do not carry one.
func originIsOurs(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" || origin == "null" {
		return origin == ""
	}

	return origin == "http://"+r.Host
}
