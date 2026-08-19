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

package cmd

import (
	"github.com/pulumi-proserv/pulumi-tool-import/pkg/version"
	"github.com/spf13/cobra"
)

// newVersionCmd builds the `version` command. Release builds stamp
// pkg/version.Version through ldflags; this is what makes that value
// reachable, so an installed plugin can be identified without inspecting the
// binary.
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the pulumi-tool-import version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.Println(version.Version)
			return nil
		},
	}
}

func init() {
	rootCmd.AddCommand(newVersionCmd())
}
