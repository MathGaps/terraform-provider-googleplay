// Copyright (c) MathGaps
// SPDX-License-Identifier: MPL-2.0

// Package fakeplay is an in-memory stand-in for the parts of the Google Play
// Developer API this provider calls. Tests point the real generated client at
// it, so requests and responses go over HTTP in the API's own JSON.
//
// It models the behaviour the provider depends on: edits that only take effect
// on commit, a commit invalidating the app's other open edits, products whose
// state changes only through the dedicated calls, and the defaults the server
// fills in, and the rules the API documents for refusing a request (deleting
// what has been published, reusing an id). It is not a faithful emulation of
// everything else.
package fakeplay

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"google.golang.org/api/androidpublisher/v3"
)

// RegionsVersion is the regions version the fake reports.
const RegionsVersion = "2022/02"

// Server is a fake Google Play Developer API.
type Server struct {
	*httptest.Server

	mu       sync.Mutex
	users    map[string]*androidpublisher.User // by lower-case email
	apps     map[string]*app
	requests []string
	nextEdit int
	// failures are the one-shot errors armed by FailNext, in arming order.
	failures []failure

	// Expansions maps a permission to the permissions the server stores in
	// its place, for app-level and account-level permissions alike. It starts
	// with the one replacement observed against the live API.
	Expansions map[string][]string

	// pageLimit, when positive, caps the page of every list that pages. See
	// SetPageLimit.
	pageLimit int
}

type app struct {
	tracks        []string
	testers       map[string][]string
	edits         map[string]*edit
	subscriptions map[string]*androidpublisher.Subscription
	products      map[string]*androidpublisher.OneTimeProduct
	// offers holds the subscription offers by offerKey.
	offers map[string]*androidpublisher.SubscriptionOffer
	// retiredOffers holds the offerKey of every offer ever deleted. Play
	// Console's help says an offer id cannot be reused, and the fake holds
	// the API to that.
	retiredOffers map[string]bool
}

// offerKey identifies an offer within an app.
func offerKey(productID, basePlanID, offerID string) string {
	return productID + "/" + basePlanID + "/" + offerID
}

// failure is one request FailNext arms to be answered with an error.
type failure struct {
	method string
	path   *regexp.Regexp
	err    *apiError
}

// FailNext makes the next request with this method whose path matches
// pathPattern answer with code and status instead of being served, once. It
// stands in for what a test cannot otherwise provoke: a call that fails after
// an earlier one in the same operation succeeded. A 429 or 5xx is retried by
// the client, so use a status it gives up on.
func (s *Server) FailNext(method, pathPattern string, code int, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.failures = append(s.failures, failure{
		method: method,
		path:   regexp.MustCompile(pathPattern),
		err:    &apiError{code, status, "failure armed by the test"},
	})
}

// edit is a private copy of the app's tracks and testers.
type edit struct {
	tracks  []string
	testers map[string][]string
}

// New starts a fake with one app per package name. Each app starts with the
// built-in tracks and nothing else.
func New(packageNames ...string) *Server {
	s := &Server{
		users: map[string]*androidpublisher.User{},
		apps:  map[string]*app{},
		Expansions: map[string][]string{
			"CAN_ACCESS_APP": {"CAN_VIEW_APP_QUALITY", "CAN_VIEW_NON_FINANCIAL_DATA"},
		},
	}
	for _, name := range packageNames {
		s.apps[name] = &app{
			tracks:        []string{"production", "beta", "alpha", "internal"},
			testers:       map[string][]string{},
			edits:         map[string]*edit{},
			subscriptions: map[string]*androidpublisher.Subscription{},
			products:      map[string]*androidpublisher.OneTimeProduct{},
			offers:        map[string]*androidpublisher.SubscriptionOffer{},
			retiredOffers: map[string]bool{},
		}
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))

	return s
}

// SetPageLimit caps the page of every list that pages at n items, whatever
// page size the request asks for. The API documents that it "may return fewer
// than" the page size, so a client has to follow the page tokens; this makes
// it.
func (s *Server) SetPageLimit(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.pageLimit = n
}

// Requests returns every request received so far as "METHOD path?query".
func (s *Server) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.requests)
}

// OpenEdits returns the number of edits of the app that were opened and
// neither committed nor deleted.
func (s *Server) OpenEdits(packageName string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	if a, ok := s.apps[packageName]; ok {
		return len(a.edits)
	}

	return 0
}

// Tracks returns the app's committed track names.
func (s *Server) Tracks(packageName string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.apps[packageName].tracks)
}

// PutTesters sets the Google Groups of a track as if in Play Console.
func (s *Server) PutTesters(packageName, track string, groups ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.apps[packageName].testers[track] = slices.Clone(groups)
}

// Testers returns the committed Google Groups of a track.
func (s *Server) Testers(packageName, track string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.apps[packageName].testers[track])
}

// User returns a copy of the user with the given email, or nil.
func (s *Server) User(email string) *androidpublisher.User {
	s.mu.Lock()
	defer s.mu.Unlock()

	return clone(s.users[strings.ToLower(email)])
}

// PutUser stores a user as if it had been created in Play Console.
func (s *Server) PutUser(user *androidpublisher.User) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.users[strings.ToLower(user.Email)] = clone(user)
}

// DeleteUser removes a user as if it had been removed in Play Console.
func (s *Server) DeleteUser(email string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.users, strings.ToLower(email))
}

// Subscription returns a copy of a subscription, or nil.
func (s *Server) Subscription(packageName, productID string) *androidpublisher.Subscription {
	s.mu.Lock()
	defer s.mu.Unlock()

	return clone(s.apps[packageName].subscriptions[productID])
}

// PutSubscription stores a subscription as if it had been created in Play
// Console, exactly as given.
func (s *Server) PutSubscription(sub *androidpublisher.Subscription) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.apps[sub.PackageName].subscriptions[sub.ProductId] = clone(sub)
}

// Offer returns a copy of a subscription offer, or nil.
func (s *Server) Offer(packageName, productID, basePlanID, offerID string) *androidpublisher.SubscriptionOffer {
	s.mu.Lock()
	defer s.mu.Unlock()

	return clone(s.apps[packageName].offers[offerKey(productID, basePlanID, offerID)])
}

// PutOffer stores a subscription offer as if it had been created in Play
// Console, exactly as given.
func (s *Server) PutOffer(offer *androidpublisher.SubscriptionOffer) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.apps[offer.PackageName].offers[offerKey(offer.ProductId, offer.BasePlanId, offer.OfferId)] = clone(offer)
}

// OneTimeProduct returns a copy of a one-time product, or nil.
func (s *Server) OneTimeProduct(packageName, productID string) *androidpublisher.OneTimeProduct {
	s.mu.Lock()
	defer s.mu.Unlock()

	return clone(s.apps[packageName].products[productID])
}

// clone deep-copies an API value through its JSON form.
func clone[T any](in *T) *T {
	if in == nil {
		return nil
	}

	data, err := json.Marshal(in)
	if err != nil {
		panic(err)
	}

	out := new(T)
	if err := json.Unmarshal(data, out); err != nil {
		panic(err)
	}

	return out
}

type apiError struct {
	code    int
	status  string
	message string
}

func notFound(format string, args ...any) *apiError {
	return &apiError{http.StatusNotFound, "NOT_FOUND", fmt.Sprintf(format, args...)}
}

func badRequest(format string, args ...any) *apiError {
	return &apiError{http.StatusBadRequest, "INVALID_ARGUMENT", fmt.Sprintf(format, args...)}
}

func failedPrecondition(format string, args ...any) *apiError {
	return &apiError{http.StatusBadRequest, "FAILED_PRECONDITION", fmt.Sprintf(format, args...)}
}

func conflict(format string, args ...any) *apiError {
	return &apiError{http.StatusConflict, "ALREADY_EXISTS", fmt.Sprintf(format, args...)}
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry := r.Method + " " + r.URL.Path
	if r.URL.RawQuery != "" {
		entry += "?" + r.URL.RawQuery
	}
	s.requests = append(s.requests, entry)

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}

	var result any
	apiErr := s.armedFailure(r)
	if apiErr == nil {
		result, apiErr = s.route(r, body)
	}

	w.Header().Set("Content-Type", "application/json")
	if apiErr != nil {
		w.WriteHeader(apiErr.code)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"code":    apiErr.code,
				"message": apiErr.message,
				"status":  apiErr.status,
			},
		})

		return
	}

	if result == nil {
		result = struct{}{}
	}
	_ = json.NewEncoder(w).Encode(result)
}

// armedFailure takes the first failure FailNext armed for this request.
func (s *Server) armedFailure(r *http.Request) *apiError {
	for i, f := range s.failures {
		if f.method == r.Method && f.path.MatchString(r.URL.Path) {
			s.failures = slices.Delete(s.failures, i, i+1)

			return f.err
		}
	}

	return nil
}

func decode[T any](body []byte) (*T, *apiError) {
	out := new(T)
	if len(body) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return nil, badRequest("invalid JSON payload: %v", err)
	}

	return out, nil
}

func (s *Server) route(r *http.Request, body []byte) (any, *apiError) {
	path, ok := strings.CutPrefix(r.URL.Path, "/androidpublisher/v3/")
	if !ok {
		return nil, notFound("unknown path %s", r.URL.Path)
	}

	parts := strings.Split(path, "/")
	switch {
	case parts[0] == "developers" && len(parts) >= 3:
		return s.routeDeveloper(r, parts[2:], body)
	case parts[0] == "applications" && len(parts) >= 3:
		a, ok := s.apps[parts[1]]
		if !ok {
			return nil, notFound("Package not found: %s.", parts[1])
		}

		return s.routeApp(r, a, parts[1], parts[2:], body)
	}

	return nil, notFound("unknown path %s", r.URL.Path)
}

// --- users and grants --------------------------------------------------------

func (s *Server) routeDeveloper(r *http.Request, parts []string, body []byte) (any, *apiError) {
	developer := strings.Split(strings.TrimPrefix(r.URL.Path, "/androidpublisher/v3/"), "/")[1]
	if parts[0] != "users" {
		return nil, notFound("unknown path %s", r.URL.Path)
	}

	switch len(parts) {
	case 1: // users
		switch r.Method {
		case http.MethodGet:
			return s.listUsers(r)
		case http.MethodPost:
			user, apiErr := decode[androidpublisher.User](body)
			if apiErr != nil {
				return nil, apiErr
			}
			if user.Email == "" {
				return nil, badRequest("email is required")
			}
			key := strings.ToLower(user.Email)
			if _, exists := s.users[key]; exists {
				return nil, conflict("User %s already exists.", user.Email)
			}
			// What the live API answers for a user who would hold nothing: a
			// create must carry an account permission or a grant.
			if len(user.DeveloperAccountPermissions) == 0 && len(user.Grants) == 0 {
				return nil, badRequest("No permissions set for this user.")
			}
			user.Name = "developers/" + developer + "/users/" + user.Email
			user.AccessState = "INVITED"
			user.DeveloperAccountPermissions = s.expand(user.DeveloperAccountPermissions)
			for _, grant := range user.Grants {
				if grant.PackageName == "" || len(grant.AppLevelPermissions) == 0 {
					return nil, badRequest("a grant needs a packageName and appLevelPermissions")
				}
				grant.Name = user.Name + "/grants/" + grant.PackageName
				grant.AppLevelPermissions = s.expand(grant.AppLevelPermissions)
			}
			s.users[key] = user

			return clone(user), nil
		}
	case 2: // users/{email}
		user, ok := s.users[strings.ToLower(parts[1])]
		if !ok {
			return nil, notFound("User %s not found.", parts[1])
		}
		switch r.Method {
		case http.MethodPatch:
			patch, apiErr := decode[androidpublisher.User](body)
			if apiErr != nil {
				return nil, apiErr
			}
			for _, field := range maskFields(r) {
				switch field {
				case "developerAccountPermissions":
					user.DeveloperAccountPermissions = s.expand(patch.DeveloperAccountPermissions)
				case "expirationTime":
					user.ExpirationTime = patch.ExpirationTime
				default:
					return nil, badRequest("unknown update mask field %q", field)
				}
			}

			return clone(user), nil
		case http.MethodDelete:
			delete(s.users, strings.ToLower(parts[1]))

			return nil, nil
		}
	case 3: // users/{email}/grants
		user, ok := s.users[strings.ToLower(parts[1])]
		if !ok {
			return nil, notFound("User %s not found.", parts[1])
		}
		if parts[2] == "grants" && r.Method == http.MethodPost {
			grant, apiErr := decode[androidpublisher.Grant](body)
			if apiErr != nil {
				return nil, apiErr
			}
			if grant.PackageName == "" {
				return nil, badRequest("packageName is required")
			}
			if slices.ContainsFunc(user.Grants, func(g *androidpublisher.Grant) bool { return g.PackageName == grant.PackageName }) {
				return nil, conflict("Grant for %s already exists.", grant.PackageName)
			}
			grant.Name = user.Name + "/grants/" + grant.PackageName
			grant.AppLevelPermissions = s.expand(grant.AppLevelPermissions)
			user.Grants = append(user.Grants, grant)

			return clone(grant), nil
		}
	case 4: // users/{email}/grants/{package}
		user, ok := s.users[strings.ToLower(parts[1])]
		if !ok {
			return nil, notFound("User %s not found.", parts[1])
		}
		index := slices.IndexFunc(user.Grants, func(g *androidpublisher.Grant) bool { return g.PackageName == parts[3] })
		if parts[2] != "grants" || index < 0 {
			return nil, notFound("Grant %s not found.", parts[3])
		}
		switch r.Method {
		case http.MethodPatch:
			patch, apiErr := decode[androidpublisher.Grant](body)
			if apiErr != nil {
				return nil, apiErr
			}
			for _, field := range maskFields(r) {
				if field != "appLevelPermissions" {
					return nil, badRequest("unknown update mask field %q", field)
				}
				user.Grants[index].AppLevelPermissions = s.expand(patch.AppLevelPermissions)
			}

			return clone(user.Grants[index]), nil
		case http.MethodDelete:
			user.Grants = slices.Delete(user.Grants, index, index+1)

			return nil, nil
		}
	}

	return nil, notFound("unknown path %s %s", r.Method, r.URL.Path)
}

// expand replaces each permission that has an expansion with what the server
// stores for it, without duplicates, keeping the order.
func (s *Server) expand(permissions []string) []string {
	var out []string
	for _, permission := range permissions {
		stored, ok := s.Expansions[permission]
		if !ok {
			stored = []string{permission}
		}
		for _, p := range stored {
			if !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}

	return out
}

// listUsers returns every user in one response. Like the real API it refuses
// to page: the request must carry pageSize=-1 and no page token.
func (s *Server) listUsers(r *http.Request) (any, *apiError) {
	query := r.URL.Query()
	if query.Get("pageSize") != "-1" || query.Get("pageToken") != "" {
		return nil, badRequest("Pagination is not supported: pageSize must be -1.")
	}

	emails := make([]string, 0, len(s.users))
	for email := range s.users {
		emails = append(emails, email)
	}
	slices.Sort(emails)

	resp := &androidpublisher.ListUsersResponse{}
	for _, email := range emails {
		resp.Users = append(resp.Users, clone(s.users[email]))
	}

	return resp, nil
}

func maskFields(r *http.Request) []string {
	mask := r.URL.Query().Get("updateMask")
	if mask == "" {
		return nil
	}

	return strings.Split(mask, ",")
}

// --- apps --------------------------------------------------------------------

func (s *Server) routeApp(r *http.Request, a *app, packageName string, parts []string, body []byte) (any, *apiError) {
	switch {
	case parts[0] == "edits":
		return s.routeEdits(r, a, parts[1:], body)
	case parts[0] == "edits:commit":
		return nil, notFound("unknown path %s", r.URL.Path)
	case parts[0] == "subscriptions":
		return s.routeSubscriptions(r, a, packageName, parts[1:], body)
	case strings.EqualFold(parts[0], "onetimeproducts"):
		return s.routeProducts(r, a, packageName, parts[1:], body)
	case parts[0] == "pricing:convertRegionPrices" && r.Method == http.MethodPost:
		return convertRegionPrices(body)
	}

	return nil, notFound("unknown path %s %s", r.Method, r.URL.Path)
}

// --- edits, tracks, testers --------------------------------------------------

func (s *Server) routeEdits(r *http.Request, a *app, parts []string, body []byte) (any, *apiError) {
	if len(parts) == 0 {
		if r.Method != http.MethodPost {
			return nil, notFound("unknown path %s %s", r.Method, r.URL.Path)
		}
		s.nextEdit++
		id := fmt.Sprintf("edit-%d", s.nextEdit)
		copied := &edit{tracks: slices.Clone(a.tracks), testers: map[string][]string{}}
		for track, groups := range a.testers {
			copied.testers[track] = slices.Clone(groups)
		}
		a.edits[id] = copied

		return &androidpublisher.AppEdit{Id: id, ExpiryTimeSeconds: "4102444800"}, nil
	}

	id, action, _ := strings.Cut(parts[0], ":")
	e, ok := a.edits[id]
	if !ok {
		// What the API answers for an edit a commit of another edit invalidated.
		return nil, failedPrecondition("This Edit has been deleted or is no longer valid: %s.", id)
	}

	if len(parts) == 1 {
		switch {
		case action == "commit" && r.Method == http.MethodPost:
			a.tracks = e.tracks
			a.testers = e.testers
			// A commit invalidates every other open edit of the app.
			a.edits = map[string]*edit{}

			return &androidpublisher.AppEdit{Id: id}, nil
		case action == "" && r.Method == http.MethodDelete:
			delete(a.edits, id)

			return nil, nil
		}

		return nil, notFound("unknown path %s %s", r.Method, r.URL.Path)
	}

	switch parts[1] {
	case "tracks":
		if len(parts) == 2 {
			switch r.Method {
			case http.MethodGet:
				resp := &androidpublisher.TracksListResponse{Kind: "androidpublisher#tracksListResponse"}
				for _, track := range e.tracks {
					resp.Tracks = append(resp.Tracks, &androidpublisher.Track{Track: track})
				}

				return resp, nil
			case http.MethodPost:
				config, apiErr := decode[androidpublisher.TrackConfig](body)
				if apiErr != nil {
					return nil, apiErr
				}
				if config.Track == "" || config.Type != "CLOSED_TESTING" || config.FormFactor == "" {
					return nil, badRequest("track, type (CLOSED_TESTING) and formFactor are required")
				}
				if slices.Contains(e.tracks, config.Track) {
					return nil, conflict("Track %s already exists.", config.Track)
				}
				e.tracks = append(e.tracks, config.Track)

				return &androidpublisher.Track{Track: config.Track}, nil
			}
		}
		if len(parts) == 3 && r.Method == http.MethodGet {
			if !slices.Contains(e.tracks, parts[2]) {
				return nil, notFound("Track not found: %s.", parts[2])
			}

			return &androidpublisher.Track{Track: parts[2]}, nil
		}
	case "testers":
		if len(parts) != 3 {
			break
		}
		if !slices.Contains(e.tracks, parts[2]) {
			return nil, notFound("Track not found: %s.", parts[2])
		}
		switch r.Method {
		case http.MethodGet:
			return &androidpublisher.Testers{GoogleGroups: e.testers[parts[2]]}, nil
		case http.MethodPut:
			testers, apiErr := decode[androidpublisher.Testers](body)
			if apiErr != nil {
				return nil, apiErr
			}
			e.testers[parts[2]] = testers.GoogleGroups

			return testers, nil
		}
	}

	return nil, notFound("unknown path %s %s", r.Method, r.URL.Path)
}

// --- subscriptions -----------------------------------------------------------

func requireRegionsVersion(r *http.Request) *apiError {
	if r.URL.Query().Get("regionsVersion.version") == "" {
		return badRequest("regionsVersion.version is required")
	}

	return nil
}

// fillBasePlanDefaults applies what the server fills in when a field is left
// out.
func fillBasePlanDefaults(plan *androidpublisher.BasePlan) {
	if t := plan.AutoRenewingBasePlanType; t != nil {
		if t.GracePeriodDuration == "" {
			t.GracePeriodDuration = "P3D"
		}
		if t.ResubscribeState == "" {
			t.ResubscribeState = "RESUBSCRIBE_STATE_ACTIVE"
		}
		if t.ProrationMode == "" {
			t.ProrationMode = "SUBSCRIPTION_PRORATION_MODE_CHARGE_ON_NEXT_BILLING_DATE"
		}
	}
	if t := plan.InstallmentsBasePlanType; t != nil {
		if t.GracePeriodDuration == "" {
			t.GracePeriodDuration = "P3D"
		}
		if t.ResubscribeState == "" {
			t.ResubscribeState = "RESUBSCRIBE_STATE_ACTIVE"
		}
		if t.ProrationMode == "" {
			t.ProrationMode = "SUBSCRIPTION_PRORATION_MODE_CHARGE_ON_NEXT_BILLING_DATE"
		}
	}
	if t := plan.PrepaidBasePlanType; t != nil && t.TimeExtension == "" {
		t.TimeExtension = "TIME_EXTENSION_ACTIVE"
	}
}

// mergeBasePlans replaces the stored base plans with the incoming ones. State
// is output only: an existing plan keeps its state and a new one is a draft.
// Dropping a plan this way is refused, so a client has to use the delete call.
func mergeBasePlans(existing, incoming []*androidpublisher.BasePlan) ([]*androidpublisher.BasePlan, *apiError) {
	states := map[string]string{}
	for _, plan := range existing {
		states[plan.BasePlanId] = plan.State
	}

	seen := map[string]bool{}
	for _, plan := range incoming {
		if plan.BasePlanId == "" {
			return nil, badRequest("basePlanId is required")
		}
		if seen[plan.BasePlanId] {
			return nil, badRequest("duplicate base plan %s", plan.BasePlanId)
		}
		seen[plan.BasePlanId] = true

		types := 0
		for _, set := range []bool{plan.AutoRenewingBasePlanType != nil, plan.PrepaidBasePlanType != nil, plan.InstallmentsBasePlanType != nil} {
			if set {
				types++
			}
		}
		if types != 1 {
			return nil, badRequest("base plan %s must set exactly one base plan type", plan.BasePlanId)
		}

		plan.State = "DRAFT"
		if state, ok := states[plan.BasePlanId]; ok {
			plan.State = state
		}
		fillBasePlanDefaults(plan)
	}

	for id := range states {
		if !seen[id] {
			return nil, failedPrecondition("Base plan %s cannot be removed by updating the subscription.", id)
		}
	}

	return incoming, nil
}

func (s *Server) routeSubscriptions(r *http.Request, a *app, packageName string, parts []string, body []byte) (any, *apiError) {
	if len(parts) == 0 && r.Method == http.MethodGet {
		return listSubscriptions(r, a, s.pageLimit)
	}
	if len(parts) == 0 {
		if r.Method != http.MethodPost {
			return nil, notFound("unknown path %s %s", r.Method, r.URL.Path)
		}
		if apiErr := requireRegionsVersion(r); apiErr != nil {
			return nil, apiErr
		}
		productID := r.URL.Query().Get("productId")
		if productID == "" {
			return nil, badRequest("productId is required")
		}
		if _, exists := a.subscriptions[productID]; exists {
			return nil, conflict("Subscription %s already exists.", productID)
		}
		sub, apiErr := decode[androidpublisher.Subscription](body)
		if apiErr != nil {
			return nil, apiErr
		}

		return s.storeSubscription(a, packageName, productID, sub, nil)
	}

	productID := parts[0]
	sub, exists := a.subscriptions[productID]

	if len(parts) >= 4 && parts[1] == "basePlans" && parts[3] == "offers" {
		return s.routeOffers(r, a, packageName, productID, parts[2], parts[4:], body)
	}

	if len(parts) >= 4 && parts[1] == "basePlans" && parts[3] == "offers" {
		return s.routeOffers(r, a, packageName, productID, parts[2], parts[4:], body)
	}

	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			if !exists {
				return nil, notFound("Subscription not found: %s.", productID)
			}

			return clone(sub), nil
		case http.MethodPatch:
			if apiErr := requireRegionsVersion(r); apiErr != nil {
				return nil, apiErr
			}
			mask := maskFields(r)
			if len(mask) == 0 {
				return nil, badRequest("updateMask is required")
			}
			patch, apiErr := decode[androidpublisher.Subscription](body)
			if apiErr != nil {
				return nil, apiErr
			}
			if !exists {
				if r.URL.Query().Get("allowMissing") != "true" {
					return nil, notFound("Subscription not found: %s.", productID)
				}

				return s.storeSubscription(a, packageName, productID, patch, nil)
			}

			return s.storeSubscription(a, packageName, productID, patch, mask)
		case http.MethodDelete:
			if !exists {
				return nil, notFound("Subscription not found: %s.", productID)
			}
			for _, plan := range sub.BasePlans {
				if plan.State != "DRAFT" {
					return nil, failedPrecondition("Subscription %s has had a base plan published and cannot be deleted.", productID)
				}
			}
			delete(a.subscriptions, productID)
			a.dropOffers(productID, "")

			return nil, nil
		}

		return nil, notFound("unknown path %s %s", r.Method, r.URL.Path)
	}

	if !exists {
		return nil, notFound("Subscription not found: %s.", productID)
	}
	if len(parts) != 3 || parts[1] != "basePlans" {
		return nil, notFound("unknown path %s %s", r.Method, r.URL.Path)
	}

	basePlanID, action, _ := strings.Cut(parts[2], ":")
	index := slices.IndexFunc(sub.BasePlans, func(p *androidpublisher.BasePlan) bool { return p.BasePlanId == basePlanID })
	if index < 0 {
		return nil, notFound("Base plan not found: %s.", basePlanID)
	}
	plan := sub.BasePlans[index]

	switch {
	case action == "activate" && r.Method == http.MethodPost:
		plan.State = "ACTIVE"

		return clone(sub), nil
	case action == "deactivate" && r.Method == http.MethodPost:
		if plan.State != "ACTIVE" {
			return nil, failedPrecondition("Base plan %s is not active.", basePlanID)
		}
		plan.State = "INACTIVE"

		return clone(sub), nil
	case action == "" && r.Method == http.MethodDelete:
		if plan.State != "DRAFT" {
			return nil, failedPrecondition("Base plan %s is not a draft and cannot be deleted.", basePlanID)
		}
		sub.BasePlans = slices.Delete(sub.BasePlans, index, index+1)
		a.dropOffers(productID, basePlanID)

		return nil, nil
	}

	return nil, notFound("unknown path %s %s", r.Method, r.URL.Path)
}

// storeSubscription creates the subscription (mask nil) or applies the masked
// fields of patch to the stored one.
func (s *Server) storeSubscription(a *app, packageName, productID string, patch *androidpublisher.Subscription, mask []string) (any, *apiError) {
	current, exists := a.subscriptions[productID]
	if !exists || mask == nil {
		current = &androidpublisher.Subscription{PackageName: packageName, ProductId: productID}
		mask = []string{"listings", "basePlans", "taxAndComplianceSettings", "restrictedPaymentCountries"}
	}

	for _, field := range mask {
		switch field {
		case "listings":
			if len(patch.Listings) == 0 {
				return nil, badRequest("listings must contain at least one entry")
			}
			current.Listings = patch.Listings
		case "basePlans":
			merged, apiErr := mergeBasePlans(current.BasePlans, patch.BasePlans)
			if apiErr != nil {
				return nil, apiErr
			}
			current.BasePlans = merged
		case "taxAndComplianceSettings":
			current.TaxAndComplianceSettings = patch.TaxAndComplianceSettings
		case "restrictedPaymentCountries":
			current.RestrictedPaymentCountries = patch.RestrictedPaymentCountries
		default:
			return nil, badRequest("unknown update mask field %q", field)
		}
	}

	// The server always reports tax and compliance settings, filling in the
	// withdrawal right type.
	if current.TaxAndComplianceSettings == nil {
		current.TaxAndComplianceSettings = &androidpublisher.SubscriptionTaxAndComplianceSettings{}
	}
	if current.TaxAndComplianceSettings.EeaWithdrawalRightType == "" {
		current.TaxAndComplianceSettings.EeaWithdrawalRightType = "WITHDRAWAL_RIGHT_DIGITAL_CONTENT"
	}

	a.subscriptions[productID] = current

	return clone(current), nil
}

// listSubscriptions pages through the subscriptions of the app in product id
// order. showArchived is not read: the client documents it as "Deprecated:
// subscription archiving is not supported".
func listSubscriptions(r *http.Request, a *app, limit int) (any, *apiError) {
	ids := make([]string, 0, len(a.subscriptions))
	for id := range a.subscriptions {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	page, next, apiErr := pageOf(r, len(ids), limit)
	if apiErr != nil {
		return nil, apiErr
	}

	resp := &androidpublisher.ListSubscriptionsResponse{NextPageToken: next}
	for _, id := range ids[page[0]:page[1]] {
		resp.Subscriptions = append(resp.Subscriptions, clone(a.subscriptions[id]))
	}

	return resp, nil
}

// --- subscription offers -----------------------------------------------------

// dropOffers forgets the offers of a deleted subscription, or of one of its
// base plans when basePlanID is set. Their ids stay retired.
func (a *app) dropOffers(productID, basePlanID string) {
	for key, offer := range a.offers {
		if offer.ProductId == productID && (basePlanID == "" || offer.BasePlanId == basePlanID) {
			a.retiredOffers[key] = true
			delete(a.offers, key)
		}
	}
}

// validateOffer applies the rules the API documents for a subscription offer.
func validateOffer(offer *androidpublisher.SubscriptionOffer) *apiError {
	// "Must contain at least one and at most two entries."
	if len(offer.Phases) < 1 || len(offer.Phases) > 2 {
		return badRequest("an offer must have one or two phases, not %d", len(offer.Phases))
	}
	// "Must contain at least one entry."
	if len(offer.RegionalConfigs) == 0 {
		return badRequest("regionalConfigs must contain at least one entry")
	}

	regions := map[string]bool{}
	for _, config := range offer.RegionalConfigs {
		if config.RegionCode == "" || regions[config.RegionCode] {
			return badRequest("regionalConfigs must name each region once")
		}
		regions[config.RegionCode] = true
	}

	for i, phase := range offer.Phases {
		if phase.Duration == "" || phase.RecurrenceCount < 1 {
			return badRequest("phase %d needs a duration and a recurrenceCount", i)
		}
		// "This list must contain exactly one entry for each region for which
		// the subscription offer has a regional config."
		seen := map[string]bool{}
		for _, config := range phase.RegionalConfigs {
			if !regions[config.RegionCode] || seen[config.RegionCode] {
				return badRequest("phase %d must have exactly one regional config for each region of the offer", i)
			}
			seen[config.RegionCode] = true
			if apiErr := validatePhasePrice(i, config.RegionCode, config.Price != nil, config.AbsoluteDiscount != nil,
				config.RelativeDiscount, config.Free != nil); apiErr != nil {
				return apiErr
			}
		}
		if len(seen) != len(regions) {
			return badRequest("phase %d must have exactly one regional config for each region of the offer", i)
		}
		if other := phase.OtherRegionsConfig; other != nil {
			if apiErr := validatePhasePrice(i, "other regions", other.OtherRegionsPrices != nil, other.AbsoluteDiscounts != nil,
				other.RelativeDiscount, other.Free != nil); apiErr != nil {
				return apiErr
			}
		}
	}

	if targeting := offer.Targeting; targeting != nil {
		// "Only allows 'this subscription' and 'any subscription in app'."
		if rule := targeting.AcquisitionRule; rule != nil {
			if rule.Scope == nil || rule.Scope.SpecificSubscriptionInApp != "" ||
				(rule.Scope.ThisSubscription == nil) == (rule.Scope.AnySubscriptionInApp == nil) {
				return badRequest("an acquisition rule's scope must be this subscription or any subscription in the app")
			}
		}
		// "Only allows 'this subscription' and 'specific subscription in app'."
		if rule := targeting.UpgradeRule; rule != nil {
			if rule.Scope == nil || rule.Scope.AnySubscriptionInApp != nil ||
				(rule.Scope.ThisSubscription == nil) == (rule.Scope.SpecificSubscriptionInApp == "") {
				return badRequest("an upgrade rule's scope must be this subscription or a specific subscription in the app")
			}
		}
	}

	return nil
}

// validatePhasePrice requires exactly one way of pricing a phase, and a
// relative discount "strictly larger than 0 and strictly smaller than 1".
func validatePhasePrice(phase int, region string, price, absolute bool, relative float64, free bool) *apiError {
	set := 0
	for _, isSet := range []bool{price, absolute, relative != 0, free} {
		if isSet {
			set++
		}
	}
	if set != 1 {
		return badRequest("phase %d in %s must set exactly one of price, absoluteDiscount, relativeDiscount and free", phase, region)
	}
	if relative != 0 && (relative <= 0 || relative >= 1) {
		return badRequest("phase %d in %s: relativeDiscount must be strictly between 0 and 1", phase, region)
	}

	return nil
}

func (s *Server) routeOffers(r *http.Request, a *app, packageName, productID, basePlanID string, parts []string, body []byte) (any, *apiError) {
	if len(parts) == 0 && r.Method == http.MethodGet {
		return listOffers(r, a, productID, basePlanID, s.pageLimit)
	}

	sub, exists := a.subscriptions[productID]
	if !exists {
		return nil, notFound("Subscription not found: %s.", productID)
	}
	planIndex := slices.IndexFunc(sub.BasePlans, func(p *androidpublisher.BasePlan) bool { return p.BasePlanId == basePlanID })
	if planIndex < 0 {
		return nil, notFound("Base plan not found: %s.", basePlanID)
	}

	if len(parts) == 0 {
		if r.Method != http.MethodPost {
			return nil, notFound("unknown path %s %s", r.Method, r.URL.Path)
		}
		if apiErr := requireRegionsVersion(r); apiErr != nil {
			return nil, apiErr
		}
		offerID := r.URL.Query().Get("offerId")
		if offerID == "" {
			return nil, badRequest("offerId is required")
		}
		offer, apiErr := decode[androidpublisher.SubscriptionOffer](body)
		if apiErr != nil {
			return nil, apiErr
		}

		return a.createOffer(sub.BasePlans[planIndex], packageName, productID, basePlanID, offerID, offer)
	}
	if len(parts) != 1 {
		return nil, notFound("unknown path %s %s", r.Method, r.URL.Path)
	}

	offerID, action, _ := strings.Cut(parts[0], ":")
	key := offerKey(productID, basePlanID, offerID)
	offer, exists := a.offers[key]

	switch {
	case action == "" && r.Method == http.MethodGet:
		if !exists {
			return nil, notFound("Offer not found: %s.", offerID)
		}

		return clone(offer), nil
	case action == "" && r.Method == http.MethodPatch:
		if apiErr := requireRegionsVersion(r); apiErr != nil {
			return nil, apiErr
		}
		patch, apiErr := decode[androidpublisher.SubscriptionOffer](body)
		if apiErr != nil {
			return nil, apiErr
		}
		if !exists {
			// "If set to true, and the subscription offer ... doesn't exist,
			// an offer will be created. If a new offer is created,
			// update_mask is ignored."
			if r.URL.Query().Get("allowMissing") != "true" {
				return nil, notFound("Offer not found: %s.", offerID)
			}

			return a.createOffer(sub.BasePlans[planIndex], packageName, productID, basePlanID, offerID, patch)
		}
		mask := maskFields(r)
		if len(mask) == 0 {
			return nil, badRequest("updateMask is required")
		}

		updated := clone(offer)
		for _, field := range mask {
			switch field {
			case "phases":
				updated.Phases = patch.Phases
			case "regionalConfigs":
				updated.RegionalConfigs = patch.RegionalConfigs
			case "otherRegionsConfig":
				updated.OtherRegionsConfig = patch.OtherRegionsConfig
			case "targeting":
				updated.Targeting = patch.Targeting
			case "offerTags":
				updated.OfferTags = patch.OfferTags
			default:
				// The ids are immutable and the state is output only.
				return nil, badRequest("unknown or immutable update mask field %q", field)
			}
		}
		if apiErr := validateOffer(updated); apiErr != nil {
			return nil, apiErr
		}
		a.offers[key] = updated

		return clone(updated), nil
	case action == "" && r.Method == http.MethodDelete:
		if !exists {
			return nil, notFound("Offer not found: %s.", offerID)
		}
		// "Can only be done for draft offers."
		if offer.State != "DRAFT" {
			return nil, failedPrecondition("Offer %s is %s: only a draft offer can be deleted.", offerID, offer.State)
		}
		delete(a.offers, key)
		a.retiredOffers[key] = true

		return nil, nil
	case action == "activate" && r.Method == http.MethodPost:
		if !exists {
			return nil, notFound("Offer not found: %s.", offerID)
		}
		offer.State = "ACTIVE"

		return clone(offer), nil
	case action == "deactivate" && r.Method == http.MethodPost:
		if !exists {
			return nil, notFound("Offer not found: %s.", offerID)
		}
		if offer.State != "ACTIVE" {
			return nil, failedPrecondition("Offer %s is not active.", offerID)
		}
		offer.State = "INACTIVE"

		return clone(offer), nil
	}

	return nil, notFound("unknown path %s %s", r.Method, r.URL.Path)
}

// createOffer stores a new draft offer on a base plan.
func (a *app) createOffer(plan *androidpublisher.BasePlan, packageName, productID, basePlanID, offerID string,
	offer *androidpublisher.SubscriptionOffer,
) (any, *apiError) {
	key := offerKey(productID, basePlanID, offerID)
	if _, exists := a.offers[key]; exists {
		return nil, conflict("Offer %s already exists.", offerID)
	}
	if a.retiredOffers[key] {
		return nil, conflict("Offer id %s was used before and cannot be reused.", offerID)
	}
	// "Only auto-renewing base plans can have subscription offers."
	if plan.AutoRenewingBasePlanType == nil {
		return nil, failedPrecondition("Only auto-renewing base plans can have subscription offers.")
	}
	// The ids in the body "must be equal to" the ones in the path.
	for _, pair := range [][2]string{
		{offer.PackageName, packageName}, {offer.ProductId, productID},
		{offer.BasePlanId, basePlanID}, {offer.OfferId, offerID},
	} {
		if pair[0] != "" && pair[0] != pair[1] {
			return nil, badRequest("the offer names %s where the request names %s", pair[0], pair[1])
		}
	}
	if apiErr := validateOffer(offer); apiErr != nil {
		return nil, apiErr
	}

	offer.PackageName, offer.ProductId, offer.BasePlanId, offer.OfferId = packageName, productID, basePlanID, offerID
	// State is output only: a new offer is a draft until activated.
	offer.State = "DRAFT"
	a.offers[key] = offer

	return clone(offer), nil
}

// listOffers pages through the offers of a base plan, of a subscription (base
// plan "-") or of the app (product and base plan "-"), 50 at a time unless
// the request asks for up to 1000.
func listOffers(r *http.Request, a *app, productID, basePlanID string, limit int) (any, *apiError) {
	// "Must be specified as '-' if product_id is specified as '-'."
	if productID == "-" && basePlanID != "-" {
		return nil, badRequest("basePlanId must be - when productId is -")
	}
	if productID != "-" {
		sub, exists := a.subscriptions[productID]
		if !exists {
			return nil, notFound("Subscription not found: %s.", productID)
		}
		if basePlanID != "-" && !slices.ContainsFunc(sub.BasePlans, func(p *androidpublisher.BasePlan) bool { return p.BasePlanId == basePlanID }) {
			return nil, notFound("Base plan not found: %s.", basePlanID)
		}
	}

	var keys []string
	for key, offer := range a.offers {
		if (productID == "-" || offer.ProductId == productID) && (basePlanID == "-" || offer.BasePlanId == basePlanID) {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)

	page, next, apiErr := pageOf(r, len(keys), limit)
	if apiErr != nil {
		return nil, apiErr
	}

	resp := &androidpublisher.ListSubscriptionOffersResponse{NextPageToken: next}
	for _, key := range keys[page[0]:page[1]] {
		resp.SubscriptionOffers = append(resp.SubscriptionOffers, clone(a.offers[key]))
	}

	return resp, nil
}

// pageOf reads pageSize and pageToken and returns the [start, end) of the page
// within n items and the token of the next page, if any. The token is the
// offset of the next page: opaque to a client. A positive limit caps the page.
func pageOf(r *http.Request, n, limit int) ([2]int, string, *apiError) {
	query := r.URL.Query()

	size := 50
	if value := query.Get("pageSize"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 {
			return [2]int{}, "", badRequest("invalid pageSize %q", value)
		}
		if parsed > 0 {
			size = min(parsed, 1000)
		}
	}

	if limit > 0 {
		size = min(size, limit)
	}

	start := 0
	if token := query.Get("pageToken"); token != "" {
		parsed, err := strconv.Atoi(token)
		if err != nil || parsed < 0 || parsed > n {
			return [2]int{}, "", badRequest("invalid pageToken %q", token)
		}
		start = parsed
	}

	end := min(start+size, n)
	next := ""
	if end < n {
		next = strconv.Itoa(end)
	}

	return [2]int{start, end}, next, nil
}

// --- one-time products -------------------------------------------------------

func (s *Server) routeProducts(r *http.Request, a *app, packageName string, parts []string, body []byte) (any, *apiError) {
	if len(parts) == 0 {
		return nil, notFound("unknown path %s %s", r.Method, r.URL.Path)
	}

	productID := parts[0]
	product, exists := a.products[productID]

	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			if !exists {
				return nil, notFound("One-time product not found: %s.", productID)
			}

			return clone(product), nil
		case http.MethodPatch:
			if apiErr := requireRegionsVersion(r); apiErr != nil {
				return nil, apiErr
			}
			mask := maskFields(r)
			if len(mask) == 0 {
				return nil, badRequest("updateMask is required")
			}
			patch, apiErr := decode[androidpublisher.OneTimeProduct](body)
			if apiErr != nil {
				return nil, apiErr
			}
			if !exists {
				if r.URL.Query().Get("allowMissing") != "true" {
					return nil, notFound("One-time product not found: %s.", productID)
				}
				mask = nil
			}

			return s.storeProduct(r, a, packageName, productID, patch, mask)
		case http.MethodDelete:
			if !exists {
				return nil, notFound("One-time product not found: %s.", productID)
			}
			delete(a.products, productID)

			return nil, nil
		}

		return nil, notFound("unknown path %s %s", r.Method, r.URL.Path)
	}

	if !exists {
		return nil, notFound("One-time product not found: %s.", productID)
	}
	if len(parts) != 2 || r.Method != http.MethodPost {
		return nil, notFound("unknown path %s %s", r.Method, r.URL.Path)
	}

	find := func(id string) int {
		return slices.IndexFunc(product.PurchaseOptions, func(o *androidpublisher.OneTimeProductPurchaseOption) bool {
			return o.PurchaseOptionId == id
		})
	}

	switch parts[1] {
	case "purchaseOptions:batchUpdateStates":
		req, apiErr := decode[androidpublisher.BatchUpdatePurchaseOptionStatesRequest](body)
		if apiErr != nil {
			return nil, apiErr
		}
		resp := &androidpublisher.BatchUpdatePurchaseOptionStatesResponse{}
		for _, update := range req.Requests {
			switch {
			case update.ActivatePurchaseOptionRequest != nil:
				index := find(update.ActivatePurchaseOptionRequest.PurchaseOptionId)
				if index < 0 {
					return nil, notFound("Purchase option not found.")
				}
				product.PurchaseOptions[index].State = "ACTIVE"
			case update.DeactivatePurchaseOptionRequest != nil:
				index := find(update.DeactivatePurchaseOptionRequest.PurchaseOptionId)
				if index < 0 {
					return nil, notFound("Purchase option not found.")
				}
				if product.PurchaseOptions[index].State != "ACTIVE" {
					return nil, failedPrecondition("Purchase option is not active.")
				}
				product.PurchaseOptions[index].State = "INACTIVE"
			default:
				return nil, badRequest("empty state update request")
			}
			resp.OneTimeProducts = append(resp.OneTimeProducts, clone(product))
		}

		return resp, nil
	case "purchaseOptions:batchDelete":
		req, apiErr := decode[androidpublisher.BatchDeletePurchaseOptionsRequest](body)
		if apiErr != nil {
			return nil, apiErr
		}
		// "All requests must delete purchase options from different one-time
		// products", and this path names a single product.
		if len(req.Requests) != 1 {
			return nil, badRequest("all requests must delete purchase options from different one-time products")
		}
		index := find(req.Requests[0].PurchaseOptionId)
		if index < 0 {
			return nil, notFound("Purchase option not found.")
		}
		product.PurchaseOptions = slices.Delete(product.PurchaseOptions, index, index+1)

		return nil, nil
	}

	return nil, notFound("unknown path %s %s", r.Method, r.URL.Path)
}

// storeProduct creates the product (mask nil) or applies the masked fields of
// patch to the stored one.
func (s *Server) storeProduct(r *http.Request, a *app, packageName, productID string, patch *androidpublisher.OneTimeProduct, mask []string) (any, *apiError) {
	current, exists := a.products[productID]
	if !exists || mask == nil {
		current = &androidpublisher.OneTimeProduct{PackageName: packageName, ProductId: productID}
		mask = []string{"listings", "purchaseOptions", "taxAndComplianceSettings", "restrictedPaymentCountries", "offerTags"}
	}

	for _, field := range mask {
		switch field {
		case "listings":
			if len(patch.Listings) == 0 {
				return nil, badRequest("listings must contain at least one entry")
			}
			current.Listings = patch.Listings
		case "purchaseOptions":
			states := map[string]string{}
			for _, option := range current.PurchaseOptions {
				states[option.PurchaseOptionId] = option.State
			}
			seen := map[string]bool{}
			for _, option := range patch.PurchaseOptions {
				if (option.BuyOption == nil) == (option.RentOption == nil) {
					return nil, badRequest("purchase option %s must set exactly one of buyOption and rentOption", option.PurchaseOptionId)
				}
				seen[option.PurchaseOptionId] = true
				option.State = "DRAFT"
				if state, ok := states[option.PurchaseOptionId]; ok {
					option.State = state
				}
				if option.TaxAndComplianceSettings == nil {
					option.TaxAndComplianceSettings = &androidpublisher.PurchaseOptionTaxAndComplianceSettings{}
				}
				if option.TaxAndComplianceSettings.WithdrawalRightType == "" {
					option.TaxAndComplianceSettings.WithdrawalRightType = "WITHDRAWAL_RIGHT_DIGITAL_CONTENT"
				}
			}
			for id := range states {
				if !seen[id] {
					return nil, failedPrecondition("Purchase option %s cannot be removed by updating the product.", id)
				}
			}
			current.PurchaseOptions = patch.PurchaseOptions
		case "taxAndComplianceSettings":
			current.TaxAndComplianceSettings = patch.TaxAndComplianceSettings
		case "restrictedPaymentCountries":
			current.RestrictedPaymentCountries = patch.RestrictedPaymentCountries
		case "offerTags":
			current.OfferTags = patch.OfferTags
		default:
			return nil, badRequest("unknown update mask field %q", field)
		}
	}

	current.RegionsVersion = &androidpublisher.RegionsVersion{Version: r.URL.Query().Get("regionsVersion.version")}
	a.products[productID] = current

	return clone(current), nil
}

// --- price conversion --------------------------------------------------------

// convertRegionPrices answers with fixed, made-up conversions: enough to show
// the shape of the response, including a price below one unit and a currency
// without minor units.
func convertRegionPrices(body []byte) (any, *apiError) {
	req, apiErr := decode[androidpublisher.ConvertRegionPricesRequest](body)
	if apiErr != nil {
		return nil, apiErr
	}
	if req.Price == nil || req.Price.CurrencyCode == "" {
		return nil, badRequest("price is required")
	}

	return &androidpublisher.ConvertRegionPricesResponse{
		ConvertedRegionPrices: map[string]androidpublisher.ConvertedRegionPrice{
			"US": {
				RegionCode: "US",
				Price:      req.Price,
				TaxAmount:  &androidpublisher.Money{CurrencyCode: req.Price.CurrencyCode},
			},
			"GB": {
				RegionCode: "GB",
				Price:      &androidpublisher.Money{CurrencyCode: "GBP", Units: req.Price.Units, Nanos: 490_000_000},
				TaxAmount:  &androidpublisher.Money{CurrencyCode: "GBP", Nanos: 750_000_000},
			},
			"JP": {
				RegionCode: "JP",
				Price:      &androidpublisher.Money{CurrencyCode: "JPY", Units: 800},
				TaxAmount:  &androidpublisher.Money{CurrencyCode: "JPY", Units: 73},
			},
		},
		ConvertedOtherRegionsPrice: &androidpublisher.ConvertedOtherRegionsPrice{
			UsdPrice: &androidpublisher.Money{CurrencyCode: "USD", Units: req.Price.Units, Nanos: req.Price.Nanos},
			EurPrice: &androidpublisher.Money{CurrencyCode: "EUR", Units: req.Price.Units, Nanos: 590_000_000},
		},
		RegionVersion: &androidpublisher.RegionsVersion{Version: RegionsVersion},
	}, nil
}
