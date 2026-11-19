package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	PolicyGuarded    = "Guarded"
	PolicyCompatible = "DrainCompatible"
	Finalizer        = "maintenance.example.com/cleanup"
	ClaimAnnotation  = "maintenance.example.com/claim"
	HoldAnnotation   = "maintenance.example.com/hold-cordon"
	MaxRecords       = 256
)

// DrainOptions 将原生 drain 的显式行为保留为任务契约。
type DrainOptions struct {
	// +optional
	// +kubebuilder:default=false
	IgnoreDaemonSets bool `json:"ignoreDaemonSets"`
	// +optional
	// +kubebuilder:default=false
	Force bool `json:"force"`
	// +optional
	// +kubebuilder:default=false
	DeleteEmptyDirData bool `json:"deleteEmptyDirData"`
	// +optional
	// +kubebuilder:default=false
	DisableEviction bool `json:"disableEviction"`
	// +optional
	// +kubebuilder:default=-1
	// +kubebuilder:validation:Minimum=-1
	GracePeriodSeconds int64 `json:"gracePeriodSeconds"`
	// +optional
	// +kubebuilder:default=0
	// +kubebuilder:validation:Minimum=0
	SkipWaitForDeleteTimeoutSeconds int64 `json:"skipWaitForDeleteTimeoutSeconds"`
	// +optional
	// +kubebuilder:default=""
	// +kubebuilder:validation:MaxLength=1024
	PodSelector string `json:"podSelector"`
	// +optional
	// +kubebuilder:default=500
	// +kubebuilder:validation:Minimum=0
	ChunkSize int64 `json:"chunkSize"`
	// +kubebuilder:default=Client
	// +kubebuilder:validation:Enum=Client;Server
	DryRun string `json:"dryRun,omitempty"`
}

// NodeMaintenanceSpec 描述维护意图，执行语义创建后不可变。
// +kubebuilder:validation:XValidation:rule="self.executionPolicy != 'Guarded' || (!self.drain.disableEviction && self.drain.skipWaitForDeleteTimeoutSeconds == 0 && size(self.drain.podSelector) == 0)",message="Guarded does not allow bypass or partial-drain options"
type NodeMaintenanceSpec struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="nodeName is immutable"
	NodeName string `json:"nodeName"`
	// +optional
	// +kubebuilder:default=Execute
	// +kubebuilder:validation:Enum=Execute;PlanOnly
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="mode is immutable"
	Mode string `json:"mode"`
	// +optional
	// +kubebuilder:default=Guarded
	// +kubebuilder:validation:Enum=Guarded;DrainCompatible
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="executionPolicy is immutable"
	ExecutionPolicy string `json:"executionPolicy"`
	// +optional
	// +kubebuilder:default=Maintenance
	// +kubebuilder:validation:Enum=Maintenance;Restored
	// +kubebuilder:validation:XValidation:rule="oldSelf != 'Restored' || self == 'Restored'",message="Restored is terminal intent"
	DesiredState string `json:"desiredState"`
	Paused       bool   `json:"paused,omitempty"`
	// +optional
	// +kubebuilder:default=1800
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=604800
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="timeoutSeconds is immutable"
	TimeoutSeconds int64 `json:"timeoutSeconds"`
	// +optional
	// +kubebuilder:default=Default
	// +kubebuilder:validation:Enum=Default;Wait;Evict
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="jobPolicy is immutable"
	JobPolicy string `json:"jobPolicy"`
	// +kubebuilder:validation:MaxLength=1024
	Reason string `json:"reason,omitempty"`
	// +optional
	// +kubebuilder:default={ignoreDaemonSets:false,force:false,deleteEmptyDirData:false,disableEviction:false,gracePeriodSeconds:-1,skipWaitForDeleteTimeoutSeconds:0,podSelector:"",chunkSize:500,dryRun:Client}
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="drain options are immutable"
	Drain DrainOptions `json:"drain"`
}

// EffectiveJobPolicy 解析策略相关默认值，不隐式改变用户指定策略。
func (s NodeMaintenanceSpec) EffectiveJobPolicy() string {
	if s.JobPolicy == "Wait" || s.JobPolicy == "Evict" {
		return s.JobPolicy
	}
	if s.ExecutionPolicy == PolicyCompatible {
		return "Evict"
	}
	return "Wait"
}

// ObjectIdentity 通过 UID 区分同名对象。
type ObjectIdentity struct {
	Kind      string    `json:"kind,omitempty"`
	Namespace string    `json:"namespace,omitempty"`
	Name      string    `json:"name"`
	UID       types.UID `json:"uid"`
}

// VolumeIdentity 保存需要重验的 PVC/PV 绑定和后端身份。
type VolumeIdentity struct {
	PVC        ObjectIdentity `json:"pvc"`
	PV         ObjectIdentity `json:"pv"`
	SourceHash string         `json:"sourceHash"`
}

// WorkloadBaseline 冻结增强恢复谓词所依赖的规格。
type WorkloadBaseline struct {
	ObjectIdentity  `json:",inline"`
	Generation      int64     `json:"generation"`
	Desired         int32     `json:"desired"`
	TemplateHash    string    `json:"templateHash"`
	ReplicaSetUID   types.UID `json:"replicaSetUID,omitempty"`
	Revision        string    `json:"revision,omitempty"`
	StartOrdinal    int32     `json:"startOrdinal,omitempty"`
	MinReadySeconds int32     `json:"minReadySeconds,omitempty"`
}

// Operation 是单个删除副作用的持久日志，不承诺恰好一次执行。
type Operation struct {
	ObjectIdentity    `json:",inline"`
	ID                string           `json:"id"`
	Action            string           `json:"action"`
	State             string           `json:"state"`
	UnresolvedUnknown bool             `json:"unresolvedUnknown,omitempty"`
	OwnerUID          types.UID        `json:"ownerUID,omitempty"`
	RecoveryKind      string           `json:"recoveryKind,omitempty"`
	Ordinal           int32            `json:"ordinal,omitempty"`
	Volumes           []VolumeIdentity `json:"volumes,omitempty"`
	Attempts          int32            `json:"attempts,omitempty"`
	LastReason        string           `json:"lastReason,omitempty"`
	Accepted          bool             `json:"accepted,omitempty"`
	RetryAfter        *metav1.Time     `json:"retryAfter,omitempty"`
}

// JobObservation 记录本节点曾观察到的实例结果，不推断 Job 的业务成功。
type JobObservation struct {
	ObjectIdentity `json:",inline"`
	Outcome        string `json:"outcome"`
}

// PodReport 保存当前分类而非无界审计历史。
type PodReport struct {
	ObjectIdentity `json:",inline"`
	Action         string `json:"action"`
	Reason         string `json:"reason"`
}

// CheckReport 区分有见证的可行、可证明阻塞和未知。
type CheckReport struct {
	Result     string            `json:"result"`
	Reason     string            `json:"reason"`
	Message    string            `json:"message,omitempty"`
	Placements map[string]string `json:"placements,omitempty"`
}

type NodeMaintenanceStatus struct {
	Phase                    string       `json:"phase,omitempty"`
	ObservedGeneration       int64        `json:"observedGeneration,omitempty"`
	NodeUID                  types.UID    `json:"nodeUID,omitempty"`
	NodeClaimStarted         bool         `json:"nodeClaimStarted,omitempty"`
	NodeClaimResourceVersion string       `json:"nodeClaimResourceVersion,omitempty"`
	NodeClaimed              bool         `json:"nodeClaimed,omitempty"`
	BaselineCaptured         bool         `json:"baselineCaptured,omitempty"`
	StartedAt                *metav1.Time `json:"startedAt,omitempty"`
	Deadline                 *metav1.Time `json:"deadline,omitempty"`
	JobPolicy                string       `json:"jobPolicy,omitempty"`
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// +kubebuilder:validation:MaxItems=256
	Baselines        []WorkloadBaseline `json:"baselines,omitempty"`
	CurrentOperation *Operation         `json:"currentOperation,omitempty"`
	// +kubebuilder:validation:MaxItems=256
	PendingCleanup []Operation `json:"pendingCleanup,omitempty"`
	// +kubebuilder:validation:MaxItems=256
	JobObservations []JobObservation `json:"jobObservations,omitempty"`
	// +kubebuilder:validation:MaxItems=256
	Pods                 []PodReport `json:"pods,omitempty"`
	Compute              CheckReport `json:"compute,omitempty"`
	Storage              CheckReport `json:"storage,omitempty"`
	Recovery             CheckReport `json:"recovery,omitempty"`
	CompletedOperations  int32       `json:"completedOperations,omitempty"`
	NodeCleanupConfirmed bool        `json:"nodeCleanupConfirmed,omitempty"`
	CleanupResult        string      `json:"cleanupResult,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=nm
// +kubebuilder:printcolumn:name="Node",type=string,JSONPath=`.spec.nodeName`
// +kubebuilder:printcolumn:name="Policy",type=string,JSONPath=`.spec.executionPolicy`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="MaintenanceReady")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NodeMaintenance struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              NodeMaintenanceSpec   `json:"spec"`
	Status            NodeMaintenanceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type NodeMaintenanceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NodeMaintenance `json:"items"`
}

func init() { SchemeBuilder.Register(&NodeMaintenance{}, &NodeMaintenanceList{}) }
