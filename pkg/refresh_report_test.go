// Copyright 2016-2025, Pulumi Corporation.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package pkg

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fixture mirrors what `pulumi refresh --preview-only --json` actually
// emits (verified against the pinned pulumi/pkg source): an unchanged
// resource carries op "refresh" with newState a pre-Read COPY of oldState —
// there are no live values on that step — and the CLI rewrites a step only to
// op "update" (with a detailed diff and real post-Read state) or "delete".

const (
	urnRouteNoDiff  = "urn:pulumi:dev::p::aws:ec2/vpnConnectionRoute:VpnConnectionRoute::route"
	urnGatewayDrift = "urn:pulumi:dev::p::aws:ec2/vpnGateway:VpnGateway::gw"
	urnAttachGone   = "urn:pulumi:dev::p::aws:iot/policyAttachment:PolicyAttachment::attach"
	urnUnreported   = "urn:pulumi:dev::p::aws:ec2/vpc:Vpc::missing"
)

const refreshFixture = `{
  "steps": [
    {
      "op": "refresh",
      "urn": "` + urnRouteNoDiff + `",
      "oldState": {"outputs": {"destinationCidrBlock": "10.0.0.0/16", "vpnConnectionId": "vpn-1"}},
      "newState": {"outputs": {"destinationCidrBlock": "10.0.0.0/16", "vpnConnectionId": "vpn-1"}}
    },
    {
      "op": "update",
      "urn": "` + urnGatewayDrift + `",
      "oldState": {"id": "vgw-1", "outputs": {"amazonSideAsn": "64512", "tags": {"env": "dev"},
        "subnets": ["a", "b"]}},
      "newState": {"id": "vgw-1", "outputs": {"amazonSideAsn": "64999", "tags": {"env": "dev"},
        "subnets": ["b", "a"]}}
    },
    {
      "op": "delete",
      "urn": "` + urnAttachGone + `",
      "oldState": {"outputs": {"policy": "p"}},
      "newState": null
    }
  ],
  "changeSummary": {"refresh": 1, "update": 1, "delete": 1}
}`

func reportForFixture(t *testing.T, urns ...string) []string {
	t.Helper()
	digest, err := ParsePreviewJSON([]byte(refreshFixture))
	require.NoError(t, err)
	return BuildRefreshReport(digest, urns)
}

// A resource the provider reports GONE is the sharpest finding: the injected
// ID resolves to nothing, and the next "pulumi up" would create — or, for a
// wrong-but-plausible ID, replace — a live resource.
func TestRefreshReport_GoneResourceIsFlagged(t *testing.T) {
	t.Parallel()

	joined := strings.Join(reportForFixture(t, urnAttachGone), "\n")
	assert.Contains(t, joined, "PolicyAttachment::attach")
	assert.Contains(t, joined, "GONE")
}

// A property live disagrees on is named with both values; an array that
// differs only in element order is annotated rather than dumped, since Read
// commonly reorders multi-valued properties and that noise must not train the
// operator to skim.
func TestRefreshReport_DisagreementNamesPropertyAndBothValues(t *testing.T) {
	t.Parallel()

	joined := strings.Join(reportForFixture(t, urnGatewayDrift), "\n")
	assert.Contains(t, joined, "amazonSideAsn")
	assert.Contains(t, joined, "64512")
	assert.Contains(t, joined, "64999")
	assert.Contains(t, joined, "subnets: differs only in element order")
	assert.NotContains(t, joined, "tags", "unchanged properties are not listed")
}

// An op "refresh" step carries a pre-Read copy of oldState, not live values;
// the report must say only what that establishes (the ID resolves) and never
// imply a comparison happened.
func TestRefreshReport_NoDiffSaysWhatWasLearned(t *testing.T) {
	t.Parallel()

	joined := strings.Join(reportForFixture(t, urnRouteNoDiff), "\n")
	assert.Contains(t, joined, "no diff reported")
	assert.Contains(t, joined, "ID resolves")
	assert.NotContains(t, strings.ToLower(joined), "confirmed")
	assert.NotContains(t, strings.ToLower(joined), "verified")
}

// A URN the refresh did not mention at all is its own finding — silence is
// never allowed to read as success.
func TestRefreshReport_UnreportedURNIsNamed(t *testing.T) {
	t.Parallel()

	joined := strings.Join(reportForFixture(t, urnUnreported), "\n")
	assert.Contains(t, joined, "Vpc::missing")
	assert.Contains(t, joined, "not reported")
}

// The ID is the one value the docs identify as genuinely checked against the
// cloud for every type; a Read that rewrites it must show in the diff.
func TestRefreshReport_ChangedIDIsDiffed(t *testing.T) {
	t.Parallel()

	digest, err := ParsePreviewJSON([]byte(`{"steps":[{"op":"update",
		"urn":"urn:pulumi:dev::p::aws:x/y:Y::r",
		"oldState":{"id":"old-id","outputs":{"a":"1"}},
		"newState":{"id":"live-id","outputs":{"a":"1"}}}]}`))
	require.NoError(t, err)
	joined := strings.Join(BuildRefreshReport(digest, []string{"urn:pulumi:dev::p::aws:x/y:Y::r"}), "\n")
	assert.Contains(t, joined, "id: injected=old-id live=live-id")
}

// One value is truncated at exactly maxRenderedValueRunes runes, with the
// ellipsis marker, and the untruncated payload never reaches the line —
// multi-byte runes are never cut mid-sequence.
func TestRefreshReport_TruncatesLongValuesAtTheLimit(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("é", 300)
	digest, err := ParsePreviewJSON([]byte(`{"steps":[{"op":"update",
		"urn":"urn:pulumi:dev::p::aws:x/y:Y::r",
		"oldState":{"outputs":{"body":"` + long + `"}},
		"newState":{"outputs":{"body":"short"}}}]}`))
	require.NoError(t, err)
	lines := BuildRefreshReport(digest, []string{"urn:pulumi:dev::p::aws:x/y:Y::r"})
	joined := strings.Join(lines, "\n")
	assert.NotContains(t, joined, long, "the full payload must not appear")
	rendered := strings.Repeat("é", maxRenderedValueRunes) + "…"
	assert.Contains(t, joined, rendered, "truncation is at the limit, on runes, with the marker")
}

// Per-resource diff lines are capped with an explicit remainder, matching
// formatDiffReasons' precedent, so a widely-normalising Read cannot bury the
// GONE lines.
func TestRefreshReport_CapsDiffLines(t *testing.T) {
	t.Parallel()

	var olds, news []string
	for i := 0; i < 12; i++ {
		olds = append(olds, fmt.Sprintf("%q: \"old%d\"", fmt.Sprintf("p%02d", i), i))
		news = append(news, fmt.Sprintf("%q: \"new%d\"", fmt.Sprintf("p%02d", i), i))
	}
	digest, err := ParsePreviewJSON([]byte(`{"steps":[{"op":"update",
		"urn":"urn:pulumi:dev::p::aws:x/y:Y::r",
		"oldState":{"outputs":{` + strings.Join(olds, ",") + `}},
		"newState":{"outputs":{` + strings.Join(news, ",") + `}}}]}`))
	require.NoError(t, err)
	lines := BuildRefreshReport(digest, []string{"urn:pulumi:dev::p::aws:x/y:Y::r"})
	joined := strings.Join(lines, "\n")
	assert.Contains(t, joined, "and 4 more")
	assert.LessOrEqual(t, len(lines), 2+maxDiffLinesPerResource,
		"header + capped diffs + remainder")
}

// The args must never run a real refresh: --preview-only is load-bearing,
// because a real refresh can delete an injected resource from state when Read
// reports it gone.
func TestRefreshPreviewArgs_NeverRunARealRefresh(t *testing.T) {
	t.Parallel()

	args := refreshPreviewJSONArgs("dev")
	assert.Contains(t, args, "refresh")
	assert.Contains(t, args, "--preview-only")
	assert.Contains(t, args, "--json")
	assert.NotContains(t, args, "--yes")
	assert.NotContains(t, args, "--skip-preview")
}
