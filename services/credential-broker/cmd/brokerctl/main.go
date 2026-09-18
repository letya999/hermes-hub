// brokerctl is a development/control adapter. It cannot invoke the secret-bearing
// runtime materialization endpoint, and never accepts secret values as arguments.
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/letya999/credential-broker/client"
	"github.com/letya999/credential-broker/identity"
	"github.com/letya999/credential-broker/internal/securefs"
	"github.com/letya999/credential-broker/internal/strictjson"
)

func run(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("brokerctl", flag.ContinueOnError)
	fs.SetOutput(out)
	base := fs.String("url", "http://127.0.0.1:8787", "broker origin")
	keyFile := fs.String("key-file", "", "trusted Hub signing-key file")
	keyID := fs.String("key-id", "toolhub", "registered signing key ID")
	issuer := fs.String("issuer", "hermes-toolhub", "registered issuer")
	audience := fs.String("audience", "broker:control", "broker:control or broker:approve")
	actorFile := fs.String("actor-file", "", "canonical identity JSON file")
	method := fs.String("method", "GET", "HTTP method")
	path := fs.String("path", "/v1/contracts", "control API path")
	bodyFile := fs.String("body-file", "", "non-secret request JSON file")
	if e := fs.Parse(args); e != nil {
		return e
	}
	if *keyFile == "" || *actorFile == "" || fs.NArg() != 0 || *audience != "broker:control" && *audience != "broker:approve" || strings.HasPrefix(*path, "/v1/runtime/") {
		return errors.New("invalid control arguments")
	}
	info, e := os.Lstat(*keyFile)
	if e != nil || info.Mode().Perm()&0077 != 0 {
		return errors.New("signing key permissions must be 0600")
	}
	private, e := securefs.Read(filepath.Dir(*keyFile), filepath.Base(*keyFile), 65)
	if e != nil || len(private) != ed25519.PrivateKeySize {
		return errors.New("invalid signing key")
	}
	defer clear(private)
	raw, e := securefs.Read(filepath.Dir(*actorFile), filepath.Base(*actorFile), 8192)
	if e != nil {
		return errors.New("cannot read actor")
	}
	var actor identity.Actor
	if strictjson.Decode(raw, &actor) != nil {
		return errors.New("invalid actor")
	}
	var in any
	if *bodyFile != "" {
		raw, e := securefs.Read(filepath.Dir(*bodyFile), filepath.Base(*bodyFile), 65536)
		if e != nil {
			return errors.New("cannot read request")
		}
		if !strictjson.Valid(raw) || json.Unmarshal(raw, &in) != nil {
			return errors.New("invalid request")
		}
	}
	c, e := client.New(*base, client.Signer{KeyID: *keyID, Issuer: *issuer, Audience: *audience, PrivateKey: private}, actor, nil)
	if e != nil {
		return e
	}
	var result any
	if e = c.Do(ctx, *method, *path, in, &result); e != nil {
		return e
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}
func main() {
	if e := run(context.Background(), os.Args[1:], os.Stdout); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
