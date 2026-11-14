package frpagent

import (
	"encoding/json"
	"io"
	"os"
	"strings"
)

// Only Normalize output (re-derived and compared on restore) may enter here.
// Do not cache the original auth object. frpc can reopen its config, but the
// pinned release resolves its file token source once when initializing auth.
func (s *Store) stageRuntime(name string, normalized []byte) (*os.File, error) {
	if name != "active.json" && name != "candidate.json" {
		return nil, ErrStorage
	}
	var fields map[string]json.RawMessage
	var auth struct {
		Token *string `json:"token"`
	}
	if json.Unmarshal(normalized, &fields) != nil || json.Unmarshal(fields["auth"], &auth) != nil || auth.Token == nil {
		return nil, ErrConfig
	}
	token := *auth.Token
	// FRP trims file-source whitespace. Never silently change a credential.
	if !safeString(token, 1024) || strings.TrimSpace(token) != token {
		return nil, ErrConfig
	}
	fields["auth"] = json.RawMessage(`{"tokenSource":{"type":"file","file":{"path":"/dev/fd/4"}}}`)
	runtime, err := json.Marshal(fields)
	if err != nil {
		return nil, ErrConfig
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, ErrStorage
	}
	// Bounded token fits an empty pipe on both supported systems. Closing the
	// writer before spawning the child also gives its os.ReadFile a definite EOF.
	_, err = io.WriteString(w, token)
	closed := w.Close()
	if err != nil || closed != nil {
		r.Close()
		return nil, ErrStorage
	}
	if err := s.Write(name, runtime, 0600); err != nil {
		r.Close()
		return nil, err
	}
	return r, nil
}
