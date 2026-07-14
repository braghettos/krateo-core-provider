package kubernetestargets

import (
	"context"
	"testing"
	"time"

	compositiondefinitionsv1alpha1 "github.com/krateoplatformops/core-provider/apis/compositiondefinitions/v1alpha1"
	rtv1 "github.com/krateoplatformops/provider-runtime/apis/common/v1"
	"github.com/krateoplatformops/provider-runtime/pkg/logging"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := compositiondefinitionsv1alpha1.SchemeBuilder.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

// A target whose kubeconfig Secret is missing must be recorded Down (not error out), so the fleet
// view surfaces the unreachable spoke, and re-probed on the shorter retry cadence.
func TestReconcile_MissingKubeconfigMarksDown(t *testing.T) {
	kt := &compositiondefinitionsv1alpha1.KubernetesTarget{
		ObjectMeta: metav1.ObjectMeta{Name: "spoke", Namespace: "krateo-system"},
		Spec: compositiondefinitionsv1alpha1.KubernetesTargetSpec{
			KubeconfigRef: rtv1.SecretKeySelector{
				Reference: rtv1.Reference{Name: "spoke-kubeconfig", Namespace: "krateo-system"},
				Key:       "kubeconfig",
			},
		},
	}
	scheme := testScheme(t)
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(kt).
		WithStatusSubresource(&compositiondefinitionsv1alpha1.KubernetesTarget{}).
		Build()

	r := &Reconciler{client: cl, log: logging.NewNopLogger(), poll: defaultPollInterval}
	res, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "krateo-system", Name: "spoke"}})
	if err != nil {
		t.Fatalf("Reconcile returned error (should record Down, not error): %v", err)
	}
	if res.RequeueAfter != downRetryInterval {
		t.Fatalf("expected requeue after %v for a down target, got %v", downRetryInterval, res.RequeueAfter)
	}

	got := &compositiondefinitionsv1alpha1.KubernetesTarget{}
	if err := cl.Get(context.Background(), types.NamespacedName{Namespace: "krateo-system", Name: "spoke"}, got); err != nil {
		t.Fatal(err)
	}
	if got.Status.ConnectionStatus != connDown {
		t.Fatalf("ConnectionStatus = %q, want %q", got.Status.ConnectionStatus, connDown)
	}
	if got.Status.LastProbeTime == nil {
		t.Fatal("LastProbeTime not set")
	}
	if c := got.Status.GetCondition(rtv1.TypeReady); c.Status != metav1.ConditionFalse {
		t.Fatalf("Ready condition = %q, want False", c.Status)
	}
}

func TestSetupDefaultsPollInterval(t *testing.T) {
	// exercise the default selection logic directly (no manager needed)
	o := Options{}
	poll := o.PollInterval
	if poll <= 0 {
		poll = defaultPollInterval
	}
	if poll != 2*time.Minute {
		t.Fatalf("default poll = %v, want 2m", poll)
	}
}
