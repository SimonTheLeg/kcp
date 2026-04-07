/*
Copyright 2026 The kcp Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package measurement

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
)

// Parameter is an ordered key-value pair used to describe test metadata
// (e.g. workspace count, target QPS) in a section header.
type Parameter struct {
	Key   string
	Value string
}

// Section pairs a human-readable title with the Sink that collected
// measurements for that phase of the load test.
type Section struct {
	Title      string
	Parameters []Parameter
	Sink       Sink
}

// Report aggregates multiple measurement sections and can pretty-print
// them as a single formatted table.
type Report struct {
	Sections []Section
}

// PrettyPrint writes all sections to w as a formatted table.
// Each section is printed with its title as a header followed by the
// its parameters andkey/value results from the associated Sink.
func (r *Report) PrettyPrint(w io.Writer) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)

	for i, sec := range r.Sections {
		// separate subsequents sections with a blank line for readability
		if i > 0 {
			fmt.Fprintln(tw)
		}

		fmt.Fprintf(tw, "=== %s ===\n", sec.Title)
		for _, p := range sec.Parameters {
			fmt.Fprintf(tw, "  %s:\t%s\n", p.Key, p.Value)
		}
		fmt.Fprintf(tw, "Metric\tValue\n")
		fmt.Fprintf(tw, "------\t-----\n")

		results := sec.Sink.Results()

		// Sort keys for deterministic output.
		keys := make([]string, 0, len(results))
		for k := range results {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		for _, k := range keys {
			fmt.Fprintf(tw, "%s\t%.0f\n", k, results[k])
		}

		// Separator between sections.
		fmt.Fprintf(tw, "%s\n", strings.Repeat("-", 40))
	}

	tw.Flush()
}
