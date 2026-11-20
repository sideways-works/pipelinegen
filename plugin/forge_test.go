package plugin

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestOtherKindsDescribeUnchanged(t *testing.T) {
	d := Description{Protocol: Protocol, Kind: KindRunner, Name: "r", Version: "v1", Capabilities: []string{CallPipeline}}

	got, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}

	want := `{"protocol":1,"kind":"runner","name":"r","version":"v1","capabilities":["pipeline"]}`
	if string(got) != want {
		t.Fatalf("describe = %s, want %s", got, want)
	}
}

func TestForgeDescribeFlat(t *testing.T) {
	in := `{"protocol":1,"kind":"forge","name":"c","version":"v1",
		"capabilities":["changes","statuses"],
		"api_hosts":["https://api.example.test"],
		"request_headers":["Accept"],"response_headers":["Link"],
		"auth":{"header":"Authorization","scheme":"Bearer"},
		"purposes":["read","changes","report"],"git_user":"x-access-token",
		"limits":{"status_text_max":140,"comment_max":65536,"markdown":true},
		"states":{"failure":"failed"},
		"fallback":{"error":{"id":"s","method":"POST","url":"https://api.example.test/{commit}","auth":{"purpose":"report"}}}}`

	var d Description
	if err := json.Unmarshal([]byte(in), &d); err != nil {
		t.Fatal(err)
	}

	switch {
	case d.Kind != KindForge || d.APIHosts[0] != "https://api.example.test":
		t.Fatalf("kind or api hosts: %+v", d)
	case d.Auth == nil || d.Auth.Scheme != "Bearer" || d.GitUser != "x-access-token":
		t.Fatalf("auth: %+v", d.Auth)
	case d.Limits == nil || d.Limits.StatusTextMax != 140 || d.States[StateFailure] != "failed":
		t.Fatalf("limits or states: %+v %v", d.Limits, d.States)
	case d.Fallback[StateError].Auth == nil || d.Fallback[StateError].Auth.Purpose != PurposeReport:
		t.Fatalf("fallback: %+v", d.Fallback)
	}

	out, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(out), "ForgeDescription") || !strings.Contains(string(out), `"git_user":"x-access-token"`) {
		t.Fatalf("not flat: %s", out)
	}
}

func TestCallPurposes(t *testing.T) {
	for _, call := range ForgeCalls {
		p, ok := CallPurposes[call]

		switch {
		case (call == CallLinks || call == CallIdentify) && ok:
			t.Errorf("%s has purpose %q; it sends no requests", call, p)
		case call != CallLinks && call != CallIdentify && !slices.Contains([]string{PurposeChanges, PurposeReport}, p):
			t.Errorf("%s has purpose %q", call, p)
		}
	}

	for call, p := range CallPurposes {
		if p == PurposeStore || p == PurposeRead {
			t.Errorf("%s may carry the %s token", call, p)
		}
	}
}
