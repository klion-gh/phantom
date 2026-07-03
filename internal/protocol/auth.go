package protocol

import (
	"crypto/hmac"
	"crypto/sha256"
)

// ComputeAuthTag/VerifyAuthTag exist only because internal/tunnel/multiplexer.go
// (ported unchanged from v1) still has an in-band FrameAuth code path. v2
// constructs every Multiplexer with sendAuth=false/expectAuth=false - real
// session authentication happens earlier, during the disguised handshake in
// internal/handshake - so this path is dead code at runtime, kept only so
// multiplexer.go compiles without modification.
func ComputeAuthTag(psk []byte, tlsBinding []byte) []byte {
	mac := hmac.New(sha256.New, psk)
	mac.Write(tlsBinding)
	return mac.Sum(nil)
}

func VerifyAuthTag(psk, tlsBinding, tag []byte) bool {
	expected := ComputeAuthTag(psk, tlsBinding)
	return hmac.Equal(tag, expected)
}
