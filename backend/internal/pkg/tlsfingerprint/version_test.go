package tlsfingerprint

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestVersionedClientHelloMatchesNativeCaptures(t *testing.T) {
	base := DefaultProfile()
	for _, version := range []string{"2.1.301", "2.1.302", "2.1.301"} {
		t.Run(version, func(t *testing.T) {
			data, err := os.ReadFile("testdata/claude-" + version + "-linux-model-hello.json")
			if err != nil {
				t.Fatal(err)
			}
			var want helloShape
			if err := json.Unmarshal(data, &want); err != nil {
				t.Fatal(err)
			}
			got := captureHello(t, ProfileForUserAgent(base, "claude-cli/"+version+" (external, cli)"))
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("wire shape differs: got %+v; want %+v", got, want)
			}
		})
	}
	if base.TransportVariant() != "" {
		t.Fatal("mutated shared profile")
	}
}

func TestVersionProfileSelection(t *testing.T) {
	for _, tc := range []struct {
		ua     string
		modern bool
	}{
		{"claude-cli/2.1.301", false}, {"claude-cli/2.1.302 (external, cli)", true},
		{"claude-cli/2.1.303", true}, {"claude-cli/2.2.0", true}, {"claude-cli/3.0.0", true},
		{"claude-cli/2.1.30", false}, {"claude-cli/2.1.302garbage", false},
		{"claude-cli/2.1.302.1", false}, {"", false}, {"other/2.1.302", false},
		{"claude-cli/999999999999999999999999999.1.302", false},
	} {
		t.Run(tc.ua, func(t *testing.T) {
			p := ProfileForUserAgent(DefaultProfile(), tc.ua)
			if (p.TransportVariant() != "") != tc.modern {
				t.Fatalf("wrong selection for %q", tc.ua)
			}
		})
	}
	if ProfileForUserAgent(nil, "claude-cli/2.1.302") != nil {
		t.Fatal("changed nil profile")
	}
}
