package cli

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
)

const integrationModulePath = "example.com/gomod-cooldown-test/dep"

type testProxyVersion struct {
	version string
	stamp   time.Time
	files   []testProxyFile
}

type testProxyFile struct {
	name     string
	contents string
}

type testProxyModule struct {
	path     string
	versions []testProxyVersion
	listed   []string
	latest   string
}

type testProxyResponse struct {
	status      int
	contentType string
	body        []byte
}

type recordingModuleProxy struct {
	server    *httptest.Server
	mu        sync.Mutex
	responses map[string]testProxyResponse
	requests  []string
	unknown   []string
}

func TestGoCommandFiltersUpgrade(t *testing.T) {
	p := newRecordingModuleProxy(t, []testProxyModule{standardIntegrationModule()})
	dir := writeIntegrationModule(t, "v1.0.0")
	code, _, stderr := runGoThroughCooldown(t, p.server.URL, dir, "get", "-u", integrationModulePath)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	if got := requiredVersion(t, dir, integrationModulePath); got != "v1.1.0" {
		t.Fatalf("required version=%q, want v1.1.0", got)
	}
	if p.countSuffix("/@v/list") == 0 || p.countContains("v1.1.0.zip") == 0 {
		t.Fatalf("requests did not exercise discovery and download: %v", p.allRequests())
	}
	if p.countContains("v1.2.0.zip") != 0 {
		t.Fatalf("downloaded version still in cooldown: %v", p.allRequests())
	}
	p.assertNoUnknown(t)
}

func TestGoCommandExplicitAndPinnedVersionsBypassDiscovery(t *testing.T) {
	for _, tt := range []struct {
		name    string
		current string
		args    []string
	}{
		{name: "explicit recent version", current: "v1.0.0", args: []string{"mod", "download", integrationModulePath + "@v1.2.0"}},
		{name: "already pinned recent version", current: "v1.2.0", args: []string{"mod", "download", "all"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := newRecordingModuleProxy(t, []testProxyModule{standardIntegrationModule()})
			dir := writeIntegrationModule(t, tt.current)
			code, _, stderr := runGoThroughCooldown(t, p.server.URL, dir, tt.args...)
			if code != 0 {
				t.Fatalf("exit=%d stderr=%s", code, stderr)
			}
			if p.countSuffix("/@v/list") != 0 || p.countSuffix("/@latest") != 0 {
				t.Fatalf("explicit version used discovery endpoints: %v", p.allRequests())
			}
			if p.countContains("v1.2.0") == 0 {
				t.Fatalf("recent pinned version was not downloaded: %v", p.allRequests())
			}
			p.assertNoUnknown(t)
		})
	}
}

func TestGoCommandReportsOnlyEligibleVersions(t *testing.T) {
	p := newRecordingModuleProxy(t, []testProxyModule{standardIntegrationModule()})
	dir := writeIntegrationModule(t, "v1.0.0")
	code, stdout, stderr := runGoThroughCooldown(t, p.server.URL, dir, "list", "-m", "-versions", integrationModulePath)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	fields := strings.Fields(stdout)
	want := []string{integrationModulePath, "v1.0.0", "v1.0.1-GA", "v1.1.0"}
	if !slices.Equal(fields, want) {
		t.Fatalf("versions=%q, want %q", fields, want)
	}
	p.assertNoUnknown(t)
}

func TestGoCommandSkipsUnavailableListedVersion(t *testing.T) {
	p := newRecordingModuleProxy(t, []testProxyModule{standardIntegrationModule()})
	p.setResponse(t, versionEndpoint(integrationModulePath, "v1.0.1-GA", ".info"), testProxyResponse{
		status: http.StatusNotFound, contentType: "text/plain", body: []byte("not found\n"),
	})
	dir := writeIntegrationModule(t, "v1.0.0")
	code, _, stderr := runGoThroughCooldown(t, p.server.URL, dir, "get", "-u", "all")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	if got := requiredVersion(t, dir, integrationModulePath); got != "v1.1.0" {
		t.Fatalf("required version=%q, want v1.1.0", got)
	}
	escapedUnavailable := versionEndpoint(integrationModulePath, "v1.0.1-GA", ".info")
	if got := countMatching(p.allRequests(), func(path string) bool { return path == escapedUnavailable }); got != 1 {
		t.Fatalf("unavailable .info requests=%d, want 1; all requests: %v", got, p.allRequests())
	}
	p.assertNoUnknown(t)
}

func TestGoCommandDoesNotUpgradeAWSSDKToOldIncompatiblePrerelease(t *testing.T) {
	const awsPath = "github.com/aws/aws-sdk-go-v2"
	const currentVersion = "v1.42.1"
	const incompatible = "v2.0.0-preview.4+incompatible"
	current := time.Now().UTC().Truncate(time.Second)
	p := newRecordingModuleProxy(t, []testProxyModule{{
		path: awsPath,
		versions: []testProxyVersion{
			{
				version: "v1.41.12", stamp: current.Add(-60 * 24 * time.Hour),
				files: []testProxyFile{{name: "aws/middleware/middleware.go", contents: "package middleware\n"}},
			},
			{
				version: currentVersion, stamp: current.Add(-time.Hour),
				files: []testProxyFile{{name: "aws/middleware/middleware.go", contents: "package middleware\n"}},
			},
			{version: incompatible, stamp: current.Add(-60 * 24 * time.Hour)},
		},
		listed: []string{"v1.41.12", currentVersion, incompatible},
		latest: currentVersion,
	}})
	notFound := testProxyResponse{status: http.StatusNotFound, contentType: "text/plain", body: []byte("not found\n")}
	for _, path := range []string{awsPath + "/aws", awsPath + "/aws/middleware"} {
		p.setResponseIfAbsent(moduleEndpoint(path, "/@v/list"), notFound)
		p.setResponseIfAbsent(moduleEndpoint(path, "/@latest"), notFound)
	}
	dir := writeIntegrationModuleFor(t, awsPath, currentVersion, awsPath+"/aws/middleware")
	code, _, stderr := runGoThroughCooldown(t, p.server.URL, dir, "get", "-u", "all")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	if got := requiredVersion(t, dir, awsPath); got != currentVersion {
		t.Fatalf("required version=%q, want current %s", got, currentVersion)
	}
	if p.countContains(incompatible+".zip") != 0 {
		t.Fatalf("downloaded incompatible prerelease: %v", p.allRequests())
	}
	if p.countContains(currentVersion+".mod") == 0 {
		t.Fatalf("module-awareness heuristic was not exercised: %v", p.allRequests())
	}
	p.assertNoUnknown(t)
}

func TestGoCommandKeepsLegacyIncompatibleUpgrade(t *testing.T) {
	current := time.Now().UTC().Truncate(time.Second)
	incompatible := "v2.0.0-preview.1+incompatible"
	p := newRecordingModuleProxy(t, []testProxyModule{{
		path: integrationModulePath,
		versions: []testProxyVersion{
			{version: "v1.0.0", stamp: current.Add(-60 * 24 * time.Hour)},
			{version: "v1.1.0", stamp: current.Add(-time.Hour)},
			{version: incompatible, stamp: current.Add(-60 * 24 * time.Hour)},
		},
		listed: []string{"v1.0.0", "v1.1.0", incompatible},
		latest: "v1.1.0",
	}})
	legacyMod := testProxyResponse{
		status: http.StatusOK, contentType: "text/plain", body: []byte("module " + integrationModulePath + "\n"),
	}
	p.setResponse(t, versionEndpoint(integrationModulePath, "v1.1.0", ".mod"), legacyMod)
	p.setResponse(t, versionEndpoint(integrationModulePath, incompatible, ".mod"), legacyMod)
	dir := writeIntegrationModule(t, "v1.1.0")
	code, _, stderr := runGoThroughCooldown(t, p.server.URL, dir, "get", "-u", "all")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	if got := requiredVersion(t, dir, integrationModulePath); got != incompatible {
		t.Fatalf("required version=%q, want legacy update %s", got, incompatible)
	}
	if p.countContains(incompatible+".zip") == 0 {
		t.Fatalf("legacy incompatible version was not downloaded: %v", p.allRequests())
	}
	p.assertNoUnknown(t)
}

func TestGoCommandExactAndPinnedIncompatibleVersionBypassDiscovery(t *testing.T) {
	stamp := time.Now().UTC().Truncate(time.Second).Add(-60 * 24 * time.Hour)
	version := "v2.0.0-preview.1+incompatible"
	for _, tt := range []struct {
		name   string
		pinned bool
	}{
		{name: "exact"},
		{name: "pinned", pinned: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := newRecordingModuleProxy(t, []testProxyModule{{
				path: integrationModulePath,
				versions: []testProxyVersion{
					{version: version, stamp: stamp},
				},
				listed: []string{version},
				latest: version,
			}})
			dir := writeBareIntegrationModule(t)
			args := []string{"mod", "download", integrationModulePath + "@" + version}
			if tt.pinned {
				dir = writeIntegrationModule(t, version)
				args = []string{"mod", "download", "all"}
			}
			code, _, stderr := runGoThroughCooldown(t, p.server.URL, dir, args...)
			if code != 0 {
				t.Fatalf("exit=%d stderr=%s", code, stderr)
			}
			if p.countSuffix("/@v/list") != 0 || p.countSuffix("/@latest") != 0 {
				t.Fatalf("incompatible version used discovery: %v", p.allRequests())
			}
			if p.countContains(version+".zip") == 0 {
				t.Fatalf("incompatible version was not downloaded: %v", p.allRequests())
			}
			p.assertNoUnknown(t)
		})
	}
}

func TestGoCommandPseudoLatest(t *testing.T) {
	for _, tt := range []struct {
		name    string
		major   string
		age     time.Duration
		allowed bool
	}{
		{name: "eligible", major: "v0", age: 30 * 24 * time.Hour, allowed: true},
		{name: "eligible incompatible", major: "v2", age: 30 * 24 * time.Hour, allowed: true},
		{name: "still in cooldown", major: "v0", age: time.Hour, allowed: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			testGoCommandPseudoLatest(t, tt.major, tt.age, tt.allowed)
		})
	}
}

func testGoCommandPseudoLatest(t *testing.T, major string, age time.Duration, allowed bool) {
	t.Helper()
	stamp := time.Now().UTC().Truncate(time.Second).Add(-age)
	version := module.PseudoVersion(major, "", stamp, "abcdefabcdef")
	if major == "v2" {
		version += "+incompatible"
	}
	path := "example.com/gomod-cooldown-test/pseudo"
	p := newRecordingModuleProxy(t, []testProxyModule{{
		path: path, versions: []testProxyVersion{{version: version, stamp: stamp}}, latest: version,
	}})
	dir := writeBareIntegrationModule(t)
	code, stdout, stderr := runGoThroughCooldown(t, p.server.URL, dir, "list", "-m", path+"@latest")
	if allowed && (code != 0 || !slices.Equal(strings.Fields(stdout), []string{path, version})) {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !allowed && (code == 0 || !strings.Contains(stderr, "excluded module=")) {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if p.countSuffix("/@latest") == 0 {
		t.Fatalf("Go command did not exercise @latest: %v", p.allRequests())
	}
	p.assertNoUnknown(t)
}

func TestGoCommandFailsClosedWithoutChangingGoMod(t *testing.T) {
	for _, tt := range []struct {
		name     string
		response testProxyResponse
	}{
		{name: "upstream error", response: testProxyResponse{status: http.StatusInternalServerError, body: []byte("failed\n")}},
		{name: "malformed metadata", response: testProxyResponse{status: http.StatusOK, contentType: "application/json", body: []byte("not-json")}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			testGoCommandMetadataFailure(t, tt.response)
		})
	}
}

func testGoCommandMetadataFailure(t *testing.T, response testProxyResponse) {
	t.Helper()
	p := newRecordingModuleProxy(t, []testProxyModule{standardIntegrationModule()})
	p.setResponse(t, versionEndpoint(integrationModulePath, "v1.1.0", ".info"), response)
	dir := writeIntegrationModule(t, "v1.0.0")
	before := readFile(t, filepath.Join(dir, "go.mod"))
	code, _, stderr := runGoThroughCooldown(t, p.server.URL, dir, "get", "-u", integrationModulePath)
	if code == 0 || !strings.Contains(stderr, "502 Bad Gateway") {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if after := readFile(t, filepath.Join(dir, "go.mod")); !bytes.Equal(after, before) {
		t.Fatalf("go.mod changed after failed update\nbefore:\n%s\nafter:\n%s", before, after)
	}
	p.assertNoUnknown(t)
}

func standardIntegrationModule() testProxyModule {
	now := time.Now().UTC().Truncate(time.Second)
	return testProxyModule{
		path: integrationModulePath,
		versions: []testProxyVersion{
			{version: "v1.0.0", stamp: now.Add(-60 * 24 * time.Hour)},
			{version: "v1.0.1-GA", stamp: now.Add(-45 * 24 * time.Hour)},
			{version: "v1.1.0", stamp: now.Add(-30 * 24 * time.Hour)},
			{version: "v1.2.0", stamp: now.Add(-time.Hour)},
		},
		listed: []string{"v1.0.0", "v1.0.1-GA", "v1.1.0", "v1.2.0"},
		latest: "v1.2.0",
	}
}

func newRecordingModuleProxy(t *testing.T, modules []testProxyModule) *recordingModuleProxy {
	t.Helper()
	p := &recordingModuleProxy{responses: make(map[string]testProxyResponse)}
	for _, mod := range modules {
		p.addModule(t, mod)
	}
	p.server = httptest.NewServer(http.HandlerFunc(p.serveHTTP))
	t.Cleanup(p.server.Close)
	return p
}

func (p *recordingModuleProxy) addModule(t *testing.T, mod testProxyModule) {
	t.Helper()
	listPath := moduleEndpoint(mod.path, "/@v/list")
	p.responses[listPath] = testProxyResponse{status: http.StatusOK, contentType: "text/plain", body: []byte(strings.Join(mod.listed, "\n") + "\n")}
	versions := make(map[string]testProxyVersion, len(mod.versions))
	for _, version := range mod.versions {
		versions[version.version] = version
		p.addVersion(t, mod.path, version)
	}
	latest, ok := versions[mod.latest]
	if !ok {
		t.Fatalf("latest version %q is not configured for %s", mod.latest, mod.path)
	}
	p.responses[moduleEndpoint(mod.path, "/@latest")] = infoResponse(t, latest)
	p.addPrefixNotFoundResponses(mod)
}

func (p *recordingModuleProxy) addPrefixNotFoundResponses(mod testProxyModule) {
	prefix := mod.path
	notFound := testProxyResponse{status: http.StatusNotFound, contentType: "text/plain", body: []byte("not found\n")}
	for {
		slash := strings.LastIndexByte(prefix, '/')
		if slash < 0 {
			return
		}
		prefix = prefix[:slash]
		if _, err := module.EscapePath(prefix); err != nil {
			continue
		}
		p.setResponseIfAbsent(moduleEndpoint(prefix, "/@v/list"), notFound)
		p.setResponseIfAbsent(moduleEndpoint(prefix, "/@latest"), notFound)
		for _, version := range mod.versions {
			for _, suffix := range []string{".info", ".mod", ".zip"} {
				p.setResponseIfAbsent(versionEndpoint(prefix, version.version, suffix), notFound)
			}
		}
	}
}

func (p *recordingModuleProxy) setResponseIfAbsent(path string, response testProxyResponse) {
	if _, ok := p.responses[path]; !ok {
		p.responses[path] = response
	}
}

func (p *recordingModuleProxy) addVersion(t *testing.T, path string, version testProxyVersion) {
	t.Helper()
	p.responses[versionEndpoint(path, version.version, ".info")] = infoResponse(t, version)
	p.responses[versionEndpoint(path, version.version, ".mod")] = testProxyResponse{
		status: http.StatusOK, contentType: "text/plain", body: []byte("module " + path + "\n\ngo 1.20\n"),
	}
	p.responses[versionEndpoint(path, version.version, ".zip")] = testProxyResponse{
		status: http.StatusOK, contentType: "application/zip", body: moduleZip(t, path, version.version, version.files),
	}
}

func infoResponse(t *testing.T, version testProxyVersion) testProxyResponse {
	t.Helper()
	body, err := json.Marshal(struct {
		Version string
		Time    time.Time
	}{Version: version.version, Time: version.stamp})
	if err != nil {
		t.Fatal(err)
	}
	return testProxyResponse{status: http.StatusOK, contentType: "application/json", body: body}
}

func moduleZip(t *testing.T, path, version string, files []testProxyFile) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	prefix := path + "@" + version + "/"
	writeZipEntry(t, zw, prefix+"go.mod", "module "+path+"\n\ngo 1.20\n")
	writeZipEntry(t, zw, prefix+"dep.go", "package dep\n")
	for _, file := range files {
		writeZipEntry(t, zw, prefix+file.name, file.contents)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close module zip: %v", err)
	}
	return buf.Bytes()
}

func writeZipEntry(t *testing.T, zw *zip.Writer, name, contents string) {
	t.Helper()
	w, err := zw.Create(name)
	if err != nil {
		t.Fatalf("create zip entry %q: %v", name, err)
	}
	if _, err := io.WriteString(w, contents); err != nil {
		t.Fatalf("write zip entry %q: %v", name, err)
	}
}

func (p *recordingModuleProxy) serveHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	p.mu.Lock()
	p.requests = append(p.requests, path)
	response, ok := p.responses[path]
	if !ok {
		p.unknown = append(p.unknown, path)
	}
	p.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	if response.contentType != "" {
		w.Header().Set("Content-Type", response.contentType)
	}
	w.WriteHeader(response.status)
	_, _ = w.Write(response.body)
}

func (p *recordingModuleProxy) setResponse(t *testing.T, path string, response testProxyResponse) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.responses[path]; !ok {
		t.Fatalf("cannot replace unconfigured response %q", path)
	}
	p.responses[path] = response
}

func (p *recordingModuleProxy) allRequests() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.requests)
}

func (p *recordingModuleProxy) countSuffix(suffix string) int {
	return countMatching(p.allRequests(), func(path string) bool { return strings.HasSuffix(path, suffix) })
}

func (p *recordingModuleProxy) countContains(part string) int {
	return countMatching(p.allRequests(), func(path string) bool { return strings.Contains(path, part) })
}

func (p *recordingModuleProxy) assertNoUnknown(t *testing.T) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.unknown) != 0 {
		t.Fatalf("unexpected GOPROXY requests: %v", p.unknown)
	}
}

func countMatching(values []string, match func(string) bool) int {
	count := 0
	for _, value := range values {
		if match(value) {
			count++
		}
	}
	return count
}

func moduleEndpoint(path, suffix string) string {
	escaped, err := module.EscapePath(path)
	if err != nil {
		panic(fmt.Sprintf("escape module path %q: %v", path, err))
	}
	return "/" + escaped + suffix
}

func versionEndpoint(path, version, suffix string) string {
	escaped, err := module.EscapeVersion(version)
	if err != nil {
		panic(fmt.Sprintf("escape module version %q: %v", version, err))
	}
	return moduleEndpoint(path, "/@v/"+escaped+suffix)
}

func writeIntegrationModule(t *testing.T, version string) string {
	t.Helper()
	return writeIntegrationModuleFor(t, integrationModulePath, version, integrationModulePath)
}

func writeIntegrationModuleFor(t *testing.T, modulePath, version, importPath string) string {
	t.Helper()
	dir := t.TempDir()
	contents := fmt.Sprintf("module example.com/gomod-cooldown-test/root\n\ngo 1.25.0\n\nrequire %s %s\n", modulePath, version)
	writeFile(t, filepath.Join(dir, "go.mod"), []byte(contents))
	writeFile(t, filepath.Join(dir, "root.go"), []byte("package root\n\nimport _ \""+importPath+"\"\n"))
	return dir
}

func writeBareIntegrationModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), []byte("module example.com/gomod-cooldown-test/root\n\ngo 1.25.0\n"))
	return dir
}

func requiredVersion(t *testing.T, dir, path string) string {
	t.Helper()
	filename := filepath.Join(dir, "go.mod")
	file, err := modfile.Parse(filename, readFile(t, filename), nil)
	if err != nil {
		t.Fatalf("parse resulting go.mod: %v", err)
	}
	for _, req := range file.Require {
		if req.Mod.Path == path {
			return req.Mod.Version
		}
	}
	t.Fatalf("requirement %q is missing", path)
	return ""
}

func runGoThroughCooldown(t *testing.T, upstream, dir string, goArgs ...string) (int, string, string) {
	t.Helper()
	configureIsolatedGo(t)
	goExecutable, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("find go executable: %v", err)
	}
	args := []string{
		"--time-source=commit", "--cooldown=14d", "--upstream=" + upstream, "--upstream-timeout=5s",
		"--", goExecutable, "-C", dir,
	}
	args = append(args, goArgs...)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var stdout, stderr lockedBuffer
	code := Run(ctx, args, nil, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func configureIsolatedGo(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	for key, value := range map[string]string{
		"GO111MODULE": "on", "GOENV": "off", "GOFLAGS": "-modcacherw", "GOINSECURE": "", "GONOPROXY": "",
		"GONOSUMDB": "", "GOPATH": filepath.Join(root, "gopath"), "GOPRIVATE": "", "GOSUMDB": "off",
		"GOTELEMETRY": "off", "GOTOOLCHAIN": "local", "GOVCS": "*:off", "GOWORK": "off",
		"GOCACHE": filepath.Join(root, "gocache"), "GOMODCACHE": filepath.Join(root, "gomodcache"),
	} {
		t.Setenv(key, value)
	}
}

func writeFile(t *testing.T, path string, contents []byte) {
	t.Helper()
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return contents
}
