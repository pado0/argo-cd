package admin

import (
	"testing"

	clustermocks "github.com/argoproj/gitops-engine/pkg/cache/mocks"
	"github.com/argoproj/gitops-engine/pkg/health"
	"github.com/argoproj/gitops-engine/pkg/utils/kube"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"

	"github.com/argoproj/argo-cd/v3/common"
	statecache "github.com/argoproj/argo-cd/v3/controller/cache"
	cachemocks "github.com/argoproj/argo-cd/v3/controller/cache/mocks"
	"github.com/argoproj/argo-cd/v3/controller/metrics"
	"github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	appfake "github.com/argoproj/argo-cd/v3/pkg/client/clientset/versioned/fake"
	argocdclient "github.com/argoproj/argo-cd/v3/reposerver/apiclient"
	"github.com/argoproj/argo-cd/v3/reposerver/apiclient/mocks"
	"github.com/argoproj/argo-cd/v3/test"
	"github.com/argoproj/argo-cd/v3/util/argo/normalizers"
	"github.com/argoproj/argo-cd/v3/util/db"
	"github.com/argoproj/argo-cd/v3/util/settings"
)

func TestGetReconcileResults(t *testing.T) {
	ctx := t.Context()

	appClientset := appfake.NewSimpleClientset(&v1alpha1.Application{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "default",
		},
		Status: v1alpha1.ApplicationStatus{
			Health: v1alpha1.AppHealthStatus{Status: health.HealthStatusHealthy},
			Sync:   v1alpha1.SyncStatus{Status: v1alpha1.SyncStatusCodeOutOfSync},
		},
	})

	result, err := getReconcileResults(ctx, appClientset, "default", "")
	require.NoError(t, err)

	expectedResults := []appReconcileResult{{
		Name:   "test",
		Health: health.HealthStatusHealthy,
		Sync:   &v1alpha1.SyncStatus{Status: v1alpha1.SyncStatusCodeOutOfSync},
	}}
	assert.ElementsMatch(t, expectedResults, result)
}

func TestGetReconcileResults_Refresh(t *testing.T) {
	ctx := t.Context()

	argoCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      common.ArgoCDConfigMapName,
			Namespace: "default",
			Labels: map[string]string{
				"app.kubernetes.io/part-of": "argocd",
			},
		},
	}
	argoCDSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      common.ArgoCDSecretName,
			Namespace: "default",
			Labels: map[string]string{
				"app.kubernetes.io/part-of": "argocd",
			},
		},
		Data: map[string][]byte{
			"admin.password":   nil,
			"server.secretkey": nil,
		},
	}
	proj := &v1alpha1.AppProject{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "default",
			Namespace: "default",
		},
		Spec: v1alpha1.AppProjectSpec{Destinations: []v1alpha1.ApplicationDestination{{Namespace: "*", Server: "*"}}},
	}

	app := &v1alpha1.Application{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "default",
		},
		Spec: v1alpha1.ApplicationSpec{
			Source:  &v1alpha1.ApplicationSource{},
			Project: "default",
			Destination: v1alpha1.ApplicationDestination{
				Server:    v1alpha1.KubernetesInternalAPIServerAddr,
				Namespace: "default",
			},
		},
	}

	appClientset := appfake.NewSimpleClientset(app, proj)
	deployment := test.NewDeployment()
	kubeClientset := kubefake.NewClientset(deployment, argoCM, argoCDSecret)
	clusterCache := clustermocks.ClusterCache{}
	clusterCache.On("IsNamespaced", mock.Anything).Return(true, nil)
	clusterCache.On("GetGVKParser", mock.Anything).Return(nil)
	repoServerClient := mocks.RepoServerServiceClient{}
	repoServerClient.On("GenerateManifest", mock.Anything, mock.Anything).Return(&argocdclient.ManifestResponse{
		Manifests: []string{test.DeploymentManifest},
	}, nil)
	repoServerClientset := mocks.Clientset{RepoServerServiceClient: &repoServerClient}
	liveStateCache := cachemocks.LiveStateCache{}
	liveStateCache.On("GetManagedLiveObjs", mock.Anything, mock.Anything, mock.Anything).Return(map[kube.ResourceKey]*unstructured.Unstructured{
		kube.GetResourceKey(deployment): deployment,
	}, nil)
	liveStateCache.On("GetVersionsInfo", mock.Anything).Return("v1.2.3", nil, nil)
	liveStateCache.On("Init").Return(nil, nil)
	liveStateCache.On("GetClusterCache", mock.Anything).Return(&clusterCache, nil)
	liveStateCache.On("IsNamespaced", mock.Anything, mock.Anything).Return(true, nil)

	result, err := reconcileApplications(ctx, kubeClientset, appClientset, "default", &repoServerClientset, "",
		func(_ db.ArgoDB, _ cache.SharedIndexInformer, _ *settings.SettingsManager, _ *metrics.MetricsServer) statecache.LiveStateCache {
			return &liveStateCache
		},
		false,
		normalizers.IgnoreNormalizerOpts{},
	)

	require.NoError(t, err)

	assert.Equal(t, health.HealthStatusMissing, result[0].Health)
	assert.Equal(t, v1alpha1.SyncStatusCodeOutOfSync, result[0].Sync.Status)
}

func TestDiffReconcileResults_NoDifferences(t *testing.T) {
	logs, err := captureStdout(func() {
		require.NoError(t, diffReconcileResults(
			reconcileResults{Applications: []appReconcileResult{{
				Name: "app1",
				Sync: &v1alpha1.SyncStatus{Status: v1alpha1.SyncStatusCodeOutOfSync},
			}}},
			reconcileResults{Applications: []appReconcileResult{{
				Name: "app1",
				Sync: &v1alpha1.SyncStatus{Status: v1alpha1.SyncStatusCodeOutOfSync},
			}}},
		))
	})
	require.NoError(t, err)
	assert.Equal(t, "app1\n", logs)
}

func TestDiffReconcileResults_DifferentApps(t *testing.T) {
	logs, err := captureStdout(func() {
		require.NoError(t, diffReconcileResults(
			reconcileResults{Applications: []appReconcileResult{{
				Name: "app1",
				Sync: &v1alpha1.SyncStatus{Status: v1alpha1.SyncStatusCodeOutOfSync},
			}, {
				Name: "app2",
				Sync: &v1alpha1.SyncStatus{Status: v1alpha1.SyncStatusCodeOutOfSync},
			}}},
			reconcileResults{Applications: []appReconcileResult{{
				Name: "app1",
				Sync: &v1alpha1.SyncStatus{Status: v1alpha1.SyncStatusCodeOutOfSync},
			}, {
				Name: "app3",
				Sync: &v1alpha1.SyncStatus{Status: v1alpha1.SyncStatusCodeOutOfSync},
			}}},
		))
	})
	require.NoError(t, err)
	assert.Equal(t, `app1
app2
1,9d0
< conditions: null
< health: ""
< name: app2
< sync:
<   comparedTo:
<     destination: {}
<     source:
<       repoURL: ""
<   status: OutOfSync
app3
0a1,9
> conditions: null
> health: ""
> name: app3
> sync:
>   comparedTo:
>     destination: {}
>     source:
>       repoURL: ""
>   status: OutOfSync
`, logs)
}
