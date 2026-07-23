package model

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maximumProviderEventTypeBytes  = 128
	maximumProviderIDBytes         = 256
	maximumProviderNameBytes       = 128
	maximumProviderEnumBytes       = 64
	maximumProviderDiagnosticBytes = 64 << 10
)

// validateOpaqueProviderValue rejects provider-owned metadata that cannot be
// safely retained verbatim. Correlation values and authenticated opaque state
// must never be rewritten: changing them would either corrupt pairing or make
// replay unverifiable. A credential reflection therefore terminates the
// response with a generic protocol error before the value becomes a map key,
// event field, diagnostic, or durable record.
func (c *AzureClient) validateOpaqueProviderValue(field, value string, maximum int, allowSpace bool) error {
	if value == "" {
		return nil
	}
	if !utf8.ValidString(value) || maximum > 0 && len(value) > maximum {
		return fmt.Errorf("%w: provider %s is unsafe", ErrProtocol, field)
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.In(character, unicode.Cf, unicode.Zl, unicode.Zp) || !allowSpace && unicode.IsSpace(character) {
			return fmt.Errorf("%w: provider %s is unsafe", ErrProtocol, field)
		}
	}
	if c.redact(value) != value {
		return fmt.Errorf("%w: provider %s reflected configured credential", ErrProtocol, field)
	}
	return nil
}

func (c *AzureClient) validateAzureEnvelopeMetadata(rawType string, envelope azureEventEnvelope) error {
	if rawType == "" {
		return fmt.Errorf("%w: provider event type is missing", ErrProtocol)
	}
	if err := c.validateOpaqueProviderValue("event type", rawType, maximumProviderEventTypeBytes, false); err != nil {
		return err
	}
	if err := c.validateOpaqueProviderValue("event item id", envelope.ItemID, maximumProviderIDBytes, false); err != nil {
		return err
	}
	if err := c.validateOpaqueProviderValue("event tool name", envelope.Name, maximumProviderNameBytes, true); err != nil {
		return err
	}
	if err := c.validateAzureResponseMetadata(envelope.Response); err != nil {
		return err
	}
	return c.validateAzureItemMetadata(envelope.Item)
}

func (c *AzureClient) validateAzureResponseMetadata(response azureResponse) error {
	fields := []struct {
		name       string
		value      string
		maximum    int
		allowSpace bool
	}{
		{name: "response id", value: response.ID, maximum: maximumProviderIDBytes},
		{name: "response model", value: response.Model, maximum: maximumProviderIDBytes},
		{name: "response status", value: response.Status, maximum: maximumProviderEnumBytes},
		{name: "previous response id", value: response.PreviousResponseID, maximum: maximumProviderIDBytes},
	}
	for _, field := range fields {
		if err := c.validateOpaqueProviderValue(field.name, field.value, field.maximum, field.allowSpace); err != nil {
			return err
		}
	}
	for _, item := range response.Output {
		if err := c.validateAzureItemMetadata(item); err != nil {
			return err
		}
	}
	return nil
}

func (c *AzureClient) validateAzureItemMetadata(item azureResponseItem) error {
	fields := []struct {
		name       string
		value      string
		maximum    int
		allowSpace bool
	}{
		{name: "item type", value: item.Type, maximum: maximumProviderEnumBytes},
		{name: "item id", value: item.ID, maximum: maximumProviderIDBytes},
		{name: "item role", value: item.Role, maximum: maximumProviderEnumBytes},
		{name: "item status", value: item.Status, maximum: maximumProviderEnumBytes},
		{name: "item phase", value: item.Phase, maximum: maximumProviderEnumBytes},
		{name: "call id", value: item.CallID, maximum: maximumProviderIDBytes},
		{name: "tool name", value: item.Name, maximum: maximumProviderNameBytes, allowSpace: true},
		{name: "encrypted reasoning state", value: item.EncryptedContent, allowSpace: true},
	}
	for _, field := range fields {
		if err := c.validateOpaqueProviderValue(field.name, field.value, field.maximum, field.allowSpace); err != nil {
			return err
		}
	}
	for _, part := range item.Content {
		if err := c.validateOpaqueProviderValue("content type", part.Type, maximumProviderEnumBytes, false); err != nil {
			return err
		}
	}
	for _, part := range item.Summary {
		if err := c.validateOpaqueProviderValue("summary type", part.Type, maximumProviderEnumBytes, false); err != nil {
			return err
		}
	}
	return nil
}

func (c *AzureClient) validateItemMetadata(item Item) error {
	fields := []struct {
		name       string
		value      string
		maximum    int
		allowSpace bool
	}{
		{name: "item type", value: string(item.Type), maximum: maximumProviderEnumBytes},
		{name: "item id", value: item.ID, maximum: maximumProviderIDBytes},
		{name: "API response id", value: item.APIResponseID, maximum: maximumProviderIDBytes},
		{name: "item role", value: string(item.Role), maximum: maximumProviderEnumBytes},
		{name: "item status", value: item.Status, maximum: maximumProviderEnumBytes},
		{name: "item phase", value: item.Phase, maximum: maximumProviderEnumBytes},
		{name: "call id", value: item.CallID, maximum: maximumProviderIDBytes},
		{name: "tool name", value: item.Name, maximum: maximumProviderNameBytes, allowSpace: true},
		{name: "encrypted reasoning state", value: item.EncryptedContent, allowSpace: true},
	}
	for _, field := range fields {
		if err := c.validateOpaqueProviderValue(field.name, field.value, field.maximum, field.allowSpace); err != nil {
			return err
		}
	}
	for _, part := range item.Content {
		if err := c.validateOpaqueProviderValue("content type", string(part.Type), maximumProviderEnumBytes, false); err != nil {
			return err
		}
	}
	for _, part := range item.Summary {
		if err := c.validateOpaqueProviderValue("summary type", string(part.Type), maximumProviderEnumBytes, false); err != nil {
			return err
		}
	}
	return nil
}

// validateFunctionCallArguments treats Arguments as a nested JSON document,
// not merely as the outer event's string spelling. This closes aliases such as
// \uXXXX and escaped solidus sequences that are semantically credentials but
// do not occur literally in the enclosing request or event JSON.
func (c *AzureClient) validateFunctionCallArguments(arguments string) error {
	if arguments == "" || c.credentialSanitizer().Empty() {
		return nil
	}
	reflected, err := c.credentialSanitizer().JSONContains([]byte(arguments))
	if err != nil {
		return fmt.Errorf("%w: function-call arguments could not be safely inspected", ErrProtocol)
	}
	if reflected {
		return fmt.Errorf("%w: function-call arguments reflected configured credential material", ErrProtocol)
	}
	return nil
}

func (c *AzureClient) validateFunctionCallItems(items []Item) error {
	for _, item := range items {
		if item.Type == ItemFunctionCall {
			if err := c.validateFunctionCallArguments(item.Arguments); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *AzureClient) validateCanonicalCredentialEnvelope(name string, value any) error {
	if c.credentialSanitizer().Empty() {
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("%w: provider %s could not be safely encoded", ErrProtocol, name)
	}
	reflected, err := c.credentialSanitizer().JSONContains(encoded)
	if err != nil {
		return fmt.Errorf("%w: provider %s could not be safely inspected", ErrProtocol, name)
	}
	if reflected {
		return fmt.Errorf("%w: provider %s reflected configured credential material", ErrProtocol, name)
	}
	return nil
}

func (c *AzureClient) validatePublicResponseEnvelope(response *Response) error {
	if response == nil {
		return nil
	}
	if err := c.validateFunctionCallItems(response.Output); err != nil {
		return err
	}
	return c.validateCanonicalCredentialEnvelope("response envelope", response)
}

func (c *AzureClient) validatePublicEventEnvelope(event Event) error {
	if event.Call != nil && event.Call.Type == ItemFunctionCall {
		if err := c.validateFunctionCallArguments(event.Call.Arguments); err != nil {
			return err
		}
	}
	if err := c.validatePublicResponseEnvelope(event.Response); err != nil {
		return err
	}
	if event.Error != nil && c.redact(event.Error.Error()) != event.Error.Error() {
		return fmt.Errorf("%w: provider error composition reflected configured credential material", ErrProtocol)
	}
	return c.validateCanonicalCredentialEnvelope("event envelope", event)
}

// finalizeProviderError closes both the human Error rendering and the exported
// structured field envelope. Retry classification and parsed delay remain
// private/internal even if provider diagnostics must be replaced wholesale.
func (c *AzureClient) finalizeProviderError(providerError *ProviderError) *ProviderError {
	if providerError == nil {
		return nil
	}
	result := *providerError
	result.display = c.redact(result.composeError())
	result.displaySet = true
	if err := c.validateCanonicalCredentialEnvelope("error envelope", &result); err == nil {
		return &result
	}
	result.Code = ""
	result.Type = ""
	result.Param = ""
	result.Message = c.redact("provider request failed")
	result.RequestID = ""
	result.display = c.redact(result.composeError())
	return &result
}

// sanitizeProviderErrorFields treats a provider error as one untrusted record,
// not as independently safe strings. A malicious endpoint could otherwise
// place adjacent fragments of the credential in separate fields and rely on a
// structured consumer to reassemble them. Correlation is unnecessary for an
// error, so any cross-field reflection is replaced wholesale.
func (c *AzureClient) sanitizeProviderErrorFields(fields azureError, requestID string) (azureError, string) {
	values := []string{
		fields.Code,
		fields.Type,
		fields.Param,
		fields.Message,
		requestID,
	}
	// Redact exact matches in each raw field first. Then detect credentials
	// reconstructed across raw provider-owned field boundaries before
	// normalization can replace invalid UTF-8 or unsafe Unicode formatting
	// characters with a different spelling.
	for index := range values {
		values[index] = c.redact(values[index])
	}
	if c.providerValuesReflectCredential(values) {
		return azureError{Message: c.sanitizeProviderDiagnostic("provider returned unsafe error metadata")}, ""
	}
	for index := range values {
		// Redact on both sides of normalization. The first pass ensures our own
		// normalization cannot turn an exact reflected credential into an
		// uncovered alias; the second catches ordinary safe literals after the
		// provider text has been canonicalized.
		values[index] = c.redact(normalizeProviderDiagnostic(values[index]))
	}
	if c.providerValuesReflectCredential(values) {
		return azureError{Message: c.sanitizeProviderDiagnostic("provider returned unsafe error metadata")}, ""
	}
	for index := range values {
		values[index] = boundProviderDiagnostic(values[index])
	}
	// Bounding changes field boundaries. Check the exact exported values again:
	// truncating a safe tail could otherwise bring a credential prefix at the
	// cutoff directly next to a suffix in another structured field.
	if c.providerValuesReflectCredential(values) {
		return azureError{Message: c.sanitizeProviderDiagnostic("provider returned unsafe error metadata")}, ""
	}
	return azureError{
		Code: values[0], Type: values[1],
		Param: values[2], Message: values[3],
	}, values[4]
}

func normalizeProviderDiagnostic(value string) string {
	value = strings.ToValidUTF8(value, "\uFFFD")
	var normalized strings.Builder
	normalized.Grow(len(value))
	for _, character := range value {
		if unicode.IsControl(character) || unicode.In(character, unicode.Cf, unicode.Zl, unicode.Zp) {
			character = '\uFFFD'
		}
		normalized.WriteRune(character)
	}
	return normalized.String()
}

func boundProviderDiagnostic(value string) string {
	if len(value) <= maximumProviderDiagnosticBytes {
		return value
	}
	value = value[:maximumProviderDiagnosticBytes]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func (c *AzureClient) sanitizeProviderDiagnostic(value string) string {
	// Redact before normalization so replacing invalid UTF-8, controls, or
	// formatting characters cannot turn an exact credential into an uncovered
	// alias. Redact again after normalization, then bound; bounding first can
	// retain a credential prefix whose final bytes fell just past the cutoff.
	return boundProviderDiagnostic(c.redact(normalizeProviderDiagnostic(c.redact(value))))
}

func (c *AzureClient) providerValuesReflectCredential(values []string) bool {
	return c.credentialSanitizer().ContainsAcrossPermutations(values)
}
