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
	"testing"

	"github.com/stretchr/testify/assert"
)

const randomProvider = "registry.terraform.io/hashicorp/random"

func randomProberVersions() map[string]string {
	return map[string]string{randomProvider: "3.7.2"}
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
