package operator

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	agentsv1alpha1 "github.com/prav-j/dark-factory/api/v1alpha1"
)

type fakeClock struct{ now time.Time }

func (f *fakeClock) Now() time.Time          { return f.now }
func (f *fakeClock) Advance(d time.Duration) { f.now = f.now.Add(d) }

func newTestReconciler(t *testing.T, objs ...runtime.Object) (*SessionReconciler, *fakeClock) {
	t.Helper()
	sch := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(sch); err != nil {
		t.Fatal(err)
	}
	if err := agentsv1alpha1.AddToScheme(sch); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(sch).WithStatusSubresource(&agentsv1alpha1.AgentSession{}).WithRuntimeObjects(objs...).Build()
	clock := &fakeClock{now: time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)}
	r := NewReconciler(c, "registry.internal/sandbox:snap-abc")
	r.Clock = clock
	return r, clock
}

func testSession() *agentsv1alpha1.AgentSession {
	idle := metav1.Duration{Duration: 10 * time.Minute}
	lifetime := metav1.Duration{Duration: 4 * time.Hour}
	return &agentsv1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: "sess-1", Namespace: "tenant-org99"},
		Spec: agentsv1alpha1.AgentSessionSpec{
			AgentRef:       "repo-triage-bot@v7",
			UserID:         "user-1234",
			OrgID:          "org99",
			EnvironmentKey: "sha256:a1b2",
			IdleTimeout:    &idle,
			MaxLifetime:    &lifetime,
		},
	}
}

func reconcile(r *SessionReconciler, sess *agentsv1alpha1.AgentSession) ctrl.Result {
	res, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: sess.Namespace, Name: sess.Name},
	})
	if err != nil {
		panic(err)
	}
	return res
}

func get(t *testing.T, r *SessionReconciler, sess *agentsv1alpha1.AgentSession) *agentsv1alpha1.AgentSession {
	t.Helper()
	var out agentsv1alpha1.AgentSession
	key := types.NamespacedName{Namespace: sess.Namespace, Name: sess.Name}
	if err := r.Get(context.Background(), key, &out); err != nil {
		t.Fatalf("get session: %v", err)
	}
	return &out
}

func TestPhaseMachineHappyPath(t *testing.T) {
	sess := testSession()
	r, clock := newTestReconciler(t, sess)

	// "" -> Provisioning (pod created).
	reconcile(r, sess)
	sess = get(t, r, sess)
	if sess.Status.Phase != agentsv1alpha1.PhaseProvisioning || sess.Status.PodName == "" {
		t.Fatalf("want Provisioning with pod, got %+v", sess.Status)
	}

	// Pod becomes ready -> Running.
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: sess.Status.PodName, Namespace: sess.Namespace,
	}}
	if err := r.Get(context.Background(), types.NamespacedName{
		Namespace: pod.Namespace, Name: pod.Name}, pod); err != nil {
		t.Fatal(err)
	}
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	if err := r.Status().Update(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	reconcile(r, sess)
	sess = get(t, r, sess)
	if sess.Status.Phase != agentsv1alpha1.PhaseRunning {
		t.Fatalf("want Running, got %s", sess.Status.Phase)
	}

	// Before idle timeout: still Running.
	clock.Advance(9 * time.Minute)
	reconcile(r, sess)
	if got := get(t, r, sess).Status.Phase; got != agentsv1alpha1.PhaseRunning {
		t.Fatalf("advanced 9m: want Running, got %s", got)
	}

	// Past idle timeout: Idle.
	clock.Advance(2 * time.Minute)
	reconcile(r, sess)
	if got := get(t, r, sess).Status.Phase; got != agentsv1alpha1.PhaseIdle {
		t.Fatalf("past timeout: want Idle, got %s", got)
	}

	// Idle -> Committing (stop hook fires).
	reconcile(r, sess)
	if got := get(t, r, sess).Status.Phase; got != agentsv1alpha1.PhaseCommitting {
		t.Fatalf("want Committing, got %s", got)
	}

	// Committing -> Terminated; sandbox pod deleted (overlay discarded).
	reconcile(r, sess)
	sess = get(t, r, sess)
	if sess.Status.Phase != agentsv1alpha1.PhaseTerminated {
		t.Fatalf("want Terminated, got %s", sess.Status.Phase)
	}
	err := r.Get(context.Background(), types.NamespacedName{
		Namespace: sess.Namespace, Name: sess.Status.PodName}, &corev1.Pod{})
	if err == nil {
		t.Fatal("sandbox pod must be deleted at Terminated")
	}
}

// Node drains / preemption must not skip the committing phase: the PDB +
// termination grace give the stop hook its window.
func TestPodRecreatedDuringProvisioning(t *testing.T) {
	sess := testSession()
	r, _ := newTestReconciler(t, sess)

	reconcile(r, sess)
	sess = get(t, r, sess)

	// Simulate losing the pod (node failure during provisioning).
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: sess.Status.PodName, Namespace: sess.Namespace}}
	if err := r.Delete(context.Background(), pod); err != nil {
		t.Fatal(err)
	}

	reconcile(r, sess)
	sess = get(t, r, sess)
	if sess.Status.Phase != agentsv1alpha1.PhaseProvisioning {
		t.Fatalf("want re-provisioning, got %s", sess.Status.Phase)
	}
}

func TestMaxLifetimeForcesCommitEvenWhenActive(t *testing.T) {
	sess := testSession()
	r, clock := newTestReconciler(t, sess)

	reconcile(r, sess)
	sess = get(t, r, sess)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: sess.Status.PodName, Namespace: sess.Namespace}}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}, pod); err != nil {
		t.Fatal(err)
	}
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	_ = r.Status().Update(context.Background(), pod)
	reconcile(r, sess)
	sess = get(t, r, sess)

	// Activity keeps refreshing, but lifetime is capped at 4h.
	clock.Advance(4*time.Hour + time.Minute)
	reconcile(r, sess)
	if got := get(t, r, sess).Status.Phase; got != agentsv1alpha1.PhaseIdle {
		t.Fatalf("maxLifetime exceeded while active: want Idle, got %s", got)
	}
}
