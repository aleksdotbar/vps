package releases

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// SnapshotSHA256 preserves the stored release fingerprint contract, including
// its original JSON field names/order. Inputs contain references, never secret
// values; this hash may safely travel with public operation observations.
func SnapshotSHA256(config, domains []byte, owner string, secretVersion int64) (string, error) {
	data, err := json.Marshal(struct {
		Config        json.RawMessage
		Domains       json.RawMessage
		Owner         string
		SecretVersion int64
	}{config, domains, owner, secretVersion})
	if err != nil {
		return "", ErrInvalidArtifact
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}
