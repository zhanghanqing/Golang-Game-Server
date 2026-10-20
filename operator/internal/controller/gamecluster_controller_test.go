package controller

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	gsv1 "gameserver/operator/api/v1alpha1"
)

// fakeNodes plays the game nodes' admin API. Players are keyed by pod IP.
type fakeNodes struct {
	mu       sync.Mutex
	players  map[string]int
	draining map[string]bool
}

func newFakeNodes() *fakeNodes {
	return &fakeNodes{players: map[string]int{}, draining: map[string]bool{}}
}

func (f *fakeNodes) Status(_ context.Context, ip string) (NodeStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return NodeStatus{Players: f.players[ip], Draining: f.draining[ip]}, nil
}

func (f *fakeNodes) Drain(ctx context.Context, ip string) (NodeStatus, error) {
	f.mu.Lock()
	f.draining[ip] = true
	f.mu.Unlock()
	return f.Status(ctx, ip)
}

func (f *fakeNodes) set(ip string, players int) {
	f.mu.Lock()
	f.players[ip] = players
	f.mu.Unlock()
}

type harness struct {
	t     *testing.T
	c     client.Client
	r     *GameClusterReconciler
	nodes *fakeNodes
	now   time.Time
	ips   map[string]string // pod name -> ip
}

func newHarness(t *testing.T, gc *gsv1.GameCluster) *harness {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = gsv1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(gc).WithStatusSubresource(&gsv1.GameCluster{}).Build()
	h := &harness{t: t, c: c, nodes: newFakeNodes(), now: t0, ips: map[string]string{}}
	h.r = &GameClusterReconciler{
		Client: c, Scheme: scheme, Nodes: h.nodes,
		Recorder: events.NewFakeRecorder(100),
		Now:      func() time.Time { return h.now },
	}
	return h
}

func (h *harness) reconcile() {
	h.t.Helper()
	_, err := h.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "demo"}})
	if err != nil {
		h.t.Fatal(err)
	}
}

// gamePods returns live game pods, marking new ones Running and Ready with an IP.
func (h *harness) gamePods() []corev1.Pod {
	h.t.Helper()
	var list corev1.PodList
	if err := h.c.List(context.Background(), &list, client.MatchingLabels{labelComponent: roleGame}); err != nil {
		h.t.Fatal(err)
	}
	for i := range list.Items {
		p := &list.Items[i]
		if p.Status.PodIP == "" {
			p.Status.Phase = corev1.PodRunning
			p.Status.PodIP = "10.0.0." + string(rune('a'+len(h.ips)))
			p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
			if err := h.c.Status().Update(context.Background(), p); err != nil {
				h.t.Fatal(err)
			}
			h.ips[p.Name] = p.Status.PodIP
		}
	}
	return list.Items
}

func (h *harness) cluster() *gsv1.GameCluster {
	gc := &gsv1.GameCluster{}
	if err := h.c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "demo"}, gc); err != nil {
		h.t.Fatal(err)
	}
	return gc
}

func (h *harness) updateSpec(f func(*gsv1.GameClusterSpec)) {
	gc := h.cluster()
	f(&gc.Spec)
	if err := h.c.Update(context.Background(), gc); err != nil {
		h.t.Fatal(err)
	}
}

func demoCluster() *gsv1.GameCluster {
	return &gsv1.GameCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default", UID: "uid-demo"},
		Spec: gsv1.GameClusterSpec{
			Image: "gameserver:v1",
			Infra: gsv1.InfraSpec{NatsURL: "nats://nats:4222", RedisAddr: "redis:6379", MongoURI: "mongodb://mongo:27017"},
			Gate:  gsv1.GateSpec{Replicas: 2},
			Login: gsv1.LoginSpec{Replicas: 2},
			Game:  gsv1.GameSpec{Replicas: 2, DrainTimeout: metav1.Duration{Duration: 5 * time.Minute}},
		},
	}
}

func TestReconcileCreatesEverything(t *testing.T) {
	h := newHarness(t, demoCluster())
	h.reconcile()
	ctx := context.Background()

	cm := &corev1.ConfigMap{}
	if err := h.c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "demo-config"}, cm); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"gate", "login", "game"} {
		cfg := cm.Data[role+".yaml"]
		if !strings.Contains(cfg, `nats_url: "nats://nats:4222"`) || !strings.Contains(cfg, "roles: ["+role+"]") {
			t.Fatalf("%s config:\n%s", role, cfg)
		}
	}
	if !strings.Contains(cm.Data["login.yaml"], "worker_id: ${WORKER_ID}") {
		t.Fatalf("login config lacks worker id:\n%s", cm.Data["login.yaml"])
	}

	dep := &appsv1.Deployment{}
	if err := h.c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "demo-gate"}, dep); err != nil {
		t.Fatal(err)
	}
	if *dep.Spec.Replicas != 2 || dep.OwnerReferences[0].Name != "demo" {
		t.Fatalf("gate deployment %+v", dep.Spec)
	}
	sts := &appsv1.StatefulSet{}
	if err := h.c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "demo-login"}, sts); err != nil {
		t.Fatal(err)
	}
	env := sts.Spec.Template.Spec.Containers[0].Env
	if env[1].Name != "WORKER_ID" || !strings.Contains(env[1].ValueFrom.FieldRef.FieldPath, "pod-index") {
		t.Fatalf("login env %+v", env)
	}
	svc := &corev1.Service{}
	if err := h.c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "demo-gate"}, svc); err != nil {
		t.Fatal(err)
	}
	if len(svc.Spec.Ports) != 2 {
		t.Fatalf("gate service ports %+v", svc.Spec.Ports)
	}

	pods := h.gamePods()
	if len(pods) != 2 {
		t.Fatalf("want 2 game pods, got %d", len(pods))
	}
	for _, p := range pods {
		if !strings.HasPrefix(p.Name, "demo-game-") || p.Labels[labelVersion] == "" {
			t.Fatalf("game pod %s labels %v", p.Name, p.Labels)
		}
		sc := p.Spec.Containers[0].SecurityContext
		if !*sc.ReadOnlyRootFilesystem || *sc.AllowPrivilegeEscalation {
			t.Fatal("game pod is not hardened")
		}
	}

	// Pods ready, players online: status reflects them.
	h.nodes.set(h.ips[pods[0].Name], 7)
	h.nodes.set(h.ips[pods[1].Name], 5)
	h.reconcile()
	st := h.cluster().Status
	if st.Players != 12 || st.Game.Ready != 2 || st.Game.Desired != 2 || len(st.Nodes) != 2 {
		t.Fatalf("status %+v", st)
	}
	if st.Phase != gsv1.PhasePending { // gate and login report 0 ready in the fake API
		t.Fatalf("phase %s", st.Phase)
	}
}

func TestAutoscaleDrainAndRemove(t *testing.T) {
	h := newHarness(t, demoCluster())
	h.reconcile()
	pods := h.gamePods()

	// Switch to autoscaling: 25 players at 10 per node -> 3 nodes.
	h.updateSpec(func(s *gsv1.GameClusterSpec) {
		s.Game.Autoscale = &gsv1.AutoscaleSpec{MinReplicas: 1, MaxReplicas: 5, TargetPlayersPerNode: 10,
			ScaleDownDelay: metav1.Duration{Duration: time.Minute}}
	})
	h.nodes.set(h.ips[pods[0].Name], 15)
	h.nodes.set(h.ips[pods[1].Name], 10)
	h.reconcile()
	pods = h.gamePods()
	if len(pods) != 3 {
		t.Fatalf("scale up: want 3 pods, got %d", len(pods))
	}
	if h.cluster().Status.LastScaleTime == nil {
		t.Fatal("LastScaleTime not set")
	}

	// Players leave. Within the cool-down nothing happens.
	for _, p := range pods {
		h.nodes.set(h.ips[p.Name], 0)
	}
	h.nodes.set(h.ips[pods[0].Name], 4)
	h.nodes.set(h.ips[pods[1].Name], 1)
	h.now = h.now.Add(30 * time.Second)
	h.reconcile()
	if d := countDraining(h.gamePods()); d != 0 {
		t.Fatalf("drained %d nodes during cool-down", d)
	}

	// After the cool-down the emptiest node (the new one with 0 players) drains.
	h.now = h.now.Add(time.Minute)
	h.reconcile()
	pods = h.gamePods()
	var victim string
	for _, p := range pods {
		if p.Annotations[annotationDrain] != "" {
			victim = p.Name
		}
	}
	if victim == "" || h.ips[victim] == h.ips[pods[0].Name] || h.ips[victim] == h.ips[pods[1].Name] {
		t.Fatalf("wrong victim %q", victim)
	}
	if !h.nodes.draining[h.ips[victim]] {
		t.Fatal("drain endpoint not called")
	}
	st := h.cluster().Status
	if st.Game.Draining != 1 || st.Phase != gsv1.PhaseScaling {
		t.Fatalf("status during drain %+v", st)
	}

	// It is empty, so the next pass deletes it.
	h.reconcile()
	for _, p := range h.gamePods() {
		if p.Name == victim {
			t.Fatal("drained pod not deleted")
		}
	}
}

func TestDrainTimeoutAndRestartedProcess(t *testing.T) {
	h := newHarness(t, demoCluster())
	h.reconcile()
	pods := h.gamePods()
	h.nodes.set(h.ips[pods[0].Name], 3)
	h.nodes.set(h.ips[pods[1].Name], 8)
	h.updateSpec(func(s *gsv1.GameClusterSpec) { s.Game.Replicas = 1 })
	h.reconcile()

	victim := pods[0].Name // fewer players
	ip := h.ips[victim]
	if !h.nodes.draining[ip] {
		t.Fatal("expected drain of the emptier node")
	}
	// The container restarts and forgets it was draining: the operator re-sends drain.
	h.nodes.mu.Lock()
	h.nodes.draining[ip] = false
	h.nodes.mu.Unlock()
	h.now = h.now.Add(time.Minute)
	h.reconcile()
	if !h.nodes.draining[ip] {
		t.Fatal("drain not re-sent after restart")
	}
	// Players never leave; after DrainTimeout the node is removed anyway.
	h.now = h.now.Add(5 * time.Minute)
	h.reconcile()
	if len(h.gamePods()) != 1 {
		t.Fatal("timed-out node not deleted")
	}
}

func TestRollingUpdateSurgesFirst(t *testing.T) {
	h := newHarness(t, demoCluster())
	h.reconcile()
	old := h.gamePods()
	h.updateSpec(func(s *gsv1.GameClusterSpec) { s.Image = "gameserver:v2" })

	h.reconcile() // surge
	pods := h.gamePods()
	if len(pods) != 3 {
		t.Fatalf("want surge to 3 pods, got %d", len(pods))
	}
	if h.cluster().Status.Phase != gsv1.PhaseUpdating {
		t.Fatalf("phase %s", h.cluster().Status.Phase)
	}
	h.reconcile() // drain one old node
	if countDraining(h.gamePods()) != 1 {
		t.Fatal("no old node draining")
	}
	for range 10 { // drained nodes are empty in the fake: the update completes
		h.reconcile()
		h.gamePods()
	}
	pods = h.gamePods()
	if len(pods) != 2 {
		t.Fatalf("want 2 pods after update, got %d", len(pods))
	}
	for _, p := range pods {
		if p.Name == old[0].Name || p.Name == old[1].Name || p.Spec.Containers[0].Image != "gameserver:v2" {
			t.Fatalf("old pod survived: %s %s", p.Name, p.Spec.Containers[0].Image)
		}
	}
}

func countDraining(pods []corev1.Pod) int {
	n := 0
	for _, p := range pods {
		if p.Annotations[annotationDrain] != "" {
			n++
		}
	}
	return n
}
