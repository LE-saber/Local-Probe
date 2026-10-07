package commandpath

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/LE-saber/Local-Probe/internal/policy"
)

type testBinding struct{}

func (testBinding) MarshalJSON() ([]byte, error)  { return nil, ErrLocalOnly }
func (testBinding) PreviewToken() (string, error) { return "", ErrUnsupported }
func (testBinding) Commitment() [32]byte          { return [32]byte{} }
func (testBinding) Revalidate(context.Context) error {
	return ErrUnsupported
}
func (testBinding) Close() error { return nil }

type testResolver struct{}

func (testResolver) MarshalJSON() ([]byte, error) { return nil, ErrLocalOnly }
func (testResolver) Resolve(context.Context, policy.BoundScope, string, string) (PathBinding, error) {
	return nil, ErrUnsupported
}

func TestLocalOnlyInterfacesRequireExplicitJSONRejection(t *testing.T) {
	var binding PathBinding = testBinding{}
	if _, err := json.Marshal(binding); !errors.Is(err, ErrLocalOnly) {
		t.Fatalf("PathBinding JSON error = %v, want ErrLocalOnly", err)
	}
	var resolver TrustedResolver = testResolver{}
	if _, err := json.Marshal(resolver); !errors.Is(err, ErrLocalOnly) {
		t.Fatalf("TrustedResolver JSON error = %v, want ErrLocalOnly", err)
	}
}
