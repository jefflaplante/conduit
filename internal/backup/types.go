package backup

import "time"

// BackupComponents is a bitmask of which components are included in a backup.
type BackupComponents uint32

const (
	ComponentDatabase BackupComponents = 1 << iota
	ComponentConfig
	ComponentWorkspace
	ComponentSSHKeys
	ComponentSkills
)

func (c BackupComponents) Has(flag BackupComponents) bool {
	return c&flag != 0
}

func (c BackupComponents) String() string {
	var parts []string
	if c.Has(ComponentDatabase) {
		parts = append(parts, "database")
	}
	if c.Has(ComponentConfig) {
		parts = append(parts, "config")
	}
	if c.Has(ComponentWorkspace) {
		parts = append(parts, "workspace")
	}
	if c.Has(ComponentSSHKeys) {
		parts = append(parts, "ssh_keys")
	}
	if c.Has(ComponentSkills) {
		parts = append(parts, "skills")
	}
	if len(parts) == 0 {
		return "none"
	}
	result := parts[0]
	for _, p := range parts[1:] {
		result += ", " + p
	}
	return result
}

// BackupManifest describes the contents and origin of a backup archive.
type BackupManifest struct {
	Version        string           `json:"version"`
	Timestamp      time.Time        `json:"timestamp"`
	GatewayVersion string           `json:"gateway_version"`
	Components     BackupComponents `json:"components"`
	OriginalPaths  OriginalPaths    `json:"original_paths"`
	DatabaseInfo   DatabaseInfo     `json:"database_info"`
}

// OriginalPaths records where files were located on the source system.
type OriginalPaths struct {
	Config       string   `json:"config"`
	Database     string   `json:"database"`
	WorkspaceDir string   `json:"workspace_dir"`
	SSHHostKey   string   `json:"ssh_host_key,omitempty"`
	SSHAuthKeys  string   `json:"ssh_authorized_keys,omitempty"`
	SkillsPaths  []string `json:"skills_paths,omitempty"`
}

// DatabaseInfo records basic database metadata.
type DatabaseInfo struct {
	Size       int64 `json:"size"`
	TableCount int   `json:"table_count"`
}

// BackupOptions configures backup creation.
type BackupOptions struct {
	ConfigPath     string
	OutputPath     string
	IncludeSSHKeys bool
	IncludeSkills  bool
	Verbose        bool
}

// RestoreOptions configures backup restoration.
type RestoreOptions struct {
	BackupPath     string
	DryRun         bool
	Force          bool
	SkipConfig     bool
	RestoreSSHKeys bool
	ConfigPath     string
	DatabasePath   string
	WorkspacePath  string
	Verbose        bool
}

// ListOptions configures backup inspection.
type ListOptions struct {
	BackupPath string
	JSONOutput bool
	Verbose    bool
}

// BackupResult is returned by CreateBackup.
type BackupResult struct {
	ArchivePath string           `json:"archive_path"`
	FileCount   int              `json:"file_count"`
	TotalSize   int64            `json:"total_size"`
	Components  BackupComponents `json:"components"`
	Duration    time.Duration    `json:"duration"`
	Warnings    []string         `json:"warnings,omitempty"`
}

// RestoreResult is returned by RestoreBackup.
type RestoreResult struct {
	FilesRestored int              `json:"files_restored"`
	FilesSkipped  int              `json:"files_skipped"`
	Components    BackupComponents `json:"components"`
	Warnings      []string         `json:"warnings,omitempty"`
}

// ListResult is returned by ListBackup.
type ListResult struct {
	Manifest BackupManifest `json:"manifest"`
	Files    []FileEntry    `json:"files"`
}

// FileEntry describes a single file in the backup archive.
type FileEntry struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	Mode string `json:"mode"`
}
