package engine

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"boot-orchestrator/safety"
)

// TestProvisionRequestJSON_NeverContainsSecret is the regression test for
// Phase 1 item A.1: Credentials.Password and Credentials.PasswordHash must
// never survive a json.Marshal of anything that embeds Credentials
// (ProvisionRequest today; any future caller gets the same protection for
// free). Before Credentials.Password/PasswordHash had `json:"-"`,
// encoding/json used the exported field names verbatim and both the
// plaintext password and the derived hash were serialized.
func TestProvisionRequestJSON_NeverContainsSecret(t *testing.T) {
	req := ProvisionRequest{
		TargetPartition: "/dev/loop0p2",
		Credentials: Credentials{
			Username:       "janedoe",
			Password:       "super-secret-plaintext",
			PasswordHash:   "$6$roundsOfSaltAndHash$abcdef0123456789",
			AllowRootLogin: false,
		},
	}

	out, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("json.Marshal(ProvisionRequest): %v", err)
	}
	got := string(out)

	if strings.Contains(got, "super-secret-plaintext") {
		t.Fatalf("serialized ProvisionRequest contains the plaintext password:\n%s", got)
	}
	if strings.Contains(got, "$6$roundsOfSaltAndHash") {
		t.Fatalf("serialized ProvisionRequest contains the password hash:\n%s", got)
	}
	// The username is not a secret and is expected to round-trip.
	if !strings.Contains(got, "janedoe") {
		t.Fatalf("expected the non-secret username to still serialize, got:\n%s", got)
	}
}

// TestJournalSessionJSON_NeverContainsSecret documents and guards the fact
// that safety.SessionState (what actually gets written to
// orchestrator_journal.json on disk) has no Credentials field at all, so a
// journal can never leak a password/hash regardless of what ProvisionRequest
// carries in memory.
func TestJournalSessionJSON_NeverContainsSecret(t *testing.T) {
	j := safety.Journal{
		TxnID:     "test-txn",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Session: safety.SessionState{
			TargetOSID: "ubuntu-24.04",
			TargetDisk: "/dev/loop0",
		},
	}

	out, err := json.Marshal(&j)
	if err != nil {
		t.Fatalf("json.Marshal(Journal): %v", err)
	}
	got := string(out)

	for _, secretLike := range []string{"password", "Password", "pwhash", "PasswordHash"} {
		if strings.Contains(got, secretLike) {
			t.Fatalf("serialized Journal unexpectedly mentions %q:\n%s", secretLike, got)
		}
	}
}
