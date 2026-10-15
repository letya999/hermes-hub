package toolhub

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func tarGzFixture(t *testing.T, members map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	w := tar.NewWriter(gz)
	for name, body := range members {
		if err := w.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0755, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func releaseServer(t *testing.T, assetName string, assetBody, checksums []byte, apiDigest string) *httptest.Server {
	t.Helper()
	sum := sha256.Sum256(assetBody)
	var serverURL string
	var mux http.ServeMux
	mux.HandleFunc("/repos/BurntSushi/ripgrep/releases/tags/14.1.1", func(w http.ResponseWriter, _ *http.Request) {
		digest := ""
		if apiDigest != "" {
			digest = `"digest":"` + apiDigest + `",`
		}
		fmt.Fprintf(w, `{"tag_name":"14.1.1","assets":[{%s"name":%q,"browser_download_url":%q},{"name":"checksums.txt","browser_download_url":%q}]}`,
			digest, assetName, serverURL+"/dl/asset", serverURL+"/dl/checksums")
	})
	mux.HandleFunc("/dl/asset", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(assetBody) })
	mux.HandleFunc("/dl/checksums", func(w http.ResponseWriter, _ *http.Request) {
		if checksums == nil {
			fmt.Fprintf(w, "%x  %s\n", sum, assetName)
			return
		}
		_, _ = w.Write(checksums)
	})
	server := httptest.NewServer(&mux)
	t.Cleanup(server.Close)
	serverURL = server.URL
	return server
}

func languageContext(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	w := tar.NewWriter(&buffer)
	for name, content := range files {
		if err := w.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0644, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
