package model

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/greenpau/agentx/pkg/config"
	"github.com/greenpau/agentx/pkg/redact"
)

const (
	defaultRetryBase        = 500 * time.Millisecond
	defaultRetryMaximum     = 32 * time.Second
	defaultRetryWindow      = 2 * time.Minute
	defaultMaximumEvent     = 32 << 20
	defaultMaximumErrorBody = 1 << 20
	defaultMaximumResponse  = 64 << 20
	defaultMaximumEvents    = 100_000
	defaultMaximumItems     = 4_096
	defaultMaximumToolCalls = 256
	defaultMaximumArguments = 4 << 20
	defaultUserAgent        = "agentx-go/1"
	azureAPIKeyHeader       = "api-key"
)

// RetryInfo describes a retry before any provider stream event has been
// delivered. Error is a detached, credential-redacted observation.
type RetryInfo struct {
	Attempt     int
	MaxAttempts int
	Delay       time.Duration
	Error       error
	RequestID   string

	display    string
	displaySet bool
}

func (i RetryInfo) String() string {
	if i.displaySet {
		return i.display
	}
	return i.composeString()
}

func (i RetryInfo) composeString() string {
	message := fmt.Sprintf("model retry attempt %d/%d after %s", i.Attempt, i.MaxAttempts, i.Delay)
	if i.RequestID != "" {
		message += ": request_id=" + i.RequestID
	}
	if i.Error != nil {
		message += ": " + safeModelErrorString(i.Error)
	}
	return message
}

func (i RetryInfo) GoString() string { return i.String() }
func (i RetryInfo) Format(state fmt.State, verb rune) {
	switch verb {
	case 'q':
		_, _ = fmt.Fprintf(state, "%q", i.String())
	default:
		_, _ = fmt.Fprint(state, i.String())
	}
}

// AzureOptions supplies operational dependencies. Zero values select safe
// defaults. The hooks primarily make backoff and time testable without a live
// service; they must not receive raw headers, bodies, or credentials.
type AzureOptions struct {
	HTTPClient *http.Client
	// CredentialSanitizer contains every configured credential that can reach
	// this provider request through contributed context or tool descriptors.
	// The API key is always unioned in by NewAzureClient.
	CredentialSanitizer *redact.Set
	RetryBase           time.Duration
	// RetryMaximum caps only ordinary exponential backoff. A valid provider
	// Retry-After value is honored directly when it fits inside RetryWindow.
	RetryMaximum time.Duration
	// RetryWindow bounds the wall-clock/planned-delay interval in which a
	// request may start replacement attempts. It does not impose a lifetime on
	// a successfully opened response stream.
	RetryWindow              time.Duration
	MaximumEventBytes        int
	MaximumErrorBytes        int64
	MaximumResponseBytes     int
	MaximumResponseEvents    int
	MaximumResponseItems     int
	MaximumToolCalls         int
	MaximumCallArgumentBytes int
	UserAgent                string
	Now                      func() time.Time
	Jitter                   func(maximum time.Duration) time.Duration
	Sleep                    func(ctx context.Context, delay time.Duration) error
	OnRetry                  func(RetryInfo)
}

// AzureClient adapts the provider-neutral model boundary to Azure OpenAI's
// Responses API. Its credential fields are private, and String/GoString redact
// them to prevent accidental diagnostic disclosure.
type AzureClient struct {
	endpoint       url.URL
	logicalModel   string
	deployment     string
	apiKey         string
	credentialSet  *redact.Set
	apiVersion     string
	effort         string
	requestTimeout time.Duration
	watchdog       time.Duration
	maxRetries     int

	httpClient               *http.Client
	retryBase                time.Duration
	retryMaximum             time.Duration
	retryWindow              time.Duration
	maximumEventBytes        int
	maximumErrorBytes        int64
	maximumResponseBytes     int
	maximumResponseEvents    int
	maximumResponseItems     int
	maximumToolCalls         int
	maximumCallArgumentBytes int
	userAgent                string
	now                      func() time.Time
	jitter                   func(time.Duration) time.Duration
	sleep                    func(context.Context, time.Duration) error
	onRetry                  func(RetryInfo)
}

// azureRequestCompositionError preserves protocol classification without
// retaining any request field or configured credential in diagnostics.
type azureRequestCompositionError struct {
	message string
}

func (e *azureRequestCompositionError) Error() string { return e.message }
func (e *azureRequestCompositionError) Unwrap() error { return ErrProtocol }
func (e *azureRequestCompositionError) GoString() string {
	return e.message
}
func (e *azureRequestCompositionError) Format(state fmt.State, verb rune) {
	switch verb {
	case 'q':
		_, _ = fmt.Fprintf(state, "%q", e.message)
	default:
		_, _ = fmt.Fprint(state, e.message)
	}
}

// NewAzureClient validates and copies configuration into an immutable client.
// The configured Azure deployment, not a presentation label, is always sent as
// the wire model value.
func NewAzureClient(configuration config.Azure, options AzureOptions) (*AzureClient, error) {
	if err := configuration.Validate(); err != nil {
		return nil, fmt.Errorf("construct Azure model client: %w", err)
	}
	credentialSet := redact.Union(redact.New(configuration.APIKey), options.CredentialSanitizer)
	if credentialSet.LiteralCount() > 256 || credentialSet.TotalLiteralBytes() > 64<<10 {
		return nil, errors.New("construct Azure model client: credential redaction workload exceeds its limit")
	}
	if !credentialSet.Empty() && credentialSet.TerminalMarker() == "" {
		return nil, errors.New("construct Azure model client: credential material has no safe streaming projection")
	}
	options.HTTPClient = azureRedirectSafeClient(options.HTTPClient)
	if options.RetryBase <= 0 {
		options.RetryBase = defaultRetryBase
	}
	if options.RetryMaximum <= 0 {
		options.RetryMaximum = defaultRetryMaximum
	}
	if options.RetryMaximum < options.RetryBase {
		return nil, fmt.Errorf("construct Azure model client: retry maximum is below retry base")
	}
	if options.RetryWindow < 0 {
		return nil, fmt.Errorf("construct Azure model client: retry window must not be negative")
	}
	if options.RetryWindow == 0 {
		options.RetryWindow = defaultRetryWindow
	}
	if options.MaximumEventBytes <= 0 {
		options.MaximumEventBytes = defaultMaximumEvent
	}
	if options.MaximumErrorBytes <= 0 {
		options.MaximumErrorBytes = defaultMaximumErrorBody
	}
	if options.MaximumResponseBytes <= 0 {
		options.MaximumResponseBytes = defaultMaximumResponse
	}
	if options.MaximumResponseEvents <= 0 {
		options.MaximumResponseEvents = defaultMaximumEvents
	}
	if options.MaximumResponseItems <= 0 {
		options.MaximumResponseItems = defaultMaximumItems
	}
	if options.MaximumToolCalls <= 0 {
		options.MaximumToolCalls = defaultMaximumToolCalls
	}
	if options.MaximumCallArgumentBytes <= 0 {
		options.MaximumCallArgumentBytes = defaultMaximumArguments
	}
	if options.UserAgent == "" {
		options.UserAgent = defaultUserAgent
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Jitter == nil {
		options.Jitter = randomJitter
	}
	if options.Sleep == nil {
		options.Sleep = sleepContext
	}
	endpoint := *configuration.Endpoint
	client := &AzureClient{
		endpoint:                 endpoint,
		logicalModel:             configuration.ModelName,
		deployment:               configuration.Deployment,
		apiKey:                   configuration.APIKey,
		credentialSet:            credentialSet,
		apiVersion:               configuration.APIVersion,
		effort:                   configuration.ReasoningEffort,
		requestTimeout:           configuration.RequestTimeout,
		watchdog:                 configuration.StreamWatchdog,
		maxRetries:               configuration.MaxRetries,
		httpClient:               options.HTTPClient,
		retryBase:                options.RetryBase,
		retryMaximum:             options.RetryMaximum,
		retryWindow:              options.RetryWindow,
		maximumEventBytes:        options.MaximumEventBytes,
		maximumErrorBytes:        options.MaximumErrorBytes,
		maximumResponseBytes:     options.MaximumResponseBytes,
		maximumResponseEvents:    options.MaximumResponseEvents,
		maximumResponseItems:     options.MaximumResponseItems,
		maximumToolCalls:         options.MaximumToolCalls,
		maximumCallArgumentBytes: options.MaximumCallArgumentBytes,
		userAgent:                options.UserAgent,
		now:                      options.Now,
		jitter:                   options.Jitter,
		sleep:                    options.Sleep,
		onRetry:                  options.OnRetry,
	}
	requestEndpoint := client.requestEndpoint()
	if err := client.validateRequestMetadata(&requestEndpoint, client.requestHeaders(), "construct Azure model client"); err != nil {
		return nil, err
	}
	return client, nil
}

// azureRedirectSafeClient preserves the caller's transport, timeout, cookie
// jar, and other client semantics in a private copy while refusing every HTTP
// redirect. A 307/308 can otherwise replay both the Azure api-key header and
// the complete POST body to a Location chosen by a compromised gateway.
func azureRedirectSafeClient(source *http.Client) *http.Client {
	if source == nil {
		source = &http.Client{}
	}
	client := *source
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		// ErrUseLastResponse returns the unopened redirect response to execute,
		// which normalizes it as a nonretryable provider error. No follow-up
		// request is created and the redirect target sees no credential or body.
		return http.ErrUseLastResponse
	}
	return &client
}

// ModelName returns the logical model identity from configuration. Azure may
// use a differently named deployment on the wire.
func (c *AzureClient) ModelName() string { return c.logicalModel }

// String deliberately exposes no credential material.
func (c *AzureClient) String() string {
	if c == nil {
		return "AzureClient<nil>"
	}
	return c.redact("AzureClient{configured}")
}

// GoString prevents %#v from traversing the client's private credential field.
func (c *AzureClient) GoString() string { return c.String() }
func (c *AzureClient) Format(state fmt.State, verb rune) {
	switch verb {
	case 'q':
		_, _ = fmt.Fprintf(state, "%q", c.String())
	default:
		_, _ = fmt.Fprint(state, c.String())
	}
}

// Stream starts a stateless, streaming Responses API request. Transport and
// retry ownership transfer to the returned stream on success.
func (c *AzureClient) Stream(ctx context.Context, request Request) (Stream, error) {
	if ctx == nil {
		return nil, fmt.Errorf("start Azure response: nil context")
	}
	if err := request.Validate(); err != nil {
		return nil, fmt.Errorf("start Azure response: %w", err)
	}
	if err := c.validateFunctionCallItems(request.Input); err != nil {
		return nil, fmt.Errorf("start Azure response: %w", err)
	}
	effort := request.Reasoning.Effort
	if effort == "" {
		effort = c.effort
	}
	if !validEffort(effort) {
		return nil, fmt.Errorf("start Azure response: %w: unsupported reasoning effort %q", ErrProtocol, effort)
	}
	wireRequest, err := c.projectRequest(request, effort)
	if err != nil {
		return nil, fmt.Errorf("start Azure response: %w", err)
	}
	payload, err := json.Marshal(wireRequest)
	if err != nil {
		return nil, fmt.Errorf("encode Azure response request: %w", err)
	}
	reflected, err := c.credentialSanitizer().JSONContains(payload)
	if err != nil {
		return nil, fmt.Errorf("inspect Azure response request: %w", ErrProtocol)
	}
	if reflected {
		return nil, fmt.Errorf("inspect Azure response request: %w: request reflected configured credential material", ErrProtocol)
	}
	streamContext, cancel := context.WithCancel(ctx)
	stream := &azureStream{
		client:            c,
		ctx:               streamContext,
		cancel:            cancel,
		payload:           payload,
		retryStarted:      c.currentTime(),
		calls:             make(map[string]*callAccumulator),
		completedCalls:    make(map[string]Item),
		announcedCallIDs:  make(map[string]struct{}),
		streamedTextParts: make(map[streamedTextKey]*strings.Builder),
	}
	if err := stream.openWithRetry(nil); err != nil {
		cancel()
		return nil, err
	}
	return stream, nil
}

type azureWireRequest struct {
	Model              string             `json:"model"`
	Instructions       string             `json:"instructions,omitempty"`
	Input              []map[string]any   `json:"input"`
	Tools              []azureWireTool    `json:"tools,omitempty"`
	Reasoning          azureWireReasoning `json:"reasoning"`
	MaxOutputTokens    int                `json:"max_output_tokens,omitempty"`
	PreviousResponseID string             `json:"previous_response_id,omitempty"`
	ParallelToolCalls  *bool              `json:"parallel_tool_calls,omitempty"`
	Metadata           map[string]string  `json:"metadata,omitempty"`
	Include            []string           `json:"include,omitempty"`
	Store              bool               `json:"store"`
	Stream             bool               `json:"stream"`
}

type azureWireTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      bool            `json:"strict,omitempty"`
}

type azureWireReasoning struct {
	Effort string `json:"effort"`
}

func (c *AzureClient) projectRequest(request Request, effort string) (azureWireRequest, error) {
	input := make([]map[string]any, 0, len(request.Input))
	for _, item := range request.Input {
		projected, err := projectItem(item)
		if err != nil {
			return azureWireRequest{}, err
		}
		input = append(input, projected)
	}
	tools := make([]azureWireTool, 0, len(request.Tools))
	for _, tool := range request.Tools {
		tools = append(tools, azureWireTool{
			Type: "function", Name: tool.Name, Description: tool.Description,
			Parameters: append(json.RawMessage(nil), tool.Parameters...), Strict: tool.Strict,
		})
	}
	wire := azureWireRequest{
		Model:              c.deployment,
		Instructions:       request.Instructions,
		Input:              input,
		Tools:              tools,
		Reasoning:          azureWireReasoning{Effort: effort},
		MaxOutputTokens:    request.MaxOutputTokens,
		PreviousResponseID: request.PreviousResponseID,
		ParallelToolCalls:  request.ParallelToolCalls,
		Metadata:           cloneMetadata(request.Metadata),
		Store:              false,
		Stream:             true,
	}
	if effort != "none" {
		// With store=false, encrypted reasoning is the replay-safe form. The
		// engine may persist and replay this output item without server state.
		wire.Include = []string{"reasoning.encrypted_content"}
	}
	return wire, nil
}

func projectItem(item Item) (map[string]any, error) {
	projected := map[string]any{"type": string(item.Type)}
	if item.ID != "" {
		projected["id"] = item.ID
	}
	if item.Status != "" {
		projected["status"] = item.Status
	}
	if item.Phase != "" {
		projected["phase"] = item.Phase
	}
	switch item.Type {
	case ItemMessage:
		projected["role"] = string(item.Role)
		content := make([]map[string]string, 0, len(item.Content))
		for _, part := range item.Content {
			wirePart := map[string]string{"type": string(part.Type)}
			if part.Type == ContentRefusal {
				wirePart["refusal"] = part.Text
			} else {
				wirePart["text"] = part.Text
			}
			content = append(content, wirePart)
		}
		projected["content"] = content
	case ItemFunctionCall:
		projected["call_id"] = item.CallID
		projected["name"] = item.Name
		projected["arguments"] = item.Arguments
	case ItemFunctionCallOutput:
		projected["call_id"] = item.CallID
		projected["output"] = item.Output
	case ItemReasoning:
		if item.EncryptedContent != "" {
			projected["encrypted_content"] = item.EncryptedContent
		}
		// Azure's Responses input schema requires summary on every replayed
		// reasoning item, including encrypted items for which the provider did
		// not expose any summary text. Preserve that distinction as an empty
		// array instead of omitting the member.
		summary := make([]map[string]string, 0, len(item.Summary))
		for _, part := range item.Summary {
			summary = append(summary, map[string]string{"type": string(part.Type), "text": part.Text})
		}
		projected["summary"] = summary
	default:
		return nil, fmt.Errorf("%w: cannot project an unsupported item type", ErrProtocol)
	}
	return projected, nil
}

func cloneMetadata(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func (c *AzureClient) requestEndpoint() url.URL {
	endpoint := c.endpoint
	if c.apiVersion != "" {
		query := endpoint.Query()
		query.Set("api-version", c.apiVersion)
		endpoint.RawQuery = query.Encode()
	}
	return endpoint
}

func (c *AzureClient) requestHeaders() http.Header {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	header.Set("Accept", "text/event-stream")
	header.Set(azureAPIKeyHeader, c.apiKey)
	header.Set("User-Agent", c.userAgent)
	return header
}

func (c *AzureClient) newRequest(ctx context.Context, payload []byte) (*http.Request, context.CancelFunc, error) {
	attemptContext, cancel := context.WithTimeout(ctx, c.requestTimeout)
	endpoint := c.requestEndpoint()
	header := c.requestHeaders()
	if err := c.validateRequestMetadata(&endpoint, header, "create Azure response request"); err != nil {
		cancel()
		return nil, nil, err
	}
	request, err := http.NewRequestWithContext(attemptContext, http.MethodPost, endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		cancel()
		return nil, nil, c.requestCompositionError("create Azure response request")
	}
	request.Header = header
	if err := c.validateRequestMetadata(request.URL, request.Header, "create Azure response request"); err != nil {
		cancel()
		return nil, nil, err
	}
	reflected, err := c.credentialSanitizer().JSONContains(payload)
	if err != nil || reflected {
		cancel()
		return nil, nil, c.requestCompositionError("create Azure response request")
	}
	return request, cancel, nil
}

func (c *AzureClient) validateRequestMetadata(endpoint *url.URL, header http.Header, operation string) error {
	credentials := c.credentialSanitizer()
	if endpoint == nil || azureURLContainsCredential(credentials, endpoint) {
		return c.requestCompositionError(operation)
	}
	if c.apiKey == "" || !credentials.Covers(redact.New(c.apiKey)) || apiKeyContainsAnotherCredential(credentials, c.apiKey) {
		return c.requestCompositionError(operation)
	}

	nonAuth := make(http.Header)
	var apiKeyValues []string
	for name, values := range header {
		canonicalName := textproto.CanonicalMIMEHeaderKey(name)
		if strings.EqualFold(name, azureAPIKeyHeader) {
			if credentials.Contains(name) || credentials.Contains(canonicalName) {
				return c.requestCompositionError(operation)
			}
			apiKeyValues = append(apiKeyValues, values...)
			continue
		}
		if credentials.Contains(name) || credentials.Contains(canonicalName) {
			return c.requestCompositionError(operation)
		}
		copied := append([]string(nil), values...)
		nonAuth[canonicalName] = append(nonAuth[canonicalName], copied...)
		for _, value := range copied {
			trimmed := textproto.TrimString(value)
			if !validAzureHeaderValue(value) ||
				credentials.Contains(value) ||
				credentials.Contains(trimmed) ||
				credentials.Contains(canonicalName+": "+trimmed+"\r\n") {
				return c.requestCompositionError(operation)
			}
		}
	}
	if len(apiKeyValues) != 1 || apiKeyValues[0] != c.apiKey {
		return c.requestCompositionError(operation)
	}
	var physical bytes.Buffer
	if err := nonAuth.Write(&physical); err != nil || credentials.Contains(physical.String()) {
		return c.requestCompositionError(operation)
	}
	return nil
}

func azureURLContainsCredential(credentials *redact.Set, endpoint *url.URL) bool {
	values := []string{
		endpoint.String(),
		endpoint.RequestURI(),
		endpoint.Scheme,
		endpoint.Opaque,
		endpoint.Host,
		endpoint.Hostname(),
		endpoint.Port(),
		endpoint.Path,
		endpoint.RawPath,
		endpoint.EscapedPath(),
		endpoint.RawQuery,
		endpoint.Fragment,
		endpoint.RawFragment,
	}
	if endpoint.User != nil {
		values = append(values, endpoint.User.String())
	}
	for _, value := range values {
		if credentials.Contains(value) {
			return true
		}
	}
	query, err := url.ParseQuery(endpoint.RawQuery)
	if err != nil {
		return true
	}
	for name, values := range query {
		if credentials.Contains(name) {
			return true
		}
		for _, value := range values {
			if credentials.Contains(value) {
				return true
			}
		}
	}
	return false
}

// The complete set always contains the selected Azure key itself. Matching
// either proper one-byte-short view distinguishes another configured literal
// embedded anywhere in that key from a legitimate duplicate of the exact key.
func apiKeyContainsAnotherCredential(credentials *redact.Set, apiKey string) bool {
	if len(apiKey) < 2 {
		return false
	}
	return credentials.Contains(apiKey[:len(apiKey)-1]) || credentials.Contains(apiKey[1:])
}

func validAzureHeaderValue(value string) bool {
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character < ' ' && character != '\t' || character == 0x7f {
			return false
		}
	}
	return true
}

func (c *AzureClient) requestCompositionError(operation string) error {
	diagnostic := operation + ": invalid model protocol: Azure request metadata reflected configured credential material"
	safe, suppressed := c.credentialSanitizer().Redact(diagnostic)
	if suppressed || safe == "" {
		safe = c.credentialSanitizer().TerminalMarker()
	}
	if safe == "" {
		safe = "Azure request rejected"
	}
	return &azureRequestCompositionError{message: safe}
}

func (c *AzureClient) execute(ctx context.Context, payload []byte) (*http.Response, context.CancelFunc, error) {
	request, cancel, err := c.newRequest(ctx, payload)
	if err != nil {
		return nil, nil, err
	}
	response, err := awaitAzureRequest(request.Context(), c.httpClient, request)
	if err != nil {
		attemptErr := request.Context().Err()
		cancel()
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		if attemptErr == context.DeadlineExceeded || inspectModelError(err).deadline {
			return nil, nil, fmt.Errorf("%w after %s", ErrRequestTimeout, c.requestTimeout)
		}
		return nil, nil, fmt.Errorf("Azure response transport: %s", c.sanitizeProviderDiagnostic(safeModelErrorString(err)))
	}
	if response == nil {
		cancel()
		return nil, nil, errors.New("Azure response transport failed")
	}
	// Redirects are refused, so the adapter-owned request is the only valid
	// response provenance. Do not let an injected RoundTripper replace its
	// cancellation context with an unrelated or non-cancellable one.
	response.Request = request
	if attemptErr := request.Context().Err(); attemptErr != nil {
		closeProviderBody(response.Body)
		cancel()
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, nil, fmt.Errorf("%w after %s", ErrRequestTimeout, c.requestTimeout)
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		if contentType := response.Header.Get("Content-Type"); contentType != "" {
			if validationErr := c.validateOpaqueProviderValue("HTTP content type", contentType, maximumProviderIDBytes, true); validationErr != nil {
				closeProviderBody(response.Body)
				cancel()
				return nil, nil, validationErr
			}
			mediaType, _, parseErr := mime.ParseMediaType(contentType)
			if parseErr != nil || !strings.EqualFold(mediaType, "text/event-stream") {
				closeProviderBody(response.Body)
				cancel()
				return nil, nil, fmt.Errorf("%w: Azure streaming response has an invalid content type", ErrProtocol)
			}
		}
		return response, cancel, nil
	}
	providerError := c.decodeHTTPError(request.Context(), response)
	closeProviderBody(response.Body)
	cancel()
	if ctx.Err() != nil {
		return nil, nil, ctx.Err()
	}
	if inspectModelError(providerError).deadline {
		return nil, nil, fmt.Errorf("%w after %s", ErrRequestTimeout, c.requestTimeout)
	}
	return nil, nil, providerError
}

type azureRequestResult struct {
	response *http.Response
	err      error
}

// awaitAzureRequest contains an injected RoundTripper that ignores request
// cancellation. Normal net/http transports leave no goroutine behind because
// they honor request.Context. A hostile transport can strand only this one
// goroutine for each attempt admitted by the bounded retry coordinator.
func awaitAzureRequest(ctx context.Context, client *http.Client, request *http.Request) (*http.Response, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	results := make(chan azureRequestResult, 1)
	go func() {
		response, err := doAzureRequest(client, request)
		result := azureRequestResult{response: response, err: err}
		select {
		case results <- result:
		case <-ctx.Done():
			if response != nil {
				closeProviderBody(response.Body)
			}
		}
	}()
	select {
	case result := <-results:
		if result.err != nil && result.response != nil {
			closeProviderBody(result.response.Body)
		}
		return result.response, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type azureErrorBodyResult struct {
	body []byte
}

// decodeHTTPError gives one bounded-size reader ownership of the response
// body, but never joins that reader. An injected Body can ignore both request
// cancellation and concurrent Close; the attempt coordinator must still be
// able to return its exact caller-cancel or provider-timeout classification.
func (c *AzureClient) decodeHTTPError(ctx context.Context, response *http.Response) error {
	requestID := response.Header.Get("apim-request-id")
	if requestID == "" {
		requestID = response.Header.Get("x-request-id")
	}
	if err := c.validateOpaqueProviderValue("request id", requestID, maximumProviderIDBytes, false); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	results := make(chan azureErrorBodyResult, 1)
	go func() {
		var body []byte
		func() {
			defer func() {
				_ = recover()
			}()
			body, _ = io.ReadAll(io.LimitReader(response.Body, c.maximumErrorBytes+1))
		}()
		select {
		case results <- azureErrorBodyResult{body: body}:
		case <-ctx.Done():
		}
	}()
	var body []byte
	select {
	case result := <-results:
		body = result.body
	case <-ctx.Done():
		return ctx.Err()
	}
	var envelope struct {
		Error   azureError `json:"error"`
		Code    string     `json:"code"`
		Message string     `json:"message"`
		Param   string     `json:"param"`
		Type    string     `json:"type"`
	}
	_ = json.Unmarshal(body, &envelope)
	fields := envelope.Error
	if fields.Message == "" {
		fields = azureError{Code: envelope.Code, Message: envelope.Message, Param: envelope.Param, Type: envelope.Type}
	}
	if fields.Message == "" {
		fields.Message = http.StatusText(response.StatusCode)
	}
	fields, requestID = c.sanitizeProviderErrorFields(fields, requestID)
	retryable := retryableStatus(response.StatusCode)
	switch strings.ToLower(strings.TrimSpace(response.Header.Get("x-should-retry"))) {
	case "true":
		retryable = true
	case "false":
		retryable = false
	}
	now := c.currentTime()
	retryDelay, hasRetryDelay := parseRetryAfter(response.Header.Get("Retry-After"), now)
	return c.finalizeProviderError(&ProviderError{
		StatusCode: response.StatusCode,
		Code:       fields.Code,
		Type:       fields.Type,
		Param:      fields.Param,
		Message:    fields.Message,
		RequestID:  requestID,
		Retryable:  retryable,
		retryDelay: retryDelay, hasRetryDelay: hasRetryDelay,
	})
}

func (c *AzureClient) redact(value string) string {
	return c.credentialSanitizer().Apply(value)
}

func (c *AzureClient) credentialSanitizer() *redact.Set {
	if c == nil {
		return redact.New()
	}
	if c.credentialSet != nil {
		return c.credentialSet
	}
	return redact.New(c.apiKey)
}

func (c *AzureClient) sanitizeError(err error) error {
	if err == nil {
		return err
	}
	inspection := inspectModelError(err)
	message := c.sanitizeProviderDiagnostic(safeModelErrorString(err))
	// Preserve cancellation categories, which drive the no-retry rule, without
	// retaining an unsafe custom error string.
	switch {
	case inspection.cancelled:
		return context.Canceled
	case inspection.deadline:
		return context.DeadlineExceeded
	case inspection.closed:
		return ErrClosed
	case inspection.eof:
		return io.EOF
	case inspection.protocol:
		return fmt.Errorf("%w: %s", ErrProtocol, message)
	}
	if inspection.provider != nil {
		provider := inspection.provider
		fields, requestID := c.sanitizeProviderErrorFields(azureError{
			Code: provider.Code, Type: provider.Type,
			Param: provider.Param, Message: provider.Message,
		}, provider.RequestID)
		return c.finalizeProviderError(&ProviderError{
			StatusCode: provider.StatusCode,
			Code:       fields.Code,
			Type:       fields.Type,
			Param:      fields.Param,
			Message:    fields.Message,
			RequestID:  requestID,
			Retryable:  provider.Retryable,
			retryDelay: provider.retryDelay, hasRetryDelay: provider.hasRetryDelay,
		})
	}
	// Never return a caller-owned error merely because its first Error call was
	// safe. Stateful formatters can change on a later diagnostic invocation.
	return errors.New(message)
}

func retryableStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusConflict || status == http.StatusTooManyRequests || status >= 500
}

func retryableTransport(err error) bool {
	if err == nil {
		return false
	}
	inspection := inspectModelError(err)
	if inspection.cancelled || inspection.protocol {
		return false
	}
	if inspection.provider != nil {
		return inspection.provider.Retryable
	}
	// Errors returned by http.Client.Do are transport errors. Deadlines owned by
	// an attempt are retryable when the caller's parent context remains active.
	return true
}

func (c *AzureClient) retryDelay(failure error, retryNumber int) time.Duration {
	providerError := inspectModelError(failure).provider
	if providerError != nil {
		if providerError.hasRetryDelay {
			// A provider-directed delay supersedes the ordinary exponential
			// backoff cap. openWithRetry separately checks the total retry
			// window before sleeping, and Sleep remains context-cancellable.
			return providerError.retryDelay
		}
	}
	base := c.retryBase
	for i := 1; i < retryNumber && base < c.retryMaximum; i++ {
		if base > c.retryMaximum/2 {
			base = c.retryMaximum
			break
		}
		base *= 2
	}
	if base > c.retryMaximum {
		base = c.retryMaximum
	}
	maximumJitter := base / 4
	jitter := c.retryJitter(maximumJitter)
	if jitter < 0 {
		jitter = 0
	}
	if jitter > maximumJitter {
		jitter = maximumJitter
	}
	delay := base + jitter
	if delay > c.retryMaximum {
		return c.retryMaximum
	}
	return delay
}

func parseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	const maximumDurationSeconds = int64(^uint64(0)>>1) / int64(time.Second)
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 && seconds <= maximumDurationSeconds {
		return time.Duration(seconds) * time.Second, true
	}
	when, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	delay := when.Sub(now)
	if delay < 0 {
		delay = 0
	}
	return delay, true
}

func randomJitter(maximum time.Duration) time.Duration {
	if maximum <= 0 {
		return 0
	}
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return 0
	}
	return time.Duration(binary.LittleEndian.Uint64(raw[:]) % (uint64(maximum) + 1))
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// RetryExhaustedError reports the final safe cause after the configured retry
// ceiling. Attempts includes the initial request.
type RetryExhaustedError struct {
	Attempts    int
	Last        error
	RetryWindow time.Duration

	display    string
	displaySet bool
}

func (e *RetryExhaustedError) Error() string {
	if e == nil {
		return "model request retries exhausted"
	}
	if e.displaySet {
		return e.display
	}
	return e.composeError()
}

func (e *RetryExhaustedError) composeError() string {
	last := "provider operation failed"
	if e.Last != nil {
		last = safeModelErrorString(e.Last)
	}
	if e.RetryWindow > 0 {
		return fmt.Sprintf("model request retry window %s exhausted after %d attempts: %s", e.RetryWindow, e.Attempts, last)
	}
	return fmt.Sprintf("model request failed after %d attempts: %s", e.Attempts, last)
}

func (e *RetryExhaustedError) Unwrap() error  { return e.Last }
func (e *RetryExhaustedError) String() string { return e.Error() }
func (e *RetryExhaustedError) GoString() string {
	return e.Error()
}
func (e *RetryExhaustedError) Format(state fmt.State, verb rune) {
	switch verb {
	case 'q':
		_, _ = fmt.Fprintf(state, "%q", e.Error())
	default:
		_, _ = fmt.Fprint(state, e.Error())
	}
}

func (c *AzureClient) retryExhaustedError(attempts int, last error, retryWindow time.Duration) *RetryExhaustedError {
	failure := &RetryExhaustedError{Attempts: attempts, Last: last, RetryWindow: retryWindow}
	failure.display = c.redact(failure.composeError())
	failure.displaySet = true
	return failure
}

func (c *AzureClient) retryInfo(attempt, maximum int, delay time.Duration, failure error) RetryInfo {
	observationError := failure
	providerError := inspectModelError(failure).provider
	if providerError != nil {
		copy := *providerError
		// Retry-After has already been reduced to Delay and remains internal to
		// scheduling. Give observers a detached structured error so callbacks
		// cannot mutate the active retry classification or delay.
		copy.retryDelay = 0
		copy.hasRetryDelay = false
		observationError = &copy
	} else if failure != nil {
		observationError = errors.New(c.redact(safeModelErrorString(failure)))
	}
	info := RetryInfo{
		Attempt: attempt, MaxAttempts: maximum, Delay: delay,
		Error: observationError, RequestID: requestIDFromError(failure),
	}
	info.display = c.redact(info.composeString())
	info.displaySet = true
	return info
}

func (c *AzureClient) currentTime() (now time.Time) {
	now = time.Now()
	if c == nil || c.now == nil {
		return now
	}
	defer func() {
		if recover() != nil {
			now = time.Now()
		}
	}()
	return c.now()
}

func (c *AzureClient) retryJitter(maximum time.Duration) (jitter time.Duration) {
	if c == nil || c.jitter == nil {
		return 0
	}
	defer func() {
		if recover() != nil {
			jitter = 0
		}
	}()
	return c.jitter(maximum)
}

func (c *AzureClient) sleepBeforeRetry(ctx context.Context, delay time.Duration) (err error) {
	if c == nil || c.sleep == nil {
		return errors.New("retry sleep callback is unavailable")
	}
	defer func() {
		if recover() != nil {
			err = errors.New("retry sleep callback panicked")
		}
	}()
	return c.sleep(ctx, delay)
}

func (c *AzureClient) notifyRetry(info RetryInfo) {
	if c == nil || c.onRetry == nil {
		return
	}
	defer func() {
		_ = recover()
	}()
	c.onRetry(info)
}
