package tool

import (
	"errors"
	"path/filepath"

	"github.com/greenpau/agentx/pkg/sandbox"
	"github.com/greenpau/agentx/pkg/task"
)

// CoreOptions selects the portable workstation profile. Optional build-specific
// capabilities are absent rather than registered as name-only stubs.
type CoreOptions struct {
	Workspace      string
	Tasks          *task.Manager
	Ask            AskFunc
	LegacyTodos    bool
	Shell          string
	Environment    []string
	FileTracker    *FileTracker
	Results        *ResultStore
	Sandbox        *sandbox.Runner
	ProtectedPaths []string
}

// NewCoreRegistry builds a deterministic registry for local execution.
func NewCoreRegistry(options CoreOptions) (*Registry, error) {
	if options.Workspace == "" {
		return nil, errors.New("workspace is required")
	}
	workspace, err := filepath.Abs(options.Workspace)
	if err != nil {
		return nil, err
	}
	tracker := options.FileTracker
	if tracker == nil {
		tracker = NewFileTracker()
	}
	descriptors := []Descriptor{
		readDescriptor(workspace, tracker), writeDescriptor(workspace, tracker), editDescriptor(workspace, tracker),
		globDescriptor(workspace, options.ProtectedPaths), grepDescriptor(workspace, options.ProtectedPaths),
		bashDescriptor(workspace, options.Shell, options.Tasks, options.Environment, options.Sandbox),
	}
	if options.Ask != nil {
		descriptors = append(descriptors, askUserQuestionDescriptor(options.Ask))
	}
	if options.Tasks != nil {
		descriptors = append(descriptors, taskOutputDescriptor(options.Tasks), taskStopDescriptor(options.Tasks))
		if options.LegacyTodos {
			descriptors = append(descriptors, todoWriteDescriptor(options.Tasks))
		} else {
			descriptors = append(descriptors,
				taskCreateDescriptor(options.Tasks), taskGetDescriptor(options.Tasks),
				taskListDescriptor(options.Tasks), taskUpdateDescriptor(options.Tasks),
			)
		}
	}
	if options.Results != nil {
		descriptors = append(descriptors, resultReadDescriptor(options.Results))
	}
	return NewRegistry(descriptors...)
}
