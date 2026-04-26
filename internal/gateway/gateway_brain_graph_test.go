package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"conduit/internal/brain"
	"conduit/internal/config"
)

func newBrainForTest(t *testing.T) *brain.Brain {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "brain.db")
	b, err := brain.New(dbPath, brain.WithAutoFlushInterval(0))
	require.NoError(t, err)
	t.Cleanup(func() { b.Close() })
	return b
}

func newGraphTestGateway(t *testing.T, dashboardEnabled bool, b *brain.Brain) *Gateway {
	t.Helper()
	cfg := &config.Config{}
	cfg.Brain.DashboardEnabled = dashboardEnabled
	return &Gateway{config: cfg, brainService: b}
}

func TestHandleBrainGraph_DashboardDisabled(t *testing.T) {
	gw := newGraphTestGateway(t, false, newBrainForTest(t))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/brain/graph", nil)

	gw.handleBrainGraph(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	var resp map[string]string
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Contains(t, resp["error"], "not enabled")
}

func TestHandleBrainGraph_BrainNil(t *testing.T) {
	gw := newGraphTestGateway(t, true, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/brain/graph", nil)

	gw.handleBrainGraph(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	var resp map[string]string
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Contains(t, resp["error"], "disabled")
}

func TestHandleBrainGraph_MethodNotAllowed(t *testing.T) {
	gw := newGraphTestGateway(t, true, newBrainForTest(t))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/brain/graph", nil)

	gw.handleBrainGraph(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestHandleBrainGraph_EmptyHappyPath(t *testing.T) {
	gw := newGraphTestGateway(t, true, newBrainForTest(t))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/brain/graph", nil)

	gw.handleBrainGraph(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		Nodes  []interface{}  `json:"nodes"`
		Edges  []interface{}  `json:"edges"`
		Counts map[string]int `json:"counts"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, 0, resp.Counts["nodes"])
	assert.Equal(t, 0, resp.Counts["edges"])
}

func TestHandleBrainGraph_PopulatedHappyPath(t *testing.T) {
	b := newBrainForTest(t)
	ctx := brain.WithUserID(t.Context(), "user1")
	require.NoError(t, b.Store(ctx, "solar.battery.config", "abc", brain.TierLongTerm, "file:solar.md"))
	require.NoError(t, b.Store(ctx, "solar.battery.plan", "def", brain.TierLongTerm, "file:solar.md"))
	_, err := b.DB().Exec(`INSERT INTO brain_relationships (key_a, key_b, relationship, confidence) VALUES (?,?,?,?)`,
		"solar.battery.config", "solar.battery.plan", "namespace", 0.9)
	require.NoError(t, err)

	gw := newGraphTestGateway(t, true, b)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/brain/graph?min_confidence=0.5", nil)

	gw.handleBrainGraph(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		Nodes []struct {
			Key string `json:"key"`
		} `json:"nodes"`
		Edges []struct {
			KeyA         string  `json:"key_a"`
			KeyB         string  `json:"key_b"`
			Relationship string  `json:"relationship"`
			Confidence   float64 `json:"confidence"`
		} `json:"edges"`
		Counts map[string]int `json:"counts"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, 2, resp.Counts["nodes"])
	assert.Equal(t, 1, resp.Counts["edges"])
	assert.Equal(t, "namespace", resp.Edges[0].Relationship)
}

func TestHandleBrainGraph_ConfidenceFilterStripsEdge(t *testing.T) {
	b := newBrainForTest(t)
	ctx := brain.WithUserID(t.Context(), "user1")
	require.NoError(t, b.Store(ctx, "k1", "v1", brain.TierLongTerm, ""))
	require.NoError(t, b.Store(ctx, "k2", "v2", brain.TierLongTerm, ""))
	_, err := b.DB().Exec(`INSERT INTO brain_relationships (key_a, key_b, relationship, confidence) VALUES (?,?,?,?)`,
		"k1", "k2", "related", 0.3)
	require.NoError(t, err)

	gw := newGraphTestGateway(t, true, b)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/brain/graph?min_confidence=0.8", nil)

	gw.handleBrainGraph(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		Counts map[string]int `json:"counts"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, 2, resp.Counts["nodes"])
	assert.Equal(t, 0, resp.Counts["edges"])
}
