#!/bin/bash

# OCGO-022: Heartbeat Loop Testing and Validation - Test Runner
# This script runs the complete OCGO-022 test suite and generates a validation report

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Test configuration
PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TEST_WORKSPACE="/tmp/ocgo-022-validation-$(date +%s)"
RESULTS_DIR="${TEST_WORKSPACE}/results"

echo -e "${BLUE}🚀 OCGO-022: Heartbeat Loop Testing and Validation${NC}"
echo -e "${BLUE}==================================================${NC}"
echo "Project Root: $PROJECT_ROOT"
echo "Test Workspace: $TEST_WORKSPACE"
echo "Results Directory: $RESULTS_DIR"
echo

# Create test workspace
mkdir -p "$TEST_WORKSPACE" "$RESULTS_DIR"

# Initialize test counters
TOTAL_TESTS=0
PASSED_TESTS=0
FAILED_TESTS=0
SKIPPED_TESTS=0

# Function to run a test and track results
run_test() {
    local test_name="$1"
    local test_command="$2"
    local description="$3"
    local result_file="$RESULTS_DIR/${test_name}.log"
    
    echo -e "${YELLOW}📋 Running: $test_name${NC}"
    echo "   $description"
    
    TOTAL_TESTS=$((TOTAL_TESTS + 1))
    
    if eval "$test_command" > "$result_file" 2>&1; then
        echo -e "   ${GREEN}✅ PASSED${NC}"
        PASSED_TESTS=$((PASSED_TESTS + 1))
        return 0
    else
        echo -e "   ${RED}❌ FAILED${NC}"
        echo "   Log: $result_file"
        FAILED_TESTS=$((FAILED_TESTS + 1))
        return 1
    fi
}

# Function to run optional test (don't fail overall if skipped)
run_optional_test() {
    local test_name="$1"
    local test_command="$2"
    local description="$3"
    local skip_reason="$4"
    
    if [ -n "$skip_reason" ]; then
        echo -e "${YELLOW}📋 Skipping: $test_name${NC}"
        echo "   $description"
        echo -e "   ${YELLOW}⏭️  SKIPPED: $skip_reason${NC}"
        SKIPPED_TESTS=$((SKIPPED_TESTS + 1))
        return 0
    else
        run_test "$test_name" "$test_command" "$description"
    fi
}

# Change to project directory
cd "$PROJECT_ROOT"

echo -e "${BLUE}📦 Preparing Test Environment${NC}"
echo "=================================================="

# Check if Go is available
if ! command -v go &> /dev/null; then
    echo -e "${RED}❌ Go compiler not found. Cannot run tests.${NC}"
    echo "Please install Go or run tests in a Go-enabled environment."
    exit 1
fi

# Check Go version
go_version=$(go version | cut -d' ' -f3)
echo "Go Version: $go_version"

# Build project to ensure it compiles
echo "Building project..."
if go build -o /tmp/conduit ./cmd/conduit > "$RESULTS_DIR/build.log" 2>&1; then
    echo -e "${GREEN}✅ Build successful${NC}"
else
    echo -e "${RED}❌ Build failed${NC}"
    echo "Build log: $RESULTS_DIR/build.log"
    exit 1
fi

echo

echo -e "${BLUE}🧪 Running Unit Tests${NC}"
echo "=================================================="

# Unit tests for alert queue processing
run_test "alert_queue_unit_tests" \
    "go test ./internal/heartbeat -run TestAlertQueue -v" \
    "Alert queue processing and severity routing unit tests"

run_test "heartbeat_queue_comprehensive" \
    "go test ./internal/heartbeat -run TestAlertQueueSeverityRouting -v" \
    "Comprehensive alert queue severity routing tests"

run_test "heartbeat_routing_tests" \
    "go test ./internal/heartbeat -run TestAlertSeverityRouter -v" \
    "Alert routing logic and quiet hours tests"

run_test "heartbeat_task_tests" \
    "go test ./internal/heartbeat -run TestTaskInterpreter -v" \
    "HEARTBEAT.md task parsing and interpretation tests"

echo

echo -e "${BLUE}🔗 Running Integration Tests${NC}"
echo "=================================================="

# Create test HEARTBEAT.md file
cat > "$TEST_WORKSPACE/HEARTBEAT.md" << 'EOF'
# HEARTBEAT.md - Integration Test

## Check Alert Queue
- Check if any alerts are pending in the queue
- If there are critical alerts, deliver immediately regardless of time
- If there are warning alerts during awake hours (7 AM - 11 PM PT), deliver them
- If there are info alerts, batch them and deliver during next awake period

## System Health Check
- Check system metrics and resource usage
- Report any anomalies or concerning trends

## Integration Test Task
- Validate that heartbeat loop is functioning correctly
- Test timezone-aware processing
EOF

# Integration tests
run_test "heartbeat_integration_basic" \
    "go test ./test/integration -run TestHeartbeatIntegration -v" \
    "Basic heartbeat system integration tests"

run_test "heartbeat_loop_comprehensive" \
    "go test ./test/integration -run TestHeartbeatLoopComprehensive -v -args $TEST_WORKSPACE" \
    "Comprehensive heartbeat loop testing with real files"

run_test "heartbeat_timezone_awareness" \
    "go test ./test/integration -run TestHeartbeatTimezoneAwareness -v" \
    "Timezone-aware quiet hours behavior testing"

run_test "heartbeat_error_scenarios" \
    "go test ./test/integration -run TestHeartbeatErrorScenarios -v" \
    "Error scenario testing: corruption, delivery failures"

run_test "heartbeat_cron_integration" \
    "go test ./test/integration -run TestHeartbeatCronIntegration -v" \
    "Cron job integration and lifecycle testing"

echo

echo -e "${BLUE}⚡ Running Performance Tests${NC}"
echo "=================================================="

# Performance validation
run_test "heartbeat_performance_validation" \
    "go run test/scripts/heartbeat_performance_validation.go $TEST_WORKSPACE" \
    "Performance overhead validation (<5% requirement)"

run_optional_test "heartbeat_load_tests" \
    "go test ./test/load -run TestHeartbeatPerformance -v" \
    "Load testing and performance benchmarks" \
    "$([ -z "$RUN_LOAD_TESTS" ] && echo "Set RUN_LOAD_TESTS=1 to enable")"

run_optional_test "heartbeat_stress_tests" \
    "go test ./test/load -run TestHeartbeatStressTest -v" \
    "Stress testing under high load" \
    "$([ -z "$RUN_STRESS_TESTS" ] && echo "Set RUN_STRESS_TESTS=1 to enable")"

echo

echo -e "${BLUE}🔍 Running Error Injection Tests${NC}"
echo "=================================================="

# Create corrupted test files
echo "Creating corrupted test files..."
echo '{"alerts": [{"id": "broken", "invalid_json"}' > "$TEST_WORKSPACE/corrupted_alerts.json"
echo '# Corrupted HEARTBEAT.md
## Task 1
- Valid instruction
```bash
echo "unclosed code block' > "$TEST_WORKSPACE/HEARTBEAT_CORRUPTED.md"

run_test "corrupted_file_recovery" \
    "go test ./internal/heartbeat -run TestSharedAlertQueue_CorruptedFileRecovery -v" \
    "Corrupted file recovery and error handling"

run_test "timeout_handling" \
    "go test ./test/integration -run TestTimeoutScenarios -v" \
    "Task timeout handling and recovery"

run_test "resource_exhaustion" \
    "go test ./test/integration -run TestResourceExhaustion -v" \
    "Resource exhaustion and system stability"

echo

echo -e "${BLUE}📊 Generating Test Report${NC}"
echo "=================================================="

# Generate comprehensive test report
REPORT_FILE="$RESULTS_DIR/OCGO-022-validation-report.md"

cat > "$REPORT_FILE" << EOF
# OCGO-022 Validation Report

**Generated:** $(date)  
**Project:** Conduit Go Gateway  
**Test Suite:** Heartbeat Loop Testing and Validation  

## Summary

| Metric | Count |
|--------|-------|
| Total Tests | $TOTAL_TESTS |
| Passed | $PASSED_TESTS |
| Failed | $FAILED_TESTS |
| Skipped | $SKIPPED_TESTS |
| Success Rate | $(( (PASSED_TESTS * 100) / (TOTAL_TESTS - SKIPPED_TESTS) ))% |

## Test Results

### ✅ Success Criteria Validation

EOF

# Add detailed results
for result_file in "$RESULTS_DIR"/*.log; do
    if [ -f "$result_file" ]; then
        test_name=$(basename "$result_file" .log)
        if grep -q "PASS\|✅" "$result_file" 2>/dev/null; then
            echo "- ✅ **$test_name**: PASSED" >> "$REPORT_FILE"
        elif grep -q "FAIL\|❌" "$result_file" 2>/dev/null; then
            echo "- ❌ **$test_name**: FAILED" >> "$REPORT_FILE"
        else
            echo "- ⚠️ **$test_name**: UNKNOWN" >> "$REPORT_FILE"
        fi
    fi
done

cat >> "$REPORT_FILE" << EOF

## Performance Analysis

EOF

# Include performance results if available
if [ -f "$TEST_WORKSPACE/heartbeat_performance_results.json" ]; then
    echo "Performance validation results:" >> "$REPORT_FILE"
    echo '```json' >> "$REPORT_FILE"
    cat "$TEST_WORKSPACE/heartbeat_performance_results.json" >> "$REPORT_FILE"
    echo '```' >> "$REPORT_FILE"
fi

cat >> "$REPORT_FILE" << EOF

## Test Environment

- **Go Version:** $go_version
- **Test Workspace:** $TEST_WORKSPACE
- **Project Root:** $PROJECT_ROOT
- **Test Duration:** $(date)

## Files Generated

EOF

# List all result files
ls -la "$RESULTS_DIR" | tail -n +2 | awk '{print "- " $9 " (" $5 " bytes)"}' >> "$REPORT_FILE"

echo "Test report generated: $REPORT_FILE"

echo

echo -e "${BLUE}📋 Final Results${NC}"
echo "=================================================="
echo -e "Total Tests:    ${BLUE}$TOTAL_TESTS${NC}"
echo -e "Passed:         ${GREEN}$PASSED_TESTS${NC}"
echo -e "Failed:         ${RED}$FAILED_TESTS${NC}"
echo -e "Skipped:        ${YELLOW}$SKIPPED_TESTS${NC}"

if [ $FAILED_TESTS -eq 0 ]; then
    echo
    echo -e "${GREEN}🎉 OCGO-022 VALIDATION: SUCCESS${NC}"
    echo -e "${GREEN}All critical tests passed. Heartbeat loop is ready for production.${NC}"
    echo
    echo "📁 Test artifacts saved to: $RESULTS_DIR"
    echo "📊 Detailed report: $REPORT_FILE"
    exit 0
else
    echo
    echo -e "${RED}❌ OCGO-022 VALIDATION: FAILED${NC}"
    echo -e "${RED}$FAILED_TESTS test(s) failed. Review logs before proceeding.${NC}"
    echo
    echo "📁 Test artifacts saved to: $RESULTS_DIR"
    echo "📊 Detailed report: $REPORT_FILE"
    echo
    echo "Failed test logs:"
    find "$RESULTS_DIR" -name "*.log" -exec grep -l "FAIL\|❌" {} \; | while read log_file; do
        echo "  - $log_file"
    done
    exit 1
fi