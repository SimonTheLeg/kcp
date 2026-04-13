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
	"bytes"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kcp-dev/kcp/test/load/pkg/stats"
)

func TestMemoryDropAndResults(t *testing.T) {
	m := &Memory{
		Stats: []stats.NamedStat{stats.Avg(), stats.P99()},
	}

	m.Drop(Measurement{Name: "duration_ms", Value: 10})
	m.Drop(Measurement{Name: "duration_ms", Value: 20})
	m.Drop(Measurement{Name: "duration_ms", Value: 30})

	results := m.Results()

	require.InDelta(t, 20.0, results["avg_duration_ms"], 0.01)
	require.GreaterOrEqual(t, results["p99_duration_ms"], 25.0)
}

func TestMemoryResultsEmpty(t *testing.T) {
	m := &Memory{
		Stats: []stats.NamedStat{stats.Avg()},
	}

	results := m.Results()
	require.Empty(t, results)
}

func TestMemoryMultipleMeasurements(t *testing.T) {
	m := &Memory{
		Stats: []stats.NamedStat{stats.Avg()},
	}

	m.Drop(Measurement{Name: "latency", Value: 100})
	m.Drop(Measurement{Name: "latency", Value: 200})
	m.Drop(Measurement{Name: "throughput", Value: 50})

	results := m.Results()

	require.InDelta(t, 150.0, results["avg_latency"], 0.01)
	require.InDelta(t, 50.0, results["avg_throughput"], 0.01)
}

func TestRecordElapsedDurationMS(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := &Memory{
			Stats: []stats.NamedStat{stats.Avg()},
		}

		start := time.Now()
		time.Sleep(100 * time.Millisecond)
		RecordElapsedDurationMS(start, m)

		results := m.Results()
		require.InDelta(t, 100.0, results["avg_duration_ms"], 0.01)
	})
}

func TestSectionStartEnd(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &Section{
			Sink: &Memory{Stats: []stats.NamedStat{stats.Avg()}},
		}
		s.Start()
		time.Sleep(200 * time.Millisecond)
		s.End()

		require.Equal(t, 200*time.Millisecond, s.TotalDuration)
	})
}

func TestReportPrettyPrint(t *testing.T) {
	m := &Memory{
		Stats: []stats.NamedStat{stats.Avg()},
	}
	m.Drop(Measurement{Name: "duration_ms", Value: 42})

	report := &Report{
		Sections: []Section{
			{
				Title: "Test Section",
				Parameters: []Parameter{
					{Key: "Count", Value: "10"},
				},
				TotalDuration: 5 * time.Second,
				Sink:          m,
			},
		},
	}

	var buf bytes.Buffer
	report.PrettyPrint(&buf)
	output := buf.String()

	require.Contains(t, output, "=== Test Section ===")
	require.Contains(t, output, "Count:")
	require.Contains(t, output, "10")
	require.Contains(t, output, "Total Duration:")
	require.Contains(t, output, "5s")
	require.Contains(t, output, "avg_duration_ms")
}

func TestReportPrettyPrintWithErrors(t *testing.T) {
	m := &Memory{
		Stats: []stats.NamedStat{stats.Avg()},
	}

	report := &Report{
		Sections: []Section{
			{
				Title:  "Failing Section",
				Errors: []error{errors.New("something went wrong")},
				Sink:   m,
			},
		},
	}

	var buf bytes.Buffer
	report.PrettyPrint(&buf)
	output := buf.String()

	require.Contains(t, output, "Errors: 1")
	require.Contains(t, output, "something went wrong")
}
