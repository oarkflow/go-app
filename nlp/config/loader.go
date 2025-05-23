package config

import (
	"encoding/json"
	"io/ioutil"
	"path/filepath"
	
	"gopkg.in/yaml.v3"
)

type Config struct {
	Grammar         string `yaml:"grammar.rules_file"`
	Summarization   string `yaml:"summarization.config_file"`
	QA              string `yaml:"qa.patterns_file"`
	Event           string `yaml:"event_patterns_file"`
	Measurement     string `yaml:"measurement_units.json"`
	Transliteration string `yaml:"transliteration_map.json"`
	Segmentation    string `yaml:"segmentation_rules.json"`
	Coref           string `yaml:"coref_rules.json"`
}

func Load(baseDir string) (*Config, error) {
	data, err := ioutil.ReadFile(filepath.Join(baseDir, "config.yaml"))
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// Generic JSON loader
func LoadJSON[T any](path string) (*T, error) {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg T
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
