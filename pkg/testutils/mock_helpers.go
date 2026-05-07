package testutils
import (
	"time"
	"github.com/sourcenetwork/defradb/keyring"
	"github.com/sourcenetwork/defradb/crypto"
	"github.com/sourcenetwork/immutable"
	"github.com/sourcenetwork/defradb/acp/identity"
)
// MockKeyring implements keyring.Keyring for testing.
type MockKeyring struct {
	Data   map[string][]byte
	GetErr error
}

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

func (m *MockKeyring) Set(name string, data []byte) error {
	if m.Data == nil {
		m.Data = make(map[string][]byte)
	}
	m.Data[name] = data
	return nil
}

func (m *MockKeyring) Delete(name string) error {
	delete(m.Data, name)
	return nil
}

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

func (m *MockFullIdentity) PublicKey() crypto.PublicKey { return m.PubKey }
func (m *MockFullIdentity) DID() string                 { return "did:key:mock" }
func (m *MockFullIdentity) ToPublicRawIdentity() identity.PublicRawIdentity {
	return identity.PublicRawIdentity{}
}
func (m *MockFullIdentity) BearerToken() string           { return "" }
func (m *MockFullIdentity) PrivateKey() crypto.PrivateKey { return m.PrivKey }
func (m *MockFullIdentity) IntoRawIdentity() identity.RawIdentity {
	return identity.RawIdentity{}
}
func (m *MockFullIdentity) NewToken(
	duration time.Duration,
	audience immutable.Option[string],
	authorizedAccount immutable.Option[string],
) ([]byte, error) {
	return nil, nil
}
func (m *MockFullIdentity) SetBearerToken(token string) {}
func (m *MockFullIdentity) UpdateToken(
	duration time.Duration,
	audience immutable.Option[string],
	authorizedAccount immutable.Option[string],
) error {
	return nil
}

// MockIdentityNotFull implements identity.Identity but NOT FullIdentity.
type MockIdentityNotFull struct{}

func (m *MockIdentityNotFull) PublicKey() crypto.PublicKey { return nil }
func (m *MockIdentityNotFull) DID() string                 { return "did:key:mock" }
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

func (m *MockPrivateKey) Equal(crypto.Key) bool       { return false }
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