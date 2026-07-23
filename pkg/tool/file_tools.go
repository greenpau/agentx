package tool

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/greenpau/agentx/pkg/permission"
)

const (
	defaultReadLines  = 2_000
	maximumReadLines  = 5_000
	maximumReadBytes  = 16 << 20
	maximumEditBytes  = 16 << 20
	maximumWriteBytes = 16 << 20
)

type readInput struct {
	FilePath string `json:"file_path"`
	Offset   int    `json:"offset,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

func readDescriptor(workspace string, tracker *FileTracker) Descriptor {
	return Descriptor{
		Name: "Read", Source: SourceBuiltin, Description: "Read a bounded workspace text file by absolute path with numbered lines.",
		InputSchema: objectSchema(map[string]any{
			"file_path": stringSchema("Absolute path to the file"),
			"offset":    integerSchema("One-based first line", 1, 1_000_000_000),
			"limit":     integerSchema("Maximum lines", 1, maximumReadLines),
		}, "file_path"),
		Validate: func(raw json.RawMessage) (any, error) {
			var input readInput
			if err := decodeStrict(raw, &input); err != nil {
				return nil, err
			}
			if !filepath.IsAbs(input.FilePath) {
				return nil, errors.New("file_path must be absolute")
			}
			if _, err := workspaceRelative(workspace, input.FilePath); err != nil {
				return nil, err
			}
			if input.Offset == 0 {
				input.Offset = 1
			}
			if input.Limit == 0 {
				input.Limit = defaultReadLines
			}
			if input.Offset < 1 || input.Limit < 1 || input.Limit > maximumReadLines {
				return nil, errors.New("offset and limit are outside supported bounds")
			}
			return input, nil
		},
		Classify: func(any) permission.Classification {
			return permission.Classification{ReadOnly: true, ConcurrencySafe: true}
		},
		ProjectPermission: func(value any, raw json.RawMessage) (permission.Request, error) {
			input := value.(readInput)
			return permission.Request{Input: raw, Paths: []permission.PathAccess{{Path: input.FilePath, Operation: permission.PathRead}}}, nil
		},
		Call: func(ctx context.Context, _ CallContext, value any) (Output, error) {
			input := value.(readInput)
			rooted, err := openWorkspaceParent(workspace, input.FilePath, false)
			if err != nil {
				return Output{}, invocationError("execution_failed", "open workspace path: %v", err)
			}
			defer rooted.Close()
			if err := rooted.Verify(); err != nil {
				return Output{}, invocationError("execution_failed", "verify authorized parent: %v", err)
			}
			file, err := openRegular(rooted.parent, rooted.leaf, maximumReadBytes, false)
			if err != nil {
				return Output{}, invocationError("execution_failed", "open file: %v", err)
			}
			defer file.Close()
			observation, err := fingerprintOpenFile(file)
			if err != nil {
				return Output{}, invocationError("execution_failed", "fingerprint read snapshot: %v", err)
			}
			if _, err := file.Seek(0, io.SeekStart); err != nil {
				return Output{}, invocationError("execution_failed", "rewind read snapshot: %v", err)
			}
			reader := bufio.NewReader(io.LimitReader(file, maximumReadBytes+1))
			var output strings.Builder
			lineNumber := 0
			returned := 0
			truncated := false
			for {
				if err := ctx.Err(); err != nil {
					return Output{}, err
				}
				line, readErr := reader.ReadString('\n')
				if line != "" {
					lineNumber++
					if lineNumber >= input.Offset && returned < input.Limit {
						fmt.Fprintf(&output, "%6d→%s", lineNumber, line)
						if !strings.HasSuffix(line, "\n") {
							output.WriteByte('\n')
						}
						returned++
					} else if returned >= input.Limit {
						truncated = true
						break
					}
				}
				if errors.Is(readErr, io.EOF) {
					break
				}
				if readErr != nil {
					return Output{}, invocationError("execution_failed", "read file: %v", readErr)
				}
			}
			current, err := fingerprintOpenFile(file)
			if err != nil {
				return Output{}, invocationError("execution_failed", "verify read snapshot: %v", err)
			}
			if !sameFingerprint(observation, current) {
				return Output{}, invocationError("stale_file", "file changed while it was being read; no observation was recorded")
			}
			if err := tracker.recordObservation(input.FilePath, observation); err != nil {
				return Output{}, invocationError("execution_failed", "record read observation: %v", err)
			}
			if truncated {
				fmt.Fprintf(&output, "[truncated; continue with offset=%d]\n", input.Offset+returned)
			}
			return Output{Content: output.String(), Metadata: map[string]any{"line_count": returned, "truncated": truncated}}, nil
		},
		MaxResultChars: -1,
	}
}

type writeInput struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

func writeDescriptor(workspace string, tracker *FileTracker) Descriptor {
	return Descriptor{
		Name: "Write", Source: SourceBuiltin, Description: "Create or replace a complete workspace text file with stale-read and no-clobber checks.",
		InputSchema: objectSchema(map[string]any{
			"file_path": stringSchema("Absolute path to create or replace"),
			"content":   stringSchema("Complete file contents"),
		}, "file_path", "content"),
		Validate: func(raw json.RawMessage) (any, error) {
			var input writeInput
			if err := decodeStrict(raw, &input); err != nil {
				return nil, err
			}
			if !filepath.IsAbs(input.FilePath) {
				return nil, errors.New("file_path must be absolute")
			}
			if _, err := workspaceRelative(workspace, input.FilePath); err != nil {
				return nil, err
			}
			if len(input.Content) > maximumWriteBytes {
				return nil, fmt.Errorf("content exceeds the %d-byte write limit", maximumWriteBytes)
			}
			return input, nil
		},
		Classify: func(any) permission.Classification { return permission.Classification{} },
		ProjectPermission: func(value any, raw json.RawMessage) (permission.Request, error) {
			input := value.(writeInput)
			return permission.Request{Input: raw, Paths: []permission.PathAccess{{Path: input.FilePath, Operation: permission.PathWrite}}}, nil
		},
		Call: func(ctx context.Context, _ CallContext, value any) (Output, error) {
			if err := ctx.Err(); err != nil {
				return Output{}, err
			}
			input := value.(writeInput)
			rooted, err := openWorkspaceParent(workspace, input.FilePath, true)
			if err != nil {
				return Output{}, invocationError("execution_failed", "open workspace path: %v", err)
			}
			defer rooted.Close()
			existed := false
			if info, inspectErr := rooted.parent.Lstat(rooted.leaf); inspectErr == nil {
				existed = true
				if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
					return Output{}, invocationError("execution_failed", "write target is not a regular file")
				}
				current, openErr := rooted.parent.Open(rooted.leaf)
				if openErr != nil {
					return Output{}, invocationError("execution_failed", "open write target: %v", openErr)
				}
				currentErr := tracker.RequireCurrentFile(input.FilePath, current)
				closeErr := current.Close()
				if currentErr != nil {
					return Output{}, invocationError("stale_file", "%v", currentErr)
				}
				if closeErr != nil {
					return Output{}, invocationError("execution_failed", "close write target: %v", closeErr)
				}
			} else if !errors.Is(inspectErr, os.ErrNotExist) {
				return Output{}, invocationError("execution_failed", "inspect write target: %v", inspectErr)
			}
			created, err := atomicWriteRoot(rooted, []byte(input.Content), existed, func(file *os.File) error {
				return tracker.RequireCurrentFile(input.FilePath, file)
			})
			if err != nil {
				return Output{}, invocationError("execution_failed", "write file: %v", err)
			}
			written, err := rooted.parent.Open(rooted.leaf)
			if err != nil {
				return Output{}, invocationError("execution_failed", "open written file: %v", err)
			}
			defer written.Close()
			if err := tracker.ObserveFile(input.FilePath, written); err != nil {
				return Output{}, invocationError("execution_failed", "record written file: %v", err)
			}
			action := "updated"
			if created {
				action = "created"
			}
			return Output{Content: fmt.Sprintf("%s %s (%d bytes)", action, input.FilePath, len(input.Content))}, nil
		},
	}
}

type editInput struct {
	FilePath   string `json:"file_path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

func editDescriptor(workspace string, tracker *FileTracker) Descriptor {
	return Descriptor{
		Name: "Edit", Source: SourceBuiltin, Description: "Replace one unique text occurrence or all exact occurrences in a previously read workspace file with stale-read and no-clobber checks.",
		InputSchema: objectSchema(map[string]any{
			"file_path":   stringSchema("Absolute path to a previously read file"),
			"old_string":  stringSchema("Exact text to replace"),
			"new_string":  stringSchema("Replacement text"),
			"replace_all": booleanSchema("Replace every exact occurrence"),
		}, "file_path", "old_string", "new_string"),
		Validate: func(raw json.RawMessage) (any, error) {
			var input editInput
			if err := decodeStrict(raw, &input); err != nil {
				return nil, err
			}
			if !filepath.IsAbs(input.FilePath) {
				return nil, errors.New("file_path must be absolute")
			}
			if _, err := workspaceRelative(workspace, input.FilePath); err != nil {
				return nil, err
			}
			if input.OldString == input.NewString {
				return nil, errors.New("old_string and new_string must differ")
			}
			if input.OldString == "" {
				return nil, errors.New("old_string cannot be empty")
			}
			return input, nil
		},
		Semantic: func(value any) error {
			if strings.EqualFold(filepath.Ext(value.(editInput).FilePath), ".ipynb") {
				return errors.New("notebook files require the notebook editing capability")
			}
			return nil
		},
		Classify: func(any) permission.Classification { return permission.Classification{} },
		ProjectPermission: func(value any, raw json.RawMessage) (permission.Request, error) {
			input := value.(editInput)
			return permission.Request{Input: raw, Paths: []permission.PathAccess{{Path: input.FilePath, Operation: permission.PathWrite}}}, nil
		},
		Call: func(ctx context.Context, _ CallContext, value any) (Output, error) {
			if err := ctx.Err(); err != nil {
				return Output{}, err
			}
			input := value.(editInput)
			rooted, err := openWorkspaceParent(workspace, input.FilePath, false)
			if err != nil {
				return Output{}, invocationError("execution_failed", "open workspace path: %v", err)
			}
			defer rooted.Close()
			if err := rooted.Verify(); err != nil {
				return Output{}, invocationError("execution_failed", "verify authorized parent: %v", err)
			}
			file, err := openRegular(rooted.parent, rooted.leaf, maximumEditBytes, true)
			if err != nil {
				return Output{}, invocationError("execution_failed", "open edit target: %v", err)
			}
			if err := tracker.RequireCurrentFile(input.FilePath, file); err != nil {
				_ = file.Close()
				return Output{}, invocationError("stale_file", "%v", err)
			}
			b, err := io.ReadAll(io.LimitReader(file, maximumEditBytes+1))
			closeErr := file.Close()
			if err != nil {
				return Output{}, invocationError("execution_failed", "read edit target: %v", err)
			}
			if closeErr != nil {
				return Output{}, invocationError("execution_failed", "close edit target: %v", closeErr)
			}
			if len(b) > maximumEditBytes {
				return Output{}, invocationError("execution_failed", "edit target exceeds %d bytes", maximumEditBytes)
			}
			count := strings.Count(string(b), input.OldString)
			if count == 0 || (count > 1 && !input.ReplaceAll) {
				return Output{}, invocationError("stale_file", "edit match count changed before write")
			}
			limit := 1
			if input.ReplaceAll {
				limit = -1
			}
			updated := strings.Replace(string(b), input.OldString, input.NewString, limit)
			if _, err := atomicWriteRoot(rooted, []byte(updated), true, func(current *os.File) error {
				return tracker.RequireCurrentFile(input.FilePath, current)
			}); err != nil {
				return Output{}, invocationError("execution_failed", "write edit target: %v", err)
			}
			written, err := rooted.parent.Open(rooted.leaf)
			if err != nil {
				return Output{}, invocationError("execution_failed", "open edited file: %v", err)
			}
			defer written.Close()
			if err := tracker.ObserveFile(input.FilePath, written); err != nil {
				return Output{}, invocationError("execution_failed", "record edited file: %v", err)
			}
			return Output{Content: fmt.Sprintf("updated %s (%d replacement(s))", input.FilePath, count)}, nil
		},
	}
}

func workspaceRelative(workspace, path string) (string, error) {
	relative, err := filepath.Rel(workspace, path)
	if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("built-in file capabilities are confined to the workspace")
	}
	return relative, nil
}

type workspaceParent struct {
	parent         *os.Root
	validator      *os.Root
	parentRelative string
	parentInfo     os.FileInfo
	leaf           string
}

func (p *workspaceParent) Close() error {
	return errors.Join(p.parent.Close(), p.validator.Close())
}

func (p *workspaceParent) Verify() error {
	info, err := p.validator.Lstat(p.parentRelative)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || !os.SameFile(info, p.parentInfo) {
		return errors.New("authorized parent directory changed during tool execution")
	}
	return nil
}

// openWorkspaceParent walks and pins each directory without following a
// symlink and rejects filesystem-boundary crossings. The returned parent root
// lets leaf operations stay bound to the inspected directory even if a
// concurrent process renames another directory onto the lexical pathname.
func openWorkspaceParent(workspace, target string, create bool) (*workspaceParent, error) {
	relative, err := workspaceRelative(workspace, target)
	if err != nil {
		return nil, err
	}
	validator, err := os.OpenRoot(workspace)
	if err != nil {
		return nil, err
	}
	current, err := os.OpenRoot(workspace)
	if err != nil {
		_ = validator.Close()
		return nil, err
	}
	fail := func(cause error) (*workspaceParent, error) {
		_ = current.Close()
		_ = validator.Close()
		return nil, cause
	}
	workspaceFile, err := current.Open(".")
	if err != nil {
		return fail(err)
	}
	workspaceInfo, err := workspaceFile.Stat()
	if err != nil {
		_ = workspaceFile.Close()
		return fail(err)
	}
	workspaceDevice, err := openedFileDevice(workspaceFile, workspaceInfo)
	closeErr := workspaceFile.Close()
	if err != nil {
		return fail(err)
	}
	if closeErr != nil {
		return fail(closeErr)
	}
	parentRelative := filepath.Dir(relative)
	if parentRelative != "." {
		for _, component := range strings.Split(parentRelative, string(filepath.Separator)) {
			if component == "" || component == "." {
				continue
			}
			info, inspectErr := current.Lstat(component)
			if errors.Is(inspectErr, os.ErrNotExist) && create {
				if mkdirErr := current.Mkdir(component, 0o755); mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
					return fail(mkdirErr)
				}
				info, inspectErr = current.Lstat(component)
			}
			if inspectErr != nil {
				return fail(inspectErr)
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return fail(fmt.Errorf("workspace path component %q is not a real directory", component))
			}
			next, openErr := current.OpenRoot(component)
			if openErr != nil {
				return fail(openErr)
			}
			openedInfo, statErr := next.Stat(".")
			if statErr != nil || !os.SameFile(info, openedInfo) {
				_ = next.Close()
				if statErr != nil {
					return fail(statErr)
				}
				return fail(errors.New("workspace directory changed while it was opened"))
			}
			directoryFile, openErr := next.Open(".")
			if openErr != nil {
				_ = next.Close()
				return fail(openErr)
			}
			device, deviceErr := openedFileDevice(directoryFile, openedInfo)
			closeErr := directoryFile.Close()
			if deviceErr != nil || closeErr != nil || device != workspaceDevice {
				_ = next.Close()
				if deviceErr != nil {
					return fail(deviceErr)
				}
				if closeErr != nil {
					return fail(closeErr)
				}
				return fail(errors.New("workspace file capability refuses filesystem-boundary crossings"))
			}
			_ = current.Close()
			current = next
		}
	}
	parentInfo, err := current.Stat(".")
	if err != nil {
		return fail(err)
	}
	result := &workspaceParent{parent: current, validator: validator, parentRelative: parentRelative, parentInfo: parentInfo, leaf: filepath.Base(relative)}
	if err := result.Verify(); err != nil {
		_ = result.Close()
		return nil, err
	}
	return result, nil
}

func openRegular(root *os.Root, relative string, maximum int64, enforceMaximum bool) (*os.File, error) {
	info, err := root.Lstat(relative)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("refusing to access a symlink")
	}
	file, err := root.Open(relative)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("path did not resolve to the authorized regular file")
	}
	links, err := openedFileLinkCount(file, opened)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("verify regular-file identity: %w", err)
	}
	if links != 1 {
		_ = file.Close()
		return nil, errors.New("refusing an ambiguous multiply-linked file")
	}
	if enforceMaximum && opened.Size() > maximum {
		_ = file.Close()
		return nil, fmt.Errorf("file exceeds the %d-byte limit", maximum)
	}
	return file, nil
}

func atomicWriteRoot(path *workspaceParent, content []byte, expectExisting bool, verifyExisting func(*os.File) error) (bool, error) {
	root := path.parent
	relative := path.leaf
	mode := os.FileMode(0o644)
	var expected os.FileInfo
	info, inspectErr := root.Lstat(relative)
	switch {
	case !expectExisting && errors.Is(inspectErr, os.ErrNotExist):
	case !expectExisting && inspectErr == nil:
		return false, errors.New("write target appeared after authorization; read it before replacing")
	case !expectExisting:
		return false, inspectErr
	case expectExisting && inspectErr != nil:
		return false, inspectErr
	case info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular():
		return false, errors.New("refusing to replace a non-regular file")
	default:
		mode = info.Mode().Perm()
		current, err := openRegular(root, relative, maximumEditBytes, false)
		if err != nil {
			return false, err
		}
		expected, err = current.Stat()
		if err == nil && verifyExisting != nil {
			err = verifyExisting(current)
		}
		closeErr := current.Close()
		if err != nil || closeErr != nil {
			return false, errors.Join(err, closeErr)
		}
	}

	temporaryName, temporary, temporaryInfo, err := createRootTemporary(root, ".agentx-write-", mode, content)
	if err != nil {
		return false, err
	}
	temporaryPresent := true
	defer func() {
		if temporaryPresent {
			_ = removeRootIfSame(root, temporaryName, temporaryInfo)
		}
	}()
	if err := temporary.Close(); err != nil {
		return false, err
	}
	if err := path.Verify(); err != nil {
		return false, err
	}

	if !expectExisting {
		if _, err := root.Lstat(relative); !errors.Is(err, os.ErrNotExist) {
			if err == nil {
				return false, errors.New("write target appeared before commit; it was not replaced")
			}
			return false, err
		}
		if err := root.Link(temporaryName, relative); err != nil {
			return false, fmt.Errorf("activate new file without replacement: %w", err)
		}
		if err := verifyRootIdentity(root, relative, temporaryInfo, 2); err != nil {
			return false, err
		}
		if err := removeRootIfSame(root, temporaryName, temporaryInfo); err != nil {
			return false, err
		}
		temporaryPresent = false
		if err := verifyRootIdentity(root, relative, temporaryInfo, 1); err != nil {
			return false, err
		}
		if err := syncWorkspaceRoot(path); err != nil {
			return false, err
		}
		return true, nil
	}

	// Move the current pathname into a private, reserved backup slot before
	// activating the new inode. This converts a racy replace into two no-clobber
	// operations: if another actor substitutes the target, that object is
	// verified and restored rather than overwritten.
	backupName, placeholder, placeholderInfo, err := createRootTemporary(root, ".agentx-backup-", 0o600, nil)
	if err != nil {
		return false, err
	}
	if err := placeholder.Close(); err != nil {
		return false, err
	}
	backupState := placeholderInfo
	backupPresent := true
	defer func() {
		if backupPresent && os.SameFile(backupState, placeholderInfo) {
			_ = removeRootIfSame(root, backupName, placeholderInfo)
		}
	}()
	currentInfo, err := root.Lstat(relative)
	if err != nil || !os.SameFile(currentInfo, expected) {
		if err == nil {
			err = errors.New("write target identity changed before quarantine")
		}
		return false, err
	}
	if err := renameIntoReservedRoot(root, relative, backupName, placeholderInfo); err != nil {
		return false, fmt.Errorf("quarantine existing target: %w", err)
	}
	backupState, err = root.Lstat(backupName)
	if err != nil {
		return false, err
	}
	restore := func(cause error) error {
		restoreErr := restoreRootBackup(root, backupName, relative, backupState)
		if restoreErr == nil {
			backupPresent = false
		}
		return errors.Join(cause, restoreErr)
	}
	if !backupState.Mode().IsRegular() || !os.SameFile(backupState, expected) {
		return false, restore(errors.New("write target was substituted before commit; the substituted object was not overwritten"))
	}
	backup, err := root.Open(backupName)
	if err != nil {
		return false, restore(err)
	}
	openedBackup, verifyErr := backup.Stat()
	if verifyErr == nil && (!os.SameFile(openedBackup, expected) || !openedBackup.Mode().IsRegular()) {
		verifyErr = errors.New("quarantined target identity is not the authorized file")
	}
	if verifyErr == nil {
		links, linkErr := openedFileLinkCount(backup, openedBackup)
		if linkErr != nil || links != 1 {
			verifyErr = errors.Join(errors.New("quarantined target has an ambiguous link identity"), linkErr)
		}
	}
	if verifyErr == nil && verifyExisting != nil {
		verifyErr = verifyExisting(backup)
	}
	closeErr := backup.Close()
	if verifyErr != nil || closeErr != nil {
		return false, restore(errors.Join(verifyErr, closeErr))
	}
	if _, err := root.Lstat(relative); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			err = errors.New("write target reappeared during commit; it was not overwritten")
		}
		return false, restore(err)
	}
	if err := root.Link(temporaryName, relative); err != nil {
		return false, restore(fmt.Errorf("activate replacement without clobber: %w", err))
	}
	if err := verifyRootIdentity(root, relative, temporaryInfo, 2); err != nil {
		return false, restore(err)
	}
	if err := removeRootIfSame(root, temporaryName, temporaryInfo); err != nil {
		return false, err
	}
	temporaryPresent = false
	if err := verifyRootIdentity(root, relative, temporaryInfo, 1); err != nil {
		return false, err
	}
	if err := removeRootIfSame(root, backupName, backupState); err != nil {
		return false, err
	}
	backupPresent = false
	if err := syncWorkspaceRoot(path); err != nil {
		return false, err
	}
	return false, nil
}

func createRootTemporary(root *os.Root, prefix string, mode os.FileMode, content []byte) (string, *os.File, os.FileInfo, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", nil, nil, err
	}
	name := prefix + hex.EncodeToString(nonce[:])
	file, err := root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return "", nil, nil, err
	}
	fail := func(cause error) (string, *os.File, os.FileInfo, error) {
		info, _ := file.Stat()
		_ = file.Close()
		if info != nil {
			_ = removeRootIfSame(root, name, info)
		}
		return "", nil, nil, cause
	}
	if err := file.Chmod(mode); err != nil {
		return fail(err)
	}
	if err := writeAllFile(file, content); err != nil {
		return fail(err)
	}
	if err := file.Sync(); err != nil {
		return fail(err)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != int64(len(content)) {
		if err == nil {
			err = errors.New("temporary file identity changed while writing")
		}
		return fail(err)
	}
	links, err := openedFileLinkCount(file, info)
	if err != nil || links != 1 {
		return fail(errors.Join(errors.New("temporary file has an ambiguous link identity"), err))
	}
	return name, file, info, nil
}

func writeAllFile(file *os.File, content []byte) error {
	for len(content) > 0 {
		written, err := file.Write(content)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		content = content[written:]
	}
	return nil
}

func verifyRootIdentity(root *os.Root, name string, expected os.FileInfo, expectedLinks uint64) error {
	pathInfo, err := root.Lstat(name)
	if err != nil || !pathInfo.Mode().IsRegular() || !os.SameFile(pathInfo, expected) {
		return errors.New("activated file identity changed")
	}
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(opened, expected) {
		return errors.New("activated file changed while opening")
	}
	links, err := openedFileLinkCount(file, opened)
	if err != nil || links != expectedLinks {
		return errors.Join(errors.New("activated file link identity is incoherent"), err)
	}
	return nil
}

func removeRootIfSame(root *os.Root, name string, expected os.FileInfo) error {
	current, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !os.SameFile(current, expected) {
		return errors.New("temporary path identity changed before cleanup")
	}
	return root.Remove(name)
}

func restoreRootBackup(root *os.Root, backupName, targetName string, backupInfo os.FileInfo) error {
	if _, err := root.Lstat(targetName); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return errors.New("target was occupied while restoring quarantined file; backup was retained as " + backupName)
		}
		return err
	}
	if err := root.Link(backupName, targetName); err != nil {
		return fmt.Errorf("restore quarantined file from %s: %w", backupName, err)
	}
	if err := removeRootIfSame(root, backupName, backupInfo); err != nil {
		return fmt.Errorf("remove restored quarantine link %s: %w", backupName, err)
	}
	return nil
}

func syncWorkspaceRoot(path *workspaceParent) error {
	if err := path.Verify(); err != nil {
		return err
	}
	directory, err := path.parent.Open(".")
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if err := path.Verify(); err != nil {
		return errors.Join(syncErr, closeErr, err)
	}
	return errors.Join(syncErr, closeErr)
}
