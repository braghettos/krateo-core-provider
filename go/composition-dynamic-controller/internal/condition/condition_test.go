package condition

import (
	"testing"

	"github.com/krateo-platformops/unstructured-runtime/pkg/tools/unstructured/condition"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestReconcileGracefullyPaused(t *testing.T) {
	result := ReconcileGracefullyPaused()

	// Test Type
	if result.Type != condition.TypeReady {
		t.Errorf("Expected Type to be %s, got %s", condition.TypeReady, result.Type)
	}

	// Test Status
	if result.Status != metav1.ConditionTrue {
		t.Errorf("Expected Status to be %s, got %s", metav1.ConditionTrue, result.Status)
	}

	// Test Reason
	if result.Reason != ReasonReconcileGracefullyPaused {
		t.Errorf("Expected Reason to be %s, got %s", ReasonReconcileGracefullyPaused, result.Reason)
	}

	// Test LastTransitionTime is set (not zero)
	if result.LastTransitionTime.IsZero() {
		t.Error("Expected LastTransitionTime to be set, got zero time")
	}

	// Test Message is empty (default)
	if result.Message != "" {
		t.Errorf("Expected Message to be empty, got %s", result.Message)
	}
}

func TestReasonReconcileGracefullyPausedConstant(t *testing.T) {
	expected := "ReconcileGracefullyPaused"
	if ReasonReconcileGracefullyPaused != expected {
		t.Errorf("Expected constant to be %s, got %s", expected, ReasonReconcileGracefullyPaused)
	}
}
