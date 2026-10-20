package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// GameClusterSpec is the desired state of one game server cluster:
// gate nodes (client connections), login nodes (authentication) and game
// nodes (stateful gameplay), plus the shared infrastructure they connect to.
type GameClusterSpec struct {
	// Image of the game server. All roles run the same image.
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`

	// ImagePullPolicy of every container.
	// +kubebuilder:default=IfNotPresent
	// +optional
	ImagePullPolicy corev1.PullPolicy `json:"imagePullPolicy,omitempty"`

	// Infrastructure addresses shared by every node.
	Infra InfraSpec `json:"infra"`

	// Gate nodes accept TCP and WebSocket clients.
	// +optional
	Gate GateSpec `json:"gate,omitempty"`

	// Login nodes authenticate players and choose their game node.
	// +optional
	Login LoginSpec `json:"login,omitempty"`

	// Game nodes host online players. The operator manages them pod by pod
	// so it can pick which node to remove and drain it first.
	// +optional
	Game GameSpec `json:"game,omitempty"`

	// LogLevel of every node: debug, info, warn or error.
	// +kubebuilder:validation:Enum=debug;info;warn;error
	// +kubebuilder:default=info
	// +optional
	LogLevel string `json:"logLevel,omitempty"`
}

// InfraSpec points at the message bus, the registry and the database.
type InfraSpec struct {
	// NatsURL of the message bus, e.g. nats://nats:4222.
	// +kubebuilder:validation:MinLength=1
	NatsURL string `json:"natsURL"`
	// RedisAddr of the service registry and presence directory, e.g. redis:6379.
	// +kubebuilder:validation:MinLength=1
	RedisAddr string `json:"redisAddr"`
	// MongoURI of the player database, e.g. mongodb://mongo:27017.
	// +kubebuilder:validation:MinLength=1
	MongoURI string `json:"mongoURI"`
	// MongoDatabase name.
	// +kubebuilder:default=game
	// +optional
	MongoDatabase string `json:"mongoDatabase,omitempty"`
}

// GateSpec configures gate nodes (a Deployment behind a Service).
type GateSpec struct {
	// Replicas of the gate Deployment.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=2
	// +optional
	Replicas int32 `json:"replicas,omitempty"`
	// ServiceType of the client-facing Service.
	// +kubebuilder:validation:Enum=ClusterIP;NodePort;LoadBalancer
	// +kubebuilder:default=ClusterIP
	// +optional
	ServiceType corev1.ServiceType `json:"serviceType,omitempty"`
	// Console serves the browser test console on the gate admin port (9100).
	// For development and QA only.
	// +optional
	Console bool `json:"console,omitempty"`
	// Resources of each gate container.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

// LoginSpec configures login nodes (a StatefulSet, so each pod gets a stable
// index used as its unique Snowflake worker id).
type LoginSpec struct {
	// Replicas of the login StatefulSet.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1024
	// +kubebuilder:default=1
	// +optional
	Replicas int32 `json:"replicas,omitempty"`
	// Resources of each login container.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

// GameSpec configures game nodes.
type GameSpec struct {
	// Replicas is the fixed number of game nodes when Autoscale is not set.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=2
	// +optional
	Replicas int32 `json:"replicas,omitempty"`

	// Autoscale sizes the game tier from the number of online players.
	// When set, Replicas is ignored.
	// +optional
	Autoscale *AutoscaleSpec `json:"autoscale,omitempty"`

	// DrainTimeout is how long a draining node waits for its players to
	// leave before it is shut down (remaining players are saved, kicked and
	// reconnect to another node).
	// +kubebuilder:default="5m"
	// +optional
	DrainTimeout metav1.Duration `json:"drainTimeout,omitempty"`

	// GMEnabled turns on debug GM commands. Never in production.
	// +optional
	GMEnabled bool `json:"gmEnabled,omitempty"`

	// Resources of each game container.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

// AutoscaleSpec sizes the game tier as ceil(onlinePlayers / targetPlayersPerNode),
// clamped to [minReplicas, maxReplicas].
// +kubebuilder:validation:XValidation:rule="self.minReplicas <= self.maxReplicas",message="minReplicas must not exceed maxReplicas"
type AutoscaleSpec struct {
	// MinReplicas is the smallest number of game nodes.
	// +kubebuilder:validation:Minimum=1
	MinReplicas int32 `json:"minReplicas"`
	// MaxReplicas is the largest number of game nodes.
	// +kubebuilder:validation:Minimum=1
	MaxReplicas int32 `json:"maxReplicas"`
	// TargetPlayersPerNode is the planned load of one game node.
	// +kubebuilder:validation:Minimum=1
	TargetPlayersPerNode int32 `json:"targetPlayersPerNode"`
	// ScaleDownDelay is the minimum time between a scaling action and the
	// next scale-down, so short dips in traffic do not remove nodes.
	// +kubebuilder:default="2m"
	// +optional
	ScaleDownDelay metav1.Duration `json:"scaleDownDelay,omitempty"`
}

// Phase values of GameClusterStatus.
const (
	PhasePending  = "Pending"  // waiting for nodes to start
	PhaseRunning  = "Running"  // everything at the desired state
	PhaseScaling  = "Scaling"  // adding or draining game nodes
	PhaseUpdating = "Updating" // replacing game nodes with a new version
)

// Game node phases reported in GameNodeStatus.
const (
	NodeStarting = "Starting"
	NodeActive   = "Active"
	NodeDraining = "Draining"
)

// ComponentStatus counts the pods of a stateless component.
// Status counters are optional in the schema: status is written with merge
// patches that only carry changed fields.
type ComponentStatus struct {
	// +optional
	Desired int32 `json:"desired"`
	// +optional
	Ready int32 `json:"ready"`
}

// GameTierStatus summarizes the game nodes.
type GameTierStatus struct {
	// Desired number of active game nodes (after autoscaling).
	// +optional
	Desired int32 `json:"desired"`
	// Ready active nodes.
	// +optional
	Ready int32 `json:"ready"`
	// Draining nodes waiting for their players to leave.
	// +optional
	Draining int32 `json:"draining"`
	// Outdated active nodes still running an old pod template.
	// +optional
	Outdated int32 `json:"outdated"`
}

// GameNodeStatus describes one game node.
type GameNodeStatus struct {
	// Name of the pod, which is also the node id inside the game cluster.
	Name string `json:"name"`
	// Phase is Starting, Active or Draining.
	Phase string `json:"phase"`
	// Players online on the node; -1 when unknown.
	Players int32 `json:"players"`
	// Version is the pod template hash the node was created from.
	Version string `json:"version"`
	// DrainStartedAt is set while the node drains.
	// +optional
	DrainStartedAt *metav1.Time `json:"drainStartedAt,omitempty"`
}

// GameClusterStatus is the observed state.
type GameClusterStatus struct {
	// ObservedGeneration is the spec generation the status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Phase is a one-word summary: Pending, Running, Scaling or Updating.
	// +optional
	Phase string `json:"phase,omitempty"`
	// Players online in the whole cluster.
	// +optional
	Players int32 `json:"players"`
	// +optional
	Gate ComponentStatus `json:"gate,omitempty"`
	// +optional
	Login ComponentStatus `json:"login,omitempty"`
	// +optional
	Game GameTierStatus `json:"game,omitempty"`
	// Nodes lists every game node.
	// +optional
	Nodes []GameNodeStatus `json:"nodes,omitempty"`
	// LastScaleTime is when game nodes were last added or drained.
	// +optional
	LastScaleTime *metav1.Time `json:"lastScaleTime,omitempty"`
	// Conditions: Ready.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// GameCluster is a complete game server deployment managed by the operator.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=gc
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Players",type=integer,JSONPath=`.status.players`
// +kubebuilder:printcolumn:name="Game",type=string,JSONPath=`.status.game.ready`,description="ready game nodes"
// +kubebuilder:printcolumn:name="Desired",type=integer,JSONPath=`.status.game.desired`
// +kubebuilder:printcolumn:name="Draining",type=integer,JSONPath=`.status.game.draining`
// +kubebuilder:printcolumn:name="Gate",type=integer,JSONPath=`.status.gate.ready`
// +kubebuilder:printcolumn:name="Login",type=integer,JSONPath=`.status.login.ready`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type GameCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GameClusterSpec   `json:"spec,omitempty"`
	Status GameClusterStatus `json:"status,omitempty"`
}

// GameClusterList is a list of GameCluster.
//
// +kubebuilder:object:root=true
type GameClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GameCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&GameCluster{}, &GameClusterList{})
}
