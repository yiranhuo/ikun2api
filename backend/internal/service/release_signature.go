package service

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"strings"
)

const ReleaseRepository = "yiranhuo/ikun2api"
const ReleaseSigningPublicKey = "y9uMni6i/8vgqKKnLrupN2wA5lUGk93TJejY8RNxGsI="

func verifyReleaseSignature(checksums, signature []byte) error {
	key, err := base64.StdEncoding.DecodeString(ReleaseSigningPublicKey)
	if err != nil {
		return fmt.Errorf("invalid embedded release key")
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(signature)))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("invalid release signature")
	}
	if !ed25519.Verify(ed25519.PublicKey(key), checksums, sig) {
		return fmt.Errorf("release signature verification failed")
	}
	return nil
}
