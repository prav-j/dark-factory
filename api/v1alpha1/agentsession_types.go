// Package v1alpha1 defines the AgentSession CRD (specs/16-deployment-sessions.md).
// +kubebuilder:object:generate=true
// +groupName=agents.platform
package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// Phase is the session lifecycle state.
// Provisioning -> Running -> Idle -> Committing -> Terminated.
type Phase string

const (
	PhaseProvisioning Phase = "Provisioning"
	PhaseRunning      Phase = "Running"
	PhaseIdle         Phase = "Idle"
	PhaseCommitting   Phase = "Committing"
	PhaseTerminated   Phase = "Terminated"
)

// AgentSessionSpec is the desired session state.
type AgentSessionSpec struct {
	AgentRef       string                      `json:"agentRef"` // name@version
	UserID         string                      `json:"userId"`
	OrgID          string                      `json:"orgId"`
	EnvironmentKey string                      `json:"environmentKey"` // snapshot cache key (§15)
	Resources      corev1.ResourceRequirements `json:"resources,omitempty"`
	IdleTimeout    *metav1.Duration            `json:"idleTimeout,omitempty"` // default 10m
	MaxLifetime    *metav1.Duration            `json:"maxLifetime,omitempty"` // hard cap, e.g. 4h
	PriorityClass  string                      `json:"priorityClassName,omitempty"`
}

// AgentSessionStatus reports observed state.
type AgentSessionStatus struct {
	Phase              Phase        `json:"phase,omitempty"`
	PodName            string       `json:"podName,omitempty"`
	WarmHit            bool         `json:"warmHit,omitempty"`
	StartedAt          *metav1.Time `json:"startedAt,omitempty"`
	LastActivity       *metav1.Time `json:"lastActivity,omitempty"`
	TerminatedAt       *metav1.Time `json:"terminatedAt,omitempty"`
	ObservedGeneration int64        `json:"observedGeneration,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
//+kubebuilder:printcolumn:name="Agent",type=string,JSONPath=`.spec.agentRef`
//+kubebuilder:printcolumn:name="Warm",type=boolean,JSONPath=`.status.warmHit`

// AgentSession owns exactly one sandbox pod; a session contains one or more
// runs. Namespace-per-org provides hard isolation.
type AgentSession struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AgentSessionSpec   `json:"spec,omitempty"`
	Status AgentSessionStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// AgentSessionList is the collection type.
type AgentSessionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgentSession `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(GroupVersion, &AgentSession{}, &AgentSessionList{})
		metav1.AddToGroupVersion(s, GroupVersion)
		return nil
	})
}
