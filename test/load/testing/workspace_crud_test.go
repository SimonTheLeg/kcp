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

package workspace

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/kcp-dev/kcp/test/load/pkg/framework"
	"github.com/kcp-dev/kcp/test/load/pkg/measurement"
	"github.com/kcp-dev/kcp/test/load/pkg/stats"
	"github.com/kcp-dev/kcp/test/load/pkg/tuningset"
)

const workspaceCount = 1000

var (
	// TODO use the builtin GVR
	workspaceGVR = schema.GroupVersionResource{
		Group:    "tenancy.kcp.io",
		Version:  "v1alpha1",
		Resource: "workspaces",
	}
	configMapGVR = schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "configmaps",
	}
)

// configForCluster returns a rest.Config that targets the given kcp logical
// cluster path (e.g. "root" or "root:my-ws"). Any existing /clusters/ segment
// in the host URL is replaced.
// TODO I think this exists also somewhere upstream
func configForCluster(base *rest.Config, clusterPath string) *rest.Config {
	cfg := rest.CopyConfig(base)
	host := strings.TrimSuffix(cfg.Host, "/")
	if idx := strings.Index(host, "/clusters/"); idx != -1 {
		host = host[:idx]
	}
	cfg.Host = host + "/clusters/" + clusterPath
	return cfg
}

// createWorkspaces creates workspaceCount workspaces under the root workspace
// and waits for each to become Ready. It returns the URLs of the ready workspaces.
func createWorkspaces(t *testing.T, rootClient dynamic.Interface, qps float64) []string {
	t.Helper()

	fmt.Println("Phase 1: creating workspaces")

	sink := &measurement.Memory{
		Stats: []stats.NamedStat{stats.P99(), stats.Avg()},
	}

	workspaceURLs := make([]string, workspaceCount)

	ts := tuningset.NewUniformQPS(qps, workspaceCount, 0)
	action := func(seq int, s measurement.Sink) error {
		defer measurement.RecordElapsedDurationMS(time.Now(), s)

		ctx := context.Background()
		wsName := fmt.Sprintf("loadtest-%d", seq)

		ws := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "tenancy.kcp.io/v1alpha1",
				"kind":       "Workspace",
				"metadata": map[string]interface{}{
					"name": wsName,
				},
			},
		}

		_, err := rootClient.Resource(workspaceGVR).Create(ctx, ws, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("create workspace %s: %w", wsName, err)
		}

		// Poll until the workspace reaches the Ready phase.
		// TODO rewrite this using stretchr/testify
		err = wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
			got, err := rootClient.Resource(workspaceGVR).Get(ctx, wsName, metav1.GetOptions{})
			if err != nil {
				// TODO investigate transient error
				return false, nil // transient error, retry
			}
			phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
			if phase == "Ready" {
				url, _, _ := unstructured.NestedString(got.Object, "spec", "URL")
				workspaceURLs[seq] = url
				return true, nil
			}
			return false, nil
		})
		if err != nil {
			return fmt.Errorf("workspace %s did not become ready: %w", wsName, err)
		}

		return nil
	}

	errs := framework.Execute(ts, action, sink)
	require.Empty(t, errs, "workspace creation phase encountered errors", errs)

	fmt.Printf("=== Workspace Creation Results %d workspaces (%.0f qps) ===\n", workspaceCount, qps)
	for k, v := range sink.Results() {
		fmt.Printf("  %s: %.2f ms\n", k, v)
	}

	return workspaceURLs
}

// crudConfigMaps performs a Create/Update/Delete cycle for a ConfigMap in each
// of the workspaces identified by workspaceURLs.
func crudConfigMaps(t *testing.T, baseCfg *rest.Config, workspaceURLs []string, qps float64) {
	t.Helper()

	fmt.Println("Phase 2: CRUD ConfigMaps")

	sink := &measurement.Memory{
		Stats: []stats.NamedStat{stats.P99(), stats.Avg()},
	}

	// Pre-create a dynamic client per workspace so client setup time is
	// not included in the CRUD measurement.
	// TODO change this pre-allocation, I don't think we need to do that, we can just trigger the measurement later
	wsClients := make([]dynamic.Interface, workspaceCount)
	for i := range workspaceCount {
		wsCfg := rest.CopyConfig(baseCfg)
		wsCfg.Host = workspaceURLs[i]
		var err error
		wsClients[i], err = dynamic.NewForConfig(wsCfg)
		require.NoError(t, err)
	}

	ts := tuningset.NewUniformQPS(qps, workspaceCount, 0)
	action := func(seq int, s measurement.Sink) error {
		defer measurement.RecordElapsedDurationMS(time.Now(), s)

		ctx := context.Background()
		client := wsClients[seq]
		cmName := fmt.Sprintf("loadtest-cm-%d", seq)

		// --- Create ---
		cm := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]interface{}{
					"name":      cmName,
					"namespace": "default",
				},
				"data": map[string]interface{}{
					"key1": "value1",
					"key2": "value2",
				},
			},
		}

		created, err := client.Resource(configMapGVR).Namespace("default").Create(ctx, cm, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("create configmap in ws %d: %w", seq, err)
		}

		// --- Update ---
		if err := unstructured.SetNestedStringMap(created.Object, map[string]string{
			"key1": "updated-value1",
			"key2": "updated-value2",
			"key3": "new-value3",
		}, "data"); err != nil {
			return fmt.Errorf("set data for configmap in ws %d: %w", seq, err)
		}
		_, err = client.Resource(configMapGVR).Namespace("default").Update(ctx, created, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("update configmap in ws %d: %w", seq, err)
		}

		// --- Delete ---
		err = client.Resource(configMapGVR).Namespace("default").Delete(ctx, cmName, metav1.DeleteOptions{})
		if err != nil {
			return fmt.Errorf("delete configmap in ws %d: %w", seq, err)
		}

		return nil
	}

	errs := framework.Execute(ts, action, sink)
	require.Empty(t, errs, "CRUD phase encountered errors")

	fmt.Printf("=== ConfigMap CRUD Results %d workspaces (%.0f qps) ===\n", workspaceCount, qps)
	for k, v := range sink.Results() {
		fmt.Printf("  %s: %.2f ms\n", k, v)
	}
}

func TestWorkspaceCRUD(t *testing.T) {
	// TODO remove this later
	t.Setenv(string(framework.KCPFrontProxyKubeconfig), "/Users/simonbein/code/github/simontheleg/kcp/test/load/setup/admin.kubeconfig")

	cfg := framework.Require(t, framework.KCPFrontProxyKubeconfig)

	// Disable client-side rate limiting entirely so the tuning sets control the actual QPS.
	// TODO move this into a central place during setup
	cfg.FrontProxyKubeconfig.QPS = -1

	// Client targeting the root workspace
	rootConfig := configForCluster(cfg.FrontProxyKubeconfig, "root")
	rootClient, err := dynamic.NewForConfig(rootConfig)
	require.NoError(t, err)

	// Clean up workspaces when the test finishes
	// TODO fix cleanup later
	// t.Cleanup(func() {
	// 	for i := range workspaceCount {
	// 		wsName := fmt.Sprintf("loadtest-ws-%d", i)
	// 		if err = rootClient.Resource(workspaceGVR).Delete(context.Background(), wsName, metav1.DeleteOptions{}); err != nil {
	// 			t.Logf("failed to delete workspace %s. Please clean up manually!: %v", wsName, err)
	// 		}
	// 	}
	// })

	createWorkspaceQPS := 5.0
	crudConfigMapQPS := 10.0

	workspaceURLs := createWorkspaces(t, rootClient, createWorkspaceQPS)
	crudConfigMaps(t, cfg.FrontProxyKubeconfig, workspaceURLs, crudConfigMapQPS)
}
