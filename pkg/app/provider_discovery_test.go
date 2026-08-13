package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/greenpau/agentx/pkg/config"
)

type partialProviderDiscoveryWriter struct {
	writes int
	bytes  int
}

func (writer *partialProviderDiscoveryWriter) Write(data []byte) (int, error) {
	writer.writes++
	accepted := len(data) / 2
	writer.bytes += accepted
	return accepted, io.ErrClosedPipe
}

func providerDiscoveryFixtures() []testAuthFileProvider {
	return []testAuthFileProvider{
		{
			ID: "sol-east", Type: "azure_openai",
			Capabilities: testAuthFileCapabilities{Reasoning: testAuthFileReasoning{
				Efforts: []string{"none", "low", "high", "max"}, DefaultEffort: "high",
			}},
			AzureOpenAI: testAzureOpenAIAuthFile{
				Endpoint: "https://127.0.0.1:1/sol-private-route", Model: "gpt-5.6-sol",
				Deployment: "sol-private-deployment", APIKey: "sol-private-api-key", APIVersion: "sol-private-api-version",
			},
		},
		{
			ID: "terra-west", Type: "azure_openai",
			Capabilities: testAuthFileCapabilities{Reasoning: testAuthFileReasoning{
				Efforts: []string{"none", "medium", "xhigh"}, DefaultEffort: "medium",
			}},
			AzureOpenAI: testAzureOpenAIAuthFile{
				Endpoint: "https://127.0.0.1:1/terra-private-route", Model: "gpt-5.6-terra",
				Deployment: "terra-private-deployment", APIKey: "terra-private-api-key", APIVersion: "terra-private-api-version",
			},
		},
	}
}

func TestProviderDiscoveryJSONIsExactProviderNeutralAndSDKCompatible(t *testing.T) {
	home := filepath.Join(t.TempDir(), "agentx-home")
	providers := providerDiscoveryFixtures()
	writeTestAuthRegistry(t, home, providers)
	t.Setenv("AGENTX_HOME", home)
	// Discovery validates declared metadata but must not derive or select an
	// effective provider from process reasoning configuration.
	t.Setenv("AGENTX_REASONING_EFFORT", "unsupported-for-every-provider")

	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".agentx"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".agentx", "mcp.json"), []byte(`{not-json`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(workspace)

	var stdout, stderr bytes.Buffer
	if err := Run(
		t.Context(),
		[]string{"--list-providers", "--output-format", "json"},
		strings.NewReader("ignored stdin"),
		&stdout,
		&stderr,
	); err != nil {
		t.Fatalf("provider discovery: %v; stderr=%q", err, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("provider discovery wrote diagnostics: %q", stderr.String())
	}
	const want = "{\"version\":1,\"providers\":[{\"default\":false,\"defaultReasoningEffort\":\"high\",\"description\":\"Deployment-backed model endpoint configured by AgentX-home auth.json\",\"displayName\":\"sol-east (gpt-5.6-sol)\",\"id\":\"sol-east\",\"model\":\"gpt-5.6-sol\",\"providerType\":\"azure_openai\",\"reasoning\":{\"defaultEffort\":\"high\",\"efforts\":[\"none\",\"low\",\"high\",\"max\"],\"supported\":true},\"selected\":false,\"supportedReasoningEfforts\":[\"none\",\"low\",\"high\",\"max\"],\"supportsEffort\":true,\"value\":\"sol-east\"},{\"default\":false,\"defaultReasoningEffort\":\"medium\",\"description\":\"Deployment-backed model endpoint configured by AgentX-home auth.json\",\"displayName\":\"terra-west (gpt-5.6-terra)\",\"id\":\"terra-west\",\"model\":\"gpt-5.6-terra\",\"providerType\":\"azure_openai\",\"reasoning\":{\"defaultEffort\":\"medium\",\"efforts\":[\"none\",\"medium\",\"xhigh\"],\"supported\":true},\"selected\":false,\"supportedReasoningEfforts\":[\"none\",\"medium\",\"xhigh\"],\"supportsEffort\":true,\"value\":\"terra-west\"}]}\n"
	if stdout.String() != want {
		t.Fatalf("provider discovery JSON mismatch\ngot:  %s\nwant: %s", stdout.String(), want)
	}

	var discovery providerDiscoveryResult
	if err := json.Unmarshal(stdout.Bytes(), &discovery); err != nil {
		t.Fatal(err)
	}
	registry, err := config.LoadProviderRegistry(filepath.Join(home, config.DefaultAuthFile))
	if err != nil {
		t.Fatal(err)
	}
	session := newSDKWireSession(t)
	session.config.Providers = registry.Providers()
	sdk, err := sdkInitializeResponse(session)
	if err != nil {
		t.Fatal(err)
	}
	sdkWire, err := json.Marshal(sdk["providers"])
	if err != nil {
		t.Fatal(err)
	}
	var normalizedSDK []map[string]any
	if err := json.Unmarshal(sdkWire, &normalizedSDK); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(discovery.Providers, normalizedSDK) {
		t.Fatalf("discovery and SDK provider descriptors drifted:\ndiscovery=%#v\nSDK=%#v", discovery.Providers, sdk["providers"])
	}
	for _, forbidden := range []string{
		"127.0.0.1", "private-route", "private-deployment", "private-api-key", "private-api-version",
		`\"endpoint\"`, `\"deployment\"`, `\"api_version\"`, `\"api_key\"`, `\"provider_binding\"`,
	} {
		if strings.Contains(stdout.String(), forbidden) {
			t.Fatalf("provider discovery exposed private routing material %q: %s", forbidden, stdout.String())
		}
	}
	entries, err := os.ReadDir(filepath.Join(home, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("provider discovery constructed session state: %#v", entries)
	}
}

func TestProviderDiscoveryTextIsBoundedCompleteRecord(t *testing.T) {
	home := filepath.Join(t.TempDir(), "agentx-home")
	providers := providerDiscoveryFixtures()
	providers[1].Default = true
	writeTestAuthRegistry(t, home, providers)
	t.Setenv("AGENTX_HOME", home)

	var stdout bytes.Buffer
	if err := Run(t.Context(), []string{"--list-providers"}, strings.NewReader(""), &stdout, io.Discard); err != nil {
		t.Fatal(err)
	}
	const want = "ID\tTYPE\tMODEL\tDEFAULT\tREASONING_EFFORTS\tDEFAULT_EFFORT\n" +
		"sol-east\tazure_openai\tgpt-5.6-sol\tfalse\tnone,low,high,max\thigh\n" +
		"terra-west\tazure_openai\tgpt-5.6-terra\ttrue\tnone,medium,xhigh\tmedium\n"
	if stdout.String() != want {
		t.Fatalf("provider discovery text mismatch\ngot:  %q\nwant: %q", stdout.String(), want)
	}
}

func TestProviderDiscoveryFailureCommitsNoStdout(t *testing.T) {
	for _, test := range []struct {
		name   string
		format string
		auth   func(t *testing.T, home string)
	}{
		{
			name: "malformed registry", format: "json",
			auth: func(t *testing.T, home string) {
				if err := os.MkdirAll(home, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(home, config.DefaultAuthFile), []byte(`{"version":2,"providers":[`), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "multiple declared defaults", format: "json",
			auth: func(t *testing.T, home string) {
				providers := providerDiscoveryFixtures()
				providers[0].Default = true
				providers[1].Default = true
				writeTestAuthRegistry(t, home, providers)
			},
		},
		{
			name: "JSON output collides with credential", format: "json",
			auth: func(t *testing.T, home string) {
				providers := providerDiscoveryFixtures()
				providers[1].AzureOpenAI.APIKey = "displayName"
				writeTestAuthRegistry(t, home, providers)
			},
		},
		{
			name: "JSON wrapper framing collides with credential", format: "json",
			auth: func(t *testing.T, home string) {
				providers := providerDiscoveryFixtures()
				providers[1].AzureOpenAI.APIKey = `"version":1`
				writeTestAuthRegistry(t, home, providers)
			},
		},
		{
			name: "text output collides with credential", format: "text",
			auth: func(t *testing.T, home string) {
				providers := providerDiscoveryFixtures()
				providers[1].AzureOpenAI.APIKey = "REASONING_EFFORTS"
				writeTestAuthRegistry(t, home, providers)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "agentx-home")
			test.auth(t, home)
			t.Setenv("AGENTX_HOME", home)
			var stdout bytes.Buffer
			err := Run(
				t.Context(),
				[]string{"--list-providers", "--output-format", test.format},
				strings.NewReader(""),
				&stdout,
				io.Discard,
			)
			if err == nil {
				t.Fatal("unsafe provider discovery unexpectedly succeeded")
			}
			if stdout.Len() != 0 {
				t.Fatalf("failed discovery committed stdout: %q", stdout.String())
			}
		})
	}
}

func TestProviderDiscoveryContainsWriterFailuresWithoutRetry(t *testing.T) {
	home := filepath.Join(t.TempDir(), "agentx-home")
	writeTestAuthRegistry(t, home, providerDiscoveryFixtures())
	t.Setenv("AGENTX_HOME", home)

	for _, format := range []string{"text", "json"} {
		t.Run(format+" partial failure", func(t *testing.T) {
			writer := &partialProviderDiscoveryWriter{}
			err := Run(
				t.Context(),
				[]string{"--list-providers", "--output-format", format},
				strings.NewReader(""),
				writer,
				io.Discard,
			)
			if !errors.Is(err, errTerminalWriterFailed) {
				t.Fatalf("partial writer error = %v, want sealed terminal failure", err)
			}
			if writer.writes != 1 || writer.bytes == 0 {
				t.Fatalf("partial writer calls = %d, accepted bytes = %d", writer.writes, writer.bytes)
			}
		})
		t.Run(format+" short write", func(t *testing.T) {
			err := Run(
				t.Context(),
				[]string{"--list-providers", "--output-format", format},
				strings.NewReader(""),
				terminalShortWriter{},
				io.Discard,
			)
			if !errors.Is(err, io.ErrShortWrite) {
				t.Fatalf("short writer error = %v", err)
			}
		})
		t.Run(format+" panic", func(t *testing.T) {
			err := Run(
				t.Context(),
				[]string{"--list-providers", "--output-format", format},
				strings.NewReader(""),
				panicWriter{},
				io.Discard,
			)
			if !errors.Is(err, errTerminalWriterPanicked) {
				t.Fatalf("panic writer error = %v", err)
			}
		})
	}
}
