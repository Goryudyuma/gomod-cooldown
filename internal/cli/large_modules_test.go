package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

type largeModuleFixture struct {
	name         string
	path         string
	digest       string
	requireCount int
	pseudoCount  int
}

type expectedModuleUpdate struct {
	current string
	next    string
	allowed bool
}

type goListedModule struct {
	Path    string
	Version string
	Main    bool
	Update  *goListedModule
	Error   *struct{ Err string }
}

func TestLargeModuleFixtureIntegrity(t *testing.T) {
	for _, fixture := range largeModuleFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			raw, file := readLargeModuleFixture(t, fixture)
			sum := sha256.Sum256(raw)
			if got := hex.EncodeToString(sum[:]); got != fixture.digest {
				t.Fatalf("SHA-256=%s, want %s", got, fixture.digest)
			}
			if got := len(file.Require); got != fixture.requireCount {
				t.Fatalf("require count=%d, want %d", got, fixture.requireCount)
			}
			if got := pseudoRequirementCount(file); got != fixture.pseudoCount {
				t.Fatalf("pseudo-version count=%d, want %d", got, fixture.pseudoCount)
			}
		})
	}
}

func TestLargeRepositoryGoModsThroughRealGoCommand(t *testing.T) {
	for _, fixture := range largeModuleFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			testLargeRepositoryGoMod(t, fixture)
		})
	}
}

func testLargeRepositoryGoMod(t *testing.T, fixture largeModuleFixture) {
	t.Helper()
	_, file := readLargeModuleFixture(t, fixture)
	dir, normalized := writeNormalizedLargeModule(t, file)
	proxyModules, expected := largeProxyModules(t, file.Require)
	p := newRecordingModuleProxy(t, proxyModules)
	code, stdout, stderr := runGoThroughCooldown(t, p.server.URL, dir, "list", "-mod=mod", "-m", "-u", "-json", "all")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	assertLargeModuleOutput(t, stdout, file.Module.Mod.Path, expected)
	if after := readFile(t, filepath.Join(dir, "go.mod")); !bytes.Equal(after, normalized) {
		t.Fatal("go list unexpectedly changed the normalized temporary go.mod")
	}
	if p.countSuffix("/@v/list") == 0 || p.countContains(".info") == 0 || p.countContains(".mod") == 0 {
		t.Fatalf("incomplete GOPROXY exercise: %v", p.allRequests())
	}
	if p.countContains(".zip") != 0 {
		t.Fatalf("go list unexpectedly downloaded module source: %v", p.allRequests())
	}
	if !strings.Contains(stderr, "excluded module=") {
		t.Fatalf("no cooldown exclusion was logged: %s", stderr)
	}
	p.assertNoUnknown(t)
}

func largeModuleFixtures() []largeModuleFixture {
	return []largeModuleFixture{
		{
			name: "prometheus", path: "testdata/large-modules/prometheus/go.mod",
			digest: "5a5c328d946db544e782d28c8a2bf9feab2da530a6a09fe9d59a311f11fab14c", requireCount: 250, pseudoCount: 30,
		},
		{
			name: "helm", path: "testdata/large-modules/helm/go.mod",
			digest: "0514561bb6ded52510d15bafb5eace5d60064f954b42f811f8e75d25d0f0d805", requireCount: 173, pseudoCount: 24,
		},
		{
			name: "caddy", path: "testdata/large-modules/caddy/go.mod",
			digest: "ad63430a5588ce8cd311f18c9c572c7de0c491e6d00cedbeb5274ab71fe280ac", requireCount: 169, pseudoCount: 18,
		},
	}
}

func readLargeModuleFixture(t *testing.T, fixture largeModuleFixture) ([]byte, *modfile.File) {
	t.Helper()
	raw := readFile(t, fixture.path)
	file, err := modfile.Parse(fixture.path, raw, nil)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return raw, file
}

func pseudoRequirementCount(file *modfile.File) int {
	count := 0
	for _, req := range file.Require {
		if module.IsPseudoVersion(req.Mod.Version) {
			count++
		}
	}
	return count
}

func writeNormalizedLargeModule(t *testing.T, file *modfile.File) (string, []byte) {
	t.Helper()
	if err := file.AddGoStmt("1.25.0"); err != nil {
		t.Fatalf("normalize go directive: %v", err)
	}
	file.DropToolchainStmt()
	normalized, err := file.Format()
	if err != nil {
		t.Fatalf("format normalized fixture: %v", err)
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), normalized)
	return dir, normalized
}

func largeProxyModules(t *testing.T, requirements []*modfile.Require) ([]testProxyModule, map[string]expectedModuleUpdate) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	modules := make([]testProxyModule, 0, len(requirements))
	expected := make(map[string]expectedModuleUpdate, len(requirements))
	for i, req := range requirements {
		next := syntheticNextVersion(t, req.Mod.Path, req.Mod.Version)
		allowed := i%2 == 0
		nextStamp := now.Add(-time.Hour)
		if allowed {
			nextStamp = now.Add(-15 * 24 * time.Hour)
		}
		currentStamp := currentVersionTime(t, req.Mod.Version, now)
		listed := []string{next}
		if !module.IsPseudoVersion(req.Mod.Version) {
			listed = []string{req.Mod.Version, next}
		}
		modules = append(modules, testProxyModule{
			path: req.Mod.Path,
			versions: []testProxyVersion{
				{version: req.Mod.Version, stamp: currentStamp},
				{version: next, stamp: nextStamp},
			},
			listed: listed,
			latest: next,
		})
		expected[req.Mod.Path] = expectedModuleUpdate{current: req.Mod.Version, next: next, allowed: allowed}
	}
	return modules, expected
}

func currentVersionTime(t *testing.T, version string, now time.Time) time.Time {
	t.Helper()
	if !module.IsPseudoVersion(version) {
		return now.Add(-60 * 24 * time.Hour)
	}
	stamp, err := module.PseudoVersionTime(version)
	if err != nil {
		t.Fatalf("parse pseudo-version time %q: %v", version, err)
	}
	return stamp
}

func syntheticNextVersion(t *testing.T, path, current string) string {
	t.Helper()
	major := semver.Major(current)
	if major == "" {
		t.Fatalf("non-semver requirement %s@%s", path, current)
	}
	next := major + ".999999.0"
	if strings.HasSuffix(current, "+incompatible") {
		next += "+incompatible"
	}
	if semver.Compare(next, current) <= 0 {
		t.Fatalf("synthetic update %s is not newer than %s", next, current)
	}
	if err := module.Check(path, next); err != nil {
		t.Fatalf("invalid synthetic update %s@%s: %v", path, next, err)
	}
	return next
}

func assertLargeModuleOutput(
	t *testing.T,
	stdout string,
	mainPath string,
	expected map[string]expectedModuleUpdate,
) {
	t.Helper()
	modules := decodeListedModules(t, stdout)
	if len(modules) != len(expected)+1 {
		t.Fatalf("listed module count=%d, want %d", len(modules), len(expected)+1)
	}
	mainModule, ok := modules[mainPath]
	if !ok || !mainModule.Main {
		t.Fatalf("main module %q is missing", mainPath)
	}
	for path, want := range expected {
		assertListedDependency(t, modules[path], path, want)
	}
}

func decodeListedModules(t *testing.T, stdout string) map[string]goListedModule {
	t.Helper()
	modules := make(map[string]goListedModule)
	decoder := json.NewDecoder(strings.NewReader(stdout))
	for {
		var got goListedModule
		err := decoder.Decode(&got)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("decode go list output: %v\n%s", err, stdout)
		}
		modules[got.Path] = got
	}
	return modules
}

func assertListedDependency(t *testing.T, got goListedModule, path string, want expectedModuleUpdate) {
	t.Helper()
	if got.Path == "" {
		t.Fatalf("dependency %q is missing from go list output", path)
	}
	if got.Error != nil {
		t.Fatalf("dependency %q error: %s", path, got.Error.Err)
	}
	if got.Version != want.current {
		t.Fatalf("dependency %q version=%q, want %q", path, got.Version, want.current)
	}
	if want.allowed && (got.Update == nil || got.Update.Version != want.next) {
		t.Fatalf("dependency %q update=%v, want %q", path, got.Update, want.next)
	}
	if !want.allowed && got.Update != nil {
		t.Fatalf("dependency %q exposed recent update %q", path, got.Update.Version)
	}
}

func (m goListedModule) String() string {
	if m.Path == "" {
		return "<missing>"
	}
	return fmt.Sprintf("%s@%s", m.Path, m.Version)
}
