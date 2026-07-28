package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/greenpau/agentx/pkg/attachment"
	"github.com/greenpau/agentx/pkg/cli"
	"github.com/greenpau/agentx/pkg/command"
	"github.com/greenpau/agentx/pkg/config"
	"github.com/greenpau/agentx/pkg/engine"
	"github.com/greenpau/agentx/pkg/protocol"
	"github.com/greenpau/agentx/pkg/surface"
)

const (
	maximumQueuedInputs     = 128
	maximumQueuedInputBytes = 16 << 20
)

var (
	errStructuredQueueFull       = errors.New("structured input queue capacity exceeded")
	errLastStructuredResultError = errors.New("structured input closed after an unsuccessful result")
	errStructuredInputOwnership  = errors.New("duplex structured input must be close-interruptible")
	errStructuredInputRead       = errors.New("structured input reader failed")
	errStructuredInputWorker     = errors.New("structured input worker failed")
)

type inputQueue struct {
	mu            sync.Mutex
	items         []queuedInput
	prompts       map[string]struct{}
	bytes         int
	closed        bool
	err           error
	lastRejection error
	notify        chan struct{}
}

type queuedInput struct {
	envelope surface.InputEnvelope
	message  protocol.Message
	bytes    int
}

func newInputQueue() *inputQueue {
	return &inputQueue{notify: make(chan struct{}, 1), prompts: make(map[string]struct{})}
}
func (q *inputQueue) push(item surface.InputEnvelope) error {
	return q.pushMessage(item, protocol.Message{})
}

func (q *inputQueue) pushMessage(item surface.InputEnvelope, message protocol.Message) error {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return surface.ErrClosed
	}
	if item.UUID != "" {
		if _, duplicate := q.prompts[item.UUID]; duplicate {
			q.mu.Unlock()
			return engine.ErrDuplicatePrompt
		}
	}
	size := inputEnvelopeSize(item)
	if len(q.items) >= maximumQueuedInputs || size > maximumQueuedInputBytes || q.bytes > maximumQueuedInputBytes-size {
		q.mu.Unlock()
		return errStructuredQueueFull
	}
	// Keep one stable priority queue. Inserting after every item of equal or
	// higher precedence preserves FIFO within now, next, and later while still
	// allowing a later-arriving urgent workload to overtake deferred work.
	rank := inputPriorityRank(item.Priority)
	insert := len(q.items)
	for index, queued := range q.items {
		if inputPriorityRank(queued.envelope.Priority) > rank {
			insert = index
			break
		}
	}
	q.items = append(q.items, queuedInput{})
	copy(q.items[insert+1:], q.items[insert:])
	q.items[insert] = queuedInput{envelope: item, message: message, bytes: size}
	if item.UUID != "" {
		q.prompts[item.UUID] = struct{}{}
	}
	q.bytes += size
	q.mu.Unlock()
	q.signal()
	return nil
}

func inputPriorityRank(priority string) int {
	switch priority {
	case "now":
		return 0
	case "later":
		return 2
	case "", "next":
		return 1
	default:
		// The structured reader rejects unknown priorities before admission.
		// Treat direct internal callers conservatively as ordinary next work.
		return 1
	}
}
func (q *inputQueue) close(err error) {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.closed = true
	q.err = err
	// EOF drains already accepted records. A fatal framing, schema, or output
	// error invalidates the protocol, so queued semantic work is discarded and
	// must not produce side effects after the failure was observed.
	if err != nil {
		q.items = nil
		q.prompts = make(map[string]struct{})
		q.bytes = 0
	}
	q.mu.Unlock()
	q.signal()
}
func (q *inputQueue) signal() {
	select {
	case q.notify <- struct{}{}:
	default:
	}
}
func (q *inputQueue) next(ctx context.Context) (surface.InputEnvelope, error) {
	queued, err := q.nextMessage(ctx)
	return queued.envelope, err
}

func (q *inputQueue) nextMessage(ctx context.Context) (queuedInput, error) {
	for {
		q.mu.Lock()
		if len(q.items) > 0 {
			queued := q.items[0]
			q.items = q.items[1:]
			q.bytes -= queued.bytes
			q.mu.Unlock()
			return queued, nil
		}
		closed, err := q.closed, q.err
		q.mu.Unlock()
		if closed {
			if err != nil {
				return queuedInput{}, err
			}
			return queuedInput{}, io.EOF
		}
		select {
		case <-ctx.Done():
			return queuedInput{}, ctx.Err()
		case <-q.notify:
		}
	}
}

func (q *inputQueue) releasePrompt(promptID string) {
	if promptID == "" {
		return
	}
	q.mu.Lock()
	delete(q.prompts, promptID)
	q.mu.Unlock()
}

func (q *inputQueue) reject(err error) {
	if err == nil {
		return
	}
	q.mu.Lock()
	q.lastRejection = err
	q.mu.Unlock()
}

func (q *inputQueue) rejection() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.lastRejection
}

func inputEnvelopeSize(item surface.InputEnvelope) int {
	payload := 0
	addInputEnvelopeBytes(&payload, len(item.Type))
	addInputEnvelopeBytes(&payload, len(item.UUID))
	addInputEnvelopeBytes(&payload, len(item.SessionID))
	addInputEnvelopeBytes(&payload, len(item.Message))
	addInputEnvelopeBytes(&payload, len(item.ToolUseResult))
	addInputEnvelopeBytes(&payload, len(item.Priority))
	addInputEnvelopeBytes(&payload, len(item.Timestamp))
	addInputEnvelopeBytes(&payload, len(item.RequestID))
	if item.ParentToolUseID != nil {
		addInputEnvelopeBytes(&payload, len(*item.ParentToolUseID))
	}
	if item.Request != nil {
		addInputEnvelopeBytes(&payload, len(item.Request.Subtype))
		addInputEnvelopeBytes(&payload, len(item.Request.Data))
	}
	if item.Response != nil {
		addInputEnvelopeBytes(&payload, len(item.Response.Subtype))
		addInputEnvelopeBytes(&payload, len(item.Response.RequestID))
		addInputEnvelopeBytes(&payload, len(item.Response.Response))
		addInputEnvelopeBytes(&payload, len(item.Response.Error))
		for _, pending := range item.Response.PendingPermissionRequests {
			addInputEnvelopeBytes(&payload, 64+len(pending.Type)+len(pending.RequestID))
			if pending.Request != nil {
				addInputEnvelopeBytes(&payload, len(pending.Request.Subtype))
				addInputEnvelopeBytes(&payload, len(pending.Request.Data))
			}
		}
	}
	for key, value := range item.EnvironmentVariables {
		addInputEnvelopeBytes(&payload, 32+len(key)+len(value))
	}
	if original := item.OriginalByteSize(); original > payload {
		payload = original
	}
	addInputEnvelopeBytes(&payload, 256)
	return payload
}

func addInputEnvelopeBytes(total *int, amount int) {
	if amount <= 0 || *total == int(^uint(0)>>1) {
		return
	}
	maximum := int(^uint(0) >> 1)
	if amount > maximum-*total {
		*total = maximum
		return
	}
	*total += amount
}

type activeTurn struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	fatal  bool
}

type structuredInputReader struct {
	reader *io.PipeReader
	stop   func()
}

type structuredReadResult struct {
	data   []byte
	eof    bool
	failed bool
}

// structuredInputSource rejects arbitrary non-closable readers before a
// duplex stream is initialized. There is no general way to cancel a blocked
// io.Reader; accepted ReadClosers own the contract that Close makes an active
// Read return. Known in-memory readers are finite and can be safely adapted.
func structuredInputSource(source io.Reader) (io.ReadCloser, error) {
	if source == nil {
		return nil, errStructuredInputOwnership
	}
	if closer, ok := source.(io.ReadCloser); ok {
		return closer, nil
	}
	switch source.(type) {
	case *bytes.Buffer, *bytes.Reader, *strings.Reader:
		return io.NopCloser(source), nil
	default:
		return nil, errStructuredInputOwnership
	}
}

// newStructuredInputReader isolates callback-owned Read and Close methods from
// the protocol pump. Cancellation always closes the owned pipe and joins that
// pump even if a broken source ignores Close or either callback panics. A
// source that violates the ReadCloser interruption contract may leave only its
// isolated callback goroutine behind; it cannot retain session state or keep
// structured shutdown open.
func newStructuredInputReader(ctx context.Context, source io.ReadCloser) structuredInputReader {
	reader, writer := io.Pipe()
	pumpCtx, cancel := context.WithCancel(ctx)
	reads := make(chan structuredReadResult)
	pumpDone := make(chan struct{})
	var once sync.Once
	go readStructuredSource(pumpCtx.Done(), source, reads)
	go func() {
		defer close(pumpDone)
		defer func() {
			if recover() != nil {
				_ = writer.CloseWithError(errStructuredInputWorker)
			}
		}()
		for {
			select {
			case <-pumpCtx.Done():
				_ = writer.CloseWithError(pumpCtx.Err())
				return
			case result, ok := <-reads:
				if !ok {
					_ = writer.Close()
					return
				}
				if len(result.data) > 0 {
					if _, err := writer.Write(result.data); err != nil {
						return
					}
				}
				switch {
				case result.failed:
					_ = writer.CloseWithError(errStructuredInputRead)
					return
				case result.eof:
					_ = writer.Close()
					return
				}
			}
		}
	}()
	stop := func() {
		once.Do(func() {
			cancel()
			_ = reader.CloseWithError(context.Canceled)
			_ = writer.CloseWithError(context.Canceled)
			// Close is callback-owned and may panic or block despite the
			// ReadCloser contract. It therefore cannot sit on the protocol's
			// join path.
			go closeStructuredSource(source)
		})
		<-pumpDone
	}
	return structuredInputReader{reader: reader, stop: stop}
}

func readStructuredSource(stop <-chan struct{}, source io.Reader, output chan<- structuredReadResult) {
	defer close(output)
	buffer := make([]byte, 32<<10)
	for {
		select {
		case <-stop:
			return
		default:
		}
		count, eof, failed := readStructuredSourceOnce(source, buffer)
		result := structuredReadResult{eof: eof, failed: failed}
		if count > 0 && !failed {
			// Copy before handing data to the concurrent pipe writer. This
			// keeps one bounded source buffer without reusing bytes that the
			// pump still owns.
			result.data = append([]byte(nil), buffer[:count]...)
		}
		if count == 0 && !eof && !failed {
			runtime.Gosched()
			continue
		}
		select {
		case output <- result:
		case <-stop:
			return
		}
		if eof || failed {
			return
		}
	}
}

// readStructuredSourceOnce never formats, unwraps, or classifies a
// callback-owned error. Only exact io.EOF has protocol meaning; every other
// return error and every panic becomes the same value-opaque failure.
func readStructuredSourceOnce(source io.Reader, buffer []byte) (count int, eof, failed bool) {
	defer func() {
		if recover() != nil {
			count, eof, failed = 0, false, true
		}
	}()
	count, err := source.Read(buffer)
	if count < 0 || count > len(buffer) {
		return 0, false, true
	}
	if err == nil {
		return count, false, false
	}
	if err == io.EOF {
		return count, true, false
	}
	return count, false, true
}

func closeStructuredSource(source io.Closer) {
	defer func() {
		_ = recover()
	}()
	_ = source.Close()
}

// set returns false when the input/output protocol has already failed. The
// fatal latch closes the dequeue-to-active race: a failure observed after a
// record is popped but before its turn context is installed still prevents the
// future turn from starting.
func (a *activeTurn) set(cancel context.CancelFunc) bool {
	a.mu.Lock()
	if a.fatal {
		a.mu.Unlock()
		cancel()
		return false
	}
	a.cancel = cancel
	a.mu.Unlock()
	return true
}
func (a *activeTurn) clear() { a.mu.Lock(); a.cancel = nil; a.mu.Unlock() }
func (a *activeTurn) interrupt() bool {
	a.mu.Lock()
	cancel := a.cancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
		return true
	}
	return false
}
func (a *activeTurn) abort() {
	a.mu.Lock()
	a.fatal = true
	cancel := a.cancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func runStructured(ctx context.Context, opts cli.Options, workspace string, stdin io.Reader, stdout, stderr io.Writer) (returnErr error) {
	inputSource, err := structuredInputSource(stdin)
	if err != nil {
		return err
	}
	encoder := surface.NewEncoder(stdout)
	broker := surface.NewControlBroker()
	defer broker.Close()
	sink := &streamSink{encoder: encoder, includePartial: opts.IncludePartial, replayUserMessages: opts.ReplayUserMessages}
	interactions := &structuredInteractions{broker: broker, encoder: encoder}
	session, err := buildSession(ctx, buildOptions{CLI: opts, Workspace: workspace, Sink: sink, Approver: interactions.Approve, Ask: interactions.Ask, Stderr: stderr})
	if err != nil {
		return err
	}
	defer func() {
		returnErr = redactOperationalError(errors.Join(returnErr, session.Close()), session.sanitize)
	}()
	if err := encoder.SetValidator(credentialJSONValidator(session.credentials)); err != nil {
		return err
	}
	sink.model = session.config.Azure.ModelName
	interactions.sessionID = string(session.engine.SessionID())
	if err := encodeSDKInit(encoder, session, opts); err != nil {
		return err
	}
	queue := newInputQueue()
	active := &activeTurn{}
	if strings.TrimSpace(opts.Prompt) != "" || len(opts.Attachments) > 0 {
		var (
			envelopeMessage json.RawMessage
			message         protocol.Message
			promptID        string
		)
		if len(opts.Attachments) == 0 {
			envelopeMessage, _ = json.Marshal(opts.Prompt)
		} else {
			if strings.HasPrefix(strings.TrimSpace(opts.Prompt), "/") {
				return &cli.UsageError{Message: "attachments cannot accompany a slash or local command"}
			}
			promptID, err = surface.NewUUID()
			if err != nil {
				return err
			}
			message, err = session.importInitialAttachments(ctx, opts.Prompt, opts.Attachments, promptID)
			if err != nil {
				resultErr := encodeRejectedStructuredUser(encoder, session, promptID, err)
				return errors.Join(err, resultErr)
			}
		}
		if err := queue.pushMessage(surface.InputEnvelope{
			Type: "user", Message: envelopeMessage, UUID: promptID,
		}, message); err != nil {
			return err
		}
	}
	readerCtx, stopReader := context.WithCancel(ctx)
	input := newStructuredInputReader(readerCtx, inputSource)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		readStructuredInput(readerCtx, input.reader, stderr, encoder, broker, queue, active, session, opts.ReplayUserMessages)
	}()
	expiryCtx, stopExpiry := context.WithCancel(readerCtx)
	expiryDone := make(chan struct{})
	go func() {
		defer close(expiryDone)
		session.monitorUploadExpiry(expiryCtx, encoder, func(err error) {
			active.abort()
			broker.Close()
			queue.close(err)
		})
	}()
	defer func() {
		stopReader()
		input.stop()
		<-readerDone
		stopExpiry()
		<-expiryDone
	}()
	lastResultFailed := false
	sawUserInput := false
	for {
		queued, nextErr := queue.nextMessage(ctx)
		if nextErr == io.EOF {
			if lastResultFailed {
				return errLastStructuredResultError
			}
			if rejected := queue.rejection(); rejected != nil {
				return rejected
			}
			if !sawUserInput {
				return encodeStructuredInputFailure(
					encoder,
					session,
					errors.New("structured input closed before receiving a user request"),
				)
			}
			return nil
		}
		if nextErr != nil {
			return encodeStructuredInputFailure(encoder, session, nextErr)
		}
		envelope := queued.envelope
		message := queued.message
		sawUserInput = true
		if session.engine.HasPromptID(envelope.UUID) {
			queue.releasePrompt(envelope.UUID)
			if err := encodeDuplicate(encoder, session, envelope, opts.ReplayUserMessages); err != nil {
				return err
			}
			continue
		}
		if len(message.Content) == 0 {
			decoded, decodeErr := surface.DecodeUserMessage(envelope.Message)
			if decodeErr != nil {
				queue.releasePrompt(envelope.UUID)
				return fmt.Errorf("invalid structured user message: %w", decodeErr)
			}
			message, decodeErr = session.admitUserMessage(ctx, decoded, envelope.UUID)
			if decodeErr != nil {
				queue.releasePrompt(envelope.UUID)
				return fmt.Errorf("invalid structured user message: %w", decodeErr)
			}
		}
		text := userMessageText(message)
		turnCtx, cancel := context.WithCancel(ctx)
		if !active.set(cancel) {
			queue.releasePrompt(envelope.UUID)
			continue
		}
		if err := encodeSessionState(encoder, session.engine.SessionID(), "running"); err != nil {
			cancel()
			active.clear()
			return err
		}
		commandStarted := time.Now()
		commandResult, isCommand, commandErr := session.dispatchUserCommand(turnCtx, text, true)
		if isCommand && commandErr == nil && commandResult.Kind == command.ResultPrompt {
			text = commandResult.Prompt
			if strings.TrimSpace(text) == "" {
				commandErr = errors.New("prompt command produced empty model input")
			} else {
				message.Content = []protocol.ContentBlock{protocol.TextBlock(text)}
			}
		}
		if isCommand && (commandErr != nil || commandResult.Kind != command.ResultPrompt) {
			outcome := session.localCommandOutcome(commandResult, commandErr, commandStarted)
			commandOutput := commandResult.Output
			if commandErr != nil {
				commandOutput = commandErr.Error()
			}
			replayErr := encodeStructuredCommandInput(encoder, session, envelope, opts.ReplayUserMessages)
			outputErr := encodeSDKLocalCommandOutput(encoder, session.engine.SessionID(), session.sanitize(commandOutput))
			resultErr := encodeSDKPromptResult(encoder, outcome, commandErr, envelope.UUID)
			idleErr := encodeSessionState(encoder, session.engine.SessionID(), "idle")
			cancel()
			active.clear()
			queue.releasePrompt(envelope.UUID)
			if err := errors.Join(replayErr, outputErr, resultErr, idleErr); err != nil {
				return errors.Join(commandErr, err)
			}
			lastResultFailed = commandErr != nil
			continue
		}
		outcome, runErr := session.submitMessage(turnCtx, message, envelope.UUID)
		cancel()
		active.clear()
		queue.releasePrompt(envelope.UUID)
		if outcome.SessionID == "" {
			outcome.SessionID = session.engine.SessionID()
		}
		if errors.Is(runErr, engine.ErrDuplicatePrompt) {
			duplicateErr := encodeDuplicate(encoder, session, envelope, opts.ReplayUserMessages)
			idleErr := encodeSessionState(encoder, session.engine.SessionID(), "idle")
			if err := errors.Join(duplicateErr, idleErr); err != nil {
				return err
			}
			continue
		}
		resultErr := encodeSDKPromptResult(encoder, outcome, runErr, envelope.UUID)
		idleErr := encodeSessionState(encoder, session.engine.SessionID(), "idle")
		if err := errors.Join(resultErr, idleErr); err != nil {
			return errors.Join(runErr, err)
		}
		if runErr != nil {
			if errors.Is(runErr, context.Canceled) && ctx.Err() == nil {
				lastResultFailed = true
				continue
			}
			return runErr
		}
		lastResultFailed = false
	}
}

func encodeStructuredInputFailure(encoder *surface.Encoder, session *runtimeSession, cause error) error {
	if cause == nil {
		cause = errors.New("structured input failed")
	}
	publicErr := redactOperationalError(cause, session.sanitize)
	outcome := engine.Outcome{
		SessionID:  session.engine.SessionID(),
		Status:     protocol.TurnResultError,
		StopReason: "input_error",
		Usage:      protocol.Usage{Model: session.config.Azure.ModelName},
	}
	if err := encodeSDKResult(encoder, outcome, publicErr); err != nil {
		return errors.Join(publicErr, err)
	}
	return publicErr
}

func encodeRejectedStructuredUser(
	encoder *surface.Encoder,
	session *runtimeSession,
	promptID string,
	cause error,
) error {
	if cause == nil {
		cause = errors.New("user input was rejected")
	}
	outcome := engine.Outcome{
		SessionID:  session.engine.SessionID(),
		Status:     protocol.TurnResultError,
		StopReason: "input_error",
		Usage:      protocol.Usage{Model: session.config.Azure.ModelName},
	}
	record, err := sdkResultRecord(outcome, redactOperationalError(cause, session.sanitize))
	if err != nil {
		return err
	}
	if promptID != "" {
		record["prompt_uuid"] = promptID
	}
	return encoder.Encode(record)
}

func (r *runtimeSession) admitUserMessage(
	ctx context.Context,
	input surface.UserMessage,
	promptID string,
) (protocol.Message, error) {
	if err := input.Validate(); err != nil {
		return protocol.Message{}, err
	}
	if err := protocol.ValidatePromptID(promptID); err != nil {
		return protocol.Message{}, err
	}
	if err := r.validatePromptAttachmentSet(input, promptID); err != nil {
		return protocol.Message{}, err
	}
	if input.HasAttachments() {
		if err := surface.ValidateUUID(promptID); err != nil {
			return protocol.Message{}, errors.New("attachment-bearing user input requires a canonical UUID")
		}
		if strings.HasPrefix(strings.TrimSpace(input.Text()), "/") {
			return protocol.Message{}, errors.New("attachments cannot accompany a slash or local command")
		}
		if r.attachments == nil || !r.inputMediaEnabled {
			return protocol.Message{}, errors.New("attachment input is unavailable for this session")
		}
		ids := input.AttachmentIDs()
		resolved, err := r.attachments.ResolveMany(ctx, ids)
		if err != nil {
			return protocol.Message{}, errors.New("one or more attachment references are missing or tampered")
		}
		defer func() {
			for index := range resolved {
				clear(resolved[index].Bytes)
			}
		}()
		byID := make(map[attachment.ID]attachment.Manifest, len(resolved))
		for _, item := range resolved {
			byID[item.Manifest.AttachmentID] = item.Manifest
		}
		for _, block := range input.Content {
			if block.Type == surface.UserContentAttachment &&
				(block.Attachment == nil || byID[block.Attachment.AttachmentID] != *block.Attachment) {
				return protocol.Message{}, errors.New("attachment reference metadata does not match committed content")
			}
		}
	}

	message := protocol.Message{Role: protocol.RoleUser}
	for _, block := range input.Content {
		switch block.Type {
		case surface.UserContentText:
			message.Content = append(message.Content, protocol.TextBlock(block.Text))
		case surface.UserContentAttachment:
			message.Content = append(message.Content, protocol.AttachmentBlock(*block.Attachment))
		}
	}
	if err := message.Validate(); err != nil {
		return protocol.Message{}, err
	}
	return message, nil
}

func (r *runtimeSession) validatePromptAttachmentSet(
	input surface.UserMessage,
	promptID string,
) error {
	ids := input.AttachmentIDs()
	referenced := make(map[attachment.ID]struct{}, len(ids))
	for _, id := range ids {
		referenced[id] = struct{}{}
	}
	r.uploadMu.Lock()
	defer r.uploadMu.Unlock()
	for _, owner := range r.uploadPrompts {
		if owner == promptID {
			return errors.New("attachment prompt still has an incomplete upload")
		}
	}
	for _, id := range ids {
		if owner := r.attachmentPrompts[id]; owner != "" && owner != promptID {
			return errors.New("attachment reference belongs to a different prompt UUID")
		}
	}
	// Every successfully committed import correlated to this prompt is part of
	// one atomic submitted set. Omitting it would silently drop an import while
	// still consuming the prompt UUID.
	for id, size := range r.attachmentReserved {
		if size <= 0 || r.attachmentPrompts[id] != promptID {
			continue
		}
		if _, present := referenced[id]; !present {
			return errors.New("attachment prompt omits a committed attachment")
		}
	}
	return nil
}

func (r *runtimeSession) handleAttachmentImport(
	ctx context.Context,
	encoder *surface.Encoder,
	record surface.AttachmentImport,
) error {
	if r.attachments == nil || !r.inputMediaEnabled {
		return encodeAttachmentImportRejected(encoder, record.UploadID, "attachments_unavailable")
	}
	switch record.Operation {
	case surface.AttachmentImportBegin:
		if r.engine.HasPromptID(record.PromptUUID) {
			return encodeAttachmentImportRejected(encoder, record.UploadID, "prompt_already_accepted")
		}
		r.uploadMu.Lock()
		r.ensureUploadLedgerLocked()
		_, uploadExists := r.uploadPrompts[record.UploadID]
		_, terminalSent := r.uploadTerminalSent[record.UploadID]
		if uploadExists || terminalSent {
			r.uploadMu.Unlock()
			return encodeAttachmentImportRejected(encoder, record.UploadID, "duplicate_upload")
		}
		limits := r.attachments.Limits()
		// Every active application-level upload reserves the terminal-ledger
		// entry needed to settle it exactly once. Reject overflow before
		// admission so a stream of otherwise valid begin records cannot grow
		// uploadTerminalSent beyond the advertised session ceiling.
		if len(r.uploadTerminalSent)+len(r.uploadPrompts) >= limits.MaxUploadsPerSession {
			r.uploadMu.Unlock()
			return encodeAttachmentImportRejected(encoder, record.UploadID, "resource_limit")
		}
		if r.promptCounts[record.PromptUUID] >= limits.MaxAttachmentsPerMessage ||
			record.SizeBytes > limits.MaxAggregateBytes-r.promptBytes[record.PromptUUID] {
			// Retain only enough correlation to emit this upload ID's sole
			// terminal failure. Absence from uploadReserved ensures this
			// unadmitted begin cannot alter the prompt's existing reservation.
			r.uploadPrompts[record.UploadID] = record.PromptUUID
			r.uploadMu.Unlock()
			return r.encodeTerminalImportFailure(
				encoder, record.UploadID, record.AttachmentID, "resource_limit",
			)
		}
		r.uploadPrompts[record.UploadID] = record.PromptUUID
		r.uploadReserved[record.UploadID] = record.SizeBytes
		r.promptCounts[record.PromptUUID]++
		r.promptBytes[record.PromptUUID] += record.SizeBytes
		r.uploadMu.Unlock()
		request, _ := record.BeginUpload()
		ack, err := r.attachments.Begin(ctx, request)
		if err != nil {
			return r.encodeTerminalImportFailure(encoder, record.UploadID, record.AttachmentID, uploadErrorCode(err))
		}
		return r.encodeAttachmentAcknowledgement(encoder, ack)
	case surface.AttachmentImportChunk:
		if err := r.attachments.Chunk(ctx, record.UploadID, uint64(record.Sequence), record.Data); err != nil {
			ack, abortErr := r.attachments.Abort(ctx, record.UploadID)
			if abortErr == nil {
				ack.Reason = uploadErrorCode(err)
				return r.encodeAttachmentAcknowledgement(encoder, ack)
			}
			if outcome, exists := r.attachments.UploadOutcome(record.UploadID); exists {
				outcome.Reason = uploadErrorCode(err)
				return r.encodeAttachmentAcknowledgement(encoder, outcome)
			}
			return encodeAttachmentImportRejected(encoder, record.UploadID, uploadErrorCode(err))
		}
		return nil
	case surface.AttachmentImportCommit:
		ack, err := r.attachments.Commit(ctx, record.UploadID)
		if err != nil && !ack.Terminal {
			if outcome, exists := r.attachments.UploadOutcome(record.UploadID); exists {
				return r.encodeAttachmentAcknowledgement(encoder, outcome)
			}
			return encodeAttachmentImportRejected(encoder, record.UploadID, uploadErrorCode(err))
		}
		if ack.Terminal {
			return r.encodeAttachmentAcknowledgement(encoder, ack)
		}
		return nil
	case surface.AttachmentImportAbort:
		ack, err := r.attachments.Abort(ctx, record.UploadID)
		if err != nil {
			if outcome, exists := r.attachments.UploadOutcome(record.UploadID); exists {
				return r.encodeAttachmentAcknowledgement(encoder, outcome)
			}
			return encodeAttachmentImportRejected(encoder, record.UploadID, uploadErrorCode(err))
		}
		return r.encodeAttachmentAcknowledgement(encoder, ack)
	default:
		return encodeAttachmentImportRejected(encoder, record.UploadID, "unsupported_operation")
	}
}

func (r *runtimeSession) encodeTerminalImportFailure(
	encoder *surface.Encoder,
	uploadID attachment.UploadID,
	attachmentID attachment.ID,
	code string,
) error {
	return r.encodeAttachmentAcknowledgement(encoder, attachment.UploadAcknowledgement{
		UploadID: uploadID, AttachmentID: attachmentID,
		Status: attachment.UploadFailed, Terminal: true, Reason: code,
	})
}

func (r *runtimeSession) encodeAttachmentAcknowledgement(
	encoder *surface.Encoder,
	ack attachment.UploadAcknowledgement,
) error {
	var discard *attachment.Manifest
	r.uploadMu.Lock()
	r.ensureUploadLedgerLocked()
	if ack.Terminal && r.uploadTerminalSent[ack.UploadID] {
		r.uploadMu.Unlock()
		return encodeAttachmentImportRejected(encoder, ack.UploadID, "upload_already_terminal")
	}
	promptID := r.uploadPrompts[ack.UploadID]
	if ack.Terminal {
		r.uploadTerminalSent[ack.UploadID] = true
		reserved, tracked := r.uploadReserved[ack.UploadID]
		delete(r.uploadPrompts, ack.UploadID)
		delete(r.uploadReserved, ack.UploadID)
		if ack.Status == attachment.UploadCommitted && ack.Manifest != nil {
			base := r.promptBytes[promptID] - reserved
			if base < 0 {
				base = 0
			}
			limit := r.attachments.Limits().MaxAggregateBytes
			if ack.Manifest.SizeBytes > limit-base {
				manifest := *ack.Manifest
				discard = &manifest
				ack.Status = attachment.UploadFailed
				ack.Manifest = nil
				ack.Reason = "resource_limit"
				r.releasePromptReservationLocked(promptID, reserved, tracked)
			} else {
				r.promptBytes[promptID] = base + ack.Manifest.SizeBytes
				r.attachmentPrompts[ack.Manifest.AttachmentID] = promptID
				r.attachmentReserved[ack.Manifest.AttachmentID] = ack.Manifest.SizeBytes
			}
		} else {
			r.releasePromptReservationLocked(promptID, reserved, tracked)
		}
	}
	r.uploadMu.Unlock()
	var discardErr error
	if discard != nil {
		discardCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		discardErr = r.attachments.DiscardUnreferenced(discardCtx, discard.AttachmentID)
		cancel()
	}
	record := map[string]any{
		"type": "attachment_import_result", "version": attachment.ProtocolVersion,
		"upload_id": ack.UploadID, "attachment_id": ack.AttachmentID,
		"status": ack.Status, "terminal": ack.Terminal,
	}
	if promptID != "" {
		record["prompt_uuid"] = promptID
	}
	if ack.Manifest != nil {
		record["attachment"] = ack.Manifest
	}
	if ack.Reason != "" {
		record["reason"] = ack.Reason
	}
	return errors.Join(discardErr, encoder.Encode(record))
}

func (r *runtimeSession) ensureUploadLedgerLocked() {
	if r.uploadPrompts == nil {
		r.uploadPrompts = make(map[attachment.UploadID]string)
	}
	if r.uploadReserved == nil {
		r.uploadReserved = make(map[attachment.UploadID]int64)
	}
	if r.attachmentPrompts == nil {
		r.attachmentPrompts = make(map[attachment.ID]string)
	}
	if r.attachmentReserved == nil {
		r.attachmentReserved = make(map[attachment.ID]int64)
	}
	if r.promptCounts == nil {
		r.promptCounts = make(map[string]int)
	}
	if r.promptBytes == nil {
		r.promptBytes = make(map[string]int64)
	}
	if r.uploadTerminalSent == nil {
		r.uploadTerminalSent = make(map[attachment.UploadID]bool)
	}
}

func (r *runtimeSession) releasePromptReservationLocked(
	promptID string,
	size int64,
	counted bool,
) {
	if !counted {
		return
	}
	if r.promptCounts[promptID] > 0 {
		r.promptCounts[promptID]--
	}
	r.promptBytes[promptID] -= size
	if r.promptBytes[promptID] < 0 {
		r.promptBytes[promptID] = 0
	}
	if r.promptCounts[promptID] == 0 {
		delete(r.promptCounts, promptID)
		delete(r.promptBytes, promptID)
	}
}

func (r *runtimeSession) consumePromptAttachmentReservations(
	message protocol.Message,
	promptID string,
) {
	r.uploadMu.Lock()
	defer r.uploadMu.Unlock()
	r.ensureUploadLedgerLocked()
	for _, block := range message.Content {
		if block.Type != protocol.ContentAttachment ||
			r.attachmentPrompts[block.AttachmentID] != promptID {
			continue
		}
		size, reserved := r.attachmentReserved[block.AttachmentID]
		if !reserved {
			continue
		}
		delete(r.attachmentReserved, block.AttachmentID)
		r.releasePromptReservationLocked(promptID, size, true)
	}
}

func encodeAttachmentImportRejected(
	encoder *surface.Encoder,
	uploadID attachment.UploadID,
	code string,
) error {
	return encoder.Encode(map[string]any{
		"type": "attachment_import_rejected", "version": attachment.ProtocolVersion,
		"upload_id": uploadID, "status": "rejected", "terminal": false,
		"reason": code,
	})
}

func uploadErrorCode(err error) string {
	switch {
	case errors.Is(err, attachment.ErrDuplicate):
		return "duplicate"
	case errors.Is(err, attachment.ErrSequence):
		return "invalid_sequence"
	case errors.Is(err, attachment.ErrBase64):
		return "invalid_base64"
	case errors.Is(err, attachment.ErrSizeMismatch):
		return "size_mismatch"
	case errors.Is(err, attachment.ErrDigestMismatch):
		return "digest_mismatch"
	case errors.Is(err, attachment.ErrMediaMismatch):
		return "mime_mismatch"
	case errors.Is(err, attachment.ErrUnsupportedMedia):
		return "unsupported_media"
	case errors.Is(err, attachment.ErrMalformedMedia):
		return "malformed_media"
	case errors.Is(err, attachment.ErrResourceLimit):
		return "resource_limit"
	case errors.Is(err, attachment.ErrUploadExpired):
		return "timeout"
	case errors.Is(err, attachment.ErrUploadTerminal):
		return "upload_already_terminal"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	default:
		return "import_failed"
	}
}

func encodeDuplicate(encoder *surface.Encoder, session *runtimeSession, envelope surface.InputEnvelope, replay bool) error {
	if !replay {
		return nil
	}
	message, exists := session.engine.AcceptedPrompt(envelope.UUID)
	if !exists {
		legacy, err := surface.DecodeUserMessage(envelope.Message)
		if err != nil || legacy.HasAttachments() {
			return errors.New("accepted prompt replay metadata is unavailable")
		}
		message = protocol.Message{
			Role: protocol.RoleUser,
			Content: []protocol.ContentBlock{
				protocol.TextBlock(legacy.Text()),
			},
		}
	}
	wireMessage := map[string]any{"role": "user", "content": apiTextContent(message.Content)}
	if messageContentHasAttachments(message.Content) {
		wireMessage["content_version"] = attachment.ProtocolVersion
	}
	record := map[string]any{
		"type": "user", "message": wireMessage,
		"parent_tool_use_id": nil, "uuid": envelope.UUID,
		"session_id": session.engine.SessionID(), "isReplay": true,
	}
	if envelope.IsSynthetic {
		record["isSynthetic"] = true
	}
	return encoder.Encode(record)
}

func encodeStructuredCommandInput(encoder *surface.Encoder, session *runtimeSession, envelope surface.InputEnvelope, replay bool) error {
	if !replay {
		return nil
	}
	if envelope.UUID == "" {
		uuid, err := surface.NewUUID()
		if err != nil {
			return err
		}
		envelope.UUID = uuid
	}
	decoded, err := surface.DecodeUserMessage(envelope.Message)
	if err != nil || decoded.HasAttachments() {
		return errors.New("local command replay requires text-only input")
	}
	return encoder.Encode(map[string]any{
		"type": "user", "message": map[string]any{
			"role":    "user",
			"content": []map[string]any{{"type": "text", "text": decoded.Text()}},
		},
		"parent_tool_use_id": nil, "uuid": envelope.UUID,
		"session_id": session.engine.SessionID(), "isReplay": true,
	})
}

// runStructuredOneShot emits the same NDJSON event contract without treating
// ordinary text stdin as a duplex control channel.
func runStructuredOneShot(ctx context.Context, opts cli.Options, workspace string, stdin io.Reader, stdout, stderr io.Writer) (returnErr error) {
	encoder := surface.NewEncoder(stdout)
	sink := &streamSink{encoder: encoder, includePartial: opts.IncludePartial, replayUserMessages: opts.ReplayUserMessages}
	session, err := buildSession(ctx, buildOptions{CLI: opts, Workspace: workspace, Sink: sink, Stderr: stderr})
	if err != nil {
		return err
	}
	defer func() {
		returnErr = redactOperationalError(errors.Join(returnErr, session.Close()), session.sanitize)
	}()
	if err := encoder.SetValidator(credentialJSONValidator(session.credentials)); err != nil {
		return err
	}
	sink.model = session.config.Azure.ModelName
	if err := encodeSDKInit(encoder, session, opts); err != nil {
		return err
	}
	promptText, err := headlessPromptContextWithTerminalWarnings(ctx, opts.Prompt, stdin, stderr, session.credentials, stdinFirstByteTimeout)
	if err != nil {
		return encodeStructuredInputFailure(encoder, session, err)
	}
	if strings.TrimSpace(promptText) == "" && len(opts.Attachments) == 0 {
		return encodeStructuredInputFailure(
			encoder,
			session,
			&cli.UsageError{Message: "a prompt is required in headless mode"},
		)
	}
	if err := encodeSessionState(encoder, session.engine.SessionID(), "running"); err != nil {
		return err
	}
	if len(opts.Attachments) > 0 {
		if strings.HasPrefix(strings.TrimSpace(promptText), "/") {
			return encodeStructuredInputFailure(
				encoder, session,
				&cli.UsageError{Message: "attachments cannot accompany a slash or local command"},
			)
		}
		promptID, err := surface.NewUUID()
		if err != nil {
			return err
		}
		message, err := session.importInitialAttachments(ctx, promptText, opts.Attachments, promptID)
		if err != nil {
			resultErr := encodeRejectedStructuredUser(encoder, session, promptID, err)
			idleErr := encodeSessionState(encoder, session.engine.SessionID(), "idle")
			return errors.Join(err, resultErr, idleErr)
		}
		outcome, runErr := session.submitMessage(ctx, message, promptID)
		if outcome.SessionID == "" {
			outcome.SessionID = session.engine.SessionID()
		}
		resultErr := encodeSDKPromptResult(encoder, outcome, runErr, promptID)
		idleErr := encodeSessionState(encoder, session.engine.SessionID(), "idle")
		if err := errors.Join(resultErr, idleErr); err != nil {
			return errors.Join(runErr, err)
		}
		return runErr
	}
	commandStarted := time.Now()
	commandResult, isCommand, commandErr := session.dispatchUserCommand(ctx, promptText, true)
	if isCommand && commandErr == nil && commandResult.Kind == command.ResultPrompt {
		promptText = commandResult.Prompt
		if strings.TrimSpace(promptText) == "" {
			commandErr = errors.New("prompt command produced empty model input")
		}
	}
	if isCommand && (commandErr != nil || commandResult.Kind != command.ResultPrompt) {
		outcome := session.localCommandOutcome(commandResult, commandErr, commandStarted)
		commandOutput := commandResult.Output
		if commandErr != nil {
			commandOutput = commandErr.Error()
		}
		outputErr := encodeSDKLocalCommandOutput(encoder, session.engine.SessionID(), session.sanitize(commandOutput))
		resultErr := encodeSDKResult(encoder, outcome, commandErr)
		idleErr := encodeSessionState(encoder, session.engine.SessionID(), "idle")
		if err := errors.Join(outputErr, resultErr, idleErr); err != nil {
			return errors.Join(commandErr, err)
		}
		return commandErr
	}
	outcome, runErr := session.submitPrompt(ctx, promptText, "")
	if outcome.SessionID == "" {
		outcome.SessionID = session.engine.SessionID()
	}
	resultErr := encodeSDKResult(encoder, outcome, runErr)
	idleErr := encodeSessionState(encoder, session.engine.SessionID(), "idle")
	if err := errors.Join(resultErr, idleErr); err != nil {
		return errors.Join(runErr, err)
	}
	return runErr
}

func readStructuredInput(ctx context.Context, stdin io.Reader, stderr io.Writer, encoder *surface.Encoder, broker *surface.ControlBroker, queue *inputQueue, active *activeTurn, session *runtimeSession, replay bool) {
	warnings := newTerminalLineWriter(stderr, session.credentials)
	decoder := surface.NewDecoder(stdin, warnings)
	fail := func(err error) {
		if cleanupErr := session.settleUploads(encoder, attachment.AbortProcessFailure); cleanupErr != nil {
			err = errors.Join(err, cleanupErr)
		}
		active.abort()
		broker.Close()
		queue.close(err)
	}
	defer func() {
		if recover() != nil {
			fail(errStructuredInputWorker)
		}
	}()
	defer func() {
		if err := warnings.Flush(); err != nil {
			fail(err)
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			fail(err)
			return
		}
		envelope, err := decoder.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				if cleanupErr := session.settleUploads(encoder, attachment.AbortEOF); cleanupErr != nil {
					fail(cleanupErr)
					return
				}
				broker.Close()
				queue.close(nil)
			} else {
				fail(err)
			}
			return
		}
		if envelope.SessionID != "" && envelope.SessionID != string(session.engine.SessionID()) {
			streamErr := fmt.Errorf("structured input session_id %q does not match active session %q", envelope.SessionID, session.engine.SessionID())
			fail(streamErr)
			return
		}
		switch envelope.Type {
		case "user":
			if envelope.UUID == "" {
				envelope.UUID, err = surface.NewUUID()
				if err != nil {
					fail(err)
					return
				}
			}
			if envelope.Priority != "" && envelope.Priority != "now" && envelope.Priority != "next" && envelope.Priority != "later" {
				rejection := fmt.Errorf("unsupported structured priority %q", envelope.Priority)
				queue.reject(rejection)
				if err := encodeRejectedStructuredUser(encoder, session, envelope.UUID, rejection); err != nil {
					fail(err)
					return
				}
				continue
			}
			if envelope.IsReplay && (envelope.UUID == "" || envelope.SessionID == "") {
				if err := encodeRejectedStructuredUser(encoder, session, envelope.UUID, errors.New("replayed user input requires uuid and session_id")); err != nil {
					fail(err)
					return
				}
				continue
			}
			decoded, decodeErr := surface.DecodeUserMessage(envelope.Message)
			if decodeErr != nil {
				rejection := errors.New("invalid structured user message")
				queue.reject(rejection)
				if err := encodeRejectedStructuredUser(encoder, session, envelope.UUID, rejection); err != nil {
					fail(err)
					return
				}
				continue
			}
			message, admissionErr := session.admitUserMessage(ctx, decoded, envelope.UUID)
			if admissionErr != nil {
				queue.reject(admissionErr)
				if err := encodeRejectedStructuredUser(encoder, session, envelope.UUID, admissionErr); err != nil {
					fail(err)
					return
				}
				continue
			}
			interrupt := !session.engine.HasPromptID(envelope.UUID)
			if err := enqueueStructuredMessage(queue, active, envelope, message, interrupt); err != nil {
				queue.reject(err)
				if err := encodeRejectedStructuredUser(encoder, session, envelope.UUID, err); err != nil {
					fail(err)
					return
				}
				continue
			}
			session.consumePromptAttachmentReservations(message, envelope.UUID)
		case "attachment_import":
			if envelope.AttachmentImport == nil {
				fail(errors.New("attachment import payload is missing"))
				return
			}
			if err := session.handleAttachmentImport(ctx, encoder, *envelope.AttachmentImport); err != nil {
				fail(err)
				return
			}
		case "control_response":
			if envelope.Response == nil || envelope.Response.RequestID == "" {
				fail(errors.New("structured control response requires response.request_id"))
				return
			}
			resolved := broker.Resolve(envelope.Response.RequestID, *envelope.Response)
			if replay && resolved {
				if err := encoder.Encode(surface.OutputEnvelope{Type: "control_response", Response: envelope.Response}); err != nil {
					fail(err)
					return
				}
			}
		case "control_cancel_request":
			if envelope.RequestID == "" {
				fail(errors.New("structured control cancellation requires request_id"))
				return
			}
			broker.Cancel(envelope.RequestID)
		case "control_request":
			if envelope.RequestID == "" {
				fail(errors.New("structured control request requires request_id"))
				return
			}
			if err := handleControl(envelope, encoder, broker, active, session); err != nil {
				fail(err)
				return
			}
		case "update_environment_variables":
			if stderr != nil {
				record := "warning: live environment mutation is unavailable; credential and tool environments are frozen per session\n"
				if err := writeTerminalRecord(stderr, session.credentials, record); err != nil {
					fail(err)
					return
				}
			}
		case "assistant", "system":
			if stderr != nil {
				record := fmt.Sprintf("warning: ignored unsupported historical %s replay record\n", envelope.Type)
				if err := writeTerminalRecord(stderr, session.credentials, record); err != nil {
					fail(err)
					return
				}
			}
		case "keep_alive":
		}
	}
}

func (r *runtimeSession) settleUploads(
	encoder *surface.Encoder,
	reason attachment.AbortReason,
) error {
	if r.attachments == nil {
		return nil
	}
	var result error
	for _, ack := range r.attachments.AbortAll(reason) {
		result = errors.Join(result, r.encodeAttachmentAcknowledgement(encoder, ack))
	}
	return result
}

func (r *runtimeSession) monitorUploadExpiry(
	ctx context.Context,
	encoder *surface.Encoder,
	fail func(error),
) {
	if r.attachments == nil {
		return
	}
	notifications := r.attachments.UploadNotifications()
	for {
		select {
		case <-ctx.Done():
			return
		case ack, open := <-notifications:
			if !open {
				return
			}
			if err := r.encodeAttachmentAcknowledgement(encoder, ack); err != nil {
				fail(err)
				return
			}
		}
	}
}

// enqueueStructuredUser makes admission precede interruption. A queue that is
// already closed or full cannot cancel useful in-flight work for an input it
// will never retain. Once admitted, priority-now uses the same active-turn
// cancellation primitive as the explicit interrupt control; the normal turn
// result and accepted tool settlements still complete before the queued prompt
// becomes the next serialized workload.
func enqueueStructuredUser(queue *inputQueue, active *activeTurn, envelope surface.InputEnvelope) error {
	if err := queue.push(envelope); err != nil {
		return err
	}
	if envelope.Priority == "now" && active != nil {
		active.interrupt()
	}
	return nil
}

func enqueueStructuredMessage(
	queue *inputQueue,
	active *activeTurn,
	envelope surface.InputEnvelope,
	message protocol.Message,
	interrupt bool,
) error {
	if err := queue.pushMessage(envelope, message); err != nil {
		return err
	}
	if interrupt && envelope.Priority == "now" && active != nil {
		active.interrupt()
	}
	return nil
}

func handleControl(envelope surface.InputEnvelope, encoder *surface.Encoder, broker *surface.ControlBroker, active *activeTurn, session *runtimeSession) error {
	var payload any
	var operationErr error
	if envelope.Request == nil {
		operationErr = errors.New("missing request")
	} else {
		switch envelope.Request.Subtype {
		case "initialize":
			if hasControlFields(envelope.Request.Data) {
				operationErr = errors.New("initialize hooks, MCP injection, prompt replacement, agent definitions, and structured output schemas are unavailable in this runtime profile")
			} else {
				payload, operationErr = sdkInitializeResponse(session)
			}
		case "interrupt":
			if hasControlFields(envelope.Request.Data) {
				operationErr = errors.New("interrupt does not accept operation fields")
			} else {
				active.interrupt()
				return broker.AbortPendingThen(func() error {
					return encodeControlResponse(encoder, envelope.RequestID, nil, nil)
				})
			}
		case "get_context_usage":
			if hasControlFields(envelope.Request.Data) {
				operationErr = errors.New("get_context_usage does not accept operation fields")
			} else {
				payload = sdkContextUsage(session)
			}
		case "mcp_status":
			if hasControlFields(envelope.Request.Data) {
				operationErr = errors.New("mcp_status does not accept operation fields")
			} else {
				payload = map[string]any{"mcpServers": sdkMCPServers(session)}
			}
		case "set_model":
			operationErr = fmt.Errorf("model is fixed to %q by auth.json", session.config.Azure.ModelName)
		case "set_permission_mode":
			operationErr = errors.New("live permission-mode mutation is unavailable; start a new session with --permission-mode")
		case "set_max_thinking_tokens":
			operationErr = errors.New("the gpt-5.6-sol Azure profile uses reasoning effort and does not expose a mutable thinking-token budget")
		default:
			operationErr = fmt.Errorf("unsupported control request %q", envelope.Request.Subtype)
		}
	}
	return encodeControlResponse(encoder, envelope.RequestID, payload, operationErr)
}

func encodeSDKInit(encoder *surface.Encoder, session *runtimeSession, opts cli.Options) error {
	uuid, err := surface.NewUUID()
	if err != nil {
		return err
	}
	commands, err := command.Builtins(session)
	if err != nil {
		return err
	}
	commandNames := make([]string, 0)
	for _, descriptor := range commands.DescriptorsForSurface(false, true) {
		if descriptor.UserInvocable {
			commandNames = append(commandNames, descriptor.Name)
		}
	}
	mode := effectivePermissionMode(opts)
	record := map[string]any{
		"type": "system", "subtype": "init", "apiKeySource": sdkAPIKeySource(session.config),
		"agentx_version": ProductVersion(), "cwd": session.workspace, "tools": toolNames(session),
		"mcp_servers": sdkMCPServers(session), "model": session.config.Azure.ModelName, "permissionMode": mode,
		"slash_commands": commandNames, "output_style": sdkOutputStyle(session), "skills": sdkSkillNames(session),
		"plugins": sdkPlugins(session), "agents": []any{}, "uuid": uuid, "session_id": session.engine.SessionID(),
	}
	if capabilities := sdkInputCapabilities(session); capabilities != nil {
		record["input_capabilities"] = capabilities
	}
	return encoder.Encode(record)
}

func sdkInitializeResponse(session *runtimeSession) (map[string]any, error) {
	registry, err := command.Builtins(session)
	if err != nil {
		return nil, err
	}
	commands := make([]map[string]any, 0)
	for _, descriptor := range registry.DescriptorsForSurface(false, true) {
		if !descriptor.UserInvocable {
			continue
		}
		commands = append(commands, map[string]any{
			"name": descriptor.Name, "description": descriptor.Description, "argumentHint": descriptor.ArgumentHint,
		})
	}
	modelName := session.config.Azure.ModelName
	result := map[string]any{
		"commands":                commands,
		"agents":                  []any{},
		"output_style":            sdkOutputStyle(session),
		"available_output_styles": sdkAvailableOutputStyles(session),
		"models": []map[string]any{{
			"value": modelName, "displayName": modelName,
			"description":    "Deployment-backed Azure OpenAI Responses model configured by AgentX-home auth.json",
			"supportsEffort": true,
		}},
		"account": map[string]any{"apiKeySource": sdkAPIKeySource(session.config), "apiProvider": "foundry"},
		"pid":     os.Getpid(),
	}
	if capabilities := sdkInputCapabilities(session); capabilities != nil {
		result["input_capabilities"] = capabilities
	}
	return result, nil
}

func sdkInputCapabilities(session *runtimeSession) map[string]any {
	if session == nil || !session.inputMediaEnabled {
		return nil
	}
	capability := session.inputMedia
	return map[string]any{
		"attachments": map[string]any{
			"protocol_version": capability.Attachment.ProtocolVersion,
			"sources":          capability.Attachment.Sources,
			"media_types":      capability.Attachment.MediaTypes,
			"limits":           capability.Attachment.Limits,
			"provider_limits": map[string]any{
				"max_request_items":       capability.MaxRequestItems,
				"max_encoded_media_bytes": capability.MaxEncodedBytes,
				"max_request_bytes":       capability.MaxRequestBytes,
				"max_ndjson_record_bytes": surface.MaxNDJSONRecordBytes,
			},
		},
	}
}

func sdkAPIKeySource(runtime config.Runtime) string {
	source := runtime.Provenance["AZURE_OPENAI_SUBSCRIPTION_KEY"]
	switch source {
	case config.SourceFile:
		return "user"
	case config.SourceProcess, config.SourceFlag:
		return "temporary"
	default:
		return "user"
	}
}

func sdkSkillNames(session *runtimeSession) []string {
	names := make([]string, 0, len(session.skills.Skills))
	for _, skill := range session.skills.Skills {
		if skill.UserInvocable && skill.Availability.Usable() {
			names = append(names, skill.CanonicalName)
		}
	}
	sort.Strings(names)
	return names
}

func sdkMCPServers(session *runtimeSession) []map[string]any {
	snapshot := session.services.extensions.mcpState
	if session.services.extensions.mcp != nil {
		snapshot = session.services.extensions.mcp.Snapshot()
	}
	servers := make([]map[string]any, 0, len(snapshot.Servers))
	for _, server := range snapshot.Servers {
		servers = append(servers, map[string]any{"name": server.Name, "status": server.State})
	}
	return servers
}

func sdkPlugins(session *runtimeSession) []map[string]any {
	plugins := make([]map[string]any, 0, len(session.services.extensions.plugins.Plugins))
	for _, plugin := range session.services.extensions.plugins.Plugins {
		if !plugin.Availability.Usable() {
			continue
		}
		plugins = append(plugins, map[string]any{"name": plugin.Name, "path": plugin.Root, "source": plugin.CanonicalID})
	}
	return plugins
}

func sdkOutputStyle(session *runtimeSession) string {
	if style := session.services.extensions.selection.Style; style != nil && style.Availability.Usable() {
		return style.CanonicalName
	}
	return "default"
}

func sdkAvailableOutputStyles(session *runtimeSession) []string {
	names := make([]string, 0, len(session.services.extensions.styles.Styles)+1)
	seen := make(map[string]bool)
	for _, style := range session.services.extensions.styles.Styles {
		if style.Availability.Usable() && !seen[style.CanonicalName] {
			seen[style.CanonicalName] = true
			names = append(names, style.CanonicalName)
		}
	}
	if !seen["default"] {
		names = append(names, "default")
	}
	sort.Strings(names)
	return names
}

func effectivePermissionMode(opts cli.Options) string {
	if opts.DangerouslyBypass {
		return "bypassPermissions"
	}
	if opts.PermissionMode == "" {
		return "default"
	}
	return opts.PermissionMode
}

func encodeSessionState(encoder *surface.Encoder, sessionID protocol.SessionID, state string) error {
	if state != "idle" && state != "running" && state != "requires_action" {
		return fmt.Errorf("invalid SDK session state %q", state)
	}
	if sessionID == "" {
		return errors.New("SDK session state requires session_id")
	}
	uuid, err := surface.NewUUID()
	if err != nil {
		return err
	}
	return encoder.Encode(map[string]any{
		"type": "system", "subtype": "session_state_changed", "state": state,
		"uuid": uuid, "session_id": sessionID,
	})
}

func encodeSDKLocalCommandOutput(encoder *surface.Encoder, sessionID protocol.SessionID, content string) error {
	if sessionID == "" {
		return errors.New("SDK local command output requires session_id")
	}
	uuid, err := surface.NewUUID()
	if err != nil {
		return err
	}
	return encoder.Encode(map[string]any{
		"type": "system", "subtype": "local_command_output", "content": content,
		"uuid": uuid, "session_id": sessionID,
	})
}

func encodeSDKResult(encoder *surface.Encoder, outcome engine.Outcome, runErr error) error {
	record, err := sdkResultRecord(outcome, runErr)
	if err != nil {
		return err
	}
	return encoder.Encode(record)
}

func encodeSDKPromptResult(
	encoder *surface.Encoder,
	outcome engine.Outcome,
	runErr error,
	promptID string,
) error {
	record, err := sdkResultRecord(outcome, runErr)
	if err != nil {
		return err
	}
	if promptID != "" {
		record["prompt_uuid"] = promptID
	}
	return encoder.Encode(record)
}

// sdkResultRecord is the single public terminal-result projection for both
// aggregate JSON and streaming SDK output. Keeping it shared prevents a
// surface from leaking internal turn-status names or non-UUID identifiers.
func sdkResultRecord(outcome engine.Outcome, runErr error) (map[string]any, error) {
	if outcome.SessionID == "" {
		return nil, errors.New("SDK result requires session_id")
	}
	uuid, err := surface.NewUUID()
	if err != nil {
		return nil, err
	}
	subtype := "error_during_execution"
	switch {
	case outcome.Status == protocol.TurnResultSuccess && runErr == nil:
		subtype = "success"
	case outcome.Status == protocol.TurnResultMaxTurns:
		subtype = "error_max_turns"
	case outcome.Status == protocol.TurnResultMaxBudget:
		subtype = "error_max_budget_usd"
	}
	stopReason := any(nil)
	if outcome.StopReason != "" {
		stopReason = outcome.StopReason
	}
	var cost any
	if outcome.Usage.CostUSD != nil {
		cost = *outcome.Usage.CostUSD
	}
	permissionDenials, err := sdkPermissionDenials(outcome.PermissionDenials)
	if err != nil {
		return nil, err
	}
	record := map[string]any{
		"type": "result", "subtype": subtype,
		"duration_ms": outcome.Duration.Milliseconds(), "duration_api_ms": outcome.APIDuration.Milliseconds(),
		"is_error": subtype != "success", "num_turns": outcome.ModelTurns,
		"stop_reason": stopReason, "total_cost_usd": cost,
		"usage": sdkAggregateUsage(outcome.Usage), "modelUsage": sdkModelUsage(outcome.Usage),
		"permission_denials": permissionDenials, "uuid": uuid, "session_id": outcome.SessionID,
	}
	if outcome.PromptID != "" {
		record["prompt_uuid"] = outcome.PromptID
	}
	if subtype == "success" {
		record["result"] = outcome.Text
	} else {
		message := "turn failed"
		if runErr != nil {
			message = safeOperationalErrorText(redactOperationalError(runErr, nil))
		}
		record["errors"] = []string{message}
	}
	return record, nil
}

func sdkPermissionDenials(source []engine.PermissionDenial) ([]engine.PermissionDenial, error) {
	result := make([]engine.PermissionDenial, len(source))
	for index, denial := range source {
		if strings.TrimSpace(denial.ToolName) == "" || denial.ToolUseID == "" {
			return nil, fmt.Errorf("permission denial %d requires tool name and tool-use ID", index)
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(denial.ToolInput, &object); err != nil || object == nil {
			return nil, fmt.Errorf("permission denial %d tool input must be a JSON object", index)
		}
		result[index] = denial
		result[index].ToolInput = append(json.RawMessage(nil), denial.ToolInput...)
	}
	return result, nil
}

func sdkAggregateUsage(usage protocol.Usage) map[string]any {
	return map[string]any{
		"input_tokens": usage.InputTokens, "output_tokens": usage.OutputTokens,
		"cache_creation_input_tokens": int64(0), "cache_read_input_tokens": usage.CachedInputTokens,
		"reasoning_output_tokens": usage.ReasoningTokens, "total_tokens": usage.TotalTokens,
	}
}

func sdkModelUsage(usage protocol.Usage) map[string]any {
	var cost any
	if usage.CostUSD != nil {
		cost = *usage.CostUSD
	}
	return map[string]any{
		usage.Model: map[string]any{
			"inputTokens": usage.InputTokens, "outputTokens": usage.OutputTokens,
			"cacheReadInputTokens": usage.CachedInputTokens, "cacheCreationInputTokens": int64(0),
			"webSearchRequests": 0, "costUSD": cost, "contextWindow": 128_000,
			"maxOutputTokens": engine.DefaultMaxOutputTokens,
		},
	}
}

func sdkContextUsage(session *runtimeSession) map[string]any {
	status := session.engine.Status()
	tokens := status.Usage.TotalTokens
	percentage := float64(tokens) / 128_000 * 100
	return map[string]any{
		"categories":  []map[string]any{{"name": "Conversation", "tokens": tokens, "color": "blue"}},
		"totalTokens": tokens, "maxTokens": 128_000, "rawMaxTokens": 128_000,
		"percentage": percentage, "gridRows": []any{}, "model": status.Model,
		"memoryFiles": []any{}, "mcpTools": []any{}, "agents": []any{},
		"isAutoCompactEnabled": false,
		"apiUsage":             sdkAggregateUsage(status.Usage),
	}
}

func encodeControlResponse(encoder *surface.Encoder, requestID protocol.RequestID, payload any, operationErr error) error {
	body := &surface.ControlResponseBody{RequestID: requestID}
	if operationErr != nil {
		body.Subtype = "error"
		body.Error = operationErr.Error()
	} else {
		body.Subtype = "success"
		if payload != nil {
			encoded, err := json.Marshal(payload)
			if err != nil {
				return fmt.Errorf("encode control response payload: %w", err)
			}
			body.Response = encoded
		}
	}
	return encoder.Encode(surface.OutputEnvelope{Type: "control_response", Response: body})
}

func hasControlFields(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var fields map[string]json.RawMessage
	return json.Unmarshal(raw, &fields) != nil || len(fields) != 0
}

func toolNames(session *runtimeSession) []string {
	descriptors := session.registry.Descriptors()
	names := make([]string, 0, len(descriptors))
	for _, descriptor := range descriptors {
		names = append(names, descriptor.Name)
	}
	return names
}

var _ engine.EventSink = (*streamSink)(nil)
