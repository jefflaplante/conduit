package brain

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"conduit/internal/database"
)

// GraphNode is a single LTM entry rendered for the graph dashboard.
// It is a flatter projection of Entry: no working/scratch tier, no stale flag,
// and Value may be truncated by GraphOptions.ValueTruncate.
type GraphNode struct {
	Key         string    `json:"key"`
	Value       string    `json:"value"`
	Source      string    `json:"source,omitempty"`
	Salience    float64   `json:"salience"`
	Warmth      float64   `json:"warmth,omitempty"`
	AccessCount int       `json:"access_count"`
	CreatedAt   time.Time `json:"created_at"`
	Truncated   bool      `json:"truncated,omitempty"`
}

// GraphEdge is a single relationship between two LTM keys.
type GraphEdge struct {
	KeyA            string     `json:"key_a"`
	KeyB            string     `json:"key_b"`
	Relationship    string     `json:"relationship"`
	Confidence      float64    `json:"confidence"`
	LastTraversedAt *time.Time `json:"last_traversed_at,omitempty"`
}

// Graph is the full node+edge payload returned by Brain.ListGraph.
type Graph struct {
	Nodes []*GraphNode `json:"nodes"`
	Edges []*GraphEdge `json:"edges"`
}

// GraphOptions filters and shapes the graph returned by ListGraph.
type GraphOptions struct {
	SourcePrefix  string  // only nodes whose source starts with this prefix
	MinSalience   float64 // only nodes with salience >= this
	MinConfidence float64 // only edges with confidence >= this
	ValueTruncate int     // bytes; 0 = full value
	NodeLimit     int     // hard cap; 0 = no cap
}

// ListGraph returns the LTM graph (nodes + edges). Edges referencing keys not
// present in the returned node set are stripped. Expired entries are excluded.
func (b *Brain) ListGraph(ctx context.Context, opts GraphOptions) (*Graph, error) {
	g := &Graph{Nodes: []*GraphNode{}, Edges: []*GraphEdge{}}

	nodeQuery := `SELECT key, value, source, salience, warmth, access_count, created_at
		FROM brain_ltm
		WHERE (expires_at IS NULL OR expires_at > strftime('%Y-%m-%d %H:%M:%f', 'now'))
		  AND salience >= ?`
	args := []interface{}{opts.MinSalience}
	if opts.SourcePrefix != "" {
		nodeQuery += " AND source LIKE ?"
		args = append(args, opts.SourcePrefix+"%")
	}
	nodeQuery += " ORDER BY salience DESC"
	if opts.NodeLimit > 0 {
		nodeQuery += " LIMIT ?"
		args = append(args, opts.NodeLimit)
	}

	keys := make(map[string]struct{})
	err := database.RetryOnBusy(5, func() error {
		rows, err := b.db.QueryContext(ctx, nodeQuery, args...)
		if err != nil {
			return fmt.Errorf("query nodes: %w", err)
		}
		defer rows.Close()
		nodes := make([]*GraphNode, 0, 256)
		ks := make(map[string]struct{}, 256)
		for rows.Next() {
			n := &GraphNode{}
			if err := rows.Scan(&n.Key, &n.Value, &n.Source, &n.Salience, &n.Warmth, &n.AccessCount, &n.CreatedAt); err != nil {
				return fmt.Errorf("scan node: %w", err)
			}
			if opts.ValueTruncate > 0 && len(n.Value) > opts.ValueTruncate {
				n.Value = n.Value[:opts.ValueTruncate]
				n.Truncated = true
			}
			nodes = append(nodes, n)
			ks[n.Key] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate nodes: %w", err)
		}
		g.Nodes = nodes
		keys = ks
		return nil
	})
	if err != nil {
		return nil, err
	}

	if len(keys) == 0 {
		return g, nil
	}

	edgeQuery := `SELECT key_a, key_b, relationship, confidence, last_traversed_at
		FROM brain_relationships
		WHERE confidence >= ?`
	err = database.RetryOnBusy(5, func() error {
		rows, err := b.db.QueryContext(ctx, edgeQuery, opts.MinConfidence)
		if err != nil {
			return fmt.Errorf("query edges: %w", err)
		}
		defer rows.Close()
		edges := make([]*GraphEdge, 0, 64)
		for rows.Next() {
			e := &GraphEdge{}
			var lt sql.NullTime
			var rel sql.NullString
			if err := rows.Scan(&e.KeyA, &e.KeyB, &rel, &e.Confidence, &lt); err != nil {
				return fmt.Errorf("scan edge: %w", err)
			}
			if rel.Valid {
				e.Relationship = rel.String
			}
			if e.Relationship == "" {
				e.Relationship = "related"
			}
			if _, ok := keys[e.KeyA]; !ok {
				continue
			}
			if _, ok := keys[e.KeyB]; !ok {
				continue
			}
			if lt.Valid {
				t := lt.Time
				e.LastTraversedAt = &t
			}
			edges = append(edges, e)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate edges: %w", err)
		}
		g.Edges = edges
		return nil
	})
	if err != nil {
		return nil, err
	}

	return g, nil
}
