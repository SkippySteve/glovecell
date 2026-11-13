package hubspoke

import "context"

// T3Runtime is the spoke side for T3 Code (`t3 serve`). Not verified yet.
// Candidate offer: `t3 auth pairing create` with a scoped token.
type T3Runtime struct{}

func (T3Runtime) Kind() string { return "t3" }

func (T3Runtime) ParseOffer([]byte) (Offer, error) {
	return Offer{}, ErrNotImplemented
}

// T3Enroller is the hub side for T3 linked environments (beta). Candidate:
// `t3 environment link --pairing-code … --access …`. Phone visibility of
// linked machines through a hub is unverified.
type T3Enroller struct{}

func (T3Enroller) Kind() string { return "t3" }

func (T3Enroller) Enroll(context.Context, string, Offer) (Enrolled, error) {
	return Enrolled{}, ErrNotImplemented
}
func (T3Enroller) Connect(context.Context, string) error { return ErrNotImplemented }
func (T3Enroller) RegisterProject(context.Context, string, string) error {
	return ErrNotImplemented
}
func (T3Enroller) Revoke(context.Context, string) error       { return ErrNotImplemented }
func (T3Enroller) List(context.Context) ([]Enrolled, error)   { return nil, ErrNotImplemented }

// HerdrRuntime is reserved. The Herdr GUI has no known pairing interface yet.
type HerdrRuntime struct{}

func (HerdrRuntime) Kind() string { return "herdr" }

func (HerdrRuntime) ParseOffer([]byte) (Offer, error) {
	return Offer{}, ErrNotImplemented
}
