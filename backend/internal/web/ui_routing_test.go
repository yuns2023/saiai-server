package web

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseUserUI(t *testing.T) {
	for raw, want := range map[string]UserUI{
		"":          UserUIClassic,
		"classic":   UserUIClassic,
		" Classic ": UserUIClassic,
		"aog":       UserUIAOG,
		"AOG":       UserUIAOG,
	} {
		got, err := ParseUserUI(raw)
		require.NoError(t, err, raw)
		assert.Equal(t, want, got, raw)
	}
	for _, raw := range []string{"new", "aog2", "classic,aog"} {
		_, err := ParseUserUI(raw)
		assert.Error(t, err, raw)
	}
}

func TestClassifyAOGPath(t *testing.T) {
	classicFiles := map[string]bool{
		"assets/index-abc.js": true,
		"logo.png":            true,
		"index.html":          true,
	}
	aogFiles := map[string]bool{
		"_aog/app-123.js":  true,
		"_aog/favicon.svg": true,
		"index.html":       true,
	}
	classify := func(p string) (uiRoute, string) {
		return classifyAOGPath(p,
			func(n string) bool { return classicFiles[n] },
			func(n string) bool { return aogFiles[n] },
		)
	}

	cases := []struct {
		path  string
		route uiRoute
		name  string
	}{
		// customer pages and unknown paths go to the AOG SPA
		{"/", routeAOGIndex, ""},
		{"/index.html", routeAOGIndex, ""},
		{"/dashboard", routeAOGIndex, ""},
		{"/login", routeAOGIndex, ""},
		{"/purchase", routeAOGIndex, ""},
		{"/setup/", routeClassicIndex, ""}, // unreachable in practice: /setup/ is an API bypass
		{"/no/such/page", routeAOGIndex, ""},

		// admin console and the setup wizard entry stay classic
		{"/admin", routeClassicIndex, ""},
		{"/admin/", routeClassicIndex, ""},
		{"/admin/users", routeClassicIndex, ""},
		{"/admin//users", routeClassicIndex, ""},
		{"/setup", routeClassicIndex, ""},

		// look-alikes do not get the admin console
		{"/adminx", routeAOGIndex, ""},
		{"/admin../", routeAOGIndex, ""},
		{"/Admin/users", routeAOGIndex, ""},
		{"/setupx", routeAOGIndex, ""},

		// dot segments are resolved before deciding
		{"/dashboard/../admin/users", routeClassicIndex, ""},
		{"/admin/../dashboard", routeAOGIndex, ""},

		// AOG assets: only from the AOG build, never a page
		{"/_aog/app-123.js", routeAOGAsset, "_aog/app-123.js"},
		{"/_aog/missing.js", routeNotFound, ""},
		{"/_aog/", routeNotFound, ""},
		{"/_aog", routeNotFound, ""},
		{"/_aog/../assets/index-abc.js", routeClassicAsset, "assets/index-abc.js"},
		{"/_aog/../../../etc/passwd", routeAOGIndex, ""},

		// classic files keep working for the admin console
		{"/assets/index-abc.js", routeClassicAsset, "assets/index-abc.js"},
		{"/logo.png", routeClassicAsset, "logo.png"},
		{"/assets/missing.js", routeNotFound, ""},
		{"/assets/", routeNotFound, ""},
	}
	for _, tc := range cases {
		route, name := classify(tc.path)
		assert.Equal(t, tc.route, route, tc.path)
		assert.Equal(t, tc.name, name, tc.path)
	}
}
