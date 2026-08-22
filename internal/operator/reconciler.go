// Package operator implements the AgentSession controller (specs/16):
// reconcile sessions through Provisioning -> Running -> Idle -> Committing
// -> Terminated, with idle detection driving graceful shutdown via the
// harness stop hook.
package operator

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	agentsv1alpha1 "github.com/prav-j/dark-factory/api/v1alpha1"
)

const (
	defaultIdleTimeout = 10 * time.Minute

	// CommitGrace bounds the stop-hook commit window before the pod dies.
	// The harness receives it as the preStop grace; PDBs protect mid-commit
	// sessions from node drains.
	CommitGrace = 2 * time.Minute
)

// Clock abstracts time for deterministic reconciliation tests.
type Clock interface{ Now() time.Time }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// SessionReconciler drives AgentSession phase transitions.
type SessionReconciler struct {
	client.Client
	SandboxImage string // session pod image (snapshot-derived in prod)
	Clock        Clock
}

func NewReconciler(c client.Client, sandboxImage string) *SessionReconciler {
	return &SessionReconciler{Client: c, SandboxImage: sandboxImage, Clock: realClock{}}
}

// Reconcile implements one step of the phase machine per invocation.
func (r *SessionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var sess agentsv1alpha1.AgentSession
	if err := r.Get(ctx, req.NamespacedName, &sess); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	switch sess.Status.Phase {
	case "":
		return r.toProvisioning(ctx, &sess)
	case agentsv1alpha1.PhaseProvisioning:
		return r.checkPodReady(ctx, &sess)
	case agentsv1alpha1.PhaseRunning:
		return r.detectIdle(ctx, &sess)
	case agentsv1alpha1.PhaseIdle:
		return r.beginCommit(ctx, &sess)
	case agentsv1alpha1.PhaseCommitting:
		return r.finishCommit(ctx, &sess)
	case agentsv1alpha1.PhaseTerminated:
		return ctrl.Result{}, nil
	}
	logger.Info("unknown phase", "phase", sess.Status.Phase)
	return ctrl.Result{}, nil
}

func (r *SessionReconciler) toProvisioning(ctx context.Context, sess *agentsv1alpha1.AgentSession) (ctrl.Result, error) {
	pod := r.sessionPod(sess)
	if err := r.Create(ctx, pod); err != nil && !apierrors.IsAlreadyExists(err) {
		return ctrl.Result{}, err
	}
	now := metav1.NewTime(r.Clock.Now())
	sess.Status.Phase = agentsv1alpha1.PhaseProvisioning
	sess.Status.PodName = pod.Name
	sess.Status.StartedAt = &now
	return r.updateStatus(ctx, sess, 2*time.Second)
}

func (r *SessionReconciler) checkPodReady(ctx context.Context, sess *agentsv1alpha1.AgentSession) (ctrl.Result, error) {
	var pod corev1.Pod
	err := r.Get(ctx, types.NamespacedName{Namespace: sess.Namespace, Name: sess.Status.PodName}, &pod)
	if apierrors.IsNotFound(err) {
		return r.toProvisioning(ctx, sess) // recreate lost pods
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			sess.Status.Phase = agentsv1alpha1.PhaseRunning
			now := metav1.NewTime(r.Clock.Now())
			sess.Status.LastActivity = &now
			return r.updateStatus(ctx, sess, 0)
		}
	}
	return r.updateStatus(ctx, sess, 5*time.Second) // keep waiting on readiness
}

// detectIdle transitions Running -> Idle after idleTimeout without activity.
// The hard maxLifetime cap forces the same transition regardless of activity.
func (r *SessionReconciler) detectIdle(ctx context.Context, sess *agentsv1alpha1.AgentSession) (ctrl.Result, error) {
	timeout := defaultIdleTimeout
	if sess.Spec.IdleTimeout != nil {
		timeout = sess.Spec.IdleTimeout.Duration
	}
	now := r.Clock.Now()

	if sess.Spec.MaxLifetime != nil && sess.Status.StartedAt != nil &&
		now.Sub(sess.Status.StartedAt.Time) >= sess.Spec.MaxLifetime.Duration {
		sess.Status.Phase = agentsv1alpha1.PhaseIdle
		return r.updateStatus(ctx, sess, 0)
	}

	last := time.Time{}
	if sess.Status.LastActivity != nil {
		last = sess.Status.LastActivity.Time
	}
	if !last.IsZero() && now.Sub(last) >= timeout {
		sess.Status.Phase = agentsv1alpha1.PhaseIdle
		return r.updateStatus(ctx, sess, 0)
	}

	requeue := timeout
	if !last.IsZero() {
		if remaining := timeout - now.Sub(last); remaining > 0 {
			requeue = remaining
		}
	}
	return r.updateStatus(ctx, sess, requeue)
}

// beginCommit fires the harness stop hook: the pod's preStop lifecycle hook
// signals the harness, which commits work to git and emits the session
// manifest within CommitGrace (specs/16.2).
func (r *SessionReconciler) beginCommit(ctx context.Context, sess *agentsv1alpha1.AgentSession) (ctrl.Result, error) {
	sess.Status.Phase = agentsv1alpha1.PhaseCommitting
	return r.updateStatus(ctx, sess, CommitGrace)
}

// finishCommit terminates: the overlay is discarded — durable state is git +
// transcript + manifest. There is deliberately no filesystem checkpointing.
func (r *SessionReconciler) finishCommit(ctx context.Context, sess *agentsv1alpha1.AgentSession) (ctrl.Result, error) {
	var pod corev1.Pod
	err := r.Get(ctx, types.NamespacedName{Namespace: sess.Namespace, Name: sess.Status.PodName}, &pod)
	if err == nil {
		if delErr := r.Delete(ctx, &pod); delErr != nil && !apierrors.IsNotFound(delErr) {
			return ctrl.Result{}, delErr
		}
	} else if !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	now := metav1.NewTime(r.Clock.Now())
	sess.Status.Phase = agentsv1alpha1.PhaseTerminated
	sess.Status.TerminatedAt = &now
	return r.updateStatus(ctx, sess, 0)
}

func (r *SessionReconciler) updateStatus(ctx context.Context, sess *agentsv1alpha1.AgentSession, requeueAfter time.Duration) (ctrl.Result, error) {
	if err := r.Status().Update(ctx, sess); err != nil {
		return ctrl.Result{}, err
	}
	res := ctrl.Result{}
	if requeueAfter > 0 {
		res.RequeueAfter = requeueAfter
	}
	return res, nil
}

func (r *SessionReconciler) sessionPod(sess *agentsv1alpha1.AgentSession) *corev1.Pod {
	grace := int64(CommitGrace.Seconds())

	labels := map[string]string{
		"app":                         "dark-factory-sandbox",
		"agents.platform/session":     sess.Name,
		"agents.platform/org":         sess.Spec.OrgID,
		"agents.platform/user":        sess.Spec.UserID,
		"agents.platform/environment": sanitizeLabel(sess.Spec.EnvironmentKey),
	}

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: sess.Name + "-sandbox", Namespace: sess.Namespace, Labels: labels},
		Spec: corev1.PodSpec{
			PriorityClassName:             sess.Spec.PriorityClass,
			AutomountServiceAccountToken:  boolPtr(false),
			TerminationGracePeriodSeconds: &grace,
			Containers: []corev1.Container{{
				Name:         "harness",
				Image:        r.SandboxImage,
				VolumeMounts: []corev1.VolumeMount{{Name: "workspace", MountPath: "/workspace"}},
			}},
			Volumes: []corev1.Volume{{
				Name:         "workspace",
				VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}, // COW overlay in prod (§15)
			}},
		},
	}
}

func boolPtr(b bool) *bool { return &b }

// SetupWithManager registers the controller.
func (r *SessionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Clock == nil {
		r.Clock = realClock{}
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentsv1alpha1.AgentSession{}).
		Complete(r)
}

// sanitizeLabel makes a value k8s-label-safe (alphanumerics, '-', '_', '.';
// must start/end alphanumeric). Colons in snapshot keys ("sha256:abc") are
// the common offender.
func sanitizeLabel(v string) string {
	out := make([]byte, 0, len(v))
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
			out = append(out, c)
		default:
			out = append(out, '-')
		}
	}
	if len(out) == 0 {
		return "none"
	}
	if out[0] == '-' || out[0] == '_' || out[0] == '.' {
		out[0] = 'x'
	}
	last := out[len(out)-1]
	if last == '-' || last == '_' || last == '.' {
		out[len(out)-1] = 'x'
	}
	return string(out)
}
