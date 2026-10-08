// Copyright (c) MathGaps
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestTrackFormFactor(t *testing.T) {
	for track, want := range map[string]string{
		"qa":              "DEFAULT",
		"production":      "DEFAULT",
		"wear:qa":         "WEAR",
		"automotive:beta": "AUTOMOTIVE",
		"wearable":        "DEFAULT",
	} {
		if got := trackFormFactor(track); got != want {
			t.Errorf("trackFormFactor(%q) = %q, want %q", track, got, want)
		}
	}
}

func TestTrackResource_lifecycle(t *testing.T) {
	fake := newUnitFake(t)

	const name = "googleplay_track.test"
	config := func(track string) string {
		return fmt.Sprintf(`
resource "googleplay_track" "test" {
  package_name = %q
  track        = %q
}`, unitPackage, track)
	}

	noOpenEdits := func(*terraform.State) error {
		if open := fake.OpenEdits(unitPackage); open != 0 {
			return fmt.Errorf("%d edits were left open", open)
		}

		return nil
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		CheckDestroy: func(s *terraform.State) error {
			// The API cannot delete a track: destroy only forgets it.
			if !slices.Contains(fake.Tracks(unitPackage), "wear:qa") {
				return fmt.Errorf("the track is gone, but the API has no call that deletes one")
			}
			if len(requestsMatching(fake, `^DELETE .*/tracks`)) != 0 {
				return fmt.Errorf("a track delete was attempted")
			}

			return noOpenEdits(s)
		},
		Steps: []resource.TestStep{
			{
				Config: config("qa"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(name, "id", unitPackage+"/qa"),
					resource.TestCheckResourceAttr(name, "form_factor", "DEFAULT"),
					resource.TestCheckResourceAttr(name, "type", "CLOSED_TESTING"),
					func(*terraform.State) error {
						if !slices.Contains(fake.Tracks(unitPackage), "qa") {
							return fmt.Errorf("the track was not committed: %v", fake.Tracks(unitPackage))
						}

						return nil
					},
					noOpenEdits,
				),
			},
			importSteps(name)[0],
			importSteps(name)[1],
			{
				// Every attribute forces replacement; the form factor follows
				// the prefix of the name.
				Config: config("wear:qa"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(name, "id", unitPackage+"/wear:qa"),
					resource.TestCheckResourceAttr(name, "form_factor", "WEAR"),
					noOpenEdits,
				),
			},
			importSteps(name)[0],
			importSteps(name)[1],
		},
	})

	// Releases are a pipeline's business: no request ever carried one.
	for _, request := range fake.Requests() {
		if regexp.MustCompile(`^(PUT|PATCH) .*/tracks/`).MatchString(request) {
			t.Errorf("a track was updated, which could overwrite its releases: %s", request)
		}
	}
}

func TestTrackResource_formFactorMustMatchPrefix(t *testing.T) {
	newUnitFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "googleplay_track" "test" {
  package_name = %q
  track        = "qa"
  form_factor  = "WEAR"
}`, unitPackage),
			ExpectError: regexp.MustCompile(`Form factor does not match the track name`),
		}},
	})
}

// Creating a track that exists fails with the API's own message, and the edit
// opened for it is deleted rather than left behind.
func TestTrackResource_createExistingFails(t *testing.T) {
	fake := newUnitFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "googleplay_track" "test" {
  package_name = %q
  track        = "alpha"
}`, unitPackage),
			ExpectError: regexp.MustCompile(`Track alpha already exists`),
		}},
	})

	if open := fake.OpenEdits(unitPackage); open != 0 {
		t.Errorf("%d edits were left open after a failed create", open)
	}
}

func TestTrackTestersResource_lifecycle(t *testing.T) {
	fake := newUnitFake(t)

	const name = "googleplay_track_testers.test"
	config := func(groups string) string {
		return fmt.Sprintf(`
resource "googleplay_track" "qa" {
  package_name = %[1]q
  track        = "qa"
}

resource "googleplay_track_testers" "test" {
  package_name  = googleplay_track.qa.package_name
  track         = googleplay_track.qa.track
  google_groups = [%[2]s]
}

# A second resource of the same app: its edit must not collide with the first.
resource "googleplay_track_testers" "internal" {
  package_name  = %[1]q
  track         = "internal"
  google_groups = ["internal-testers@example.com"]
}`, unitPackage, groups)
	}

	testersAre := func(track string, want ...string) resource.TestCheckFunc {
		return func(*terraform.State) error {
			got := fake.Testers(unitPackage, track)
			slices.Sort(got)
			if !slices.Equal(got, want) {
				return fmt.Errorf("testers of %s = %v, want %v", track, got, want)
			}
			if open := fake.OpenEdits(unitPackage); open != 0 {
				return fmt.Errorf("%d edits were left open", open)
			}

			return nil
		}
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		// Destroy empties the list.
		CheckDestroy: resource.ComposeAggregateTestCheckFunc(testersAre("qa"), testersAre("internal")),
		Steps: []resource.TestStep{
			{
				Config: config(`"qa@example.com", "beta@example.com"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(name, "id", unitPackage+"/qa"),
					resource.TestCheckResourceAttr(name, "google_groups.#", "2"),
					testersAre("qa", "beta@example.com", "qa@example.com"),
					testersAre("internal", "internal-testers@example.com"),
				),
			},
			{
				Config: config(`"qa@example.com"`),
				Check:  testersAre("qa", "qa@example.com"),
			},
			importSteps(name)[0],
			importSteps(name)[1],
			{
				// An empty set is a valid configuration: no groups.
				Config: config(``),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(name, "google_groups.#", "0"),
					testersAre("qa"),
				),
			},
		},
	})
}

func TestTracksDataSource(t *testing.T) {
	newUnitFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
data "googleplay_tracks" "test" {
  package_name = %q
}`, unitPackage),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.googleplay_tracks.test", "names.#", "4"),
				resource.TestCheckTypeSetElemAttr("data.googleplay_tracks.test", "names.*", "internal"),
				resource.TestCheckResourceAttr("data.googleplay_tracks.test", "tracks.0.track", "production"),
				resource.TestCheckResourceAttr("data.googleplay_tracks.test", "tracks.0.releases.#", "0"),
			),
		}},
	})
}

func TestTracksDataSource_unknownPackage(t *testing.T) {
	newUnitFake(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{{
			Config: `
data "googleplay_tracks" "test" {
  package_name = "com.example.missing"
}`,
			// The API's own message reaches the diagnostic.
			ExpectError: regexp.MustCompile(`Package not found: com\.example\.missing`),
		}},
	})
}

// --- acceptance --------------------------------------------------------------

// testAccTrack is the one custom track the acceptance tests use. A track
// cannot be deleted, so the suite never invents a name.
func testAccTrack() string {
	if track := os.Getenv(envTestTrack); track != "" {
		return track
	}

	return "tf-acc"
}

// TestAccTrackResource_create creates the fixed test track. It can succeed
// once per app, ever: the track cannot be deleted and creating it again fails.
// It therefore runs only when GOOGLEPLAY_TEST_CREATE_TRACK is set.
func TestAccTrackResource_create(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t, envTestCreateTrack) },
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
resource "googleplay_track" "test" {
  package_name = %q
  track        = %q
}`, os.Getenv(envTestPackage), testAccTrack()),
				Check: resource.TestCheckResourceAttr("googleplay_track.test", "type", "CLOSED_TESTING"),
			},
			{
				ResourceName:      "googleplay_track.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// TestAccTrackTestersResource_basic sets and clears the tester groups of the
// fixed test track, which must exist: see ACCEPTANCE_TESTING.md.
func TestAccTrackTestersResource_basic(t *testing.T) {
	packageName, group := os.Getenv(envTestPackage), os.Getenv(envTestGroup)

	config := func(groups string) string {
		return fmt.Sprintf(`
resource "googleplay_track_testers" "test" {
  package_name  = %q
  track         = %q
  google_groups = [%s]
}`, packageName, testAccTrack(), groups)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t, envTestGroup) },
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config(fmt.Sprintf("%q", group)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("googleplay_track_testers.test", "google_groups.#", "1"),
					resource.TestCheckTypeSetElemAttr("googleplay_track_testers.test", "google_groups.*", group),
				),
			},
			{
				ResourceName:      "googleplay_track_testers.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				Config: config(""),
				Check:  resource.TestCheckResourceAttr("googleplay_track_testers.test", "google_groups.#", "0"),
			},
		},
	})
}

func TestAccTracksDataSource_basic(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
data "googleplay_tracks" "test" {
  package_name = %q
}`, os.Getenv(envTestPackage)),
			Check: resource.TestCheckTypeSetElemAttr("data.googleplay_tracks.test", "names.*", "production"),
		}},
	})
}
