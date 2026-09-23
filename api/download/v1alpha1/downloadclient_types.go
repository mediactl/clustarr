/*
Copyright 2026 The Clustarr Authors.

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// Condition types reported on a DownloadClient.
const (
	// DownloadClientConditionReady is True when the client is configured and usable.
	DownloadClientConditionReady = "Ready"
	// DownloadClientConditionEngineReady is True when every engine replica is ready.
	DownloadClientConditionEngineReady = "EngineReady"
	// DownloadClientConditionDiskSpaceOK is True when the client has room for new work.
	DownloadClientConditionDiskSpaceOK = "DiskSpaceOK"
)

// HealthAction is what a usenet client does with a download whose article
// health falls below the abort threshold.
//
// +kubebuilder:validation:Enum=pause;delete
type HealthAction string

// Health actions.
const (
	// HealthActionPause pauses the download and waits for operator input.
	HealthActionPause HealthAction = "pause"
	// HealthActionDelete deletes the download and blocklists the release.
	HealthActionDelete HealthAction = "delete"
)

// TorrentSpec configures the embedded anacrolix/torrent engine. It applies
// only when spec.protocol is torrent.
type TorrentSpec struct {
	// ListenPort is the TCP/uTP port the engine listens on for peers.
	// +optional
	// +kubebuilder:default=42069
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	ListenPort int32 `json:"listenPort,omitempty"`

	// PublicIP is the address advertised to peers and trackers when the engine
	// cannot discover it, e.g. behind NAT or a gateway. Unset means autodetect.
	// +optional
	PublicIP *string `json:"publicIP,omitempty"`

	// EnableDHT joins the distributed hash table for peer discovery.
	// +optional
	// +kubebuilder:default=true
	EnableDHT *bool `json:"enableDHT,omitempty"`

	// EnablePEX enables peer exchange with connected peers.
	// +optional
	// +kubebuilder:default=true
	EnablePEX *bool `json:"enablePEX,omitempty"`

	// MaxActive caps how many torrents may transfer at once per replica.
	// +optional
	// +kubebuilder:default=200
	// +kubebuilder:validation:Minimum=1
	MaxActive int32 `json:"maxActive,omitempty"`

	// DownloadLimitBps caps the aggregate download rate in bytes per second.
	// Zero or unset means unlimited.
	// +optional
	// +kubebuilder:validation:Minimum=0
	DownloadLimitBps int64 `json:"downloadLimitBps,omitempty"`

	// UploadLimitBps caps the aggregate upload rate in bytes per second.
	// Zero or unset means unlimited.
	// +optional
	// +kubebuilder:validation:Minimum=0
	UploadLimitBps int64 `json:"uploadLimitBps,omitempty"`

	// MaxUnverifiedBytes caps how many downloaded-but-unhashed bytes the engine
	// keeps in flight before it stops requesting more pieces.
	// +optional
	// +kubebuilder:default=134217728
	// +kubebuilder:validation:Minimum=0
	MaxUnverifiedBytes int64 `json:"maxUnverifiedBytes,omitempty"`

	// Seed is the default seed goal for torrents on this client. A Download may
	// override it with its own spec.seedCriteria.
	// +optional
	// +kubebuilder:default={ratio:"1",seedTime:"168h",packSeedTime:"336h",inactiveTime:"24h"}
	Seed *commonv1alpha1.SeedCriteria `json:"seed,omitempty"`

	// RemoveCompleted removes a torrent from the engine once its seed goal is met.
	// +optional
	// +kubebuilder:default=true
	RemoveCompleted *bool `json:"removeCompleted,omitempty"`
}

// NNTPProvider is one upstream usenet server. Providers are tried in priority
// order; backup providers are only used to repair missing articles.
type NNTPProvider struct {
	// Name identifies the provider within this client and keys the list.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`

	// Host is the NNTP server hostname.
	// +required
	// +kubebuilder:validation:MinLength=1
	Host string `json:"host"`

	// Port is the NNTP port.
	// +optional
	// +kubebuilder:default=563
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port,omitempty"`

	// TLS enables implicit TLS on the connection.
	// +optional
	// +kubebuilder:default=true
	TLS *bool `json:"tls,omitempty"`

	// Connections is the maximum number of simultaneous connections allowed by
	// the provider's plan.
	// +optional
	// +kubebuilder:default=8
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=256
	Connections int32 `json:"connections,omitempty"`

	// SecretRef names a Secret in the same namespace holding the provider
	// credentials. Recognised keys: username, password.
	// +required
	SecretRef corev1.LocalObjectReference `json:"secretRef"`

	// Backup marks the provider as fill-only: it is used to fetch articles the
	// primary providers could not supply, never for the bulk of a download.
	// +optional
	Backup bool `json:"backup,omitempty"`

	// Priority orders providers of the same class; lower wins.
	// +optional
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=50
	Priority int32 `json:"priority,omitempty"`

	// QuotaBytes is the monthly transfer allowance of the provider's plan.
	// Unset means unmetered.
	// +optional
	// +kubebuilder:validation:Minimum=0
	QuotaBytes *int64 `json:"quotaBytes,omitempty"`
}

// PostProcessSpec controls what the usenet engine does with a completed
// download before it is handed to the importer.
type PostProcessSpec struct {
	// Par2 repairs the download from its par2 recovery volumes when articles
	// are missing or corrupt.
	// +optional
	// +kubebuilder:default=true
	Par2 *bool `json:"par2,omitempty"`

	// Unpack extracts rar/7z/zip archives after a successful repair.
	// +optional
	// +kubebuilder:default=true
	Unpack *bool `json:"unpack,omitempty"`

	// DeleteArchives removes the source archives and par2 volumes after a
	// successful unpack.
	// +optional
	// +kubebuilder:default=true
	DeleteArchives *bool `json:"deleteArchives,omitempty"`

	// CleanupPatterns lists glob patterns of junk files removed after unpack,
	// e.g. "*.nfo", "*sample*".
	// +optional
	// +kubebuilder:validation:MaxItems=64
	CleanupPatterns []string `json:"cleanupPatterns,omitempty"`
}

// ScratchSpec sizes the working area each usenet engine replica uses while
// downloading, repairing and unpacking.
type ScratchSpec struct {
	// SizeLimit is the capacity of the scratch volume. A Go client always
	// sends a Quantity, so the DownloadClient controller floors a zero one to
	// this default: a zero-byte scratch volume has no coherent meaning.
	// +optional
	// +kubebuilder:default="50Gi"
	SizeLimit resource.Quantity `json:"sizeLimit,omitempty"`

	// StorageClassName selects the StorageClass of the scratch volume. Unset
	// means an emptyDir backed by node storage is used instead of a PVC.
	// +optional
	StorageClassName *string `json:"storageClassName,omitempty"`
}

// UsenetSpec configures the NNTP engine. It applies only when spec.protocol
// is usenet.
type UsenetSpec struct {
	// Providers lists the NNTP servers the engine may use. At least one
	// non-backup provider is required.
	// +required
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=32
	Providers []NNTPProvider `json:"providers"`

	// PostProcess controls par2 repair, unpacking and cleanup.
	// +optional
	PostProcess *PostProcessSpec `json:"postProcess,omitempty"`

	// PropagationDelay is how long after a release is published the engine
	// waits before starting it, so articles have time to propagate.
	// +optional
	PropagationDelay *metav1.Duration `json:"propagationDelay,omitempty"`

	// PreCheck verifies that the first articles of an NZB exist before the
	// engine commits to the download.
	// +optional
	PreCheck bool `json:"preCheck,omitempty"`

	// AbortHealthPercent is the article-health floor, as a whole percentage.
	// A download whose projected health falls below it triggers healthAction.
	// +optional
	// +kubebuilder:default=90
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	AbortHealthPercent int32 `json:"abortHealthPercent,omitempty"`

	// HealthAction is what happens when abortHealthPercent is breached.
	// +optional
	// +kubebuilder:default=pause
	HealthAction HealthAction `json:"healthAction,omitempty"`

	// Scratch sizes the per-replica working area.
	// +optional
	Scratch *ScratchSpec `json:"scratch,omitempty"`
}

// DownloadClientSpec defines the desired state of DownloadClient. Exactly one
// of torrent or usenet is set, selected by protocol.
//
// +kubebuilder:validation:XValidation:rule="self.protocol == 'torrent' ? (has(self.torrent) && !has(self.usenet)) : (has(self.usenet) && !has(self.torrent))",message="torrent must be set for protocol torrent and usenet for protocol usenet, never both"
// +kubebuilder:validation:XValidation:rule="self.protocol != 'usenet' || !has(self.replicas) || self.replicas == 1",message="usenet clients must have replicas == 1 in v1alpha1"
type DownloadClientSpec struct {
	// Protocol selects which engine this client runs. It is immutable: change
	// the protocol by creating a new DownloadClient.
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="protocol is immutable"
	Protocol commonv1alpha1.Protocol `json:"protocol"`

	// Enabled turns the client off without deleting it. A disabled client keeps
	// its engine running for in-flight work but is not assigned new Downloads.
	// +optional
	// +kubebuilder:default=true
	Enabled *bool `json:"enabled,omitempty"`

	// Priority orders clients of the same protocol when grabarr picks one for a
	// Download; lower wins.
	// +optional
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=50
	Priority int32 `json:"priority,omitempty"`

	// Replicas is the number of engine shards, backing a StatefulSet owned by
	// this client. Each Download is pinned to one ordinal for its lifetime; see
	// Download.status.engine. Usenet clients are limited to one replica in
	// v1alpha1.
	// +optional
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=64
	Replicas int32 `json:"replicas,omitempty"`

	// Categories maps a media kind to the subdirectory downloads of that kind
	// are written to. A kind with no entry uses the kind's own name.
	// +optional
	Categories map[string]string `json:"categories,omitempty"`

	// Torrent configures the torrent engine. Required when protocol is torrent.
	// +optional
	Torrent *TorrentSpec `json:"torrent,omitempty"`

	// Usenet configures the NNTP engine. Required when protocol is usenet.
	// +optional
	Usenet *UsenetSpec `json:"usenet,omitempty"`

	// Resources is the resource requirements of each engine replica.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// NodeSelector constrains the engine replicas to matching nodes.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Tolerations are applied to the engine replicas.
	// +optional
	// +kubebuilder:validation:MaxItems=32
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
}

// EngineStatus reports the workload backing a download client.
type EngineStatus struct {
	// WorkloadRef is the name of the StatefulSet running the engine, in the
	// same namespace as the DownloadClient.
	// +optional
	WorkloadRef string `json:"workloadRef,omitempty"`

	// Replicas is the number of engine replicas that currently exist.
	// +optional
	Replicas int32 `json:"replicas,omitempty"`

	// ReadyReplicas is the number of engine replicas that are ready.
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`
}

// DownloadClientStatus defines the observed state of DownloadClient.
type DownloadClientStatus struct {
	// ObservedGeneration is the spec generation this status was computed from.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Engine reports the StatefulSet backing this client.
	// +optional
	Engine *EngineStatus `json:"engine,omitempty"`

	// Active is the number of Downloads currently transferring on this client.
	// +optional
	Active int32 `json:"active,omitempty"`

	// Queued is the number of Downloads assigned to this client but not yet
	// transferring.
	// +optional
	Queued int32 `json:"queued,omitempty"`

	// Seeding is the number of Downloads seeding on this client. Torrent only.
	// +optional
	Seeding int32 `json:"seeding,omitempty"`

	// DownloadRateBps is the aggregate download rate in bytes per second.
	// +optional
	DownloadRateBps int64 `json:"downloadRateBps,omitempty"`

	// UploadRateBps is the aggregate upload rate in bytes per second.
	// +optional
	UploadRateBps int64 `json:"uploadRateBps,omitempty"`

	// FreeBytes is the free space remaining on the client's download volume.
	// +optional
	FreeBytes int64 `json:"freeBytes,omitempty"`

	// Conditions holds Ready, EngineReady and DiskSpaceOK.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// DownloadClient is a torrent or usenet download engine managed by grabarr.
// The controller owns a StatefulSet of spec.replicas engine pods; Downloads
// are assigned to this client and pinned to one of its ordinals.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:ac:generate=true
// +kubebuilder:resource:scope=Namespaced,shortName=dlclient,categories=clustarr
// +kubebuilder:printcolumn:name="Protocol",type="string",JSONPath=".spec.protocol"
// +kubebuilder:printcolumn:name="Enabled",type="boolean",JSONPath=".spec.enabled"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Replicas",type="integer",JSONPath=".status.engine.readyReplicas"
// +kubebuilder:printcolumn:name="Active",type="integer",JSONPath=".status.active"
// +kubebuilder:printcolumn:name="Queued",type="integer",JSONPath=".status.queued",priority=1
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type DownloadClient struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DownloadClientSpec   `json:"spec,omitempty"`
	Status DownloadClientStatus `json:"status,omitempty"`
}

// DownloadClientList contains a list of DownloadClient.
//
// +kubebuilder:object:root=true
// +kubebuilder:ac:generate=true
type DownloadClientList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DownloadClient `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DownloadClient{}, &DownloadClientList{})
}
