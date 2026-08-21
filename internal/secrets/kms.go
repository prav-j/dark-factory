package secrets

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/kms"
)

// KMSRootKeys resolves per-version root keys by decrypting versioned
// ciphertext blobs with AWS KMS (production backend for RootKeyProvider).
// The ciphertexts are provisioned out-of-band (Terraform/deploy) and injected
// as config; plaintext keys are cached after first decrypt.
type KMSRootKeys struct {
	Client      *kms.Client
	Ciphertexts map[int][]byte // KEK version -> KMS-wrapped root key

	cache map[int][]byte
}

func NewKMSRootKeys(client *kms.Client, ciphertexts map[int][]byte) *KMSRootKeys {
	return &KMSRootKeys{Client: client, Ciphertexts: ciphertexts, cache: map[int][]byte{}}
}

// RootKey implements RootKeyProvider.
func (k *KMSRootKeys) RootKey(version int) ([]byte, error) {
	if plain, ok := k.cache[version]; ok {
		return plain, nil
	}
	ct, ok := k.Ciphertexts[version]
	if !ok {
		return nil, fmt.Errorf("no KMS ciphertext configured for KEK v%d", version)
	}
	out, err := k.Client.Decrypt(context.Background(), &kms.DecryptInput{
		CiphertextBlob: ct,
	})
	if err != nil {
		return nil, fmt.Errorf("kms decrypt v%d: %w", version, err)
	}
	k.cache[version] = out.Plaintext
	return out.Plaintext, nil
}

// ParseCiphertexts converts base64-encoded config values into the map form.
func ParseCiphertexts(encoded map[string]string) (map[int][]byte, error) {
	out := make(map[int][]byte, len(encoded))
	for v, b64 := range encoded {
		var version int
		if _, err := fmt.Sscanf(v, "%d", &version); err != nil {
			return nil, fmt.Errorf("bad KEK version key %q: %w", v, err)
		}
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("bad ciphertext for v%s: %w", v, err)
		}
		out[version] = raw
	}
	return out, nil
}
