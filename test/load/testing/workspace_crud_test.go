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
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1kcp "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"

	"github.com/kcp-dev/kcp/test/load/pkg/framework"
	"github.com/kcp-dev/kcp/test/load/pkg/measurement"
	"github.com/kcp-dev/kcp/test/load/pkg/stats"
	"github.com/kcp-dev/kcp/test/load/pkg/tuningset"
)

const workspaceCount = 1000
const WorkspaceNamePrefix = "loadtest-ws-"

// workspaceName returns the predictable name for a workspace at the given
// sequence number.
func workspaceName(seq int) string {
	return fmt.Sprintf("%s%d", WorkspaceNamePrefix, seq)
}

// workspaceClusterPath returns the logical cluster path for a workspace
// at the given sequence number (e.g. "root:loadtest-ws-0").
func workspaceClusterPath(seq int) logicalcluster.Path {
	return core.RootCluster.Path().Join(workspaceName(seq))
}

// workspacesExist checks whether count workspaces already exist by
// verifying that the last workspace name is present. This is a cheap heuristic
// that avoids listing all workspaces.
func workspacesExist(client kcpclientset.ClusterInterface, count int) bool {
	lastName := workspaceName(count - 1)
	_, err := client.Cluster(core.RootCluster.Path()).TenancyV1alpha1().Workspaces().Get(context.Background(), lastName, metav1.GetOptions{})
	return err == nil
}

// createWorkspaces creates workspaceCount workspaces under the root workspace
// and waits for each to become Ready.
func createWorkspaces(t *testing.T, client kcpclientset.ClusterInterface, qps float64) measurement.Section {
	t.Helper()

	section := measurement.Section{
		Title: "Workspace Creation",
		Parameters: []measurement.Parameter{
			{Key: "Workspaces", Value: fmt.Sprintf("%d", workspaceCount)},
			{Key: "QPS", Value: fmt.Sprintf("%.0f", qps)},
		},
		Sink: &measurement.Memory{
			Stats: []stats.NamedStat{stats.P99(), stats.Avg()},
		},
	}

	wsClient := client.Cluster(core.RootCluster.Path()).TenancyV1alpha1().Workspaces()

	ts := tuningset.NewUniformQPS(qps, workspaceCount, 0)
	action := func(seq int, s measurement.Sink) error {
		defer measurement.RecordElapsedDurationMS(time.Now(), s)

		ctx := context.Background()
		wsName := workspaceName(seq)

		ws := &tenancyv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{
				Name: wsName,
			},
		}

		_, err := wsClient.Create(ctx, ws, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("create workspace %s: %w", wsName, err)
		}

		// Poll until the workspace reaches the Ready phase.
		err = wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
			got, err := wsClient.Get(ctx, wsName, metav1.GetOptions{})
			if err != nil {
				// on errors we want to retry
				return false, nil //nolint:nilerr
			}
			return got.Status.Phase == corev1alpha1kcp.LogicalClusterPhaseReady, nil
		})
		if err != nil {
			return fmt.Errorf("workspace %s did not become ready: %w", wsName, err)
		}

		return nil
	}

	errs := framework.Execute(ts, action, section.Sink)
	require.Empty(t, errs, "workspace creation phase encountered errors", errs)

	return section
}

// crudConfigMaps performs a Create/Update/Delete cycle for a ConfigMap in each
// of the workspaces.
func crudConfigMaps(t *testing.T, kubeClusterClient kcpkubernetesclientset.ClusterInterface, qps float64) measurement.Section {
	t.Helper()

	section := measurement.Section{
		Title: "ConfigMap CRUD",
		Parameters: []measurement.Parameter{
			{Key: "Workspaces", Value: fmt.Sprintf("%d", workspaceCount)},
			{Key: "QPS", Value: fmt.Sprintf("%.0f", qps)},
		},
		Sink: &measurement.Memory{
			Stats: []stats.NamedStat{stats.P99(), stats.Avg()},
		},
	}

	ts := tuningset.NewUniformQPS(qps, workspaceCount, 0)
	action := func(seq int, s measurement.Sink) error {
		cmClient := kubeClusterClient.Cluster(workspaceClusterPath(seq)).CoreV1().ConfigMaps("default")

		defer measurement.RecordElapsedDurationMS(time.Now(), s)

		ctx := context.Background()
		cmName := fmt.Sprintf("loadtest-cm-%d", seq)

		// --- Create ---
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cmName,
				Namespace: "default",
			},
			Data: map[string]string{
				"key1": "value1",
				"key2": "value2",
			},
		}

		created, err := cmClient.Create(ctx, cm, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("create configmap in ws %d: %w", seq, err)
		}

		// --- Update ---
		created.Data = map[string]string{
			"key1": "updated-value1",
			"key2": "updated-value2",
			"key3": "new-value3",
		}
		_, err = cmClient.Update(ctx, created, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("update configmap in ws %d: %w", seq, err)
		}

		// --- Delete ---
		err = cmClient.Delete(ctx, cmName, metav1.DeleteOptions{})
		if err != nil {
			return fmt.Errorf("delete configmap in ws %d: %w", seq, err)
		}

		return nil
	}

	errs := framework.Execute(ts, action, section.Sink)
	require.Empty(t, errs, "CRUD phase encountered errors")

	return section
}

func TestWorkspaceCRUD(t *testing.T) {
	// TODO remove this later
	t.Setenv(string(framework.KCPFrontProxyKubeconfig), "/Users/simonbein/code/github/simontheleg/kcp/test/load/setup/admin.kubeconfig")

	cfg := framework.Require(t, framework.KCPFrontProxyKubeconfig)

	client, err := kcpclientset.NewForConfig(cfg.FrontProxyKubeconfig)
	require.NoError(t, err)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg.FrontProxyKubeconfig)
	require.NoError(t, err)

	// Clean up workspaces when the test finishes
	// TODO fix cleanup later
	// t.Cleanup(func() {
	// 	wsClient := client.Cluster(core.RootCluster.Path()).TenancyV1alpha1().Workspaces()
	// 	for i := range workspaceCount {
	// 		wsName := workspaceName(i)
	// 		if err = wsClient.Delete(context.Background(), wsName, metav1.DeleteOptions{}); err != nil {
	// 			t.Logf("failed to delete workspace %s. Please clean up manually!: %v", wsName, err)
	// 		}
	// 	}
	// })

	createWorkspaceQPS := 5.0
	crudConfigMapQPS := 10.0

	var sections []measurement.Section

	t.Logf("Creating required workspaces")
	if workspacesExist(client, workspaceCount) {
		t.Logf("workspaces already exist, skipping creation")
	} else {
		createSection := createWorkspaces(t, client, createWorkspaceQPS)
		sections = append(sections, createSection)
	}

	t.Logf("Running configmap CRUD operations")
	crudSection := crudConfigMaps(t, kubeClusterClient, crudConfigMapQPS)
	sections = append(sections, crudSection)

	report := &measurement.Report{
		Sections: sections,
	}
	report.PrettyPrint(os.Stdout)
}
