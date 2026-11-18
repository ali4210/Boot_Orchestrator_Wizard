package engine

import (
	"bytes"
	"fmt"
	"io"

	"github.com/ProtonMail/go-crypto/openpgp"
)

// VerifyDetachedSignature checks that signatureData is a valid OpenPGP
// detached signature over the bytes read from signedContent, made by a
// key present in armoredKeyring (an ASCII-armored public keyring).
// Audit item 2.7: a SHA256 checksum alone does not protect against a
// compromised mirror serving a tampered file alongside a tampered
// checksum of its own — only a signature from the distro's actual
// publishing key (obtained independently of the mirror being verified)
// closes that gap. Accepts both ASCII-armored (".asc") and binary
// (".sig"/".gpg") detached signature formats, since distros are not
// consistent about which they publish. Returns the signing identity's
// self-reported name/email (or its key fingerprint, if the key carries no
// identity) on success — callers should log this so a user can see WHO
// supposedly signed the payload, not just that "some" signature matched.
//
// Deliberately uses github.com/ProtonMail/go-crypto/openpgp rather than
// the standard library's golang.org/x/crypto/openpgp (already a
// dependency of this module via other packages): upstream has marked
// x/crypto/openpgp deprecated and explicitly "unsafe by design," and
// recommends this fork as the maintained drop-in replacement — using the
// deprecated one for a security control would be the wrong call here.
func VerifyDetachedSignature(signedContent io.Reader, signatureData []byte, armoredKeyring []byte) (signerIdentity string, err error) {
	keyring, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(armoredKeyring))
	if err != nil {
		return "", fmt.Errorf("parsing signing keyring: %w", err)
	}

	var signer *openpgp.Entity
	if looksArmored(signatureData) {
		signer, err = openpgp.CheckArmoredDetachedSignature(keyring, signedContent, bytes.NewReader(signatureData), nil)
	} else {
		signer, err = openpgp.CheckDetachedSignature(keyring, signedContent, bytes.NewReader(signatureData), nil)
	}
	if err != nil {
		return "", fmt.Errorf("signature verification failed: %w", err)
	}
	if signer == nil {
		return "", fmt.Errorf("signature verification returned no signer")
	}

	for _, identity := range signer.Identities {
		if identity != nil && identity.Name != "" {
			return identity.Name, nil
		}
	}
	return fmt.Sprintf("key %X (no self-reported identity)", signer.PrimaryKey.Fingerprint), nil
}

// looksArmored reports whether data appears to be ASCII-armored OpenPGP
// (begins with the standard "-----BEGIN PGP SIGNATURE-----" header,
// allowing for leading whitespace) rather than raw binary packet data.
func looksArmored(data []byte) bool {
	trimmed := bytes.TrimLeft(data, " \t\r\n")
	const marker = "-----BEGIN PGP SIGNATURE-----"
	return len(trimmed) >= len(marker) && bytes.Equal(trimmed[:len(marker)], []byte(marker))
}
