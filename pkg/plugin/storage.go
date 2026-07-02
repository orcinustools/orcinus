package plugin

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const storageNS = "orcinus-storage"

// spreadAcrossNodes prefers scheduling pods (matching labels) onto distinct
// nodes — soft so it still schedules on a single-node cluster.
func spreadAcrossNodes(labels map[string]string) *corev1.Affinity {
	return &corev1.Affinity{
		PodAntiAffinity: &corev1.PodAntiAffinity{
			PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{
				Weight: 100,
				PodAffinityTerm: corev1.PodAffinityTerm{
					TopologyKey:   "kubernetes.io/hostname",
					LabelSelector: &metav1.LabelSelector{MatchLabels: labels},
				},
			}},
		},
	}
}

// topologySpread evens pods across nodes (soft), complementing anti-affinity.
func topologySpread(labels map[string]string) []corev1.TopologySpreadConstraint {
	return []corev1.TopologySpreadConstraint{{
		MaxSkew:           1,
		TopologyKey:       "kubernetes.io/hostname",
		WhenUnsatisfiable: corev1.ScheduleAnyway,
		LabelSelector:     &metav1.LabelSelector{MatchLabels: labels},
	}}
}

// buildStorage resolves the storage plugin for the requested provider.
func buildStorage(o Options) (built, error) {
	switch o.Provider {
	case "", "local-path":
		// Shipped with the cluster as the default StorageClass — nothing to do.
		return built{}, nil
	case "longhorn":
		b := built{Manifests: []string{
			"https://raw.githubusercontent.com/longhorn/longhorn/v1.7.2/deploy/longhorn.yaml",
		}}
		// Custom StorageClass with an explicit replica count (the default
		// "longhorn" class is immutable, so we add "longhorn-ha").
		if o.Replicas > 0 {
			b.Objects = []runtime.Object{longhornStorageClass(o.Replicas)}
		}
		return b, nil
	case "nfs":
		if o.NFSServer == "" || o.NFSPath == "" {
			return built{}, fmt.Errorf("nfs provider needs --nfs-server and --nfs-path")
		}
		return built{Objects: nfsObjects(o)}, nil
	case "minio":
		b := built{Objects: minioObjects(o)}
		if o.Replicas <= 1 {
			b.WaitFor = []WaitTarget{{Namespace: storageNS, Name: "minio"}} // standalone Deployment
		}
		return b, nil
	case "rook-ceph":
		return built{
			Manifests: []string{
				"https://raw.githubusercontent.com/rook/rook/v1.15.4/deploy/examples/crds.yaml",
				"https://raw.githubusercontent.com/rook/rook/v1.15.4/deploy/examples/common.yaml",
				"https://raw.githubusercontent.com/rook/rook/v1.15.4/deploy/examples/operator.yaml",
			},
			WaitFor:     []WaitTarget{{Namespace: "rook-ceph", Name: "rook-ceph-operator"}},
			PostObjects: cephObjects(o), // need CRDs from crds.yaml
		}, nil
	default:
		return built{}, fmt.Errorf("unknown storage provider %q (want: local-path|longhorn|nfs|minio|rook-ceph)", o.Provider)
	}
}

// longhornStorageClass returns a Longhorn StorageClass with an explicit replica
// count (data mirrored across that many nodes).
func longhornStorageClass(replicas int) runtime.Object {
	expand := true
	return &storagev1.StorageClass{
		TypeMeta:             metav1.TypeMeta{APIVersion: "storage.k8s.io/v1", Kind: "StorageClass"},
		ObjectMeta:           metav1.ObjectMeta{Name: "longhorn-ha"},
		Provisioner:          "driver.longhorn.io",
		AllowVolumeExpansion: &expand,
		Parameters: map[string]string{
			"numberOfReplicas":    fmt.Sprintf("%d", replicas),
			"staleReplicaTimeout": "30",
			"fromBackup":          "",
		},
	}
}

// cephObjects builds a configurable Rook CephCluster + a replicated CephBlockPool
// and a StorageClass ("ceph-block") from the given options.
func cephObjects(o Options) []runtime.Object {
	// Device selection: a filter narrows disks; otherwise use all devices.
	storage := map[string]interface{}{"useAllNodes": true}
	if o.CephDeviceFilter != "" {
		storage["useAllDevices"] = false
		storage["deviceFilter"] = o.CephDeviceFilter
	} else {
		storage["useAllDevices"] = true
	}

	cluster := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "ceph.rook.io/v1",
		"kind":       "CephCluster",
		"metadata":   map[string]interface{}{"name": "rook-ceph", "namespace": "rook-ceph"},
		"spec": map[string]interface{}{
			"cephVersion":     map[string]interface{}{"image": "quay.io/ceph/ceph:v18.2.4"},
			"dataDirHostPath": "/var/lib/rook",
			"mon":             map[string]interface{}{"count": int64(3), "allowMultiplePerNode": false},
			"mgr":             map[string]interface{}{"count": int64(2)},
			"dashboard":       map[string]interface{}{"enabled": true},
			"storage":         storage,
		},
	}}

	size := int64(o.Replicas)
	if size < 1 {
		size = 3
	}
	failureDomain := o.CephFailureDomain
	if failureDomain == "" {
		failureDomain = "host"
	}
	pool := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "ceph.rook.io/v1",
		"kind":       "CephBlockPool",
		"metadata":   map[string]interface{}{"name": "replicapool", "namespace": "rook-ceph"},
		"spec": map[string]interface{}{
			"failureDomain": failureDomain,
			"replicated":    map[string]interface{}{"size": size},
		},
	}}

	expand := true
	sc := &storagev1.StorageClass{
		TypeMeta:             metav1.TypeMeta{APIVersion: "storage.k8s.io/v1", Kind: "StorageClass"},
		ObjectMeta:           metav1.ObjectMeta{Name: "ceph-block"},
		Provisioner:          "rook-ceph.rbd.csi.ceph.com",
		AllowVolumeExpansion: &expand,
		Parameters: map[string]string{
			"clusterID":     "rook-ceph",
			"pool":          "replicapool",
			"imageFormat":   "2",
			"imageFeatures": "layering",
			"csi.storage.k8s.io/provisioner-secret-name":       "rook-csi-rbd-provisioner",
			"csi.storage.k8s.io/provisioner-secret-namespace":  "rook-ceph",
			"csi.storage.k8s.io/controller-expand-secret-name": "rook-csi-rbd-provisioner",
			"csi.storage.k8s.io/controller-expand-secret-namespace": "rook-ceph",
			"csi.storage.k8s.io/node-stage-secret-name":             "rook-csi-rbd-node",
			"csi.storage.k8s.io/node-stage-secret-namespace":        "rook-ceph",
			"csi.storage.k8s.io/fstype":                             "ext4",
		},
	}
	return []runtime.Object{cluster, pool, sc}
}

func ns(name string) *corev1.Namespace {
	return &corev1.Namespace{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"},
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}
}

// nfsObjects builds the nfs-subdir-external-provisioner + a StorageClass "nfs".
func nfsObjects(o Options) []runtime.Object {
	const app = "nfs-provisioner"
	const provisioner = "orcinus.io/nfs"
	labels := map[string]string{"app": app}
	replicas := int32(1)

	sa := &corev1.ServiceAccount{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ServiceAccount"},
		ObjectMeta: metav1.ObjectMeta{Name: app, Namespace: storageNS},
	}
	clusterRole := &rbacv1.ClusterRole{
		TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "ClusterRole"},
		ObjectMeta: metav1.ObjectMeta{Name: app + "-runner"},
		Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{""}, Resources: []string{"nodes"}, Verbs: []string{"get", "list", "watch"}},
			{APIGroups: []string{""}, Resources: []string{"persistentvolumes"}, Verbs: []string{"get", "list", "watch", "create", "delete"}},
			{APIGroups: []string{""}, Resources: []string{"persistentvolumeclaims"}, Verbs: []string{"get", "list", "watch", "update"}},
			{APIGroups: []string{"storage.k8s.io"}, Resources: []string{"storageclasses"}, Verbs: []string{"get", "list", "watch"}},
			{APIGroups: []string{""}, Resources: []string{"events"}, Verbs: []string{"create", "update", "patch"}},
		},
	}
	crb := &rbacv1.ClusterRoleBinding{
		TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "ClusterRoleBinding"},
		ObjectMeta: metav1.ObjectMeta{Name: app + "-runner"},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: app, Namespace: storageNS}},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: app + "-runner"},
	}
	role := &rbacv1.Role{
		TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "Role"},
		ObjectMeta: metav1.ObjectMeta{Name: app + "-leader", Namespace: storageNS},
		Rules:      []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"endpoints"}, Verbs: []string{"get", "list", "watch", "create", "update", "patch"}}},
	}
	rb := &rbacv1.RoleBinding{
		TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "RoleBinding"},
		ObjectMeta: metav1.ObjectMeta{Name: app + "-leader", Namespace: storageNS},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: app, Namespace: storageNS}},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: app + "-leader"},
	}
	dep := &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{Name: app, Namespace: storageNS, Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					ServiceAccountName: app,
					Containers: []corev1.Container{{
						Name:  app,
						Image: "registry.k8s.io/sig-storage/nfs-subdir-external-provisioner:v4.0.2",
						VolumeMounts: []corev1.VolumeMount{{Name: "nfs-root", MountPath: "/persistentvolumes"}},
						Env: []corev1.EnvVar{
							{Name: "PROVISIONER_NAME", Value: provisioner},
							{Name: "NFS_SERVER", Value: o.NFSServer},
							{Name: "NFS_PATH", Value: o.NFSPath},
						},
					}},
					Volumes: []corev1.Volume{{
						Name:         "nfs-root",
						VolumeSource: corev1.VolumeSource{NFS: &corev1.NFSVolumeSource{Server: o.NFSServer, Path: o.NFSPath}},
					}},
				},
			},
		},
	}
	archiveOnDelete := "false"
	sc := &storagev1.StorageClass{
		TypeMeta:    metav1.TypeMeta{APIVersion: "storage.k8s.io/v1", Kind: "StorageClass"},
		ObjectMeta:  metav1.ObjectMeta{Name: "nfs"},
		Provisioner: provisioner,
		Parameters:  map[string]string{"archiveOnDelete": archiveOnDelete},
	}
	return []runtime.Object{ns(storageNS), sa, clusterRole, crb, role, rb, dep, sc}
}

// minioObjects builds MinIO: standalone (replicas<=1) or distributed/HA (>=2).
func minioObjects(o Options) []runtime.Object {
	if o.Replicas >= 2 {
		return minioDistributed(o)
	}
	return minioStandalone(o)
}

func minioSize(o Options) string {
	if o.Size == "" {
		return "10Gi"
	}
	return o.Size
}

func minioSecret() *corev1.Secret {
	return &corev1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: "minio-creds", Namespace: storageNS},
		Type:       corev1.SecretTypeOpaque,
		StringData: map[string]string{"MINIO_ROOT_USER": "minioadmin", "MINIO_ROOT_PASSWORD": "minioadmin"},
	}
}

// minioStandalone builds a single-instance MinIO (S3-compatible object storage).
func minioStandalone(o Options) []runtime.Object {
	const app = "minio"
	labels := map[string]string{"app": app}
	size := minioSize(o)
	replicas := int32(1)

	secret := minioSecret()
	pvc := &corev1.PersistentVolumeClaim{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "PersistentVolumeClaim"},
		ObjectMeta: metav1.ObjectMeta{Name: "minio-data", Namespace: storageNS},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(size)}},
		},
	}
	dep := &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{Name: app, Namespace: storageNS, Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:    app,
						Image:   "minio/minio:RELEASE.2024-09-22T00-33-43Z",
						Args:    []string{"server", "/data", "--console-address", ":9001"},
						EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "minio-creds"}}}},
						Ports: []corev1.ContainerPort{
							{Name: "api", ContainerPort: 9000},
							{Name: "console", ContainerPort: 9001},
						},
						VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data"}},
					}},
					Volumes: []corev1.Volume{{
						Name:         "data",
						VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "minio-data"}},
					}},
				},
			},
		},
	}
	svc := &corev1.Service{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: metav1.ObjectMeta{Name: app, Namespace: storageNS, Labels: labels},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports: []corev1.ServicePort{
				{Name: "api", Port: 9000, TargetPort: intstr.FromInt(9000)},
				{Name: "console", Port: 9001, TargetPort: intstr.FromInt(9001)},
			},
		},
	}
	return []runtime.Object{ns(storageNS), secret, pvc, dep, svc}
}

// minioDistributed builds an HA MinIO StatefulSet (erasure-coded across N pods,
// each with its own PVC). For real fault tolerance spread pods across nodes.
func minioDistributed(o Options) []runtime.Object {
	const app = "minio"
	labels := map[string]string{"app": app}
	size := minioSize(o)
	replicas := int32(o.Replicas)

	// Peer endpoints via the headless Service, using MinIO's ellipsis syntax.
	endpoints := fmt.Sprintf("http://minio-{0...%d}.minio-hl.%s.svc.cluster.local/data",
		o.Replicas-1, storageNS)

	headless := &corev1.Service{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: metav1.ObjectMeta{Name: "minio-hl", Namespace: storageNS, Labels: labels},
		Spec: corev1.ServiceSpec{
			ClusterIP:                "None",
			PublishNotReadyAddresses: true,
			Selector:                 labels,
			Ports:                    []corev1.ServicePort{{Name: "api", Port: 9000, TargetPort: intstr.FromInt(9000)}},
		},
	}
	client := &corev1.Service{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: metav1.ObjectMeta{Name: app, Namespace: storageNS, Labels: labels},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports: []corev1.ServicePort{
				{Name: "api", Port: 9000, TargetPort: intstr.FromInt(9000)},
				{Name: "console", Port: 9001, TargetPort: intstr.FromInt(9001)},
			},
		},
	}
	sts := &appsv1.StatefulSet{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "StatefulSet"},
		ObjectMeta: metav1.ObjectMeta{Name: app, Namespace: storageNS, Labels: labels},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: "minio-hl",
			Replicas:    &replicas,
			Selector:    &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Affinity:                  spreadAcrossNodes(labels),
					TopologySpreadConstraints: topologySpread(labels),
					Containers: []corev1.Container{{
						Name:    app,
						Image:   "minio/minio:RELEASE.2024-09-22T00-33-43Z",
						Args:    []string{"server", "--console-address", ":9001", endpoints},
						EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "minio-creds"}}}},
						Ports: []corev1.ContainerPort{
							{Name: "api", ContainerPort: 9000},
							{Name: "console", ContainerPort: 9001},
						},
						VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data"}},
					}},
				},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{
				ObjectMeta: metav1.ObjectMeta{Name: "data"},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(size)}},
				},
			}},
		},
	}
	return []runtime.Object{ns(storageNS), minioSecret(), headless, client, sts}
}
