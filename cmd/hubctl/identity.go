package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/internal/communication"
	"github.com/letya999/hermes-hub/internal/iam"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/identityhub"
	"github.com/letya999/hermes-hub/internal/locale"
	"io"
	"os"
	"strconv"
	"strings"
)

func runIdentity(ctx context.Context, out io.Writer, args []string) error {
	lang := locale.Operator()
	invalid := func() error { return errors.New(locale.T(lang, "hubctl.identity-invalid")) }
	if len(args) == 0 || args[0] != "migrate" {
		return errors.New(locale.T(lang, "hubctl.identity-usage"))
	}
	f := flag.NewFlagSet("identity migrate", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	source := f.String("config", "", locale.T(lang, "hubctl.identity-source"))
	control := f.String("control-dir", "", locale.T(lang, "hubctl.identity-control-dir"))
	environment := f.String("env", "", locale.T(lang, "hubctl.identity-environment"))
	name := f.String("import-id", "", locale.T(lang, "hubctl.identity-import-id"))
	links := f.String("oidc-links", "", locale.T(lang, "hubctl.identity-links"))
	envUser := f.String("legacy-env-user", "", locale.T(lang, "hubctl.identity-env-user"))
	apply := f.Bool("apply", false, locale.T(lang, "hubctl.identity-apply"))
	if f.Parse(args[1:]) != nil || f.NArg() != 0 || *source == "" || *control == "" || !identity.ValidID(*name) {
		return errors.New(locale.T(lang, "hubctl.identity-usage"))
	}
	path, err := identityhub.StorePath(*control, *environment)
	if err != nil {
		return invalid()
	}
	body, err := readIdentityMigrationSource(*source)
	if err != nil {
		return invalid()
	}
	config, err := communication.DecodeYAML(bytes.NewReader(body))
	if err != nil || config.Validate() != nil {
		return invalid()
	}
	input := identityhub.LegacyConfiguration{Name: *name, Organization: config.OrganizationID, Issuer: config.OIDCIssuer, Users: config.Users}
	for _, group := range config.Groups {
		if group.Mode != "topics" {
			input.SharedSpaces = append(input.SharedSpaces, identityhub.LegacySharedSpace{ID: group.Space, Members: group.Members})
		}
	}
	if *envUser != "" {
		if err := identityMigrationEnvironmentUser(&input, *envUser); err != nil {
			return invalid()
		}
	}
	if *links != "" {
		body, err := readIdentityMigrationSource(*links)
		if err != nil {
			return invalid()
		}
		var legacy []iam.Link
		d := json.NewDecoder(bytes.NewReader(body))
		d.DisallowUnknownFields()
		if d.Decode(&legacy) != nil {
			return invalid()
		}
		var extra any
		if d.Decode(&extra) != io.EOF {
			return invalid()
		}
		for _, link := range legacy {
			channel, err := communication.LegacyChannelIdentity(link.TransportID, config.Users)
			if err != nil {
				return invalid()
			}
			input.VerifiedLinks = append(input.VerifiedLinks, contract.OIDCLink{Channel: channel, PrincipalID: link.Principal, Issuer: link.Issuer, Subject: link.Subject})
		}
	}
	if *apply {
		r, err := identityhub.Open(path)
		if err != nil {
			return errors.New(locale.T(lang, "hubctl.identity-failed"))
		}
		defer r.Close()
		if _, err := r.ImportLegacyConfiguration(ctx, input); err != nil {
			return errors.New(locale.T(lang, "hubctl.identity-failed"))
		}
	}
	return json.NewEncoder(out).Encode(struct {
		Applied           bool `json:"applied"`
		PrivateSpaces     int  `json:"private_spaces"`
		SharedSpaces      int  `json:"shared_spaces"`
		VerifiedOIDCLinks int  `json:"verified_oidc_links"`
	}{*apply, len(input.Users) - len(input.SharedSpaces), len(input.SharedSpaces), len(input.VerifiedLinks)})
}

func readIdentityMigrationSource(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, contract.ErrInvalid
	}
	f, err := os.Open(path) // #nosec G703 -- explicit operator input, checked regular file and bounded read
	if err != nil {
		return nil, contract.ErrInvalid
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, contract.ErrInvalid
	}
	body, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		return nil, contract.ErrInvalid
	}
	return body, nil
}

func identityMigrationEnvironmentUser(input *identityhub.LegacyConfiguration, userID string) error {
	for _, group := range input.SharedSpaces {
		if group.ID == userID {
			return contract.ErrInvalid
		}
	}
	for i := range input.Users {
		u := &input.Users[i]
		if u.ID != userID {
			continue
		}
		if len(u.TelegramIDs) != 0 || len(u.SlackIDs) != 0 {
			return contract.ErrInvalid
		}
		for _, item := range strings.Split(os.Getenv("TELEGRAM_ALLOWED_USERS"), ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			id, err := strconv.ParseInt(item, 10, 64)
			if err != nil || id <= 0 || strconv.FormatInt(id, 10) != item {
				return contract.ErrInvalid
			}
			u.TelegramIDs = append(u.TelegramIDs, id)
		}
		for _, item := range strings.Split(os.Getenv("SLACK_ALLOWED_USERS"), ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			team, user, ok := strings.Cut(item, "/")
			if !ok || team == "" || user == "" || strings.ContainsAny(item, "\r\n\t ") || strings.Contains(user, "/") {
				return contract.ErrInvalid
			}
			u.SlackIDs = append(u.SlackIDs, communication.SlackLink{TeamID: team, UserID: user})
		}
		return nil
	}
	return contract.ErrInvalid
}
