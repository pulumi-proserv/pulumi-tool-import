// Copyright 2016-2026, Pulumi Corporation.
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

// Regenerates pkg/importid/aws-import-id-formats.json from a
// terraform-provider-aws checkout. Run through "make update-import-id-formats".
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func main() {
	version := flag.String("provider-version", "", "terraform-provider-aws tag to clone, e.g. v6.38.0")
	dir := flag.String("provider-dir", "", "existing checkout to scrape instead of cloning")
	label := flag.String("version-label", "", "version recorded in the table when --provider-dir is used")
	out := flag.String("out", "pkg/importid/aws-import-id-formats.json", "output path")
	flag.Parse()

	if (*version == "") == (*dir == "") {
		fmt.Fprintln(os.Stderr, "exactly one of --provider-version or --provider-dir is required")
		os.Exit(2)
	}
	root, ver := *dir, *label
	if *version != "" {
		var err error
		root, err = ensureCheckout(*version)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		ver = *version
	}
	if ver == "" {
		fmt.Fprintln(os.Stderr, "--version-label is required with --provider-dir")
		os.Exit(2)
	}

	f, sum, err := buildFormats(root, ver, func(w string) { fmt.Fprintln(os.Stderr, "WARNING:", w) })
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := writeFormats(*out, f); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "%s: %d types — %d template, %d manual (tests), %d manual (docs-only), %d sensitive hits\n",
		*out, len(f.Types), sum.Templates, sum.ManualFromTests, sum.ManualFromDocs, sum.SensitiveHits)
}

// ensureCheckout sparse-clones the provider at tag into the user cache and
// returns the path, reusing an existing clone of the same tag.
func ensureCheckout(tag string) (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dest := filepath.Join(cache, "pulumi-tool-import", "terraform-provider-aws@"+tag)
	// Every sparse path must be probed, not just the first: a cache left by an
	// older sparse set has internal/service but no names/, and reusing it would
	// silently classify every names.Attr* lookup as manual.
	complete := true
	for _, p := range [][]string{{"internal", "service"}, {"internal", "acctest"}, {"website", "docs", "r"}, {"names"}} {
		if _, err := os.Stat(filepath.Join(dest, filepath.Join(p...))); err != nil {
			complete = false
			break
		}
	}
	if complete {
		return dest, nil
	}
	_ = os.RemoveAll(dest)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	steps := [][]string{
		{"git", "clone", "--quiet", "--depth", "1", "--branch", tag, "--filter=blob:none", "--sparse",
			"https://github.com/hashicorp/terraform-provider-aws.git", dest},
		// internal/acctest is not parsed: classify.go hard-codes the import-ID
		// helpers' semantics. It is checked out so a reviewer can diff those
		// against the source at the scraped tag.
		{"git", "-C", dest, "sparse-checkout", "set", "internal/service", "internal/acctest", "website/docs/r", "names"},
	}
	for _, args := range steps {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("%v: %w", args, err)
		}
	}
	return dest, nil
}
