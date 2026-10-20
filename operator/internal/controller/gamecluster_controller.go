// Package controller implements the GameCluster operator.
//
// One reconcile pass:
//
//	GameCluster ──► ConfigMap (server configs, one file per role)
//	            ├─► gate:  Deployment + Service (TCP 7000 / WebSocket 7001)
//	            ├─► login: StatefulSet + headless Service (pod index = Snowflake worker id)
//	            └─► game:  Pods managed one by one
//	                          observe ─► plan ─► delete / drain / create
//	                          (players are read from each pod's /game/status)
//
// The game tier is not a Deployment because a Deployment removes pods at
// random. Game nodes hold players in memory, so the operator chooses the node
// to remove, drains it, and only deletes it once it is empty or timed out.
package controller

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	gsv1 "gameserver/operator/api/v1alpha1"
)

// +kubebuilder:rbac:groups=gameserver.dev,resources=gameclusters,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=gameserver.dev,resources=gameclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services;configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments;statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// GameClusterReconciler reconciles GameCluster objects.
type GameClusterReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder
	Nodes    NodeClient
	// PollInterval is how often player counts are refreshed. Default 5s.
	PollInterval time.Duration
	// Now is the clock (replaced in tests).
	Now func() time.Time

	exp expectations
}

// SetupWithManager registers the controller and the objects it watches.
func (r *GameClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&gsv1.GameCluster{}).
		Owns(&corev1.Pod{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.Service{}).
		Owns(&appsv1.Deployment{}).
		Owns(&appsv1.StatefulSet{}).
		Named("gamecluster").
		Complete(r)
}

func (r *GameClusterReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *GameClusterReconciler) pollInterval() time.Duration {
	if r.PollInterval > 0 {
		return r.PollInterval
	}
	return 5 * time.Second
}

// Reconcile brings one GameCluster one step closer to its spec.
func (r *GameClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	gc := &gsv1.GameCluster{}
	if err := r.Get(ctx, req.NamespacedName, gc); err != nil {
		if apierrors.IsNotFound(err) {
			r.exp.forget(req.NamespacedName)
			return ctrl.Result{}, nil // children are garbage collected via owner references
		}
		return ctrl.Result{}, err
	}
	if !gc.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	spec := withDefaults(gc.Spec)
	configs := renderConfigs(spec)
	before := gc.DeepCopy()

	if err := r.reconcileStateless(ctx, gc, spec, configs); err != nil {
		return ctrl.Result{}, err
	}

	pods, err := r.gamePods(ctx, gc)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !r.exp.satisfied(req.NamespacedName, pods) {
		// The cache has not caught up with our last writes yet; planning now
		// could create or drain a node twice.
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	version := gameVersion(spec, configs)
	nodes, podIPs := r.observe(ctx, pods)
	in := planInput{
		Now:          r.now(),
		Nodes:        nodes,
		Version:      version,
		Replicas:     int(spec.Game.Replicas),
		DrainTimeout: spec.Game.DrainTimeout.Duration,
	}
	if a := spec.Game.Autoscale; a != nil {
		in.Autoscale = &autoscalePolicy{
			Min: int(a.MinReplicas), Max: int(a.MaxReplicas), Target: int(a.TargetPlayersPerNode),
			ScaleDownDelay: a.ScaleDownDelay.Duration,
		}
	}
	if gc.Status.LastScaleTime != nil {
		in.LastScale = gc.Status.LastScaleTime.Time
	}
	p := plan(in)
	for _, msg := range p.Reasons {
		logger.Info(msg)
	}

	applyErr := r.apply(ctx, gc, spec, version, p, podIPs)
	if p.Scaled {
		gc.Status.LastScaleTime = &metav1.Time{Time: in.Now}
	}

	// Re-list so the status reflects this pass's changes as far as the cache knows.
	if pods, err = r.gamePods(ctx, gc); err == nil {
		nodes, _ = r.observeCached(pods, nodes)
	}
	if err := r.updateStatus(ctx, gc, before, spec, version, p, nodes); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{RequeueAfter: time.Second}, nil // the object changed; retry with fresh data
		}
		return ctrl.Result{}, err
	}
	if applyErr != nil {
		return ctrl.Result{}, applyErr
	}
	return ctrl.Result{RequeueAfter: r.pollInterval()}, nil
}

// reconcileStateless creates or updates the config, gate and login objects.
func (r *GameClusterReconciler) reconcileStateless(ctx context.Context, gc *gsv1.GameCluster, spec gsv1.GameClusterSpec, configs map[string]string) error {
	ns := gc.Namespace
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: configMapName(gc), Namespace: ns}}
	gateDep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: gateName(gc), Namespace: ns}}
	gateSvc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: gateName(gc), Namespace: ns}}
	loginSvc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: loginName(gc), Namespace: ns}}
	loginSts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: loginName(gc), Namespace: ns}}

	steps := []struct {
		obj    client.Object
		mutate func()
	}{
		{cm, func() { mutateConfigMap(cm, gc, configs) }},
		{gateDep, func() { mutateGateDeployment(gateDep, gc, spec, hashOf(configs[roleGate+".yaml"])) }},
		{gateSvc, func() { mutateGateService(gateSvc, gc, spec) }},
		{loginSvc, func() { mutateLoginService(loginSvc, gc) }},
		{loginSts, func() { mutateLoginStatefulSet(loginSts, gc, spec, hashOf(configs[roleLogin+".yaml"])) }},
	}
	for _, s := range steps {
		_, err := controllerutil.CreateOrUpdate(ctx, r.Client, s.obj, func() error {
			s.mutate()
			return controllerutil.SetControllerReference(gc, s.obj, r.Scheme)
		})
		if err != nil {
			return fmt.Errorf("reconcile %T %s: %w", s.obj, s.obj.GetName(), err)
		}
	}
	gc.Status.Gate = gsv1.ComponentStatus{Desired: spec.Gate.Replicas, Ready: gateDep.Status.ReadyReplicas}
	gc.Status.Login = gsv1.ComponentStatus{Desired: spec.Login.Replicas, Ready: loginSts.Status.ReadyReplicas}
	return nil
}

// gamePods lists the game pods owned by gc.
func (r *GameClusterReconciler) gamePods(ctx context.Context, gc *gsv1.GameCluster) ([]corev1.Pod, error) {
	var list corev1.PodList
	if err := r.List(ctx, &list, client.InNamespace(gc.Namespace), client.MatchingLabels(selectorLabels(gc, roleGame))); err != nil {
		return nil, err
	}
	out := list.Items[:0]
	for _, p := range list.Items {
		if metav1.IsControlledBy(&p, gc) {
			out = append(out, p)
		}
	}
	slices.SortFunc(out, func(a, b corev1.Pod) int { return cmp.Compare(a.Name, b.Name) })
	return out, nil
}

// observe turns pods into planner nodes, asking each running pod for its
// player count in parallel. A draining pod whose process does not report
// draining (for example after a container restart) is told to drain again.
func (r *GameClusterReconciler) observe(ctx context.Context, pods []corev1.Pod) ([]gameNode, map[string]string) {
	nodes, ips := r.observeCached(pods, nil)
	var wg sync.WaitGroup
	for i := range nodes {
		ip := ips[nodes[i].Name]
		if ip == "" || nodes[i].Failed {
			continue
		}
		wg.Go(func() {
			cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			st, err := r.Nodes.Status(cctx, ip)
			if err != nil {
				return // unknown: keep -1
			}
			if nodes[i].Drain != nil && !st.Draining {
				if st2, err := r.Nodes.Drain(cctx, ip); err == nil {
					st = st2
				}
			}
			nodes[i].Players = st.Players
		})
	}
	wg.Wait()
	return nodes, ips
}

// observeCached builds nodes from pod objects only, reusing player counts
// from a previous observation when available.
func (r *GameClusterReconciler) observeCached(pods []corev1.Pod, prev []gameNode) ([]gameNode, map[string]string) {
	players := map[string]int{}
	for _, n := range prev {
		players[n.Name] = n.Players
	}
	var nodes []gameNode
	ips := map[string]string{}
	for i := range pods {
		pod := &pods[i]
		if !pod.DeletionTimestamp.IsZero() {
			continue // terminating: already on its way out
		}
		n := gameNode{
			Name:    pod.Name,
			Version: pod.Labels[labelVersion],
			Ready:   podReady(pod),
			Failed:  pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded,
			Players: -1,
		}
		if v, ok := players[pod.Name]; ok {
			n.Players = v
		}
		if s := pod.Annotations[annotationDrain]; s != "" {
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				n.Drain = &t
			}
		}
		if pod.Status.Phase == corev1.PodRunning && pod.Status.PodIP != "" {
			ips[pod.Name] = pod.Status.PodIP
		}
		nodes = append(nodes, n)
	}
	return nodes, ips
}

func podReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// apply executes a plan: deletes first, then drains, then creates.
func (r *GameClusterReconciler) apply(ctx context.Context, gc *gsv1.GameCluster, spec gsv1.GameClusterSpec, version string, p gamePlan, ips map[string]string) error {
	key := types.NamespacedName{Namespace: gc.Namespace, Name: gc.Name}
	var errs []error

	for _, name := range p.Delete {
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: gc.Namespace}}
		if err := r.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
			errs = append(errs, err)
			continue
		}
		r.exp.expect(key, name, expectDeleted)
		r.event(gc, corev1.EventTypeNormal, "NodeRemoved", "Delete", "Removed game node %s", name)
	}

	for _, name := range p.Drain {
		pod := &corev1.Pod{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: gc.Namespace, Name: name}, pod); err != nil {
			errs = append(errs, err)
			continue
		}
		// Record the decision on the pod first: it survives operator restarts.
		patch := client.MergeFrom(pod.DeepCopy())
		if pod.Annotations == nil {
			pod.Annotations = map[string]string{}
		}
		pod.Annotations[annotationDrain] = r.now().UTC().Format(time.RFC3339)
		if err := r.Patch(ctx, pod, patch); err != nil {
			errs = append(errs, err)
			continue
		}
		r.exp.expect(key, name, expectDraining)
		if ip := ips[name]; ip != "" {
			if _, err := r.Nodes.Drain(ctx, ip); err != nil {
				// Retried by observe() on the next pass.
				log.FromContext(ctx).Info("drain call failed, will retry", "pod", name, "err", err.Error())
			}
		}
		r.event(gc, corev1.EventTypeNormal, "NodeDraining", "Drain", "Draining game node %s: %s", name, firstOr(p.Reasons, ""))
	}

	for range p.Create {
		pod := newGamePod(gc, spec, version)
		if err := controllerutil.SetControllerReference(gc, pod, r.Scheme); err != nil {
			return err
		}
		if err := r.Create(ctx, pod); err != nil {
			errs = append(errs, err)
			continue
		}
		r.exp.expect(key, pod.Name, expectCreated)
		r.event(gc, corev1.EventTypeNormal, "NodeCreated", "Create", "Created game node %s: %s", pod.Name, firstOr(p.Reasons, ""))
	}
	return errors.Join(errs...)
}

func firstOr(s []string, def string) string {
	if len(s) > 0 {
		return s[0]
	}
	return def
}

func (r *GameClusterReconciler) event(gc *gsv1.GameCluster, typ, reason, action, note string, args ...any) {
	if r.Recorder != nil {
		r.Recorder.Eventf(gc, nil, typ, reason, action, note, args...)
	}
}

// updateStatus writes the observed state (only if it changed).
func (r *GameClusterReconciler) updateStatus(ctx context.Context, gc, before *gsv1.GameCluster, spec gsv1.GameClusterSpec, version string, p gamePlan, nodes []gameNode) error {
	st := &gc.Status
	st.ObservedGeneration = gc.Generation
	st.Players = int32(p.Players)
	st.Game = gsv1.GameTierStatus{Desired: int32(p.Desired)}
	st.Nodes = st.Nodes[:0]
	for _, n := range nodes {
		ns := gsv1.GameNodeStatus{Name: n.Name, Players: int32(n.Players), Version: n.Version}
		switch {
		case n.Drain != nil:
			ns.Phase = gsv1.NodeDraining
			ns.DrainStartedAt = &metav1.Time{Time: *n.Drain}
			st.Game.Draining++
		case n.Ready && !n.Failed:
			ns.Phase = gsv1.NodeActive
			st.Game.Ready++
			if n.Version != version {
				st.Game.Outdated++
			}
		default:
			ns.Phase = gsv1.NodeStarting
		}
		st.Nodes = append(st.Nodes, ns)
	}
	if len(st.Nodes) == 0 {
		st.Nodes = nil
	}

	ready := st.Gate.Ready >= spec.Gate.Replicas && st.Login.Ready >= spec.Login.Replicas &&
		st.Game.Ready >= st.Game.Desired
	switch {
	case st.Game.Outdated > 0:
		st.Phase = gsv1.PhaseUpdating
	case st.Game.Draining > 0 || st.Game.Ready != st.Game.Desired:
		if st.Game.Ready == 0 && st.Game.Desired > 0 {
			st.Phase = gsv1.PhasePending
		} else {
			st.Phase = gsv1.PhaseScaling
		}
	case !ready:
		st.Phase = gsv1.PhasePending
	default:
		st.Phase = gsv1.PhaseRunning
	}
	cond := metav1.Condition{Type: "Ready", Status: metav1.ConditionFalse, Reason: st.Phase, ObservedGeneration: gc.Generation,
		Message: fmt.Sprintf("gate %d/%d, login %d/%d, game %d/%d", st.Gate.Ready, spec.Gate.Replicas, st.Login.Ready, spec.Login.Replicas, st.Game.Ready, st.Game.Desired)}
	if ready {
		cond.Status = metav1.ConditionTrue
		cond.Reason = "AllNodesReady"
	}
	meta.SetStatusCondition(&st.Conditions, cond)

	if equality.Semantic.DeepEqual(before.Status, gc.Status) {
		return nil
	}
	// A full update (not a merge patch) so zero values such as "players: 0"
	// are written too. A conflict just means another write won; the request
	// is retried with fresh data.
	return r.Status().Update(ctx, gc)
}

// ---------------------------------------------------------------------------
// Expectations: remember writes until the informer cache reflects them.

type expectKind int

const (
	expectCreated expectKind = iota
	expectDraining
	expectDeleted
)

type expectation struct {
	kind  expectKind
	since time.Time
}

// expectationTTL bounds how long we wait for the cache; after it the
// expectation is dropped so a lost watch event cannot block the controller.
const expectationTTL = 30 * time.Second

type expectations struct {
	mu sync.Mutex
	m  map[types.NamespacedName]map[string]expectation
}

func (e *expectations) expect(key types.NamespacedName, pod string, kind expectKind) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.m == nil {
		e.m = map[types.NamespacedName]map[string]expectation{}
	}
	if e.m[key] == nil {
		e.m[key] = map[string]expectation{}
	}
	e.m[key][pod] = expectation{kind: kind, since: time.Now()}
}

func (e *expectations) forget(key types.NamespacedName) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.m, key)
}

// satisfied reports whether pods reflect every pending write for key.
func (e *expectations) satisfied(key types.NamespacedName, pods []corev1.Pod) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	pending := e.m[key]
	byName := map[string]*corev1.Pod{}
	for i := range pods {
		byName[pods[i].Name] = &pods[i]
	}
	for name, exp := range pending {
		pod := byName[name]
		var ok bool
		switch exp.kind {
		case expectCreated:
			ok = pod != nil
		case expectDraining:
			ok = pod == nil || pod.Annotations[annotationDrain] != ""
		case expectDeleted:
			ok = pod == nil || !pod.DeletionTimestamp.IsZero()
		}
		if ok || time.Since(exp.since) > expectationTTL {
			delete(pending, name)
		}
	}
	return len(pending) == 0
}
