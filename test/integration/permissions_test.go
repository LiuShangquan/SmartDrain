package integration

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/smartdrain/smartdrain/internal/testutil"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func TestUIDPreconditionsAndDefaultRBAC(t *testing.T) {
	if os.Getenv("SMARTDRAIN_API_TESTS") != "1" {
		t.Skip("set SMARTDRAIN_API_TESTS=1")
	}
	ctx := context.Background()
	cfg, err := clientcmd.BuildConfigFromFlags("", os.Getenv("KUBECONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	admin, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.CoreV1().Nodes().Get(ctx, "smartdrain-dev-worker", metav1.GetOptions{}); err != nil {
		t.Fatal("requires dedicated test cluster", err)
	}
	ns := fmt.Sprintf("sd-api-%d", time.Now().UnixNano())
	if _, err := admin.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.CoreV1().Namespaces().Delete(ctx, ns, metav1.DeleteOptions{}) })
	p := testutil.Pod("same-name", "", "100m")
	p.Namespace = ns
	p.UID = ""
	p.Status = corev1.PodStatus{}
	p.Spec.NodeSelector = map[string]string{"smartdrain.dev/nonexistent": "true"}
	original, err := admin.CoreV1().Pods(ns).Create(ctx, p, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	zero := int64(0)
	if err := admin.CoreV1().Pods(ns).Delete(ctx, p.Name, metav1.DeleteOptions{GracePeriodSeconds: &zero, Preconditions: &metav1.Preconditions{UID: &original.UID}}); err != nil {
		t.Fatal(err)
	}
	if err := wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 10*time.Second, true, func(context.Context) (bool, error) {
		_, err := admin.CoreV1().Pods(ns).Get(ctx, p.Name, metav1.GetOptions{})
		return apierrors.IsNotFound(err), nil
	}); err != nil {
		t.Fatal(err)
	}
	replacement, err := admin.CoreV1().Pods(ns).Create(ctx, p, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	err = admin.PolicyV1().Evictions(ns).Evict(ctx, &policyv1.Eviction{ObjectMeta: metav1.ObjectMeta{Name: p.Name, Namespace: ns}, DeleteOptions: &metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &original.UID}}})
	if !apierrors.IsConflict(err) {
		t.Fatalf("old-UID eviction did not conflict: %v", err)
	}
	current, err := admin.CoreV1().Pods(ns).Get(ctx, p.Name, metav1.GetOptions{})
	if err != nil || current.UID != replacement.UID || current.DeletionTimestamp != nil {
		t.Fatal("same-name replacement was touched", err)
	}
	saConfig := rest.CopyConfig(cfg)
	saConfig.Impersonate = rest.ImpersonationConfig{UserName: "system:serviceaccount:smartdrain-system:smartdrain-controller-manager"}
	sa, err := kubernetes.NewForConfig(saConfig)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		verb, resource, sub string
		allowed             bool
	}{{"delete", "pods", "", false}, {"create", "pods", "eviction", true}, {"patch", "nodes", "", true}, {"patch", "deployments", "", false}, {"delete", "persistentvolumeclaims", "", false}} {
		group := ""
		if tc.resource == "deployments" {
			group = "apps"
		}
		review, err := sa.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authorizationv1.SelfSubjectAccessReview{Spec: authorizationv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &authorizationv1.ResourceAttributes{Verb: tc.verb, Group: group, Resource: tc.resource, Subresource: tc.sub, Namespace: ns}}}, metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if review.Status.Allowed != tc.allowed {
			t.Fatalf("unexpected default permission %+v: %+v", tc, review.Status)
		}
	}
}
