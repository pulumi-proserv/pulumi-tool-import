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

// Regenerates provider-major import-ID catalogs from a
// terraform-provider-aws checkout. Run through "make update-import-id-formats".
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pulumi-proserv/pulumi-tool-import/importids/catalog"
)

func main() {
	version := flag.String("provider-version", "", "terraform-provider-aws tag to clone, e.g. v6.38.0")
	dir := flag.String("provider-dir", "", "existing checkout to scrape instead of cloning")
	label := flag.String("version-label", "", "version recorded in the table when --provider-dir is used")
	out := flag.String("out", "", "output path (defaults to formats.json beside --source)")
	sourcePath := flag.String("source", "", "provider-major source.json generation pin")
	check := flag.Bool("check", false, "compare generated output with --out without writing")
	printHashes := flag.Bool("print-helper-hashes", false, "print modeled helper hashes for review without generating a table")
	flag.Parse()

	var source *catalog.Source
	if *sourcePath != "" {
		if *version != "" || *dir != "" {
			fail(fmt.Errorf("--source cannot be combined with --provider-version or --provider-dir"))
		}
		data, err := os.ReadFile(*sourcePath)
		if err != nil {
			fail(err)
		}
		source = &catalog.Source{}
		if err := json.Unmarshal(data, source); err != nil {
			fail(err)
		}
		if source.Provider != "hashicorp/aws" || source.PulumiVersion == "" || source.UpstreamVersion == "" || len(source.UpstreamRevision) != 40 {
			fail(fmt.Errorf("incomplete or unsupported catalog source pin: %s", *sourcePath))
		}
		*version = source.UpstreamRevision
		if *out == "" {
			*out = filepath.Join(filepath.Dir(*sourcePath), "formats.json")
		}
	}
	if *out == "" && !*printHashes {
		fail(fmt.Errorf("--out is required without --source"))
	}
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
	if *printHashes {
		hashes, err := helperHashes(root)
		if err != nil {
			fail(err)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(hashes); err != nil {
			fail(err)
		}
		return
	}

	var sources []catalog.Source
	if source != nil {
		ver = source.UpstreamVersion
		sources = append(sources, *source)
	}
	f, sum, err := buildFormats(root, ver, func(w string) { fmt.Fprintln(os.Stderr, "WARNING:", w) }, sources...)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *check {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(f); err != nil {
			fail(err)
		}
		data, err := os.ReadFile(*out)
		if err != nil {
			fail(err)
		}
		if !bytes.Equal(data, buf.Bytes()) {
			fail(fmt.Errorf("%s is stale; run make update-import-id-formats", *out))
		}
	} else if err := writeFormats(*out, f); err != nil {
		fail(err)
	}
	fmt.Fprintf(os.Stderr, "%s: %d types — %d template, %d manual (tests), %d manual (docs-only), %d sensitive hits\n",
		*out, len(f.Types), sum.Templates, sum.ManualFromTests, sum.ManualFromDocs, sum.SensitiveHits)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

// ensureCheckout sparse-clones the provider at tag into the user cache and
// returns the path, reusing an existing clone of the same tag.
func ensureCheckout(tag string) (string, error) {
	if strings.ContainsAny(tag, `/\\`) || tag == "" || strings.HasPrefix(tag, "-") {
		return "", fmt.Errorf("invalid provider source ref %q", tag)
	}
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
		return dest, verifyCheckout(dest, tag)
	}
	_ = os.RemoveAll(dest)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	steps := [][]string{
		{"git", "init", "--quiet", dest},
		{"git", "-C", dest, "remote", "add", "origin", "https://github.com/hashicorp/terraform-provider-aws.git"},
		{"git", "-C", dest, "fetch", "--quiet", "--depth", "1", "--filter=blob:none", "origin", tag},
		// Include helper implementations so their hashes can be verified.
		{"git", "-C", dest, "sparse-checkout", "set", "internal/service", "internal/acctest", "website/docs/r", "names"},
		{"git", "-C", dest, "checkout", "--quiet", "--detach", "FETCH_HEAD"},
	}
	for _, args := range steps {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("%v: %w", args, err)
		}
	}
	return dest, verifyCheckout(dest, tag)
}

func verifyCheckout(dir, ref string) error {
	head, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return fmt.Errorf("reading cached provider revision: %w", err)
	}
	want, err := exec.Command("git", "-C", dir, "rev-parse", "--verify", ref+"^{commit}").Output()
	if err != nil {
		return fmt.Errorf("resolving cached provider ref %s: %w", ref, err)
	}
	if !bytes.Equal(head, want) {
		return fmt.Errorf("cached provider %s is not at %s", dir, ref)
	}
	dirty, err := exec.Command("git", "-C", dir, "status", "--porcelain").Output()
	if err != nil {
		return fmt.Errorf("checking cached provider worktree: %w", err)
	}
	if len(dirty) != 0 {
		return fmt.Errorf("cached provider %s has local changes; generation requires a clean source checkout", dir)
	}
	return nil
}
