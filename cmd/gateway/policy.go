package main

import (
	"encoding/json"
	"fmt"
	"io"

	"text/tabwriter"
	"time"

	"conduit/internal/datadir"
	"conduit/internal/policy"

	"github.com/spf13/cobra"
)

// PolicyRootCmd is `conduit policy` (conduit-25lt.2).
func PolicyRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "policy",
		Short: "Inspect tool action policy decisions",
	}
	cmd.AddCommand(policyReportCmd())
	return cmd
}

func policyReportCmd() *cobra.Command {
	var (
		since   time.Duration
		limit   int
		asJSON  bool
		logPath string
	)
	cmd := &cobra.Command{
		Use:   "report",
		Short: "Summarise recorded tool policy decisions",
		Long: `Summarise the tool action policy's recorded decisions: counts per action
class and decision, then every call the policy would have stopped (deny) or
asked about (ask), newest first. In shadow mode nothing was blocked; this is
how to review the shadow week before enforcement.

Reads <data_dir>/logs/policy-decisions.jsonl (and its rotated .1 file).`,
		Example: `  conduit policy report
  conduit policy report --since 168h
  conduit policy report --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if logPath == "" {
				dataDir := ""
				if cfg := loadConfigIfPresent(cfgFile); cfg != nil {
					dataDir = cfg.DataDir
				}
				dd, err := datadir.New(dataDir)
				if err != nil {
					return fmt.Errorf("resolve data dir: %w", err)
				}
				logPath = policy.DecisionLogPath(dd.Root())
			}
			from := time.Now().Add(-since)
			recs, err := policy.ReadRecords(logPath, from)
			if err != nil {
				return fmt.Errorf("read decision log: %w", err)
			}
			return writePolicyReport(cmd.OutOrStdout(), logPath, from, policy.Summarize(recs), limit, asJSON)
		},
	}
	cmd.Flags().DurationVar(&since, "since", 24*time.Hour, "How far back to report (e.g. 24h, 168h)")
	cmd.Flags().IntVar(&limit, "limit", 50, "Maximum flagged calls to list (0 = all)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	cmd.Flags().StringVar(&logPath, "log", "", "Decision log path (default: <data_dir>/logs/policy-decisions.jsonl)")
	return cmd
}

func writePolicyReport(w io.Writer, path string, from time.Time, s policy.Summary, limit int, asJSON bool) error {
	flagged := s.Flagged
	// newest first
	for i, j := 0, len(flagged)-1; i < j; i, j = i+1, j-1 {
		flagged[i], flagged[j] = flagged[j], flagged[i]
	}
	if limit > 0 && len(flagged) > limit {
		flagged = flagged[:limit]
	}
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]interface{}{
			"log": path, "since": from.UTC(), "total": s.Total, "counts": s.Counts, "flagged": flagged,
		})
	}

	fmt.Fprintf(w, "Tool policy decisions since %s: %d\nLog: %s\n", from.UTC().Format("2006-01-02 15:04 MST"), s.Total, path)
	if s.Total == 0 {
		fmt.Fprintln(w, "\nNo decisions recorded in this period.")
		return nil
	}
	type row struct{ allow, ask, deny int }
	rows := map[string]*row{}
	var order []string
	for _, c := range s.Counts {
		r, ok := rows[c.Class]
		if !ok {
			r = &row{}
			rows[c.Class] = r
			order = append(order, c.Class)
		}
		switch c.Decision {
		case policy.Allow:
			r.allow += c.Count
		case policy.Ask:
			r.ask += c.Count
		case policy.Deny:
			r.deny += c.Count
		}
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "CLASS\tALLOW\tASK\tDENY")
	for _, c := range order {
		r := rows[c]
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\n", c, r.allow, r.ask, r.deny)
	}
	tw.Flush()

	if len(s.Flagged) == 0 {
		fmt.Fprintln(w, "\nNothing would have been stopped or asked about.")
		return nil
	}
	fmt.Fprintf(w, "\nWould have been stopped (deny) or asked about (ask), newest first (%d of %d):\n", len(flagged), len(s.Flagged))
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TIME (UTC)\tDECISION\tCLASS\tTARGET\tTOOL\tORIGIN\tRULE\tPURPOSE")
	for _, r := range flagged {
		purpose := r.Purpose
		if purpose == "" {
			purpose = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.Time.UTC().Format("01-02 15:04"), r.Decision, r.Class,
			r.Target, r.Tool, r.Origin, r.Rule, purpose)
	}
	tw.Flush()
	fmt.Fprintln(w, "\nShadow mode records only; none of these calls were blocked.")
	return nil
}
