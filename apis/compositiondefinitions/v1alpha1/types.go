package v1alpha1

import (
	rtv1 "github.com/krateoplatformops/provider-runtime/apis/common/v1"
	"github.com/krateoplatformops/provider-runtime/pkg/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
type CompositionDefinitionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []CompositionDefinition `json:"items"`
}

// GetItems of this CompositionDefinitionList.
func (l *CompositionDefinitionList) GetItems() []resource.Managed {
	items := make([]resource.Managed, len(l.Items))
	for i := range l.Items {
		items[i] = &l.Items[i]
	}
	return items
}

type Credentials struct {
	// Username: username for private repo
	Username string `json:"username"`
	// PasswordRef: reference to secret containing password for private repo
	PasswordRef rtv1.SecretKeySelector `json:"passwordRef"`
}

// +kubebuilder:validation:XValidation:rule="!has(oldSelf.version) || has(self.version)", message="Version is required once set"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.repo) || has(self.repo)", message="Repo is required once set"
type ChartInfo struct {
	// Url: oci or tgz full url
	Url string `json:"url"`
	// Version: desired chart version, needed for oci charts and for helm repo urls
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxLength=20
	Version string `json:"version,omitempty"`
	// Repo: Helm repo name
	// Should be set only for helm repo urls
	// If specified with OCI registries, it will be used as the repository name, instead the URL should contain the full path to the chart.
	// This is ignored for tgz archives
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxLength=256
	Repo string `json:"repo,omitempty"`

	// InsecureSkipVerifyTLS: skip tls verification
	// +optional
	InsecureSkipVerifyTLS bool `json:"insecureSkipVerifyTLS,omitempty"`

	// Credentials: credentials for private repos
	// +optional
	Credentials *Credentials `json:"credentials,omitempty"`
}

type ChartInfoProps struct {
	// Url: oci or tgz full url
	Url string `json:"url"`
	// Version: desired chart version, needed for oci charts and for helm repo urls
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxLength=20
	Version string `json:"version,omitempty"`
	// Repo: helm repo name (for helm repo urls only)
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MaxLength=256
	Repo string `json:"repo,omitempty"`

	// InsecureSkipVerifyTLS: skip tls verification
	// +optional
	InsecureSkipVerifyTLS bool `json:"insecureSkipVerifyTLS,omitempty"`

	// Credentials: credentials for private repos
	// +optional
	Credentials *Credentials `json:"credentials,omitempty"`
}

// DeploymentMode is the (status-only) classification of where the
// composition-dynamic-controller is deployed.
type DeploymentMode string

const (
	// DeploymentModeLocal: deployed into the management cluster (the default when no
	// target is referenced).
	DeploymentModeLocal DeploymentMode = "Local"
	// DeploymentModeRemote: deployed into a remote target cluster.
	DeploymentModeRemote DeploymentMode = "Remote"
)

// TargetReference references a cluster-scoped KubernetesTarget by name.
type TargetReference struct {
	// Name of the cluster-scoped KubernetesTarget.
	Name string `json:"name"`
}

// DeploymentTarget selects where the composition-dynamic-controller, the generated CRD
// and its RBAC are deployed. With no targetRef, deployment is local (the management
// cluster).
type DeploymentTarget struct {
	// TargetRef references a cluster-scoped KubernetesTarget describing the remote
	// cluster to deploy to. When omitted, deployment is local.
	// +optional
	TargetRef *TargetReference `json:"targetRef,omitempty"`
}

type CompositionDefinitionSpec struct {
	// rtv1.ManagedSpec `json:",inline"`
	Chart *ChartInfo `json:"chart,omitempty"`

	// Deploy: selects whether the composition-dynamic-controller (and the generated
	// CRD and RBAC) are deployed locally in the management cluster (the default) or to
	// a remote target cluster referenced by a KubernetesTarget. When omitted, deployment
	// is local.
	// +optional
	Deploy *DeploymentTarget `json:"deploy,omitempty"`
}

// KubernetesTargetSpec describes how to reach a remote target cluster.
type KubernetesTargetSpec struct {
	// KubeconfigRef: reference to a Kubernetes Secret key holding the kubeconfig of the
	// target cluster. The Secret is the credential rotation seam - populate and rotate
	// it via External Secrets Operator.
	KubeconfigRef rtv1.SecretKeySelector `json:"kubeconfigRef"`
}

//+kubebuilder:object:root=true
//+kubebuilder:resource:scope=Cluster,categories={krateo,core}

// KubernetesTarget is a cluster-scoped reference to a remote cluster, used by a
// CompositionDefinition's spec.deploy.targetRef to deploy compositions remotely.
type KubernetesTarget struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec KubernetesTargetSpec `json:"spec,omitempty"`
}

//+kubebuilder:object:root=true

// KubernetesTargetList is a list of KubernetesTarget.
type KubernetesTargetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []KubernetesTarget `json:"items"`
}

type VersionDetail struct {
	// Version: the version of the chart that is served. It is the version of the CRD.
	// +optional
	Version string `json:"version"`

	// Served: whether the version is served
	// +optional
	Served bool `json:"served"`

	// Stored: whether the version is stored
	// +optional
	Stored bool `json:"stored"`

	// Chart: the chart information
	// +optional
	Chart *ChartInfoProps `json:"chart"`
}

type Managed struct {
	// VersionInfo: the version information of the chart
	// +optional
	VersionInfo []VersionDetail `json:"versionInfo,omitempty"`

	// Group: the generated custom resource Group
	// +optional
	Group string `json:"group,omitempty"`

	// Kind: the generated custom resource Kind
	// +optional
	Kind string `json:"kind,omitempty"`
}

// TargetStatus reports the cluster the composition-dynamic-controller (and the
// generated CRD and RBAC) are deployed to.
type TargetStatus struct {
	// Mode: where the controller is deployed (Local or Remote).
	// +optional
	Mode string `json:"mode,omitempty"`

	// ConnectionStatus: Healthy when the target cluster is reachable, Down otherwise.
	// +optional
	ConnectionStatus string `json:"connectionStatus,omitempty"`

	// Version: the Kubernetes version reported by the target cluster.
	// +optional
	Version string `json:"version,omitempty"`

	// KubeconfigSecretResourceVersion: the resourceVersion of the kubeconfig Secret last
	// used to reach a remote target, for credential-rotation traceability.
	// +optional
	KubeconfigSecretResourceVersion string `json:"kubeconfigSecretResourceVersion,omitempty"`
}

// CompositionDefinitionStatus is the status of a CompositionDefinition.
type CompositionDefinitionStatus struct {
	rtv1.ConditionedStatus `json:",inline"`

	// Target: information about the cluster the controller is deployed to.
	// +optional
	Target *TargetStatus `json:"target,omitempty"`

	// Kind: the kind of the custom resource - Last applied kind
	Kind string `json:"kind,omitempty"`

	// Resource: the resource of the custom resource - Last applied resource
	Resource string `json:"resource,omitempty"`

	// ApiVersion: the api version of the custom resource - Last applied apiVersion
	ApiVersion string `json:"apiVersion,omitempty"`

	// Managed: information about the managed resources
	Managed Managed `json:"managed,omitempty"`

	// PackageURL: .tgz or oci chart direct url
	// +optional
	PackageURL string `json:"packageUrl,omitempty"`

	// Digest: the digest of the managed resources
	// +optional
	Digest string `json:"digest,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:scope=Namespaced,categories={krateo,defs,core}
//+kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
//+kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
//+kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
//+kubebuilder:printcolumn:name="API VERSION",type="string",JSONPath=".status.apiVersion",priority=10
//+kubebuilder:printcolumn:name="KIND",type="string",JSONPath=".status.kind",priority=10
//+kubebuilder:printcolumn:name="PACKAGE URL",type="string",JSONPath=".status.packageUrl",priority=10
//+kubebuilder:printcolumn:name="TARGET",type="string",JSONPath=".status.target.mode",priority=10
//+kubebuilder:printcolumn:name="CONNECTION",type="string",JSONPath=".status.target.connectionStatus",priority=10

// CompositionDefinition is a definition type with a spec and a status.
type CompositionDefinition struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CompositionDefinitionSpec   `json:"spec,omitempty"`
	Status CompositionDefinitionStatus `json:"status,omitempty"`
}

// SetConditions of this CompositionDefinition.
func (mg *CompositionDefinition) SetConditions(c ...rtv1.Condition) {
	mg.Status.SetConditions(c...)
}

// GetCondition of this CompositionDefinition.
func (mg *CompositionDefinition) GetCondition(ct rtv1.ConditionType) rtv1.Condition {
	return mg.Status.GetCondition(ct)
}
