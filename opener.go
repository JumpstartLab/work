package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A box has no browser for a person to sign in with, and a sign-in link copied
// out of its terminal is easily cut short (it wraps over several lines, and
// tmux hides that from the terminal). So `work` listens here, every ssh
// connection forwards the box's openPort back to it, and the box's BROWSER
// helper (server-admin dev/jmoses/bin/open-on-client) posts the link.
//
// Anything running on the box can post, so this opens sign-in pages only:
// https, an exact host, and that host's sign-in paths. Claude's own sign-in is
// left out on purpose: the link it hands a browser returns to a random localhost
// port that isn't forwarded; the short link it prints asks for a code instead.
const openPort = "3336"

var signInPaths = map[string][]string{
	"my.jumpstartlab.com":       {"/oauth/authorize"},
	"identity.jumpstartlab.com": {"/oauth/v2/authorize", "/device"},
}

// signInURL returns the normalized URL if it is a sign-in page we may open.
func signInURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if strings.ContainsAny(raw, " \t\r\n\\") {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" {
		return "", false
	}
	for _, p := range signInPaths[u.Host] { // exact: a port is part of Host
		if u.EscapedPath() == p {
			return u.String(), true
		}
	}
	return "", false
}

// openerHandler opens posted sign-in links, at most one every few seconds. The
// custom header keeps web pages in the local browser out: they cannot send it
// cross-origin without a preflight, which is refused.
func openerHandler(open func(string)) http.Handler {
	var mu sync.Mutex
	var last time.Time
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/open" || r.Header.Get("X-Work-Open") != "1" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
		if err != nil {
			http.Error(w, "too large", http.StatusRequestEntityTooLarge)
			return
		}
		u, ok := signInURL(string(body))
		if !ok {
			http.Error(w, "not a sign-in page", http.StatusForbidden)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if !last.IsZero() && time.Since(last) < 3*time.Second {
			http.Error(w, "too soon", http.StatusTooManyRequests)
			return
		}
		last = time.Now()
		open(u)
		w.WriteHeader(http.StatusNoContent)
	})
}

// startOpener serves the opener on a free localhost port and returns the port,
// or "" when it can't: the box then shows the link as usual.
func startOpener(open func(string)) (string, func()) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, "work: sign-in links from the box won't open by themselves:", err)
		return "", func() {}
	}
	srv := &http.Server{Handler: openerHandler(open), ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(l) }()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port), func() { _ = srv.Close() }
}
