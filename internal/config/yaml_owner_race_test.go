package config

import (
	"reflect"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"
)

// Marshal the shared config while its owner and baseline initialize, matching
// settings persistence and a first concurrent host decision. Run with -race.
func TestConfigYAMLConcurrentOwnerInitialization(t *testing.T) {
	for i := 0; i < 300; i++ {
		cfg := &Config{}
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			if _, err := yaml.Marshal(cfg); err != nil {
				t.Errorf("marshal: %v", err)
			}
		}()
		go func() { defer wg.Done(); <-start; _ = cfg.WebFetchPolicyOwner(); _ = cfg.WebFetchBaseline() }()
		close(start)
		wg.Wait()
	}
}

func TestConfigYAMLProjection(t *testing.T) {
	configType, projectionType := reflect.TypeOf(Config{}), reflect.TypeOf(configYAML{})
	var exported []reflect.StructField
	for i := 0; i < configType.NumField(); i++ {
		field := configType.Field(i)
		if field.IsExported() {
			exported = append(exported, field)
		}
	}
	if len(exported) != projectionType.NumField() {
		t.Fatalf("projection has %d fields; Config has %d exported", projectionType.NumField(), len(exported))
	}
	for i, field := range exported {
		projected := projectionType.Field(i)
		if projected.Name != field.Name || projected.Type != field.Type || projected.Tag.Get("yaml") != field.Tag.Get("yaml") {
			t.Errorf("field %d: projection=%v Config=%v", i, projected, field)
		}
	}
	original := &Config{
		Server:    ServerConfig{Host: "localhost", Port: 1234, Secret: "fake-secret"},
		LLM:       LLMConfig{Provider: "fake-provider", Model: "fake-model"},
		Inference: InferenceConfig{Backend: "external", Port: 1235, ExtraArgs: []string{"fake-arg"}},
		Users:     []UserConfig{{Name: "fake-user", Role: "parent", PIN: "fake-pin"}},
		Gateways:  GatewaysConfig{Telegram: TelegramConfig{Enabled: true, Token: "fake-token"}},
		Policies:  PoliciesConfig{Dir: "fake-policies"}, Approval: ApprovalConfig{ExpiryHours: 7},
		Skills: SkillsConfig{Dir: "fake-skills", AutoSecCheck: true}, Storage: StorageConfig{DBPath: "fake-db"},
		SecCheck: SecCheckConfig{Enabled: true}, Tools: ToolsConfig{WebFetch: WebFetchConfig{Enabled: true, URLPolicy: "denylist", URLDenylist: []string{"blocked.example"}}},
	}
	data, err := yaml.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Config
	if err := yaml.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	left, right := reflect.ValueOf(original).Elem(), reflect.ValueOf(&decoded).Elem()
	for _, field := range exported {
		if !reflect.DeepEqual(left.FieldByName(field.Name).Interface(), right.FieldByName(field.Name).Interface()) {
			t.Errorf("round-trip mismatch in %s: got %#v want %#v", field.Name, right.FieldByName(field.Name).Interface(), left.FieldByName(field.Name).Interface())
		}
	}
	zero, err := yaml.Marshal(&Config{})
	if err != nil {
		t.Fatal(err)
	}
	var mapping map[string]any
	if err := yaml.Unmarshal(zero, &mapping); err != nil {
		t.Fatal(err)
	}
	if _, present := mapping["tools"]; present {
		t.Fatal("empty tools must remain omitted")
	}
}
