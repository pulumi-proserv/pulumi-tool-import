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

package importsupport

import (
	"context"
	"errors"
	"testing"

	"github.com/pulumi-proserv/pulumi-tool-import/pkg/tfprovider"
	"github.com/pulumi/pulumi-terraform-bridge/v3/pkg/vendored/opentofu/providers"
	"github.com/pulumi/pulumi-terraform-bridge/v3/pkg/vendored/opentofu/tfdiags"
	"github.com/stretchr/testify/assert"
)

const (
	randomProvider        = "registry.terraform.io/hashicorp/random"
	randomProviderVersion = "3.7.2"
)

func randomProberVersions() map[string]string {
	return map[string]string{randomProvider: randomProviderVersion}
}

// random_shuffle declares no importer; random_id does. Both are answered by an
// unconfigured provider, with no credentials and no API calls.
func TestProberReportsTypeWithoutImporterAsUnsupported(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p := NewProber(randomProberVersions())
	defer p.Close(ctx)

	assert.Equal(t, Unsupported, p.Check(ctx, randomProvider, "random_shuffle"))
}

func TestProberReportsTypeWithImporterAsSupported(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p := NewProber(randomProberVersions())
	defer p.Close(ctx)

	assert.Equal(t, Supported, p.Check(ctx, randomProvider, "random_id"))
}

// Results are memoized so a digest with many resources of one type probes once.
// A cached verdict survives the provider process being shut down.
func TestProberMemoizesVerdicts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p := NewProber(randomProberVersions())

	first := p.Check(ctx, randomProvider, "random_shuffle")
	p.Close(ctx)
	second := p.Check(ctx, randomProvider, "random_shuffle")

	assert.Equal(t, Unsupported, first)
	assert.Equal(t, first, second)
}

// With no locked version there is nothing to load, so the curated list answers
// for the types it covers.
func TestProberFallsBackToCuratedListWhenProviderCannotLoad(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p := NewProber(nil)
	defer p.Close(ctx)

	assert.Equal(t, Unsupported,
		p.Check(ctx, "registry.terraform.io/hashicorp/aws", "aws_vpn_gateway_route_propagation"))
}

// The curated list is a floor, not an oracle: a type it does not cover is
// reported Unknown rather than guessed as importable.
func TestProberReportsUnknownForUncoveredTypeWhenProviderCannotLoad(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p := NewProber(nil)
	defer p.Close(ctx)

	assert.Equal(t, Unknown,
		p.Check(ctx, "registry.terraform.io/hashicorp/aws", "aws_s3_bucket"))
}

// deadProvider stands in for a plugin whose process has died: every call fails
// at the transport rather than returning a provider diagnostic.
type deadProvider struct {
	providers.Interface
	closed bool
}

func (d *deadProvider) Name() string    { return "dead" }
func (d *deadProvider) Version() string { return "0.0.0" }

func (d *deadProvider) Close(context.Context) error {
	d.closed = true
	return nil
}

func (d *deadProvider) ImportResourceState(
	context.Context, providers.ImportResourceStateRequest,
) providers.ImportResourceStateResponse {
	var diags tfdiags.Diagnostics
	return providers.ImportResourceStateResponse{
		Diagnostics: diags.Append(errors.New("rpc error: code = Unavailable desc = transport is closing")),
	}
}

const awsProvider = "registry.terraform.io/hashicorp/aws"

func proberWithDeadProvider() (*Prober, *deadProvider, *[]string) {
	dead := &deadProvider{}
	warnings := &[]string{}
	p := NewProber(map[string]string{awsProvider: "5.100.0"})
	p.loadProvider = func(context.Context, string, string) (tfprovider.Provider, error) {
		return dead, nil
	}
	p.Warn = func(msg string) { *warnings = append(*warnings, msg) }
	return p, dead, warnings
}

// A dead plugin must not turn every type into "importable" — that would put
// genuinely non-importable resources back into the import file.
func TestProberFallsBackWhenTheProviderDies(t *testing.T) {
	t.Parallel()
	p, _, warnings := proberWithDeadProvider()
	defer p.Close(context.Background())

	assert.Equal(t, Unsupported,
		p.Check(context.Background(), awsProvider, "aws_vpn_gateway_route_propagation"))
	assert.Equal(t, Unknown,
		p.Check(context.Background(), awsProvider, "aws_s3_bucket"))
	assert.NotEmpty(t, *warnings, "a provider that stops responding must be reported")
}

// The dead handle is dropped rather than reused for every subsequent type.
func TestProberDiscardsTheDeadProvider(t *testing.T) {
	t.Parallel()
	p, dead, _ := proberWithDeadProvider()
	defer p.Close(context.Background())

	p.Check(context.Background(), awsProvider, "aws_s3_bucket")

	assert.True(t, dead.closed, "the dead provider should be shut down")
	assert.Empty(t, p.providers, "the dead provider should not stay cached")
}
