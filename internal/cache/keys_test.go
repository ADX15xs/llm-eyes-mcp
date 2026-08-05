package cache

import (
	"strings"
	"testing"
)

// keys_test verifies that every cache key is deterministic, content-addressed,
// and that parameters that should not matter (order) do not change the key.

func TestL0Key(t *testing.T) {
	const md5 = "AbC0123456789DEF"
	got := L0Key(md5)
	want := "raw:abc0123456789def"
	if got != want {
		t.Fatalf("L0Key = %q, want %q (must be lower-cased)", got, want)
	}
	if !strings.HasPrefix(got, "raw:") {
		t.Errorf("L0Key should be namespaced with raw: prefix")
	}
}

func TestL1KeyEmbedsIntentAndSchema(t *testing.T) {
	const md5 = "00112233445566778899aabbccddeeff"
	hard := L1Key(md5, "hard")
	soft := L1Key(md5, "soft")
	if hard == soft {
		t.Fatalf("intent tag must separate hard/soft renditions: both = %q", hard)
	}
	for _, k := range []string{hard, soft} {
		if !strings.Contains(k, "v1") {
			t.Errorf("L1Key must carry SchemaVersion: %q", k)
		}
		if !strings.Contains(k, "proc:") {
			t.Errorf("L1Key must be namespaced with proc: prefix: %q", k)
		}
		if !strings.Contains(k, strings.ToLower(md5)) {
			t.Errorf("L1Key must carry the md5: %q", k)
		}
	}
	// Same input must be stable across calls.
	if L1Key(md5, "hard") != hard {
		t.Errorf("L1Key must be deterministic")
	}
}

func TestL2KeyKeyedByToolAndModelAndParams(t *testing.T) {
	const md5 = "deadbeef00112233445566778899aabb"
	a := L2Key(md5, "measure_image", "glm-4.6v-flash", "abc123")
	b := L2Key(md5, "measure_image", "glm-4.6v-flash", "abc123")
	if a != b {
		t.Fatalf("L2Key must be deterministic: %q != %q", a, b)
	}
	if L2Key(md5, "measure_image", "glm-4.6v-flash", "abc123") ==
		L2Key(md5, "measure_image", "glm-4.6v-flash", "def456") {
		t.Errorf("different param hash must yield a different L2 key")
	}
	if L2Key(md5, "measure_image", "v1", "abc123") ==
		L2Key(md5, "measure_image", "v2", "abc123") {
		t.Errorf("different model version must yield a different L2 key")
	}
	if L2Key(md5, "measure_image", "v1", "abc123") ==
		L2Key(md5, "describe_image", "v1", "abc123") {
		t.Errorf("different tool must yield a different L2 key")
	}
	if !strings.HasPrefix(a, "vlm:") {
		t.Errorf("L2Key must be namespaced: %q", a)
	}
	if !strings.Contains(a, "v1") {
		t.Errorf("L2Key must carry SchemaVersion: %q", a)
	}
}

func TestL3KeyOrderIndependent(t *testing.T) {
	const md5 = "feedface00112233445566778899aabb"
	p1 := map[string]string{"a": "1", "b": "2"}
	p2 := map[string]string{"b": "2", "a": "1"}
	if L3Key(md5, "distance", "v1", p1) != L3Key(md5, "distance", "v1", p2) {
		t.Fatalf("param order must not change L3 key: %v vs %v", p1, p2)
	}
	if L3Key(md5, "distance", "v1", p1) == L3Key(md5, "area", "v1", p1) {
		t.Errorf("different action must yield a different L3 key")
	}
}

func TestL3KeyModelVersionMatters(t *testing.T) {
	const md5 = "feedface00112233445566778899aabb"
	p := map[string]string{"labels": "a|b"}
	if L3Key(md5, "distance", "v1", p) == L3Key(md5, "distance", "v2", p) {
		t.Errorf("different model version must yield a different L3 key")
	}
}

func TestParamHashOrderIndependentAndStable(t *testing.T) {
	p1 := map[string]string{"y": "9", "x": "1"}
	p2 := map[string]string{"x": "1", "y": "9"}
	if ParamHash(p1) != ParamHash(p2) {
		t.Fatalf("ParamHash order must be independent: %q != %q", ParamHash(p1), ParamHash(p2))
	}
	if ParamHash(p1) != ParamHash(p1) {
		t.Errorf("ParamHash must be stable")
	}
	if len(ParamHash(p1)) != 12 {
		t.Errorf("ParamHash must be a 12-char digest, got %d", len(ParamHash(p1)))
	}
}

func TestParamHashEmpty(t *testing.T) {
	if ParamHash(nil) != "noparam" {
		t.Errorf("empty param hash must be the sentinel %q, got %q", "noparam", ParamHash(nil))
	}
	if ParamHash(map[string]string{}) != "noparam" {
		t.Errorf("empty param map must be the sentinel %q", "noparam")
	}
}

func TestSanitizeMakesKeysSafe(t *testing.T) {
	// Model versions from various backends: spaces, slashes, colons, unicode.
	cases := map[string]string{
		"glm-4.6v-flash": "glm-4.6v-flash",
		"Qwen/VL 2.5":    "Qwen_VL_2.5",
		"model:1/2":      "model_1_2",
		"模型-测试":          "__-__",
		"":               "unknown",
	}
	for in, want := range cases {
		if got := sanitize(in); got != want {
			t.Errorf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}
