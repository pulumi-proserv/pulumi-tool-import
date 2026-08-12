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
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/pulumi-proserv/pulumi-tool-import/pkg/tfprovider"
	"github.com/pulumi/pulumi-terraform-bridge/v3/pkg/vendored/opentofu/providers"
)

//go:embed fallback.json
var fallbackData []byte

// probeID is the dummy ID handed to ImportResourceState. Both Terraform SDKs
// reject a missing importer before looking at the ID, so its value only ever
// shows up in errors from types that *do* support import.
const probeID = "pulumi-tool-import-probe"

// fallbackTypes are the resource types the curated list covers.
var fallbackTypes = loadFallbackTypes()

func loadFallbackTypes() map[string]bool {
	var parsed struct {
		TerraformTypes []string `json:"terraformTypes"`
	}
	types := map[string]bool{}
	if err := json.Unmarshal(fallbackData, &parsed); err != nil {
		// The list is embedded at build time; a parse failure is a build bug.
		return types
	}
	for _, t := range parsed.TerraformTypes {
		types[t] = true
	}
	return types
}

// Prober answers whether a Terraform resource type supports import, by asking
// the provider itself.
//
// Providers are loaded lazily, at most once each, and every verdict is
// memoized: a digest with 159 resources spanning 40 types costs 40 probes.
// When a provider cannot be loaded — no locked version, no network, an
// air-gapped run — the curated fallback list answers for the types it covers
// and everything else is Unknown.
//
// A Prober is safe for concurrent use. Close it when done.
type Prober struct {
	// versions maps provider source address to the exact version to load.
	versions map[string]string
	// Warn receives one-line diagnostics; defaults to writing to stderr.
	Warn func(string)

	mu        sync.Mutex
	verdicts  map[string]Support // "provider|type" -> verdict
	providers map[string]tfprovider.Provider
	failed    map[string]bool // providers already known not to load

	// loadProvider is tfprovider.LoadProvider, replaced in tests.
	loadProvider func(ctx context.Context, providerAddr, version string) (tfprovider.Provider, error)
}

// NewProber returns a Prober that loads each provider at its locked version.
// Versions typically come from tofu.LockedProviderVersions; a provider missing
// from the map is not probed.
func NewProber(versions map[string]string) *Prober {
	return &Prober{
		versions:     versions,
		verdicts:     map[string]Support{},
		providers:    map[string]tfprovider.Provider{},
		failed:       map[string]bool{},
		loadProvider: tfprovider.LoadProvider,
		Warn: func(msg string) {
			fmt.Fprintf(os.Stderr, "Warning: %s\n", msg)
		},
	}
}

// Check reports whether tfType can be imported. providerAddr is the full
// Terraform provider source address (e.g. "registry.terraform.io/hashicorp/aws").
func (p *Prober) Check(ctx context.Context, providerAddr, tfType string) Support {
	key := providerAddr + "|" + tfType

	p.mu.Lock()
	defer p.mu.Unlock()

	if verdict, ok := p.verdicts[key]; ok {
		return verdict
	}

	verdict := p.check(ctx, providerAddr, tfType)
	p.verdicts[key] = verdict
	return verdict
}

// check performs the probe. The caller holds p.mu.
func (p *Prober) check(ctx context.Context, providerAddr, tfType string) Support {
	provider, ok := p.provider(ctx, providerAddr)
	if !ok {
		return p.fallback(tfType)
	}

	resp := provider.ImportResourceState(ctx, providers.ImportResourceStateRequest{
		TypeName: tfType,
		ID:       probeID,
	})

	err := resp.Diagnostics.Err()
	verdict := Classify(err)
	if verdict == Unknown {
		// The provider never answered — most likely the plugin died. Every
		// later probe would fail the same way, so stop using this provider
		// and answer from the curated list instead of reporting nothing.
		p.Warn(fmt.Sprintf("Terraform provider %s stopped responding while checking import support "+
			"for %s (%v); falling back to the curated list of non-importable types", providerAddr, tfType, err))
		p.discard(ctx, providerAddr)
		return p.fallback(tfType)
	}
	return verdict
}

// discard shuts down a provider and marks it unusable. The caller holds p.mu.
func (p *Prober) discard(ctx context.Context, providerAddr string) {
	if provider, ok := p.providers[providerAddr]; ok {
		_ = provider.Close(ctx)
		delete(p.providers, providerAddr)
	}
	p.failed[providerAddr] = true
}

// provider returns a running provider, loading it on first use. The caller
// holds p.mu.
func (p *Prober) provider(ctx context.Context, providerAddr string) (tfprovider.Provider, bool) {
	if provider, ok := p.providers[providerAddr]; ok {
		return provider, true
	}
	if p.failed[providerAddr] {
		return nil, false
	}

	version := p.versions[providerAddr]
	if version == "" {
		p.failed[providerAddr] = true
		p.Warn(fmt.Sprintf("no locked version for provider %s, cannot check which of its resource types "+
			"support import; run \"terraform init\" so .terraform.lock.hcl is present", providerAddr))
		return nil, false
	}

	provider, err := p.loadProvider(ctx, providerAddr, version)
	if err != nil {
		p.failed[providerAddr] = true
		p.Warn(fmt.Sprintf("could not load Terraform provider %s %s to check import support: %v",
			providerAddr, version, err))
		return nil, false
	}

	p.providers[providerAddr] = provider
	return provider, true
}

// fallback consults the curated list. Types it does not cover are Unknown:
// guessing "importable" would re-create the failure this package exists to
// prevent, and guessing "non-importable" would divert healthy resources.
func (p *Prober) fallback(tfType string) Support {
	if fallbackTypes[tfType] {
		return Unsupported
	}
	return Unknown
}

// Close shuts down every provider the Prober started. Verdicts already
// gathered remain available.
func (p *Prober) Close(ctx context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for addr, provider := range p.providers {
		if err := provider.Close(ctx); err != nil {
			p.Warn(fmt.Sprintf("closing Terraform provider %s: %v", addr, err))
		}
		delete(p.providers, addr)
		// A provider that has been shut down must not be reused; a later
		// Check for an unmemoized type falls back rather than crashing.
		p.failed[addr] = true
	}
}
