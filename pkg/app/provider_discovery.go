package app

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/greenpau/agentx/pkg/cli"
	"github.com/greenpau/agentx/pkg/config"
	"github.com/greenpau/agentx/pkg/redact"
	"github.com/greenpau/agentx/pkg/surface"
)

const (
	providerDiscoveryVersion       = 1
	maximumProviderDiscoveryOutput = 1 << 20
)

type providerDiscoveryResult struct {
	Version   int              `json:"version"`
	Providers []map[string]any `json:"providers"`
}

func runProviderDiscovery(
	home *applicationHome,
	format cli.OutputFormat,
	stdout io.Writer,
) error {
	if home == nil {
		return errors.New("AgentX home is unavailable for provider discovery")
	}
	registry, err := home.loadProviderRegistry()
	credentials := registry.CredentialSanitizer()
	if err != nil {
		if credentials.Empty() {
			return err
		}
		return redactOperationalError(err, credentials.Apply)
	}
	descriptors := registry.Providers()
	providers := sdkProviderCatalogFromDescriptors(descriptors)
	if format == cli.OutputJSON {
		return writeProviderDiscoveryJSON(stdout, credentials, providerDiscoveryResult{
			Version: providerDiscoveryVersion, Providers: providers,
		})
	}
	return writeProviderDiscoveryText(stdout, credentials, descriptors)
}

func writeProviderDiscoveryJSON(
	writer io.Writer,
	credentials *redact.Set,
	result providerDiscoveryResult,
) error {
	var buffered strings.Builder
	encoder := surface.NewEncoder(&buffered)
	if err := encoder.SetValidator(credentialJSONValidator(credentials)); err != nil {
		return err
	}
	if err := encoder.Encode(result); err != nil {
		return err
	}
	if buffered.Len() > maximumProviderDiscoveryOutput {
		return errors.New("provider discovery output exceeds its size limit")
	}
	// Encode into private memory first. A validation, marshal, or size failure
	// therefore cannot commit a partial public protocol record.
	return writeStringExact(writer, buffered.String())
}

func writeProviderDiscoveryText(
	writer io.Writer,
	credentials *redact.Set,
	descriptors []config.ProviderDescriptor,
) error {
	var output strings.Builder
	output.WriteString("ID\tTYPE\tMODEL\tDEFAULT\tREASONING_EFFORTS\tDEFAULT_EFFORT\n")
	for _, provider := range descriptors {
		fmt.Fprintf(
			&output,
			"%s\t%s\t%s\t%t\t%s\t%s\n",
			provider.ID,
			provider.Type,
			provider.Model,
			provider.Default,
			strings.Join(provider.Reasoning.Efforts, ","),
			provider.Reasoning.DefaultEffort,
		)
		if output.Len() > maximumProviderDiscoveryOutput {
			return errors.New("provider discovery output exceeds its size limit")
		}
	}
	// Guard the complete physical record after headers, separators, and final
	// newline are assembled so no pair of safe fields can reconstruct a key.
	// Discovery is an exact metadata protocol: reject an unsafe frame instead
	// of silently replacing part of a descriptor with a redaction marker.
	physical, err := terminalRecord(output.String(), credentials)
	if err != nil {
		return err
	}
	if physical != output.String() {
		return errors.New("provider discovery output overlaps configured credential material")
	}
	return writeStringExact(writer, physical)
}
