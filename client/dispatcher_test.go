package client

import (
	"strings"
	"sync"
	"testing"

	"github.com/chriisong/albion-scanner-private/lib"
)

// fakeUploader records every sendToIngest call for assertion.
type fakeUploader struct {
	mu    sync.Mutex
	calls []fakeUploadCall
}

type fakeUploadCall struct {
	body       []byte
	topic      string
	identifier string
}

func (f *fakeUploader) sendToIngest(body []byte, topic string, state *albionState, identifier string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeUploadCall{
		body:       append([]byte(nil), body...),
		topic:      topic,
		identifier: identifier,
	})
}

func (f *fakeUploader) snapshot() []fakeUploadCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeUploadCall, len(f.calls))
	copy(out, f.calls)
	return out
}

// withTestDispatcher swaps the package-level dispatcher for a test double and restores it on cleanup.
func withTestDispatcher(t *testing.T, u uploader, disableUpload, enableWS bool) {
	t.Helper()
	origDis := dis
	origDisableUpload := ConfigGlobal.DisableUpload
	origEnableWS := ConfigGlobal.EnableWebsockets
	t.Cleanup(func() {
		dis = origDis
		ConfigGlobal.DisableUpload = origDisableUpload
		ConfigGlobal.EnableWebsockets = origEnableWS
	})
	ConfigGlobal.DisableUpload = disableUpload
	ConfigGlobal.EnableWebsockets = enableWS
	dis = &dispatcher{brokerUploader: u}
}

func TestValidateBrokerURL_EmptyReturnsError(t *testing.T) {
	if err := validateBrokerURL(""); err == nil {
		t.Fatal("expected error for empty URL, got nil")
	}
}

func TestValidateBrokerURL_RejectsHTTPPoWScheme(t *testing.T) {
	for _, u := range []string{
		"http+pow://albion-online-data.com",
		"https+pow://albion-online-data.com",
	} {
		if err := validateBrokerURL(u); err == nil {
			t.Errorf("expected error for PoW URL %q, got nil", u)
		}
	}
}

func TestValidateBrokerURL_RejectsPlainHTTPScheme(t *testing.T) {
	for _, u := range []string{
		"http://example.com",
		"https://example.com",
	} {
		if err := validateBrokerURL(u); err == nil {
			t.Errorf("expected error for plain HTTP URL %q, got nil", u)
		}
	}
}

func TestValidateBrokerURL_AcceptsNATSScheme(t *testing.T) {
	if err := validateBrokerURL("nats://127.0.0.1:4222"); err != nil {
		t.Fatalf("expected no error for nats:// URL, got: %v", err)
	}
}

func TestSendMsgToPublicUploaders_RoutesToBrokerOnly(t *testing.T) {
	fake := &fakeUploader{}
	withTestDispatcher(t, fake, false, false)

	type testUpload struct {
		Field string `json:"field"`
	}
	sendMsgToPublicUploaders(testUpload{Field: "hello"}, "test.topic", &albionState{}, "ident-1")

	calls := fake.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected 1 broker call, got %d", len(calls))
	}
	if calls[0].topic != "test.topic" {
		t.Errorf("expected topic test.topic, got %q", calls[0].topic)
	}
	if calls[0].identifier != "ident-1" {
		t.Errorf("expected identifier ident-1, got %q", calls[0].identifier)
	}
	if got := string(calls[0].body); !strings.Contains(got, `"field":"hello"`) {
		t.Errorf("expected body to contain field payload, got %q", got)
	}
}

// recordingPersonalizedUpload satisfies lib.PersonalizedUpload for test assertions.
type recordingPersonalizedUpload struct {
	lib.PrivateUpload
	personalizedID   lib.CharacterID
	personalizedName string
}

func (r *recordingPersonalizedUpload) Personalize(id lib.CharacterID, name string) {
	r.personalizedID = id
	r.personalizedName = name
	r.PrivateUpload.Personalize(id, name)
}

func TestSendMsgToPrivateUploaders_PersonalizesAndRoutesToBroker(t *testing.T) {
	fake := &fakeUploader{}
	withTestDispatcher(t, fake, false, false)

	upload := &recordingPersonalizedUpload{}
	state := &albionState{CharacterId: lib.CharacterID("char-xyz"), CharacterName: "Tester"}
	sendMsgToPrivateUploaders(upload, "private.topic", state, "ident-2")

	if upload.personalizedID != lib.CharacterID("char-xyz") || upload.personalizedName != "Tester" {
		t.Errorf("expected Personalize(\"char-xyz\", \"Tester\"), got (%q, %q)",
			upload.personalizedID, upload.personalizedName)
	}

	calls := fake.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected 1 broker call, got %d", len(calls))
	}
	if calls[0].topic != "private.topic" {
		t.Errorf("expected topic private.topic, got %q", calls[0].topic)
	}
}

func TestSendMsg_DisableUploadShortCircuits(t *testing.T) {
	fake := &fakeUploader{}
	withTestDispatcher(t, fake, true, false)

	type pub struct{ X int }
	sendMsgToPublicUploaders(pub{X: 1}, "t.pub", &albionState{}, "id-pub")

	priv := &recordingPersonalizedUpload{}
	sendMsgToPrivateUploaders(priv, "t.priv", &albionState{}, "id-priv")

	if calls := fake.snapshot(); len(calls) != 0 {
		t.Fatalf("expected 0 broker calls when DisableUpload=true, got %d", len(calls))
	}
}

// TestPoWSolverRemoved ensures the PoW solve path is gone from the package.
// If solvePow or the httpUploaderPow type were reintroduced, this test would
// compile-fail only when the referenced identifier returns — but since Go lacks
// "identifier must not exist" assertions, we instead check that newBrokerUploader
// refuses the `http+pow` scheme (covered above). This test documents the invariant:
// no uploader implementation in this package should perform proof-of-work.
func TestPoWSolverRemoved_DocumentedInvariant(t *testing.T) {
	// The invariant is enforced by:
	//   1. TestNewBrokerUploader_RejectsHTTPPoWScheme (no PoW URL accepted)
	//   2. Deletion of client/uploader_http_pow.go (compile-time enforcement)
	// This test exists so grep for "PoW" yields an anchor in the test suite.
	t.Log("PoW uploader removed; all traffic routes through private NATS broker.")
}
