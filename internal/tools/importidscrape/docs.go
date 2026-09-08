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

package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type DocEntry struct {
	TFType    string
	Example   string
	Source    string
	Divergent bool
}

var (
	importLineRe = regexp.MustCompile(`terraform import\s+(aws_[a-z0-9_]+)\.\S+\s+(\S+)`)
	importIDRe   = regexp.MustCompile(`^\s*id\s*=\s*"([^"]*)"`)
	importToRe   = regexp.MustCompile(`^\s*to\s*=\s*(aws_[a-z0-9_]+)\.`)
	usingAttrRe  = regexp.MustCompile("using (?:the|its) `([a-z0-9_]+)`")
	separatorRe  = regexp.MustCompile(`[^/:,|_]+[/:,|_][^/:,|_]+`)
)

// collectDocs extracts the import example under each resource doc's
// "## Import" heading. The `terraform import` line wins; the `import {}`
// block's id is the fallback.
func collectDocs(providerRoot string) (map[string]DocEntry, error) {
	dir := filepath.Join(providerRoot, "website", "docs", "r")
	files, err := filepath.Glob(filepath.Join(dir, "*.markdown"))
	if err != nil {
		return nil, err
	}
	out := map[string]DocEntry{}
	for _, path := range files {
		e, ok, err := docEntryFromFile(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if ok {
			e.Source = "terraform-provider-aws/website/docs/r/" + filepath.Base(path)
			out[e.TFType] = e
		}
	}
	return out, nil
}

func docEntryFromFile(path string) (DocEntry, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return DocEntry{}, false, err
	}
	defer f.Close()

	var e DocEntry
	inImport, usesAttr := false, false
	var blockType, blockID string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "## ") {
			inImport = strings.TrimSpace(line) == "## Import"
			continue
		}
		if !inImport {
			continue
		}
		if m := importLineRe.FindStringSubmatch(line); m != nil && e.Example == "" {
			e.TFType, e.Example = m[1], m[2]
		}
		if m := importToRe.FindStringSubmatch(line); m != nil {
			blockType = m[1]
		}
		if m := importIDRe.FindStringSubmatch(line); m != nil && blockID == "" {
			blockID = m[1]
		}
		if usingAttrRe.MatchString(line) {
			usesAttr = true
		}
	}
	if err := sc.Err(); err != nil {
		return DocEntry{}, false, err
	}
	if e.Example == "" && blockID != "" {
		e.TFType, e.Example = blockType, blockID
	}
	if e.TFType == "" {
		return DocEntry{}, false, nil
	}
	e.Divergent = usesAttr || separatorRe.MatchString(e.Example)
	return e, true, nil
}
