package model

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigPartialAndInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"alert":{"cpu_warn_percent":70,"zombie_warn_count":0},"engine":{"interval":"25ms"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Alert.CPUWarnPercent != 70 || cfg.Alert.ZombieWarnCount != 0 || cfg.Alert.MemWarnMB != 2048 {
		t.Fatalf("partial config did not preserve defaults: %+v", cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{`null`, `[]`, `{"engine":{"typo":1}}`, `{} {}`, `{"engine":{"interval":"bad"}}`} {
		if err := os.WriteFile(path, []byte(input), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(path); err == nil {
			t.Errorf("accepted %s", input)
		}
	}
}
