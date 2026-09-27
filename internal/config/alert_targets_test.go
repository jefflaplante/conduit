package config

import "testing"

// conduit-31jg.73: alert_targets is validated but only alert_targets[0]
// (telegram chat_id) is used; anything else is flagged as reserved.
func TestUnroutedAlertTargets(t *testing.T) {
	tg := AlertTarget{Name: "tg", Type: "telegram", Config: map[string]string{"chat_id": "1"}}
	cases := []struct {
		name    string
		targets []AlertTarget
		want    bool
	}{
		{"none", nil, false},
		{"single telegram", []AlertTarget{tg}, false},
		{"two targets", []AlertTarget{tg, tg}, true},
		{"non-telegram", []AlertTarget{{Name: "w", Type: "webhook"}}, true},
		{"severity filter", []AlertTarget{{Name: "tg", Type: "telegram", Severity: []string{"critical"}}}, true},
	}
	for _, c := range cases {
		if got := unroutedAlertTargets(c.targets); got != c.want {
			t.Errorf("%s: unroutedAlertTargets = %v, want %v", c.name, got, c.want)
		}
	}
}
