package v1beta1

import (
	libcnd "github.com/kubev2v/forklift/pkg/lib/condition"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed
type ChangedBlockStackerPhase string

const (
	ChangedBlockStackerKind = "ChangedBlockStacker"

	ChangedBlockStackerPhasePending   = "Pending"
	ChangedBlockStackerPhaseRunning   = "Running"
	ChangedBlockStackerPhaseSucceeded = "Succeeded"
	ChangedBlockStackerPhaseFailed    = "Failed"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +k8s:openapi-gen=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName={cbs,cbses}
// ChangedBlockStacker coordinates copying disk data differences between two checkpoints, or snapshots,
// or restore points, or whatever other terminology is used by a source provider for a warm migration.
// Each ChangedBlockStacker finds a list of changed blocks and stacks them on top of an existing PVC,
// and the final disk is built up layer by layer while the source VM is still running. As a side effect,
// this can handle cold migration just by not specifying any beginning checkpoint.
type ChangedBlockStacker struct {
	meta.TypeMeta   `json:",inline"`
	meta.ObjectMeta `json:"metadata,omitempty"`

	Spec ChangedBlockStackerSpec `json:"spec"`
	// +optional
	Status ChangedBlockStackerStatus `json:"status"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ChangedBlockStackerList struct {
	meta.TypeMeta `json:",inline"`
	meta.ListMeta `json:"metadata,omitempty"`
	Items         []ChangedBlockStacker `json:"items"`
}

type ChangedBlockStackerSpec struct {
	// Name of the PVC to receive data
	PVC string `json:"pvc"`
	// +optional
	// Starting checkpoint reference, differences are computed between here and "To"
	// Leave empty to copy the whole disk for cold migration
	From string `json:"from,omitempty"`
	// +optional
	// Ending checkpoint reference, different sources may have different formats
	To string `json:"to,omitempty"`

	// Sources, exactly one should be filled out
	// +optional
	Nutanix *ChangedBlockStackerSourceNutanix `json:"nutanix,omitempty"`
	// +optional
	VMware *ChangedBlockStackerSourceVMware `json:"vmware,omitempty"`
}

type ChangedBlockStackerStatus struct {
	// +optional
	Cursor ChangedBlockStackerCursor `json:"cursor,omitempty"`
	// +optional
	// Bytes copied so far for this delta
	BytesCopied int64 `json:"bytesCopied,omitempty"`
	// +optional
	Phase ChangedBlockStackerPhase `json:"phase,omitempty"`
	// +optional
	libcnd.Conditions `json:",inline"`
}

type ChangedBlockStackerCursor struct {
	// +optional
	// Raw disk offset. Everything up to this point has been copied.
	// As long as To and From have not changed, it should be safe to resume from here.
	Offset int64 `json:"offset,omitempty"`
}

type ChangedBlockStackerSourceVMware struct {
}

type ChangedBlockStackerSourceNutanix struct {
}

func init() {
	SchemeBuilder.Register(&ChangedBlockStacker{}, &ChangedBlockStackerList{})
}
