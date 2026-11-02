package agentauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
)

const enrollmentPrefix = "cne1."
const credentialPrefix = "cna1."

type token struct {
	raw    string
	id     string
	digest [sha256.Size]byte
}

func issueToken(prefix string) (token, error) {
	var id [16]byte
	var secret [32]byte
	if _, err := rand.Read(id[:]); err != nil {
		return token{}, err
	}
	if _, err := rand.Read(secret[:]); err != nil {
		return token{}, err
	}
	public := hex.EncodeToString(id[:])
	raw := prefix + public + "." + base64.RawURLEncoding.EncodeToString(secret[:])
	return token{raw: raw, id: public, digest: sha256.Sum256([]byte(raw))}, nil
}

func parseToken(prefix, raw string) (token, bool) {
	if len(raw) != len(prefix)+32+1+43 || !strings.HasPrefix(raw, prefix) {
		return token{}, false
	}
	body := raw[len(prefix):]
	id := body[:32]
	if body[32] != '.' || strings.ToLower(id) != id {
		return token{}, false
	}
	if _, err := hex.DecodeString(id); err != nil {
		return token{}, false
	}
	if _, err := base64.RawURLEncoding.Strict().DecodeString(body[33:]); err != nil {
		return token{}, false
	}
	return token{id: id, digest: sha256.Sum256([]byte(raw))}, true
}
