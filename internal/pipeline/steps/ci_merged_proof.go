package steps

import (
	"context"
	"fmt"

	"github.com/kunchenguid/no-mistakes/internal/scm"
)

// validateMergedState verifies provider evidence for hosts that can prove which
// exact PR head was merged. Providers without that optional capability retain
// their existing lifecycle behavior.
func validateMergedState(ctx context.Context, host scm.Host, pr *scm.PR, expectedHead string) error {
	if !host.Capabilities().MergedProof {
		return nil
	}
	proofHost, ok := host.(scm.MergedProofHost)
	if !ok {
		return fmt.Errorf("SCM provider advertises merged proof but does not implement it")
	}
	proof, err := proofHost.GetMergedProof(ctx, pr, expectedHead)
	if err != nil {
		return fmt.Errorf("verify merged PR proof: %w", err)
	}
	if !proof.Merged {
		return fmt.Errorf("verify merged PR proof: PR %s is not merged", pr.Number)
	}
	if proof.Number != pr.Number || proof.URL != pr.URL {
		return fmt.Errorf("verify merged PR proof: proof identifies PR %s at %q, want PR %s at %q", proof.Number, proof.URL, pr.Number, pr.URL)
	}
	if expectedHead != "" && proof.HeadSHA != expectedHead {
		return fmt.Errorf("verify merged PR proof: %w: expected %s, got %s", scm.ErrHeadChanged, expectedHead, proof.HeadSHA)
	}
	return nil
}
