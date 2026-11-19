package integration

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestKubectlSamples exercises client schema validation as well as server defaults.
func TestKubectlSamples(t *testing.T) {
	if os.Getenv("SMARTDRAIN_API_TESTS") != "1" {
		t.Skip("set SMARTDRAIN_API_TESTS=1 with the dedicated test kubeconfig")
	}
	kubectl := os.Getenv("SMARTDRAIN_KUBECTL")
	if kubectl == "" {
		kubectl = "kubectl"
	}
	for _, sample := range []string{"guarded.yaml", "compatible.yaml", "plan.yaml"} {
		t.Run(sample, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			path := filepath.Join("..", "..", "config", "samples", sample)
			if out, err := exec.CommandContext(ctx, kubectl, "apply", "--dry-run=server", "-f", path).CombinedOutput(); err != nil {
				t.Fatalf("sample cannot be submitted: %v: %s", err, out)
			}
		})
	}
}
