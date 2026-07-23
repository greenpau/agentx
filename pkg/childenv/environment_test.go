package childenv

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func environmentMap(items []string) map[string]string {
	result := make(map[string]string)
	for _, item := range items {
		name, value, ok := strings.Cut(item, "=")
		if ok {
			result[name] = value
		}
	}
	return result
}

func TestShellDropsCredentialNamesAliasesAndInjectionControls(t *testing.T) {
	toolchain := t.TempDir()
	secret := "model-secret-value"
	result := environmentMap(Shell([]string{
		"PATH=" + toolchain + string(os.PathListSeparator) + ".",
		"HOME=" + toolchain,
		"LANG=C",
		"AZURE_OPENAI_SUBSCRIPTION_KEY=" + secret,
		"SAFE_ALIAS=prefix-" + secret + "-suffix",
		"BASH_ENV=/tmp/startup",
		"NODE_OPTIONS=--require=/tmp/evil",
		"LD_PRELOAD=/tmp/evil.so",
	}))
	for _, name := range []string{"AZURE_OPENAI_SUBSCRIPTION_KEY", "SAFE_ALIAS", "BASH_ENV", "NODE_OPTIONS", "LD_PRELOAD"} {
		if _, ok := result[name]; ok {
			t.Errorf("shell retained %s", name)
		}
	}
	if result["HOME"] != toolchain || result["LANG"] != "C" {
		t.Fatalf("safe shell environment = %#v", result)
	}
	if !strings.Contains(result["PATH"], toolchain) || strings.Contains(result["PATH"], string(os.PathListSeparator)+"."+string(os.PathListSeparator)) {
		t.Fatalf("safe PATH = %q", result["PATH"])
	}
}

func TestShellMatchesTrimmedModelCredentialAndNeverReturnsNil(t *testing.T) {
	const secret = "model-secret-with-padding"
	got := environmentMap(Shell([]string{
		"AZURE_OPENAI_SUBSCRIPTION_KEY=  " + secret + "  ",
		"LANG=" + secret,
	}))
	if _, ok := got["LANG"]; ok {
		t.Fatal("trimmed runtime credential survived through a renamed value")
	}
	oversized := profileSerialize(map[string]string{
		"PATH": "", "LANG": strings.Repeat("x", maximumEnvironmentBytes),
	}, nil)
	if oversized == nil {
		t.Fatal("profile serialization failure returned nil and would inherit ambient environment")
	}
	if values := environmentMap(oversized); len(values) != 1 {
		t.Fatalf("profile fallback = %#v", oversized)
	}
}

func TestDuplicateCaseCredentialEntriesCannotHideEarlierSecretValue(t *testing.T) {
	const secret = "first-case-sensitive-secret"
	got := environmentMap(Shell([]string{
		"AZURE_OPENAI_SUBSCRIPTION_KEY=" + secret,
		"azure_openai_subscription_key=decoy",
		"LANG=prefix-" + secret,
	}))
	if _, ok := got["LANG"]; ok {
		t.Fatal("case-duplicate credential name hid an earlier secret value")
	}
}

func TestNonSecretMapDropsRenamedSecretValue(t *testing.T) {
	const secret = "ambient-token-value"
	got := NonSecretMap([]string{"PLUGIN_TOKEN=" + secret, "RENAMED=" + secret, "SAFE=value"})
	if _, ok := got["PLUGIN_TOKEN"]; ok {
		t.Fatal("credential-named value survived")
	}
	if _, ok := got["RENAMED"]; ok {
		t.Fatal("renamed credential value survived")
	}
	if got["SAFE"] != "value" {
		t.Fatalf("safe value missing: %#v", got)
	}
}

func TestModelCredentialNameCoversProviderFamiliesWithoutTreatingEndpointAsSecret(t *testing.T) {
	for _, name := range []string{
		"AZURE_OPENAI_SUBSCRIPTION_KEY", "azure_openai_api_key", "OPENAI_API_KEY",
		"AGENTX_AUTH_TOKEN", "AGENTX_OAUTH_TOKEN", "AWS_ACCESS_KEY_ID",
		"AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_CONFIG_FILE", "AWS_PROFILE",
		"AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE", "AZURE_CLIENT_CERTIFICATE_PATH",
		"AZURE_FEDERATED_TOKEN_FILE", "GOOGLE_APPLICATION_CREDENTIALS",
		"GOOGLE_GHA_CREDS_PATH", "CLOUDSDK_AUTH_ACCESS_TOKEN", "CLOUDSDK_CONFIG",
	} {
		if !ModelCredentialName(name) {
			t.Errorf("provider credential name %q was not protected", name)
		}
	}
	for _, name := range []string{"AZURE_OPENAI_ENDPOINT", "AZURE_OPENAI_DEPLOYMENT", "OPENAI_ORGANIZATION"} {
		if ModelCredentialName(name) {
			t.Errorf("noncredential provider setting %q was blocked", name)
		}
	}
}

func TestSensitiveNameCoversCredentialStoresAndCapabilitySockets(t *testing.T) {
	for _, name := range []string{
		"DOCKER_HOST",
		"DBUS_SESSION_BUS_ADDRESS",
		"GPG_AGENT_INFO",
		"XDG_RUNTIME_DIR",
		"NPM_CONFIG_USERCONFIG",
		"DOTENV_CONFIG_PATH",
		"CODEX_HOME",
		"AGENTX_CONFIG_DIR",
		"GNUPGHOME",
	} {
		if !SensitiveName(name) {
			t.Errorf("credential store or capability variable %q was not protected", name)
		}
	}
}

func TestHookUsesSnapshotAndExplicitAllowWithoutSecretAliases(t *testing.T) {
	root := t.TempDir()
	const secret = "ambient-subscription-value"
	got, err := Hook(map[string]string{
		"PATH": root, "HOME": root, "SAFE": "visible",
		"AZURE_OPENAI_SUBSCRIPTION_KEY": secret, "ALIAS": "x" + secret,
	}, map[string]bool{"SAFE": true, "ALIAS": true, "AZURE_OPENAI_SUBSCRIPTION_KEY": true}, map[string]string{"AGENTX_PROJECT_DIR": root})
	if err != nil {
		t.Fatal(err)
	}
	values := environmentMap(got)
	if values["SAFE"] != "visible" || values["AGENTX_PROJECT_DIR"] != root {
		t.Fatalf("hook environment = %#v", values)
	}
	for _, name := range []string{"AZURE_OPENAI_SUBSCRIPTION_KEY", "ALIAS"} {
		if _, ok := values[name]; ok {
			t.Errorf("hook retained %s", name)
		}
	}
}

func TestMCPBlocksHostModelCredentialButAllowsExplicitServerCredential(t *testing.T) {
	root := t.TempDir()
	const modelSecret = "model-subscription-value"
	environment := []string{
		"PATH=" + root,
		"AZURE_OPENAI_SUBSCRIPTION_KEY=" + modelSecret,
		"MODEL_ALIAS=" + modelSecret,
		"AWS_SHARED_CREDENTIALS_FILE=/private/model-credentials",
		"AWS_FILE_ALIAS=/private/model-credentials",
		"GITHUB_TOKEN=server-token-value",
	}
	lookup := MCPExpansionLookup(environment)
	for _, name := range []string{
		"AZURE_OPENAI_SUBSCRIPTION_KEY", "MODEL_ALIAS",
		"AWS_SHARED_CREDENTIALS_FILE", "AWS_FILE_ALIAS",
	} {
		if value, ok := lookup(name); ok || value != "" {
			t.Fatalf("model credential expansion %s = %q, %v", name, value, ok)
		}
	}
	if value, ok := lookup("GITHUB_TOKEN"); !ok || value != "server-token-value" {
		t.Fatalf("server-scoped expansion = %q, %v", value, ok)
	}
	if _, err := MCP(environment, map[string]string{"RENAMED": "prefix-" + modelSecret}); err == nil {
		t.Fatal("renamed host model credential reached MCP child")
	}
	got, err := MCP(environment, map[string]string{"GITHUB_TOKEN": "server-token-value"})
	if err != nil {
		t.Fatal(err)
	}
	if environmentMap(got)["GITHUB_TOKEN"] != "server-token-value" {
		t.Fatalf("explicit MCP credential missing: %#v", got)
	}
}

func TestGenericProcessOverlayRejectsModelCredentialAliases(t *testing.T) {
	const secret = "generic-process-model-secret"
	environment := []string{"AZURE_OPENAI_SUBSCRIPTION_KEY=" + secret, "LANG=C"}
	if _, err := Process(environment, false, map[string]string{"RENAMED": secret}); err == nil {
		t.Fatal("generic process accepted renamed model credential")
	}
	got, err := Process(environment, true, map[string]string{"HELPER_MODE": "1"})
	if err != nil {
		t.Fatal(err)
	}
	values := environmentMap(got)
	if values["HELPER_MODE"] != "1" || values["LANG"] != "C" {
		t.Fatalf("generic process environment = %#v", values)
	}
	if _, ok := values["AZURE_OPENAI_SUBSCRIPTION_KEY"]; ok {
		t.Fatal("generic process inherited model credential")
	}
	empty, err := Process(environment, false, nil)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty process environment = %#v, %v", empty, err)
	}
}

func TestGitEnvironmentIsCredentialFreeAndDisablesConfig(t *testing.T) {
	root := t.TempDir()
	const secret = "git-secret-value"
	got := environmentMap(Git([]string{
		"PATH=" + root,
		"HOME=" + root,
		"AWS_SECRET_ACCESS_KEY=" + secret,
		"LANG=" + secret,
	}))
	if _, ok := got["HOME"]; ok {
		t.Fatal("Git context inherited HOME")
	}
	if _, ok := got["AWS_SECRET_ACCESS_KEY"]; ok {
		t.Fatal("Git context inherited credential")
	}
	if _, ok := got["LANG"]; ok {
		t.Fatal("Git context retained renamed credential value")
	}
	if got["GIT_CONFIG_GLOBAL"] != os.DevNull || got["GIT_CONFIG_NOSYSTEM"] != "1" || got["GIT_TERMINAL_PROMPT"] != "0" ||
		got["GIT_CONFIG_COUNT"] != "2" || got["GIT_CONFIG_KEY_0"] != "core.fsmonitor" || got["GIT_CONFIG_VALUE_0"] != "false" ||
		got["GIT_CONFIG_KEY_1"] != "core.hooksPath" || got["GIT_CONFIG_VALUE_1"] != os.DevNull {
		t.Fatalf("Git safety environment = %#v", got)
	}
}

func TestDirectoriesRejectsCredentialAliasesAndNonDirectories(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	const secret = "/private/credential-value"
	got := Directories([]string{
		"TOKEN=" + secret,
		"TMPDIR=" + secret,
		"GOCACHE=" + root,
		"GOMODCACHE=" + missing,
	}, "TMPDIR", "GOCACHE", "GOMODCACHE")
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	resolvedMissing := filepath.Join(resolvedRoot, "missing")
	if len(got) != 2 || got[0] != resolvedRoot || got[1] != resolvedMissing {
		t.Fatalf("safe directories = %#v", got)
	}
	if roots := Directories([]string{"TMPDIR=" + string(os.PathSeparator)}, "TMPDIR"); len(roots) != 0 {
		t.Fatalf("filesystem root became sandbox write root: %#v", roots)
	}
}

func TestHookDoesNotConstructCredentialWhileNormalizingControls(t *testing.T) {
	const secret = "credential-recreated-after-normalization"
	got, err := Hook(map[string]string{
		"AZURE_OPENAI_SUBSCRIPTION_KEY": secret,
		"ALIAS":                         "credential-recreated-\nafter-normalization",
	}, map[string]bool{"ALIAS": true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := environmentMap(got)["ALIAS"]; ok {
		t.Fatal("hook line-control normalization reconstructed a host credential")
	}
}

func TestCanonicalCredentialPathsCannotBeRenamed(t *testing.T) {
	root := t.TempDir()
	intermediate := filepath.Join(root, "intermediate")
	credentialDir := filepath.Join(root, "credential-dir")
	for _, path := range []string{intermediate, credentialDir} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	credentialAlias := intermediate + string(os.PathSeparator) + ".." + string(os.PathSeparator) + "credential-dir"
	environment := []string{
		"AWS_SHARED_CREDENTIALS_FILE=" + credentialAlias,
		"TMPDIR=" + credentialDir,
	}
	if _, ok := environmentMap(Shell(environment))["TMPDIR"]; ok {
		t.Fatal("canonicalized credential path survived under a benign name")
	}
	if _, err := MCP(environment, map[string]string{"RENAMED": credentialDir}); err == nil {
		t.Fatal("canonicalized model credential path reached MCP")
	}
	if roots := Directories(environment, "TMPDIR"); len(roots) != 0 {
		t.Fatalf("canonical credential path became a writable root: %#v", roots)
	}
}

func TestCompleteChildEnvironmentProjectionCannotSpellCredential(t *testing.T) {
	environment := []string{"AZURE_OPENAI_SUBSCRIPTION_KEY=PATH", "LANG=C"}
	if got := Shell(environment); got == nil || len(got) != 0 {
		t.Fatalf("credential equal to framing key reached shell: %#v", got)
	}
	if _, err := MCP(environment, nil); err == nil {
		t.Fatal("credential equal to framing key reached MCP")
	}
	if got := NonSecretMap([]string{"TOKEN=SAFE", "SAFE=value"}); len(got) != 0 {
		t.Fatalf("credential equal to map key survived: %#v", got)
	}
	if _, err := Hook(
		map[string]string{"TOKEN": "AGENTX_PROJECT_DIR"},
		nil,
		map[string]string{"AGENTX_PROJECT_DIR": "/workspace"},
	); err == nil {
		t.Fatal("credential equal to runtime-owned key reached hook")
	}
}

func TestAmbiguousAndOversizedAmbientEnvironmentsFailClosed(t *testing.T) {
	duplicate := []string{"LANG=C", "lang=en_US.UTF-8"}
	if got := Shell(duplicate); got == nil || len(got) != 0 {
		t.Fatalf("case-duplicate shell environment = %#v", got)
	}
	if _, err := MCP(duplicate, nil); err == nil {
		t.Fatal("case-duplicate MCP environment was accepted")
	}
	if value, ok := MCPExpansionLookup(duplicate)("LANG"); ok || value != "" {
		t.Fatalf("case-duplicate expansion = %q, %v", value, ok)
	}

	oversized := make([]string, maximumEnvironmentItems+1)
	for index := range oversized {
		oversized[index] = fmt.Sprintf("SAFE_%03d=value", index)
	}
	if got := Shell(oversized); got == nil || len(got) != 0 {
		t.Fatalf("oversized shell environment = %#v", got)
	}
	if _, err := Process(oversized, true, nil); err == nil {
		t.Fatal("oversized process environment was accepted")
	}

	invalidCredentialName := []string{
		"AZURE_OPENAI_AUTH-TOKEN=invalid-name-secret",
		"LANG=invalid-name-secret",
	}
	if got := Shell(invalidCredentialName); got == nil || len(got) != 0 {
		t.Fatalf("invalid credential name alias = %#v", got)
	}
	if _, err := MCP(invalidCredentialName, nil); err == nil {
		t.Fatal("invalid model-credential name was ignored")
	}
}

func TestEnvironmentMapsRejectCaseCollisionsAndUnsafeText(t *testing.T) {
	if _, err := Hook(
		map[string]string{"SAFE": "one", "safe": "two"},
		map[string]bool{"SAFE": true},
		nil,
	); err == nil {
		t.Fatal("case-colliding hook snapshot was accepted")
	}
	if _, err := serialize(map[string]string{"SAFE": "one", "safe": "two"}); err == nil {
		t.Fatal("case-colliding serialized environment was accepted")
	}
	if _, err := Process(nil, false, map[string]string{"SAFE": "line\nbreak"}); err == nil {
		t.Fatal("control-bearing explicit process value was accepted")
	}
	if _, err := MCP(nil, map[string]string{"SAFE": string([]byte{0xff})}); err == nil {
		t.Fatal("invalid UTF-8 explicit MCP value was accepted")
	}
}

func TestCredentialScopeAndExpansionSnapshotsAreBoundedAndImmutable(t *testing.T) {
	tooManySecrets := make([]string, maximumSecretItems+1)
	for index := range tooManySecrets {
		tooManySecrets[index] = fmt.Sprintf("AGENTX_AUTH_TOKEN_%03d=secret-%03d", index, index)
	}
	if got := Shell(tooManySecrets); got == nil || len(got) != 0 {
		t.Fatalf("oversized credential scope = %#v", got)
	}
	if _, err := MCP(tooManySecrets, nil); err == nil {
		t.Fatal("oversized MCP credential scope was accepted")
	}

	environment := []string{"SAFE=before"}
	lookup := MCPExpansionLookup(environment)
	environment[0] = "SAFE=after"
	if value, ok := lookup("SAFE"); !ok || value != "before" {
		t.Fatalf("expansion snapshot changed with caller slice: %q, %v", value, ok)
	}
}
