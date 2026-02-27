package backup

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
)

// ListBackup inspects a backup archive and returns its contents.
func ListBackup(opts ListOptions) (*ListResult, error) {
	f, err := os.Open(opts.BackupPath)
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("open gzip: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)

	result := &ListResult{}
	var manifestFound bool

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tar entry: %w", err)
		}

		if hdr.Name == "manifest.json" {
			data, err := io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("read manifest: %w", err)
			}
			m, err := UnmarshalManifest(data)
			if err != nil {
				return nil, fmt.Errorf("parse manifest: %w", err)
			}
			result.Manifest = *m
			manifestFound = true
		}

		result.Files = append(result.Files, FileEntry{
			Path: hdr.Name,
			Size: hdr.Size,
			Mode: fmt.Sprintf("%04o", hdr.Mode),
		})
	}

	if !manifestFound {
		return nil, fmt.Errorf("manifest.json not found in archive")
	}

	return result, nil
}

// PrintListResult outputs the listing in human-readable or JSON format.
func PrintListResult(result *ListResult, opts ListOptions) error {
	if opts.JSONOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}

	m := result.Manifest
	fmt.Printf("Backup: %s\n", opts.BackupPath)
	fmt.Printf("Created: %s\n", m.Timestamp.Format("2006-01-02 15:04:05 UTC"))
	fmt.Printf("Gateway version: %s\n", m.GatewayVersion)
	fmt.Printf("Components: %s\n", m.Components)
	fmt.Printf("Database size: %s\n", formatBytes(m.DatabaseInfo.Size))
	fmt.Printf("Tables: %d\n", m.DatabaseInfo.TableCount)
	fmt.Printf("Files: %d\n", len(result.Files))

	if opts.Verbose {
		fmt.Println()
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "MODE\tSIZE\tPATH")
		fmt.Fprintln(w, "----\t----\t----")
		for _, f := range result.Files {
			fmt.Fprintf(w, "%s\t%s\t%s\n", f.Mode, formatBytes(f.Size), f.Path)
		}
		w.Flush()
	}

	return nil
}

func formatBytes(b int64) string {
	switch {
	case b >= 1024*1024*1024:
		return fmt.Sprintf("%.1f GB", float64(b)/(1024*1024*1024))
	case b >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(b)/(1024*1024))
	case b >= 1024:
		return fmt.Sprintf("%.1f KB", float64(b)/1024)
	default:
		return fmt.Sprintf("%d B", b)
	}
}
