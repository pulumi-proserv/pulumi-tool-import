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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The refresh report (#41) is the one check that consults the deployed
// resource: injection's values come from the Terraform state file, and the
// verifying preview compares program against injected state — neither ever
// reads live. "pulumi refresh --preview-only --json" does, and every step
// carries both oldState and newState, so the report can say which property
// live disagrees on. It is a report, never a gate: the tool cannot tell stale
// state from a wrong program from Read normalisation noise, so the human
// adjudicates.

const refreshFixture = `{
  "steps": [
    {
      "op": "same",
      "urn": "urn:pulumi:dev::p::aws:ec2/vpnConnectionRoute:VpnConnectionRoute::route",
      "oldState": {"outputs": {"destinationCidrBlock": "10.0.0.0/16", "vpnConnectionId": "vpn-1"}},
      "newState": {"outputs": {"destinationCidrBlock": "10.0.0.0/16", "vpnConnectionId": "vpn-1"}}
    },
    {
      "op": "update",
      "urn": "urn:pulumi:dev::p::aws:ec2/vpnGateway:VpnGateway::gw",
      "oldState": {"outputs": {"amazonSideAsn": "64512", "tags": {"env": "dev"}}},
      "newState": {"outputs": {"amazonSideAsn": "64999", "tags": {"env": "dev"}}}
    },
    {
      "op": "delete",
      "urn": "urn:pulumi:dev::p::aws:iot/policyAttachment:PolicyAttachment::attach",
      "oldState": {"outputs": {"policy": "p"}},
      "newState": null
    }
  ],
  "changeSummary": {"same": 1, "update": 1, "delete": 1}
}`

func report(t *testing.T, urns ...string) []string {
	t.Helper()
	digest, err := ParseRefreshJSON([]byte(refreshFixture))
	require.NoError(t, err)
	return BuildRefreshReport(digest, urns)
}

// A resource the provider reports GONE is the sharpest finding: the injected
// ID resolves to nothing, and the next "pulumi up" would create — or, for a
// wrong-but-plausible ID, replace — a live resource. It leads the report.
func TestRefreshReport_GoneResourceIsFlagged(t *testing.T) {
	t.Parallel()

	lines := report(t, "urn:pulumi:dev::p::aws:iot/policyAttachment:PolicyAttachment::attach")
	require.NotEmpty(t, lines)
	joined := strings.Join(lines, "\n")
	assert.Contains(t, joined, "PolicyAttachment::attach")
	assert.Contains(t, joined, "GONE")
}

// A property live disagrees on is named with both values, so the reader can
// adjudicate: stale Terraform state, a wrong program, or Read normalisation.
func TestRefreshReport_DisagreementNamesPropertyAndBothValues(t *testing.T) {
	t.Parallel()

	lines := report(t, "urn:pulumi:dev::p::aws:ec2/vpnGateway:VpnGateway::gw")
	joined := strings.Join(lines, "\n")
	assert.Contains(t, joined, "amazonSideAsn")
	assert.Contains(t, joined, "64512")
	assert.Contains(t, joined, "64999")
	assert.NotContains(t, joined, "tags", "unchanged properties are not listed")
}

// "No diff" is not "confirmed": for a Read that returns its input, agreement
// is arithmetic. The report says the resource reported no change rather than
// implying live agreement was verified.
func TestRefreshReport_NoChangeIsReportedAsSuchNotAsConfirmation(t *testing.T) {
	t.Parallel()

	lines := report(t, "urn:pulumi:dev::p::aws:ec2/vpnConnectionRoute:VpnConnectionRoute::route")
	joined := strings.Join(lines, "\n")
	assert.Contains(t, joined, "no change")
	assert.NotContains(t, strings.ToLower(joined), "confirmed")
	assert.NotContains(t, strings.ToLower(joined), "verified")
}

// A URN the refresh did not mention at all is its own finding — silence is
// never allowed to read as success.
func TestRefreshReport_UnreportedURNIsNamed(t *testing.T) {
	t.Parallel()

	lines := report(t, "urn:pulumi:dev::p::aws:ec2/vpc:Vpc::missing")
	joined := strings.Join(lines, "\n")
	assert.Contains(t, joined, "Vpc::missing")
	assert.Contains(t, joined, "not reported")
}

// Secrets in the refresh JSON arrive masked by the CLI; the report must not
// unmask anything, and long values are truncated so a report line stays a line.
func TestRefreshReport_TruncatesLongValues(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("x", 500)
	digest, err := ParseRefreshJSON([]byte(`{"steps":[{"op":"update",
		"urn":"urn:pulumi:dev::p::aws:x/y:Y::r",
		"oldState":{"outputs":{"body":"` + long + `"}},
		"newState":{"outputs":{"body":"short"}}}]}`))
	require.NoError(t, err)
	lines := BuildRefreshReport(digest, []string{"urn:pulumi:dev::p::aws:x/y:Y::r"})
	for _, l := range lines {
		assert.LessOrEqual(t, len(l), 240, "line: %s", l)
	}
}

func TestRefreshPreviewArgs(t *testing.T) {
	t.Parallel()

	args := refreshPreviewJSONArgs("dev")
	assert.Contains(t, args, "refresh")
	assert.Contains(t, args, "--preview-only")
	assert.Contains(t, args, "--json")
	assert.NotContains(t, args, "--yes", "the report path must never run a real refresh")
}
