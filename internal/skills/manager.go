package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"
)

// Manager orchestrates skill discovery, loading, and execution
type Manager struct {
	config      SkillsConfig
	discovery   *SkillDiscovery
	loader      *SkillLoader
	executor    *Executor
	integrator  *SkillIntegrator
	validator   *SkillValidator
	cache       *skillCache
	mu          sync.RWMutex
	initialized bool
}

// skillCache manages cached skills with TTL
type skillCache struct {
	skills []Skill
	expiry time.Time
	mu     sync.RWMutex
}

// NewManager creates a new skills manager
func NewManager(config SkillsConfig) *Manager {
	// Set defaults if not configured
	if len(config.SearchPaths) == 0 {
		config.SearchPaths = []string{
			"/usr/local/lib/conduit/skills",
			"./skills",
			"/opt/conduit/skills",
		}
	}

	if config.Execution.TimeoutSeconds == 0 {
		config.Execution.TimeoutSeconds = 30
	}

	if config.Cache.TTLSeconds == 0 {
		config.Cache.TTLSeconds = 3600 // 1 hour default
	}

	discovery := NewSkillDiscovery(config.SearchPaths)
	executor := NewExecutor(config.Execution)
	integrator := NewSkillIntegrator(executor)

	return &Manager{
		config:     config,
		discovery:  discovery,
		loader:     NewSkillLoader(),
		executor:   executor,
		integrator: integrator,
		validator:  NewSkillValidator(),
		cache: &skillCache{
			skills: []Skill{},
			expiry: time.Time{},
		},
	}
}

// Initialize discovers and loads all available skills
func (m *Manager) Initialize(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.config.Enabled {
		log.Println("Skills system is disabled")
		return nil
	}

	log.Println("Initializing skills system...")

	skills, err := m.discovery.DiscoverSkills(ctx)
	if err != nil {
		return fmt.Errorf("failed to discover skills: %w", err)
	}

	// Cache the discovered skills
	m.cache.mu.Lock()
	m.cache.skills = skills
	m.cache.expiry = time.Now().Add(time.Duration(m.config.Cache.TTLSeconds) * time.Second)
	m.cache.mu.Unlock()

	m.initialized = true

	log.Printf("Skills system initialized with %d skills", len(skills))
	return nil
}

// GetAvailableSkills returns all available skills, using cache if valid
func (m *Manager) GetAvailableSkills(ctx context.Context) ([]Skill, error) {
	// Check cache first if enabled
	if m.config.Cache.Enabled {
		m.cache.mu.RLock()
		if time.Now().Before(m.cache.expiry) && len(m.cache.skills) > 0 {
			skills := m.cache.skills
			m.cache.mu.RUnlock()
			return skills, nil
		}
		m.cache.mu.RUnlock()
	}

	// Cache miss or disabled, discover fresh skills
	skills, err := m.discovery.DiscoverSkills(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to discover skills: %w", err)
	}

	// Update cache
	if m.config.Cache.Enabled {
		m.cache.mu.Lock()
		m.cache.skills = skills
		m.cache.expiry = time.Now().Add(time.Duration(m.config.Cache.TTLSeconds) * time.Second)
		m.cache.mu.Unlock()
	}

	return skills, nil
}

// GetSkill retrieves a specific skill by name
func (m *Manager) GetSkill(ctx context.Context, name string) (*Skill, error) {
	skills, err := m.GetAvailableSkills(ctx)
	if err != nil {
		return nil, err
	}

	for _, skill := range skills {
		if skill.Name == name {
			return &skill, nil
		}
	}

	return nil, fmt.Errorf("skill not found: %s", name)
}

// ExecuteSkill executes a skill action with the given arguments
func (m *Manager) ExecuteSkill(ctx context.Context, skillName, action string, args map[string]interface{}) (*ExecutionResult, error) {
	if !m.config.Enabled {
		return &ExecutionResult{
			Success: false,
			Error:   "skills system is disabled",
		}, nil
	}

	// Get the skill
	skill, err := m.GetSkill(ctx, skillName)
	if err != nil {
		return &ExecutionResult{
			Success: false,
			Error:   err.Error(),
		}, nil
	}

	// Check if action is allowed
	if !m.isActionAllowed(skillName, action) {
		return &ExecutionResult{
			Success: false,
			Error:   fmt.Sprintf("action '%s' not allowed for skill '%s'", action, skillName),
		}, nil
	}

	// Execute the skill
	return m.executor.ExecuteSkill(ctx, *skill, action, args)
}

// GenerateTools creates tool instances from all available skills
func (m *Manager) GenerateTools(ctx context.Context) ([]SkillToolInterface, error) {
	if !m.config.Enabled {
		return []SkillToolInterface{}, nil
	}

	skills, err := m.GetAvailableSkills(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get skills: %w", err)
	}

	return m.integrator.GenerateToolsFromSkills(skills), nil
}

// BuildSystemPromptContext creates contextual information for agent prompts
func (m *Manager) BuildSystemPromptContext(ctx context.Context) (string, error) {
	if !m.config.Enabled {
		return "", nil
	}

	skills, err := m.GetAvailableSkills(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get skills: %w", err)
	}

	if len(skills) == 0 {
		return "", nil
	}

	return m.integrator.BuildSkillsContext(skills), nil
}

// ValidateSkillRequirements checks if all requirements for a skill are met
func (m *Manager) ValidateSkillRequirements(ctx context.Context, skillName string) error {
	skill, err := m.GetSkill(ctx, skillName)
	if err != nil {
		return err
	}

	return m.validator.ValidateRequirements(*skill)
}

// GetSkillStatus returns status information for all skills
func (m *Manager) GetSkillStatus(ctx context.Context) ([]SkillStatus, error) {
	skills, err := m.GetAvailableSkills(ctx)
	if err != nil {
		return nil, err
	}

	var statuses []SkillStatus

	for _, skill := range skills {
		status := SkillStatus{
			Name:        skill.Name,
			Description: skill.Description,
			Location:    skill.Location,
			Available:   true,
		}

		// Check requirements
		if err := m.validator.ValidateRequirements(skill); err != nil {
			status.Available = false
			status.Error = err.Error()
			status.MissingRequirements = m.validator.GetMissingRequirements(skill)
		}

		// Get available actions
		status.Actions = m.integrator.extractAvailableActions(skill)

		statuses = append(statuses, status)
	}

	return statuses, nil
}

// ReloadSkills forces a reload of skills from the filesystem
func (m *Manager) ReloadSkills(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Clear cache
	m.cache.mu.Lock()
	m.cache.skills = []Skill{}
	m.cache.expiry = time.Time{}
	m.cache.mu.Unlock()

	// Rediscover skills
	skills, err := m.discovery.DiscoverSkills(ctx)
	if err != nil {
		return fmt.Errorf("failed to reload skills: %w", err)
	}

	// Update cache
	if m.config.Cache.Enabled {
		m.cache.mu.Lock()
		m.cache.skills = skills
		m.cache.expiry = time.Now().Add(time.Duration(m.config.Cache.TTLSeconds) * time.Second)
		m.cache.mu.Unlock()
	}

	log.Printf("Reloaded %d skills", len(skills))
	return nil
}

// IsEnabled returns whether the skills system is enabled
func (m *Manager) IsEnabled() bool {
	return m.config.Enabled
}

// IsInitialized returns whether the skills system has been initialized
func (m *Manager) IsInitialized() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.initialized
}

// GetConfig returns the current skills configuration
func (m *Manager) GetConfig() SkillsConfig {
	return m.config
}

// isActionAllowed checks if an action is allowed for a given skill
func (m *Manager) isActionAllowed(skillName, action string) bool {
	// If no allowed actions configured, allow all
	allowedActions, exists := m.config.Execution.AllowedActions[skillName]
	if !exists {
		return true
	}

	// Check if action is in allowed list
	for _, allowed := range allowedActions {
		if allowed == action {
			return true
		}
	}

	return false
}

// SkillStatus represents the status of a skill
type SkillStatus struct {
	Name                string              `json:"name"`
	Description         string              `json:"description"`
	Location            string              `json:"location"`
	Available           bool                `json:"available"`
	Error               string              `json:"error,omitempty"`
	Actions             []string            `json:"actions"`
	MissingRequirements map[string][]string `json:"missing_requirements,omitempty"`
}

// MarshalJSON provides JSON serialization for the manager status
func (m *Manager) MarshalJSON() ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	status := struct {
		Enabled     bool   `json:"enabled"`
		Initialized bool   `json:"initialized"`
		SkillCount  int    `json:"skill_count"`
		CacheExpiry string `json:"cache_expiry,omitempty"`
	}{
		Enabled:     m.config.Enabled,
		Initialized: m.initialized,
	}

	m.cache.mu.RLock()
	status.SkillCount = len(m.cache.skills)
	if !m.cache.expiry.IsZero() {
		status.CacheExpiry = m.cache.expiry.Format(time.RFC3339)
	}
	m.cache.mu.RUnlock()

	return json.Marshal(status)
}
