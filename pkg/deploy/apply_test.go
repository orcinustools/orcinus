package deploy

import (
	"context"
	"testing"

	"github.com/orcinustools/orcinus/pkg/compose"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestIsStatefulSetClaim(t *testing.T) {
	prefixes := []string{"redisdata-redis-", "data-db-"}
	cases := []struct {
		name string
		want bool
	}{
		{"redisdata-redis-0", true},
		{"redisdata-redis-11", true},
		{"data-db-0", true},
		{"redisdata", false},             // the standalone claim orcinus applies itself
		{"redisdata-redis-", false},      // no ordinal
		{"redisdata-redis-0-bak", false}, // ordinal must be the whole suffix
		{"redisdata-redis-x", false},
		{"other-redis-0", false},
	}
	for _, tc := range cases {
		if got := isStatefulSetClaim(tc.name, prefixes); got != tc.want {
			t.Errorf("isStatefulSetClaim(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
	if isStatefulSetClaim("redisdata-redis-0", nil) {
		t.Error("no prefixes: nothing should be treated as a StatefulSet claim")
	}
}

const (
	testNS      = "default"
	testProject = "office"
)

func ownedLabels() map[string]any {
	return map[string]any{
		compose.LabelManagedBy: compose.ManagedByValue,
		compose.LabelProject:   testProject,
	}
}

func pvcObj(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "PersistentVolumeClaim",
		"metadata": map[string]any{
			"name": name, "namespace": testNS, "labels": ownedLabels(),
		},
	}}
}

func stsObj(name, claimTemplate string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "StatefulSet",
		"metadata": map[string]any{
			"name": name, "namespace": testNS, "labels": ownedLabels(),
		},
		"spec": map[string]any{
			"volumeClaimTemplates": []any{
				map[string]any{"metadata": map[string]any{"name": claimTemplate}},
			},
		},
	}}
}

func newFakeApplier(objs ...runtime.Object) *Applier {
	listKinds := map[schema.GroupVersionResource]string{}
	for _, gvr := range prunableGVRs {
		kind := map[string]string{
			"deployments":            "DeploymentList",
			"statefulsets":           "StatefulSetList",
			"daemonsets":             "DaemonSetList",
			"services":               "ServiceList",
			"configmaps":             "ConfigMapList",
			"secrets":                "SecretList",
			"persistentvolumeclaims": "PersistentVolumeClaimList",
			"ingresses":              "IngressList",
		}[gvr.Resource]
		listKinds[gvr] = kind
	}
	scheme := runtime.NewScheme()
	return &Applier{dyn: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, objs...)}
}

func exists(t *testing.T, a *Applier, gvr schema.GroupVersionResource, name string) bool {
	t.Helper()
	_, err := a.dyn.Resource(gvr).Namespace(testNS).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil && !errors.IsNotFound(err) {
		t.Fatalf("get %s/%s: %v", gvr.Resource, name, err)
	}
	return err == nil
}

// A re-deploy must not prune the PVCs the StatefulSet controller created from
// volumeClaimTemplates: they carry the project labels but are never part of
// the applied set, and deleting one destroys the volume at the next restart.
func TestPruneKeepsStatefulSetClaims(t *testing.T) {
	a := newFakeApplier(
		stsObj("redis", "redisdata"),
		pvcObj("redisdata"),         // the standalone claim orcinus applies
		pvcObj("redisdata-redis-0"), // created by the StatefulSet controller
		pvcObj("leftover"),          // a real orphan from an earlier deploy
	)
	applied := []AppliedRef{
		{GVR: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}, Namespace: testNS, Name: "redis"},
		{GVR: pvcGVR, Namespace: testNS, Name: "redisdata"},
	}

	// PrunePVCs on: even a clean sweep must not touch a StatefulSet's claims.
	opts := ApplyOptions{Project: testProject, PrunePVCs: true}
	if err := a.prune(context.Background(), applied, opts, testNS); err != nil {
		t.Fatalf("prune: %v", err)
	}

	if !exists(t, a, pvcGVR, "redisdata-redis-0") {
		t.Error("StatefulSet-managed PVC was pruned — this is the data-loss bug")
	}
	if !exists(t, a, pvcGVR, "redisdata") {
		t.Error("applied PVC should be kept")
	}
	if exists(t, a, pvcGVR, "leftover") {
		t.Error("orphaned PVC should still be pruned")
	}
}

// Removing a service from the compose file prunes its StatefulSet. The claims
// survive (as they do when kubectl deletes a StatefulSet) so re-adding the
// service recovers the data; `orcinus rm` is the explicit way to drop it.
func TestPruneKeepsClaimsOfPrunedStatefulSet(t *testing.T) {
	a := newFakeApplier(stsObj("redis", "redisdata"), pvcObj("redisdata-redis-0"))
	stsGVR := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}

	opts := ApplyOptions{Project: testProject, PrunePVCs: true}
	if err := a.prune(context.Background(), nil, opts, testNS); err != nil {
		t.Fatalf("prune: %v", err)
	}

	if exists(t, a, stsGVR, "redis") {
		t.Error("StatefulSet no longer in the input should be pruned")
	}
	if !exists(t, a, pvcGVR, "redisdata-redis-0") {
		t.Error("claim was pruned along with its StatefulSet — data would be lost")
	}
}

// By default prune leaves every claim alone, including a plain one whose
// service left the input — but it still prunes everything that is not a claim.
func TestPruneKeepsPVCsByDefault(t *testing.T) {
	svcGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "services"}
	orphanSvc := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Service",
		"metadata": map[string]any{"name": "gone", "namespace": testNS, "labels": ownedLabels()},
	}}
	a := newFakeApplier(pvcObj("dbdata"), orphanSvc)

	if err := a.prune(context.Background(), nil, ApplyOptions{Project: testProject}, testNS); err != nil {
		t.Fatalf("prune: %v", err)
	}

	if !exists(t, a, pvcGVR, "dbdata") {
		t.Error("prune deleted a claim without --prune-pvc")
	}
	if exists(t, a, svcGVR, "gone") {
		t.Error("keeping claims must not stop non-PVC resources from being pruned")
	}
}

// --prune-pvc is the opt-in clean sweep: an orphaned claim does get deleted.
func TestPrunePVCsOptIn(t *testing.T) {
	a := newFakeApplier(pvcObj("dbdata"))

	opts := ApplyOptions{Project: testProject, PrunePVCs: true}
	if err := a.prune(context.Background(), nil, opts, testNS); err != nil {
		t.Fatalf("prune: %v", err)
	}

	if exists(t, a, pvcGVR, "dbdata") {
		t.Error("--prune-pvc should delete an orphaned claim")
	}
}

// Prune stays scoped to a project, and refuses to run without one.
func TestPruneRequiresProject(t *testing.T) {
	a := newFakeApplier(pvcObj("leftover"))
	if err := a.prune(context.Background(), nil, ApplyOptions{}, testNS); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if !exists(t, a, pvcGVR, "leftover") {
		t.Error("prune without a project scope must delete nothing")
	}
}
