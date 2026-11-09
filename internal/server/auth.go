package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// sessionCookie is the admin session cookie name.
const sessionCookie = "ftrss_session"

// sessionTTL is how long a login stays valid.
const sessionTTL = 7 * 24 * time.Hour

// authKey derives the HMAC key from the configured secrets. Password and
// token are concatenated in a fixed order; changing either invalidates all
// sessions (a security feature, not a bug).
func (s *Server) authKey() []byte {
	sum := sha256.Sum256([]byte("ftrss-session:" + s.cfg.AdminPassword + ":" + s.cfg.AdminToken))
	return sum[:]
}

// signSession returns "<expiry-unix>.<hmac(expiry)>" under the derived key.
func (s *Server) signSession(expiry time.Time) string {
	key := s.authKey()
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(expiry.Unix()))
	mac := hmac.New(sha256.New, key)
	mac.Write(buf[:])
	return strconv.FormatInt(expiry.Unix(), 10) + "." + hexEncode(mac.Sum(nil))
}

// sessionValid checks the cookie value's MAC and expiry window.
func (s *Server) sessionValid(val string, now time.Time) bool {
	dot := strings.IndexByte(val, '.')
	if dot <= 0 || dot == len(val)-1 {
		return false
	}
	exp, err := strconv.ParseInt(val[:dot], 10, 64)
	if err != nil || exp < 1 {
		return false
	}
	key := s.authKey()
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(exp))
	mac := hmac.New(sha256.New, key)
	mac.Write(buf[:])
	want := hexEncode(mac.Sum(nil))
	got := val[dot+1:]
	if len(got) != len(want) || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return false
	}
	// Expired sessions fail even with a valid MAC. The grace is zero: the
	// cookie also carries Max-Age, this re-check covers stolen values.
	return now.Unix() <= exp
}

// passwordOK reports whether the submitted password matches the configured
// admin password or admin token, in constant time.
func (s *Server) passwordOK(pw string) bool {
	okPw := subtle.ConstantTimeCompare([]byte(pw), []byte(s.cfg.AdminPassword)) == 1
	okTok := false
	if s.cfg.AdminToken != "" {
		okTok = subtle.ConstantTimeCompare([]byte(pw), []byte(s.cfg.AdminToken)) == 1
	}
	return okPw || okTok
}

// bearerOK reports whether the Authorization header carries the admin token.
func (s *Server) bearerOK(r *http.Request) bool {
	if s.cfg.AdminToken == "" {
		return false
	}
	h := r.Header.Get("Authorization")
	if len(h) <= 7 || !strings.EqualFold(h[:7], "Bearer ") {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(strings.TrimSpace(h[7:])), []byte(s.cfg.AdminToken)) == 1
}

// hasSession reports whether the request carries a valid session cookie.
func (s *Server) hasSession(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return false
	}
	return s.sessionValid(c.Value, time.Now())
}

// setSessionCookie issues a signed session cookie.
func (s *Server) setSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    s.signSession(time.Now().Add(sessionTTL)),
		Path:     "/",
		MaxAge:   int(sessionTTL / time.Second),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// clearSessionCookie expires the session cookie.
func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// auth guards UI pages: unauthenticated browsers are redirected to /login.
// A valid bearer token also authenticates (handy for scripted checks).
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.hasSession(r) && !s.bearerOK(r) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

// authAPI guards JSON endpoints: 401 JSON on missing auth (no redirect, so
// API clients get machine-readable failures).
func (s *Server) authAPI(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.hasSession(r) && !s.bearerOK(r) {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(w, r)
	}
}

// hexEncode encodes b as lowercase hex.
func hexEncode(b []byte) string {
	const hexDigits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hexDigits[v>>4]
		out[i*2+1] = hexDigits[v&0x0f]
	}
	return string(out)
}

// handleLoginPage renders the login form (no base layout).
func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if s.hasSession(r) {
		http.Redirect(w, r, "/admin/feeds", http.StatusSeeOther)
		return
	}
	s.renderLogin(w, "")
}

// renderLogin writes login.html with the given error line.
func (s *Server) renderLogin(w http.ResponseWriter, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "login", struct{ Error string }{Error: errMsg}); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

// handleLoginSubmit checks the password and issues the session cookie.
func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderLogin(w, "Invalid form submission.")
		return
	}
	pw := r.PostFormValue("password")
	if pw == "" || !s.passwordOK(pw) {
		// Same message for empty and wrong: no oracle about which failed.
		s.renderLogin(w, "Wrong password.")
		return
	}
	s.setSessionCookie(w)
	http.Redirect(w, r, "/admin/feeds", http.StatusSeeOther)
}

// handleLogout clears the session and returns to the login page.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.clearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
