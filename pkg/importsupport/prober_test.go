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
	"fmt"
	"testing"

	"github.com/pulumi-proserv/pulumi-tool-import/pkg/tfprovider"
	"github.com/pulumi/pulumi-terraform-bridge/v3/pkg/vendored/opentofu/providers"
	"github.com/pulumi/pulumi-terraform-bridge/v3/pkg/vendored/opentofu/tfdiags"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// crashyOnceProvider crashes on every call to ImportResourceState for one
// designated type and answers normally for everything else, so it stands in
// for a provider where a single resource type (e.g. aws_kinesis_stream) takes
// the plugin process down.
type crashyOnceProvider struct {
	providers.Interface
	crashType string
	closed    bool
}

func (c *crashyOnceProvider) Name() string    { return "crashy" }
func (c *crashyOnceProvider) Version() string { return "0.0.0" }

func (c *crashyOnceProvider) Close(context.Context) error {
	c.closed = true
	return nil
}

func (c *crashyOnceProvider) ImportResourceState(
	_ context.Context, req providers.ImportResourceStateRequest,
) providers.ImportResourceStateResponse {
	var diags tfdiags.Diagnostics
	if req.TypeName == c.crashType {
		return providers.ImportResourceStateResponse{
			Diagnostics: diags.Append(errors.New("rpc error: code = Unavailable desc = Plugin did not respond")),
		}
	}
	return providers.ImportResourceStateResponse{}
}

// A crash on one type must not poison the types probed after it: the
// provider is reloaded and answers the rest for real, rather than falling
// back to the curated list for everything.
func TestProberRestartsAfterASingleCrashingType(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	var loads int
	var instances []*crashyOnceProvider
	warnings := &[]string{}
	p := NewProber(map[string]string{awsProvider: "5.100.0"})
	p.loadProvider = func(context.Context, string, string) (tfprovider.Provider, error) {
		loads++
		inst := &crashyOnceProvider{crashType: "crashy"}
		instances = append(instances, inst)
		return inst, nil
	}
	p.Warn = func(msg string) { *warnings = append(*warnings, msg) }
	defer p.Close(ctx)

	a := p.Check(ctx, awsProvider, "a")
	crashy := p.Check(ctx, awsProvider, "crashy")
	b := p.Check(ctx, awsProvider, "b")

	assert.Equal(t, Supported, a)
	assert.Equal(t, Supported, b, "the type after the crash should get a real verdict from the reloaded provider")
	assert.Equal(t, Unknown, crashy, "the crashed type falls back since the curated list does not cover it")
	assert.Equal(t, 2, loads, "the provider should be reloaded once after the crash")
	require.Len(t, instances, 2)
	assert.True(t, instances[0].closed, "the crashed instance should be shut down")
	assert.NotEmpty(t, *warnings)
}

// Once a provider has crashed maxProviderRestarts times, it is given up on:
// remaining types are answered from the fallback without paying for another
// reload.
func TestProberGivesUpAfterRestartBudgetExhausted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	var loads int
	p := NewProber(map[string]string{awsProvider: "5.100.0"})
	p.loadProvider = func(context.Context, string, string) (tfprovider.Provider, error) {
		loads++
		return &alwaysCrashesProvider{}, nil
	}
	p.Warn = func(string) {}
	defer p.Close(ctx)

	verdicts := make([]Support, 0, 5)
	for _, tfType := range []string{"crash1", "crash2", "crash3", "crash4", "crash5"} {
		verdicts = append(verdicts, p.Check(ctx, awsProvider, tfType))
	}

	for _, v := range verdicts {
		assert.Equal(t, Unknown, v)
	}
	assert.Equal(t, maxProviderRestarts, loads,
		"the provider should be loaded once per crash up to the restart budget, then never again")
}

// alwaysCrashesProvider crashes on ImportResourceState for every type.
type alwaysCrashesProvider struct {
	providers.Interface
}

func (alwaysCrashesProvider) Name() string                { return "always-crashes" }
func (alwaysCrashesProvider) Version() string             { return "0.0.0" }
func (alwaysCrashesProvider) Close(context.Context) error { return nil }

func (alwaysCrashesProvider) ImportResourceState(
	context.Context, providers.ImportResourceStateRequest,
) providers.ImportResourceStateResponse {
	var diags tfdiags.Diagnostics
	return providers.ImportResourceStateResponse{
		Diagnostics: diags.Append(errors.New("rpc error: code = Unavailable desc = Plugin did not respond")),
	}
}

func TestProberResolvesEquivalentRegistryHosts(t *testing.T) {
	t.Parallel()

	p := NewProber(map[string]string{
		"registry.terraform.io/hashicorp/aws": "5.100.0",
	})
	var loadedAddr, loadedVersion string
	p.loadProvider = func(_ context.Context, providerAddr, version string) (tfprovider.Provider, error) {
		loadedAddr, loadedVersion = providerAddr, version
		return nil, fmt.Errorf("stop before launching a real provider")
	}

	_, _ = p.Provider(context.Background(), "registry.opentofu.org/hashicorp/aws")
	require.Equal(t, "registry.opentofu.org/hashicorp/aws", loadedAddr,
		"the provider loads under the requested form")
	require.Equal(t, "5.100.0", loadedVersion,
		"the version comes from the lock file's terraform.io key")
}
