package permission

const (
	maximumPermissionRules          = 4_096
	maximumPermissionEditCycles     = 16
	maximumPermissionProjectionItem = 4_096
	maximumPermissionInputBytes     = 16 << 20
	maximumPermissionAggregateBytes = 16 << 20
	maximumPermissionTextBytes      = 1 << 20
	maximumPermissionIdentifier     = 4 << 10
	maximumPermissionRuleBytes      = 64 << 10
	maximumPermissionSourceBytes    = 4 << 10
	maximumShellCommandBytes        = 1 << 20
	maximumShellWords               = 4_096
	maximumShellRedirections        = 64
)

// validPermissionRequest bounds the complete, already-validated projection
// before caller-owned slices are cloned or callback-owned data is retained.
// It deliberately does not reinterpret schema semantics owned by the tool.
func validPermissionRequest(request Request) bool {
	if len(request.Tool) > maximumPermissionIdentifier ||
		len(request.ToolUseID) > maximumPermissionIdentifier ||
		len(request.Input) > maximumPermissionInputBytes {
		return false
	}
	total := 0
	add := func(value string) bool {
		if len(value) > maximumPermissionTextBytes ||
			total > maximumPermissionAggregateBytes-len(value) {
			return false
		}
		total += len(value)
		return true
	}
	if !add(request.Content) || !add(request.MandatoryAsk) ||
		!add(request.HookAsk) || !add(request.HardDeny) {
		return false
	}
	itemCount := len(request.MatchContents) + len(request.DenyContents) +
		len(request.AllowContents) + len(request.Paths)
	if itemCount > maximumPermissionProjectionItem {
		return false
	}
	for _, values := range [][]string{
		request.MatchContents, request.DenyContents, request.AllowContents,
	} {
		for _, value := range values {
			if !add(value) {
				return false
			}
		}
	}
	for _, access := range request.Paths {
		if (access.Operation != PathRead && access.Operation != PathWrite) || !add(access.Path) {
			return false
		}
	}
	if request.Shell == nil {
		return true
	}
	shell := request.Shell
	if len(shell.Command) > maximumShellCommandBytes ||
		len(shell.Segments) > maxShellSegments ||
		len(shell.DenyCandidates)+len(shell.AllowCandidates)+len(shell.Paths)+len(shell.RemovalTargets) >
			maximumPermissionProjectionItem ||
		!add(shell.Command) || !add(shell.ReviewReason) || !add(shell.DangerReason) {
		return false
	}
	for _, values := range [][]string{
		shell.Segments, shell.DenyCandidates, shell.AllowCandidates, shell.RemovalTargets,
	} {
		for _, value := range values {
			if !add(value) {
				return false
			}
		}
	}
	for _, access := range shell.Paths {
		if (access.Operation != PathRead && access.Operation != PathWrite) || !add(access.Path) {
			return false
		}
	}
	return true
}
