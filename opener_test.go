package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSignInURLAllowsOnlySignInPages(t *testing.T) {
	for _, u := range []string{
		"https://my.jumpstartlab.com/oauth/authorize?client_id=1&redirect_uri=http%3A%2F%2Flocalhost%3A3335%2Fcallback",
		"https://identity.jumpstartlab.com/device?user_code=ABCD",
		"https://identity.jumpstartlab.com/oauth/v2/authorize?client_id=1\n",
	} {
		if got, ok := signInURL(u); !ok || got != strings.TrimSpace(u) {
			t.Errorf("should open %q unchanged, got %q %v", u, got, ok)
		}
	}
	for _, u := range []string{
		"https://claude.ai/oauth/authorize?abc", "https://claude.com/cai/oauth/authorize?code=true",
		"", "file:///etc/passwd", "-a Calculator", "javascript:alert(1)",
		"http://my.jumpstartlab.com/oauth/authorize",
		"https://my.jumpstartlab.com/", "https://my.jumpstartlab.com/share/x", "https://my.jumpstartlab.com/oauth/authorize/../x",
		"https://my.jumpstartlab.com/oauth/%61uthorize",
		"https://my.jumpstartlab.com.evil.com/oauth/authorize", "https://evilmy.jumpstartlab.com/oauth/authorize",
		"https://my.jumpstartlab.com:8443/oauth/authorize", "https://my.jumpstartlab.com@evil.com/oauth/authorize",
		"https://x@my.jumpstartlab.com/oauth/authorize", `https://evil.com\@my.jumpstartlab.com/oauth/authorize`,
		"https://my.jumpstartlab.com/oauth/authorize?a\nhttps://evil.com",
	} {
		if _, ok := signInURL(u); ok {
			t.Errorf("should refuse %q", u)
		}
	}
}

func TestOpenerServesOnlyTheHelper(t *testing.T) {
	var opened []string
	h := openerHandler(func(u string) { opened = append(opened, u) })
	const good = "https://my.jumpstartlab.com/oauth/authorize?client_id=1"
	call := func(method, path, header, body string) int {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if header != "" {
			r.Header.Set("X-Work-Open", header)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	for _, c := range []struct {
		method, path, header, body string
		want                       int
	}{
		{"POST", "/open", "", good, http.StatusNotFound},
		{"GET", "/open", "1", good, http.StatusNotFound},
		{"OPTIONS", "/open", "", "", http.StatusNotFound},
		{"POST", "/other", "1", good, http.StatusNotFound},
		{"POST", "/open", "1", "https://evil.com/", http.StatusForbidden},
		{"POST", "/open", "1", good + strings.Repeat("a", 9000), http.StatusRequestEntityTooLarge},
		{"POST", "/open", "1", good, http.StatusNoContent},
		{"POST", "/open", "1", good, http.StatusTooManyRequests},
	} {
		if got := call(c.method, c.path, c.header, c.body); got != c.want {
			t.Errorf("%s %s header=%q: %d, want %d", c.method, c.path, c.header, got, c.want)
		}
	}
	if len(opened) != 1 || opened[0] != good {
		t.Fatalf("opened %v", opened)
	}
}

func TestStartOpenerListensOnLocalhost(t *testing.T) {
	got := make(chan string, 1)
	port, stop := startOpener(func(u string) { got <- u })
	defer stop()
	r, _ := http.NewRequest("POST", "http://127.0.0.1:"+port+"/open", strings.NewReader("https://my.jumpstartlab.com/oauth/authorize?x"))
	r.Header.Set("X-Work-Open", "1")
	resp, err := http.DefaultClient.Do(r)
	if err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("%v %v", resp, err)
	}
	if u := <-got; u != "https://my.jumpstartlab.com/oauth/authorize?x" {
		t.Fatal(u)
	}
}

func TestSSHForwardsTheOpenerOnlyWhenItRuns(t *testing.T) {
	c := connect{User: "guest", Host: "guest-vm"}
	with := strings.Join(sshArgs(c, "50001"), " ")
	if !strings.Contains(with, " -R 127.0.0.1:3336:127.0.0.1:50001 guest@guest-vm ") {
		t.Fatalf("argv: %s", with)
	}
	if strings.Contains(strings.Join(sshArgs(c, ""), " "), "-R") {
		t.Fatal("forwarded with no opener")
	}
}
