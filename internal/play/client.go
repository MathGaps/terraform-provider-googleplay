// Copyright (c) MathGaps
// SPDX-License-Identifier: MPL-2.0

// Package play wraps the generated Google Play Developer API client with the
// things every resource needs: credentials, retries, serialized edits and
// readable errors. It holds no Terraform types.
package play

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"google.golang.org/api/androidpublisher/v3"
	"google.golang.org/api/option"
	htransport "google.golang.org/api/transport/http"
)

// Scope is the only OAuth scope the Google Play Developer API uses.
const Scope = androidpublisher.AndroidpublisherScope

// Config is what NewClient needs to reach the API.
type Config struct {
	// CredentialsJSON is the text of a Google credentials file, normally a
	// service account key. Empty means application default credentials.
	CredentialsJSON string

	// Endpoint replaces the API base URL. It exists for tests that point the
	// client at a fake: when it is set no credentials are loaded and requests
	// are sent unauthenticated.
	Endpoint string

	// DeveloperID is the Play Console developer account id, used as the parent
	// of users and grants.
	DeveloperID string

	// UserAgent is sent with every request.
	UserAgent string
}

// Client is a configured Google Play Developer API client.
type Client struct {
	// Service is the generated API client.
	Service *androidpublisher.Service

	developerID string

	// editLocks holds one mutex per package name. Committing an edit
	// invalidates every other open edit of the same app, so edits of one app
	// must not overlap.
	editLocks sync.Map
}

// NewClient builds a client from cfg. It does not call the API.
func NewClient(ctx context.Context, cfg Config) (*Client, error) {
	opts := []option.ClientOption{option.WithScopes(Scope)}
	if cfg.UserAgent != "" {
		opts = append(opts, option.WithUserAgent(cfg.UserAgent))
	}

	switch {
	case cfg.Endpoint != "":
		opts = append(opts, option.WithoutAuthentication())
	case cfg.CredentialsJSON != "":
		credType, err := credentialsType(cfg.CredentialsJSON)
		if err != nil {
			return nil, err
		}
		opts = append(opts, option.WithAuthCredentialsJSON(credType, []byte(cfg.CredentialsJSON)))
	}

	httpClient, _, err := htransport.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("loading Google credentials: %w", err)
	}
	httpClient.Transport = newRetryTransport(httpClient.Transport)

	serviceOpts := []option.ClientOption{option.WithHTTPClient(httpClient)}
	if cfg.Endpoint != "" {
		serviceOpts = append(serviceOpts, option.WithEndpoint(strings.TrimRight(cfg.Endpoint, "/")+"/"))
	}

	service, err := androidpublisher.NewService(ctx, serviceOpts...)
	if err != nil {
		return nil, fmt.Errorf("creating the Google Play Developer API client: %w", err)
	}

	return &Client{Service: service, developerID: cfg.DeveloperID}, nil
}

// credentialsType reads the "type" member of a credentials file. The error
// never includes the file's content.
func credentialsType(credentialsJSON string) (option.CredentialsType, error) {
	var header struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(credentialsJSON), &header); err != nil {
		return "", errors.New("the credentials are not valid JSON: expected the text of a service account key file")
	}

	switch header.Type {
	case string(option.ServiceAccount):
		return option.ServiceAccount, nil
	case string(option.AuthorizedUser):
		return option.AuthorizedUser, nil
	case string(option.ImpersonatedServiceAccount):
		return option.ImpersonatedServiceAccount, nil
	case string(option.ExternalAccount):
		return option.ExternalAccount, nil
	case "":
		return "", errors.New(`the credentials JSON has no "type" member: expected the text of a service account key file`)
	default:
		return "", fmt.Errorf("unsupported credentials type %q", header.Type)
	}
}

// ErrNoDeveloperID is returned when a users or grants call is made without a
// developer account id.
var ErrNoDeveloperID = errors.New("the Play Console developer account id is not set")

// DeveloperParent returns the "developers/{id}" resource name.
func (c *Client) DeveloperParent() (string, error) {
	if c.developerID == "" {
		return "", ErrNoDeveloperID
	}

	return "developers/" + c.developerID, nil
}

// UserName returns the "developers/{id}/users/{email}" resource name.
func (c *Client) UserName(email string) (string, error) {
	parent, err := c.DeveloperParent()
	if err != nil {
		return "", err
	}

	return parent + "/users/" + email, nil
}

// GrantName returns the
// "developers/{id}/users/{email}/grants/{packageName}" resource name.
func (c *Client) GrantName(email, packageName string) (string, error) {
	user, err := c.UserName(email)
	if err != nil {
		return "", err
	}

	return user + "/grants/" + packageName, nil
}

// FindUser lists the developer account's users, following every page, and
// returns the one with the given email address. The API has no call that gets
// one user. It returns nil, nil when no user matches.
func (c *Client) FindUser(ctx context.Context, email string) (*androidpublisher.User, error) {
	parent, err := c.DeveloperParent()
	if err != nil {
		return nil, err
	}

	var found *androidpublisher.User
	err = c.Service.Users.List(parent).Pages(ctx, func(page *androidpublisher.ListUsersResponse) error {
		for _, user := range page.Users {
			if found == nil && strings.EqualFold(user.Email, email) {
				found = user
			}
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return found, nil
}
