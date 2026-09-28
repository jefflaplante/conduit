package maintenance

import (
	"context"
	"database/sql"
	"io"
	"log"
	"os"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestSessionCleanupTask(t *testing.T) {
	// Create in-memory SQLite database
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer db.Close()

	// Create test tables
	setupTestTables(t, db)

	// Insert test data
	insertTestData(t, db)

	// Create cleanup task
	config := SessionConfig{
		RetentionDays:        30,
		CleanupEnabled:       true,
		SummarizeOld:         true,
		SummaryRetentionDays: 365,
	}

	task := NewSessionCleanupTask(db, config, log.New(os.Stdout, "[Test] ", log.LstdFlags))

	// Run cleanup
	ctx := context.Background()
	result := task.Execute(ctx)

	// Verify result
	if !result.Success {
		t.Errorf("Cleanup task failed: %s", result.Message)
		if result.Error != nil {
			t.Errorf("Error: %v", result.Error)
		}
	}

	// Verify data was cleaned up
	verifyCleanup(t, db)
}

func TestDatabaseMaintenanceTask(t *testing.T) {
	// Create temporary database file
	dbFile, err := os.CreateTemp("", "test_db_*.sqlite")
	if err != nil {
		t.Fatalf("Failed to create temp database: %v", err)
	}
	defer os.Remove(dbFile.Name())
	defer dbFile.Close()

	// Connect to the database
	db, err := sql.Open("sqlite", dbFile.Name())
	if err != nil {
		t.Fatalf("Failed to connect to test database: %v", err)
	}
	defer db.Close()

	// Create test tables and data
	setupTestTables(t, db)
	insertTestData(t, db)

	// Create maintenance task
	config := DatabaseConfig{
		VacuumEnabled:      true,
		VacuumThreshold:    0,     // Always vacuum for testing
		BackupBeforeVacuum: false, // Skip backup for testing
		OptimizeIndexes:    true,
	}

	task := NewDatabaseMaintenanceTask(db, dbFile.Name(), config, log.New(os.Stdout, "[Test] ", log.LstdFlags))

	// Run maintenance
	ctx := context.Background()
	result := task.Execute(ctx)

	// Verify result
	if !result.Success {
		t.Errorf("Database maintenance task failed: %s", result.Message)
		if result.Error != nil {
			t.Errorf("Error: %v", result.Error)
		}
	}
}

func TestScheduler(t *testing.T) {
	// Create in-memory SQLite database
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer db.Close()

	config := DefaultConfig()

	// Create scheduler
	scheduler := NewScheduler(db, config, log.New(os.Stdout, "[Test] ", log.LstdFlags))

	// Create a simple test task
	testTask := &TestTask{name: "test_task"}
	err = scheduler.RegisterTask(testTask)
	if err != nil {
		t.Fatalf("Failed to register test task: %v", err)
	}

	// Test task registration
	status := scheduler.GetStatus()
	if len(status) != 1 {
		t.Errorf("Expected 1 task, got %d", len(status))
	}

	if _, exists := status["test_task"]; !exists {
		t.Error("Test task not found in status")
	}

	// Test running a single task
	ctx := context.Background()
	err = scheduler.RunTask(ctx, "test_task")
	if err != nil {
		t.Errorf("Failed to run test task: %v", err)
	}

	// Verify task was executed
	if !testTask.executed {
		t.Error("Test task was not executed")
	}
}

// conduit-3kgo: manual runs used to be skipped outside 02:00-06:00 UTC.
// RunNow and RunTask now always execute every task, whatever the hour.
func TestScheduler_ManualRunsAlwaysExecute(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	s := NewScheduler(db, DefaultConfig(), log.New(io.Discard, "", 0))
	a, b := &TestTask{name: "a"}, &TestTask{name: "b"}
	for _, task := range []*TestTask{a, b} {
		if err := s.RegisterTask(task); err != nil {
			t.Fatal(err)
		}
	}

	if err := s.RunNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !a.executed || !b.executed {
		t.Fatalf("RunNow skipped tasks: a=%v b=%v", a.executed, b.executed)
	}
	for name, st := range s.GetStatus() {
		if st.LastRun.IsZero() || !st.LastResult.Success {
			t.Errorf("task %s status not recorded: %+v", name, st)
		}
	}

	a.executed = false
	if err := s.RunTask(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	if !a.executed {
		t.Fatal("RunTask skipped the task")
	}
	if err := s.RunTask(context.Background(), "missing"); err == nil {
		t.Fatal("RunTask of an unknown task should fail")
	}
}

// Helper functions

func setupTestTables(t *testing.T, db *sql.DB) {
	queries := []string{
		`CREATE TABLE sessions (
			key TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			channel_id TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			message_count INTEGER DEFAULT 0,
			context TEXT DEFAULT '{}'
		)`,
		`CREATE TABLE messages (
			id TEXT PRIMARY KEY,
			session_key TEXT NOT NULL,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
			metadata TEXT DEFAULT '{}',
			FOREIGN KEY (session_key) REFERENCES sessions (key)
		)`,
		`CREATE INDEX idx_sessions_updated_at ON sessions (updated_at)`,
		`CREATE INDEX idx_messages_timestamp ON messages (timestamp)`,
	}

	for _, query := range queries {
		_, err := db.Exec(query)
		if err != nil {
			t.Fatalf("Failed to create test table: %v", err)
		}
	}
}

func insertTestData(t *testing.T, db *sql.DB) {
	// Insert old sessions (should be cleaned up)
	oldTime := time.Now().AddDate(0, 0, -45).Format(time.RFC3339)

	// Insert current sessions (should be kept)
	currentTime := time.Now().AddDate(0, 0, -5).Format(time.RFC3339)

	// Old sessions
	_, err := db.Exec(`
		INSERT INTO sessions (key, user_id, channel_id, created_at, updated_at) VALUES 
		('old_session_1', 'user1', 'channel1', ?, ?),
		('old_session_2', 'user2', 'channel1', ?, ?)
	`, oldTime, oldTime, oldTime, oldTime)
	if err != nil {
		t.Fatalf("Failed to insert old sessions: %v", err)
	}

	// Current sessions
	_, err = db.Exec(`
		INSERT INTO sessions (key, user_id, channel_id, created_at, updated_at) VALUES 
		('current_session_1', 'user1', 'channel1', ?, ?),
		('current_session_2', 'user2', 'channel1', ?, ?)
	`, currentTime, currentTime, currentTime, currentTime)
	if err != nil {
		t.Fatalf("Failed to insert current sessions: %v", err)
	}

	// Insert messages for all sessions
	_, err = db.Exec(`
		INSERT INTO messages (id, session_key, role, content, timestamp) VALUES 
		('msg1', 'old_session_1', 'user', 'Old message 1', ?),
		('msg2', 'old_session_1', 'assistant', 'Old response 1', ?),
		('msg3', 'old_session_2', 'user', 'Old message 2', ?),
		('msg4', 'current_session_1', 'user', 'Current message 1', ?),
		('msg5', 'current_session_2', 'user', 'Current message 2', ?)
	`, oldTime, oldTime, oldTime, currentTime, currentTime)
	if err != nil {
		t.Fatalf("Failed to insert messages: %v", err)
	}
}

func verifyCleanup(t *testing.T, db *sql.DB) {
	// Count remaining sessions
	var sessionCount int
	err := db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&sessionCount)
	if err != nil {
		t.Fatalf("Failed to count sessions: %v", err)
	}

	// Should have 2 current sessions remaining
	if sessionCount != 2 {
		t.Errorf("Expected 2 sessions after cleanup, got %d", sessionCount)
	}

	// Count remaining messages
	var messageCount int
	err = db.QueryRow("SELECT COUNT(*) FROM messages").Scan(&messageCount)
	if err != nil {
		t.Fatalf("Failed to count messages: %v", err)
	}

	// Should have 2 current messages remaining
	if messageCount != 2 {
		t.Errorf("Expected 2 messages after cleanup, got %d", messageCount)
	}

	// Check if session summaries were created
	var summaryCount int
	err = db.QueryRow("SELECT COUNT(*) FROM session_summaries").Scan(&summaryCount)
	if err == nil {
		// Summaries table exists, should have 2 summaries for the old sessions
		if summaryCount != 2 {
			t.Errorf("Expected 2 session summaries, got %d", summaryCount)
		}
	}
}

// TestTask is a simple task implementation for testing
type TestTask struct {
	name     string
	executed bool
}

func (t *TestTask) Name() string {
	return t.name
}

func (t *TestTask) Description() string {
	return "Test task for unit testing"
}

func (t *TestTask) Execute(ctx context.Context) TaskResult {
	t.executed = true
	return TaskResult{
		Success: true,
		Message: "Test task executed successfully",
	}
}

func (t *TestTask) ShouldRun() bool {
	return true
}

func (t *TestTask) NextRun() time.Time {
	return time.Now().Add(time.Hour)
}

func (t *TestTask) IsDestructive() bool {
	return false
}
