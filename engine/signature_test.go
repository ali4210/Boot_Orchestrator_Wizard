package engine

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"

	"boot-orchestrator/discovery"
)

// genTestKeypair generates a real (if deliberately low-cost/fast for
// tests) OpenPGP entity and returns its ASCII-armored public keyring,
// plus the entity itself for signing. No real distro keys are used or
// needed — this test exercises VerifyDetachedSignature's own logic, not
// any particular distro's actual key.
func genTestKeypair(t *testing.T) (armoredPublicKeyring []byte, entity *openpgp.Entity) {
	t.Helper()
	ent, err := openpgp.NewEntity("Test Signer", "test key for signature_test.go", "signer@example.test", nil)
	if err != nil {
		t.Fatalf("generating test OpenPGP entity: %v", err)
	}

	var buf bytes.Buffer
	w, err := armor.Encode(&buf, openpgp.PublicKeyType, nil)
	if err != nil {
		t.Fatalf("opening armor encoder: %v", err)
	}
	if err := ent.Serialize(w); err != nil {
		t.Fatalf("serializing public key: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("closing armor encoder: %v", err)
	}
	return buf.Bytes(), ent
}

func armoredDetachedSign(t *testing.T, entity *openpgp.Entity, content []byte) []byte {
	t.Helper()
	var sigBuf bytes.Buffer
	if err := openpgp.ArmoredDetachSign(&sigBuf, entity, bytes.NewReader(content), nil); err != nil {
		t.Fatalf("signing content: %v", err)
	}
	return sigBuf.Bytes()
}

func binaryDetachedSign(t *testing.T, entity *openpgp.Entity, content []byte) []byte {
	t.Helper()
	var sigBuf bytes.Buffer
	if err := openpgp.DetachSign(&sigBuf, entity, bytes.NewReader(content), nil); err != nil {
		t.Fatalf("signing content: %v", err)
	}
	return sigBuf.Bytes()
}

// TestVerifyDetachedSignature_ArmoredSignatureSucceeds is the baseline:
// a real signature, over the real content, by a key actually present in
// the keyring, must verify and report the signer's identity.
func TestVerifyDetachedSignature_ArmoredSignatureSucceeds(t *testing.T) {
	keyring, entity := genTestKeypair(t)
	content := []byte("this is the ISO content (or a stand-in for it) being verified")
	sig := armoredDetachedSign(t, entity, content)

	identity, err := VerifyDetachedSignature(bytes.NewReader(content), sig, keyring)
	if err != nil {
		t.Fatalf("expected a valid armored signature to verify, got: %v", err)
	}
	if !strings.Contains(identity, "Test Signer") {
		t.Errorf("expected the reported identity to mention the signer's name, got %q", identity)
	}
}

// TestVerifyDetachedSignature_BinarySignatureSucceeds proves the
// non-armored (raw binary .sig/.gpg) format works too — distros are not
// consistent about which they publish.
func TestVerifyDetachedSignature_BinarySignatureSucceeds(t *testing.T) {
	keyring, entity := genTestKeypair(t)
	content := []byte("binary-signature content")
	sig := binaryDetachedSign(t, entity, content)

	if looksArmored(sig) {
		t.Fatalf("test setup bug: binaryDetachedSign produced armored output")
	}

	identity, err := VerifyDetachedSignature(bytes.NewReader(content), sig, keyring)
	if err != nil {
		t.Fatalf("expected a valid binary signature to verify, got: %v", err)
	}
	if identity == "" {
		t.Errorf("expected a non-empty signer identity")
	}
}

// TestVerifyDetachedSignature_TamperedContentFails is the actual security
// property under test: a signature that is valid for the ORIGINAL content
// must NOT verify against content that has since been modified (the
// "compromised mirror serves a tampered file" scenario this whole feature
// exists to catch).
func TestVerifyDetachedSignature_TamperedContentFails(t *testing.T) {
	keyring, entity := genTestKeypair(t)
	original := []byte("the real, untampered payload")
	sig := armoredDetachedSign(t, entity, original)

	tampered := []byte("the real, untampered payload, but with one byte changed!")
	if _, err := VerifyDetachedSignature(bytes.NewReader(tampered), sig, keyring); err == nil {
		t.Fatalf("expected verification to fail against tampered content, but it succeeded")
	}
}

// TestVerifyDetachedSignature_WrongKeyFails proves a signature made by a
// DIFFERENT key (an attacker's own key, signing their own tampered
// payload+checksum) is rejected when checked against the real
// distro's keyring, not just "some signature exists."
func TestVerifyDetachedSignature_WrongKeyFails(t *testing.T) {
	realKeyring, _ := genTestKeypair(t)
	_, attackerEntity := genTestKeypair(t)

	content := []byte("attacker-controlled payload")
	attackerSig := armoredDetachedSign(t, attackerEntity, content)

	if _, err := VerifyDetachedSignature(bytes.NewReader(content), attackerSig, realKeyring); err == nil {
		t.Fatalf("expected a signature from a key not in the keyring to be rejected")
	}
}

// TestVerifyDetachedSignature_CorruptSignatureBytesFails proves garbage
// signature bytes (truncated download, HTML error page served instead of
// the real .sig/.gpg file, ...) fail cleanly with an error, not a panic.
func TestVerifyDetachedSignature_CorruptSignatureBytesFails(t *testing.T) {
	keyring, _ := genTestKeypair(t)
	content := []byte("some content")

	if _, err := VerifyDetachedSignature(bytes.NewReader(content), []byte("<html>404 not found</html>"), keyring); err == nil {
		t.Fatalf("expected corrupt/non-signature bytes to fail verification")
	}
}

// TestVerifyDetachedSignature_CorruptKeyringFails proves a bad keyring
// (e.g. the key-fetch step itself returned garbage) is reported as an
// error, not silently treated as "no keys, so nothing to check."
func TestVerifyDetachedSignature_CorruptKeyringFails(t *testing.T) {
	_, entity := genTestKeypair(t)
	content := []byte("some content")
	sig := armoredDetachedSign(t, entity, content)

	if _, err := VerifyDetachedSignature(bytes.NewReader(content), sig, []byte("not a keyring at all")); err == nil {
		t.Fatalf("expected a corrupt keyring to fail rather than silently pass")
	}
}

// TestLooksArmored_DetectsBothFormats is a narrow unit test for the
// format-sniffing helper VerifyDetachedSignature relies on to pick the
// right verification path.
func TestLooksArmored_DetectsBothFormats(t *testing.T) {
	if !looksArmored([]byte("-----BEGIN PGP SIGNATURE-----\n...\n-----END PGP SIGNATURE-----\n")) {
		t.Errorf("expected armored text to be detected as armored")
	}
	if !looksArmored([]byte("\n\n  -----BEGIN PGP SIGNATURE-----\n...")) {
		t.Errorf("expected leading whitespace before the armor header to still be detected as armored")
	}
	if looksArmored([]byte{0x89, 0x01, 0x02, 0x03, 0x04}) {
		t.Errorf("expected raw binary OpenPGP packet bytes to NOT be detected as armored")
	}
	if looksArmored(nil) {
		t.Errorf("expected empty input to not be detected as armored")
	}
}

// writeTempPayload writes content to a temp file and returns its path.
func writeTempPayload(t *testing.T, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "payload.iso")
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatalf("writing temp payload: %v", err)
	}
	return path
}

// TestVerifyEntrySignature_NilOrEmptyFieldsAreANoOp is item 28's inert
// case: every current catalog entry has no SignatureURL/SigningKeyURL,
// which must be silently accepted (no error) rather than treated as a
// verification failure — exactly how an empty SHA256 is treated today.
func TestVerifyEntrySignature_NilOrEmptyFieldsAreANoOp(t *testing.T) {
	path := writeTempPayload(t, []byte("payload"))

	if _, err := verifyEntrySignature(path, nil); err != nil {
		t.Errorf("expected a nil entry to be a no-op, got: %v", err)
	}
	if _, err := verifyEntrySignature(path, &discovery.Entry{}); err != nil {
		t.Errorf("expected an entry with no SignatureURL/SigningKeyURL to be a no-op, got: %v", err)
	}
	if _, err := verifyEntrySignature(path, &discovery.Entry{SignatureURL: "https://example.test/x.sig"}); err != nil {
		t.Errorf("expected an entry with only SignatureURL (no SigningKeyURL) to be a no-op, got: %v", err)
	}
}

// TestVerifyEntrySignature_RealSignatureOverHTTPSucceeds is the
// end-to-end proof that item 28's wiring actually works: a real OpenPGP
// keypair signs the real downloaded file's content, both the signature
// and the keyring are served over real HTTP (httptest, not mocked
// in-process calls), and verifyEntrySignature fetches and verifies them
// for real.
func TestVerifyEntrySignature_RealSignatureOverHTTPSucceeds(t *testing.T) {
	content := []byte("this is the real downloaded payload content")
	path := writeTempPayload(t, content)

	keyring, entity := genTestKeypair(t)
	sig := armoredDetachedSign(t, entity, content)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sig":
			_, _ = w.Write(sig)
		case "/key":
			_, _ = w.Write(keyring)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	entry := &discovery.Entry{SignatureURL: srv.URL + "/sig", SigningKeyURL: srv.URL + "/key"}
	signer, err := verifyEntrySignature(path, entry)
	if err != nil {
		t.Fatalf("expected a real, valid signature served over HTTP to verify, got: %v", err)
	}
	if !strings.Contains(signer, "Test Signer") {
		t.Errorf("expected the reported signer identity, got %q", signer)
	}
}

// TestVerifyEntrySignature_TamperedPayloadFails is the actual security
// property: if the file at destPath doesn't match what was signed (a
// compromised mirror serving a different file than the one the real
// signature covers), verification must fail.
func TestVerifyEntrySignature_TamperedPayloadFails(t *testing.T) {
	original := []byte("the real, untampered payload")
	keyring, entity := genTestKeypair(t)
	sig := armoredDetachedSign(t, entity, original)

	tamperedPath := writeTempPayload(t, []byte("a different, tampered payload"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sig":
			_, _ = w.Write(sig)
		case "/key":
			_, _ = w.Write(keyring)
		}
	}))
	defer srv.Close()

	entry := &discovery.Entry{SignatureURL: srv.URL + "/sig", SigningKeyURL: srv.URL + "/key"}
	if _, err := verifyEntrySignature(tamperedPath, entry); err == nil {
		t.Fatalf("expected verification to fail for a payload that doesn't match the real signature")
	}
}

// TestVerifyEntrySignature_UnreachableSignatureURLFails proves a dead
// link (mirror took the signature file down, typo'd URL, ...) is
// reported as an error, not silently treated as "no signature to check."
func TestVerifyEntrySignature_UnreachableSignatureURLFails(t *testing.T) {
	path := writeTempPayload(t, []byte("payload"))
	origTimeout := signatureFetchTimeout
	signatureFetchTimeout = 2 * time.Second
	defer func() { signatureFetchTimeout = origTimeout }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	entry := &discovery.Entry{SignatureURL: srv.URL + "/missing.sig", SigningKeyURL: srv.URL + "/key"}
	if _, err := verifyEntrySignature(path, entry); err == nil {
		t.Fatalf("expected a 404 signature URL to fail verification")
	}
}
