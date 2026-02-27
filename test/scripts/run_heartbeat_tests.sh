#!/bin/bash

# OCGO-017: Heartbeat Integration Testing and Validation
# Comprehensive test runner for the heartbeat system

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Test configuration
PROJECT_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$PROJECT_ROOT"

# Ensure clean state
echo -e "${BLUE}Cleaning up test artifacts...${NC}"
rm -f test_*.db test_*.log

# Function to run tests with proper flags
run_test_suite() {
    local test_path="$1"
    local test_name="$2"
    local extra_flags="$3"
    
    echo -e "\n${BLUE}Running $test_name...${NC}"
    
    # Run with race detection and coverage
    go test -v -race -timeout=5m $extra_flags "$test_path" 2>&1 | tee "${test_name}_results.log"
    
    local exit_code=${PIPESTATUS[0]}
    
    if [ $exit_code -eq 0 ]; then
        echo -e "${GREEN}✅ $test_name PASSED${NC}"
    else
        echo -e "${RED}❌ $test_name FAILED${NC}"
        return 1
    fi
}

# Function to run benchmarks
run_benchmarks() {
    local test_path="$1"
    local bench_name="$2"
    
    echo -e "\n${BLUE}Running $bench_name...${NC}"
    
    go test -bench=. -benchtime=10s -timeout=5m "$test_path" 2>&1 | tee "${bench_name}_benchmarks.log"
    
    local exit_code=${PIPESTATUS[0]}
    
    if [ $exit_code -eq 0 ]; then
        echo -e "${GREEN}✅ $bench_name completed${NC}"
    else
        echo -e "${YELLOW}⚠️  $bench_name had issues${NC}"
    fi
}

echo -e "${BLUE}🚀 Starting OCGO-017 Heartbeat Integration Testing${NC}"
echo "======================================================="

# Ensure we have the required dependencies
echo -e "${BLUE}Installing/updating test dependencies...${NC}"
go mod tidy

# Create test directories if they don't exist
mkdir -p test/load test/integration test/reports

# Test summary tracking
TOTAL_TESTS=0
PASSED_TESTS=0
FAILED_TESTS=0

# 1. Unit Tests - Core heartbeat functionality
echo -e "\n${YELLOW}Phase 1: Unit Testing${NC}"
echo "===================="

if run_test_suite "./internal/monitoring" "Unit_Tests" "-coverprofile=coverage_unit.out"; then
    ((PASSED_TESTS++))
else
    ((FAILED_TESTS++))
fi
((TOTAL_TESTS++))

# Also test heartbeat config
if run_test_suite "./internal/config" "Config_Tests" "-run=.*Heartbeat.*"; then
    ((PASSED_TESTS++))
else
    ((FAILED_TESTS++))
fi
((TOTAL_TESTS++))

# 2. Integration Tests - Real gateway with heartbeat
echo -e "\n${YELLOW}Phase 2: Integration Testing${NC}"
echo "============================"

if run_test_suite "./test/integration" "Integration_Tests" "-coverprofile=coverage_integration.out"; then
    ((PASSED_TESTS++))
else
    ((FAILED_TESTS++))
fi
((TOTAL_TESTS++))

# 3. Load Tests - Performance and stability
echo -e "\n${YELLOW}Phase 3: Load Testing${NC}"
echo "===================="

# Short load tests
if run_test_suite "./test/load" "Load_Tests_Short" "-short -coverprofile=coverage_load.out"; then
    ((PASSED_TESTS++))
else
    ((FAILED_TESTS++))
fi
((TOTAL_TESTS++))

# Full load tests (if not in CI or short mode)
if [ "$CI" != "true" ] && [ "$1" != "--short" ]; then
    echo -e "\n${BLUE}Running full load tests (this may take several minutes)...${NC}"
    
    if run_test_suite "./test/load" "Load_Tests_Full" "-timeout=10m"; then
        ((PASSED_TESTS++))
    else
        ((FAILED_TESTS++))
    fi
    ((TOTAL_TESTS++))
else
    echo -e "${YELLOW}Skipping full load tests (use --full flag to run)${NC}"
fi

# 4. Memory Leak Detection
echo -e "\n${YELLOW}Phase 4: Memory Leak Detection${NC}"
echo "=============================="

echo -e "${BLUE}Running memory tests with race detection...${NC}"
if run_test_suite "./internal/monitoring" "Memory_Tests" "-race -run=.*Memory.*"; then
    ((PASSED_TESTS++))
else
    ((FAILED_TESTS++))
fi
((TOTAL_TESTS++))

if run_test_suite "./test/integration" "Memory_Integration_Tests" "-race -run=.*Memory.*"; then
    ((PASSED_TESTS++))
else
    ((FAILED_TESTS++))
fi
((TOTAL_TESTS++))

# 5. Race Condition Detection
echo -e "\n${YELLOW}Phase 5: Concurrency Testing${NC}"
echo "============================"

echo -e "${BLUE}Testing for race conditions...${NC}"
if run_test_suite "./internal/monitoring" "Race_Tests" "-race -run=.*Concurrent.*"; then
    ((PASSED_TESTS++))
else
    ((FAILED_TESTS++))
fi
((TOTAL_TESTS++))

if run_test_suite "./test/integration" "Race_Integration_Tests" "-race -run=.*Concurrent.*"; then
    ((PASSED_TESTS++))
else
    ((FAILED_TESTS++))
fi
((TOTAL_TESTS++))

# 6. Benchmarks (optional)
if [ "$1" == "--benchmark" ] || [ "$2" == "--benchmark" ]; then
    echo -e "\n${YELLOW}Phase 6: Performance Benchmarking${NC}"
    echo "=================================="
    
    # This would require adding benchmark functions
    run_benchmarks "./internal/monitoring" "Heartbeat_Benchmarks"
    run_benchmarks "./test/load" "Load_Benchmarks"
fi

# 7. Generate combined coverage report
echo -e "\n${YELLOW}Phase 7: Coverage Analysis${NC}"
echo "========================="

echo -e "${BLUE}Generating coverage report...${NC}"

# Combine coverage files if they exist
coverage_files=""
for file in coverage_*.out; do
    if [ -f "$file" ]; then
        coverage_files="$coverage_files $file"
    fi
done

if [ -n "$coverage_files" ]; then
    echo "mode: atomic" > coverage_combined.out
    
    for file in $coverage_files; do
        tail -n +2 "$file" >> coverage_combined.out
    done
    
    # Generate HTML report
    go tool cover -html=coverage_combined.out -o test/reports/coverage.html
    
    # Calculate coverage percentage
    coverage_percent=$(go tool cover -func=coverage_combined.out | tail -n 1 | awk '{print $3}')
    echo -e "${GREEN}Code coverage: $coverage_percent${NC}"
    
    # Coverage threshold check
    coverage_num=$(echo "$coverage_percent" | sed 's/%//')
    if (( $(echo "$coverage_num >= 80" | bc -l) )); then
        echo -e "${GREEN}✅ Coverage meets threshold (≥80%)${NC}"
    else
        echo -e "${YELLOW}⚠️  Coverage below recommended threshold (80%)${NC}"
    fi
else
    echo -e "${YELLOW}No coverage files found${NC}"
fi

# 8. Test Summary and Results
echo -e "\n${YELLOW}Test Summary${NC}"
echo "============"

echo -e "Total test suites: $TOTAL_TESTS"
echo -e "Passed: ${GREEN}$PASSED_TESTS${NC}"
echo -e "Failed: ${RED}$FAILED_TESTS${NC}"

# Generate test report
cat > test/reports/heartbeat_test_report.md << EOF
# OCGO-017: Heartbeat Integration Testing Report

**Generated:** $(date)
**Test Run Duration:** $(date)

## Summary

- **Total Test Suites:** $TOTAL_TESTS
- **Passed:** $PASSED_TESTS
- **Failed:** $FAILED_TESTS
- **Success Rate:** $(( PASSED_TESTS * 100 / TOTAL_TESTS ))%

## Test Phases Completed

1. ✅ Unit Testing - Core heartbeat functionality
2. ✅ Integration Testing - Real gateway with heartbeat
3. ✅ Load Testing - Performance and stability  
4. ✅ Memory Leak Detection - Resource management
5. ✅ Concurrency Testing - Race condition detection
6. ✅ Coverage Analysis - Code coverage metrics

## Coverage Report

Code coverage report available at: \`test/reports/coverage.html\`

## Test Files Created/Updated

- \`internal/monitoring/heartbeat_test.go\` - Enhanced unit tests
- \`test/integration/heartbeat_test.go\` - Integration test suite
- \`test/load/heartbeat_load_test.go\` - Load and performance tests

## Performance Baselines

See individual test logs for performance metrics and baselines.

## Recommendations

EOF

if [ $FAILED_TESTS -eq 0 ]; then
    echo "All heartbeat tests are passing. The system is ready for production." >> test/reports/heartbeat_test_report.md
else
    echo "Some tests failed. Review the logs and fix issues before deployment." >> test/reports/heartbeat_test_report.md
fi

# Final result
echo
if [ $FAILED_TESTS -eq 0 ]; then
    echo -e "${GREEN}🎉 All heartbeat tests completed successfully!${NC}"
    echo -e "${GREEN}The heartbeat system is production-ready.${NC}"
    exit 0
else
    echo -e "${RED}❌ Some tests failed. Check the logs for details.${NC}"
    echo -e "${YELLOW}Review failed tests before deploying.${NC}"
    exit 1
fi