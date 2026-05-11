package testutils

import (
	"time"

	"github.com/sourcenetwork/defradb/acp/identity"
	"github.com/sourcenetwork/defradb/crypto"
	"github.com/sourcenetwork/defradb/keyring"
	"github.com/sourcenetwork/immutable"
)

// MockKeyring implements keyring.Keyring for testing.
type MockKeyring struct {
	Data   map[string][]byte
	GetErr error
}

// Get returns the value for a key or an injected error.
func (m *MockKeyring) Get(name string) ([]byte, error) {
	if m.GetErr != nil {
		return nil, m.GetErr
	}
	v, ok := m.Data[name]
	if !ok {
		return nil, keyring.ErrNotFound
	}
	return v, nil
}

// Set stores a key/value pair in the in-memory mock keyring.
func (m *MockKeyring) Set(name string, data []byte) error {
	if m.Data == nil {
		m.Data = make(map[string][]byte)
	}
	m.Data[name] = data
	return nil
}

// Delete removes a key from the in-memory mock keyring.
func (m *MockKeyring) Delete(name string) error {
	delete(m.Data, name)
	return nil
}

// List returns all keys currently stored in the mock keyring.
func (m *MockKeyring) List() ([]string, error) {
	keys := make([]string, 0, len(m.Data))
	for k := range m.Data {
		keys = append(keys, k)
	}
	return keys, nil
}

// MockFullIdentity implements identity.FullIdentity for testing.
type MockFullIdentity struct {
	PrivKey crypto.PrivateKey
	PubKey  crypto.PublicKey
}

// PublicKey returns the configured mock public key.
func (m *MockFullIdentity) PublicKey() crypto.PublicKey { return m.PubKey }
// DID returns a fixed mock DID string.
func (m *MockFullIdentity) DID() string                 { return "did:key:mock" }
// ToPublicRawIdentity returns an empty mock public raw identity.
func (m *MockFullIdentity) ToPublicRawIdentity() identity.PublicRawIdentity {
	return identity.PublicRawIdentity{}
}
// BearerToken returns an empty token for mock identity usage.
func (m *MockFullIdentity) BearerToken() string           { return "" }
// PrivateKey returns the configured mock private key.
func (m *MockFullIdentity) PrivateKey() crypto.PrivateKey { return m.PrivKey }
// IntoRawIdentity returns an empty mock raw identity.
func (m *MockFullIdentity) IntoRawIdentity() identity.RawIdentity {
	return identity.RawIdentity{}
}

// NewToken returns no token and no error for testing flows.
func (m *MockFullIdentity) NewToken(
	duration time.Duration,
	audience immutable.Option[string],
	authorizedAccount immutable.Option[string],
) ([]byte, error) {
	return nil, nil
}
// SetBearerToken is a no-op for the mock full identity.
func (m *MockFullIdentity) SetBearerToken(token string) {}
// UpdateToken is a no-op update that returns nil in tests.
func (m *MockFullIdentity) UpdateToken(
	duration time.Duration,
	audience immutable.Option[string],
	authorizedAccount immutable.Option[string],
) error {
	return nil
}

// MockIdentityNotFull implements identity.Identity but NOT FullIdentity.
type MockIdentityNotFull struct{}

// PublicKey returns nil to represent missing full identity behavior.
func (m *MockIdentityNotFull) PublicKey() crypto.PublicKey { return nil }
// DID returns a fixed mock DID string.
func (m *MockIdentityNotFull) DID() string                 { return "did:key:mock" }
// ToPublicRawIdentity returns an empty mock public raw identity.
func (m *MockIdentityNotFull) ToPublicRawIdentity() identity.PublicRawIdentity {
	return identity.PublicRawIdentity{}
}

// MockPrivateKey implements crypto.PrivateKey for testing edge cases.
type MockPrivateKey struct {
	RawBytes []byte
	SignErr  error
	SignData []byte
	PubKey   crypto.PublicKey
}

// Equal reports false for all key comparisons in this mock.
func (m *MockPrivateKey) Equal(crypto.Key) bool       { return false }
// Raw returns configured raw private key bytes.
func (m *MockPrivateKey) Raw() []byte                 { return m.RawBytes }
func (m *MockPrivateKey) String() string              { return "mock-private-key" }
func (m *MockPrivateKey) Type() crypto.KeyType        { return crypto.KeyTypeSecp256k1 }
func (m *MockPrivateKey) Underlying() any             { return nil }
func (m *MockPrivateKey) GetPublic() crypto.PublicKey { return m.PubKey }
func (m *MockPrivateKey) Sign(data []byte) ([]byte, error) {
	if m.SignErr != nil {
		return nil, m.SignErr
	}
	return m.SignData, nil
}
