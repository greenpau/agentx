package app

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/greenpau/agentx/pkg/cli"
	"github.com/greenpau/agentx/pkg/platform"
	"github.com/greenpau/agentx/pkg/surface"
	"github.com/greenpau/agentx/pkg/transcript"
)

type sessionManagementOutcomeError struct {
	operation string
	status    string
}

func (err *sessionManagementOutcomeError) Error() string {
	return fmt.Sprintf("native session %s completed with status %s", err.operation, err.status)
}

func runSessionManagement(
	ctx context.Context,
	sessionsRoot *platform.OwnedDirectory,
	options cli.Options,
	workspace string,
	stdout io.Writer,
) error {
	manager, err := transcript.NewSessionManager(sessionsRoot, workspace)
	if err != nil {
		if options.ListSessions {
			result := transcript.SessionListResult{
				Version:  transcript.SessionManagementVersion,
				Status:   transcript.SessionListStoreUnsafe,
				Sessions: []transcript.SessionInventoryItem{},
			}
			if writeErr := writeSessionList(stdout, options.OutputFormat, result); writeErr != nil {
				return writeErr
			}
			return &sessionManagementOutcomeError{
				operation: "inventory",
				status:    string(transcript.SessionListStoreUnsafe),
			}
		}
		result := transcript.SessionDeleteResult{
			Version: transcript.SessionManagementVersion,
			Status:  transcript.SessionStoreUnsafe,
		}
		if transcript.ValidSessionID(options.DeleteSession) {
			result.SessionID = options.DeleteSession
		}
		if writeErr := writeSessionDelete(stdout, options.OutputFormat, result); writeErr != nil {
			return writeErr
		}
		return &sessionManagementOutcomeError{
			operation: "deletion",
			status:    string(transcript.SessionStoreUnsafe),
		}
	}
	if options.ListSessions {
		result, listErr := manager.List(ctx, options.SessionPageSize, options.SessionPageToken)
		if err := writeSessionList(stdout, options.OutputFormat, result); err != nil {
			return err
		}
		if listErr != nil {
			return &sessionManagementOutcomeError{operation: "inventory", status: string(result.Status)}
		}
		return nil
	}

	result, deleteErr := manager.Delete(ctx, options.DeleteSession, options.SessionRevision)
	if err := writeSessionDelete(stdout, options.OutputFormat, result); err != nil {
		return err
	}
	if deleteErr != nil || result.Status != transcript.SessionDeleted {
		return &sessionManagementOutcomeError{operation: "deletion", status: string(result.Status)}
	}
	return nil
}

func writeSessionList(writer io.Writer, format cli.OutputFormat, result transcript.SessionListResult) error {
	if format == cli.OutputJSON {
		return surface.NewEncoder(writer).Encode(result)
	}
	var output strings.Builder
	if result.Status != transcript.SessionListOK {
		fmt.Fprintf(&output, "status\t%s\n", result.Status)
	} else if len(result.Sessions) == 0 {
		output.WriteString("No sessions found.\n")
	} else {
		for _, item := range result.Sessions {
			fmt.Fprintf(&output, "%s\t%s\t%s\n", item.SessionID, item.UpdatedAt, item.Revision)
		}
	}
	if result.NextPageToken != "" {
		fmt.Fprintf(&output, "next_page_token\t%s\n", result.NextPageToken)
	}
	return writeStringExact(writer, output.String())
}

func writeSessionDelete(writer io.Writer, format cli.OutputFormat, result transcript.SessionDeleteResult) error {
	if format == cli.OutputJSON {
		return surface.NewEncoder(writer).Encode(result)
	}
	return writeStringExact(writer, fmt.Sprintf("%s\t%s\n", result.Status, result.SessionID))
}
