package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"conduit/internal/brain"
)

// mockBrainLister implements BrainLister for testing.
type mockBrainLister struct {
	entries map[string][]*brain.Entry // prefix -> entries
}

func newMockBrainLister() *mockBrainLister {
	return &mockBrainLister{
		entries: make(map[string][]*brain.Entry),
	}
}

func (m *mockBrainLister) addEntry(key, value string, salience float64) {
	// Determine which prefix bucket this goes into.
	for _, prefix := range []string{
		"reflect.patterns.",
		"reflect.learned.",
		"reflect.clusters.",
		"sense.tasks.",
		"sense.alerts.",
		"sense.briefing.",
	} {
		if strings.HasPrefix(key, prefix) {
			m.entries[prefix] = append(m.entries[prefix], &brain.Entry{
				Key:         key,
				Value:       value,
				Tier:        brain.TierLongTerm,
				CreatedAt:   time.Now(),
				AccessedAt:  time.Now(),
				AccessCount: 1,
				Salience:    salience,
			})
			return
		}
	}
}

func (m *mockBrainLister) List(ctx context.Context, prefix string, sourcePrefix string) ([]*brain.Entry, error) {
	entries, ok := m.entries[prefix]
	if !ok {
		return nil, nil
	}
	// Filter by sourcePrefix if given.
	if sourcePrefix != "" {
		var filtered []*brain.Entry
		for _, e := range entries {
			if strings.HasPrefix(e.Source, sourcePrefix) {
				filtered = append(filtered, e)
			}
		}
		return filtered, nil
	}
	return entries, nil
}

func TestBuildSituationAwareness_NoBrain(t *testing.T) {
	params := &SectionParams{IsMinimal: false}
	pb := &PromptBuilder{brainService: nil}

	result := pb.buildSituationAwareness(context.Background(), params)
	if result != "" {
		t.Errorf("expected empty result with nil brain, got: %s", result)
	}
}

func TestBuildSituationAwareness_MinimalMode(t *testing.T) {
	params := &SectionParams{IsMinimal: true}
	mock := newMockBrainLister()
	mock.addEntry("reflect.patterns.test", "some pattern", 0.9)
	pb := &PromptBuilder{brainService: mock}

	result := pb.buildSituationAwareness(context.Background(), params)
	if result != "" {
		t.Errorf("expected empty result in minimal mode, got: %s", result)
	}
}

func TestBuildSituationAwareness_EmptyBrain(t *testing.T) {
	params := &SectionParams{IsMinimal: false}
	mock := newMockBrainLister()
	pb := &PromptBuilder{brainService: mock}

	result := pb.buildSituationAwareness(context.Background(), params)
	if result != "" {
		t.Errorf("expected empty result with empty brain, got: %s", result)
	}
}

func TestBuildSituationAwareness_WithConfirmedPatterns(t *testing.T) {
	params := &SectionParams{IsMinimal: false}
	mock := newMockBrainLister()
	mock.addEntry("reflect.patterns.api_pagination", "Brave API returns paginated results; always check for next_page", 0.9)
	mock.addEntry("reflect.patterns.bash_timeout", "Long-running bash commands should use timeout flag", 0.7)
	pb := &PromptBuilder{brainService: mock}

	result := pb.buildSituationAwareness(context.Background(), params)

	if !strings.Contains(result, "## Situation Awareness") {
		t.Error("expected Situation Awareness header")
	}
	if !strings.Contains(result, "### Confirmed Patterns") {
		t.Error("expected Confirmed Patterns subsection")
	}
	if !strings.Contains(result, "Brave API returns paginated") {
		t.Error("expected pattern content")
	}
	if !strings.Contains(result, "Long-running bash commands") {
		t.Error("expected second pattern")
	}
}

func TestBuildSituationAwareness_WithMultipleCategories(t *testing.T) {
	params := &SectionParams{IsMinimal: false}
	mock := newMockBrainLister()

	mock.addEntry("reflect.patterns.retry_logic", "Retry with backoff on transient errors", 0.8)
	mock.addEntry("sense.tasks.active", "3 open tasks: fix auth bug, update docs, review PR", 0.6)
	mock.addEntry("sense.alerts.disk_warning", "Disk usage at 85% on /data", 0.7)
	mock.addEntry("reflect.learned.git.stash", "git stash pop can fail silently if there are conflicts", 0.5)

	pb := &PromptBuilder{brainService: mock}
	result := pb.buildSituationAwareness(context.Background(), params)

	// All categories should be present.
	for _, header := range []string{"Confirmed Patterns", "Active Work", "Recent Alerts", "Learned Patterns"} {
		if !strings.Contains(result, header) {
			t.Errorf("expected %q subsection in output", header)
		}
	}
}

func TestBuildSituationAwareness_PriorityOrder(t *testing.T) {
	params := &SectionParams{IsMinimal: false}
	mock := newMockBrainLister()

	mock.addEntry("reflect.clusters.cluster1", "Cluster of web_fetch failures", 0.3)
	mock.addEntry("reflect.patterns.confirmed1", "Confirmed pattern", 0.9)

	pb := &PromptBuilder{brainService: mock}
	result := pb.buildSituationAwareness(context.Background(), params)

	// Confirmed Patterns (priority 1) should appear before Pattern Clusters (priority 5).
	patternsIdx := strings.Index(result, "### Confirmed Patterns")
	clustersIdx := strings.Index(result, "### Pattern Clusters")
	if patternsIdx == -1 || clustersIdx == -1 {
		t.Fatal("expected both sections to be present")
	}
	if patternsIdx > clustersIdx {
		t.Error("Confirmed Patterns should appear before Pattern Clusters")
	}
}

func TestBuildSituationAwareness_SalienceOrdering(t *testing.T) {
	params := &SectionParams{IsMinimal: false}
	mock := newMockBrainLister()

	// Add entries with different salience; expect highest first.
	mock.addEntry("reflect.patterns.low", "Low salience pattern", 0.2)
	mock.addEntry("reflect.patterns.high", "High salience pattern", 0.9)
	mock.addEntry("reflect.patterns.mid", "Mid salience pattern", 0.5)

	pb := &PromptBuilder{brainService: mock}
	result := pb.buildSituationAwareness(context.Background(), params)

	highIdx := strings.Index(result, "High salience pattern")
	midIdx := strings.Index(result, "Mid salience pattern")
	lowIdx := strings.Index(result, "Low salience pattern")

	if highIdx == -1 || midIdx == -1 || lowIdx == -1 {
		t.Fatal("expected all three patterns in output")
	}
	if highIdx > midIdx || midIdx > lowIdx {
		t.Errorf("entries should be ordered by salience: high(%d) < mid(%d) < low(%d)", highIdx, midIdx, lowIdx)
	}
}

func TestBuildSituationAwareness_TokenBudgetTruncation(t *testing.T) {
	params := &SectionParams{IsMinimal: false}
	mock := newMockBrainLister()

	// Add many entries to exceed the 2000-char budget.
	for i := 0; i < 50; i++ {
		mock.addEntry(
			fmt.Sprintf("reflect.learned.item_%02d", i),
			fmt.Sprintf("This is learned pattern number %d with some extra text to take up space in the budget", i),
			float64(50-i)/50.0,
		)
	}

	// Also add a confirmed pattern (higher priority).
	mock.addEntry("reflect.patterns.important", "This critical pattern should always appear", 0.95)

	pb := &PromptBuilder{brainService: mock}
	result := pb.buildSituationAwareness(context.Background(), params)

	// The confirmed pattern (priority 1) should be present.
	if !strings.Contains(result, "This critical pattern should always appear") {
		t.Error("high-priority confirmed pattern should survive budget truncation")
	}

	// Result should be roughly within budget (2000 chars + some tolerance for headers).
	if len(result) > 2500 {
		t.Errorf("result (%d chars) exceeds budget tolerance (2500 chars)", len(result))
	}
}

func TestBuildSituationAwareness_SkipsEmptyCategories(t *testing.T) {
	params := &SectionParams{IsMinimal: false}
	mock := newMockBrainLister()

	// Only add entries for one category.
	mock.addEntry("sense.tasks.active", "Deploy v2.1", 0.6)

	pb := &PromptBuilder{brainService: mock}
	result := pb.buildSituationAwareness(context.Background(), params)

	// Should show Active Work but NOT show empty category headers.
	if !strings.Contains(result, "### Active Work") {
		t.Error("expected Active Work section")
	}
	for _, empty := range []string{"### Confirmed Patterns", "### Learned Patterns", "### Pattern Clusters", "### Recent Alerts", "### Daily Briefing"} {
		if strings.Contains(result, empty) {
			t.Errorf("should not contain empty category header: %s", empty)
		}
	}
}

func TestBuildSituationAwareness_LongValuesTruncated(t *testing.T) {
	params := &SectionParams{IsMinimal: false}
	mock := newMockBrainLister()

	longValue := strings.Repeat("x", 300)
	mock.addEntry("reflect.patterns.long", longValue, 0.8)

	pb := &PromptBuilder{brainService: mock}
	result := pb.buildSituationAwareness(context.Background(), params)

	// Value should be truncated to 200 chars with "...".
	if strings.Contains(result, longValue) {
		t.Error("long value should be truncated, not included in full")
	}
	if !strings.Contains(result, "...") {
		t.Error("truncated value should end with '...'")
	}
}

func TestBuildSituationAwareness_TimeContext(t *testing.T) {
	params := &SectionParams{IsMinimal: false, UserTimezone: "UTC"}
	mock := newMockBrainLister()
	mock.addEntry("sense.tasks.active", "Some task", 0.5)

	pb := &PromptBuilder{brainService: mock}
	result := pb.buildSituationAwareness(context.Background(), params)

	if !strings.Contains(result, "Time context:") {
		t.Error("expected time context line")
	}
}

func TestComputeTimeContext(t *testing.T) {
	result := computeTimeContext("UTC")
	if result == "" {
		t.Fatal("expected non-empty time context")
	}
	if !strings.HasPrefix(result, "Time context:") {
		t.Errorf("expected 'Time context:' prefix, got: %s", result)
	}

	// Should contain a day of week.
	days := []string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"}
	found := false
	for _, day := range days {
		if strings.Contains(result, day) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected a day of week in time context: %s", result)
	}

	// Should contain a period.
	periods := []string{"morning", "afternoon", "evening", "late night"}
	found = false
	for _, period := range periods {
		if strings.Contains(result, period) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected a time period in time context: %s", result)
	}
}

func TestComputeTimeContext_InvalidTimezone(t *testing.T) {
	// Should not panic with an invalid timezone.
	result := computeTimeContext("Invalid/Timezone")
	if result == "" {
		t.Fatal("expected non-empty time context even with invalid timezone")
	}
}

func TestComputeTimeContext_EmptyTimezone(t *testing.T) {
	result := computeTimeContext("")
	if result == "" {
		t.Fatal("expected non-empty time context with empty timezone")
	}
}

func TestBuildSituationAwareness_DailyBriefing(t *testing.T) {
	params := &SectionParams{IsMinimal: false}
	mock := newMockBrainLister()

	mock.addEntry("sense.briefing.latest", "Calendar: 2 meetings. Email: 5 unread. Weather: sunny.", 0.5)

	pb := &PromptBuilder{brainService: mock}
	result := pb.buildSituationAwareness(context.Background(), params)

	if !strings.Contains(result, "### Daily Briefing") {
		t.Error("expected Daily Briefing section")
	}
	if !strings.Contains(result, "Calendar: 2 meetings") {
		t.Error("expected briefing content")
	}
}

func TestRenderCategory(t *testing.T) {
	cat := &situationCategory{
		header:   "Test Category",
		priority: 1,
		entries: []*brain.Entry{
			{Key: "test.1", Value: "First item", Salience: 0.9},
			{Key: "test.2", Value: "Second item", Salience: 0.5},
		},
	}

	result := renderCategory(cat)
	if !strings.Contains(result, "### Test Category") {
		t.Error("expected header")
	}
	if !strings.Contains(result, "- First item") {
		t.Error("expected first item as bullet")
	}
	if !strings.Contains(result, "- Second item") {
		t.Error("expected second item as bullet")
	}
}

func TestRenderCategoryTruncated(t *testing.T) {
	cat := &situationCategory{
		header:   "Test Category",
		priority: 1,
		entries: []*brain.Entry{
			{Key: "test.1", Value: "First item", Salience: 0.9},
			{Key: "test.2", Value: "Second item", Salience: 0.5},
			{Key: "test.3", Value: "Third item", Salience: 0.3},
		},
	}

	result := renderCategoryTruncated(cat)
	if !strings.Contains(result, "### Test Category") {
		t.Error("expected header")
	}
	if !strings.Contains(result, "- First item") {
		t.Error("expected first item")
	}
	if strings.Contains(result, "Second item") {
		t.Error("should not contain second item in truncated render")
	}
	if !strings.Contains(result, "(2 more)") {
		t.Error("expected '(2 more)' count indicator")
	}
}

func TestRenderCategoryTruncated_SingleEntry(t *testing.T) {
	cat := &situationCategory{
		header:   "Test",
		priority: 1,
		entries: []*brain.Entry{
			{Key: "test.1", Value: "Only item", Salience: 0.9},
		},
	}

	result := renderCategoryTruncated(cat)
	if !strings.Contains(result, "- Only item") {
		t.Error("expected the single item")
	}
	if strings.Contains(result, "more)") {
		t.Error("should not show 'more' count for single entry")
	}
}

func TestRenderCategoryTruncated_Empty(t *testing.T) {
	cat := &situationCategory{
		header:   "Empty",
		priority: 1,
		entries:  nil,
	}
	result := renderCategoryTruncated(cat)
	if result != "" {
		t.Errorf("expected empty string for empty category, got: %s", result)
	}
}
