//go:build windows

package config

import "os"

const envFilePOSIXPermissionsEnforced = false

// Windows os.FileMode permission bits are synthesized from file attributes;
// they do not describe the file's owner or DACL. Until the Windows adapter can
// prove owner-only access through native ACL inspection, credential-file use
// fails closed instead of treating portable mode bits as security evidence.
func envFileModePermitsCredentialUse(os.FileMode) bool { return false }

func envFileOwnerPermitsCredentialUse(os.FileInfo) bool { return false }
