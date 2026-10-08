// Copyright (c) MathGaps
// SPDX-License-Identifier: MPL-2.0

package play

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/api/androidpublisher/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	"github.com/MathGaps/terraform-provider-googleplay/internal/fakeplay"
)

const testPackage = "com.example.app"

// newTestClient points a real client at the fake API.
func newTestClient(t *testing.T, endpoint string) *Client {
	t.Helper()

	client, err := NewClient(context.Background(), Config{Endpoint: endpoint, DeveloperID: "42", UserAgent: "play-test"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if !strings.HasSuffix(client.Service.BasePath, "/") {
		t.Fatalf("base path %q has no trailing slash", client.Service.BasePath)
	}

	return client
}

func TestCredentialsType(t *testing.T) {
	if _, err := credentialsType(`{"type":"service_account","private_key":"hunter2"}`); err != nil {
		t.Errorf("a service account key was rejected: %v", err)
	}

	// Whatever is wrong with the credentials, the error must not quote them.
	for _, bad := range []string{`hunter2-not-json`, `{"private_key":"hunter2"}`, `{"type":"hunter2-kind","private_key":"hunter2"}`} {
		_, err := credentialsType(bad)
		if err == nil {
			t.Errorf("credentialsType(%q) succeeded", bad)

			continue
		}
		if strings.Contains(err.Error(), "private_key") || strings.Contains(err.Error(), "not-json") {
			t.Errorf("the error quotes the credentials: %v", err)
		}
	}
}

func TestNewClientRejectsMalformedCredentialsWithoutLeaking(t *testing.T) {
	_, err := NewClient(context.Background(), Config{CredentialsJSON: `{"type":"service_account","private_key":"hunter2"`})
	if err == nil {
		t.Fatal("malformed credentials were accepted")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("the error leaks the credentials: %v", err)
	}
}

func TestResourceNames(t *testing.T) {
	client := newTestClient(t, "http://127.0.0.1:1")

	if got, _ := client.DeveloperParent(); got != "developers/42" {
		t.Errorf("DeveloperParent = %q", got)
	}
	if got, _ := client.UserName("ada@example.com"); got != "developers/42/users/ada@example.com" {
		t.Errorf("UserName = %q", got)
	}
	if got, _ := client.GrantName("ada@example.com", testPackage); got != "developers/42/users/ada@example.com/grants/com.example.app" {
		t.Errorf("GrantName = %q", got)
	}

	noDeveloper, err := NewClient(context.Background(), Config{Endpoint: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := noDeveloper.GrantName("ada@example.com", testPackage); !errors.Is(err, ErrNoDeveloperID) {
		t.Errorf("GrantName without a developer id returned %v", err)
	}
	if _, err := noDeveloper.FindUser(context.Background(), "ada@example.com"); !errors.Is(err, ErrNoDeveloperID) {
		t.Errorf("FindUser without a developer id returned %v", err)
	}
}

// The API has no get for a user, and its list refuses to page: FindUser must
// ask for everything at once with pageSize=-1.
func TestFindUserListsEveryUserInOneRequest(t *testing.T) {
	fake := fakeplay.New(testPackage)
	defer fake.Close()

	for i := range 11 {
		email := fmt.Sprintf("user%d@example.com", i)
		fake.PutUser(&androidpublisher.User{Email: email, Name: "developers/42/users/" + email})
	}

	client := newTestClient(t, fake.URL)

	user, err := client.FindUser(context.Background(), "USER10@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if user == nil || user.Email != "user10@example.com" {
		t.Fatalf("FindUser = %+v, want user10, matched case-insensitively", user)
	}

	requests := fake.Requests()
	if len(requests) != 1 || !strings.Contains(requests[0], "pageSize=-1") || strings.Contains(requests[0], "pageToken") {
		t.Errorf("the list must be one request with pageSize=-1, got %v", requests)
	}

	missing, err := client.FindUser(context.Background(), "nobody@example.com")
	if err != nil || missing != nil {
		t.Errorf("FindUser for a missing user = %+v, %v; want nil, nil", missing, err)
	}
}

// The fake refuses what the real API refuses, so a regression to paging fails
// here rather than in production.
func TestFakeRejectsPagedUserList(t *testing.T) {
	fake := fakeplay.New(testPackage)
	defer fake.Close()
	client := newTestClient(t, fake.URL)

	if _, err := client.Service.Users.List("developers/42").Do(); StatusCode(err) != http.StatusBadRequest {
		t.Errorf("a list without pageSize=-1 returned %v, want 400", err)
	}
	if _, err := client.Service.Users.List("developers/42").PageSize(50).Do(); StatusCode(err) != http.StatusBadRequest {
		t.Errorf("a list with pageSize=50 returned %v, want 400", err)
	}
}

func TestCommitEditCommits(t *testing.T) {
	fake := fakeplay.New(testPackage)
	defer fake.Close()
	client := newTestClient(t, fake.URL)
	ctx := context.Background()

	err := client.CommitEdit(ctx, testPackage, func(editID string) error {
		_, err := client.Service.Edits.Tracks.Create(testPackage, editID, &androidpublisher.TrackConfig{
			Track: "qa", FormFactor: "DEFAULT", Type: "CLOSED_TESTING",
		}).Context(ctx).Do()

		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Contains(fake.Tracks(testPackage), "qa") {
		t.Errorf("the track was not committed: %v", fake.Tracks(testPackage))
	}
	if open := fake.OpenEdits(testPackage); open != 0 {
		t.Errorf("%d edits left open", open)
	}
}

func TestReadEditDeletesTheEdit(t *testing.T) {
	fake := fakeplay.New(testPackage)
	defer fake.Close()
	client := newTestClient(t, fake.URL)
	ctx := context.Background()

	var tracks int
	err := client.ReadEdit(ctx, testPackage, func(editID string) error {
		list, err := client.Service.Edits.Tracks.List(testPackage, editID).Context(ctx).Do()
		if err == nil {
			tracks = len(list.Tracks)
		}

		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	if tracks != 4 {
		t.Errorf("listed %d tracks, want the 4 built-in ones", tracks)
	}
	if open := fake.OpenEdits(testPackage); open != 0 {
		t.Errorf("%d edits left open after a read", open)
	}

	var sawDelete, sawCommit bool
	for _, request := range fake.Requests() {
		sawDelete = sawDelete || strings.HasPrefix(request, "DELETE ")
		sawCommit = sawCommit || strings.Contains(request, ":commit")
	}
	if !sawDelete || sawCommit {
		t.Errorf("a read must delete its edit and never commit it: %v", fake.Requests())
	}
}

// A failed change must not be committed, and its edit must not be left open.
func TestCommitEditDiscardsOnFailure(t *testing.T) {
	fake := fakeplay.New(testPackage)
	defer fake.Close()
	client := newTestClient(t, fake.URL)
	ctx := context.Background()

	err := client.CommitEdit(ctx, testPackage, func(editID string) error {
		_, err := client.Service.Edits.Testers.Update(testPackage, editID, "no-such-track",
			&androidpublisher.Testers{GoogleGroups: []string{"qa@example.com"}}).Context(ctx).Do()

		return err
	})

	if !IsNotFound(err) {
		t.Fatalf("err = %v, want the API's 404", err)
	}
	if open := fake.OpenEdits(testPackage); open != 0 {
		t.Errorf("%d edits left open after a failure", open)
	}
	for _, request := range fake.Requests() {
		if strings.Contains(request, ":commit") {
			t.Errorf("a failed edit was committed: %s", request)
		}
	}

	if !strings.Contains(ErrorDetail(err), "Track not found: no-such-track") {
		t.Errorf("the API's message is missing from the detail: %s", ErrorDetail(err))
	}
}

// Edits of one app must not overlap: a commit invalidates the app's other open
// edits, so concurrent writers would lose their work.
func TestEditsOfOnePackageAreSerialized(t *testing.T) {
	fake := fakeplay.New(testPackage)
	defer fake.Close()
	client := newTestClient(t, fake.URL)
	ctx := context.Background()

	const writers = 12

	var (
		wg         sync.WaitGroup
		inFlight   atomic.Int32
		overlapped atomic.Bool
		errs       = make(chan error, writers)
	)
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- client.CommitEdit(ctx, testPackage, func(editID string) error {
				if inFlight.Add(1) > 1 {
					overlapped.Store(true)
				}
				defer inFlight.Add(-1)

				_, err := client.Service.Edits.Tracks.Create(testPackage, editID, &androidpublisher.TrackConfig{
					Track: fmt.Sprintf("track-%d", i), FormFactor: "DEFAULT", Type: "CLOSED_TESTING",
				}).Context(ctx).Do()

				return err
			})
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Errorf("a concurrent edit failed: %v", err)
		}
	}
	if overlapped.Load() {
		t.Error("two edits of the same package were open at once")
	}
	if got := len(fake.Tracks(testPackage)); got != 4+writers {
		t.Errorf("%d tracks after %d writers, want %d: a commit was lost", got, writers, 4+writers)
	}
}

func TestErrorDetailIncludesTheResponseBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"code":403,"message":"The caller does not have permission","status":"PERMISSION_DENIED","details":[{"reason":"userNotInvited"}]}}`)
	}))
	defer srv.Close()

	client := newTestClient(t, srv.URL)
	_, err := client.Service.Monetization.Subscriptions.Get(testPackage, "premium").Do()

	if StatusCode(err) != http.StatusForbidden || IsNotFound(err) {
		t.Fatalf("StatusCode = %d for %v", StatusCode(err), err)
	}

	detail := ErrorDetail(err)
	for _, want := range []string{"403", "The caller does not have permission", "userNotInvited"} {
		if !strings.Contains(detail, want) {
			t.Errorf("the detail is missing %q:\n%s", want, detail)
		}
	}

	// A body that is not the usual JSON envelope is still shown.
	raw := &googleapi.Error{Code: 502, Body: "<html>bad gateway</html>"}
	if !strings.Contains(ErrorDetail(raw), "bad gateway") {
		t.Errorf("a raw body is missing from the detail: %s", ErrorDetail(raw))
	}

	if got := ErrorDetail(errors.New("plain")); got != "plain" {
		t.Errorf("ErrorDetail of a plain error = %q", got)
	}
	if StatusCode(errors.New("plain")) != 0 || ErrorDetail(nil) != "" {
		t.Error("non-API errors must report no status and no detail")
	}
}

// --- retries -----------------------------------------------------------------

// instantRetries makes a client's retry transport wait no time and records
// the delays it asked for.
func instantRetries(t *testing.T, client *Client) *[]time.Duration {
	t.Helper()

	var delays []time.Duration
	transport := &retryTransport{
		base:        http.DefaultTransport,
		maxAttempts: retryMaxAttempts,
		baseDelay:   retryBaseDelay,
		maxDelay:    retryMaxDelay,
		sleep: func(_ context.Context, d time.Duration) error {
			delays = append(delays, d)

			return nil
		},
	}

	service, err := androidpublisher.NewService(context.Background(),
		option.WithHTTPClient(&http.Client{Transport: transport}),
		option.WithEndpoint(client.Service.BasePath))
	if err != nil {
		t.Fatal(err)
	}
	client.Service = service

	return &delays
}

func TestRetriesTransientFailures(t *testing.T) {
	var (
		calls  atomic.Int32
		bodies []string
		mu     sync.Mutex
	)
	statuses := []int{http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusInternalServerError}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(body))
		mu.Unlock()

		call := int(calls.Add(1))
		if call <= len(statuses) {
			if call == 1 {
				w.Header().Set("Retry-After", "7")
			}
			w.WriteHeader(statuses[call-1])
			_, _ = io.WriteString(w, `{"error":{"code":503,"message":"try again"}}`)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"packageName":"com.example.app","productId":"premium"}`)
	}))
	defer srv.Close()

	client := newTestClient(t, srv.URL)
	delays := instantRetries(t, client)

	sub, err := client.Service.Monetization.Subscriptions.Patch(testPackage, "premium",
		&androidpublisher.Subscription{Listings: []*androidpublisher.SubscriptionListing{{LanguageCode: "en-US", Title: "Premium"}}}).
		UpdateMask("listings").RegionsVersionVersion("2022/02").Do()
	if err != nil {
		t.Fatalf("the request was not retried to success: %v", err)
	}
	if sub.ProductId != "premium" {
		t.Errorf("unexpected response: %+v", sub)
	}
	if got := calls.Load(); got != 4 {
		t.Errorf("%d calls, want 3 failures and 1 success", got)
	}

	// The request body is replayed in full on every attempt.
	for i, body := range bodies {
		if !strings.Contains(body, `"title":"Premium"`) {
			t.Errorf("attempt %d sent the body %q", i+1, body)
		}
	}

	if len(*delays) != 3 {
		t.Fatalf("waited %d times, want 3", len(*delays))
	}
	if (*delays)[0] != 7*time.Second {
		t.Errorf("the first wait was %s, want the server's Retry-After of 7s", (*delays)[0])
	}
	// Exponential with jitter: between half of and the whole of 2s, then 4s.
	for i, base := range []time.Duration{2 * time.Second, 4 * time.Second} {
		if got := (*delays)[i+1]; got < base/2 || got > base {
			t.Errorf("wait %d was %s, want between %s and %s", i+2, got, base/2, base)
		}
	}
}

func TestRetriesGiveUp(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"code":503,"message":"The service is currently unavailable."}}`)
	}))
	defer srv.Close()

	client := newTestClient(t, srv.URL)
	instantRetries(t, client)

	_, err := client.Service.Monetization.Subscriptions.Get(testPackage, "premium").Do()
	if StatusCode(err) != http.StatusServiceUnavailable {
		t.Fatalf("err = %v, want the final 503", err)
	}
	if got := calls.Load(); got != retryMaxAttempts {
		t.Errorf("%d attempts, want %d", got, retryMaxAttempts)
	}
	if !strings.Contains(ErrorDetail(err), "currently unavailable") {
		t.Errorf("the final error lost the API's message: %s", ErrorDetail(err))
	}
}

func TestDoesNotRetryClientErrors(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict} {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.WriteHeader(status)
		}))

		client := newTestClient(t, srv.URL)
		instantRetries(t, client)

		_, err := client.Service.Monetization.Subscriptions.Get(testPackage, "premium").Do()
		if StatusCode(err) != status {
			t.Errorf("status %d: err = %v", status, err)
		}
		if got := calls.Load(); got != 1 {
			t.Errorf("status %d was tried %d times, want once", status, got)
		}

		srv.Close()
	}
}

func TestRetryStopsWhenTheContextEnds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	client := newTestClient(t, srv.URL)

	// The real sleep, against a context that ends long before the 1s backoff.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := client.Service.Monetization.Subscriptions.Get(testPackage, "premium").Context(ctx).Do()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the context's deadline", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("gave up after %s, want promptly", elapsed)
	}
}

func TestRetryDelayIsCapped(t *testing.T) {
	transport := newRetryTransport(nil)

	for attempt := 1; attempt <= 40; attempt++ {
		if got := transport.delay(attempt, ""); got < 0 || got > retryMaxDelay {
			t.Errorf("delay(%d) = %s, want within [0, %s]", attempt, got, retryMaxDelay)
		}
	}
	if got := transport.delay(1, "3600"); got != retryMaxDelay {
		t.Errorf("a long Retry-After gave %s, want the cap %s", got, retryMaxDelay)
	}
}
