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

	// userLocks holds one mutex per user email. See LockUser.
	userLocks sync.Map

	// pendingUsers holds the users that are declared but could not be created
	// yet, by lower-case email. See DeferUser.
	pendingMu    sync.Mutex
	pendingUsers map[string]PendingUser
}

// PendingUser is a user whose creation waits for its first grant.
type PendingUser struct {
	// Email is the address as configured.
	Email string
	// ExpirationTime is the configured expiry of the user's access, if any.
	ExpirationTime string
}

// LockUser serializes work on one user, matched case-insensitively, and
// returns the unlock function. Two grants for a user who does not exist yet
// must not both try to create the user.
func (c *Client) LockUser(email string) func() {
	value, _ := c.userLocks.LoadOrStore(strings.ToLower(email), &sync.Mutex{})
	mutex, _ := value.(*sync.Mutex)
	mutex.Lock()

	return mutex.Unlock
}

// DeferUser records a user that is declared with no account-wide permission.
// Google refuses to create a user who holds no permission at all ("No
// permissions set for this user"), so such a user can only come into being
// together with a grant: the grant that is created first takes the pending
// user and creates both in one call. The record lives for the life of the
// provider process, which is one plan or apply.
func (c *Client) DeferUser(user PendingUser) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()

	if c.pendingUsers == nil {
		c.pendingUsers = map[string]PendingUser{}
	}
	c.pendingUsers[strings.ToLower(user.Email)] = user
}

// PendingUser returns the deferred user with the given email, if there is one.
func (c *Client) PendingUser(email string) (PendingUser, bool) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()

	user, ok := c.pendingUsers[strings.ToLower(email)]

	return user, ok
}

// ForgetPendingUser drops a deferred user, once created or no longer wanted.
func (c *Client) ForgetPendingUser(email string) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()

	delete(c.pendingUsers, strings.ToLower(email))
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

// usersPageSizeAll is the only page size users.list accepts. The generated
// client documents it as "This must be set to -1 to disable pagination", and
// the live API rejects any other value: the list cannot be paged.
const usersPageSizeAll = -1

// ListUsers returns every user of the developer account, each with its
// per-app grants, in the single response the API gives.
func (c *Client) ListUsers(ctx context.Context) ([]*androidpublisher.User, error) {
	parent, err := c.DeveloperParent()
	if err != nil {
		return nil, err
	}

	list, err := c.Service.Users.List(parent).PageSize(usersPageSizeAll).Context(ctx).Do()
	if err != nil {
		return nil, err
	}

	return list.Users, nil
}

// FindUser returns the user with the given email address, matched
// case-insensitively. The API has no call that gets one user, so it lists them
// all. It returns nil, nil when no user matches.
func (c *Client) FindUser(ctx context.Context, email string) (*androidpublisher.User, error) {
	users, err := c.ListUsers(ctx)
	if err != nil {
		return nil, err
	}

	for _, user := range users {
		if strings.EqualFold(user.Email, email) {
			return user, nil
		}
	}

	return nil, nil
}
