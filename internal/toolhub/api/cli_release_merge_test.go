package api

import (
	"net/http"
	"testing"
)

func TestCLIDownloadRedirectGuards(t *testing.T) {
	if GithubAPIBase() != "https://api.github.com" {
		t.Fatal("api base")
	}
	mkreq := func(raw string) *http.Request {
		req, _ := http.NewRequest(http.MethodGet, raw, nil)
		return req
	}
	// release downloads only follow https hops to GitHub-owned hosts
	release := ReleaseHTTPClient()
	if err := release.CheckRedirect(mkreq("https://github.com/x/releases/a"), nil); err != nil {
		t.Fatalf("github hop: %v", err)
	}
	if err := release.CheckRedirect(mkreq("https://objects.githubusercontent.com/x"), nil); err != nil {
		t.Fatalf("githubusercontent hop: %v", err)
	}
	if err := release.CheckRedirect(mkreq("http://github.com/x"), nil); err == nil {
		t.Fatal("http downgrade followed")
	}
	if err := release.CheckRedirect(mkreq("https://evil.example.com/x"), nil); err == nil {
		t.Fatal("foreign host followed")
	}
	if err := release.CheckRedirect(mkreq("https://github.com/x"), make([]*http.Request, 5)); err == nil {
		t.Fatal("redirect chain not bounded")
	}
	// package metadata only follows the pinned registries
	pkg := PackageHTTPClient()
	if err := pkg.CheckRedirect(mkreq("https://pypi.org/pypi/x"), nil); err != nil {
		t.Fatalf("pypi hop: %v", err)
	}
	if err := pkg.CheckRedirect(mkreq("https://files.pythonhosted.org/x"), nil); err != nil {
		t.Fatalf("pythonhosted hop: %v", err)
	}
	if err := pkg.CheckRedirect(mkreq("https://example.com/x"), nil); err == nil {
		t.Fatal("foreign package host followed")
	}
}
